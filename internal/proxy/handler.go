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

	"doppelgaenger/internal/config"
	"doppelgaenger/internal/headers"
	"doppelgaenger/internal/mapping"
	"doppelgaenger/internal/observability"
	"doppelgaenger/internal/protocol"
	"doppelgaenger/internal/ratelimit"
)

const (
	protocolHTTP           = "http"
	shadowNotStarted       = "shadow_not_started"
	headerNauthilusSession = "X-Nauthilus-Session"
)

// Handler handles incoming proxy requests.
type Handler struct {
	cfg           config.Config
	adapter       protocol.Adapter
	runner        protocol.Runner
	shadowLimiter ratelimit.Limiter
	pathMapper    mapping.PathMapper
	logger        *slog.Logger
	observability *observability.Observability

	requestID atomic.Uint64
	randMu    sync.Mutex
	rng       *rand.Rand
}

// HandlerDeps describes dependencies needed for Handler.
type HandlerDeps struct {
	fx.In
	Config        config.Config
	Adapter       protocol.Adapter
	Runner        protocol.Runner
	ShadowLimiter ratelimit.Limiter
	PathMapper    mapping.PathMapper
	Logger        *slog.Logger
	Observability *observability.Observability `optional:"true"`
}

type httpLogPayload struct {
	ctx           context.Context
	reqID         uint64
	traceID       string
	corrID        string
	corrGenerated bool
	remoteAddr    string
	method        string
	path          string
	rawQuery      string
	doShadow      bool
	forcedShadow  bool
	shadowStarted bool
	primaryRes    protocol.Response
	shadowRes     protocol.Response
	shadowErr     string
	compareResult protocol.CompareResult
	compareErr    error
}

type httpRequestContext struct {
	reqID      uint64
	start      time.Time
	method     string
	path       string
	rawQuery   string
	remoteAddr string
}

// NewHandler constructs a proxy handler with its dependencies.
func NewHandler(deps HandlerDeps) *Handler {
	return &Handler{
		cfg:           deps.Config,
		adapter:       deps.Adapter,
		runner:        deps.Runner,
		shadowLimiter: deps.ShadowLimiter,
		pathMapper:    deps.PathMapper,
		logger:        deps.Logger,
		observability: deps.Observability,
		rng:           rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Handle processes a single incoming request.
func (h *Handler) Handle(c *gin.Context) {
	request := h.newHTTPRequestContext(c)

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

	if hdr.Get("X-Forwarded-Proto") == "" {
		hdr.Set("X-Forwarded-Proto", "https")
	}

	doShadow, forcedShadow := h.shouldShadow(c)
	shadowEnabledLabel = observability.BoolLabel(doShadow)

	event := request.event(requestCtx, hdr, body, primaryPath, shadowPath)

	primaryRes, ok := h.callPrimary(c, event, request, traceID, corrID, &outcome, &spanErr)
	if !ok {
		return
	}

	h.writePrimaryResponse(c, corrID, primaryRes)

	payload := request.payload(requestCtx, traceID, corrID, corrGenerated, doShadow, forcedShadow, primaryRes)

	if !doShadow {
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
		remoteAddr: clientIP(c.Request),
	}
}

func (r httpRequestContext) event(ctx context.Context, hdr http.Header, body []byte, primaryPath, shadowPath string) protocol.Event {
	return protocol.Event{
		Ctx:         ctx,
		Kind:        protocolHTTP,
		Method:      r.method,
		Path:        r.path,
		PrimaryPath: primaryPath,
		ShadowPath:  shadowPath,
		RawQuery:    r.rawQuery,
		Header:      hdr,
		Body:        body,
		RemoteAddr:  r.remoteAddr,
		RequestID:   r.reqID,
	}
}

func (r httpRequestContext) payload(ctx context.Context, traceID, corrID string, corrGenerated, doShadow, forcedShadow bool, primaryRes protocol.Response) httpLogPayload {
	return httpLogPayload{
		reqID:         r.reqID,
		ctx:           ctx,
		traceID:       traceID,
		corrID:        corrID,
		corrGenerated: corrGenerated,
		remoteAddr:    r.remoteAddr,
		method:        r.method,
		path:          r.path,
		rawQuery:      r.rawQuery,
		doShadow:      doShadow,
		forcedShadow:  forcedShadow,
		primaryRes:    primaryRes,
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

func (h *Handler) shouldShadow(c *gin.Context) (bool, bool) {
	forcedShadow := h.cfg.ShadowForceHeader != "" && c.Request.Header.Get(h.cfg.ShadowForceHeader) != ""
	sampledShadow := h.sampleShadow()
	doShadow := forcedShadow || sampledShadow

	if doShadow && !forcedShadow && h.shadowLimiter != nil && !h.shadowLimiter.Allow() {
		return false, forcedShadow
	}

	return doShadow, forcedShadow
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
	c.Writer.Header().Set("X-Request-ID", corrID)
	c.Status(primaryRes.Status)

	if len(primaryRes.Body) > 0 {
		_, _ = c.Writer.Write(primaryRes.Body)
	}
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
		h.logHTTPResult(payload)

		return
	}

	shadowRes, err := shadowSession.Receive()
	if err != nil && shadowRes.Err == nil {
		shadowRes.Err = err
	}

	payload.shadowRes = shadowRes
	payload.shadowErr = protocol.ErrString(shadowRes.Err)

	if h.runner.Comparator == nil {
		payload.compareErr = fmt.Errorf("missing protocol comparator")
		h.logHTTPResult(payload)

		return
	}

	compareResult, compareErr := h.runner.Comparator.Compare(payload.primaryRes, shadowRes)
	payload.compareResult = compareResult
	payload.compareErr = compareErr
	h.logHTTPResult(payload)
}

func (h *Handler) logHTTPResult(payload httpLogPayload) {
	compareResult := h.normalizedCompareResult(payload.compareResult)
	pKV, sKV, diffs, hasDiff := h.filteredHeaderLogFields(payload, compareResult)
	shadowSelected, shadowErr, shadowOK := shadowLogFields(payload)

	if h.observability != nil {
		h.observability.ObserveComparison(payload.ctx, protocolHTTP, comparisonMetricResult(payload, hasDiff))
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
		"diff", hasDiff,
		"diffs", diffs,
		"body_diff", compareResult.BodyDiff,
		"json_diffs", compareResult.JSONDiffs,
		"html_similarity", compareResult.HTMLSimilarity,
		"compare_err", protocol.ErrString(payload.compareErr),
	)
}

func (h *Handler) normalizedCompareResult(compareResult protocol.CompareResult) protocol.CompareResult {
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

	delete(pKV, headerNauthilusSession)
	delete(sKV, headerNauthilusSession)

	return pKV, sKV, filterSessionDiffs(diffs), hasDiff
}

func (h *Handler) shouldHideSessionHeaders(payload httpLogPayload, hasDiff bool) bool {
	return h.cfg.LogSessionOnlyOnDiff && !hasDiff && !payload.forcedShadow
}

func filterSessionDiffs(diffs []headers.HeaderDiff) []headers.HeaderDiff {
	filtered := make([]headers.HeaderDiff, 0, len(diffs))
	for _, diff := range diffs {
		if diff.Key == headerNauthilusSession {
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
	if payload.doShadow && payload.shadowErr != "" {
		return observability.ResultError
	}

	if !payload.doShadow || !payload.shadowStarted {
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
