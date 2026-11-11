package grpcproxy

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/status"

	"doppelgaenger/internal/failover"
)

// attemptFailureFunc observes one stream-creation attempt that failed before
// the request left the proxy. Another attempt follows unless the attempt
// count, the wait budget, or the request context is exhausted.
type attemptFailureFunc func(attempt failover.Attempt, target *Target, err error)

// openedStream is the outcome of opening an upstream stream.
type openedStream struct {
	stream grpc.ClientStream
	target *Target
	stats  failover.Stats
	// release frees the per-attempt context of a successful stream. It is
	// never nil and must be called once the stream is no longer used.
	release      func()
	failoverFrom string
	err          error
}

func noopRelease() {}

// streamOpener opens an upstream client stream.
type streamOpener func(ctx context.Context) openedStream

// setFailoverPolicy configures retry, failover, and the selection state that
// depends on them. It must be called once after the targets are set.
//
// A least_conn pool tracks open streams and always keeps the passive cooldown,
// even when the policy is disabled: an unreachable target has no open streams
// and would otherwise be the first choice of every call.
func (p *TargetPool) setFailoverPolicy(policy failover.Policy) {
	if p == nil {
		return
	}

	p.policy = policy

	if p.mode == selectionModeLeastConn && len(p.targets) > 1 {
		p.load = failover.NewLoad(len(p.targets))
	}

	if p.failoverEngaged() {
		p.health = failover.NewHealth(len(p.targets), policy.Cooldown)
	}
}

// failoverActive reports whether the policy can make more than one attempt.
func (p *TargetPool) failoverActive() bool {
	return p != nil && p.policy.Active(len(p.targets))
}

// failoverEngaged reports whether streams are opened through the failover
// engine: the policy is active, or least_conn needs the engine's
// connect-failure feedback for its first choice.
func (p *TargetPool) failoverEngaged() bool {
	return p != nil && (p.load != nil || p.failoverActive())
}

// openStream creates a client stream starting with the selected target.
//
// Without an active failover policy it performs exactly one NewStream call on
// the selected target. With failover only NewStream itself is repeated: no
// client message has been read or forwarded before NewStream returns, and a
// NewStream error is produced before the stream headers are committed, so the
// upstream handler never ran. Once a stream exists, failures are always final
// because client messages are forwarded as they arrive.
//
// A least_conn pool counts the stream as open on its target from the attempt
// until the returned release function is called.
func (p *TargetPool) openStream(
	ctx context.Context,
	selection targetSelection,
	fullMethod string,
	callOptions []grpc.CallOption,
	onFailure attemptFailureFunc,
) openedStream {
	if p == nil || selection.index < 0 || selection.index >= len(p.targets) {
		return openedStream{
			stats:   failover.Stats{FirstIndex: -1, FinalIndex: -1},
			release: noopRelease,
			err:     status.Error(codes.Unavailable, "no gRPC target available"),
		}
	}

	if !p.failoverEngaged() {
		return p.openSingleStream(ctx, selection.index, fullMethod, callOptions)
	}

	var single [1]int

	result := openedStream{release: noopRelease}
	runner := failover.Runner{
		Policy:       p.policy,
		Health:       p.health,
		Clock:        p.clock,
		ExtraDown:    p.transientFailure,
		FreshSuccess: p.load != nil,
	}

	result.stats = runner.Run(ctx, p.attemptOrder(selection, single[:0]), func(attempt failover.Attempt) failover.Outcome {
		target := p.targets[attempt.Index]
		stream, release, outcome, err := p.tryNewStream(ctx, attempt.Index, fullMethod, callOptions)

		result.stream, result.release, result.target, result.err = stream, release, target, err

		if outcome == failover.OutcomeRetry && onFailure != nil {
			onFailure(attempt, target, err)
		}

		return outcome
	})

	if result.stats.Attempts > 1 && result.stats.FirstIndex != result.stats.FinalIndex {
		result.failoverFrom = targetDisplayName(p.targetAt(result.stats.FirstIndex))
	}

	return result
}

// attemptOrder returns the targets to try for one call, first choice first.
// A least_conn pool makes that choice here, right before the stream is
// created. buf holds the result when the policy cannot move to another target
// and no fallbacks are needed.
func (p *TargetPool) attemptOrder(selection targetSelection, buf []int) []int {
	if selection.leastConn && p.load != nil {
		selection.index = p.load.Pick(p.available)
	}

	if !p.failoverActive() || p.policy.MaxTargets == 1 {
		return append(buf, selection.index)
	}

	return p.candidateOrder(selection)
}

// candidateOrder returns all target indexes for one call, starting with the
// selected target.
func (p *TargetPool) candidateOrder(selection targetSelection) []int {
	if selection.leastConn && p.load != nil {
		if order := p.load.Order(selection.index); len(order) == len(p.targets) {
			return order
		}
	}

	return failover.Order(selection.index, len(p.targets), selection.hashed, selection.seed)
}

func (p *TargetPool) openSingleStream(ctx context.Context, index int, fullMethod string, callOptions []grpc.CallOption) openedStream {
	target := p.targets[index]
	result := openedStream{
		target:  target,
		stats:   failover.Stats{Attempts: 1, FirstIndex: index, FinalIndex: index},
		release: noopRelease,
	}

	if target == nil || target.Conn == nil {
		result.err = status.Error(codes.Unavailable, "gRPC target connection unavailable")

		return result
	}

	result.stream, result.err = target.Conn.NewStream(ctx, genericStreamDesc(), fullMethod, callOptions...)

	return result
}

// tryNewStream runs one stream-creation attempt on the target at index. With
// an active policy the attempt is bounded by the attempt timeout. The timeout
// only covers NewStream; it is disarmed before the stream is handed to the
// caller and never limits the stream lifetime.
//
// On success the returned release function frees the attempt context and the
// target's in-flight slot. A failed attempt holds neither when it returns.
func (p *TargetPool) tryNewStream(ctx context.Context, index int, fullMethod string, callOptions []grpc.CallOption) (grpc.ClientStream, func(), failover.Outcome, error) {
	target := p.targets[index]
	if target == nil || target.Conn == nil {
		return nil, noopRelease, failover.OutcomeRetry, status.Error(codes.Unavailable, "gRPC target connection unavailable")
	}

	attemptCtx, cancelAttempt := context.WithCancel(ctx)

	var timer *time.Timer
	if p.failoverActive() && p.policy.AttemptTimeout > 0 {
		timer = time.AfterFunc(p.policy.AttemptTimeout, cancelAttempt)
	}

	release := p.trackStream(index, cancelAttempt)

	stream, err := target.Conn.NewStream(attemptCtx, genericStreamDesc(), fullMethod, callOptions...)
	timedOut := timer != nil && !timer.Stop()

	if err == nil && !timedOut {
		return stream, release, failover.OutcomeDone, nil
	}

	release()

	if err == nil {
		// The timer fired after NewStream returned: the stream exists and its
		// headers may be on the wire, so this must not be repeated.
		return nil, noopRelease, failover.OutcomeStop, status.Error(codes.Unavailable, "gRPC stream creation timed out")
	}

	return nil, noopRelease, classifyStreamCreateError(err, timedOut, ctx.Err()), err
}

// trackStream counts a stream attempt as open on the target at index and
// returns the function that ends it: it frees the in-flight slot exactly once
// and cancels the attempt context. The function may be called more than once.
func (p *TargetPool) trackStream(index int, cancel context.CancelFunc) func() {
	if p.load == nil {
		return cancel
	}

	p.load.Acquire(index)

	var released atomic.Bool

	return func() {
		if released.CompareAndSwap(false, true) {
			p.load.Release(index)
		}

		cancel()
	}
}

// classifyStreamCreateError decides whether a NewStream error may be repeated
// on the same or another target.
func classifyStreamCreateError(err error, attemptTimedOut bool, parentErr error) failover.Outcome {
	if err == nil {
		return failover.OutcomeDone
	}

	if parentErr != nil {
		return failover.OutcomeStop
	}

	switch status.Code(err) {
	case codes.Unavailable:
		return failover.OutcomeRetry
	case codes.Canceled, codes.DeadlineExceeded:
		if attemptTimedOut {
			return failover.OutcomeRetry
		}
	}

	return failover.OutcomeStop
}

// transientFailure reports targets whose channel is known to be failing, so
// they are tried after healthy ones.
func (p *TargetPool) transientFailure(index int) bool {
	if index < 0 || index >= len(p.targets) {
		return false
	}

	target := p.targets[index]
	if target == nil || target.Conn == nil {
		return true
	}

	return target.Conn.GetState() == connectivity.TransientFailure
}

func (p *TargetPool) targetAt(index int) *Target {
	if p == nil || index < 0 || index >= len(p.targets) {
		return nil
	}

	return p.targets[index]
}

func targetDisplayName(target *Target) string {
	if target == nil {
		return ""
	}

	return target.Name
}

// applyFailoverStats copies attempt details into a stream result.
func (r *grpcStreamResult) applyFailoverStats(opened openedStream) {
	if r == nil {
		return
	}

	r.Attempts = opened.stats.Attempts
	r.FailoverFrom = opened.failoverFrom
}

// openPrimaryStream opens the primary stream with the pool's failover policy
// and records failed attempts on the backend span, in logs, and in metrics.
func (h *Handler) openPrimaryStream(
	ctx context.Context,
	span trace.Span,
	rpcCtx *grpcRPCContext,
	selection targetSelection,
	fullMethod string,
) openedStream {
	pool := h.pools.Primary
	opened := pool.openStream(ctx, selection, fullMethod, h.callOptions, h.attemptFailureRecorder(ctx, span, "primary", rpcCtx.reqID))
	h.recordFailoverStats(ctx, span, "primary", pool, opened)

	return opened
}

// shadowStreamOpener returns the opener used by the shadow forwarder. It runs
// inside the shadow goroutine under the shadow timeout, so retries never delay
// the primary response.
func (h *Handler) shadowStreamOpener(pool *TargetPool, selection targetSelection, fullMethod string, reqID uint64) streamOpener {
	return func(ctx context.Context) openedStream {
		span := trace.SpanFromContext(ctx)
		opened := pool.openStream(ctx, selection, fullMethod, h.callOptions, h.attemptFailureRecorder(ctx, span, "shadow", reqID))
		h.recordFailoverStats(ctx, span, "shadow", pool, opened)

		return opened
	}
}

func (h *Handler) attemptFailureRecorder(ctx context.Context, span trace.Span, backend string, reqID uint64) attemptFailureFunc {
	return func(attempt failover.Attempt, target *Target, err error) {
		errText := ""
		if err != nil {
			errText = err.Error()
		}

		if span != nil && span.IsRecording() {
			span.AddEvent("backend.attempt.failed", trace.WithAttributes(
				attribute.Int("doppelgaenger.backend.attempt", attempt.Number),
				attribute.String("doppelgaenger.backend.selected", targetDisplayName(target)),
				attribute.String("error.message", errText),
			))
		}

		if h == nil || h.logger == nil {
			return
		}

		level := slog.LevelWarn
		if backend == "shadow" {
			level = slog.LevelDebug
		}

		h.logger.Log(ctx, level, "backend_attempt_failed",
			"protocol", ProtocolName,
			"backend", backend,
			"req_id", reqID,
			"attempt", attempt.Number,
			"selected", targetDisplayName(target),
			"err", errText,
		)
	}
}

func (h *Handler) recordFailoverStats(ctx context.Context, span trace.Span, backend string, pool *TargetPool, opened openedStream) {
	if !pool.failoverEngaged() {
		return
	}

	// The span was started before the final target was known.
	if span != nil && span.IsRecording() && opened.target != nil {
		span.SetAttributes(attribute.String("server.address", opened.target.Address))
	}

	if !pool.failoverActive() {
		return
	}

	stats := opened.stats

	if span != nil && span.IsRecording() {
		span.SetAttributes(attribute.Int("doppelgaenger.backend.attempts", stats.Attempts))
	}

	if h != nil {
		h.observability.ObserveFailoverStats(ctx, ProtocolName, backend, stats.Retries, stats.Failovers, stats.Exhausted)
	}
}
