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

// NewSelector creates a backend selection strategy from config value.
func NewSelector(mode string) Selector {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case SelectionSourceIP:
		return SourceIPHashSelector{}
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
