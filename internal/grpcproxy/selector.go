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

// targetSelection records how the first-choice target was selected, so the
// failover order can be derived from the same decision.
type targetSelection struct {
	index  int
	hashed bool
	seed   uint64
}

// Pick returns the target selected for a request.
func (p *TargetPool) Pick(info RequestInfo) *Target {
	target, _ := p.selectTarget(info)

	return target
}

// selectTarget returns the first-choice target and its selection details.
func (p *TargetPool) selectTarget(info RequestInfo) (*Target, targetSelection) {
	if p == nil || len(p.targets) == 0 {
		return nil, targetSelection{index: -1}
	}

	if len(p.targets) == 1 {
		return p.targets[0], targetSelection{}
	}

	if p.mode == selectionModeSourceIPHash {
		if selection, ok := p.pickSourceIPHash(info); ok {
			return p.targets[selection.index], selection
		}
	}

	selection := p.pickRoundRobin()

	return p.targets[selection.index], selection
}

func (p *TargetPool) pickRoundRobin() targetSelection {
	index := p.next.Add(1) - 1

	return targetSelection{index: int(index % uint64(len(p.targets)))}
}

func (p *TargetPool) pickSourceIPHash(info RequestInfo) (targetSelection, bool) {
	ip := info.PeerIP
	if ip == nil {
		ip = parsePeerIP(info.PeerAddr)
	}

	if ip == nil {
		return targetSelection{}, false
	}

	hash := fnv.New64a()
	_, _ = hash.Write([]byte(ip.String()))
	sum := hash.Sum64()

	return targetSelection{index: int(sum % uint64(len(p.targets))), hashed: true, seed: sum}, true
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
