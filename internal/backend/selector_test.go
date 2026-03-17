package backend

import "testing"

func TestRoundRobinSelectorCycles(t *testing.T) {
	selector := &RoundRobinSelector{}

	expected := []int{0, 1, 2, 0, 1, 2}
	for i, want := range expected {
		got := selector.Select(Request{}, 3)
		if got != want {
			t.Fatalf("call %d: expected %d, got %d", i, want, got)
		}
	}
}

func TestSourceIPHashSelectorStableForClient(t *testing.T) {
	selector := SourceIPHashSelector{}
	item := Request{RemoteAddr: "203.0.113.10"}

	first := selector.Select(item, 4)
	if first < 0 || first >= 4 {
		t.Fatalf("expected index in range [0,4), got %d", first)
	}

	for i := 0; i < 10; i++ {
		got := selector.Select(item, 4)
		if got != first {
			t.Fatalf("expected stable hash pinning, got %d then %d", first, got)
		}
	}
}

func TestSourceIPHashSelectorNormalizesHostPort(t *testing.T) {
	selector := SourceIPHashSelector{}

	hostOnly := selector.Select(Request{RemoteAddr: "198.51.100.42"}, 7)
	hostPort := selector.Select(Request{RemoteAddr: "198.51.100.42:5555"}, 7)
	if hostOnly != hostPort {
		t.Fatalf("expected host and host:port to map to same backend, got %d vs %d", hostOnly, hostPort)
	}
}
