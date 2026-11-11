package failover

import (
	"cmp"
	"math/rand/v2"
	"slices"
	"sync/atomic"
)

// loadStackTargets is the pool size up to which Pick and Order take their
// snapshot on the stack.
const loadStackTargets = 32

// Load counts the attempts in flight per target and orders targets by that
// count. It backs the least_conn selection mode.
//
// A slot stands for exactly one running attempt: callers Acquire right before
// an attempt starts on a target and Release once that attempt has ended,
// whatever its outcome. All methods are safe for concurrent use and accept a
// nil receiver.
//
// The counters deliberately share cache lines. Every selection reads all of
// them, so spreading them out would cost one cache miss per target instead of
// one per pool.
type Load struct {
	inFlight []atomic.Int64
}

type loadEntry struct {
	inFlight int64
	index    int
	// excluded marks a target that Pick found unavailable.
	excluded bool
}

// NewLoad creates in-flight tracking for n targets. It returns nil for an
// empty pool.
func NewLoad(n int) *Load {
	if n <= 0 {
		return nil
	}

	return &Load{inFlight: make([]atomic.Int64, n)}
}

// Len returns the number of tracked targets.
func (l *Load) Len() int {
	if l == nil {
		return 0
	}

	return len(l.inFlight)
}

// Acquire records that an attempt starts on target i.
func (l *Load) Acquire(i int) {
	if l == nil || i < 0 || i >= len(l.inFlight) {
		return
	}

	l.inFlight[i].Add(1)
}

// Release records that an attempt on target i has ended. Every Acquire must be
// matched by exactly one Release.
func (l *Load) Release(i int) {
	if l == nil || i < 0 || i >= len(l.inFlight) {
		return
	}

	l.inFlight[i].Add(-1)
}

// InFlight returns the number of attempts currently running on target i.
func (l *Load) InFlight(i int) int64 {
	if l == nil || i < 0 || i >= len(l.inFlight) {
		return 0
	}

	return l.inFlight[i].Load()
}

// Pick returns the available target with the fewest attempts in flight.
// available reports whether a target may be chosen; nil means every target.
// When no target is available the choice fails open and is made on load alone.
//
// Equally loaded targets are chosen at random. A shared turn counter would be
// cheaper to reason about, but it locks into the rhythm of the traffic: with
// overlapping requests the same targets tie again and again, and a counter
// taken modulo the number of ties then skips some of them for good.
//
// available is asked about the best candidate only, and about the next one
// when the answer is no. A healthy pool therefore costs a single call.
func (l *Load) Pick(available func(index int) bool) int {
	if l.Len() <= 1 {
		return 0
	}

	var buf [loadStackTargets]loadEntry

	entries := l.snapshot(buf[:0], -1)

	for range entries {
		best := least(entries)
		if available == nil || available(entries[best].index) {
			return entries[best].index
		}

		entries[best].excluded = true
	}

	for i := range entries {
		entries[i].excluded = false
	}

	return entries[least(entries)].index
}

// least returns the position of a least loaded entry that is not excluded,
// chosen at random among equals. At least one such entry must exist.
func least(entries []loadEntry) int {
	best, ties := -1, 0

	for i := range entries {
		switch {
		case entries[i].excluded:
		case best < 0 || entries[i].inFlight < entries[best].inFlight:
			best, ties = i, 1
		case entries[i].inFlight == entries[best].inFlight:
			ties++
		}
	}

	if ties <= 1 {
		return best
	}

	// best is the first member of the tied group; advance to the chosen one.
	group := entries[best].inFlight

	for skip := rand.IntN(ties); skip > 0; {
		best++

		if !entries[best].excluded && entries[best].inFlight == group {
			skip--
		}
	}

	return best
}

// Order returns every target index for one request: first, followed by the
// other targets with the fewest attempts in flight first. Equally loaded
// fallbacks are put in random order so they share the traffic of a failed
// first choice.
//
// Availability is not considered here. The Runner moves unavailable targets to
// the end of every round and keeps this order inside both groups.
func (l *Load) Order(first int) []int {
	n := l.Len()
	if n == 0 {
		return nil
	}

	if first < 0 || first >= n {
		first = 0
	}

	order := make([]int, 1, n)
	order[0] = first

	if n == 1 {
		return order
	}

	var buf [loadStackTargets]loadEntry

	entries := l.snapshot(buf[:0], first)
	slices.SortStableFunc(entries, compareLoad)
	shuffleTies(entries)

	for _, entry := range entries {
		order = append(order, entry.index)
	}

	return order
}

// snapshot appends the current load of every target except skip to dst.
// Working on one snapshot keeps comparisons consistent while counters change.
func (l *Load) snapshot(dst []loadEntry, skip int) []loadEntry {
	for i := range l.inFlight {
		if i != skip {
			dst = append(dst, loadEntry{inFlight: l.inFlight[i].Load(), index: i})
		}
	}

	return dst
}

// compareLoad orders targets with fewer attempts in flight first.
func compareLoad(a, b loadEntry) int {
	return cmp.Compare(a.inFlight, b.inFlight)
}

// shuffleTies rotates every run of equal entries by a random offset. entries
// must be sorted with compareLoad.
func shuffleTies(entries []loadEntry) {
	for start := 0; start < len(entries); {
		end := start + 1
		for end < len(entries) && compareLoad(entries[end], entries[start]) == 0 {
			end++
		}

		if size := end - start; size > 1 {
			rotateLeft(entries[start:end], rand.IntN(size))
		}

		start = end
	}
}

func rotateLeft(entries []loadEntry, k int) {
	if k <= 0 || k >= len(entries) {
		return
	}

	slices.Reverse(entries[:k])
	slices.Reverse(entries[k:])
	slices.Reverse(entries)
}
