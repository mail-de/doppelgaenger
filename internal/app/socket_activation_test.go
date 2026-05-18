package app

import (
	"net"
	"testing"
)

const (
	activatedHTTPName   = "http"
	activatedMilterName = "milter"
)

func TestPickActivatedListenerByName(t *testing.T) {
	first, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen first: %v", err)
	}

	defer func() { _ = first.Close() }()

	second, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen second: %v", err)
	}

	defer func() { _ = second.Close() }()

	infos := []ActivatedListenerInfo{
		{Name: activatedHTTPName, Listener: first},
		{Name: activatedMilterName, Listener: second},
	}

	chosen, activated, pickErr := PickActivatedListener(infos, activatedMilterName, "")
	if pickErr != nil {
		t.Fatalf("pick listener: %v", pickErr)
	}

	if !activated {
		t.Fatalf("expected activated=true")
	}

	if chosen != second {
		t.Fatalf("expected milter listener to be chosen")
	}
}

func TestPickActivatedListenerByPort(t *testing.T) {
	first, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen first: %v", err)
	}

	defer func() { _ = first.Close() }()

	second, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen second: %v", err)
	}

	defer func() { _ = second.Close() }()

	expectedAddr := second.Addr().String()
	infos := []ActivatedListenerInfo{
		{Name: "", Listener: first},
		{Name: "", Listener: second},
	}

	chosen, activated, pickErr := PickActivatedListener(infos, "", expectedAddr)
	if pickErr != nil {
		t.Fatalf("pick listener: %v", pickErr)
	}

	if !activated {
		t.Fatalf("expected activated=true")
	}

	if chosen != second {
		t.Fatalf("expected listener matching port %q", expectedAddr)
	}
}

func TestTCPPort(t *testing.T) {
	if got := tcpPort(":8443"); got != "8443" {
		t.Fatalf("expected 8443, got %q", got)
	}

	if got := tcpPort("[::]:8444"); got != "8444" {
		t.Fatalf("expected 8444, got %q", got)
	}
}
