package proxy

import (
	"context"
	"errors"
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

	"httpproxy/internal/backend"
	"httpproxy/internal/config"
	"httpproxy/internal/headers"
	"httpproxy/internal/ratelimit"
)

// Handler handles incoming proxy requests.
type Handler struct {
	cfg           config.Config
	primaryPool   backend.Pool
	shadowPool    backend.Pool
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
	PrimaryPool   backend.Pool `name:"primaryPool"`
	ShadowPool    backend.Pool `name:"shadowPool"`
	ShadowLimiter ratelimit.Limiter
	Logger        *slog.Logger
}

// NewHandler constructs a proxy handler with its dependencies.
func NewHandler(deps HandlerDeps) *Handler {
	return &Handler{
		cfg:           deps.Config,
		primaryPool:   deps.PrimaryPool,
		shadowPool:    deps.ShadowPool,
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

	primaryCh := make(chan backend.BackendResult, 1)
	shadowCh := make(chan backend.BackendResult, 1)

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

	shadowStarted := false
	var shadowCtx context.Context
	var shadowCancel context.CancelFunc

	if doShadow {
		shadowCtx, shadowCancel = context.WithTimeout(c.Request.Context(), h.cfg.ShadowTimeout)
		defer shadowCancel()

		shadowItem := backend.WorkItem{
			Kind:       backend.BackendShadow,
			Method:     method,
			Path:       path,
			RawQuery:   rawQuery,
			Header:     hdr,
			Body:       body,
			RemoteAddr: remoteAddr,
			RequestID:  reqID,
			Ctx:        shadowCtx,
			ResponseCh: shadowCh,
		}

		if err := h.shadowPool.Enqueue(shadowItem); err == nil {
			shadowStarted = true
		}
	}

	primaryItem := backend.WorkItem{
		Kind:       backend.BackendPrimary,
		Method:     method,
		Path:       path,
		RawQuery:   rawQuery,
		Header:     hdr,
		Body:       body,
		RemoteAddr: remoteAddr,
		RequestID:  reqID,
		Ctx:        c.Request.Context(),
		ResponseCh: primaryCh,
	}

	if err := h.primaryPool.Enqueue(primaryItem); err != nil {
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

	primaryRes := <-primaryCh
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

	var shadowRes backend.BackendResult
	shadowOK := false
	shadowErr := ""

	if shadowStarted {
		select {
		case shadowRes = <-shadowCh:
			shadowOK = shadowRes.Err == nil
			shadowErr = errString(shadowRes.Err)
		case <-shadowCtx.Done():
			shadowRes = backend.BackendResult{
				Kind:      backend.BackendShadow,
				Err:       shadowCtx.Err(),
				Duration:  h.cfg.ShadowTimeout,
				RequestID: reqID,
			}
			shadowOK = false
			shadowErr = errString(shadowRes.Err)
		}
	} else if doShadow {
		shadowErr = "shadow_not_started"
	}

	pKV, sKV, diffs, hasDiff := headers.CompareDetailed(primaryRes.Header, shadowRes.Header, h.cfg.CompareHeaders, false)

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

		"diff", hasDiff,
		"diffs", diffs,
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

func errString(err error) string {
	if err == nil {
		return ""
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}

	if errors.Is(err, context.Canceled) {
		return "canceled"
	}

	return err.Error()
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
