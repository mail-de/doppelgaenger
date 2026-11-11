package backend

import (
	"net"
	"strings"
	"sync/atomic"

	"doppelgaenger/internal/failover"
)

const (
	// SelectionRoundRobin selects backends in round-robin order.
	SelectionRoundRobin = "round_robin"
	// SelectionSourceIP selects backends deterministically by source IP.
	SelectionSourceIP = "source_ip_hash"
	// SelectionLeastConn selects the backend with the fewest requests in flight.
	SelectionLeastConn = "least_conn"
)

// Selector chooses a backend index for the given request.
type Selector interface {
	Select(item Request, backendCount int) int
}

// FallbackOrderer is implemented by selectors that define the failover
// candidate order after their first choice.
type FallbackOrderer interface {
	// Order returns all backend indexes, starting with first.
	Order(item Request, first, backendCount int) []int
}

// AttemptTracker is implemented by selectors that balance on the number of
// attempts in flight per backend. A requester binds the tracker once and then
// brackets every backend attempt with Acquire and Release.
type AttemptTracker interface {
	// Bind sizes the tracker for backendCount backends. available reports
	// whether a backend is outside its failover cooldown.
	Bind(backendCount int, available func(index int) bool)
	// Acquire records that an attempt starts on the backend.
	Acquire(index int)
	// Release records that the attempt started with Acquire has ended.
	Release(index int)
}

// NewSelector creates a backend selection strategy from config value.
func NewSelector(mode string) Selector {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case SelectionSourceIP:
		return SourceIPHashSelector{}
	case SelectionLeastConn:
		return &LeastConnSelector{}
	default:
		return &RoundRobinSelector{}
	}
}

// RoundRobinSelector distributes requests equally over all configured backends.
type RoundRobinSelector struct {
	counter atomic.Uint64
}

// Select returns the next backend index in round-robin order.
func (s *RoundRobinSelector) Select(_ Request, backendCount int) int {
	if backendCount <= 1 {
		return 0
	}

	idx := s.counter.Add(1) - 1

	return int(idx % uint64(backendCount))
}

// Order returns the remaining backends in ring order after first.
func (s *RoundRobinSelector) Order(_ Request, first, backendCount int) []int {
	return failover.Order(first, backendCount, false, 0)
}

// LeastConnSelector selects the backend with the fewest requests in flight.
// Equally loaded backends are selected at random, and a backend in cooldown is
// selected only when no other backend is left.
//
// The selector balances only after a requester has bound it; until then it
// selects in round-robin order. One instance must not be shared between
// requesters.
type LeastConnSelector struct {
	load      *failover.Load
	available func(index int) bool
	unbound   RoundRobinSelector
}

// Bind starts in-flight tracking for backendCount backends.
func (s *LeastConnSelector) Bind(backendCount int, available func(index int) bool) {
	s.load = failover.NewLoad(backendCount)
	s.available = available
}

// Acquire records that an attempt starts on the backend.
func (s *LeastConnSelector) Acquire(index int) {
	s.load.Acquire(index)
}

// Release records that an attempt on the backend has ended.
func (s *LeastConnSelector) Release(index int) {
	s.load.Release(index)
}

// Select returns the available backend with the fewest requests in flight.
func (s *LeastConnSelector) Select(item Request, backendCount int) int {
	if s.load.Len() != backendCount {
		return s.unbound.Select(item, backendCount)
	}

	return s.load.Pick(s.available)
}

// Order returns the remaining backends with the fewest requests in flight
// first, so a failed attempt moves to the least busy backend.
func (s *LeastConnSelector) Order(_ Request, first, backendCount int) []int {
	if order := s.load.Order(first); len(order) == backendCount {
		return order
	}

	return failover.Order(first, backendCount, false, 0)
}

// SourceIPHashSelector pins a source IP deterministically to one backend.
type SourceIPHashSelector struct{}

// Select returns a stable backend index for the request source IP.
func (s SourceIPHashSelector) Select(item Request, backendCount int) int {
	if backendCount <= 1 {
		return 0
	}

	ip := normalizeSourceIP(item.RemoteAddr)
	hash := fnv1a32(ip)

	return int(hash % uint32(backendCount))
}

// Order returns a stable per-client fallback order. The first entry is the
// regular hash choice, so failover never changes the target of a client whose
// selected backend is healthy.
func (s SourceIPHashSelector) Order(item Request, first, backendCount int) []int {
	return failover.Order(first, backendCount, true, uint64(fnv1a32(normalizeSourceIP(item.RemoteAddr))))
}

func normalizeSourceIP(remote string) string {
	trimmed := strings.TrimSpace(remote)
	if trimmed == "" {
		return ""
	}

	if host, _, err := net.SplitHostPort(trimmed); err == nil {
		return host
	}

	return trimmed
}

func fnv1a32(s string) uint32 {
	var hash uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		hash ^= uint32(s[i])
		hash *= 16777619
	}

	return hash
}
