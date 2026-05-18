package observability

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"doppelgaenger/internal/config"
)

func TestPrometheusHandlerRequiresBasicAuthAndServesOpenMetrics(t *testing.T) {
	obs, err := New(config.Config{
		Observability: config.ObservabilityConfig{
			PrometheusEnabled:       true,
			PrometheusHTTPAuthBasic: "metrics:secret",
		},
	}, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new observability: %v", err)
	}

	obs.ObserveIngressRequest(context.Background(), "http", http.MethodGet, OutcomeOK, "false", "false", time.Millisecond)

	unauthorized := httptest.NewRecorder()
	obs.PrometheusHandler().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without basic auth, got %d", unauthorized.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Accept", "application/openmetrics-text")
	req.SetBasicAuth("metrics", "secret")

	w := httptest.NewRecorder()
	obs.PrometheusHandler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	if contentType := w.Header().Get("Content-Type"); !strings.Contains(contentType, "application/openmetrics-text") {
		t.Fatalf("expected OpenMetrics content type, got %q", contentType)
	}

	if body := w.Body.String(); !strings.Contains(body, "doppelgaenger_ingress_requests_total") {
		t.Fatalf("expected ingress metric in response, got %q", body)
	}
}

func TestTraceIDHeaderUsesExtractedTraceContext(t *testing.T) {
	obs, err := New(config.Config{}, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new observability: %v", err)
	}

	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"

	header := http.Header{}
	header.Set("traceparent", "00-"+traceID+"-00f067aa0ba902b7-01")

	ctx := obs.ExtractHTTPContext(context.Background(), header)
	out := http.Header{}
	obs.InjectHTTPTraceContext(ctx, out)

	if got := out.Get("traceparent"); !strings.Contains(got, traceID) {
		t.Fatalf("expected propagated traceparent to contain trace id %s, got %q", traceID, got)
	}

	if got := out.Get("X-Trace-ID"); got != traceID {
		t.Fatalf("expected X-Trace-ID %s, got %q", traceID, got)
	}
}
