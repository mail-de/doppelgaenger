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
	active bool
}

func (f *failoverRuntime) init(backendCount int) {
	f.active = f.policy.Active(backendCount)
	if f.active {
		f.health = failover.NewHealth(backendCount, f.policy.Cooldown)
	}
}

func (f *failoverRuntime) runner() failover.Runner {
	return failover.Runner{Policy: f.policy, Health: f.health, Clock: f.clock}
}

// connTracker records the connection phases of one client.Do call.
//
// The transport may run several internal tries for one call. GetConn starts a
// try and resets gotConn, so gotConn describes the last try only. everGotConn
// is sticky: it records that an earlier try held a connection, after which the
// request may already have reached that backend.
type connTracker struct {
	getConn     atomic.Bool
	gotConn     atomic.Bool
	everGotConn atomic.Bool
}

func (c *connTracker) clientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GetConn: func(string) {
			c.getConn.Store(true)
			c.gotConn.Store(false)
		},
		GotConn: func(httptrace.GotConnInfo) {
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
	}
}

type connState struct {
	getConn     bool
	gotConn     bool
	everGotConn bool
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

	stats = p.failover.runner().Run(ctx, p.candidateOrder(item, first), func(attempt failover.Attempt) failover.Outcome {
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

	seen := make([]bool, n)
	for _, index := range order {
		if index < 0 || index >= n || seen[index] {
			return false
		}

		seen[index] = true
	}

	return true
}

func (p *requester) tryBackend(ctx context.Context, item Request, span trace.Span, start time.Time, attempt failover.Attempt) (Result, backendObservation, failover.Outcome) {
	observation := backendObservation{
		status: observability.StatusError,
		result: observability.ResultError,
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
	outcome := classifyHTTPAttempt(result.Err, tracker.state(), transportReplayable(req), ctx.Err())

	if outcome == failover.OutcomeRetry {
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
