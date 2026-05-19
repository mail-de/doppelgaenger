package proxy

import (
	"bytes"
	"context"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"doppelgaenger/internal/compare"
	"doppelgaenger/internal/config"
	"doppelgaenger/internal/mapping"
	"doppelgaenger/internal/pathrules"
	"doppelgaenger/internal/protocol"
	"doppelgaenger/internal/ratelimit"
)

const (
	testGlobalHeader    = "X-Global"
	testInboundHost     = "example.com"
	testHeadersRuleName = "headers"
	testRuleHeader      = "X-Rule"
	testSkippedHeader   = "X-Skip"
	testSkippedValue    = "skip"
	testAuthorization   = "Authorization"
	testBasicPrimary    = "Basic primary-secret"
	testBasicShadow     = "Basic shadow-secret"
	testMetricsRegex    = "^/metrics$"
)

func TestEnsureRequestIDPreservesHeader(t *testing.T) {
	h := &Handler{rng: rand.New(rand.NewSource(1))}
	header := http.Header{}
	header.Set("X-Request-ID", "existing")

	id, generated := h.ensureRequestID(header)
	if generated {
		t.Fatalf("expected existing request ID to be reused")
	}

	if id != "existing" {
		t.Fatalf("expected request ID to be preserved, got %q", id)
	}
}

func TestEnsureRequestIDGenerates(t *testing.T) {
	h := &Handler{rng: rand.New(rand.NewSource(1))}
	header := http.Header{}

	id, generated := h.ensureRequestID(header)
	if !generated {
		t.Fatalf("expected request ID to be generated")
	}

	if id == "" {
		t.Fatalf("expected generated request ID to be non-empty")
	}

	if header.Get("X-Request-ID") == "" {
		t.Fatalf("expected request ID to be set in header")
	}
}

func TestWritePrimaryResponsePreservesRedirectHeaders(t *testing.T) {
	const (
		redirectLocation = "/oidc/authorize/de"
		redirectCookie   = "nauthilus=abc; Path=/; HttpOnly"
	)

	gin.SetMode(gin.TestMode)

	handler := &Handler{
		cfg: config.Config{ForwardResponseHeaders: []string{testAuthStatus}},
	}
	response := protocol.Response{
		Status: http.StatusFound,
		Header: http.Header{
			testAuthStatus:    []string{testHeaderOK},
			"Location":        []string{redirectLocation},
			"Set-Cookie":      []string{redirectCookie},
			testSkippedHeader: []string{testSkippedValue},
		},
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	handler.writePrimaryResponse(c, "request-id", response)

	if got := c.Writer.Status(); got != http.StatusFound {
		t.Fatalf("expected status 302, got %d", got)
	}

	if got := rec.Header().Get("Location"); got != redirectLocation {
		t.Fatalf("expected Location %s, got %q", redirectLocation, got)
	}

	if got := rec.Header().Values("Set-Cookie"); len(got) != 1 || got[0] != redirectCookie {
		t.Fatalf("expected Set-Cookie to be preserved, got %#v", got)
	}

	if got := rec.Header().Get(testAuthStatus); got != testHeaderOK {
		t.Fatalf("expected %s %q, got %q", testAuthStatus, testHeaderOK, got)
	}

	if got := rec.Header().Get(testSkippedHeader); got != "" {
		t.Fatalf("expected %s not to be forwarded, got %q", testSkippedHeader, got)
	}
}

func TestWritePrimaryResponsePreservesCompressedResponseHeaders(t *testing.T) {
	const (
		contentEncoding = "Content-Encoding"
		contentType     = "Content-Type"
		vary            = "Vary"
		brEncoding      = "br"
		varyEncoding    = "Cookie, Accept-Encoding"
	)

	gin.SetMode(gin.TestMode)

	body := []byte{0x1b, 0x35, 0x1e, 0x00, 0xc4, 0xff}
	handler := &Handler{
		cfg: config.Config{ForwardResponseHeaders: []string{contentEncoding, contentType, vary}},
	}
	response := protocol.Response{
		Status: http.StatusOK,
		Header: http.Header{
			contentEncoding:   []string{brEncoding},
			contentType:       []string{"text/html; charset=utf-8"},
			vary:              []string{varyEncoding},
			testSkippedHeader: []string{testSkippedValue},
		},
		Body: body,
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	handler.writePrimaryResponse(c, "request-id", response)

	if got := c.Writer.Status(); got != http.StatusOK {
		t.Fatalf("expected status 200, got %d", got)
	}

	if got := rec.Header().Get(contentEncoding); got != brEncoding {
		t.Fatalf("expected %s br, got %q", contentEncoding, got)
	}

	if got := rec.Header().Get(vary); got != varyEncoding {
		t.Fatalf("expected %s to be preserved, got %q", vary, got)
	}

	if got := rec.Body.Bytes(); !bytes.Equal(got, body) {
		t.Fatalf("expected compressed body bytes %#v, got %#v", body, got)
	}

	if got := rec.Header().Get(testSkippedHeader); got != "" {
		t.Fatalf("expected %s not to be forwarded, got %q", testSkippedHeader, got)
	}
}

func TestShouldShadowNoRulesPreservesSamplingAndForceHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: config.Config{
			ShadowForceHeader:   testShadowForceHeader,
			ShadowSamplePercent: 0,
		},
		shadowLimiter: staticLimiter{allow: false},
		rng:           rand.New(rand.NewSource(1)),
	}
	decision := pathrules.Decision{ShadowMode: pathrules.ShadowModeInherit}

	result := h.shouldShadow(newShadowContext(http.MethodGet, testAuthPath, nil), decision)
	if result.doShadow || result.forced || result.skipReason != "" {
		t.Fatalf("expected no shadow without sampling or force header, got %#v", result)
	}

	header := http.Header{}
	header.Set(testShadowForceHeader, "1")

	result = h.shouldShadow(newShadowContext(http.MethodGet, testAuthPath, header), decision)
	if !result.doShadow || !result.forced || result.skipReason != "" {
		t.Fatalf("expected force header to bypass limiter without rules, got %#v", result)
	}

	h.cfg.ShadowSamplePercent = 100
	h.shadowLimiter = nil

	result = h.shouldShadow(newShadowContext(http.MethodGet, testAuthPath, nil), decision)
	if !result.doShadow || result.forced || result.skipReason != "" {
		t.Fatalf("expected sampling to enable shadow without rules, got %#v", result)
	}
}

func TestHandlePathRuleShadowNeverBlocksForceHeader(t *testing.T) {
	harness := newRuntimeHarness(t, config.Config{
		ShadowForceHeader:   testShadowForceHeader,
		ShadowSamplePercent: 100,
		CompareMode:         compare.ModeNginx,
		PathRules: []config.PathRule{
			{Name: "blocked", Match: testAuthPathRegex, Shadow: string(pathrules.ShadowModeNever), Compare: string(pathrules.CompareDecisionOff)},
		},
	}, nil)

	header := http.Header{}
	header.Set(testShadowForceHeader, "1")

	w := performRuntimeRequest(t, harness.handler, http.MethodGet, testAuthPath, header)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	logFields := waitForRuntimeLog(t, harness.logs)
	if harness.adapter.shadowSendCount() != 0 {
		t.Fatalf("expected shadow request not to start")
	}

	assertLogField(t, logFields, "path_rule", "blocked")
	assertLogField(t, logFields, "shadow_mode", string(pathrules.ShadowModeNever))
	assertLogField(t, logFields, "shadow_forced", true)
	assertLogField(t, logFields, "shadow_started", false)
	assertLogField(t, logFields, "shadow_skip_reason", pathrules.SkipReasonPathRule)
	assertLogField(t, logFields, "compare_enabled", false)
	assertLogField(t, logFields, "compare_skip_reason", pathrules.SkipReasonPathRule)
}

func TestHandleUnmatchedPathIsPrimaryOnlyWhenRulesConfigured(t *testing.T) {
	harness := newRuntimeHarness(t, config.Config{
		ShadowSamplePercent: 100,
		CompareMode:         compare.ModeNginx,
		PathRules: []config.PathRule{
			{Name: "matched", Match: "^/matched$", Shadow: string(pathrules.ShadowModeAuto), Compare: string(pathrules.CompareDecisionOn)},
		},
	}, nil)

	w := performRuntimeRequest(t, harness.handler, http.MethodGet, "/other", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	logFields := waitForRuntimeLog(t, harness.logs)
	if harness.adapter.shadowSendCount() != 0 {
		t.Fatalf("expected unmatched path not to start shadow")
	}

	assertLogField(t, logFields, "path_rule", "")
	assertLogField(t, logFields, "shadow_mode", string(pathrules.ShadowModeNever))
	assertLogField(t, logFields, "shadow_skip_reason", pathrules.SkipReasonPathUnmatched)
	assertLogField(t, logFields, "compare_enabled", false)
	assertLogField(t, logFields, "compare_skip_reason", pathrules.SkipReasonPathUnmatched)
}

func TestShouldShadowAutoAllowsSamplingAndForceHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: config.Config{
			ShadowForceHeader:   testShadowForceHeader,
			ShadowSamplePercent: 100,
		},
		rng: rand.New(rand.NewSource(1)),
	}
	decision := pathrules.Decision{ShadowMode: pathrules.ShadowModeAuto}

	result := h.shouldShadow(newShadowContext(http.MethodGet, testAuthPath, nil), decision)
	if !result.doShadow || result.forced || result.skipReason != "" {
		t.Fatalf("expected shadow:auto to allow sampled request, got %#v", result)
	}

	h.cfg.ShadowSamplePercent = 0
	header := http.Header{}
	header.Set(testShadowForceHeader, "1")

	result = h.shouldShadow(newShadowContext(http.MethodGet, testAuthPath, header), decision)
	if !result.doShadow || !result.forced || result.skipReason != "" {
		t.Fatalf("expected shadow:auto to allow force header request, got %#v", result)
	}
}

func TestShouldShadowAlwaysBypassesSamplingButRespectsLimiter(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: config.Config{
			ShadowSamplePercent: 0,
		},
		shadowLimiter: staticLimiter{allow: true},
		rng:           rand.New(rand.NewSource(1)),
	}
	decision := pathrules.Decision{ShadowMode: pathrules.ShadowModeAlways}

	result := h.shouldShadow(newShadowContext(http.MethodGet, testAuthPath, nil), decision)
	if !result.doShadow || result.forced || result.skipReason != "" {
		t.Fatalf("expected shadow:always to start without sampling, got %#v", result)
	}

	h.shadowLimiter = staticLimiter{allow: false}

	result = h.shouldShadow(newShadowContext(http.MethodGet, testAuthPath, nil), decision)
	if result.doShadow || result.skipReason != shadowSkipReasonRateLimited {
		t.Fatalf("expected shadow:always to respect rate limiter, got %#v", result)
	}
}

func TestHandleCompareOffRunsShadowAndSkipsComparison(t *testing.T) {
	harness := newRuntimeHarness(t, config.Config{
		ShadowSamplePercent: 100,
		CompareMode:         compare.ModeNginx,
		PathRules: []config.PathRule{
			{Name: "no-compare", Match: testAuthPathRegex, Shadow: string(pathrules.ShadowModeAlways), Compare: string(pathrules.CompareDecisionOff)},
		},
	}, nil)
	harness.adapter.primaryResponse.Body = []byte(`{"status":"primary"}`)
	harness.adapter.shadowResponse.Body = []byte(`{"status":"shadow"}`)

	w := performRuntimeRequest(t, harness.handler, http.MethodGet, testAuthPath, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	logFields := waitForRuntimeLog(t, harness.logs)
	if harness.adapter.shadowSendCount() != 1 {
		t.Fatalf("expected shadow request to run once")
	}

	assertLogField(t, logFields, "path_rule", "no-compare")
	assertLogField(t, logFields, "shadow_started", true)
	assertLogField(t, logFields, "compare_enabled", false)
	assertLogField(t, logFields, "compare_skip_reason", pathrules.SkipReasonPathRule)
	assertLogField(t, logFields, "diff", false)
}

func TestHandlePathRuleCompareModeJSON(t *testing.T) {
	harness := newRuntimeHarness(t, config.Config{
		ShadowSamplePercent: 100,
		CompareMode:         compare.ModeNginx,
		CompareHeaders:      []string{},
		PathRules: []config.PathRule{
			{Name: "json", Match: testAuthPathRegex, Shadow: string(pathrules.ShadowModeAlways), Compare: string(pathrules.CompareDecisionOn), CompareMode: compare.ModeJSON},
		},
	}, nil)
	harness.adapter.primaryResponse.Body = []byte(`{"ok":true}`)
	harness.adapter.shadowResponse.Body = []byte(`{"ok":false}`)

	performRuntimeRequest(t, harness.handler, http.MethodPost, testAuthPath, nil)

	logFields := waitForRuntimeLog(t, harness.logs)
	assertLogField(t, logFields, "compare_enabled", true)
	assertLogField(t, logFields, "compare_skip_reason", "")
	assertLogField(t, logFields, "compare_mode", compare.ModeJSON)
	assertLogField(t, logFields, "body_diff", true)
	assertLogField(t, logFields, "diff", true)
}

func TestHandlePathRuleCompareHeadersOverride(t *testing.T) {
	harness := newRuntimeHarness(t, config.Config{
		ShadowSamplePercent: 100,
		CompareMode:         compare.ModeNginx,
		CompareHeaders:      []string{testGlobalHeader},
		PathRules: []config.PathRule{
			{Name: testHeadersRuleName, Match: testAuthPathRegex, Shadow: string(pathrules.ShadowModeAlways), Compare: string(pathrules.CompareDecisionOn), CompareHeaders: []string{testRuleHeader}},
		},
	}, nil)
	harness.adapter.primaryResponse.Header.Set(testGlobalHeader, "primary")
	harness.adapter.shadowResponse.Header.Set(testGlobalHeader, "shadow")
	harness.adapter.primaryResponse.Header.Set(testRuleHeader, "same")
	harness.adapter.shadowResponse.Header.Set(testRuleHeader, "same")

	performRuntimeRequest(t, harness.handler, http.MethodGet, testAuthPath, nil)

	logFields := waitForRuntimeLog(t, harness.logs)
	assertLogField(t, logFields, "diff", false)

	primaryHeaders, ok := logFields["primary_headers"].(map[string]string)
	if !ok {
		t.Fatalf("expected primary_headers map, got %#v", logFields["primary_headers"])
	}

	if _, ok := primaryHeaders[testGlobalHeader]; ok {
		t.Fatalf("expected per-rule headers to replace global headers, got %#v", primaryHeaders)
	}

	if primaryHeaders[testRuleHeader] != "same" {
		t.Fatalf("expected X-Rule to be compared, got %#v", primaryHeaders)
	}
}

func TestHandlePathRuleCompareHeadersExplicitEmpty(t *testing.T) {
	harness := newRuntimeHarness(t, config.Config{
		ShadowSamplePercent: 100,
		CompareMode:         compare.ModeNginx,
		CompareHeaders:      []string{testGlobalHeader},
		PathRules: []config.PathRule{
			{Name: testHeadersRuleName, Match: testAuthPathRegex, Shadow: string(pathrules.ShadowModeAlways), Compare: string(pathrules.CompareDecisionOn), CompareHeaders: []string{}},
		},
	}, nil)
	harness.adapter.primaryResponse.Header.Set(testGlobalHeader, "primary")
	harness.adapter.shadowResponse.Header.Set(testGlobalHeader, "shadow")

	performRuntimeRequest(t, harness.handler, http.MethodGet, testAuthPath, nil)

	logFields := waitForRuntimeLog(t, harness.logs)
	assertLogField(t, logFields, "diff", false)

	primaryHeaders, ok := logFields["primary_headers"].(map[string]string)
	if !ok {
		t.Fatalf("expected primary_headers map, got %#v", logFields["primary_headers"])
	}

	if len(primaryHeaders) != 0 {
		t.Fatalf("expected explicit empty compare_headers to compare no headers, got %#v", primaryHeaders)
	}
}

func TestHandleAddsPathRuleRequestHeadersToBackendEvents(t *testing.T) {
	harness := newRuntimeHarness(t, config.Config{
		ShadowSamplePercent: 100,
		CompareMode:         compare.ModeNginx,
		PathRules: []config.PathRule{
			{
				Name:                  "metrics",
				Match:                 testMetricsRegex,
				Shadow:                string(pathrules.ShadowModeAlways),
				Compare:               string(pathrules.CompareDecisionOff),
				PrimaryRequestHeaders: map[string]string{testAuthorization: testBasicPrimary},
				ShadowRequestHeaders:  map[string]string{testAuthorization: testBasicShadow},
			},
		},
	}, nil)

	w := performRuntimeRequest(t, harness.handler, http.MethodGet, "/metrics", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	waitForRuntimeLog(t, harness.logs)

	primaryEvent, ok := harness.adapter.primaryEvent()
	if !ok {
		t.Fatalf("expected primary event to be recorded")
	}

	if primaryEvent.PrimaryRequestHeaders[testAuthorization] != testBasicPrimary {
		t.Fatalf("expected path rule primary request headers on primary event, got %#v", primaryEvent.PrimaryRequestHeaders)
	}

	shadowEvent, ok := harness.adapter.shadowEvent()
	if !ok {
		t.Fatalf("expected shadow event to be recorded")
	}

	if shadowEvent.ShadowRequestHeaders[testAuthorization] != testBasicShadow {
		t.Fatalf("expected path rule shadow request headers on shadow event, got %#v", shadowEvent.ShadowRequestHeaders)
	}
}

func TestHandlePreparesReverseProxyHeaders(t *testing.T) {
	harness := newRuntimeHarness(t, config.Config{ShadowSamplePercent: 0}, nil)

	header := http.Header{}
	header.Set("Connection", "X-Hop")
	header.Set("X-Hop", "drop")
	header.Set("X-Forwarded-For", "198.51.100.10")

	w := performRuntimeRequest(t, harness.handler, http.MethodGet, testAuthPath, header)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	event, ok := harness.adapter.primaryEvent()
	if !ok {
		t.Fatalf("expected primary event to be captured")
	}

	if event.Host != testInboundHost {
		t.Fatalf("expected inbound host to be preserved, got %q", event.Host)
	}

	if got := event.Header.Get("X-Forwarded-Host"); got != testInboundHost {
		t.Fatalf("expected X-Forwarded-Host example.com, got %q", got)
	}

	if got := event.Header.Get("X-Forwarded-Proto"); got != protocolHTTP {
		t.Fatalf("expected X-Forwarded-Proto http, got %q", got)
	}

	if got := event.Header.Get("X-Forwarded-For"); got != "198.51.100.10, 192.0.2.1" {
		t.Fatalf("expected appended X-Forwarded-For, got %q", got)
	}

	if got := event.Header.Get("X-Real-IP"); got != "198.51.100.10" {
		t.Fatalf("expected X-Real-IP from original client, got %q", got)
	}

	if got := event.Header.Get("Connection"); got != "" {
		t.Fatalf("expected Connection header to be removed, got %q", got)
	}

	if got := event.Header.Get("X-Hop"); got != "" {
		t.Fatalf("expected connection-listed header to be removed, got %q", got)
	}
}

type staticLimiter struct {
	allow bool
}

func (l staticLimiter) Allow() bool {
	return l.allow
}

type runtimeHarness struct {
	handler *Handler
	adapter *runtimeTestAdapter
	logs    chan map[string]any
}

func newRuntimeHarness(t *testing.T, cfg config.Config, limiter ratelimit.Limiter) runtimeHarness {
	t.Helper()

	if cfg.CompareMode == "" {
		cfg.CompareMode = compare.ModeNginx
	}

	if cfg.ForwardResponseHeaders == nil {
		cfg.ForwardResponseHeaders = []string{testAuthStatus}
	}

	logHandler := &captureLogHandler{records: make(chan map[string]any, 10)}
	logger := slog.New(logHandler)

	resolver, err := pathrules.NewResolver(cfg.PathRules)
	if err != nil {
		t.Fatalf("expected path rule resolver to build: %v", err)
	}

	registry, err := compare.NewRegistry(cfg, logger)
	if err != nil {
		t.Fatalf("expected HTTP compare registry to build: %v", err)
	}

	globalComparator, err := compare.NewComparator(cfg, logger)
	if err != nil {
		t.Fatalf("expected global comparator to build: %v", err)
	}

	adapter := newRuntimeTestAdapter()
	handler := &Handler{
		cfg:              cfg,
		adapter:          adapter,
		runner:           protocol.Runner{Comparator: protocol.HTTPComparator{Comparator: globalComparator}, ShadowTimeout: cfg.ShadowTimeout},
		shadowLimiter:    limiter,
		pathMapper:       mapping.DirectMapper{},
		pathRuleResolver: resolver,
		httpComparators:  registry,
		logger:           logger,
		rng:              rand.New(rand.NewSource(1)),
	}

	return runtimeHarness{
		handler: handler,
		adapter: adapter,
		logs:    logHandler.records,
	}
}

func newShadowContext(method, path string, header http.Header) *gin.Context {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	req := httptest.NewRequest(method, path, nil)
	if header != nil {
		req.Header = header.Clone()
	}

	c.Request = req

	return c
}

func performRuntimeRequest(t *testing.T, handler *Handler, method, path string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	req := httptest.NewRequest(method, path, nil)
	if header != nil {
		req.Header = header.Clone()
	}

	c.Request = req
	c.Params = gin.Params{{Key: testPathParamKey, Value: path}}

	handler.Handle(c)

	return w
}

func waitForRuntimeLog(t *testing.T, logs <-chan map[string]any) map[string]any {
	t.Helper()

	select {
	case fields := <-logs:
		return fields
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for handler log")
	}

	return nil
}

func assertLogField(t *testing.T, fields map[string]any, key string, expected any) {
	t.Helper()

	value, ok := fields[key]
	if !ok {
		t.Fatalf("expected log field %q to be present in %#v", key, fields)
	}

	if value != expected {
		t.Fatalf("expected log field %q to be %#v, got %#v", key, expected, value)
	}
}

type captureLogHandler struct {
	records chan map[string]any
	attrs   []slog.Attr
}

func (h *captureLogHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (h *captureLogHandler) Handle(_ context.Context, record slog.Record) error {
	fields := make(map[string]any, record.NumAttrs()+len(h.attrs)+1)
	fields["msg"] = record.Message

	for _, attr := range h.attrs {
		fields[attr.Key] = slogValue(attr.Value)
	}

	record.Attrs(func(attr slog.Attr) bool {
		fields[attr.Key] = slogValue(attr.Value)

		return true
	})

	h.records <- fields

	return nil
}

func (h *captureLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := &captureLogHandler{
		records: h.records,
		attrs:   make([]slog.Attr, 0, len(h.attrs)+len(attrs)),
	}
	next.attrs = append(next.attrs, h.attrs...)
	next.attrs = append(next.attrs, attrs...)

	return next
}

func (h *captureLogHandler) WithGroup(string) slog.Handler {
	return h
}

func slogValue(value slog.Value) any {
	switch value.Kind() {
	case slog.KindAny:
		return value.Any()
	case slog.KindBool:
		return value.Bool()
	case slog.KindDuration:
		return value.Duration()
	case slog.KindFloat64:
		return value.Float64()
	case slog.KindInt64:
		return value.Int64()
	case slog.KindString:
		return value.String()
	case slog.KindUint64:
		return value.Uint64()
	default:
		return value.String()
	}
}

type runtimeTestAdapter struct {
	mu sync.Mutex

	primaryResponse protocol.Response
	shadowResponse  protocol.Response
	primaryEvents   []protocol.Event
	shadowEvents    []protocol.Event
	shadowSends     int
}

func newRuntimeTestAdapter() *runtimeTestAdapter {
	return &runtimeTestAdapter{
		primaryResponse: protocol.Response{
			Status:   http.StatusOK,
			Proto:    testHTTPProto,
			Selected: testPrimaryTarget,
			Header:   http.Header{testAuthStatus: []string{testHeaderOK}},
			Body:     []byte("ok"),
		},
		shadowResponse: protocol.Response{
			Status:   http.StatusOK,
			Proto:    testHTTPProto,
			Selected: testShadowTarget,
			Header:   http.Header{testAuthStatus: []string{testHeaderOK}},
			Body:     []byte("ok"),
		},
	}
}

func (a *runtimeTestAdapter) Protocol() string {
	return protocolHTTP
}

func (a *runtimeTestAdapter) NewSession(ctx context.Context, target protocol.Target) (protocol.TestSession, error) {
	return &runtimeTestSession{adapter: a, target: target, ctx: ctx}, nil
}

func (a *runtimeTestAdapter) recordEvent(target protocol.Target, event protocol.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if target == protocol.TargetPrimary {
		a.primaryEvents = append(a.primaryEvents, cloneProtocolEvent(event))
	}

	if target == protocol.TargetShadow {
		a.shadowEvents = append(a.shadowEvents, cloneProtocolEvent(event))
		a.shadowSends++
	}
}

func (a *runtimeTestAdapter) primaryEvent() (protocol.Event, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.primaryEvents) == 0 {
		return protocol.Event{}, false
	}

	return cloneProtocolEvent(a.primaryEvents[len(a.primaryEvents)-1]), true
}

func (a *runtimeTestAdapter) shadowEvent() (protocol.Event, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.shadowEvents) == 0 {
		return protocol.Event{}, false
	}

	return cloneProtocolEvent(a.shadowEvents[len(a.shadowEvents)-1]), true
}

func (a *runtimeTestAdapter) shadowSendCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.shadowSends
}

type runtimeTestSession struct {
	adapter *runtimeTestAdapter
	target  protocol.Target
	ctx     context.Context
}

func (s *runtimeTestSession) Send(event protocol.Event) error {
	s.adapter.recordEvent(s.target, event)

	return nil
}

func (s *runtimeTestSession) Receive() (protocol.Response, error) {
	select {
	case <-s.ctx.Done():
		return protocol.Response{Err: s.ctx.Err()}, s.ctx.Err()
	default:
	}

	if s.target == protocol.TargetShadow {
		return cloneProtocolResponse(s.adapter.shadowResponse), s.adapter.shadowResponse.Err
	}

	return cloneProtocolResponse(s.adapter.primaryResponse), s.adapter.primaryResponse.Err
}

func (s *runtimeTestSession) Close() error {
	return nil
}

func cloneProtocolResponse(response protocol.Response) protocol.Response {
	response.Header = response.Header.Clone()
	response.Body = append([]byte(nil), response.Body...)

	return response
}

func cloneProtocolEvent(event protocol.Event) protocol.Event {
	event.Header = event.Header.Clone()
	event.Body = append([]byte(nil), event.Body...)
	event.Payload = append([]byte(nil), event.Payload...)
	event.PrimaryRequestHeaders = cloneStringMap(event.PrimaryRequestHeaders)
	event.ShadowRequestHeaders = cloneStringMap(event.ShadowRequestHeaders)

	return event
}
