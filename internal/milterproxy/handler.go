// Package milterproxy handles Milter proxy connections.
package milterproxy

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"

	"doppelgaenger/internal/config"
	"doppelgaenger/internal/observability"
	"doppelgaenger/internal/protocol"
	"doppelgaenger/internal/ratelimit"
)

const (
	shadowNotStarted  = "shadow_not_started"
	milterMetaCommand = "command"
)

// Handler processes accepted Milter client connections.
type Handler struct {
	cfg           config.Config
	adapter       protocol.Adapter
	runner        protocol.Runner
	shadowLimiter ratelimit.Limiter
	logger        *slog.Logger
	observability *observability.Observability

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

// HandlerDeps contains dependencies for constructing a Milter proxy handler.
type HandlerDeps struct {
	fx.In
	Config        config.Config
	Adapter       protocol.Adapter
	Runner        protocol.Runner
	ShadowLimiter ratelimit.Limiter
	Logger        *slog.Logger
	Observability *observability.Observability `optional:"true"`
}

// NewHandler constructs a Milter proxy handler.
func NewHandler(deps HandlerDeps) *Handler {
	return &Handler{
		cfg:           deps.Config,
		adapter:       deps.Adapter,
		runner:        deps.Runner,
		shadowLimiter: deps.ShadowLimiter,
		logger:        deps.Logger,
		observability: deps.Observability,
		rng:           newLockedRand(),
	}
}

// HandleConn processes one Milter client connection until it closes or fails.
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
		if h.shouldStopMilterLoop(err) {
			return
		}

		if !h.handleFrame(ctx, conn, primarySession, shadowSession, shadowEnabled, frame) {
			return
		}
	}
}

func (h *Handler) shouldStopMilterLoop(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, net.ErrClosed) || strings.Contains(err.Error(), "closed") {
		return true
	}

	h.logger.Error("milter_read_failed", "err", err)

	return true
}

func (h *Handler) handleFrame(ctx context.Context, conn net.Conn, primarySession, shadowSession protocol.TestSession, shadowEnabled bool, frame protocol.MilterFrame) bool {
	reqID := h.requestID.Add(1)
	command := string(frame.Command)
	frameCtx, span := h.startFrameSpan(ctx, reqID, command)
	frameStart := time.Now()
	traceID := observability.TraceIDFromContext(frameCtx)

	event := protocol.Event{
		Ctx:       frameCtx,
		Kind:      protocolMilter,
		Payload:   frame.Raw,
		RequestID: reqID,
		Meta: map[string]string{
			milterMetaCommand: command,
		},
	}

	result := h.runner.RunEvent(frameCtx, primarySession, shadowSession, event)
	if !h.writePrimaryFrame(frameCtx, conn, result, reqID, traceID, span, frameStart, command, shadowEnabled) {
		return false
	}

	h.logFrameResult(frameCtx, reqID, traceID, command, shadowEnabled, span, frameStart, result)

	return true
}

func (h *Handler) startFrameSpan(ctx context.Context, reqID uint64, command string) (context.Context, trace.Span) {
	if h.observability == nil {
		return ctx, nil
	}

	return h.observability.StartSpanWithKind(ctx,
		protocolMilter+" "+command,
		trace.SpanKindServer,
		attribute.String(observability.LabelProtocol, protocolMilter),
		attribute.String(observability.LabelMethod, command),
		attribute.Int64("doppelgaenger.request_id", int64(reqID)),
	)
}

func (h *Handler) writePrimaryFrame(frameCtx context.Context, conn net.Conn, result protocol.RunResult, reqID uint64, traceID string, span trace.Span, frameStart time.Time, command string, shadowEnabled bool) bool {
	if result.Primary.Err != nil {
		h.finishFrame(frameCtx, span, frameStart, command, shadowEnabled, observability.OutcomePrimaryError, result.Primary.Err, result)
		h.logger.Error("milter_primary_failed", "req_id", reqID, "trace_id", traceID, "err", result.Primary.Err)

		return false
	}

	if len(result.Primary.Raw) == 0 {
		err := errors.New("primary returned empty milter frame")
		h.finishFrame(frameCtx, span, frameStart, command, shadowEnabled, observability.OutcomePrimaryError, err, result)
		h.logger.Error("milter_primary_empty", "req_id", reqID, "trace_id", traceID)

		return false
	}

	if _, err := conn.Write(result.Primary.Raw); err != nil {
		h.finishFrame(frameCtx, span, frameStart, command, shadowEnabled, observability.OutcomeError, err, result)
		h.logger.Error("milter_write_failed", "req_id", reqID, "trace_id", traceID, "err", err)

		return false
	}

	return true
}

func (h *Handler) logFrameResult(
	frameCtx context.Context,
	reqID uint64,
	traceID string,
	command string,
	shadowEnabled bool,
	span trace.Span,
	frameStart time.Time,
	result protocol.RunResult,
) {
	shadowSelected := result.Shadow.Selected
	if shadowEnabled && !result.ShadowStarted {
		shadowSelected = shadowNotStarted
	}

	if h.observability != nil {
		h.observability.ObserveComparison(frameCtx, protocolMilter, milterComparisonMetricResult(shadowEnabled, result))
	}

	h.finishFrame(frameCtx, span, frameStart, command, shadowEnabled, observability.OutcomeOK, nil, result)
	h.logger.Info("milter_proxy",
		"req_id", reqID,
		"trace_id", traceID,
		milterMetaCommand, command,
		"shadow_enabled", shadowEnabled,
		"shadow_started", result.ShadowStarted,
		"shadow_ok", result.ShadowOK,
		"shadow_err", result.ShadowErr,
		"primary_selected", result.Primary.Selected,
		"shadow_selected", shadowSelected,
		"decision_primary", result.Primary.Decision,
		"decision_shadow", result.Shadow.Decision,
		"diff", result.Compare.Diff,
		"decision_diff", result.Compare.DecisionDiff,
		"compare_err", protocol.ErrString(result.CompareErr),
	)
}

func (h *Handler) finishFrame(ctx context.Context, span trace.Span, frameStart time.Time, command string, shadowEnabled bool, outcome string, err error, result protocol.RunResult) {
	if h.observability == nil {
		return
	}

	shadowStarted := observability.BoolLabel(result.ShadowStarted)

	h.observability.ObserveIngressRequest(ctx, protocolMilter, command, outcome, observability.BoolLabel(shadowEnabled), shadowStarted, time.Since(frameStart))
	h.observability.EndSpan(span, err)
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

func milterComparisonMetricResult(shadowEnabled bool, result protocol.RunResult) string {
	if !shadowEnabled || !result.ShadowStarted {
		return observability.ResultSkipped
	}

	if result.ShadowErr != "" || result.CompareErr != nil {
		return observability.ResultError
	}

	if result.Compare.Diff || result.Compare.DecisionDiff {
		return observability.ResultDiff
	}

	return observability.ResultSame
}
