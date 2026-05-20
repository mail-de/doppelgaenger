package grpcproxy

import (
	"hash/fnv"
	"net"
	"strings"
)

const (
	selectionModeRoundRobin   = "round_robin"
	selectionModeSourceIPHash = "source_ip_hash"
)

// RequestInfo carries request attributes used by target selection.
type RequestInfo struct {
	FullMethod string
	Service    string
	Method     string
	PeerAddr   string
	PeerIP     net.IP
}

// Pick returns the target selected for a request.
func (p *TargetPool) Pick(info RequestInfo) *Target {
	if p == nil || len(p.targets) == 0 {
		return nil
	}

	if len(p.targets) == 1 {
		return p.targets[0]
	}

	if p.mode == selectionModeSourceIPHash {
		if target := p.pickSourceIPHash(info); target != nil {
			return target
		}
	}

	return p.pickRoundRobin()
}

func (p *TargetPool) pickRoundRobin() *Target {
	index := p.next.Add(1) - 1

	return p.targets[int(index%uint64(len(p.targets)))]
}

func (p *TargetPool) pickSourceIPHash(info RequestInfo) *Target {
	ip := info.PeerIP
	if ip == nil {
		ip = parsePeerIP(info.PeerAddr)
	}

	if ip == nil {
		return nil
	}

	hash := fnv.New64a()
	_, _ = hash.Write([]byte(ip.String()))

	return p.targets[int(hash.Sum64()%uint64(len(p.targets)))]
}

func parsePeerIP(peerAddr string) net.IP {
	trimmed := strings.TrimSpace(peerAddr)
	if trimmed == "" {
		return nil
	}

	host, _, err := net.SplitHostPort(trimmed)
	if err != nil {
		return net.ParseIP(trimmed)
	}

	return net.ParseIP(host)
}
