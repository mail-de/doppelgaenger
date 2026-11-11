// Package failover implements connect-phase retry and failover over an ordered
// list of backend targets.
//
// The package is protocol neutral. Callers decide which failures are safe to
// repeat; the engine only orders candidates, applies exponential backoff
// between rounds, enforces the attempt and wait budgets, and tracks a passive
// per-target cooldown. It must only be used for failures where the request
// provably never reached the backend, because every repeated attempt sends the
// same request again.
package failover

import (
	"context"
	"math"
	"math/rand/v2"
	"time"
)

// Policy configures retry and failover behavior for one backend class.
type Policy struct {
	// Enabled turns the engine on. A disabled policy keeps the legacy
	// single-attempt behavior.
	Enabled bool
	// MaxTargets limits how many distinct targets one round may try.
	// 0 means all targets, 1 disables failover (retry the selected target only).
	MaxTargets int
	// Retries is the number of extra rounds after the first one.
	Retries int
	// BackoffInitial is the delay before the first extra round.
	BackoffInitial time.Duration
	// BackoffMultiplier grows the delay exponentially per round.
	BackoffMultiplier float64
	// BackoffMax caps a single backoff delay.
	BackoffMax time.Duration
	// BackoffJitter randomizes each delay by +/- this fraction (0..1).
	BackoffJitter float64
	// MaxWait is the total time budget after which no new attempt starts.
	// 0 means no budget beyond the attempt count and the request context.
	MaxWait time.Duration
	// Cooldown keeps a target at the end of the candidate order after a
	// connect failure. 0 disables the passive cooldown.
	Cooldown time.Duration
	// AttemptTimeout bounds how long one connect attempt may wait. Protocols
	// with their own dial timeout may ignore it.
	AttemptTimeout time.Duration
}

// Active reports whether the policy can ever make more than one attempt for a
// pool with targetCount targets.
func (p Policy) Active(targetCount int) bool {
	if !p.Enabled || targetCount <= 0 {
		return false
	}

	if p.Retries > 0 {
		return true
	}

	return targetCount > 1 && p.MaxTargets != 1
}

// Delay returns the backoff before the extra round with the given zero-based
// index. rnd must return values in [0,1); nil disables jitter.
func (p Policy) Delay(round int, rnd func() float64) time.Duration {
	if p.BackoffInitial <= 0 {
		return 0
	}

	multiplier := p.BackoffMultiplier
	if multiplier < 1 {
		multiplier = 1
	}

	maxDelay := p.BackoffMax
	if maxDelay < p.BackoffInitial {
		maxDelay = p.BackoffInitial
	}

	delay := float64(p.BackoffInitial) * math.Pow(multiplier, float64(max(round, 0)))
	if math.IsInf(delay, 0) || math.IsNaN(delay) || delay > float64(maxDelay) {
		delay = float64(maxDelay)
	}

	if p.BackoffJitter > 0 && rnd != nil {
		jitter := math.Min(p.BackoffJitter, 1)
		delay *= 1 + jitter*(2*rnd()-1)
	}

	if delay > float64(maxDelay) {
		delay = float64(maxDelay)
	}

	if delay < 0 {
		delay = 0
	}

	return time.Duration(delay)
}

// Order returns the candidate target indexes for a pool of n targets. The
// first entry is always first. Without hashing the fallbacks follow ring order.
// With hashing the fallbacks are a deterministic permutation derived from
// seed, so clients of a failed target spread over the remaining targets while
// each client keeps a stable fallback.
func Order(first, n int, hashed bool, seed uint64) []int {
	if n <= 0 {
		return nil
	}

	if first < 0 || first >= n {
		first = 0
	}

	order := make([]int, 0, n)
	order = append(order, first)

	for offset := 1; offset < n; offset++ {
		order = append(order, (first+offset)%n)
	}

	if !hashed || n <= 2 {
		return order
	}

	rest := order[1:]
	state := seed

	for i := len(rest) - 1; i > 0; i-- {
		state = splitMix64(state)
		j := int(state % uint64(i+1))
		rest[i], rest[j] = rest[j], rest[i]
	}

	return order
}

func splitMix64(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb

	return x ^ (x >> 31)
}

// Outcome classifies one attempt.
type Outcome int

const (
	// OutcomeDone means a connection was obtained. The attempt's result is
	// final, whatever it is, and the target is considered healthy.
	OutcomeDone Outcome = iota
	// OutcomeRetry means the attempt failed before the request left the
	// proxy. Another attempt is safe.
	OutcomeRetry
	// OutcomeStop means the attempt failed for a reason that must not be
	// retried and says nothing about target health (for example a cancelled
	// request context).
	OutcomeStop
)

// Attempt describes one try.
type Attempt struct {
	// Index is the target index in the pool.
	Index int
	// Number is the one-based attempt number across all rounds.
	Number int
	// Round is the zero-based round number.
	Round int
}

// Stats summarizes a Run.
type Stats struct {
	// Attempts is the number of attempts that were started.
	Attempts int
	// FirstIndex is the target index of the first attempt, or -1.
	FirstIndex int
	// FinalIndex is the target index of the last attempt, or -1.
	FinalIndex int
	// Retries counts extra attempts on the same target as the previous attempt.
	Retries int
	// Failovers counts extra attempts on a different target than the previous attempt.
	Failovers int
	// Exhausted reports that the last attempt failed with OutcomeRetry.
	Exhausted bool
	// Waited is the total time spent in backoff sleeps.
	Waited time.Duration
}

// Clock abstracts time for tests.
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

type realClock struct{}

func (realClock) Now() time.Time {
	return time.Now()
}

func (realClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// RealClock returns the wall clock implementation.
func RealClock() Clock {
	return realClock{}
}

// Runner executes attempts according to a policy.
type Runner struct {
	// Policy controls attempts, rounds, and backoff.
	Policy Policy
	// Health is the optional passive cooldown tracker.
	Health *Health
	// Clock is the time source; nil uses the wall clock.
	Clock Clock
	// ExtraDown optionally reports targets known to be down by other means.
	// Such targets are moved to the end of each round like targets in cooldown.
	ExtraDown func(index int) bool
	// FreshSuccess lets a success end a cooldown only when its attempt
	// started after the failure. Pools that select by load set it: a target
	// that is shutting down finishes its old requests while it refuses new
	// ones, has the fewest requests in flight, and would be selected again
	// with every request that still completes.
	FreshSuccess bool
}

// Run calls try for candidates in order until an attempt returns OutcomeDone or
// OutcomeStop, or until rounds, the wait budget, or ctx are exhausted. order
// must contain valid, distinct target indexes.
func (r Runner) Run(ctx context.Context, order []int, try func(Attempt) Outcome) Stats {
	stats := Stats{FirstIndex: -1, FinalIndex: -1}
	if len(order) == 0 || try == nil {
		return stats
	}

	if ctx == nil {
		ctx = context.Background()
	}

	clock := r.Clock
	if clock == nil {
		clock = realClock{}
	}

	p := r.Policy
	start := clock.Now()
	rounds := 1

	if p.Enabled && p.Retries > 0 {
		rounds += p.Retries
	}

	for round := 0; round < rounds; round++ {
		if round > 0 && !waitBeforeRound(ctx, p, clock, start, round, &stats) {
			break
		}

		if !r.runRound(ctx, clock, start, round, order, &stats, try) {
			break
		}
	}

	// A request that ended because its context ended was abandoned by the
	// caller, not exhausted by unreachable targets.
	if ctx.Err() != nil {
		stats.Exhausted = false
	}

	return stats
}

// runRound tries the round's candidates and reports whether another round may follow.
func (r Runner) runRound(ctx context.Context, clock Clock, start time.Time, round int, order []int, stats *Stats, try func(Attempt) Outcome) bool {
	for _, index := range r.roundCandidates(order) {
		if stats.Attempts > 0 && !mayStartAttempt(ctx, r.Policy, clock, start) {
			return false
		}

		mark := r.Health.mark(index)
		outcome := runAttempt(stats, round, index, try)
		stats.Exhausted = outcome == OutcomeRetry

		if outcome != OutcomeRetry {
			if outcome == OutcomeDone {
				r.markSuccess(index, mark)
			}

			return false
		}

		r.Health.MarkFailure(index)
	}

	return true
}

// markSuccess ends the cooldown of a target after a successful attempt. mark
// is the target's cooldown state from before the attempt.
func (r Runner) markSuccess(index int, mark int64) {
	if r.FreshSuccess {
		r.Health.markSuccessSince(index, mark)

		return
	}

	r.Health.MarkSuccess(index)
}

func runAttempt(stats *Stats, round, index int, try func(Attempt) Outcome) Outcome {
	switch {
	case stats.Attempts == 0:
		stats.FirstIndex = index
	case index == stats.FinalIndex:
		stats.Retries++
	default:
		stats.Failovers++
	}

	stats.Attempts++
	stats.FinalIndex = index

	return try(Attempt{Index: index, Number: stats.Attempts, Round: round})
}

func (r Runner) roundCandidates(order []int) []int {
	p := r.Policy

	// A disabled policy and max_targets=1 both pin the request to the selected
	// target; health state must never move it elsewhere.
	if !p.Enabled || p.MaxTargets == 1 {
		return order[:1]
	}

	candidates := order
	if r.Health != nil || r.ExtraDown != nil {
		candidates = r.Health.Arrange(order, r.ExtraDown)
	}

	if p.MaxTargets > 0 && p.MaxTargets < len(candidates) {
		candidates = candidates[:p.MaxTargets]
	}

	return candidates
}

func mayStartAttempt(ctx context.Context, p Policy, clock Clock, start time.Time) bool {
	if ctx.Err() != nil {
		return false
	}

	return p.MaxWait <= 0 || clock.Now().Sub(start) < p.MaxWait
}

func waitBeforeRound(ctx context.Context, p Policy, clock Clock, start time.Time, round int, stats *Stats) bool {
	if !mayStartAttempt(ctx, p, clock, start) {
		return false
	}

	delay := p.Delay(round-1, rand.Float64)

	// Never sleep into the end of the budget: an attempt after such a sleep
	// would not be allowed to start, so the sleep would only add latency.
	if p.MaxWait > 0 && clock.Now().Sub(start)+delay >= p.MaxWait {
		return false
	}

	sleepStart := clock.Now()
	err := clock.Sleep(ctx, delay)
	stats.Waited += clock.Now().Sub(sleepStart)

	return err == nil
}
