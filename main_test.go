package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/fx"
)

const (
	configFlag     = "--config"
	versionFlag    = "--version"
	testConfigPath = "/tmp/config.yaml"
)

func TestParseCLIAcceptsLongOptions(t *testing.T) {
	opts, err := parseCLI([]string{versionFlag, configFlag, testConfigPath}, io.Discard)
	if err != nil {
		t.Fatalf("parseCLI returned error: %v", err)
	}

	if !opts.version {
		t.Fatalf("expected version to be true")
	}

	if opts.configPath != testConfigPath {
		t.Fatalf("expected config path to be parsed, got %q", opts.configPath)
	}
}

func TestParseCLIAcceptsShortOptions(t *testing.T) {
	opts, err := parseCLI([]string{"-h", "-c", testConfigPath}, io.Discard)
	if err != nil {
		t.Fatalf("parseCLI returned error: %v", err)
	}

	if !opts.help {
		t.Fatalf("expected help to be true")
	}

	if opts.configPath != testConfigPath {
		t.Fatalf("expected config path to be parsed, got %q", opts.configPath)
	}
}

func TestParseCLIRejectsUnexpectedArgs(t *testing.T) {
	_, err := parseCLI([]string{"unexpected"}, io.Discard)
	if err == nil {
		t.Fatalf("expected unexpected argument to fail")
	}
}

func TestParseCLIRejectsSingleDashLongOption(t *testing.T) {
	_, err := parseCLI([]string{"-config", testConfigPath}, io.Discard)
	if err == nil {
		t.Fatalf("expected single-dash long option to fail")
	}
}

func TestPrintVersion(t *testing.T) {
	var buf bytes.Buffer

	printVersion(&buf, "v1.2.3")

	if got := buf.String(); got != "doppelgaenger v1.2.3\n" {
		t.Fatalf("unexpected version output: %q", got)
	}
}

func TestPrintUsageMentionsRuntimeFlags(t *testing.T) {
	var buf bytes.Buffer

	printUsage(&buf, "doppelgaenger")

	output := buf.String()
	for _, want := range []string{versionFlag, configFlag, "--help"} {
		if !strings.Contains(output, want) {
			t.Fatalf("usage output must mention %s: %s", want, output)
		}
	}
}

// Fx reports a failed start only to its event logger, which is disabled. The
// process used to end with exit code 1 and no output at all.
func TestRunAppReportsStartupFailureOnStderr(t *testing.T) {
	var (
		logger *slog.Logger
		stderr bytes.Buffer
	)

	application := fx.New(
		fx.NopLogger,
		fx.Provide(func() (int, error) { return 0, errors.New("configuration is broken") }),
		fx.Invoke(func(int) {}),
	)

	if code := runApp(application, &logger, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}

	// Only the cause is reported, without the dependency path around it.
	if got, want := stderr.String(), "doppelgaenger: startup failed: configuration is broken\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestRunAppReportsStartupFailureThroughLogger(t *testing.T) {
	var logs, stderr bytes.Buffer

	logger := slog.New(slog.NewTextHandler(&logs, nil))
	application := fx.New(fx.NopLogger, fx.Invoke(func(lifecycle fx.Lifecycle) {
		lifecycle.Append(fx.Hook{OnStart: func(context.Context) error {
			return errors.New("listen tcp 127.0.0.1:1: bind: address already in use")
		}})
	}))

	if code := runApp(application, &logger, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}

	record := logs.String()
	if !strings.Contains(record, "level=ERROR") || !strings.Contains(record, `msg="startup failed"`) || !strings.Contains(record, "address already in use") {
		t.Fatalf("log record = %q, want an ERROR record with the reason", record)
	}

	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want the reason in the log only", stderr.String())
	}
}

func TestRunAppReportsShutdownFailure(t *testing.T) {
	var (
		logger *slog.Logger
		stderr bytes.Buffer
	)

	application := fx.New(fx.NopLogger, fx.Invoke(func(lifecycle fx.Lifecycle, shutdowner fx.Shutdowner) {
		lifecycle.Append(fx.Hook{
			OnStart: func(context.Context) error {
				go func() { _ = shutdowner.Shutdown() }()

				return nil
			},
			OnStop: func(context.Context) error { return errors.New("flush failed") },
		})
	}))

	if code := runApp(application, &logger, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}

	if got, want := stderr.String(), "doppelgaenger: shutdown failed: flush failed\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

// The exit code requested with the shutdown must reach the process: a server
// that failed asks for 1, an ordinary stop for 0.
func TestRunAppReturnsShutdownExitCode(t *testing.T) {
	for _, want := range []int{0, 1, 3} {
		var (
			logger *slog.Logger
			stderr bytes.Buffer
		)

		application := fx.New(fx.NopLogger, fx.Invoke(func(lifecycle fx.Lifecycle, shutdowner fx.Shutdowner) {
			lifecycle.Append(fx.Hook{OnStart: func(context.Context) error {
				go func() { _ = shutdowner.Shutdown(fx.ExitCode(want)) }()

				return nil
			}})
		}))

		if code := runApp(application, &logger, &stderr); code != want {
			t.Fatalf("exit code = %d, want %d", code, want)
		}

		if stderr.Len() != 0 {
			t.Fatalf("stderr = %q, want no output for a requested shutdown", stderr.String())
		}
	}
}

// The real wiring must report a configuration error by its cause alone.
func TestRunAppReportsConfigurationError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	content := `protocol: http
primary_base_urls: ["http://127.0.0.1:1", "http://127.0.0.1:2"]
primary_selection_mode: least_conn
primary_failover:
  cooldown: "0s"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("CONFIG_FILE", path)

	var (
		logger *slog.Logger
		stderr bytes.Buffer
	)

	if code := runApp(newApp(&logger), &logger, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}

	want := "doppelgaenger: startup failed: primary_failover.cooldown must be > 0 when primary_selection_mode is least_conn\n"
	if got := stderr.String(); got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}
