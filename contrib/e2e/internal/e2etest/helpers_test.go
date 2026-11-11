//go:build e2e

package e2etest

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Linux picks the port for "listen on port 0" at random from a few thousand
// candidates. Without a record of the addresses already handed out, this many
// calls are certain to return one of them twice there.
func TestFreeAddrNeverRepeats(t *testing.T) {
	seen := map[string]struct{}{}

	for range 2000 {
		addr := FreeAddr(t)
		if _, repeated := seen[addr]; repeated {
			t.Fatalf("FreeAddr returned %s twice", addr)
		}

		seen[addr] = struct{}{}
	}
}

func waitExited(t *testing.T, process *Process) {
	t.Helper()

	select {
	case <-process.exited:
	case <-time.After(waitTimeout):
		t.Fatalf("%s did not exit", process.Name)
	}
}

// A process that ends on its own must be reported, so that waiting for it
// fails at once and with its log instead of timing out.
func TestExitedProcessReportsUnexpectedExit(t *testing.T) {
	crashed := Start(t, "crashed", filepath.Join(t.TempDir(), "crashed.log"), nil, "sh", "-c", "echo startup failed; exit 3")
	waitExited(t, crashed)

	if got := exitedProcess(t); got != crashed {
		t.Fatalf("exitedProcess() = %v, want the crashed process", got)
	}

	if crashed.exitErr == nil || !strings.Contains(crashed.exitErr.Error(), "exit status 3") {
		t.Fatalf("exit error = %v, want exit status 3", crashed.exitErr)
	}

	if tail := crashed.logTail(); !strings.Contains(tail, "startup failed") {
		t.Fatalf("log tail = %q, want the process output", tail)
	}
}

// A process that the test stopped itself has not failed.
func TestExitedProcessIgnoresStoppedProcess(t *testing.T) {
	running := Start(t, "running", filepath.Join(t.TempDir(), "running.log"), nil, "sleep", "60")

	if got := exitedProcess(t); got != nil {
		t.Fatalf("exitedProcess() = %s, want none while the process runs", got.Name)
	}

	running.Stop(t)
	waitExited(t, running)

	if got := exitedProcess(t); got != nil {
		t.Fatalf("exitedProcess() = %s, want none after Stop", got.Name)
	}

	if tail := running.logTail(); tail != "(empty)" {
		t.Fatalf("log tail = %q, want the empty marker", tail)
	}
}
