package milterproxy

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"

	"go.uber.org/fx"

	"httpproxy/internal/config"
	"httpproxy/internal/protocol"
	"httpproxy/internal/ratelimit"
)

type Handler struct {
	cfg           config.Config
	adapter       protocol.ProtocolAdapter
	runner        protocol.Runner
	shadowLimiter ratelimit.Limiter
	logger        *slog.Logger

	requestID atomic.Uint64
	rngMu     sync.Mutex
	rng       *lockedRand
}

type lockedRand struct {
	seed uint64
}

func newLockedRand() *lockedRand {
	return &lockedRand{seed: 1}
}

func (r *lockedRand) Intn(n int) int {
	r.seed = r.seed*2862933555777941757 + 3037000493
	return int((r.seed >> 33) % uint64(n))
}

type HandlerDeps struct {
	fx.In
	Config        config.Config
	Adapter       protocol.ProtocolAdapter
	Runner        protocol.Runner
	ShadowLimiter ratelimit.Limiter
	Logger        *slog.Logger
}

func NewHandler(deps HandlerDeps) *Handler {
	return &Handler{
		cfg:           deps.Config,
		adapter:       deps.Adapter,
		runner:        deps.Runner,
		shadowLimiter: deps.ShadowLimiter,
		logger:        deps.Logger,
		rng:           newLockedRand(),
	}
}

func (h *Handler) HandleConn(conn net.Conn) {
	defer func() {
		_ = conn.Close()
	}()

	ctx := context.Background()
	primarySession, err := h.adapter.NewSession(ctx, protocol.TargetPrimary)
	if err != nil {
		h.logger.Error("milter_primary_session_failed", "err", err)
		return
	}
	defer func() {
		_ = primarySession.Close()
	}()

	shadowSession, shadowEnabled := h.shadowSession(ctx)
	if shadowSession != nil {
		defer func() {
			_ = shadowSession.Close()
		}()
	}

	for {
		frame, err := protocol.ReadFrame(conn)
		if err != nil {
			if errors.Is(err, net.ErrClosed) || strings.Contains(err.Error(), "closed") {
				return
			}
			h.logger.Error("milter_read_failed", "err", err)
			return
		}

		reqID := h.requestID.Add(1)
		event := protocol.Event{
			Kind:      "milter",
			Payload:   frame.Raw,
			RequestID: reqID,
			Meta: map[string]string{
				"command": string(frame.Command),
			},
		}

		result := h.runner.RunEvent(ctx, primarySession, shadowSession, event)
		if result.Primary.Err != nil {
			h.logger.Error("milter_primary_failed", "req_id", reqID, "err", result.Primary.Err)
			return
		}

		if len(result.Primary.Raw) == 0 {
			h.logger.Error("milter_primary_empty", "req_id", reqID)
			return
		}
		if _, err := conn.Write(result.Primary.Raw); err != nil {
			h.logger.Error("milter_write_failed", "req_id", reqID, "err", err)
			return
		}

		h.logger.Info("milter_proxy",
			"req_id", reqID,
			"command", string(frame.Command),
			"shadow_enabled", shadowEnabled,
			"shadow_started", result.ShadowStarted,
			"shadow_ok", result.ShadowOK,
			"shadow_err", result.ShadowErr,
			"decision_primary", result.Primary.Decision,
			"decision_shadow", result.Shadow.Decision,
			"diff", result.Compare.Diff,
			"decision_diff", result.Compare.DecisionDiff,
			"compare_err", protocol.ErrString(result.CompareErr),
		)
	}
}

func (h *Handler) shadowSession(ctx context.Context) (protocol.TestSession, bool) {
	forcedShadow := false

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
	if !doShadow {
		return nil, false
	}

	shadowSession, err := h.adapter.NewSession(ctx, protocol.TargetShadow)
	if err != nil {
		h.logger.Error("milter_shadow_session_failed", "err", err)
		return nil, true
	}
	return shadowSession, true
}

func (h *Handler) randIntn(n int) int {
	h.rngMu.Lock()
	defer h.rngMu.Unlock()
	return h.rng.Intn(n)
}
