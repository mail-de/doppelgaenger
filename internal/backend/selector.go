package backend

import (
	"net"
	"strings"
	"sync/atomic"
)

const (
	SelectionRoundRobin = "round_robin"
	SelectionSourceIP   = "source_ip_hash"
)

// Selector chooses a backend index for the given request.
type Selector interface {
	Select(item Request, backendCount int) int
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

func (s *RoundRobinSelector) Select(_ Request, backendCount int) int {
	if backendCount <= 1 {
		return 0
	}

	idx := s.counter.Add(1) - 1

	return int(idx % uint64(backendCount))
}

// SourceIPHashSelector pins a source IP deterministically to one backend.
type SourceIPHashSelector struct{}

func (s SourceIPHashSelector) Select(item Request, backendCount int) int {
	if backendCount <= 1 {
		return 0
	}

	ip := normalizeSourceIP(item.RemoteAddr)
	hash := fnv1a32(ip)

	return int(hash % uint32(backendCount))
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
