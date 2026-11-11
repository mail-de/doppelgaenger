package backend

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"doppelgaenger/internal/failover"
	"doppelgaenger/internal/observability"
)

const protocolHTTPLabel = "http"

// RequesterOption customizes a backend requester.
type RequesterOption func(*requester)

// WithFailover enables connect-phase retry and failover. Only attempts whose
// request provably never reached a backend are repeated (see
// classifyHTTPAttempt); responses of any status and failures after a
// connection was obtained are always final.
//
// A selector that tracks attempts (least_conn) uses the policy's cooldown even
// when the policy is disabled: it then makes a single attempt, but a failure
// to reach the backend still keeps it out of the first choice for the cooldown.
func WithFailover(policy failover.Policy) RequesterOption {
	return func(p *requester) {
		p.failover.policy = policy
	}
}

// WithLogger sets the logger used for per-attempt failure records.
func WithLogger(logger *slog.Logger) RequesterOption {
	return func(p *requester) {
		p.logger = logger
	}
}

func withFailoverClock(clock failover.Clock) RequesterOption {
	return func(p *requester) {
		p.failover.clock = clock
	}
}

type failoverRuntime struct {
	policy failover.Policy
	health *failover.Health
	clock  failover.Clock
	// active reports that the policy can make more than one attempt.
	active bool
	// engaged reports that requests run through the failover engine: the
	// policy is active, or the selector needs the engine's connect-failure
	// feedback to keep unreachable backends out of its first choice.
	engaged bool
	// tracked reports that the selector balances on attempts in flight.
	tracked bool
}

func (f *failoverRuntime) init(backendCount int, tracked bool) {
	f.active = f.policy.Active(backendCount)
	f.engaged = f.active || tracked
	f.tracked = tracked

	if f.engaged {
		f.health = failover.NewHealth(backendCount, f.policy.Cooldown)
	}
}

func (f *failoverRuntime) runner() failover.Runner {
	return failover.Runner{Policy: f.policy, Health: f.health, Clock: f.clock, FreshSuccess: f.tracked}
}

// connTracker records the connection phases of one client.Do call.
//
// The transport may run several internal tries for one call. GetConn starts a
// try and resets gotConn, so gotConn describes the last try only. everGotConn
// is sticky: it records that an earlier try held a connection, after which the
// request may already have reached that backend. reused tells whether the last
// connection came from the idle pool.
type connTracker struct {
	getConn     atomic.Bool
	gotConn     atomic.Bool
	everGotConn atomic.Bool
	reused      atomic.Bool
}

func (c *connTracker) clientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GetConn: func(string) {
			c.getConn.Store(true)
			c.gotConn.Store(false)
		},
		GotConn: func(info httptrace.GotConnInfo) {
			c.reused.Store(info.Reused)
			c.gotConn.Store(true)
			c.everGotConn.Store(true)
		},
	}
}

func (c *connTracker) state() connState {
	return connState{
		getConn:     c.getConn.Load(),
		gotConn:     c.gotConn.Load(),
		everGotConn: c.everGotConn.Load(),
		reused:      c.reused.Load(),
	}
}

type connState struct {
	getConn     bool
	gotConn     bool
	everGotConn bool
	reused      bool
}

// classifyHTTPAttempt decides whether a failed attempt may be repeated.
//
// An attempt is repeated only when the parent context is still alive, the
// transport started connecting (so the error is not a local request error),
// and the last internal try never obtained a connection.
//
// net/http retries a request internally on a new connection after a reused
// connection failed. For a non-replayable request it does so only when the
// earlier backend provably did not process it: nothing was written (HTTP/1.1),
// or the backend refused the stream with REFUSED_STREAM or a GOAWAY below its
// stream ID (HTTP/2, see RFC 9113 section 8.7). For a replayable request (GET,
// HEAD, OPTIONS, TRACE, or an Idempotency-Key header, see http.Transport) it
// may also do so after the request was sent, so the earlier backend may have
// received it; such an attempt is never moved to another backend.
func classifyHTTPAttempt(err error, conn connState, replayable bool, parentErr error) failover.Outcome {
	switch {
	case err == nil:
		return failover.OutcomeDone
	case parentErr != nil:
		return failover.OutcomeStop
	case !conn.getConn:
		return failover.OutcomeStop
	case conn.gotConn:
		return failover.OutcomeStop
	case conn.everGotConn && replayable:
		return failover.OutcomeStop
	default:
		return failover.OutcomeRetry
	}
}

// brokenExchange reports an attempt that reached the transport and ended
// without a response, although the caller was still waiting: for example a
// reset or closed connection, or a protocol error.
//
// Two kinds of failure do not count. A timeout: a slow backend keeps its
// requests in flight and is avoided by its load alone. And a failure on a
// reused connection: a healthy backend closes idle connections, and a request
// that was written at that moment fails without saying anything about the
// backend. A backend that is really gone fails the next, fresh connection.
func brokenExchange(err error, conn connState, parentErr error) bool {
	if err == nil || parentErr != nil || !conn.getConn {
		return false
	}

	if conn.gotConn && conn.reused {
		return false
	}

	var timeout interface{ Timeout() bool }

	return !errors.As(err, &timeout) || !timeout.Timeout()
}

// transportReplayable mirrors the http.Transport definition of requests it may
// replay after they were written. Header names are matched case-insensitively,
// which treats more requests as replayable and is therefore conservative.
func transportReplayable(req *http.Request) bool {
	switch req.Method {
	case "", http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	}

	for name := range req.Header {
		if strings.EqualFold(name, "Idempotency-Key") || strings.EqualFold(name, "X-Idempotency-Key") {
			return true
		}
	}

	return false
}

func (p *requester) doWithFailover(item Request) Result {
	start := time.Now()

	ctx := requestContext(item)
	ctx, span := p.startBackendSpan(ctx, item)
	observation := backendObservation{
		status: observability.StatusError,
		result: observability.ResultError,
	}

	var (
		result Result
		stats  failover.Stats
	)

	defer func() {
		p.observeFailover(ctx, span, stats)
		p.finishBackendObservation(ctx, item, span, start, observation)
	}()

	first := p.selectIndex(item)
	if first < 0 {
		observation.err = errors.New(string(p.kind) + " backend has no configured base URL")

		return p.errorResult(item, "", observation.err, start)
	}

	// Fallbacks are only needed when the policy may move to another backend.
	var single [1]int

	order := single[:]
	order[0] = first

	if p.failover.active && p.failover.policy.MaxTargets != 1 {
		order = p.candidateOrder(item, first)
	}

	stats = p.failover.runner().Run(ctx, order, func(attempt failover.Attempt) failover.Outcome {
		var outcome failover.Outcome

		result, observation, outcome = p.tryBackend(ctx, item, span, start, attempt)

		return outcome
	})

	result.Attempts = stats.Attempts
	if stats.Attempts > 1 && stats.FirstIndex != stats.FinalIndex {
		result.FailoverFrom = redactedBase(p.bases, stats.FirstIndex)
	}

	return result
}

func (p *requester) candidateOrder(item Request, first int) []int {
	if orderer, ok := p.selector.(FallbackOrderer); ok {
		if order := orderer.Order(item, first, len(p.bases)); validOrder(order, first, len(p.bases)) {
			return order
		}
	}

	return failover.Order(first, len(p.bases), false, 0)
}

func validOrder(order []int, first, n int) bool {
	if len(order) != n || n == 0 || order[0] != first {
		return false
	}

	// A bit set keeps the check off the heap for every realistic pool size.
	if n <= 64 {
		return distinctIndexes(order, n)
	}

	seen := make([]bool, n)
	for _, index := range order {
		if index < 0 || index >= n || seen[index] {
			return false
		}

		seen[index] = true
	}

	return true
}

// distinctIndexes reports whether order holds distinct indexes below n, for
// n up to 64.
func distinctIndexes(order []int, n int) bool {
	var seen uint64

	for _, index := range order {
		if index < 0 || index >= n || seen&(1<<uint(index)) != 0 {
			return false
		}

		seen |= 1 << uint(index)
	}

	return true
}

func (p *requester) tryBackend(ctx context.Context, item Request, span trace.Span, start time.Time, attempt failover.Attempt) (Result, backendObservation, failover.Outcome) {
	observation := backendObservation{
		status: observability.StatusError,
		result: observability.ResultError,
	}

	if p.tracker != nil {
		p.tracker.Acquire(attempt.Index)
		defer p.tracker.Release(attempt.Index)
	}

	base := p.bases[attempt.Index]
	tracker := &connTracker{}
	attemptCtx := httptrace.WithClientTrace(ctx, tracker.clientTrace())

	req, selected, err := p.newBackendRequest(attemptCtx, item, base, span)
	if err != nil {
		observation.err = err

		return p.errorResult(item, selected, err, start), observation, failover.OutcomeStop
	}

	result := p.executeRequestOnce(req, item, selected, start, &observation)
	state := tracker.state()
	outcome := classifyHTTPAttempt(result.Err, state, transportReplayable(req), ctx.Err())

	if outcome == failover.OutcomeRetry {
		p.recordAttemptFailure(ctx, span, item, attempt, selected, result.Err)
	}

	// A backend that accepts connections and then drops the exchange answers
	// fast and has nothing in flight, so least_conn would send it most of the
	// traffic. Such a failure is never repeated, but it starts the cooldown.
	if p.tracker != nil && outcome == failover.OutcomeStop && brokenExchange(result.Err, state, ctx.Err()) {
		p.failover.health.MarkFailure(attempt.Index)
		p.recordAttemptFailure(ctx, span, item, attempt, selected, result.Err)
	}

	return result, observation, outcome
}

func (p *requester) recordAttemptFailure(ctx context.Context, span trace.Span, item Request, attempt failover.Attempt, selected string, err error) {
	errText := ""
	if err != nil {
		errText = err.Error()
	}

	if span != nil {
		span.AddEvent("backend.attempt.failed", trace.WithAttributes(
			attribute.Int("doppelgaenger.backend.attempt", attempt.Number),
			attribute.String("doppelgaenger.backend.selected", selected),
			attribute.String("error.message", errText),
		))
	}

	if p.logger == nil {
		return
	}

	level := slog.LevelWarn
	if p.kind == BackendShadow {
		level = slog.LevelDebug
	}

	p.logger.Log(ctx, level, "backend_attempt_failed",
		"protocol", protocolHTTPLabel,
		"backend", string(p.kind),
		"req_id", item.RequestID,
		"attempt", attempt.Number,
		"selected", selected,
		"err", errText,
	)
}

func (p *requester) observeFailover(ctx context.Context, span trace.Span, stats failover.Stats) {
	if !p.failover.active {
		return
	}

	if span != nil && stats.Attempts > 0 {
		span.SetAttributes(attribute.Int("doppelgaenger.backend.attempts", stats.Attempts))
	}

	p.obs.ObserveFailoverStats(ctx, protocolHTTPLabel, string(p.kind), stats.Retries, stats.Failovers, stats.Exhausted)
}

// redactedBase returns the redacted backend URL at index, or an empty string.
func redactedBase(bases []*url.URL, index int) string {
	if index < 0 || index >= len(bases) || bases[index] == nil {
		return ""
	}

	return bases[index].Redacted()
}
