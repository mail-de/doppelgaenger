package grpcproxy

import (
	"net"
	"testing"
)

const (
	selectorTargetA = "primary-a"
	selectorTargetB = "primary-b"
	selectorTargetC = "primary-c"
)

func TestTargetPoolRoundRobinSelection(t *testing.T) {
	pool := &TargetPool{
		mode: selectionModeRoundRobin,
		targets: []*Target{
			{Name: selectorTargetA},
			{Name: selectorTargetB},
		},
	}

	first := pool.Pick(RequestInfo{})
	second := pool.Pick(RequestInfo{})
	third := pool.Pick(RequestInfo{})

	if first.Name != selectorTargetA || second.Name != selectorTargetB || third.Name != selectorTargetA {
		t.Fatalf("expected round-robin selection, got %s, %s, %s", first.Name, second.Name, third.Name)
	}
}

func TestTargetPoolSourceIPHashSelectionIsStable(t *testing.T) {
	pool := &TargetPool{
		mode: selectionModeSourceIPHash,
		targets: []*Target{
			{Name: selectorTargetA},
			{Name: selectorTargetB},
			{Name: selectorTargetC},
		},
	}
	info := RequestInfo{PeerIP: net.ParseIP("192.0.2.10")}

	first := pool.Pick(info)
	second := pool.Pick(info)

	if first == nil || second == nil || first.Name != second.Name {
		t.Fatalf("expected stable source-IP hash selection, got %#v then %#v", first, second)
	}
}

func TestTargetPoolSourceIPHashFallsBackToRoundRobinWithoutPeerIP(t *testing.T) {
	pool := &TargetPool{
		mode: selectionModeSourceIPHash,
		targets: []*Target{
			{Name: selectorTargetA},
			{Name: selectorTargetB},
		},
	}

	first := pool.Pick(RequestInfo{})
	second := pool.Pick(RequestInfo{})

	if first.Name != selectorTargetA || second.Name != selectorTargetB {
		t.Fatalf("expected fallback round-robin selection, got %s then %s", first.Name, second.Name)
	}
}
