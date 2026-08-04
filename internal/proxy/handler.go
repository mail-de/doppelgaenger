// Package proxy handles incoming HTTP proxy requests.
package proxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"

	"doppelgaenger/internal/backend"
	"doppelgaenger/internal/compare"
	"doppelgaenger/internal/config"
	"doppelgaenger/internal/headers"
	"doppelgaenger/internal/mapping"
	"doppelgaenger/internal/observability"
	"doppelgaenger/internal/pathrules"
	"doppelgaenger/internal/protocol"
	"doppelgaenger/internal/ratelimit"
)

const (
	protocolHTTP                = "http"
	shadowNotStarted            = "shadow_not_started"
	shadowSkipReasonRateLimited = "rate_limited"
	compareSkipReasonNoShadow   = "no_shadow"
	headerSessionID             = "X-Session-ID"
	headerLocation              = "Location"
	headerSetCookie             = "Set-Cookie"
)

// Handler handles incoming proxy requests.
type Handler struct {
	cfg              config.Config
	adapter          protocol.Adapter
	runner           protocol.Runner
	shadowLimiter    ratelimit.Limiter
	pathMapper       mapping.PathMapper
	pathRuleResolver *pathrules.Resolver
	httpComparators  *compare.Registry
	logger           *slog.Logger
	observability    *observability.Observability

	requestID atomic.Uint64
	randMu    sync.Mutex
	rng       *rand.Rand
}

// HandlerDeps describes dependencies needed for Handler.
type HandlerDeps struct {
	fx.In
	Config           config.Config
	Adapter          protocol.Adapter
	Runner           protocol.Runner
	ShadowLimiter    ratelimit.Limiter
	PathMapper       mapping.PathMapper
	PathRuleResolver *pathrules.Resolver
	HTTPComparators  *compare.Registry
	Logger           *slog.Logger
	Observability    *observability.Observability `optional:"true"`
}

type httpLogPayload struct {
	ctx               context.Context
	reqID             uint64
	traceID           string
	corrID            string
	corrGenerated     bool
	remoteAddr        string
	method            string
	path              string
	rawQuery          string
	doShadow          bool
	forcedShadow      bool
	shadowStarted     bool
	pathRule          string
	shadowMode        string
	shadowSkipReason  string
	compareEnabled    bool
	compareMode       string
	compareHeaders    []string
	compareSkipReason string
	primaryRes        protocol.Response
	shadowRes         protocol.Response
	shadowErr         string
	compareResult     protocol.CompareResult
	compareErr        error
}

type httpRequestContext struct {
	reqID      uint64
	start      time.Time
	method     string
	path       string
	rawQuery   string
	host       string
	remoteAddr string
}

type shadowDecision struct {
	doShadow   bool
	forced     bool
	skipReason string
}

type comparisonDecision struct {
	enabled    bool
	mode       string
	headers    []string
	skipReason string
}

// NewHandler constructs a proxy handler with its dependencies.
func NewHandler(deps HandlerDeps) *Handler {
	return &Handler{
		cfg:              deps.Config,
		adapter:          deps.Adapter,
		runner:           deps.Runner,
		shadowLimiter:    deps.ShadowLimiter,
		pathMapper:       deps.PathMapper,
		pathRuleResolver: deps.PathRuleResolver,
		httpComparators:  deps.HTTPComparators,
		logger:           deps.Logger,
		observability:    deps.Observability,
		rng:              rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Handle processes a single incoming request.
func (h *Handler) Handle(c *gin.Context) {
	request := h.newHTTPRequestContext(c)
	pathDecision := h.resolvePathRule(request)

	var spanErr error

	outcome := observability.OutcomeOK
	shadowEnabledLabel := observability.BoolLabel(false)
	shadowStartedLabel := observability.BoolLabel(false)

	requestCtx, traceID, finishObservation := h.startHTTPObservation(
		c,
		request,
		&outcome,
		&shadowEnabledLabel,
		&shadowStartedLabel,
		&spanErr,
	)
	defer finishObservation()

	body, ok := h.readRequestBody(c, &outcome, &spanErr)
	if !ok {
		return
	}

	hdr := headers.Clone(c.Request.Header)

	primaryPath, shadowPath, ok := h.mapRequestPaths(c, request, traceID, &outcome, &spanErr)
	if !ok {
		return
	}

	corrID, corrGenerated := h.ensureRequestID(hdr)

	prepareForwardedHeaders(c.Request, hdr)

	shadow := h.shouldShadow(c, pathDecision)
	shadowEnabledLabel = observability.BoolLabel(shadow.doShadow)
	comparison := h.resolveComparison(pathDecision, shadow)

	event := request.event(requestCtx, hdr, body, primaryPath, shadowPath, pathDecision.PrimaryRequestHeaders, pathDecision.ShadowRequestHeaders)

	primaryRes, ok := h.callPrimary(c, event, request, traceID, corrID, &outcome, &spanErr)
	if !ok {
		return
	}

	h.writePrimaryResponse(c, corrID, primaryRes)

	payload := request.payload(requestCtx, traceID, corrID, corrGenerated, shadow, pathDecision, comparison, primaryRes)

	if !shadow.doShadow {
		h.logHTTPResult(payload)
		return
	}

	h.startShadow(requestCtx, event, payload, &shadowStartedLabel)
}

func (h *Handler) newHTTPRequestContext(c *gin.Context) httpRequestContext {
	return httpRequestContext{
		reqID:      h.requestID.Add(1),
		start:      time.Now(),
		method:     c.Request.Method,
		path:       c.Param("path"),
		rawQuery:   c.Request.URL.RawQuery,
		host:       c.Request.Host,
		remoteAddr: clientIP(c.Request),
	}
}

func (r httpRequestContext) event(ctx context.Context, hdr http.Header, body []byte, primaryPath, shadowPath string, primaryRequestHeaders, shadowRequestHeaders map[string]string) protocol.Event {
	return protocol.Event{
		Ctx:                   ctx,
		Kind:                  protocolHTTP,
		Method:                r.method,
		Path:                  r.path,
		PrimaryPath:           primaryPath,
		ShadowPath:            shadowPath,
		RawQuery:              r.rawQuery,
		Host:                  r.host,
		Header:                hdr,
		Body:                  body,
		RemoteAddr:            r.remoteAddr,
		RequestID:             r.reqID,
		PrimaryRequestHeaders: cloneStringMap(primaryRequestHeaders),
		ShadowRequestHeaders:  cloneStringMap(shadowRequestHeaders),
	}
}

func (r httpRequestContext) payload(
	ctx context.Context,
	traceID,
	corrID string,
	corrGenerated bool,
	shadow shadowDecision,
	pathDecision pathrules.Decision,
	comparison comparisonDecision,
	primaryRes protocol.Response,
) httpLogPayload {
	return httpLogPayload{
		reqID:             r.reqID,
		ctx:               ctx,
		traceID:           traceID,
		corrID:            corrID,
		corrGenerated:     corrGenerated,
		remoteAddr:        r.remoteAddr,
		method:            r.method,
		path:              r.path,
		rawQuery:          r.rawQuery,
		doShadow:          shadow.doShadow,
		forcedShadow:      shadow.forced,
		pathRule:          pathDecision.RuleName,
		shadowMode:        string(pathDecision.ShadowMode),
		shadowSkipReason:  shadow.skipReason,
		compareEnabled:    comparison.enabled,
		compareMode:       comparison.mode,
		compareHeaders:    comparison.headers,
		compareSkipReason: comparison.skipReason,
		primaryRes:        primaryRes,
	}
}

func (h *Handler) startHTTPObservation(
	c *gin.Context,
	request httpRequestContext,
	outcome *string,
	shadowEnabledLabel *string,
	shadowStartedLabel *string,
	spanErr *error,
) (context.Context, string, func()) {
	requestCtx := c.Request.Context()
	if h.observability == nil {
		return requestCtx, "", func() {}
	}

	requestCtx = h.observability.ExtractHTTPContext(requestCtx, c.Request.Header)
	requestCtx, span := h.observability.StartSpanWithKind(requestCtx,
		fmt.Sprintf("HTTP %s", request.method),
		trace.SpanKindServer,
		attribute.String(observability.LabelProtocol, protocolHTTP),
		attribute.String(observability.LabelMethod, request.method),
		attribute.String("url.path", request.path),
		attribute.String("client.address", request.remoteAddr),
		attribute.Int64("doppelgaenger.request_id", int64(request.reqID)),
	)
	c.Request = c.Request.WithContext(requestCtx)
	h.observability.SetTraceIDHeader(requestCtx, c.Writer.Header())
	traceID := observability.TraceIDFromContext(requestCtx)

	finish := func() {
		status := c.Writer.Status()
		span.SetAttributes(
			attribute.Int("http.response.status_code", status),
			attribute.String(observability.LabelOutcome, *outcome),
			attribute.String(observability.LabelShadow, *shadowEnabledLabel),
			attribute.String(observability.LabelShadowStarted, *shadowStartedLabel),
		)

		if status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, http.StatusText(status))
		}

		h.observability.ObserveIngressRequest(requestCtx, protocolHTTP, request.method, *outcome, *shadowEnabledLabel, *shadowStartedLabel, time.Since(request.start))
		h.observability.EndSpan(span, *spanErr)
	}

	return requestCtx, traceID, finish
}

func (h *Handler) readRequestBody(c *gin.Context, outcome *string, spanErr *error) ([]byte, bool) {
	if c.Request.Body == nil {
		return nil, true
	}

	reader := io.Reader(c.Request.Body)
	if h.cfg.MaxBackendBodyBytes > 0 {
		reader = io.LimitReader(c.Request.Body, h.cfg.MaxBackendBodyBytes+1)
	}

	body, err := io.ReadAll(reader)
	if err != nil {
		*outcome = observability.OutcomeBadRequest
		*spanErr = err

		c.AbortWithStatus(http.StatusBadRequest)

		return nil, false
	}

	if h.cfg.MaxBackendBodyBytes > 0 && int64(len(body)) > h.cfg.MaxBackendBodyBytes {
		err := fmt.Errorf("request body exceeds max_backend_body_bytes %d", h.cfg.MaxBackendBodyBytes)
		*outcome = observability.OutcomeBadRequest
		*spanErr = err

		c.AbortWithStatus(http.StatusRequestEntityTooLarge)

		return nil, false
	}

	return body, true
}

func (h *Handler) mapRequestPaths(c *gin.Context, request httpRequestContext, traceID string, outcome *string, spanErr *error) (string, string, bool) {
	primaryPath, shadowPath, err := h.pathMapper.Map(request.path)
	if err == nil {
		return primaryPath, shadowPath, true
	}

	*outcome = observability.OutcomeError
	*spanErr = err
	h.logger.Error("path_mapping_failed",
		"req_id", request.reqID,
		"trace_id", traceID,
		"remote", request.remoteAddr,
		"path", request.path,
		"err", err.Error(),
	)
	c.AbortWithStatus(http.StatusInternalServerError)

	return "", "", false
}

func (h *Handler) resolvePathRule(request httpRequestContext) pathrules.Decision {
	if h.pathRuleResolver == nil {
		return pathrules.Decision{
			ShadowMode:      pathrules.ShadowModeInherit,
			CompareDecision: pathrules.CompareDecisionInherit,
		}
	}

	return h.pathRuleResolver.Resolve(request.method, request.path)
}

func (h *Handler) shouldShadow(c *gin.Context, pathDecision pathrules.Decision) shadowDecision {
	forcedShadow := h.cfg.ShadowForceHeader != "" && c.Request.Header.Get(h.cfg.ShadowForceHeader) != ""

	if pathDecision.ShadowMode == pathrules.ShadowModeNever {
		reason := pathDecision.ShadowSkipReason
		if reason == "" {
			reason = pathrules.SkipReasonPathRule
		}

		return shadowDecision{forced: forcedShadow, skipReason: reason}
	}

	doShadow := false

	switch pathDecision.ShadowMode {
	case pathrules.ShadowModeAlways:
		doShadow = true
	case pathrules.ShadowModeAuto, pathrules.ShadowModeInherit:
		doShadow = forcedShadow || h.sampleShadow()
	default:
		doShadow = forcedShadow || h.sampleShadow()
	}

	if doShadow && !forcedShadow && h.shadowLimiter != nil && !h.shadowLimiter.Allow() {
		return shadowDecision{forced: forcedShadow, skipReason: shadowSkipReasonRateLimited}
	}

	return shadowDecision{doShadow: doShadow, forced: forcedShadow}
}

func (h *Handler) resolveComparison(pathDecision pathrules.Decision, shadow shadowDecision) comparisonDecision {
	decision := comparisonDecision{
		mode:    h.resolvedCompareMode(pathDecision),
		headers: h.resolvedCompareHeaders(pathDecision),
	}

	if pathDecision.CompareDecision == pathrules.CompareDecisionOff {
		decision.skipReason = pathDecision.CompareSkipReason
		if decision.skipReason == "" {
			decision.skipReason = pathrules.SkipReasonPathRule
		}

		return decision
	}

	if !shadow.doShadow {
		decision.skipReason = pathDecision.CompareSkipReason
		if decision.skipReason == "" {
			decision.skipReason = compareSkipReasonNoShadow
		}

		return decision
	}

	decision.enabled = true

	return decision
}

func (h *Handler) resolvedCompareMode(pathDecision pathrules.Decision) string {
	if strings.TrimSpace(pathDecision.CompareMode) != "" {
		return pathDecision.CompareMode
	}

	return h.cfg.CompareMode
}

func (h *Handler) resolvedCompareHeaders(pathDecision pathrules.Decision) []string {
	if !pathDecision.CompareHeadersSet {
		return nil
	}

	compareHeaders := make([]string, len(pathDecision.CompareHeaders))
	copy(compareHeaders, pathDecision.CompareHeaders)

	return compareHeaders
}

func (h *Handler) sampleShadow() bool {
	if h.cfg.ShadowSamplePercent >= 100 {
		return true
	}

	if h.cfg.ShadowSamplePercent <= 0 {
		return false
	}

	return h.randIntn(100) < h.cfg.ShadowSamplePercent
}

func (h *Handler) callPrimary(
	c *gin.Context,
	event protocol.Event,
	request httpRequestContext,
	traceID string,
	corrID string,
	outcome *string,
	spanErr *error,
) (protocol.Response, bool) {
	primarySession, err := h.adapter.NewSession(c.Request.Context(), protocol.TargetPrimary)
	if err != nil {
		h.failPrimarySession(c, err, request, traceID, corrID, outcome, spanErr)

		return protocol.Response{}, false
	}
	defer func() {
		_ = primarySession.Close()
	}()

	if err := primarySession.Send(event); err != nil {
		h.failPrimaryRequest(c, err, request, traceID, corrID, outcome, spanErr, 0)

		return protocol.Response{}, false
	}

	primaryRes, err := primarySession.Receive()
	if err != nil && primaryRes.Err == nil {
		primaryRes.Err = err
	}

	if primaryRes.Err != nil {
		h.failPrimaryRequest(c, primaryRes.Err, request, traceID, corrID, outcome, spanErr, primaryRes.Duration.Milliseconds())

		return protocol.Response{}, false
	}

	return primaryRes, true
}

func (h *Handler) failPrimarySession(c *gin.Context, err error, request httpRequestContext, traceID, corrID string, outcome *string, spanErr *error) {
	*outcome = observability.OutcomePrimaryError
	*spanErr = err
	h.logger.Error("primary_session_failed",
		"req_id", request.reqID,
		"trace_id", traceID,
		"x_request_id", corrID,
		"remote", request.remoteAddr,
		"method", request.method,
		"path", request.path,
		"err", err.Error(),
	)
	c.AbortWithStatus(http.StatusServiceUnavailable)
}

func (h *Handler) failPrimaryRequest(
	c *gin.Context,
	err error,
	request httpRequestContext,
	traceID string,
	corrID string,
	outcome *string,
	spanErr *error,
	durationMillis int64,
) {
	*outcome = observability.OutcomePrimaryError
	*spanErr = err
	h.logger.Error("primary_request_failed",
		"req_id", request.reqID,
		"trace_id", traceID,
		"x_request_id", corrID,
		"remote", request.remoteAddr,
		"method", request.method,
		"path", request.path,
		"err", err.Error(),
		"dur_ms", durationMillis,
	)
	c.AbortWithStatus(http.StatusBadGateway)
}

func (h *Handler) writePrimaryResponse(c *gin.Context, corrID string, primaryRes protocol.Response) {
	headers.WriteSelected(c.Writer, primaryRes.Header, h.cfg.ForwardResponseHeaders)
	writeRedirectResponseHeaders(c.Writer, primaryRes)
	c.Writer.Header().Set("X-Request-ID", corrID)
	c.Status(primaryRes.Status)

	if len(primaryRes.Body) > 0 {
		_, _ = c.Writer.Write(primaryRes.Body)
	}
}

func writeRedirectResponseHeaders(w http.ResponseWriter, response protocol.Response) {
	if response.Status < http.StatusMultipleChoices || response.Status > http.StatusPermanentRedirect {
		return
	}

	headers.WriteSelected(w, response.Header, []string{headerLocation, headerSetCookie})
}

func (h *Handler) startShadow(requestCtx context.Context, event protocol.Event, payload httpLogPayload, shadowStartedLabel *string) {
	shadowCtx := context.WithoutCancel(requestCtx)

	var cancel context.CancelFunc
	if h.runner.ShadowTimeout > 0 {
		shadowCtx, cancel = context.WithTimeout(shadowCtx, h.runner.ShadowTimeout)
	}

	shadowSession, err := h.adapter.NewSession(shadowCtx, protocol.TargetShadow)
	if err != nil {
		if cancel != nil {
			cancel()
		}

		payload.shadowErr = shadowNotStarted
		payload.compareEnabled = false

		if payload.compareSkipReason == "" {
			payload.compareSkipReason = compareSkipReasonNoShadow
		}

		h.logHTTPResult(payload)

		return
	}

	payload.shadowStarted = true
	*shadowStartedLabel = observability.BoolLabel(true)

	go h.runShadowAndLog(event, shadowSession, cancel, payload)
}

func (h *Handler) runShadowAndLog(event protocol.Event, shadowSession protocol.TestSession, cancel context.CancelFunc, payload httpLogPayload) {
	if cancel != nil {
		defer cancel()
	}

	defer func() {
		_ = shadowSession.Close()
	}()

	if err := shadowSession.Send(event); err != nil {
		payload.shadowRes.Err = err
		payload.shadowErr = protocol.ErrString(err)
		payload.compareEnabled = false

		if payload.compareSkipReason == "" {
			payload.compareSkipReason = compareSkipReasonNoShadow
		}

		h.logHTTPResult(payload)

		return
	}

	shadowRes, err := shadowSession.Receive()
	if err != nil && shadowRes.Err == nil {
		shadowRes.Err = err
	}

	payload.shadowRes = shadowRes
	payload.shadowErr = protocol.ErrString(shadowRes.Err)

	if !payload.compareEnabled {
		h.logHTTPResult(payload)

		return
	}

	compareResult, compareErr := h.compareHTTPResponses(payload, shadowRes)
	payload.compareResult = compareResult
	payload.compareErr = compareErr
	h.logHTTPResult(payload)
}

func (h *Handler) compareHTTPResponses(payload httpLogPayload, shadowRes protocol.Response) (protocol.CompareResult, error) {
	if h.httpComparators != nil {
		result, err := h.httpComparators.Compare(
			payload.compareMode,
			payload.compareHeaders,
			httpBackendResult(payload.primaryRes),
			httpBackendResult(shadowRes),
		)

		return protocol.CompareResult{
			Mode:           result.Mode,
			Diff:           result.Diff,
			HeaderDiff:     result.HeaderDiff,
			HeaderPrimary:  result.HeaderPrimary,
			HeaderShadow:   result.HeaderShadow,
			HeaderDiffs:    result.HeaderDiffs,
			BodyDiff:       result.BodyDiff,
			JSONDiffs:      result.JSONDiffs,
			HTMLSimilarity: result.HTMLSimilarity,
		}, err
	}

	if h.runner.Comparator == nil {
		return protocol.CompareResult{}, fmt.Errorf("missing protocol comparator")
	}

	return h.runner.Comparator.Compare(payload.primaryRes, shadowRes)
}

func httpBackendResult(response protocol.Response) backend.Result {
	return backend.Result{
		Header:   response.Header,
		Body:     response.Body,
		Err:      response.Err,
		Proto:    response.Proto,
		Duration: response.Duration,
		Status:   response.Status,
	}
}

func (h *Handler) logHTTPResult(payload httpLogPayload) {
	compareResult := h.normalizedCompareResult(payload)
	pKV, sKV, diffs, hasDiff := h.filteredHeaderLogFields(payload, compareResult)
	shadowSelected, shadowErr, shadowOK := shadowLogFields(payload)

	if h.observability != nil {
		h.observability.ObserveComparison(payload.ctx, protocolHTTP, comparisonMetricResult(payload, hasDiff))
	}

	if h.shouldSuppressHTTPLog(payload, hasDiff, shadowErr) {
		return
	}

	h.logger.Info("auth_proxy",
		"req_id", payload.reqID,
		"trace_id", payload.traceID,
		"x_request_id", payload.corrID,
		"x_request_id_generated", payload.corrGenerated,
		"remote", payload.remoteAddr,
		"method", payload.method,
		"path", payload.path,
		"query", payload.rawQuery,

		"shadow_enabled", payload.doShadow,
		"shadow_forced", payload.forcedShadow,
		"shadow_started", payload.shadowStarted,
		"path_rule", payload.pathRule,
		"shadow_mode", payload.shadowMode,
		"shadow_skip_reason", payload.shadowSkipReason,

		"primary_status", payload.primaryRes.Status,
		"primary_proto", payload.primaryRes.Proto,
		"primary_selected", payload.primaryRes.Selected,
		"primary_dur_ms", payload.primaryRes.Duration.Milliseconds(),
		"primary_headers", pKV,

		"shadow_ok", shadowOK,
		"shadow_status", payload.shadowRes.Status,
		"shadow_proto", payload.shadowRes.Proto,
		"shadow_selected", shadowSelected,
		"shadow_dur_ms", payload.shadowRes.Duration.Milliseconds(),
		"shadow_err", shadowErr,
		"shadow_headers", sKV,

		"compare_mode", compareResult.Mode,
		"compare_enabled", payload.compareEnabled,
		"compare_skip_reason", payload.compareSkipReason,
		"diff", hasDiff,
		"diffs", diffs,
		"body_diff", compareResult.BodyDiff,
		"json_diffs", compareResult.JSONDiffs,
		"html_similarity", compareResult.HTMLSimilarity,
		"compare_err", protocol.ErrString(payload.compareErr),
	)
}

func (h *Handler) normalizedCompareResult(payload httpLogPayload) protocol.CompareResult {
	compareResult := payload.compareResult
	if compareResult.Mode == "" {
		compareResult.Mode = payload.compareMode
	}

	if compareResult.Mode == "" {
		compareResult.Mode = h.cfg.CompareMode
	}

	return compareResult
}

func (h *Handler) filteredHeaderLogFields(payload httpLogPayload, compareResult protocol.CompareResult) (map[string]string, map[string]string, []headers.HeaderDiff, bool) {
	pKV := compareResult.HeaderPrimary
	sKV := compareResult.HeaderShadow
	diffs := compareResult.HeaderDiffs
	hasDiff := compareResult.Diff || payload.compareErr != nil

	if !h.shouldHideSessionHeaders(payload, hasDiff) {
		return pKV, sKV, diffs, hasDiff
	}

	delete(pKV, headerSessionID)
	delete(sKV, headerSessionID)

	return pKV, sKV, filterSessionDiffs(diffs), hasDiff
}

func (h *Handler) shouldHideSessionHeaders(payload httpLogPayload, hasDiff bool) bool {
	return h.cfg.LogSessionOnlyOnDiff && !hasDiff && !payload.forcedShadow
}

func (h *Handler) shouldSuppressHTTPLog(payload httpLogPayload, hasDiff bool, shadowErr string) bool {
	if !h.cfg.LogOnlyOnDiff {
		return false
	}

	return !hasDiff && shadowErr == "" && payload.compareErr == nil
}

func filterSessionDiffs(diffs []headers.HeaderDiff) []headers.HeaderDiff {
	filtered := make([]headers.HeaderDiff, 0, len(diffs))
	for _, diff := range diffs {
		if diff.Key == headerSessionID {
			continue
		}

		filtered = append(filtered, diff)
	}

	return filtered
}

func shadowLogFields(payload httpLogPayload) (string, string, bool) {
	if payload.doShadow && !payload.shadowStarted {
		return shadowNotStarted, shadowNotStarted, false
	}

	return payload.shadowRes.Selected, payload.shadowErr, payload.shadowStarted && payload.shadowRes.Err == nil
}

func comparisonMetricResult(payload httpLogPayload, hasDiff bool) string {
	if payload.compareSkipReason == pathrules.SkipReasonPathRule || payload.compareSkipReason == pathrules.SkipReasonPathUnmatched {
		return observability.ResultSkipped
	}

	if payload.doShadow && payload.shadowErr != "" {
		return observability.ResultError
	}

	if !payload.compareEnabled || !payload.doShadow || !payload.shadowStarted {
		return observability.ResultSkipped
	}

	if payload.compareErr != nil {
		return observability.ResultError
	}

	if hasDiff {
		return observability.ResultDiff
	}

	return observability.ResultSame
}

func (h *Handler) randIntn(n int) int {
	h.randMu.Lock()
	defer h.randMu.Unlock()

	return h.rng.Intn(n)
}

func (h *Handler) randInt63() int64 {
	h.randMu.Lock()
	defer h.randMu.Unlock()

	return h.rng.Int63()
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}

	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}

	return cloned
}

func (h *Handler) ensureRequestID(header http.Header) (rid string, generated bool) {
	rid = strings.TrimSpace(header.Get("X-Request-Id"))
	if rid == "" {
		rid = strings.TrimSpace(header.Get("X-Request-ID"))
	}

	if rid != "" {
		return rid, false
	}

	rid = fmt.Sprintf("%d-%d", time.Now().UnixNano(), h.randInt63())
	header.Set("X-Request-ID", rid)

	return rid, true
}

func prepareForwardedHeaders(r *http.Request, header http.Header) {
	headers.RemoveHopByHop(header)

	if header.Get("X-Forwarded-Proto") == "" {
		header.Set("X-Forwarded-Proto", requestScheme(r))
	}

	if header.Get("X-Forwarded-Host") == "" && r.Host != "" {
		header.Set("X-Forwarded-Host", r.Host)
	}

	if peer := peerIP(r); peer != "" {
		if existing := strings.TrimSpace(header.Get("X-Forwarded-For")); existing != "" {
			header.Set("X-Forwarded-For", existing+", "+peer)
		} else {
			header.Set("X-Forwarded-For", peer)
		}
	}

	if header.Get("X-Real-IP") == "" {
		if client := clientIP(r); client != "" {
			header.Set("X-Real-IP", client)
		}
	}
}

func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}

	if scheme := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); scheme != "" {
		parts := strings.Split(scheme, ",")
		if len(parts) > 0 && strings.TrimSpace(parts[0]) != "" {
			return strings.TrimSpace(parts[0])
		}
	}

	return protocolHTTP
}

func clientIP(r *http.Request) string {
	xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}

	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}

	return r.RemoteAddr
}

func peerIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}

	return strings.TrimSpace(r.RemoteAddr)
}
