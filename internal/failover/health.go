package failover

import (
	"sync/atomic"
	"time"
)

// Health tracks a passive per-target cooldown after connect failures.
//
// A target in cooldown is not removed from the candidate order; it is moved to
// the end. When every target is in cooldown the original order is kept, so the
// pool always fails open and never rejects a request on stale health state.
type Health struct {
	downUntil []atomic.Int64
	cooldown  time.Duration
	now       func() time.Time
}

// NewHealth creates cooldown tracking for n targets. It returns nil when the
// cooldown is disabled; all methods accept a nil receiver.
func NewHealth(n int, cooldown time.Duration) *Health {
	return newHealthWithClock(n, cooldown, time.Now)
}

func newHealthWithClock(n int, cooldown time.Duration, now func() time.Time) *Health {
	if n <= 0 || cooldown <= 0 {
		return nil
	}

	if now == nil {
		now = time.Now
	}

	return &Health{
		downUntil: make([]atomic.Int64, n),
		cooldown:  cooldown,
		now:       now,
	}
}

// MarkFailure starts the cooldown for target i.
func (h *Health) MarkFailure(i int) {
	if h == nil || i < 0 || i >= len(h.downUntil) {
		return
	}

	h.downUntil[i].Store(h.now().Add(h.cooldown).UnixNano())
}

// MarkSuccess clears the cooldown for target i.
func (h *Health) MarkSuccess(i int) {
	if h == nil || i < 0 || i >= len(h.downUntil) {
		return
	}

	// Compare-and-swap keeps a cooldown that a concurrent failure just started.
	if until := h.downUntil[i].Load(); until != 0 {
		h.downUntil[i].CompareAndSwap(until, 0)
	}
}

// Available reports whether target i is outside its cooldown.
func (h *Health) Available(i int) bool {
	if h == nil || i < 0 || i >= len(h.downUntil) {
		return true
	}

	until := h.downUntil[i].Load()

	return until == 0 || h.now().UnixNano() >= until
}

// Arrange returns order with available targets first and targets in cooldown
// (or reported down by extraDown) last. The relative order inside both groups
// is preserved. The input slice is not modified.
func (h *Health) Arrange(order []int, extraDown func(int) bool) []int {
	if len(order) == 0 {
		return order
	}

	if h == nil && extraDown == nil {
		return order
	}

	available := make([]int, 0, len(order))
	down := make([]int, 0, len(order))

	for _, index := range order {
		if h.Available(index) && (extraDown == nil || !extraDown(index)) {
			available = append(available, index)

			continue
		}

		down = append(down, index)
	}

	if len(down) == 0 {
		return order
	}

	return append(available, down...)
}
