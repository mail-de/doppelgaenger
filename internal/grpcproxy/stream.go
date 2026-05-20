package grpcproxy

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type grpcStreamResult struct {
	Started       bool
	Complete      bool
	Selected      string
	MessageCount  int
	MessageHash   string
	Header        metadata.MD
	Trailer       metadata.MD
	StatusCode    codes.Code
	StatusMessage string
	Duration      time.Duration
	Err           string
	SkipReason    string
}

type primaryReceiveResult struct {
	result grpcStreamResult
	err    error
}

type shadowSink interface {
	Offer(rawMessage)
	Close()
}

func forwardPrimaryStream(
	server grpc.ServerStream,
	primary grpc.ClientStream,
	cancel context.CancelFunc,
	shadow shadowSink,
) (grpcStreamResult, error) {
	started := time.Now()
	sendDone := make(chan error, 1)
	recvDone := make(chan primaryReceiveResult, 1)

	go func() {
		sendDone <- forwardClientToPrimary(server, primary, shadow)
	}()

	go func() {
		result, err := forwardPrimaryToClient(server, primary)
		recvDone <- primaryReceiveResult{result: result, err: err}
	}()

	result, err := waitForPrimaryForwarding(sendDone, recvDone, cancel)
	result.Duration = time.Since(started)

	return result, err
}

func waitForPrimaryForwarding(
	sendDone <-chan error,
	recvDone <-chan primaryReceiveResult,
	cancel context.CancelFunc,
) (grpcStreamResult, error) {
	for sendDone != nil || recvDone != nil {
		select {
		case err := <-sendDone:
			sendDone = nil

			if err != nil {
				cancel()

				return grpcStreamResult{
					StatusCode:    status.Code(err),
					StatusMessage: statusMessage(err),
					Err:           err.Error(),
				}, proxyStreamError("forward request to primary", err)
			}
		case received := <-recvDone:
			cancel()

			return received.result, received.err
		}
	}

	return grpcStreamResult{Complete: true, MessageHash: emptyMessageHash(), StatusCode: codes.OK}, nil
}

func forwardClientToPrimary(server grpc.ServerStream, primary grpc.ClientStream, shadow shadowSink) error {
	if shadow != nil {
		defer shadow.Close()
	}

	for {
		var msg rawMessage
		if err := server.RecvMsg(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return primary.CloseSend()
			}

			return err
		}

		if err := primary.SendMsg(cloneRawMessage(msg)); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}

			return err
		}

		if shadow != nil {
			shadow.Offer(msg)
		}
	}
}

func forwardPrimaryToClient(server grpc.ServerStream, primary grpc.ClientStream) (grpcStreamResult, error) {
	hasher := newMessageHashRecorder()
	result := grpcStreamResult{Started: true, MessageHash: hasher.Sum()}

	header, err := receivePrimaryHeader(server, primary)
	if err != nil {
		result.Trailer = cloneMetadata(primary.Trailer())
		result.setError(err)
		result.Complete = isFinalStatusError(err)

		return result, err
	}

	result.Header = header

	for {
		var msg rawMessage
		if err := primary.RecvMsg(&msg); err != nil {
			result.Trailer = cloneMetadata(primary.Trailer())
			server.SetTrailer(result.Trailer)

			if errors.Is(err, io.EOF) {
				result.StatusCode = codes.OK
				result.StatusMessage = ""
				result.Complete = true
				result.MessageHash = hasher.Sum()

				return result, nil
			}

			primaryErr := primaryStreamError("receive primary response", err)
			result.setError(primaryErr)
			result.Complete = isFinalStatusError(err)
			result.MessageHash = hasher.Sum()

			return result, primaryErr
		}

		if err := server.SendMsg(cloneRawMessage(msg)); err != nil {
			result.setError(err)
			result.MessageHash = hasher.Sum()

			return result, proxyStreamError("send response to client", err)
		}

		hasher.Add(msg)

		result.MessageCount++
		result.MessageHash = hasher.Sum()
	}
}

func receivePrimaryHeader(server grpc.ServerStream, primary grpc.ClientStream) (metadata.MD, error) {
	header, err := primary.Header()
	if err != nil {
		server.SetTrailer(cloneMetadata(primary.Trailer()))

		return nil, primaryStreamError("receive primary headers", err)
	}

	if len(header) == 0 {
		return nil, nil
	}

	cloned := cloneMetadata(header)
	if err := server.SendHeader(cloned); err != nil {
		return cloned, proxyStreamError("send primary headers to client", err)
	}

	return cloned, nil
}

func cloneRawMessage(msg rawMessage) rawMessage {
	return append(rawMessage(nil), msg...)
}

type messageHashRecorder struct {
	hash hash.Hash
}

func newMessageHashRecorder() *messageHashRecorder {
	return &messageHashRecorder{hash: sha256.New()}
}

func (r *messageHashRecorder) Add(msg rawMessage) {
	if r == nil || r.hash == nil {
		return
	}

	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(msg)))

	_, _ = r.hash.Write(length[:])
	_, _ = r.hash.Write(msg)
}

func (r *messageHashRecorder) Sum() string {
	if r == nil || r.hash == nil {
		return emptyMessageHash()
	}

	return hex.EncodeToString(r.hash.Sum(nil))
}

func emptyMessageHash() string {
	sum := sha256.Sum256(nil)

	return hex.EncodeToString(sum[:])
}

func (r *grpcStreamResult) setError(err error) {
	if r == nil || err == nil {
		return
	}

	r.StatusCode = status.Code(err)
	r.StatusMessage = statusMessage(err)
	r.Err = err.Error()
}

func statusMessage(err error) string {
	if err == nil {
		return ""
	}

	if st, ok := status.FromError(err); ok {
		return st.Message()
	}

	return err.Error()
}

func isFinalStatusError(err error) bool {
	if err == nil {
		return false
	}

	_, ok := status.FromError(err)

	return ok
}

func primaryStreamError(operation string, err error) error {
	if err == nil {
		return nil
	}

	if _, ok := status.FromError(err); ok {
		return err
	}

	return status.Errorf(codes.Unavailable, "%s: %v", operation, err)
}

func proxyStreamError(operation string, err error) error {
	if err == nil {
		return nil
	}

	if _, ok := status.FromError(err); ok {
		return err
	}

	return status.Error(codes.Internal, fmt.Sprintf("%s: %v", operation, err))
}
