package grpcproxy

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"

	"doppelgaenger/internal/config"
	"doppelgaenger/internal/logging"
)

func TestRPCLogCorrelationFields(t *testing.T) {
	var output bytes.Buffer

	h := &Handler{cfg: config.Config{LogFields: config.LogFieldsConfig{
		RequestMetadata: []string{"x-client-id"}, ResponseMetadata: []string{"x-session-id"},
	}}, logger: slog.New(logging.NewHandler(&output, true, "info"))}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-client-id", "client-value", "authorization", "secret"))
	rpcCtx := h.newRPCContext(ctx, 1, testFullMethodUnary, time.Now())
	h.logRPCResult(rpcCtx, testFullMethodUnary, Decision{}, nil, grpcShadowPolicyDecision{},
		grpcStreamResult{Header: metadata.Pairs("x-session-id", "primary-session")},
		grpcStreamResult{Header: metadata.Pairs("x-session-id", "shadow-session"), Trailer: metadata.Pairs("x-session-id", "trailer-session")}, grpcCompareResult{})

	for _, want := range []string{`"request_metadata":{"x_client_id":"client-value"}`, `"primary_response_metadata":{"x_session_id":"primary-session"}`, `"shadow_response_metadata":{"x_session_id":"shadow-session"}`, `"shadow_trailer_metadata":{"x_session_id":"trailer-session"}`} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %s: %s", want, output.String())
		}
	}

	if strings.Contains(output.String(), "secret") || strings.Contains(output.String(), "trace_id") {
		t.Fatalf("unexpected field: %s", output.String())
	}
}
