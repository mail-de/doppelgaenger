package backend

import (
	"context"
	"crypto/tls"
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

const (
	protoHTTP1 = "HTTP/1.1"
	protoHTTP2 = "HTTP/2.0"
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

func TestRequesterPreservesInboundHost(t *testing.T) {
	const expectedHost = "login.example.test"

	var gotHost string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse test server url: %v", err)
	}

	requester := NewRequester(BackendPrimary, []*url.URL{base}, nil, nil, 0, HTTPClientConfig{}, nil)
	result := requester.Do(Request{
		Kind:   BackendPrimary,
		Method: http.MethodGet,
		Path:   "/oidc/authorize",
		Host:   expectedHost,
	})

	if result.Err != nil {
		t.Fatalf("expected request to succeed, got error: %v", result.Err)
	}

	if gotHost != expectedHost {
		t.Fatalf("expected backend host %q, got %q", expectedHost, gotHost)
	}
}

func TestHTTPClientProtocolModes(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		want     string
	}{
		{name: "auto negotiates http2", protocol: HTTPProtocolAuto, want: protoHTTP2},
		{name: "http1 disables http2", protocol: HTTPProtocolHTTP1, want: protoHTTP1},
		{name: "http2 requires http2", protocol: HTTPProtocolHTTP2, want: protoHTTP2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string

			server := newHTTP2TLSServer(t, func(w http.ResponseWriter, r *http.Request) {
				got = r.Proto

				w.WriteHeader(http.StatusNoContent)
			})
			defer server.Close()

			client := newHTTPClient(serverTLSConfig(t, server), HTTPClientConfig{Protocol: tt.protocol})

			resp, err := client.Get(server.URL)
			if err != nil {
				t.Fatalf("expected request to succeed, got error: %v", err)
			}
			defer func() {
				_ = resp.Body.Close()
			}()

			if got != tt.want {
				t.Fatalf("expected backend protocol %s, got %s", tt.want, got)
			}
		})
	}
}

func TestHTTPClientHTTP2ModeRejectsHTTP1Fallback(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := newHTTPClient(serverTLSConfig(t, server), HTTPClientConfig{Protocol: HTTPProtocolHTTP2})

	resp, err := client.Get(server.URL)
	if err == nil {
		_ = resp.Body.Close()

		t.Fatalf("expected HTTP/2 mode to reject HTTP/1.1 fallback")
	}

	if !strings.Contains(err.Error(), "expected HTTP/2") {
		t.Fatalf("expected HTTP/2 fallback error, got %v", err)
	}
}

func newHTTP2TLSServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()

	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = true
	server.StartTLS()

	return server
}

func serverTLSConfig(t *testing.T, server *httptest.Server) *tls.Config {
	t.Helper()

	transport, ok := server.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected httptest client transport to be *http.Transport")
	}

	return transport.TLSClientConfig.Clone()
}
