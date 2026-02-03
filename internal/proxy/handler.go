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

	"httpproxy/internal/config"
	"httpproxy/internal/headers"
	"httpproxy/internal/protocol"
	"httpproxy/internal/ratelimit"
)

// Handler handles incoming proxy requests.
type Handler struct {
	cfg           config.Config
	adapter       protocol.ProtocolAdapter
	runner        protocol.Runner
	shadowLimiter ratelimit.Limiter
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
	Logger        *slog.Logger
}

// NewHandler constructs a proxy handler with its dependencies.
func NewHandler(deps HandlerDeps) *Handler {
	return &Handler{
		cfg:           deps.Config,
		adapter:       deps.Adapter,
		runner:        deps.Runner,
		shadowLimiter: deps.ShadowLimiter,
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

	primarySession, err := h.adapter.NewSession(c.Request.Context(), protocol.TargetPrimary)
	if err != nil {
		h.logger.Error("primary_enqueue_failed",
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

	shadowStarted := false
	shadowSession := protocol.TestSession(nil)
	var shadowCancel context.CancelFunc
	if doShadow {
		shadowCtx := c.Request.Context()
		shadowCtx, shadowCancel = context.WithTimeout(c.Request.Context(), h.cfg.ShadowTimeout)
		shadowSession, err = h.adapter.NewSession(shadowCtx, protocol.TargetShadow)
		if err == nil {
			shadowStarted = true
		}
	}
	if shadowCancel != nil {
		defer shadowCancel()
	}
	if shadowSession != nil {
		defer func() {
			_ = shadowSession.Close()
		}()
	}

	event := protocol.Event{
		Kind:       "http",
		Method:     method,
		Path:       path,
		RawQuery:   rawQuery,
		Header:     hdr,
		Body:       body,
		RemoteAddr: remoteAddr,
		RequestID:  reqID,
	}

	result := h.runner.RunEvent(c.Request.Context(), primarySession, shadowSession, event)
	primaryRes := result.Primary
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

	shadowRes := result.Shadow
	shadowOK := result.ShadowOK
	shadowErr := result.ShadowErr
	if doShadow && !shadowStarted {
		shadowErr = "shadow_not_started"
	}

	compareResult := result.Compare
	compareErr := result.CompareErr
	pKV := compareResult.HeaderPrimary
	sKV := compareResult.HeaderShadow
	diffs := compareResult.HeaderDiffs
	hasDiff := compareResult.Diff
	if compareErr != nil {
		hasDiff = true
	}

	if h.cfg.LogSessionOnlyOnDiff && !hasDiff && !forcedShadow {
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

	h.logger.Info("auth_proxy",
		"req_id", reqID,
		"x_request_id", corrID,
		"x_request_id_generated", corrGenerated,
		"remote", remoteAddr,
		"method", method,
		"path", path,
		"query", rawQuery,

		"shadow_enabled", doShadow,
		"shadow_forced", forcedShadow,
		"shadow_started", shadowStarted,

		"primary_status", primaryRes.Status,
		"primary_proto", primaryRes.Proto,
		"primary_dur_ms", primaryRes.Duration.Milliseconds(),
		"primary_headers", pKV,

		"shadow_ok", shadowOK,
		"shadow_status", shadowRes.Status,
		"shadow_proto", shadowRes.Proto,
		"shadow_dur_ms", shadowRes.Duration.Milliseconds(),
		"shadow_err", shadowErr,
		"shadow_headers", sKV,

		"compare_mode", compareResult.Mode,
		"diff", hasDiff,
		"diffs", diffs,
		"body_diff", compareResult.BodyDiff,
		"json_diffs", compareResult.JSONDiffs,
		"html_similarity", compareResult.HTMLSimilarity,
		"compare_err", protocol.ErrString(compareErr),
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
