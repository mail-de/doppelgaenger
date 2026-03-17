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
	"go.uber.org/fx"

	"doppelgaenger/internal/config"
	"doppelgaenger/internal/headers"
	"doppelgaenger/internal/mapping"
	"doppelgaenger/internal/protocol"
	"doppelgaenger/internal/ratelimit"
)

// Handler handles incoming proxy requests.
type Handler struct {
	cfg           config.Config
	adapter       protocol.ProtocolAdapter
	runner        protocol.Runner
	shadowLimiter ratelimit.Limiter
	pathMapper    mapping.PathMapper
	logger        *slog.Logger

	requestID atomic.Uint64
	randMu    sync.Mutex
	rng       *rand.Rand
}

// HandlerDeps describes dependencies needed for Handler.
type HandlerDeps struct {
	fx.In
	Config        config.Config
	Adapter       protocol.ProtocolAdapter
	Runner        protocol.Runner
	ShadowLimiter ratelimit.Limiter
	PathMapper    mapping.PathMapper
	Logger        *slog.Logger
}

type httpLogPayload struct {
	reqID         uint64
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

// NewHandler constructs a proxy handler with its dependencies.
func NewHandler(deps HandlerDeps) *Handler {
	return &Handler{
		cfg:           deps.Config,
		adapter:       deps.Adapter,
		runner:        deps.Runner,
		shadowLimiter: deps.ShadowLimiter,
		pathMapper:    deps.PathMapper,
		logger:        deps.Logger,
		rng:           rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Handle processes a single incoming request.
func (h *Handler) Handle(c *gin.Context) {
	reqID := h.requestID.Add(1)

	var body []byte
	if c.Request.Body != nil {
		b, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}
		body = b
	}

	method := c.Request.Method
	path := c.Param("path")
	rawQuery := c.Request.URL.RawQuery
	hdr := headers.Clone(c.Request.Header)
	remoteAddr := clientIP(c.Request)

	primaryPath, shadowPath, err := h.pathMapper.Map(path)
	if err != nil {
		h.logger.Error("path_mapping_failed",
			"req_id", reqID,
			"remote", remoteAddr,
			"path", path,
			"err", err.Error(),
		)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	corrID, corrGenerated := h.ensureRequestID(hdr)

	if hdr.Get("X-Forwarded-Proto") == "" {
		hdr.Set("X-Forwarded-Proto", "https")
	}

	forcedShadow := false
	if h.cfg.ShadowForceHeader != "" && c.Request.Header.Get(h.cfg.ShadowForceHeader) != "" {
		forcedShadow = true
	}

	sampledShadow := false
	if h.cfg.ShadowSamplePercent >= 100 {
		sampledShadow = true
	} else if h.cfg.ShadowSamplePercent > 0 {
		if h.randIntn(100) < h.cfg.ShadowSamplePercent {
			sampledShadow = true
		}
	}

	doShadow := forcedShadow || sampledShadow

	if doShadow && !forcedShadow && h.shadowLimiter != nil && !h.shadowLimiter.Allow() {
		doShadow = false
	}

	event := protocol.Event{
		Kind:        "http",
		Method:      method,
		Path:        path,
		PrimaryPath: primaryPath,
		ShadowPath:  shadowPath,
		RawQuery:    rawQuery,
		Header:      hdr,
		Body:        body,
		RemoteAddr:  remoteAddr,
		RequestID:   reqID,
	}

	primarySession, err := h.adapter.NewSession(c.Request.Context(), protocol.TargetPrimary)
	if err != nil {
		h.logger.Error("primary_session_failed",
			"req_id", reqID,
			"x_request_id", corrID,
			"remote", remoteAddr,
			"method", method,
			"path", path,
			"err", err.Error(),
		)
		c.AbortWithStatus(http.StatusServiceUnavailable)

		return
	}

	defer func() {
		_ = primarySession.Close()
	}()

	if err := primarySession.Send(event); err != nil {
		h.logger.Error("primary_request_failed",
			"req_id", reqID,
			"x_request_id", corrID,
			"remote", remoteAddr,
			"method", method,
			"path", path,
			"err", err.Error(),
		)
		c.AbortWithStatus(http.StatusBadGateway)

		return
	}

	primaryRes, err := primarySession.Receive()
	if err != nil && primaryRes.Err == nil {
		primaryRes.Err = err
	}
	if primaryRes.Err != nil {
		h.logger.Error("primary_request_failed",
			"req_id", reqID,
			"x_request_id", corrID,
			"remote", remoteAddr,
			"method", method,
			"path", path,
			"err", primaryRes.Err.Error(),
			"dur_ms", primaryRes.Duration.Milliseconds(),
		)
		c.AbortWithStatus(http.StatusBadGateway)

		return
	}

	headers.WriteSelected(c.Writer, primaryRes.Header, h.cfg.ForwardResponseHeaders)
	c.Writer.Header().Set("X-Request-ID", corrID)
	c.Status(primaryRes.Status)

	if len(primaryRes.Body) > 0 {
		_, _ = c.Writer.Write(primaryRes.Body)
	}

	payload := httpLogPayload{
		reqID:         reqID,
		corrID:        corrID,
		corrGenerated: corrGenerated,
		remoteAddr:    remoteAddr,
		method:        method,
		path:          path,
		rawQuery:      rawQuery,
		doShadow:      doShadow,
		forcedShadow:  forcedShadow,
		primaryRes:    primaryRes,
	}

	if !doShadow {
		h.logHTTPResult(payload)
		return
	}

	shadowCtx := context.Background()
	var cancel context.CancelFunc
	if h.runner.ShadowTimeout > 0 {
		shadowCtx, cancel = context.WithTimeout(context.Background(), h.runner.ShadowTimeout)
	}

	shadowSession, err := h.adapter.NewSession(shadowCtx, protocol.TargetShadow)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		payload.shadowErr = "shadow_not_started"
		h.logHTTPResult(payload)

		return
	}

	payload.shadowStarted = true
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
	compareResult := payload.compareResult
	if compareResult.Mode == "" {
		compareResult.Mode = h.cfg.CompareMode
	}

	pKV := compareResult.HeaderPrimary
	sKV := compareResult.HeaderShadow
	diffs := compareResult.HeaderDiffs
	hasDiff := compareResult.Diff
	if payload.compareErr != nil {
		hasDiff = true
	}

	if h.cfg.LogSessionOnlyOnDiff && !hasDiff && !payload.forcedShadow {
		delete(pKV, "X-Nauthilus-Session")
		delete(sKV, "X-Nauthilus-Session")

		filtered := make([]headers.HeaderDiff, 0, len(diffs))
		for _, d := range diffs {
			if d.Key == "X-Nauthilus-Session" {
				continue
			}
			filtered = append(filtered, d)
		}
		diffs = filtered
	}

	shadowSelected := payload.shadowRes.Selected
	if payload.doShadow && !payload.shadowStarted {
		shadowSelected = "shadow_not_started"
	}

	shadowErr := payload.shadowErr
	if payload.doShadow && !payload.shadowStarted {
		shadowErr = "shadow_not_started"
	}

	shadowOK := payload.shadowStarted && payload.shadowRes.Err == nil
	if payload.doShadow && !payload.shadowStarted {
		shadowOK = false
	}

	h.logger.Info("auth_proxy",
		"req_id", payload.reqID,
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
