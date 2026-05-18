package backend

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"doppelgaenger/internal/config"
	"doppelgaenger/internal/observability"
)

func TestRequesterPropagatesTraceContextToBackend(t *testing.T) {
	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"

	var gotTraceparent, gotTraceID string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTraceparent = r.Header.Get("traceparent")
		gotTraceID = r.Header.Get("X-Trace-ID")

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse test server url: %v", err)
	}

	obs, err := observability.New(config.Config{}, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new observability: %v", err)
	}

	header := http.Header{}
	header.Set("traceparent", "00-"+traceID+"-00f067aa0ba902b7-01")
	ctx := obs.ExtractHTTPContext(context.Background(), header)

	requester := NewRequester(BackendPrimary, []*url.URL{base}, nil, nil, 0, HTTPClientConfig{}, obs)

	result := requester.Do(Request{
		Ctx:       ctx,
		Kind:      BackendPrimary,
		Method:    http.MethodPost,
		Path:      "/auth",
		RequestID: 42,
	})

	if result.Err != nil {
		t.Fatalf("expected request to succeed, got error: %v", result.Err)
	}

	if result.Status != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d", result.Status)
	}

	if !strings.Contains(gotTraceparent, traceID) {
		t.Fatalf("expected propagated traceparent to contain trace id %s, got %q", traceID, gotTraceparent)
	}

	if gotTraceID != traceID {
		t.Fatalf("expected X-Trace-ID %s, got %q", traceID, gotTraceID)
	}
}
