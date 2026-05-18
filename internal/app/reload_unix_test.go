//go:build unix

package app

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestReexecSelfUsesProcFallback(t *testing.T) {
	original := execProcess
	defer func() { execProcess = original }()

	calls := make([]string, 0, 2)
	execProcess = func(path string, _ []string, _ []string) error {
		calls = append(calls, path)
		if len(calls) == 1 {
			return errors.New("first path not executable")
		}

		return nil
	}

	if err := reexecSelf(); err != nil {
		t.Fatalf("expected fallback exec to succeed, got error: %v", err)
	}

	if len(calls) != 2 {
		t.Fatalf("expected two exec attempts, got %d", len(calls))
	}

	if calls[1] != "/proc/self/exe" {
		t.Fatalf("expected /proc/self/exe fallback, got %q", calls[1])
	}
}

func TestHandleSIGHUPWithInvalidConfigSkipsExec(t *testing.T) {
	original := execProcess
	defer func() { execProcess = original }()

	executed := false
	execProcess = func(_ string, _ []string, _ []string) error {
		executed = true
		return nil
	}

	tempDir := t.TempDir()

	configPath := filepath.Join(tempDir, "broken.yaml")
	if err := os.WriteFile(configPath, []byte("protocol: definitely-not-supported"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("CONFIG_FILE", configPath)
	handleSIGHUP(slog.Default())

	if executed {
		t.Fatalf("expected no exec when config validation fails")
	}
}
