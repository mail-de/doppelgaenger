package grpcproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"doppelgaenger/internal/config"
	"doppelgaenger/internal/observability"
)

const (
	testCallerSecretEnv   = "TEST_INTROSPECTION_SECRET"
	testCallerSecret      = "test-secret"
	testCallerToken       = "classified-token"
	testCallerLeakToken   = "never-log-this-token"
	testCallerLeakPath    = "/leak"
	testCallerOtherIssuer = "https://other.example"
	testCallerWriteScope  = "rpc:write"

	testCallerForeignAudience = "elsewhere"
	testClaimActive           = "active"
	testClaimIssuer           = "iss"
	testClaimAudience         = "aud"
	testClaimExpiry           = "exp"
	testClaimNotBefore        = "nbf"
	testCaseIssuer            = "issuer"
	testCaseAudience          = "audience"
	testCaseDuplicate         = "duplicate"
	testCaseBadCA             = "bad-ca"
	testCaseNotBefore         = "not-before"
	testLevelWarn             = "WARN"
)

type introspectionResponder func(w http.ResponseWriter, r *http.Request)

func respondIntrospectionClaims(overrides map[string]any) introspectionResponder {
	return func(w http.ResponseWriter, _ *http.Request) {
		claims := map[string]any{testClaimActive: true, testClaimIssuer: testCallerIssuer, testClaimAudience: []string{testCallerAudience}, testCallerScopeKey: testCallerScope, testClaimExpiry: time.Now().Add(time.Minute).Unix()}
		for key, value := range overrides {
			claims[key] = value
		}

		_ = json.NewEncoder(w).Encode(claims)
	}
}

func respondIntrospectionStatus(code int) introspectionResponder {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
	}
}

func respondIntrospectionBody(body string) introspectionResponder {
	return func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}
}

func respondIntrospectionError(code string) introspectionResponder {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
	}
}

func respondIntrospectionRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, testCallerLeakPath, http.StatusTemporaryRedirect)
}

func respondIntrospectionSlowly(w http.ResponseWriter, r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-time.After(300 * time.Millisecond):
		respondIntrospectionClaims(nil)(w, r)
	}
}

// startIntrospectionServer counts introspection requests and fails the test on
// wrong proxy credentials or followed redirects.
func startIntrospectionServer(t *testing.T, calls *atomic.Int64, respond introspectionResponder) *httptest.Server {
	t.Helper()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == testCallerLeakPath {
			t.Error("introspection followed redirect")

			return
		}

		checkIntrospectionCredentials(t, r)

		if calls != nil {
			calls.Add(1)
		}

		respond(w, r)
	}))
	t.Cleanup(server.Close)

	return server
}

// newIntrospectionHandler builds a directly constructed handler against a test
// issuer; configure adjusts the caller configuration before first use.
func newIntrospectionHandler(t *testing.T, calls *atomic.Int64, respond introspectionResponder, configure func(*config.GRPCCallerAuthConfig)) *Handler {
	t.Helper()

	server := startIntrospectionServer(t, calls, respond)
	client := server.Client()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

	a := testCallerConfig(server.URL)
	if configure != nil {
		configure(&a)
	}

	return &Handler{cfg: config.Config{GRPCCallerAuth: a}, callerHTTP: client}
}

func callerContext(ctx context.Context, token string) context.Context {
	return metadata.NewIncomingContext(ctx, metadata.Pairs(bearerAuthHeader, tokenTypeBearer+" "+token))
}

func assertCallerAuthError(t *testing.T, err error, code codes.Code, cause callerAuthCause, httpStatus int) {
	t.Helper()

	if status.Code(err) != code {
		t.Fatalf("expected %s, got %v", code, err)
	}

	if code == codes.OK {
		return
	}

	authErr := asCallerAuthError(err)
	if authErr.cause != cause || authErr.httpStatus != httpStatus {
		t.Fatalf("expected cause=%s http_status=%d, got cause=%s http_status=%d", cause, httpStatus, authErr.cause, authErr.httpStatus)
	}

	if leaked := fmt.Sprintf("%v %s %s", err, authErr.detail, status.Convert(err).Message()); strings.Contains(leaked, testCallerToken) || strings.Contains(leaked, testCallerSecret) {
		t.Fatalf("credential leaked into error: %s", leaked)
	}
}

func TestCallerIntrospectionClassification(t *testing.T) {
	t.Setenv(testCallerSecretEnv, testCallerSecret)

	for _, tc := range []struct {
		name       string
		respond    introspectionResponder
		code       codes.Code
		cause      callerAuthCause
		httpStatus int
	}{
		{"active", respondIntrospectionClaims(nil), codes.OK, "", 0},
		{string(callerCauseInactive), respondIntrospectionClaims(map[string]any{testClaimActive: false}), codes.Unauthenticated, callerCauseInactive, 0},
		{testCaseIssuer, respondIntrospectionClaims(map[string]any{testClaimIssuer: testCallerOtherIssuer}), codes.Unauthenticated, callerCauseClaims, 0},
		{testCaseAudience, respondIntrospectionClaims(map[string]any{testClaimAudience: testCallerForeignAudience}), codes.Unauthenticated, callerCauseClaims, 0},
		{testCallerExpired, respondIntrospectionClaims(map[string]any{testClaimExpiry: time.Now().Add(-time.Minute).Unix()}), codes.Unauthenticated, callerCauseClaims, 0},
		{testCaseNotBefore, respondIntrospectionClaims(map[string]any{testClaimNotBefore: time.Now().Add(time.Minute).Unix()}), codes.Unauthenticated, callerCauseClaims, 0},
		{testCallerScopeKey, respondIntrospectionClaims(map[string]any{testCallerScopeKey: testCallerWriteScope}), codes.PermissionDenied, callerCauseScope, 0},
		{"too-many-requests", respondIntrospectionStatus(http.StatusTooManyRequests), codes.Unavailable, callerCauseHTTPStatus, http.StatusTooManyRequests},
		{"internal-error", respondIntrospectionStatus(http.StatusInternalServerError), codes.Unavailable, callerCauseHTTPStatus, http.StatusInternalServerError},
		{"service-unavailable", respondIntrospectionStatus(http.StatusServiceUnavailable), codes.Unavailable, callerCauseHTTPStatus, http.StatusServiceUnavailable},
		{"bad-request", respondIntrospectionStatus(http.StatusBadRequest), codes.Unavailable, callerCauseHTTPStatus, http.StatusBadRequest},
		{"invalid-request", respondIntrospectionError("invalid_request"), codes.Unavailable, callerCauseHTTPStatus, http.StatusBadRequest},
		{"invalid-client", respondIntrospectionError("invalid_client"), codes.Unavailable, callerCauseConfig, http.StatusBadRequest},
		{"unauthorized-client", respondIntrospectionError("unauthorized_client"), codes.Unavailable, callerCauseConfig, http.StatusBadRequest},
		{"client-unauthorized", respondIntrospectionStatus(http.StatusUnauthorized), codes.Unavailable, callerCauseConfig, http.StatusUnauthorized},
		{"client-forbidden", respondIntrospectionStatus(http.StatusForbidden), codes.Unavailable, callerCauseConfig, http.StatusForbidden},
		{"redirect", respondIntrospectionRedirect, codes.Unavailable, callerCauseHTTPStatus, http.StatusTemporaryRedirect},
		{"broken-json", respondIntrospectionBody("not-json"), codes.Unavailable, callerCauseResponse, 0},
		{"oversized", respondIntrospectionBody(strings.Repeat(" ", maxIntrospectionResponseBytes+1)), codes.Unavailable, callerCauseResponse, 0},
		{"timeout", respondIntrospectionSlowly, codes.Unavailable, callerCauseTimeout, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Only the timeout case uses a short timeout; others must not flake under load.
			handler := newIntrospectionHandler(t, nil, tc.respond, func(a *config.GRPCCallerAuthConfig) {
				if tc.cause == callerCauseTimeout {
					a.Timeout = 50 * time.Millisecond
				}
			})

			err := handler.authenticateCaller(callerContext(context.Background(), testCallerToken), testFullMethodUnary)
			assertCallerAuthError(t, err, tc.code, tc.cause, tc.httpStatus)
		})
	}
}

func TestCallerAuthenticationLocalFailures(t *testing.T) {
	t.Setenv(testCallerSecretEnv, testCallerSecret)

	for _, tc := range []struct {
		name      string
		values    []string
		configure func(*config.GRPCCallerAuthConfig)
		method    string
		code      codes.Code
		cause     callerAuthCause
	}{
		{testCallerMissing, nil, nil, testFullMethodUnary, codes.Unauthenticated, callerCauseMissingBearer},
		{testCaseDuplicate, []string{testCallerBearer, testCallerBearer}, nil, testFullMethodUnary, codes.Unauthenticated, callerCauseMalformedBearer},
		{"scheme", []string{"Basic " + testCallerToken}, nil, testFullMethodUnary, codes.Unauthenticated, callerCauseMalformedBearer},
		{"method", []string{testCallerBearer}, nil, testFullMethodBidiStream, codes.PermissionDenied, callerCauseMethod},
		{"invalid-config", []string{testCallerBearer}, func(a *config.GRPCCallerAuthConfig) { a.Issuer = "" }, testFullMethodUnary, codes.Unavailable, callerCauseConfig},
		{"missing-secret", []string{testCallerBearer}, func(a *config.GRPCCallerAuthConfig) { a.ClientSecretEnv = "TEST_INTROSPECTION_SECRET_UNSET" }, testFullMethodUnary, codes.Unavailable, callerCauseConfig},
		{testCaseBadCA, []string{testCallerBearer}, func(a *config.GRPCCallerAuthConfig) { a.CAFile = "/nonexistent/ca.pem" }, testFullMethodUnary, codes.Unavailable, callerCauseConfig},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := &atomic.Int64{}
			handler := newIntrospectionHandler(t, calls, respondIntrospectionClaims(nil), tc.configure)

			if tc.name == testCaseBadCA {
				handler.callerHTTP = nil
			}

			ctx := metadata.NewIncomingContext(context.Background(), metadata.MD{bearerAuthHeader: tc.values})
			assertCallerAuthError(t, handler.authenticateCaller(ctx, tc.method), tc.code, tc.cause, 0)

			if tc.cause != callerCauseMethod && calls.Load() != 0 {
				t.Fatal("local failure reached the introspection endpoint")
			}
		})
	}
}

func TestCallerIntrospectionTransportError(t *testing.T) {
	t.Setenv(testCallerSecretEnv, testCallerSecret)

	server := httptest.NewTLSServer(http.NotFoundHandler())
	a := testCallerConfig(server.URL)
	handler := &Handler{cfg: config.Config{GRPCCallerAuth: a}, callerHTTP: server.Client()}

	server.Close()

	err := handler.authenticateCaller(callerContext(context.Background(), testCallerToken), testFullMethodUnary)
	assertCallerAuthError(t, err, codes.Unavailable, callerCauseTransport, 0)
}

func TestCallerIntrospectionCallerContext(t *testing.T) {
	t.Setenv(testCallerSecretEnv, testCallerSecret)

	release := make(chan struct{})
	calls := &atomic.Int64{}
	handler := newIntrospectionHandler(t, calls, func(w http.ResponseWriter, r *http.Request) {
		<-release
		respondIntrospectionClaims(nil)(w, r)
	}, nil)
	t.Cleanup(func() { close(release) })

	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	err := handler.authenticateCaller(callerContext(canceled, testCallerToken), testFullMethodUnary)
	assertCallerAuthError(t, err, codes.Canceled, callerCauseCanceled, 0)

	if calls.Load() != 0 {
		t.Fatal("canceled caller started an introspection")
	}

	deadline, cancelDeadline := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelDeadline()

	err = handler.authenticateCaller(callerContext(deadline, testCallerToken), testFullMethodUnary)
	assertCallerAuthError(t, err, codes.DeadlineExceeded, callerCauseDeadline, 0)

	waiting, cancelWaiting := context.WithCancel(context.Background())
	result := make(chan error, 1)

	go func() {
		result <- handler.authenticateCaller(callerContext(waiting, testCallerToken), testFullMethodUnary)
	}()

	cancelWaiting()
	assertCallerAuthError(t, <-result, codes.Canceled, callerCauseCanceled, 0)
}

func TestCallerRejectionTelemetry(t *testing.T) {
	t.Setenv(testCallerSecretEnv, testCallerSecret)

	for _, tc := range []struct {
		name       string
		respond    introspectionResponder
		code       codes.Code
		outcome    string
		cause      callerAuthCause
		level      string
		httpStatus string
	}{
		{string(callerCauseInactive), respondIntrospectionClaims(map[string]any{testClaimActive: false}), codes.Unauthenticated, callerAuthRejected, callerCauseInactive, testLevelWarn, ""},
		{"unavailable", respondIntrospectionStatus(http.StatusServiceUnavailable), codes.Unavailable, callerAuthUnavailable, callerCauseHTTPStatus, testLevelWarn, "503"},
		{"misconfigured", respondIntrospectionStatus(http.StatusUnauthorized), codes.Unavailable, callerAuthUnavailable, callerCauseConfig, "ERROR", "401"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record, metrics := runCallerTelemetryCase(t, tc.respond, tc.code)

			assertLogField(t, record, "msg", "grpc caller rejected")
			assertLogField(t, record, "reason", tc.code.String())
			assertLogField(t, record, "cause", string(tc.cause))
			assertLogField(t, record, "level", tc.level)
			assertLogField(t, record, "http_status", tc.httpStatus)

			if !strings.Contains(metrics, `outcome="`+tc.outcome+`"`) || !strings.Contains(metrics, "doppelgaenger_ingress_requests_total") {
				t.Fatalf("missing %s metric", tc.outcome)
			}
		})
	}
}

func runCallerTelemetryCase(t *testing.T, respond introspectionResponder, want codes.Code) (map[string]string, string) {
	t.Helper()

	logger, logs := newCaptureLogger()

	obs, err := observability.New(config.Config{Observability: config.ObservabilityConfig{PrometheusEnabled: true}}, "test", logger)
	if err != nil {
		t.Fatal(err)
	}

	server := startIntrospectionServer(t, nil, respond)
	cfg := config.Config{GRPCBackendOIDCAuth: config.GRPCBackendOIDCAuthConfig{Enabled: true}, GRPCCallerAuth: testCallerConfig(server.URL)}
	handler := NewHandler(cfg, nil, nil, logger, nil, obs)
	handler.callerHTTP = server.Client()
	conn := newTestClientConn(t, startTestProxyWithHandler(t, handler))
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs(bearerAuthHeader, tokenTypeBearer+" "+testCallerLeakToken))

	var response rawMessage
	if err := conn.Invoke(ctx, testFullMethodUnary, rawMessage("request"), &response); status.Code(err) != want {
		t.Fatal(err)
	}

	record := logs.last()
	if record == nil {
		record = map[string]string{}
	}

	if _, ok := record["http_status"]; !ok {
		record["http_status"] = ""
	}

	recorder := httptest.NewRecorder()
	obs.PrometheusHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	metrics := recorder.Body.String()

	if strings.Contains(fmt.Sprint(record), testCallerLeakToken) || strings.Contains(metrics, testCallerLeakToken) || strings.Contains(fmt.Sprint(record), testCallerSecret) {
		t.Fatal("credential leaked to telemetry")
	}

	return record, metrics
}

func TestRejectCallerRecordsCauseOnRPCContext(t *testing.T) {
	rpcCtx := &grpcRPCContext{}
	handler := &Handler{}

	err := handler.rejectCaller(rpcCtx, callerHTTPStatusError(http.StatusTooManyRequests, ""))
	if status.Code(err) != codes.Unavailable || rpcCtx.outcome != callerAuthUnavailable || rpcCtx.callerAuthCause != string(callerCauseHTTPStatus) || rpcCtx.primaryStatus != codes.Unavailable {
		t.Fatalf("unexpected rejection state: %+v", rpcCtx)
	}

	_ = handler.rejectCaller(rpcCtx, callerContextError(canceledContext()))
	if rpcCtx.outcome != callerAuthCanceled || rpcCtx.callerAuthCause != string(callerCauseCanceled) {
		t.Fatalf("unexpected cancel state: %+v", rpcCtx)
	}
}

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	return ctx
}

func TestCallerTechnicalRejectionLogsAreRateLimited(t *testing.T) {
	logger, logs := newCaptureLogger()
	handler := &Handler{logger: logger}

	for range 3 {
		_ = handler.rejectCaller(&grpcRPCContext{}, callerHTTPStatusError(http.StatusServiceUnavailable, ""))
		_ = handler.rejectCaller(&grpcRPCContext{}, callerRejected(callerCauseInactive))
		// Distinct misconfigurations are each reported once.
		_ = handler.rejectCaller(&grpcRPCContext{}, callerMisconfigured("first misconfiguration", nil))
		_ = handler.rejectCaller(&grpcRPCContext{}, callerMisconfigured("second misconfiguration", nil))
	}

	logs.mu.Lock()
	count := len(logs.records)
	logs.mu.Unlock()

	// One record per technical key plus every caller-fault record.
	if count != 6 {
		t.Fatalf("expected 6 log records, got %d", count)
	}

	var limiter callerLogLimiter

	now := time.Now()
	for range 3 {
		limiter.allow(string(callerCauseTimeout), now)
	}

	if allowed, suppressed := limiter.allow(string(callerCauseTimeout), now.Add(callerLogInterval)); !allowed || suppressed != 2 {
		t.Fatalf("allowed=%v suppressed=%d", allowed, suppressed)
	}

	if allowed, _ := limiter.allow(string(callerCauseTransport), now); !allowed {
		t.Fatal("causes share one limit")
	}
}

func TestCallerIntrospectionMetrics(t *testing.T) {
	t.Setenv(testCallerSecretEnv, testCallerSecret)

	obs, err := observability.New(config.Config{Observability: config.ObservabilityConfig{PrometheusEnabled: true}}, "test", nil)
	if err != nil {
		t.Fatal(err)
	}

	handler := newIntrospectionHandler(t, nil, respondIntrospectionClaims(nil), func(a *config.GRPCCallerAuthConfig) {
		a.IntrospectionCacheTTL = time.Minute
		a.IntrospectionCacheMaxEntries = 1
	})
	handler.observability = obs

	for range 2 {
		if err := authenticateTestCaller(handler, testCallerToken, testFullMethodUnary); err != nil {
			t.Fatal(err)
		}
	}

	recorder := httptest.NewRecorder()
	obs.PrometheusHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	metrics := recorder.Body.String()

	for _, want := range []string{
		`doppelgaenger_grpc_caller_introspections_total{result="miss"} 1`,
		`doppelgaenger_grpc_caller_introspections_total{result="hit"} 1`,
		"doppelgaenger_grpc_caller_introspection_flights 0",
	} {
		if !strings.Contains(metrics, want) {
			t.Fatalf("missing %q in metrics", want)
		}
	}
}

func TestCallerIntrospectionSpan(t *testing.T) {
	t.Setenv(testCallerSecretEnv, testCallerSecret)

	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(collector.Close)

	for _, tracing := range []bool{false, true} {
		t.Run(fmt.Sprint("tracing=", tracing), func(t *testing.T) {
			ratio := 1.0

			obs, err := observability.New(config.Config{Observability: config.ObservabilityConfig{
				OTelEnabled: tracing, OTelTracesEnabled: tracing, OTLPEndpoint: collector.URL, OTLPInsecure: true, OTelSampleRatio: &ratio,
			}}, "test", nil)
			if err != nil {
				t.Fatal(err)
			}

			t.Cleanup(func() { _ = obs.Shutdown(context.Background()) })

			handler := newIntrospectionHandler(t, nil, respondIntrospectionStatus(http.StatusServiceUnavailable), nil)
			handler.observability = obs

			parentCtx, parent := sdktrace.NewTracerProvider().Tracer("test").Start(context.Background(), "caller")
			defer parent.End()

			result := handler.runIntrospectionFlight(parentCtx, handler.callerRuntime(), digestCallerToken(testCallerToken), testCallerToken)

			if !parent.IsRecording() {
				t.Fatal("introspection ended the caller span")
			}

			if result.span.IsValid() != tracing || (tracing && result.span.SpanID() == parent.SpanContext().SpanID()) {
				t.Fatalf("unexpected introspection span: %+v", result.span)
			}

			assertCallerAuthError(t, result.err, codes.Unavailable, callerCauseHTTPStatus, http.StatusServiceUnavailable)
		})
	}
}

func TestCallerBearerLengthLimit(t *testing.T) {
	if !validBearerValue(strings.Repeat("a", maxBearerTokenBytes)) || validBearerValue(strings.Repeat("a", maxBearerTokenBytes+1)) {
		t.Fatal("unexpected Bearer length limit")
	}

	ctx := callerContext(context.Background(), strings.Repeat("a", maxBearerTokenBytes+1))
	if _, err := callerBearerToken(ctx); asCallerAuthError(err).cause != callerCauseMalformedBearer {
		t.Fatalf("oversized Bearer: %v", err)
	}
}

// A flight answered by the cache (double-check) is reported as a hit without a request.
func TestCallerIntrospectionFlightCacheDoubleCheck(t *testing.T) {
	t.Setenv(testCallerSecretEnv, testCallerSecret)

	calls := &atomic.Int64{}
	handler := newIntrospectionHandler(t, calls, respondIntrospectionClaims(nil), func(a *config.GRPCCallerAuthConfig) {
		a.IntrospectionCacheTTL = time.Minute
		a.IntrospectionCacheMaxEntries = 1
	})
	runtime := handler.callerRuntime()
	key := digestCallerToken(testCallerToken)
	runtime.cache.put(key, validTestClaims(time.Now().Add(time.Minute)), time.Now())

	if result := handler.runIntrospectionFlight(context.Background(), runtime, key, testCallerToken); !result.cached || result.err != nil || calls.Load() != 0 {
		t.Fatalf("double-check missed the cache: %+v calls=%d", result, calls.Load())
	}
}
