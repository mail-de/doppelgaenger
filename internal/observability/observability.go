// Package observability owns OpenMetrics and OpenTelemetry runtime wiring.
package observability

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"
	"google.golang.org/grpc/metadata"

	"doppelgaenger/internal/config"
	"doppelgaenger/internal/health"
)

const (
	defaultPrometheusAddress = "127.0.0.1"
	defaultPrometheusPort    = 9464
	defaultPrometheusPath    = "/metrics"
	defaultOTelServiceName   = "doppelgaenger"
	defaultOTelSampleRatio   = 1.0
	defaultTraceIDHeader     = "X-Trace-ID"

	instrumentationName = "doppelgaenger"

	// LabelMethod is the low-cardinality method or command label.
	LabelMethod = "method"
	// LabelOutcome is the low-cardinality ingress outcome label.
	LabelOutcome = "outcome"
	// LabelProtocol is the proxy protocol label.
	LabelProtocol = "protocol"
	// LabelResult is the low-cardinality operation result label.
	LabelResult = "result"
	// LabelShadow reports whether shadow processing was enabled.
	LabelShadow = "shadow"
	// LabelShadowStarted reports whether shadow processing actually started.
	LabelShadowStarted = "shadow_started"
	// LabelStatus is the low-cardinality backend status label.
	LabelStatus = "status"
	// LabelTarget distinguishes primary and shadow backends.
	LabelTarget = "target"

	// OutcomeBadRequest labels invalid ingress requests.
	OutcomeBadRequest = "bad_request"
	// OutcomeError labels generic ingress failures.
	OutcomeError = "error"
	// OutcomeOK labels successful ingress requests.
	OutcomeOK = "ok"
	// OutcomePrimaryError labels ingress requests where the primary backend failed.
	OutcomePrimaryError = "primary_error"
	// OutcomeTimeout labels ingress requests that timed out.
	OutcomeTimeout = "timeout"

	// ResultDiff labels primary and shadow differences.
	ResultDiff = "diff"
	// ResultError labels failed operations.
	ResultError = "error"
	// ResultOK labels successful operations.
	ResultOK = "ok"
	// ResultSame labels equivalent primary and shadow results.
	ResultSame = "same"
	// ResultSkipped labels operations that were intentionally skipped.
	ResultSkipped = "skipped"
	// ResultStatusCode labels HTTP status-code differences.
	ResultStatusCode = "status_code"
	// ResultQueueFull labels backend work that degraded because a bounded queue filled.
	ResultQueueFull = "queue_full"
	// ResultTimeout labels backend work that exceeded its timeout.
	ResultTimeout = "timeout"

	// StatusError labels backend requests that failed before a usable status was available.
	StatusError = "error"
	// StatusNone labels requests without a transport status.
	StatusNone = "none"
)

var durationHistogramBucketsSeconds = []float64{
	0.0005,
	0.001,
	0.002,
	0.003,
	0.004,
	0.005,
	0.0075,
	0.01,
	0.025,
	0.05,
	0.1,
	0.25,
	0.5,
	1,
	2.5,
	5,
	10,
}

// Observability owns Prometheus/OpenMetrics collectors and OpenTelemetry providers.
type Observability struct {
	config config.ObservabilityConfig
	logger *slog.Logger

	registry *prometheus.Registry
	metrics  *prometheusMetrics

	tracer        trace.Tracer
	meter         metric.Meter
	traceProvider *sdktrace.TracerProvider
	meterProvider *sdkmetric.MeterProvider
	otelMetrics   *otelMetricInstruments

	prometheusServer *http.Server
}

type prometheusMetrics struct {
	ingressRequests      *prometheus.CounterVec
	ingressDuration      *prometheus.HistogramVec
	backendRequests      *prometheus.CounterVec
	backendDuration      *prometheus.HistogramVec
	comparisons          *prometheus.CounterVec
	observabilityStarts  *prometheus.CounterVec
	observabilityStops   *prometheus.CounterVec
	callerIntrospections *prometheus.CounterVec
	callerFlights        prometheus.Gauge
}

type otelMetricInstruments struct {
	ingressRequests      metric.Int64Counter
	ingressDuration      metric.Float64Histogram
	backendRequests      metric.Int64Counter
	backendDuration      metric.Float64Histogram
	comparisons          metric.Int64Counter
	callerIntrospections metric.Int64Counter
	callerFlights        metric.Int64UpDownCounter
}

type prometheusCounterDuration struct {
	counter  *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

type otelCounterDuration struct {
	counter  metric.Int64Counter
	duration metric.Float64Histogram
}

// New builds the process observability runtime. Exporters remain opt-in, but
// W3C tracecontext extraction/injection is installed so incoming traces can
// continue through primary and shadow backends even when local export is off.
func New(cfg config.Config, serviceVersion string, logger *slog.Logger) (*Observability, error) {
	obsCfg := normalizeConfig(cfg.Observability, serviceVersion)

	if logger == nil {
		logger = slog.Default()
	}

	otel.SetTextMapPropagator(propagation.TraceContext{})

	obs := &Observability{
		config: obsCfg,
		logger: logger,
		tracer: otel.Tracer(instrumentationName),
		meter:  otel.Meter(instrumentationName),
	}

	if obsCfg.PrometheusEnabled {
		obs.registry = prometheus.NewRegistry()
		obs.metrics = newPrometheusMetrics(obs.registry, obsCfg.PrometheusRuntimeMetrics)
	}

	if obsCfg.OTelEnabled {
		if err := obs.initializeOpenTelemetry(context.Background()); err != nil {
			return nil, err
		}
	}

	return obs, nil
}

// RegisterHooks wires optional endpoint startup and provider shutdown into Fx.
func RegisterHooks(lc fx.Lifecycle, obs *Observability, healthState *health.State) {
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			return obs.StartPrometheusServer(healthState)
		},
		OnStop: func(ctx context.Context) error {
			healthState.MarkShuttingDown()

			shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()

			return obs.Shutdown(shutdownCtx)
		},
	})
}

func normalizeConfig(cfg config.ObservabilityConfig, serviceVersion string) config.ObservabilityConfig {
	if cfg.PrometheusAddress == "" {
		cfg.PrometheusAddress = defaultPrometheusAddress
	}

	if cfg.PrometheusPort == 0 {
		cfg.PrometheusPort = defaultPrometheusPort
	}

	if cfg.PrometheusPath == "" {
		cfg.PrometheusPath = defaultPrometheusPath
	}

	if !strings.HasPrefix(cfg.PrometheusPath, "/") {
		cfg.PrometheusPath = "/" + cfg.PrometheusPath
	}

	if cfg.PrometheusTLS.MinVersion == "" {
		cfg.PrometheusTLS.MinVersion = "1.2"
	}

	if cfg.OTelServiceName == "" {
		cfg.OTelServiceName = defaultOTelServiceName
	}

	if cfg.OTelServiceVersion == "" {
		cfg.OTelServiceVersion = serviceVersion
	}

	if cfg.OTelSampleRatio == nil {
		defaultRatio := defaultOTelSampleRatio
		cfg.OTelSampleRatio = &defaultRatio
	}

	if cfg.OTLPHeaders == nil {
		cfg.OTLPHeaders = map[string]string{}
	}

	if cfg.TraceIDHeader == "" {
		cfg.TraceIDHeader = defaultTraceIDHeader
	}

	cfg.TraceIDHeader = http.CanonicalHeaderKey(cfg.TraceIDHeader)

	return cfg
}

func defaultedOTelSampleRatio(cfg config.ObservabilityConfig) float64 {
	if cfg.OTelSampleRatio == nil {
		return defaultOTelSampleRatio
	}

	return *cfg.OTelSampleRatio
}

func (o *Observability) initializeOpenTelemetry(ctx context.Context) error {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			attribute.String("service.name", o.config.OTelServiceName),
			attribute.String("service.version", o.config.OTelServiceVersion),
		),
	)
	if err != nil {
		return fmt.Errorf("creating OpenTelemetry resource: %w", err)
	}

	if o.config.OTelTracesEnabled {
		if err := o.initializeOpenTelemetryTracing(ctx, res); err != nil {
			return err
		}
	}

	if o.config.OTelMetricsEnabled {
		if err := o.initializeOpenTelemetryMetrics(ctx, res); err != nil {
			return err
		}
	}

	return nil
}

func (o *Observability) initializeOpenTelemetryTracing(ctx context.Context, res *resource.Resource) error {
	exporter, err := otlptracehttp.New(ctx, traceHTTPOptions(o.config)...)
	if err != nil {
		return fmt.Errorf("creating OTLP trace exporter: %w", err)
	}

	sampler := sdktrace.ParentBased(sdktrace.TraceIDRatioBased(defaultedOTelSampleRatio(o.config)))
	o.traceProvider = sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
		sdktrace.WithBatcher(exporter),
	)
	o.tracer = o.traceProvider.Tracer(instrumentationName)
	otel.SetTracerProvider(o.traceProvider)

	return nil
}

func (o *Observability) initializeOpenTelemetryMetrics(ctx context.Context, res *resource.Resource) error {
	exporter, err := otlpmetrichttp.New(ctx, metricHTTPOptions(o.config)...)
	if err != nil {
		return fmt.Errorf("creating OTLP metric exporter: %w", err)
	}

	o.meterProvider = sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter)),
	)
	o.meter = o.meterProvider.Meter(instrumentationName)
	otel.SetMeterProvider(o.meterProvider)

	return o.initializeOpenTelemetryInstruments()
}

func traceHTTPOptions(cfg config.ObservabilityConfig) []otlptracehttp.Option {
	endpoint, insecure := normalizedOTLPEndpoint(cfg)

	options := []otlptracehttp.Option{otlptracehttp.WithEndpoint(endpoint)}
	if insecure {
		options = append(options, otlptracehttp.WithInsecure())
	}

	if len(cfg.OTLPHeaders) > 0 {
		options = append(options, otlptracehttp.WithHeaders(cfg.OTLPHeaders))
	}

	return options
}

func metricHTTPOptions(cfg config.ObservabilityConfig) []otlpmetrichttp.Option {
	endpoint, insecure := normalizedOTLPEndpoint(cfg)

	options := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpoint(endpoint)}
	if insecure {
		options = append(options, otlpmetrichttp.WithInsecure())
	}

	if len(cfg.OTLPHeaders) > 0 {
		options = append(options, otlpmetrichttp.WithHeaders(cfg.OTLPHeaders))
	}

	return options
}

func normalizedOTLPEndpoint(cfg config.ObservabilityConfig) (string, bool) {
	if parsed, err := url.Parse(cfg.OTLPEndpoint); err == nil && parsed.Host != "" {
		return parsed.Host, cfg.OTLPInsecure || parsed.Scheme == "http"
	}

	return cfg.OTLPEndpoint, cfg.OTLPInsecure
}

func (o *Observability) initializeOpenTelemetryInstruments() error {
	instruments := &otelMetricInstruments{}

	var err error

	if instruments.ingressRequests, err = o.meter.Int64Counter("doppelgaenger_ingress_requests"); err != nil {
		return err
	}

	if instruments.ingressDuration, err = o.meter.Float64Histogram("doppelgaenger_ingress_request_duration", durationHistogramOptions()...); err != nil {
		return err
	}

	if instruments.backendRequests, err = o.meter.Int64Counter("doppelgaenger_backend_requests"); err != nil {
		return err
	}

	if instruments.backendDuration, err = o.meter.Float64Histogram("doppelgaenger_backend_request_duration", durationHistogramOptions()...); err != nil {
		return err
	}

	if instruments.comparisons, err = o.meter.Int64Counter("doppelgaenger_comparisons"); err != nil {
		return err
	}

	if instruments.callerIntrospections, err = o.meter.Int64Counter("doppelgaenger_grpc_caller_introspections"); err != nil {
		return err
	}

	if instruments.callerFlights, err = o.meter.Int64UpDownCounter("doppelgaenger_grpc_caller_introspection_flights"); err != nil {
		return err
	}

	o.otelMetrics = instruments

	return nil
}

func durationHistogramOptions() []metric.Float64HistogramOption {
	buckets := append([]float64(nil), durationHistogramBucketsSeconds...)

	return []metric.Float64HistogramOption{
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(buckets...),
	}
}

func newPrometheusMetrics(registry *prometheus.Registry, runtimeMetrics bool) *prometheusMetrics {
	metrics := &prometheusMetrics{
		ingressRequests: newCounterVec(
			"doppelgaenger_ingress_requests_total",
			"Total incoming proxy requests by protocol, method or command, outcome, and shadow state.",
			LabelProtocol,
			LabelMethod,
			LabelOutcome,
			LabelShadow,
			LabelShadowStarted,
		),
		ingressDuration: newDurationVec(
			"doppelgaenger_ingress_request_duration_seconds",
			"Incoming proxy request duration in seconds.",
			LabelProtocol,
			LabelMethod,
			LabelOutcome,
			LabelShadow,
			LabelShadowStarted,
		),
		backendRequests: newCounterVec(
			"doppelgaenger_backend_requests_total",
			"Total outgoing backend requests by protocol and target.",
			LabelProtocol,
			LabelTarget,
			LabelMethod,
			LabelStatus,
			LabelResult,
		),
		backendDuration: newDurationVec(
			"doppelgaenger_backend_request_duration_seconds",
			"Outgoing backend request duration in seconds.",
			LabelProtocol,
			LabelTarget,
			LabelMethod,
			LabelStatus,
			LabelResult,
		),
		comparisons:         newCounterVec("doppelgaenger_comparisons_total", "Total primary/shadow comparison results.", LabelProtocol, LabelResult),
		observabilityStarts: newCounterVec("doppelgaenger_observability_startups_total", "Prometheus/OpenMetrics endpoint startup attempts.", LabelResult),
		observabilityStops:  newCounterVec("doppelgaenger_observability_shutdowns_total", "Observability shutdown attempts.", LabelResult),
	}

	if runtimeMetrics {
		registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	}

	registry.MustRegister(
		metrics.ingressRequests,
		metrics.ingressDuration,
		metrics.backendRequests,
		metrics.backendDuration,
		metrics.comparisons,
		metrics.observabilityStarts,
		metrics.observabilityStops,
	)
	registerCallerIntrospectionMetrics(registry, metrics)

	return metrics
}

func registerCallerIntrospectionMetrics(registry *prometheus.Registry, metrics *prometheusMetrics) {
	metrics.callerIntrospections = newCounterVec(
		"doppelgaenger_grpc_caller_introspections_total",
		"gRPC caller token lookups by result: hit, miss, shared, error, or canceled.",
		LabelResult,
	)
	metrics.callerFlights = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "doppelgaenger_grpc_caller_introspection_flights",
		Help: "gRPC caller introspection requests currently in progress.",
	})

	registry.MustRegister(metrics.callerIntrospections, metrics.callerFlights)
}

func newCounterVec(name, help string, labels ...string) *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, labels)
}

func newDurationVec(name, help string, labels ...string) *prometheus.HistogramVec {
	return prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    name,
		Help:    help,
		Buckets: durationHistogramBucketsSeconds,
	}, labels)
}

// StartPrometheusServer starts the optional Prometheus/OpenMetrics endpoint.
func (o *Observability) StartPrometheusServer(healthState *health.State) error {
	if o == nil || !o.PrometheusEnabled() {
		return nil
	}

	address := net.JoinHostPort(o.config.PrometheusAddress, fmt.Sprintf("%d", o.config.PrometheusPort))

	listener, err := net.Listen("tcp", address)
	if err != nil {
		o.observeObservabilityStartup(ResultError)

		return fmt.Errorf("listen prometheus endpoint %s: %w", address, err)
	}

	tlsConfig, err := buildPrometheusServerTLSConfig(o.config.PrometheusTLS)
	if err != nil {
		_ = listener.Close()

		o.observeObservabilityStartup(ResultError)

		return err
	}

	server := &http.Server{
		Addr:              address,
		Handler:           o.prometheusMux(healthState),
		ReadHeaderTimeout: 5 * time.Second,
		TLSConfig:         tlsConfig,
	}
	o.prometheusServer = server
	o.observeObservabilityStartup(ResultOK)

	go func() {
		var serveErr error
		if tlsConfig != nil {
			serveErr = server.ServeTLS(listener, "", "")
		} else {
			serveErr = server.Serve(listener)
		}

		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			o.logger.Error("Prometheus endpoint stopped", "err", serveErr)
		}
	}()

	o.logger.Info(
		"Prometheus metrics endpoint started",
		"address", address,
		"path", o.PrometheusPath(),
		"tls", tlsConfig != nil,
		"basic_auth", o.config.PrometheusHTTPAuthBasic != "",
	)

	return nil
}

func (o *Observability) prometheusMux(healthState *health.State) http.Handler {
	mux := http.NewServeMux()
	if healthState != nil && o.PrometheusPath() != health.Path {
		mux.HandleFunc(health.Path, healthState.Handler)
	}

	mux.Handle(o.PrometheusPath(), o.PrometheusHandler())

	return mux
}

// PrometheusEnabled reports whether the metrics endpoint should be started.
func (o *Observability) PrometheusEnabled() bool {
	return o != nil && o.config.PrometheusEnabled && o.registry != nil
}

// PrometheusPath returns the configured metrics path with defaults applied.
func (o *Observability) PrometheusPath() string {
	if o == nil || o.config.PrometheusPath == "" {
		return defaultPrometheusPath
	}

	return o.config.PrometheusPath
}

// PrometheusHandler returns the HTTP handler for this runtime's registry.
func (o *Observability) PrometheusHandler() http.Handler {
	if o == nil || o.registry == nil {
		return http.NotFoundHandler()
	}

	handler := promhttp.HandlerFor(o.registry, promhttp.HandlerOpts{EnableOpenMetrics: true})
	if o.config.PrometheusHTTPAuthBasic == "" {
		return handler
	}

	username, password, err := config.SplitBasicAuthCredentials(o.config.PrometheusHTTPAuthBasic)
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		})
	}

	return requireHTTPBasicAuth(username, password, handler)
}

func buildPrometheusServerTLSConfig(cfg config.PrometheusTLS) (*tls.Config, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	if cfg.Cert == "" || cfg.Key == "" {
		return nil, errors.New("observability prometheus_tls requires cert and key when enabled")
	}

	minVersion, err := config.ResolveTLSMinVersion(cfg.MinVersion)
	if err != nil {
		return nil, fmt.Errorf("observability prometheus_tls: %w", err)
	}

	cert, err := tls.LoadX509KeyPair(cfg.Cert, cfg.Key)
	if err != nil {
		return nil, fmt.Errorf("observability prometheus_tls load cert/key: %w", err)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   minVersion,
	}, nil
}

func requireHTTPBasicAuth(username, password string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPassword, ok := r.BasicAuth()
		if !ok || !secureStringEqual(gotUser, username) || !secureStringEqual(gotPassword, password) {
			w.Header().Set("WWW-Authenticate", `Basic realm="doppelgaenger metrics"`)
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)

			return
		}

		next.ServeHTTP(w, r)
	})
}

func secureStringEqual(got, want string) bool {
	gotHash := sha256.Sum256([]byte(got))
	wantHash := sha256.Sum256([]byte(want))

	return subtle.ConstantTimeCompare(gotHash[:], wantHash[:]) == 1
}

// Shutdown flushes OpenTelemetry providers and stops the metrics endpoint.
func (o *Observability) Shutdown(ctx context.Context) error {
	if o == nil {
		return nil
	}

	var shutdownErrors []error
	if o.prometheusServer != nil {
		shutdownErrors = append(shutdownErrors, o.prometheusServer.Shutdown(ctx))
	}

	if o.meterProvider != nil {
		shutdownErrors = append(shutdownErrors, o.meterProvider.Shutdown(ctx))
	}

	if o.traceProvider != nil {
		shutdownErrors = append(shutdownErrors, o.traceProvider.Shutdown(ctx))
	}

	err := errors.Join(shutdownErrors...)
	if err != nil {
		o.observeObservabilityShutdown(ResultError)
	} else {
		o.observeObservabilityShutdown(ResultOK)
	}

	return err
}

// ExtractHTTPContext extracts an incoming W3C trace context from HTTP headers.
func (o *Observability) ExtractHTTPContext(ctx context.Context, header http.Header) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	if o == nil || header == nil {
		return ctx
	}

	return otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(header))
}

// InjectHTTPTraceContext injects W3C trace context and the configured bare trace-id header.
func (o *Observability) InjectHTTPTraceContext(ctx context.Context, header http.Header) {
	if ctx == nil {
		ctx = context.Background()
	}

	if o == nil || header == nil {
		return
	}

	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(header))
	o.SetTraceIDHeader(ctx, header)
}

// ExtractGRPCContext extracts an incoming W3C trace context from gRPC metadata.
func (o *Observability) ExtractGRPCContext(ctx context.Context, md metadata.MD) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	if o == nil || md == nil {
		return ctx
	}

	return otel.GetTextMapPropagator().Extract(ctx, grpcMetadataCarrier{md: md})
}

// InjectGRPCTraceContext injects W3C trace context and the configured bare trace-id metadata.
func (o *Observability) InjectGRPCTraceContext(ctx context.Context, md metadata.MD) {
	if ctx == nil {
		ctx = context.Background()
	}

	if o == nil || md == nil {
		return
	}

	otel.GetTextMapPropagator().Inject(ctx, grpcMetadataCarrier{md: md})
	o.SetGRPCTraceIDMetadata(ctx, md)
}

// SetTraceIDHeader writes the configured bare trace-id header when a valid trace is active.
func (o *Observability) SetTraceIDHeader(ctx context.Context, header http.Header) {
	if o == nil || header == nil || o.config.TraceIDHeader == "" {
		return
	}

	if header.Get(o.config.TraceIDHeader) != "" {
		return
	}

	if traceID := TraceIDFromContext(ctx); traceID != "" {
		header.Set(o.config.TraceIDHeader, traceID)
	}
}

// SetGRPCTraceIDMetadata writes the configured bare trace-id metadata when a valid trace is active.
func (o *Observability) SetGRPCTraceIDMetadata(ctx context.Context, md metadata.MD) {
	if o == nil || md == nil || o.config.TraceIDHeader == "" {
		return
	}

	key := grpcTraceIDMetadataKey(o.config.TraceIDHeader)
	if key == "" || len(md.Get(key)) > 0 {
		return
	}

	if traceID := TraceIDFromContext(ctx); traceID != "" {
		md.Set(key, traceID)
	}
}

// TraceIDFromContext returns the active span trace ID, if one exists.
func TraceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}

	spanCtx := trace.SpanFromContext(ctx).SpanContext()
	if !spanCtx.TraceID().IsValid() {
		return ""
	}

	return spanCtx.TraceID().String()
}

type grpcMetadataCarrier struct {
	md metadata.MD
}

func (c grpcMetadataCarrier) Get(key string) string {
	values := c.md.Get(strings.ToLower(key))
	if len(values) == 0 {
		return ""
	}

	return values[0]
}

func (c grpcMetadataCarrier) Set(key string, value string) {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return
	}

	c.md.Set(key, value)
}

func (c grpcMetadataCarrier) Keys() []string {
	keys := make([]string, 0, len(c.md))
	for key := range c.md {
		keys = append(keys, key)
	}

	return keys
}

func grpcTraceIDMetadataKey(value string) string {
	key := strings.ToLower(strings.TrimSpace(value))
	if key == "" {
		return ""
	}

	for _, r := range key {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}

		return ""
	}

	return key
}

func (o *Observability) traceSpansEnabled() bool {
	return o != nil && o.config.OTelEnabled && o.config.OTelTracesEnabled && o.tracer != nil
}

// StartSpan creates an internal trace span when tracing is enabled.
func (o *Observability) StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return o.StartSpanWithKind(ctx, name, trace.SpanKindInternal, attrs...)
}

// StartSpanWithKind creates a trace span with the supplied kind when tracing is enabled.
func (o *Observability) StartSpanWithKind(ctx context.Context, name string, kind trace.SpanKind, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	if ctx == nil {
		ctx = context.Background()
	}

	if !o.traceSpansEnabled() {
		return ctx, trace.SpanFromContext(ctx)
	}

	return o.tracer.Start(ctx, name, trace.WithSpanKind(kind), trace.WithAttributes(attrs...))
}

// RecordSpanError annotates a span with an error and marks it failed.
func (o *Observability) RecordSpanError(span trace.Span, err error) {
	if span == nil || err == nil {
		return
	}

	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

// EndSpan records an optional error and ends the span.
func (o *Observability) EndSpan(span trace.Span, err error) {
	if span == nil {
		return
	}

	if err != nil {
		o.RecordSpanError(span, err)
	}

	span.End()
}

// ObserveIngressRequest records one incoming proxy request.
func (o *Observability) ObserveIngressRequest(ctx context.Context, protocol, method, outcome, shadow, shadowStarted string, duration time.Duration) {
	labels, attrs := labelValuesAndAttributes(
		LabelProtocol, protocol,
		LabelMethod, method,
		LabelOutcome, outcome,
		LabelShadow, shadow,
		LabelShadowStarted, shadowStarted,
	)

	o.observeCounterDuration(ctx, o.ingressPrometheusPair(), o.ingressOTelPair(), labels, attrs, duration)
}

// ObserveBackendRequest records one outgoing primary or shadow backend exchange.
func (o *Observability) ObserveBackendRequest(ctx context.Context, protocol, target, method, status, result string, duration time.Duration) {
	labels, attrs := labelValuesAndAttributes(
		LabelProtocol, protocol,
		LabelTarget, target,
		LabelMethod, method,
		LabelStatus, status,
		LabelResult, result,
	)

	o.observeCounterDuration(ctx, o.backendPrometheusPair(), o.backendOTelPair(), labels, attrs, duration)
}

func labelValuesAndAttributes(pairs ...string) ([]string, []attribute.KeyValue) {
	labels := make([]string, 0, len(pairs)/2)
	attrs := make([]attribute.KeyValue, 0, len(pairs)/2)

	for i := 0; i+1 < len(pairs); i += 2 {
		key := pairs[i]
		value := pairs[i+1]

		labels = append(labels, value)
		attrs = append(attrs, attribute.String(key, value))
	}

	return labels, attrs
}

// ObserveComparison records the primary/shadow comparison result.
func (o *Observability) ObserveComparison(ctx context.Context, protocol, result string) {
	if o == nil {
		return
	}

	if o.metrics != nil {
		o.metrics.comparisons.WithLabelValues(protocol, result).Inc()
	}

	if o.otelMetrics != nil {
		o.otelMetrics.comparisons.Add(ctx, 1, metric.WithAttributes(
			attribute.String(LabelProtocol, protocol),
			attribute.String(LabelResult, result),
		))
	}
}

// ObserveCallerIntrospection records one gRPC caller token lookup result.
func (o *Observability) ObserveCallerIntrospection(ctx context.Context, result string) {
	if o == nil {
		return
	}

	if o.metrics != nil {
		o.metrics.callerIntrospections.WithLabelValues(result).Inc()
	}

	if o.otelMetrics != nil {
		o.otelMetrics.callerIntrospections.Add(ctx, 1, metric.WithAttributes(attribute.String(LabelResult, result)))
	}
}

// AddCallerIntrospectionFlights adjusts the number of introspection requests in progress.
func (o *Observability) AddCallerIntrospectionFlights(ctx context.Context, delta int64) {
	if o == nil {
		return
	}

	if o.metrics != nil {
		o.metrics.callerFlights.Add(float64(delta))
	}

	if o.otelMetrics != nil {
		o.otelMetrics.callerFlights.Add(ctx, delta)
	}
}

func (o *Observability) observeCounterDuration(ctx context.Context, prom prometheusCounterDuration, otelPair otelCounterDuration, labels []string, attrs []attribute.KeyValue, duration time.Duration) {
	if o == nil {
		return
	}

	if prom.counter != nil && prom.duration != nil {
		prom.counter.WithLabelValues(labels...).Inc()
		prom.duration.WithLabelValues(labels...).Observe(duration.Seconds())
	}

	if otelPair.counter != nil && otelPair.duration != nil {
		otelPair.counter.Add(ctx, 1, metric.WithAttributes(attrs...))
		otelPair.duration.Record(ctx, duration.Seconds(), metric.WithAttributes(attrs...))
	}
}

func (o *Observability) ingressPrometheusPair() prometheusCounterDuration {
	if o == nil || o.metrics == nil {
		return prometheusCounterDuration{}
	}

	return prometheusCounterDuration{o.metrics.ingressRequests, o.metrics.ingressDuration}
}

func (o *Observability) ingressOTelPair() otelCounterDuration {
	if o == nil || o.otelMetrics == nil {
		return otelCounterDuration{}
	}

	return otelCounterDuration{o.otelMetrics.ingressRequests, o.otelMetrics.ingressDuration}
}

func (o *Observability) backendPrometheusPair() prometheusCounterDuration {
	if o == nil || o.metrics == nil {
		return prometheusCounterDuration{}
	}

	return prometheusCounterDuration{o.metrics.backendRequests, o.metrics.backendDuration}
}

func (o *Observability) backendOTelPair() otelCounterDuration {
	if o == nil || o.otelMetrics == nil {
		return otelCounterDuration{}
	}

	return otelCounterDuration{o.otelMetrics.backendRequests, o.otelMetrics.backendDuration}
}

func (o *Observability) observeObservabilityStartup(result string) {
	if o != nil && o.metrics != nil {
		o.metrics.observabilityStarts.WithLabelValues(result).Inc()
	}
}

func (o *Observability) observeObservabilityShutdown(result string) {
	if o != nil && o.metrics != nil {
		o.metrics.observabilityStops.WithLabelValues(result).Inc()
	}
}

// HTTPStatusClass converts HTTP status codes into low-cardinality status labels.
func HTTPStatusClass(status int) string {
	switch {
	case status >= 100 && status < 200:
		return "1xx"
	case status >= 200 && status < 300:
		return "2xx"
	case status >= 300 && status < 400:
		return "3xx"
	case status >= 400 && status < 500:
		return "4xx"
	case status >= 500 && status < 600:
		return "5xx"
	default:
		return StatusError
	}
}

// BoolLabel renders booleans as stable metric labels.
func BoolLabel(value bool) string {
	if value {
		return "true"
	}

	return "false"
}
