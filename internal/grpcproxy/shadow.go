package grpcproxy

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"doppelgaenger/internal/config"
	"doppelgaenger/internal/observability"
)

const defaultShadowQueueSize = 128

const (
	shadowSkipReasonRateLimited       = "rate_limited"
	shadowSkipReasonQueueFull         = "queue_full"
	shadowSkipReasonTimeout           = "timeout"
	shadowSkipReasonNotStarted        = "shadow_not_started"
	shadowSkipReasonTargetUnavailable = "target_unavailable"
)

type shadowForwarder struct {
	queue  chan rawMessage
	cancel context.CancelFunc

	mu          sync.Mutex
	queueClosed bool
	result      grpcStreamResult
	startedAt   time.Time

	observability *observability.Observability
	span          trace.Span
	spanCtx       context.Context

	done     chan struct{}
	doneOnce sync.Once
	spanOnce sync.Once
}

func newCompletedShadowForwarder(result grpcStreamResult) *shadowForwarder {
	forwarder := &shadowForwarder{done: make(chan struct{})}
	forwarder.result = result
	close(forwarder.done)

	return forwarder
}

func newShadowForwarder(
	parent context.Context,
	target *Target,
	fullMethod string,
	overlay map[string]string,
	cfg config.Config,
	callOptions []grpc.CallOption,
	obs *observability.Observability,
) *shadowForwarder {
	baseCtx := context.WithoutCancel(parent)
	spanCtx, span := startShadowBackendSpan(baseCtx, obs, fullMethod, target)
	shadowCtx, cancel := context.WithTimeout(spanCtx, shadowTimeout(cfg))
	shadowCtx = outgoingContext(shadowCtx, overlay, obs)

	forwarder := &shadowForwarder{
		queue:  make(chan rawMessage, shadowQueueSize(cfg)),
		cancel: cancel,
		result: grpcStreamResult{
			Selected: target.Name,
		},
		startedAt:     time.Now(),
		observability: obs,
		span:          span,
		spanCtx:       spanCtx,
		done:          make(chan struct{}),
	}

	go forwarder.run(shadowCtx, target, fullMethod, callOptions)

	return forwarder
}

func shadowTimeout(cfg config.Config) time.Duration {
	if cfg.GRPCShadowTimeout > 0 {
		return cfg.GRPCShadowTimeout
	}

	return 500 * time.Millisecond
}

func shadowQueueSize(cfg config.Config) int {
	if cfg.GRPCShadowQueueSize > 0 {
		return cfg.GRPCShadowQueueSize
	}

	return defaultShadowQueueSize
}

func (s *shadowForwarder) Offer(msg rawMessage) {
	if s == nil || s.queue == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.queueClosed {
		return
	}

	select {
	case s.queue <- cloneRawMessage(msg):
	default:
		s.markResultLocked(shadowSkipReasonQueueFull, shadowSkipReasonQueueFull)
		s.closeQueueLocked()
	}
}

func (s *shadowForwarder) Close() {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.closeQueueLocked()
}

func (s *shadowForwarder) Wait(timeout time.Duration) grpcStreamResult {
	if s == nil {
		return grpcStreamResult{}
	}

	if timeout <= 0 {
		timeout = shadowTimeout(config.Config{})
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-s.done:
		result := s.snapshot()
		s.finishSpan(result)

		return result
	case <-timer.C:
		s.markResult(shadowSkipReasonTimeout, context.DeadlineExceeded.Error())
		s.markDuration()

		if s.cancel != nil {
			s.cancel()
		}

		result := s.snapshot()
		s.finishSpan(result)

		return result
	}
}

func (s *shadowForwarder) run(ctx context.Context, target *Target, fullMethod string, callOptions []grpc.CallOption) {
	started := time.Now()
	defer func() {
		s.markDurationSince(started)
		s.complete()
	}()

	shadow, err := target.Conn.NewStream(ctx, genericStreamDesc(), fullMethod, callOptions...)
	if err != nil {
		s.failStart(err)

		return
	}

	s.markStarted()

	sendDone := make(chan error, 1)
	recvDone := make(chan grpcStreamResult, 1)

	go func() {
		sendDone <- s.sendRequests(shadow)
	}()

	go func() {
		recvDone <- receiveShadowResponses(shadow)
	}()

	if recvResult, ok := s.waitForShadowForwarding(ctx, sendDone, recvDone); ok {
		s.mergeReceiveResult(recvResult)
	}
}

func (s *shadowForwarder) waitForShadowForwarding(
	ctx context.Context,
	sendDone <-chan error,
	recvDone <-chan grpcStreamResult,
) (grpcStreamResult, bool) {
	var recvResult grpcStreamResult

	sendOpen := true
	recvOpen := true

	for sendOpen || recvOpen {
		select {
		case err := <-sendDone:
			sendOpen = false

			if s.handleShadowSendError(err) {
				return grpcStreamResult{}, false
			}
		case result := <-recvDone:
			recvOpen = false
			recvResult = result

			if s.handleShadowReceiveError(result) {
				return grpcStreamResult{}, false
			}
		case <-ctx.Done():
			s.markResult(shadowSkipReasonTimeout, ctx.Err().Error())
			s.cancelShadow()

			return grpcStreamResult{}, false
		}
	}

	return recvResult, true
}

func (s *shadowForwarder) handleShadowSendError(err error) bool {
	if err == nil {
		return false
	}

	s.markResult("", err.Error())
	s.cancelShadow()

	return true
}

func (s *shadowForwarder) handleShadowReceiveError(result grpcStreamResult) bool {
	if result.Err == "" {
		return false
	}

	s.mergeReceiveResult(result)
	s.cancelShadow()

	return true
}

func (s *shadowForwarder) cancelShadow() {
	if s.cancel != nil {
		s.cancel()
	}
}

func (s *shadowForwarder) failStart(err error) {
	reason := shadowSkipReasonNotStarted
	if status.Code(err) == codes.Unavailable {
		reason = shadowSkipReasonTargetUnavailable
	}

	s.markResult(reason, err.Error())
	s.Close()
}

func (s *shadowForwarder) markStarted() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.result.Started = true
}

func (s *shadowForwarder) markResult(reason string, err string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.markResultLocked(reason, err)
}

func (s *shadowForwarder) markResultLocked(reason string, err string) {
	if reason != "" && s.result.SkipReason == "" {
		s.result.SkipReason = reason
	}

	if err != "" && s.result.Err == "" {
		s.result.Err = err
	}
}

func (s *shadowForwarder) mergeReceiveResult(received grpcStreamResult) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.result.Header = received.Header
	s.result.Trailer = received.Trailer
	s.result.StatusCode = received.StatusCode
	s.result.StatusMessage = received.StatusMessage
	s.result.MessageCount = received.MessageCount
	s.result.MessageHash = received.MessageHash

	s.result.Complete = received.Complete
	if received.Err != "" && s.result.Err == "" {
		s.result.Err = received.Err
	}

	if received.SkipReason != "" && s.result.SkipReason == "" {
		s.result.SkipReason = received.SkipReason
	}
}

func (s *shadowForwarder) sendRequests(shadow grpc.ClientStream) error {
	for msg := range s.queue {
		if err := shadow.SendMsg(cloneRawMessage(msg)); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}

			return err
		}
	}

	return shadow.CloseSend()
}

func receiveShadowResponses(shadow grpc.ClientStream) grpcStreamResult {
	hasher := newMessageHashRecorder()
	result := grpcStreamResult{Started: true, MessageHash: hasher.Sum()}

	header, err := shadow.Header()
	if err != nil {
		result.Trailer = cloneMetadata(shadow.Trailer())
		result.setError(err)

		if errors.Is(err, context.DeadlineExceeded) || result.StatusCode == codes.DeadlineExceeded {
			result.SkipReason = shadowSkipReasonTimeout
		} else {
			result.Complete = isFinalStatusError(err)
		}

		return result
	}

	result.Header = cloneMetadata(header)

	for {
		var msg rawMessage
		if err := shadow.RecvMsg(&msg); err != nil {
			result.Trailer = cloneMetadata(shadow.Trailer())
			if errors.Is(err, io.EOF) {
				result.StatusCode = codes.OK
				result.StatusMessage = ""
				result.Complete = true
				result.MessageHash = hasher.Sum()

				return result
			}

			result.setError(err)

			if errors.Is(err, context.DeadlineExceeded) || result.StatusCode == codes.DeadlineExceeded {
				result.SkipReason = shadowSkipReasonTimeout
			} else {
				result.Complete = isFinalStatusError(err)
			}

			result.MessageHash = hasher.Sum()

			return result
		}

		hasher.Add(msg)

		result.MessageCount++
		result.MessageHash = hasher.Sum()
	}
}

func (s *shadowForwarder) closeQueueLocked() {
	if s.queue == nil || s.queueClosed {
		return
	}

	close(s.queue)
	s.queueClosed = true
}

func (s *shadowForwarder) complete() {
	if s.cancel != nil {
		s.cancel()
	}

	s.doneOnce.Do(func() {
		close(s.done)
	})
}

func (s *shadowForwarder) snapshot() grpcStreamResult {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := s.result
	result.Header = cloneMetadata(result.Header)
	result.Trailer = cloneMetadata(result.Trailer)

	return result
}

func (s *shadowForwarder) markDuration() {
	if s == nil || s.startedAt.IsZero() {
		return
	}

	s.markDurationSince(s.startedAt)
}

func (s *shadowForwarder) markDurationSince(started time.Time) {
	if s == nil || started.IsZero() {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.result.Duration = time.Since(started)
}

func startShadowBackendSpan(ctx context.Context, obs *observability.Observability, fullMethod string, target *Target) (context.Context, trace.Span) {
	if ctx == nil {
		ctx = context.Background()
	}

	if obs == nil {
		return ctx, nil
	}

	address := ""
	if target != nil {
		address = target.Address
	}

	return obs.StartSpanWithKind(ctx,
		"gRPC shadow "+fullMethod,
		trace.SpanKindClient,
		attribute.String(observability.LabelProtocol, ProtocolName),
		attribute.String(observability.LabelTarget, "shadow"),
		attribute.String(observability.LabelMethod, fullMethod),
		attribute.String("rpc.system", "grpc"),
		attribute.String("rpc.method", fullMethod),
		attribute.String("server.address", address),
	)
}

func (s *shadowForwarder) finishSpan(result grpcStreamResult) {
	if s == nil || s.observability == nil {
		return
	}

	s.spanOnce.Do(func() {
		statusLabel := grpcStatusLabel(result.StatusCode)
		resultLabel := grpcBackendMetricResult(result, nil)
		spanErr := errorFromStreamResult(result)

		if s.span != nil {
			s.span.SetAttributes(
				attribute.String("rpc.grpc.status_code", statusLabel),
				attribute.String(observability.LabelStatus, statusLabel),
				attribute.String(observability.LabelResult, resultLabel),
			)

			if spanErr != nil || result.StatusCode != codes.OK {
				description := statusLabel
				if spanErr != nil {
					description = spanErr.Error()
				}

				s.span.SetStatus(otelcodes.Error, description)
			}
		}

		s.observability.EndSpan(s.span, spanErr)
	})
}

func errorFromStreamResult(result grpcStreamResult) error {
	if result.Err == "" {
		return nil
	}

	return errors.New(result.Err)
}
