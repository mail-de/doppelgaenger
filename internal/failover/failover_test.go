package failover

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
	// cancelOnSleep is called before a sleep returns, to simulate a context
	// that ends while the engine waits.
	cancelOnSleep context.CancelFunc
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1_700_000_000, 0)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	cancel := c.cancelOnSleep
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	return ctx.Err()
}

func enabledPolicy() Policy {
	return Policy{
		Enabled:           true,
		BackoffInitial:    100 * time.Millisecond,
		BackoffMultiplier: 2,
		BackoffMax:        time.Second,
		MaxWait:           10 * time.Second,
	}
}

func TestPolicyActive(t *testing.T) {
	tests := []struct {
		name    string
		policy  Policy
		targets int
		want    bool
	}{
		{name: "disabled", policy: Policy{Retries: 3}, targets: 3, want: false},
		{name: "no targets", policy: Policy{Enabled: true, Retries: 1}, targets: 0, want: false},
		{name: "single target without retries", policy: Policy{Enabled: true}, targets: 1, want: false},
		{name: "single target with retries", policy: Policy{Enabled: true, Retries: 1}, targets: 1, want: true},
		{name: "failover over many targets", policy: Policy{Enabled: true}, targets: 2, want: true},
		{name: "retry only pinned without retries", policy: Policy{Enabled: true, MaxTargets: 1}, targets: 3, want: false},
		{name: "retry only pinned with retries", policy: Policy{Enabled: true, MaxTargets: 1, Retries: 2}, targets: 3, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.policy.Active(tt.targets); got != tt.want {
				t.Fatalf("Active(%d) = %v, want %v", tt.targets, got, tt.want)
			}
		})
	}
}

func TestPolicyDelayGrowsExponentiallyAndCaps(t *testing.T) {
	policy := Policy{BackoffInitial: 100 * time.Millisecond, BackoffMultiplier: 2, BackoffMax: 500 * time.Millisecond}
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 500 * time.Millisecond, 500 * time.Millisecond}

	for round, expected := range want {
		if got := policy.Delay(round, nil); got != expected {
			t.Fatalf("Delay(%d) = %s, want %s", round, got, expected)
		}
	}

	if got := policy.Delay(10_000, nil); got != 500*time.Millisecond {
		t.Fatalf("Delay with huge round = %s, want cap", got)
	}
}

func TestPolicyDelayJitterStaysInBounds(t *testing.T) {
	policy := Policy{BackoffInitial: 100 * time.Millisecond, BackoffMultiplier: 2, BackoffMax: time.Second, BackoffJitter: 0.5}

	for _, sample := range []float64{0, 0.25, 0.5, 0.75, 0.999999} {
		got := policy.Delay(1, func() float64 { return sample })
		if got < 100*time.Millisecond || got > 300*time.Millisecond {
			t.Fatalf("jittered delay %s outside [100ms,300ms] for sample %v", got, sample)
		}
	}

	capped := policy.Delay(20, func() float64 { return 0.999999 })
	if capped > time.Second {
		t.Fatalf("jitter exceeded backoff_max: %s", capped)
	}
}

func TestOrderRing(t *testing.T) {
	got := Order(2, 4, false, 0)
	if !slices.Equal(got, []int{2, 3, 0, 1}) {
		t.Fatalf("ring order = %v", got)
	}

	if got := Order(9, 3, false, 0); got[0] != 0 {
		t.Fatalf("invalid first index must fall back to 0, got %v", got)
	}

	if got := Order(0, 0, false, 0); got != nil {
		t.Fatalf("empty pool must return nil, got %v", got)
	}
}

func TestOrderHashedIsStablePermutationAndSpreads(t *testing.T) {
	const targets = 5

	secondChoices := map[int]int{}

	for seed := uint64(0); seed < 2000; seed++ {
		first := int(seed % targets)
		order := Order(first, targets, true, seed)

		if order[0] != first {
			t.Fatalf("first entry changed: %v", order)
		}

		sorted := slices.Clone(order)
		slices.Sort(sorted)

		if !slices.Equal(sorted, []int{0, 1, 2, 3, 4}) {
			t.Fatalf("order is not a permutation: %v", order)
		}

		if again := Order(first, targets, true, seed); !slices.Equal(order, again) {
			t.Fatalf("order not deterministic: %v vs %v", order, again)
		}

		if first == 0 {
			secondChoices[order[1]]++
		}
	}

	// Clients of a failed target 0 must spread over all surviving targets.
	for target := 1; target < targets; target++ {
		if secondChoices[target] == 0 {
			t.Fatalf("no fallback traffic to target %d: %v", target, secondChoices)
		}
	}
}

func TestRunStopsAtFirstDone(t *testing.T) {
	var tried []int

	stats := Runner{Policy: enabledPolicy(), Clock: newFakeClock()}.Run(context.Background(), []int{1, 2, 0}, func(a Attempt) Outcome {
		tried = append(tried, a.Index)
		if a.Index == 2 {
			return OutcomeDone
		}

		return OutcomeRetry
	})

	if !slices.Equal(tried, []int{1, 2}) {
		t.Fatalf("tried %v, want [1 2]", tried)
	}

	if stats.Attempts != 2 || stats.FirstIndex != 1 || stats.FinalIndex != 2 || stats.Failovers != 1 || stats.Retries != 0 || stats.Exhausted {
		t.Fatalf("unexpected stats %+v", stats)
	}
}

func TestRunStopOutcomeEndsImmediately(t *testing.T) {
	calls := 0

	stats := Runner{Policy: enabledPolicy(), Clock: newFakeClock()}.Run(context.Background(), []int{0, 1, 2}, func(Attempt) Outcome {
		calls++

		return OutcomeStop
	})

	if calls != 1 || stats.Exhausted {
		t.Fatalf("calls=%d stats=%+v", calls, stats)
	}
}

func TestRunRoundsWithBackoff(t *testing.T) {
	policy := enabledPolicy()
	policy.Retries = 2
	clock := newFakeClock()

	var tried []int

	stats := Runner{Policy: policy, Clock: clock}.Run(context.Background(), []int{0, 1}, func(a Attempt) Outcome {
		tried = append(tried, a.Index)

		return OutcomeRetry
	})

	if !slices.Equal(tried, []int{0, 1, 0, 1, 0, 1}) {
		t.Fatalf("tried %v", tried)
	}

	if !slices.Equal(clock.sleeps, []time.Duration{100 * time.Millisecond, 200 * time.Millisecond}) {
		t.Fatalf("sleeps %v", clock.sleeps)
	}

	if !stats.Exhausted || stats.Attempts != 6 || stats.Waited != 300*time.Millisecond {
		t.Fatalf("unexpected stats %+v", stats)
	}
}

func TestRunRetryOnlyKeepsTargetDespiteCooldown(t *testing.T) {
	policy := enabledPolicy()
	policy.MaxTargets = 1
	policy.Retries = 2
	clock := newFakeClock()
	health := newHealthWithClock(3, time.Minute, clock.Now)

	var tried []int

	stats := Runner{Policy: policy, Health: health, Clock: clock}.Run(context.Background(), []int{1, 2, 0}, func(a Attempt) Outcome {
		tried = append(tried, a.Index)

		return OutcomeRetry
	})

	if !slices.Equal(tried, []int{1, 1, 1}) {
		t.Fatalf("tried %v, want pinned target", tried)
	}

	if stats.Retries != 2 || stats.Failovers != 0 {
		t.Fatalf("unexpected stats %+v", stats)
	}
}

func TestRunMaxTargetsLimitsRound(t *testing.T) {
	policy := enabledPolicy()
	policy.MaxTargets = 2

	calls := 0

	Runner{Policy: policy, Clock: newFakeClock()}.Run(context.Background(), []int{0, 1, 2, 3}, func(Attempt) Outcome {
		calls++

		return OutcomeRetry
	})

	if calls != 2 {
		t.Fatalf("calls=%d, want 2", calls)
	}
}

func TestRunBudgetStopsNewAttempts(t *testing.T) {
	policy := enabledPolicy()
	policy.MaxWait = 250 * time.Millisecond
	policy.Retries = 5
	clock := newFakeClock()

	calls := 0

	stats := Runner{Policy: policy, Clock: clock}.Run(context.Background(), []int{0}, func(Attempt) Outcome {
		calls++

		clock.Advance(10 * time.Millisecond)

		return OutcomeRetry
	})

	// 10ms attempt, 100ms sleep, 10ms attempt; the next 200ms sleep would
	// cross the 250ms budget, so the engine stops instead of sleeping.
	if calls != 2 {
		t.Fatalf("calls=%d, want 2", calls)
	}

	if !slices.Equal(clock.sleeps, []time.Duration{100 * time.Millisecond}) {
		t.Fatalf("sleeps %v", clock.sleeps)
	}

	if !stats.Exhausted {
		t.Fatalf("expected exhausted stats, got %+v", stats)
	}
}

func TestRunBudgetGatesFailoverInsideRound(t *testing.T) {
	policy := enabledPolicy()
	policy.MaxWait = 50 * time.Millisecond
	clock := newFakeClock()

	calls := 0

	Runner{Policy: policy, Clock: clock}.Run(context.Background(), []int{0, 1, 2}, func(Attempt) Outcome {
		calls++

		clock.Advance(60 * time.Millisecond)

		return OutcomeRetry
	})

	if calls != 1 {
		t.Fatalf("calls=%d, want 1", calls)
	}
}

func TestRunStopsWhenContextEndsDuringSleep(t *testing.T) {
	policy := enabledPolicy()
	policy.Retries = 3
	clock := newFakeClock()
	ctx, cancel := context.WithCancel(context.Background())
	clock.cancelOnSleep = cancel

	calls := 0

	stats := Runner{Policy: policy, Clock: clock}.Run(ctx, []int{0}, func(Attempt) Outcome {
		calls++

		return OutcomeRetry
	})

	if calls != 1 {
		t.Fatalf("calls=%d, want 1", calls)
	}

	if stats.Exhausted {
		t.Fatal("a cancelled request must not count as exhausted")
	}
}

func TestRunStopsWhenContextEndsBetweenAttempts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0

	Runner{Policy: enabledPolicy(), Clock: newFakeClock()}.Run(ctx, []int{0, 1, 2}, func(Attempt) Outcome {
		calls++

		cancel()

		return OutcomeRetry
	})

	if calls != 1 {
		t.Fatalf("calls=%d, want 1", calls)
	}
}

func TestRunDisabledPolicyMakesOneAttempt(t *testing.T) {
	calls := 0

	stats := Runner{Policy: Policy{Retries: 3}, Clock: newFakeClock()}.Run(context.Background(), []int{0, 1}, func(Attempt) Outcome {
		calls++

		return OutcomeRetry
	})

	if calls != 1 || stats.Attempts != 1 {
		t.Fatalf("calls=%d stats=%+v", calls, stats)
	}
}

func TestRunCooldownMovesFailedTargetLast(t *testing.T) {
	clock := newFakeClock()
	health := newHealthWithClock(3, time.Minute, clock.Now)
	runner := Runner{Policy: enabledPolicy(), Health: health, Clock: clock}

	runner.Run(context.Background(), []int{0, 1, 2}, func(a Attempt) Outcome {
		if a.Index == 0 {
			return OutcomeRetry
		}

		return OutcomeDone
	})

	var tried []int

	runner.Run(context.Background(), []int{0, 1, 2}, func(a Attempt) Outcome {
		tried = append(tried, a.Index)

		return OutcomeDone
	})

	if !slices.Equal(tried, []int{1}) {
		t.Fatalf("expected cooled-down target to be skipped first, tried %v", tried)
	}

	clock.Advance(time.Minute)

	tried = nil

	runner.Run(context.Background(), []int{0, 1, 2}, func(a Attempt) Outcome {
		tried = append(tried, a.Index)

		return OutcomeDone
	})

	if !slices.Equal(tried, []int{0}) {
		t.Fatalf("expected recovered target after cooldown, tried %v", tried)
	}
}

func TestRunExtraDownMovesTargetLast(t *testing.T) {
	var tried []int

	Runner{
		Policy:    enabledPolicy(),
		Clock:     newFakeClock(),
		ExtraDown: func(i int) bool { return i == 0 },
	}.Run(context.Background(), []int{0, 1}, func(a Attempt) Outcome {
		tried = append(tried, a.Index)

		return OutcomeDone
	})

	if !slices.Equal(tried, []int{1}) {
		t.Fatalf("tried %v", tried)
	}
}

func TestHealthFailsOpenWhenAllTargetsAreDown(t *testing.T) {
	clock := newFakeClock()
	health := newHealthWithClock(3, time.Minute, clock.Now)

	for i := range 3 {
		health.MarkFailure(i)
	}

	if got := health.Arrange([]int{2, 0, 1}, nil); !slices.Equal(got, []int{2, 0, 1}) {
		t.Fatalf("all-down order changed: %v", got)
	}

	health.MarkSuccess(0)

	if got := health.Arrange([]int{2, 0, 1}, nil); !slices.Equal(got, []int{0, 2, 1}) {
		t.Fatalf("available target not first: %v", got)
	}
}

// Without FreshSuccess any success on a target ends its cooldown, as before
// least_conn existed.
func TestRunSuccessEndsCooldownByDefault(t *testing.T) {
	clock := newFakeClock()
	health := newHealthWithClock(2, time.Minute, clock.Now)

	Runner{Policy: enabledPolicy(), Health: health, Clock: clock}.Run(context.Background(), []int{0, 1}, func(a Attempt) Outcome {
		health.MarkFailure(a.Index)

		return OutcomeDone
	})

	if !health.Available(0) {
		t.Fatal("a success must end the cooldown when FreshSuccess is not set")
	}
}

// With FreshSuccess a request that was already running when its target went
// away must not end the cooldown: on a draining backend old requests finish
// while new ones are refused.
func TestRunFreshSuccessIgnoresStaleSuccess(t *testing.T) {
	clock := newFakeClock()
	health := newHealthWithClock(2, time.Minute, clock.Now)
	runner := Runner{Policy: enabledPolicy(), Health: health, Clock: clock, FreshSuccess: true}

	runner.Run(context.Background(), []int{0, 1}, func(a Attempt) Outcome {
		// Another request finds the target unreachable while this one runs.
		health.MarkFailure(a.Index)

		return OutcomeDone
	})

	if health.Available(0) {
		t.Fatal("a success that started before the failure cleared the cooldown")
	}

	// The pool fails open onto the target; this attempt started after the
	// failure, so its success proves the target is back.
	health.MarkFailure(1)

	stats := runner.Run(context.Background(), []int{0, 1}, func(Attempt) Outcome {
		return OutcomeDone
	})

	if stats.FinalIndex != 0 || !health.Available(0) {
		t.Fatalf("a success after the failure must clear the cooldown: stats=%+v available=%v", stats, health.Available(0))
	}
}

func TestHealthArrangeKeepsRelativeOrderAndInput(t *testing.T) {
	clock := newFakeClock()
	health := newHealthWithClock(5, time.Minute, clock.Now)
	health.MarkFailure(1)
	health.MarkFailure(3)

	order := []int{4, 3, 2, 1, 0}

	got := health.Arrange(order, func(i int) bool { return i == 0 })
	if want := []int{4, 2, 3, 1, 0}; !slices.Equal(got, want) {
		t.Fatalf("Arrange() = %v, want %v", got, want)
	}

	if !slices.Equal(order, []int{4, 3, 2, 1, 0}) {
		t.Fatalf("Arrange() modified its input: %v", order)
	}
}

// With failover enabled Arrange runs for every request, so the healthy case
// must hand the order back untouched.
func TestHealthArrangeHealthyPoolDoesNotAllocate(t *testing.T) {
	health := NewHealth(4, time.Minute)
	order := []int{2, 3, 0, 1}
	notDown := func(int) bool { return false }

	if got := health.Arrange(order, notDown); &got[0] != &order[0] || len(got) != len(order) {
		t.Fatalf("healthy Arrange() = %v, want the input slice", got)
	}

	if allocs := testing.AllocsPerRun(200, func() { _ = health.Arrange(order, notDown) }); allocs != 0 {
		t.Fatalf("healthy Arrange() allocates %v times per call, want 0", allocs)
	}
}

func TestHealthNilAndDisabled(t *testing.T) {
	if NewHealth(3, 0) != nil {
		t.Fatal("zero cooldown must disable health tracking")
	}

	var health *Health

	health.MarkFailure(0)
	health.MarkSuccess(0)

	if !health.Available(0) {
		t.Fatal("nil health must report targets available")
	}

	if got := health.Arrange([]int{1, 0}, nil); !slices.Equal(got, []int{1, 0}) {
		t.Fatalf("nil health changed order: %v", got)
	}
}

func TestHealthConcurrentAccess(t *testing.T) {
	health := NewHealth(4, time.Millisecond)

	var wg sync.WaitGroup

	for worker := range 8 {
		wg.Add(1)

		go func(worker int) {
			defer wg.Done()

			for i := range 1000 {
				index := (worker + i) % 4
				if i%2 == 0 {
					health.MarkFailure(index)
				} else {
					health.MarkSuccess(index)
				}

				_ = health.Arrange([]int{0, 1, 2, 3}, nil)
			}
		}(worker)
	}

	wg.Wait()

	if got := health.Arrange([]int{0, 1, 2, 3}, nil); len(got) != 4 {
		t.Fatalf("arrange lost targets: %v", got)
	}
}

func TestRealClockSleepHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()

	if err := RealClock().Sleep(ctx, time.Minute); err == nil {
		t.Fatal("expected context error")
	}

	if time.Since(start) > time.Second {
		t.Fatal("sleep ignored cancelled context")
	}

	if err := RealClock().Sleep(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("unexpected sleep error: %v", err)
	}
}
