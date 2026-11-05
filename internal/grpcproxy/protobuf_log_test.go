package grpcproxy

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protowire"

	"doppelgaenger/internal/config"
	"doppelgaenger/internal/logging"
)

type protobufServerStream struct {
	grpc.ServerStream
	request  rawMessage
	response rawMessage
}

func (s *protobufServerStream) RecvMsg(m any) error { *m.(*rawMessage) = s.request; return nil }
func (s *protobufServerStream) SendMsg(m any) error {
	s.response = m.(rawMessage)
	return errors.New("client disconnected")
}

type protobufClientStream struct {
	grpc.ClientStream
	response rawMessage
}

func (s protobufClientStream) RecvMsg(m any) error { *m.(*rawMessage) = s.response; return nil }

func TestProtobufCaptureAcrossForwardingAndLogging(t *testing.T) {
	var output bytes.Buffer

	fields := []logging.ProtobufField{{Name: "session_id", Path: []int{3}, Type: "string"}}
	h := &Handler{cfg: config.Config{LogFields: config.LogFieldsConfig{GRPCMessages: []config.ProtobufLogRule{{Method: testFullMethodUnary, Request: fields, Response: fields}}}}, logger: slog.New(logging.NewHandler(&output, true, "info"))}
	rpc := h.newRPCContext(context.Background(), 1, testFullMethodUnary, time.Now())
	wire := func(value string) rawMessage {
		return protowire.AppendString(protowire.AppendTag(nil, 3, protowire.BytesType), value)
	}
	server := &protobufServerStream{request: wire("request-id")}
	wrapped := wrappedServerStream{ServerStream: server, requestProtobuf: rpc.requestProtobuf, responseProtobuf: rpc.responseProtobuf}

	var request rawMessage
	if err := wrapped.RecvMsg(&request); err != nil {
		t.Fatal(err)
	}

	if err := wrapped.SendMsg(wire("primary-id")); err == nil {
		t.Fatal("lost send error")
	}

	shadow := &shadowForwarder{capture: logging.NewProtobufCapture("shadow_response_protobuf", fields)}
	client := captureClientStream{ClientStream: protobufClientStream{response: wire("shadow-id")}, capture: shadow.capture}

	var response rawMessage
	if err := client.RecvMsg(&response); err != nil {
		t.Fatal(err)
	}

	h.logRPCResult(rpc, testFullMethodUnary, Decision{}, nil, grpcShadowPolicyDecision{}, grpcStreamResult{}, shadow.snapshot(), grpcCompareResult{})

	for _, expected := range []string{`"request_protobuf":{"session_id":"request-id"}`, `"primary_response_protobuf":{"session_id":"primary-id"}`, `"shadow_response_protobuf":{"session_id":"shadow-id"}`} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("missing %s: %s", expected, output.String())
		}
	}

	if !bytes.Equal(request, server.request) || !bytes.Equal(response, wire("shadow-id")) {
		t.Fatal("forwarding payload mutated")
	}
}
