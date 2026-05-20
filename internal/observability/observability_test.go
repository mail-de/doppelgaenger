package observability

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/grpc/metadata"

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

	if body := w.Body.String(); !strings.Contains(body, `le="0.001"`) || !strings.Contains(body, `le="0.003"`) {
		t.Fatalf("expected millisecond-scale duration buckets in response, got %q", body)
	}
}

func TestOpenTelemetryDurationHistogramsUseMillisecondScaleBuckets(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	defer func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown meter provider: %v", err)
		}
	}()

	obs := &Observability{meter: provider.Meter(instrumentationName)}
	if err := obs.initializeOpenTelemetryInstruments(); err != nil {
		t.Fatalf("initialize OpenTelemetry instruments: %v", err)
	}

	ctx := context.Background()
	obs.otelMetrics.ingressDuration.Record(ctx, 0.002)
	obs.otelMetrics.backendDuration.Record(ctx, 0.003)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}

	for _, name := range []string{
		"doppelgaenger_ingress_request_duration",
		"doppelgaenger_backend_request_duration",
	} {
		if got := histogramBounds(t, rm, name); !reflect.DeepEqual(got, durationHistogramBucketsSeconds) {
			t.Fatalf("expected %s buckets %v, got %v", name, durationHistogramBucketsSeconds, got)
		}
	}
}

func histogramBounds(t *testing.T, rm metricdata.ResourceMetrics, name string) []float64 {
	t.Helper()

	for _, scopeMetrics := range rm.ScopeMetrics {
		for _, m := range scopeMetrics.Metrics {
			if m.Name != name {
				continue
			}

			data, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("expected %s to be a float64 histogram, got %T", name, m.Data)
			}

			if len(data.DataPoints) == 0 {
				t.Fatalf("expected %s to have histogram data points", name)
			}

			return data.DataPoints[0].Bounds
		}
	}

	t.Fatalf("metric %s not found", name)

	return nil
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

func TestGRPCTraceMetadataUsesExtractedTraceContext(t *testing.T) {
	obs, err := New(config.Config{}, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new observability: %v", err)
	}

	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"

	in := metadata.Pairs("traceparent", "00-"+traceID+"-00f067aa0ba902b7-01")
	ctx := obs.ExtractGRPCContext(context.Background(), in)
	out := metadata.MD{}
	obs.InjectGRPCTraceContext(ctx, out)

	if got := strings.Join(out.Get("traceparent"), ","); !strings.Contains(got, traceID) {
		t.Fatalf("expected propagated traceparent to contain trace id %s, got %q", traceID, got)
	}

	if got := strings.Join(out.Get("x-trace-id"), ","); got != traceID {
		t.Fatalf("expected x-trace-id %s, got %q", traceID, got)
	}
}
