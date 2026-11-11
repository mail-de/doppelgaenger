//go:build e2e

package http_e2e

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"doppelgaenger/contrib/e2e/internal/e2etest"
)

// A proxy that cannot start must say why and end with exit code 1. A silent
// exit, or exit code 0, leaves the operator and the service manager guessing.
func TestStartupFailureIsReportedE2E(t *testing.T) {
	root := e2etest.RepoRoot(t)
	runDir := t.TempDir()
	doppel := e2etest.Build(t, root, filepath.Join(runDir, "bin"), "doppelgaenger", ".")

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy a port: %v", err)
	}

	t.Cleanup(func() { _ = occupied.Close() })

	busyAddr := occupied.Addr().String()
	freeAddr := e2etest.FreeAddr(t)

	const inUse = "address already in use"

	tests := []struct {
		name   string
		config string
		want   string
	}{
		{
			name: "http listener in use",
			config: fmt.Sprintf(`protocol: http
listen_addr: "%s"
primary_base_urls: ["http://127.0.0.1:1"]
shadow_base_urls: []
`, busyAddr),
			want: inUse,
		},
		{
			name: "grpc listener in use",
			config: fmt.Sprintf(`protocol: grpc
grpc_listen_addr: "%s"
shadow_sample_percent: 0
primary_grpc_targets:
  - address: "127.0.0.1:1"
`, busyAddr),
			want: inUse,
		},
		{
			name: "milter listener in use",
			config: fmt.Sprintf(`protocol: milter
milter_listen_addr: "%s"
primary_milter_addr: "127.0.0.1:1"
shadow_milter_addr: "127.0.0.1:2"
`, busyAddr),
			want: inUse,
		},
		{
			name: "metrics port in use",
			config: fmt.Sprintf(`protocol: milter
milter_listen_addr: "%s"
primary_milter_addr: "127.0.0.1:1"
shadow_milter_addr: "127.0.0.1:2"
observability:
  prometheus_enabled: true
  prometheus_address: "127.0.0.1"
  prometheus_port: %s
`, freeAddr, e2etest.PortOnly(busyAddr)),
			want: inUse,
		},
		{
			name: "invalid configuration",
			config: `protocol: http
primary_base_urls: ["http://127.0.0.1:1", "http://127.0.0.1:2"]
primary_selection_mode: least_conn
primary_failover:
  cooldown: "0s"
`,
			want: "primary_failover.cooldown must be > 0 when primary_selection_mode is least_conn",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			e2etest.WriteFile(t, configPath, tt.config)

			assertStartupFailure(t, doppel, configPath, tt.want)
		})
	}

	t.Run("missing configuration file", func(t *testing.T) {
		assertStartupFailure(t, doppel, filepath.Join(t.TempDir(), "missing.yaml"), "missing.yaml")
	})
}

func assertStartupFailure(t *testing.T, doppel, configPath, want string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	output, err := exec.CommandContext(ctx, doppel, "--config", configPath).CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("proxy kept running instead of failing to start:\n%s", output)
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("exit = %v, want exit code 1:\n%s", err, output)
	}

	if !strings.Contains(string(output), want) {
		t.Fatalf("output does not name the reason %q:\n%s", want, output)
	}
}
