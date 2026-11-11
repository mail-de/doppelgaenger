package failover

import (
	"slices"
	"sync"
	"testing"
)

func acquireN(load *Load, index, n int) {
	for range n {
		load.Acquire(index)
	}
}

func unavailable(down ...int) func(int) bool {
	return func(index int) bool {
		return !slices.Contains(down, index)
	}
}

func TestLoadPickPrefersFewestInFlight(t *testing.T) {
	load := NewLoad(3)
	acquireN(load, 0, 2)
	acquireN(load, 1, 1)
	acquireN(load, 2, 3)

	for range 5 {
		if got := load.Pick(nil); got != 1 {
			t.Fatalf("Pick() = %d, want least loaded target 1", got)
		}
	}

	load.Release(0)
	load.Release(0)

	if got := load.Pick(nil); got != 0 {
		t.Fatalf("Pick() = %d, want released target 0", got)
	}
}

// assertShares fails unless every listed target received between 80% and 120%
// of an equal share of total. The picks are random; with the totals used here
// the bounds are more than ten standard deviations wide.
func assertShares(t *testing.T, counts []int, total int, targets ...int) {
	t.Helper()

	share := total / len(targets)
	sum := 0

	for _, index := range targets {
		sum += counts[index]

		if counts[index] < share*8/10 || counts[index] > share*12/10 {
			t.Fatalf("picks per target = %v, want about %d for each of %v", counts, share, targets)
		}
	}

	if sum != total {
		t.Fatalf("picks per target = %v, want all %d picks on %v", counts, total, targets)
	}
}

func TestLoadPickSpreadsIdlePoolEvenly(t *testing.T) {
	load := NewLoad(3)
	counts := make([]int, 3)

	for range 30000 {
		counts[load.Pick(nil)]++
	}

	assertShares(t, counts, 30000, 0, 1, 2)
}

// An unavailable target must not hand its share to its neighbour: the
// survivors share its traffic evenly instead of the next target in the list
// doubling up.
func TestLoadPickSpreadsEvenlyAroundUnavailableTargets(t *testing.T) {
	tests := []struct {
		name    string
		targets int
		down    []int
		up      []int
	}{
		{name: "first of three", targets: 3, down: []int{0}, up: []int{1, 2}},
		{name: "second of four", targets: 4, down: []int{1}, up: []int{0, 2, 3}},
		{name: "two of four", targets: 4, down: []int{0, 2}, up: []int{1, 3}},
		{name: "three of six", targets: 6, down: []int{0, 1, 4}, up: []int{2, 3, 5}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			load := NewLoad(tt.targets)
			counts := make([]int, tt.targets)

			for range 30000 {
				counts[load.Pick(unavailable(tt.down...))]++
			}

			assertShares(t, counts, 30000, tt.up...)
		})
	}
}

func TestLoadPickSharesOnlyAmongTiedTargets(t *testing.T) {
	load := NewLoad(4)
	acquireN(load, 1, 5)

	counts := make([]int, 4)
	for range 30000 {
		counts[load.Pick(nil)]++
	}

	assertShares(t, counts, 30000, 0, 2, 3)
}

// Requests that overlap make the same targets tie again and again. A turn
// counter taken modulo the number of ties locks into that rhythm and starves
// some targets for good; every target must keep its share.
func TestLoadPickKeepsEveryTargetBusyWithOverlappingRequests(t *testing.T) {
	for _, tt := range []struct{ targets, overlap int }{{3, 1}, {5, 2}, {4, 1}, {2, 1}} {
		load := NewLoad(tt.targets)
		counts := make([]int, tt.targets)
		running := make([]int, 0, tt.overlap+1)

		for range 30000 {
			index := load.Pick(nil)
			load.Acquire(index)

			counts[index]++

			running = append(running, index)
			if len(running) > tt.overlap {
				load.Release(running[0])
				running = running[1:]
			}
		}

		all := make([]int, tt.targets)
		for i := range all {
			all[i] = i
		}

		assertShares(t, counts, 30000, all...)
	}
}

func TestLoadPickPrefersBusyAvailableOverIdleUnavailable(t *testing.T) {
	load := NewLoad(2)
	acquireN(load, 1, 7)

	if got := load.Pick(unavailable(0)); got != 1 {
		t.Fatalf("Pick() = %d, want available target 1", got)
	}
}

func TestLoadPickFailsOpenWhenNothingIsAvailable(t *testing.T) {
	load := NewLoad(3)
	acquireN(load, 0, 2)
	acquireN(load, 2, 1)

	if got := load.Pick(unavailable(0, 1, 2)); got != 1 {
		t.Fatalf("Pick() = %d, want least loaded target 1 despite cooldown", got)
	}
}

// Availability can be expensive to check (a gRPC channel state takes a lock),
// so a healthy pool must ask about the chosen target only.
func TestLoadPickChecksAvailabilityOfTheWinnerOnly(t *testing.T) {
	load := NewLoad(5)
	acquireN(load, 0, 3)
	acquireN(load, 1, 1)
	acquireN(load, 2, 2)
	acquireN(load, 4, 4)

	var asked []int

	got := load.Pick(func(index int) bool {
		asked = append(asked, index)

		return index != 3
	})

	// Target 3 is idle but unavailable, target 1 is the next best.
	if got != 1 || !slices.Equal(asked, []int{3, 1}) {
		t.Fatalf("Pick() = %d after asking about %v, want 1 after asking about [3 1]", got, asked)
	}
}

func TestLoadOrderStartsWithFirstThenLeastLoaded(t *testing.T) {
	load := NewLoad(4)
	acquireN(load, 0, 1)
	acquireN(load, 1, 9)
	acquireN(load, 2, 4)
	acquireN(load, 3, 2)

	if got, want := load.Order(1), []int{1, 0, 3, 2}; !slices.Equal(got, want) {
		t.Fatalf("Order(1) = %v, want %v", got, want)
	}

	if got, want := load.Order(-1), []int{0, 3, 2, 1}; !slices.Equal(got, want) {
		t.Fatalf("Order(-1) = %v, want %v with first normalized to 0", got, want)
	}
}

func TestLoadOrderIsPermutationAndSpreadsEqualFallbacks(t *testing.T) {
	load := NewLoad(4)
	acquireN(load, 3, 5)

	firstFallback := make([]int, 4)

	for range 2000 {
		order := load.Order(0)

		sorted := slices.Clone(order)
		slices.Sort(sorted)

		if order[0] != 0 || !slices.Equal(sorted, []int{0, 1, 2, 3}) {
			t.Fatalf("Order(0) = %v, want a permutation starting with 0", order)
		}

		if order[3] != 3 {
			t.Fatalf("Order(0) = %v, want busiest target 3 last", order)
		}

		firstFallback[order[1]]++
	}

	// Targets 1 and 2 are equally loaded; each must take a fair share of the
	// first fallback position (expected 1000 each, far outside random noise).
	if firstFallback[1] < 800 || firstFallback[2] < 800 {
		t.Fatalf("first fallback counts = %v, want targets 1 and 2 to share", firstFallback)
	}
}

func TestLoadSingleTargetAndNil(t *testing.T) {
	single := NewLoad(1)
	if got := single.Pick(unavailable(0)); got != 0 {
		t.Fatalf("single Pick() = %d, want 0", got)
	}

	if got := single.Order(0); !slices.Equal(got, []int{0}) {
		t.Fatalf("single Order() = %v, want [0]", got)
	}

	if NewLoad(0) != nil {
		t.Fatal("NewLoad(0) must be nil")
	}

	var load *Load

	load.Acquire(0)
	load.Release(0)

	if load.Len() != 0 || load.InFlight(0) != 0 || load.Pick(nil) != 0 || load.Order(0) != nil {
		t.Fatal("nil Load must be a no-op")
	}
}

func TestLoadIgnoresOutOfRangeIndexes(t *testing.T) {
	load := NewLoad(2)

	load.Acquire(-1)
	load.Acquire(2)
	load.Release(-1)
	load.Release(2)

	if load.InFlight(0) != 0 || load.InFlight(1) != 0 || load.InFlight(5) != 0 {
		t.Fatal("out-of-range indexes must not change counters")
	}
}

func TestLoadLargePoolUsesHeapSnapshot(t *testing.T) {
	const targets = loadStackTargets + 8

	load := NewLoad(targets)

	for i := range targets {
		acquireN(load, i, 2)
	}

	load.Release(targets - 1)
	load.Release(targets - 1)

	if got := load.Pick(nil); got != targets-1 {
		t.Fatalf("Pick() = %d, want %d", got, targets-1)
	}

	if order := load.Order(0); len(order) != targets || order[1] != targets-1 {
		t.Fatalf("Order(0) starts %v, want %d as first fallback", order[:2], targets-1)
	}
}

func TestLoadPickDoesNotAllocate(t *testing.T) {
	load := NewLoad(8)
	acquireN(load, 3, 2)

	available := unavailable(5)

	if allocs := testing.AllocsPerRun(200, func() { load.Pick(available) }); allocs != 0 {
		t.Fatalf("Pick() allocates %v times per call, want 0", allocs)
	}

	if allocs := testing.AllocsPerRun(200, func() { load.Order(0) }); allocs > 1 {
		t.Fatalf("Order() allocates %v times per call, want at most 1", allocs)
	}
}

// Concurrent attempts must balance: every slot is returned, and with all
// workers holding their slot at the same time no target is left out.
func TestLoadConcurrentAcquireRelease(t *testing.T) {
	const (
		targets = 4
		workers = 32
		rounds  = 500
	)

	load := NewLoad(targets)
	picked := make([]int64, targets)

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)

	for range workers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			local := make([]int64, targets)

			for range rounds {
				index := load.Pick(nil)
				load.Acquire(index)

				local[index]++

				_ = load.Order(index)

				load.Release(index)
			}

			mu.Lock()
			defer mu.Unlock()

			for i, n := range local {
				picked[i] += n
			}
		}()
	}

	wg.Wait()

	for i := range targets {
		if got := load.InFlight(i); got != 0 {
			t.Fatalf("target %d has %d attempts in flight after all were released", i, got)
		}

		if picked[i] == 0 {
			t.Fatalf("target %d was never picked: %v", i, picked)
		}
	}
}

func BenchmarkLoadPick(b *testing.B) {
	load := NewLoad(8)
	health := NewHealth(8, 0)

	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			index := load.Pick(health.Available)
			load.Acquire(index)
			load.Release(index)
		}
	})
}

func BenchmarkLoadOrder(b *testing.B) {
	load := NewLoad(8)

	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = load.Order(0)
		}
	})
}
