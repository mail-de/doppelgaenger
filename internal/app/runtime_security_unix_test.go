//go:build unix

package app

import (
	"io"
	"log/slog"
	"os"
	"testing"

	"doppelgaenger/internal/config"
)

func TestApplyRuntimeSecuritySkipsWhenAlreadyApplied(t *testing.T) {
	t.Setenv(runtimeSecurityAppliedEnv, "1")
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))

	// Intentionally use a non-existing user/group. If skip does not work,
	// resolveIdentities would fail.
	cfg := config.Config{
		RunAsUser:  "definitely-non-existing-user-for-test",
		RunAsGroup: "definitely-non-existing-group-for-test",
		ChrootDir:  "/definitely/non-existing/chroot",
	}

	if err := ApplyRuntimeSecurity(cfg, logger); err != nil {
		t.Fatalf("expected skip without error, got: %v", err)
	}
}

func TestApplyRuntimeSecurityNoSecurityConfigured(t *testing.T) {
	_ = os.Unsetenv(runtimeSecurityAppliedEnv)
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))

	if err := ApplyRuntimeSecurity(config.Config{}, logger); err != nil {
		t.Fatalf("expected no-op without error, got: %v", err)
	}
}
