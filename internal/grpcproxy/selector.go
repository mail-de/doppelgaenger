package grpcproxy

import (
	"hash/fnv"
	"net"
	"strings"
)

const (
	selectionModeRoundRobin   = "round_robin"
	selectionModeSourceIPHash = "source_ip_hash"
	selectionModeLeastConn    = "least_conn"
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
	// leastConn defers the choice to openStream, which picks the target with
	// the fewest open streams right before the stream is created. index is
	// unused until then.
	leastConn bool
}

// pendingTarget stands in for the target of a least_conn pool until the
// stream is opened. It has no name and no address, so nothing reports a
// target that was never tried.
var pendingTarget = &Target{}

// Pick returns the target selected for a request. A least_conn pool chooses
// when the stream is opened and returns a placeholder here.
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

	// A call can spend time between selection and stream creation, for
	// example on a token refresh. Choosing by load this early would send all
	// calls that arrive in the meantime to the same target, so least_conn
	// hands out a placeholder here and decides in openStream.
	if p.load != nil {
		return pendingTarget, targetSelection{leastConn: true}
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

// available reports whether a target is outside its cooldown and its channel
// is not known to be failing.
func (p *TargetPool) available(index int) bool {
	return p.health.Available(index) && !p.transientFailure(index)
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
