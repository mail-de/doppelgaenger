//go:build e2e

package grpc_e2e

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"doppelgaenger/contrib/e2e/internal/e2etest"
)

const (
	expectedTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	traceparent     = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
)

func TestGRPCProxyE2E(t *testing.T) {
	root := e2etest.RepoRoot(t)
	runDir := t.TempDir()
	binDir := filepath.Join(runDir, "bin")

	doppel := e2etest.Build(t, root, binDir, "doppelgaenger", ".")
	fakeGRPC := e2etest.Build(t, root, binDir, "fakegrpcserver", "./cmd/fakegrpcserver")
	grpcProbe := e2etest.Build(t, root, binDir, "grpcprobe", "./cmd/grpcprobe")
	collector := e2etest.Build(t, root, binDir, "fakeotlpcollector", "./cmd/fakeotlpcollector")

	env := newGRPCEnv(t, runDir)
	processes := startGRPCStack(t, env, doppel, fakeGRPC, collector)
	defer stopProcesses(t, processes)

	assertPrimaryOnlyUnary(t, env, grpcProbe)
	assertForcedShadowUnary(t, env, grpcProbe)
	assertShadowNeverBlocksForce(t, env, grpcProbe)
	assertMetadataDiff(t, env, grpcProbe)
	assertShadowErrorDoesNotAffectClient(t, env, grpcProbe)
	assertServerStreaming(t, env, grpcProbe)
	assertComparisonError(t, env, grpcProbe)
	assertGRPCMetrics(t, env)

	processes.doppel.Stop(t)
	assertGRPCOTLPSummary(t, env)
}

type grpcEnv struct {
	proxyAddr        string
	primaryAddr      string
	shadowAddr       string
	metricsAddr      string
	collectorAddr    string
	proxyConfig      string
	primaryLog       string
	shadowLog        string
	doppelLog        string
	collectorLog     string
	collectorHealth  string
	collectorSummary string
}

type grpcProcesses struct {
	collector *e2etest.Process
	primary   *e2etest.Process
	shadow    *e2etest.Process
	doppel    *e2etest.Process
}

func newGRPCEnv(t *testing.T, runDir string) grpcEnv {
	t.Helper()

	env := grpcEnv{
		proxyAddr:     e2etest.FreeAddr(t),
		primaryAddr:   e2etest.FreeAddr(t),
		shadowAddr:    e2etest.FreeAddr(t),
		metricsAddr:   e2etest.FreeAddr(t),
		collectorAddr: e2etest.FreeAddr(t),
	}

	env.proxyConfig = filepath.Join(runDir, "doppelgaenger-grpc.yaml")
	env.primaryLog = filepath.Join(runDir, "primary-grpc.jsonl")
	env.shadowLog = filepath.Join(runDir, "shadow-grpc.jsonl")
	env.doppelLog = filepath.Join(runDir, "doppelgaenger-grpc.log")
	env.collectorLog = filepath.Join(runDir, "collector-grpc.jsonl")
	env.collectorHealth = e2etest.AddrURL(env.collectorAddr, "/healthz")
	env.collectorSummary = e2etest.AddrURL(env.collectorAddr, "/summary")

	e2etest.WriteFile(t, env.proxyConfig, grpcProxyConfig(env))

	return env
}

func grpcProxyConfig(env grpcEnv) string {
	return fmt.Sprintf(`protocol: grpc
grpc_listen_addr: "%s"
grpc_tls:
  enabled: false
primary_grpc_targets:
  - name: primary
    address: "%s"
    tls:
      enabled: false
shadow_grpc_targets:
  - name: shadow
    address: "%s"
    tls:
      enabled: false
shadow_sample_percent: 0
shadow_rps: 0
grpc_shadow_timeout: "400ms"
grpc_shadow_force_metadata: "x-shadow"
grpc_compare_mode: status
grpc_compare_metadata:
  - "grpc-status"
  - "grpc-message"
grpc_rules:
  - name: never-shadow
    service: "e2e.Never"
    shadow: never
    compare: off

  - name: timeout-error
    service: "e2e.Timeout"
    shadow: auto
    compare: on
    compare_mode: status
    primary_metadata:
      x-target: "primary-overlay"
    shadow_metadata:
      x-target: "shadow-overlay"
      x-fake-delay: "2s"

  - name: shadow-error
    service: "e2e.ShadowError"
    shadow: auto
    compare: on
    compare_mode: status
    primary_metadata:
      x-target: "primary-overlay"
    shadow_metadata:
      x-target: "shadow-overlay"
      x-fake-status: "Unavailable"

  - name: metadata-diff
    service: "e2e.MetadataDiff"
    shadow: auto
    compare: on
    compare_mode: status_metadata
    compare_metadata:
      - "grpc-status"
      - "x-backend"
    primary_metadata:
      x-target: "primary-overlay"
    shadow_metadata:
      x-target: "shadow-overlay"

  - name: stream-count
    service: "e2e.Stream"
    methods: ["ServerStream"]
    shadow: auto
    compare: on
    compare_mode: message_count
    primary_metadata:
      x-target: "primary-overlay"
    shadow_metadata:
      x-target: "shadow-overlay"

  - name: catch-all
    service: "*"
    shadow: auto
    compare: on
    compare_mode: status
    primary_metadata:
      x-target: "primary-overlay"
    shadow_metadata:
      x-target: "shadow-overlay"
log_json: true
observability:
  prometheus_enabled: true
  prometheus_address: "127.0.0.1"
  prometheus_port: %s
  prometheus_path: "/metrics"
  prometheus_runtime_metrics: false
  otel_enabled: true
  otel_traces_enabled: true
  otel_metrics_enabled: true
  otel_service_name: "doppelgaenger-e2e-grpc"
  otel_exporter_otlp_endpoint: "http://%s"
  otel_exporter_otlp_insecure: true
  otel_sample_ratio: 1.0
  trace_id_header: "X-Trace-ID"
`, env.proxyAddr, env.primaryAddr, env.shadowAddr, e2etest.PortOnly(env.metricsAddr), env.collectorAddr)
}

func startGRPCStack(t *testing.T, env grpcEnv, doppel, fakeGRPC, collector string) grpcProcesses {
	t.Helper()

	processes := grpcProcesses{}
	processes.collector = e2etest.Start(t, "fakeotlpcollector", env.collectorLog, nil, collector, "-listen", env.collectorAddr)
	e2etest.WaitHTTP(t, env.collectorHealth)

	commonLogArgs := []string{
		"-log-metadata-key", "traceparent",
		"-log-metadata-key", "x-trace-id",
		"-log-metadata-key", "x-shadow",
		"-log-metadata-key", "x-target",
		"-log-metadata-key", "x-e2e",
		"-log-metadata-key", "x-fake-status",
		"-log-metadata-key", "x-fake-delay",
	}

	primaryArgs := append([]string{
		"-listen", env.primaryAddr,
		"-mode", "auto",
		"-response-prefix", "primary:",
		"-header", "x-backend=primary",
		"-trailer", "x-trailer=primary",
	}, commonLogArgs...)
	processes.primary = e2etest.Start(t, "fakegrpc-primary", env.primaryLog, nil, fakeGRPC, primaryArgs...)
	e2etest.WaitTCP(t, env.primaryAddr)

	shadowArgs := append([]string{
		"-listen", env.shadowAddr,
		"-mode", "auto",
		"-response-prefix", "shadow:",
		"-header", "x-backend=shadow",
		"-trailer", "x-trailer=shadow",
	}, commonLogArgs...)
	processes.shadow = e2etest.Start(t, "fakegrpc-shadow", env.shadowLog, nil, fakeGRPC, shadowArgs...)
	e2etest.WaitTCP(t, env.shadowAddr)

	processes.doppel = e2etest.Start(t, "doppelgaenger-grpc", env.doppelLog, nil, doppel, "--config", env.proxyConfig)
	e2etest.WaitTCP(t, env.proxyAddr)
	e2etest.WaitHTTP(t, e2etest.AddrURL(env.metricsAddr, "/metrics"))

	return processes
}

func assertPrimaryOnlyUnary(t *testing.T, env grpcEnv, grpcProbe string) {
	t.Helper()

	runProbe(t, grpcProbe,
		"-addr", env.proxyAddr,
		"-method", "/e2e.PrimaryOnly/Unary",
		"-mode", "unary",
		"-payload", "primary-only",
		"-metadata", "x-e2e=primary-only",
		"-expect-status", "OK",
		"-expect-count", "1",
		"-expect-message", "primary:primary-only",
	)

	e2etest.WaitFileContains(t, env.primaryLog, "/e2e.PrimaryOnly/Unary", "primary-only")
	time.Sleep(300 * time.Millisecond)
	e2etest.MustFileNotContain(t, env.shadowLog, "primary-only")
}

func assertForcedShadowUnary(t *testing.T, env grpcEnv, grpcProbe string) {
	t.Helper()

	runProbe(t, grpcProbe,
		"-addr", env.proxyAddr,
		"-method", "/e2e.Force/Unary",
		"-mode", "unary",
		"-payload", "forced-shadow",
		"-metadata", "x-shadow=force",
		"-metadata", "x-e2e=forced-shadow",
		"-metadata", "traceparent="+traceparent,
		"-expect-status", "OK",
		"-expect-count", "1",
		"-expect-message", "primary:forced-shadow",
	)

	e2etest.WaitFileContains(t, env.primaryLog, "/e2e.Force/Unary", "forced-shadow", "primary-overlay", expectedTraceID)
	e2etest.WaitFileContains(t, env.shadowLog, "/e2e.Force/Unary", "forced-shadow", "shadow-overlay", expectedTraceID)
	e2etest.WaitFileContains(t, env.doppelLog, `"msg":"grpc_proxy"`, `"grpc_rule":"catch-all"`, `"shadow_started":true`, `"compare_outcome":"same"`)
}

func assertShadowNeverBlocksForce(t *testing.T, env grpcEnv, grpcProbe string) {
	t.Helper()

	runProbe(t, grpcProbe,
		"-addr", env.proxyAddr,
		"-method", "/e2e.Never/Unary",
		"-mode", "unary",
		"-payload", "never-blocked",
		"-metadata", "x-shadow=force",
		"-metadata", "x-e2e=never-blocked",
		"-expect-status", "OK",
		"-expect-count", "1",
		"-expect-message", "primary:never-blocked",
	)

	e2etest.WaitFileContains(t, env.primaryLog, "/e2e.Never/Unary", "never-blocked")
	e2etest.WaitFileContains(t, env.doppelLog, `"grpc_rule":"never-shadow"`, `"shadow_skip_reason":"grpc_rule"`, `"compare_skip_reason":"grpc_rule"`)
	time.Sleep(300 * time.Millisecond)
	e2etest.MustFileNotContain(t, env.shadowLog, "never-blocked")
}

func assertMetadataDiff(t *testing.T, env grpcEnv, grpcProbe string) {
	t.Helper()

	runProbe(t, grpcProbe,
		"-addr", env.proxyAddr,
		"-method", "/e2e.MetadataDiff/Unary",
		"-mode", "unary",
		"-payload", "metadata-diff",
		"-metadata", "x-shadow=force",
		"-metadata", "x-e2e=metadata-diff",
		"-expect-status", "OK",
		"-expect-count", "1",
		"-expect-message", "primary:metadata-diff",
	)

	e2etest.WaitFileContains(t, env.primaryLog, "/e2e.MetadataDiff/Unary", "primary-overlay")
	e2etest.WaitFileContains(t, env.shadowLog, "/e2e.MetadataDiff/Unary", "shadow-overlay")
	e2etest.WaitFileContains(t, env.doppelLog, `"grpc_rule":"metadata-diff"`, `"compare_mode":"status_metadata"`, `"compare_outcome":"diff"`, `header_metadata:x-backend`)
}

func assertShadowErrorDoesNotAffectClient(t *testing.T, env grpcEnv, grpcProbe string) {
	t.Helper()

	runProbe(t, grpcProbe,
		"-addr", env.proxyAddr,
		"-method", "/e2e.ShadowError/Unary",
		"-mode", "unary",
		"-payload", "shadow-error",
		"-metadata", "x-shadow=force",
		"-metadata", "x-e2e=shadow-error",
		"-expect-status", "OK",
		"-expect-count", "1",
		"-expect-message", "primary:shadow-error",
	)

	e2etest.WaitFileContains(t, env.shadowLog, "/e2e.ShadowError/Unary", "Unavailable", "shadow-error")
	e2etest.WaitFileContains(t, env.doppelLog, `"grpc_rule":"shadow-error"`, `"shadow_status":"Unavailable"`, `"primary_status":"OK"`, `"compare_outcome":"diff"`)
}

func assertServerStreaming(t *testing.T, env grpcEnv, grpcProbe string) {
	t.Helper()

	runProbe(t, grpcProbe,
		"-addr", env.proxyAddr,
		"-method", "/e2e.Stream/ServerStream",
		"-mode", "server-stream",
		"-payload", "stream-seed",
		"-metadata", "x-shadow=force",
		"-metadata", "x-e2e=server-stream",
		"-expect-status", "OK",
		"-expect-count", "3",
	)

	e2etest.WaitFileContains(t, env.primaryLog, "/e2e.Stream/ServerStream", `"response_message_count":3`)
	e2etest.WaitFileContains(t, env.shadowLog, "/e2e.Stream/ServerStream", `"response_message_count":3`)
	e2etest.WaitFileContains(t, env.doppelLog, `"grpc_rule":"stream-count"`, `"primary_message_count":3`, `"shadow_message_count":3`)
}

func assertComparisonError(t *testing.T, env grpcEnv, grpcProbe string) {
	t.Helper()

	runProbe(t, grpcProbe,
		"-addr", env.proxyAddr,
		"-method", "/e2e.Timeout/Unary",
		"-mode", "unary",
		"-payload", "timeout-shadow",
		"-metadata", "x-shadow=force",
		"-metadata", "x-e2e=timeout-shadow",
		"-expect-status", "OK",
		"-expect-count", "1",
		"-expect-message", "primary:timeout-shadow",
	)

	e2etest.WaitFileContains(t, env.doppelLog, `"grpc_rule":"timeout-error"`, `"shadow_skip_reason":"timeout"`, `"compare_outcome":"error"`)
}

func assertGRPCMetrics(t *testing.T, env grpcEnv) {
	t.Helper()

	e2etest.WaitCondition(t, "gRPC OpenMetrics", func() bool {
		_, _, body := e2etest.Get(t, e2etest.AddrURL(env.metricsAddr, "/metrics"), nil)

		return containsAll(body,
			"doppelgaenger_ingress_requests_total",
			`protocol="grpc"`,
			`method="/e2e.Force/Unary"`,
			`target="primary"`,
			`target="shadow"`,
			"doppelgaenger_backend_requests_total",
			"doppelgaenger_comparisons_total",
			`result="same"`,
			`result="diff"`,
			`result="skipped"`,
			`result="error"`,
		)
	})
}

func assertGRPCOTLPSummary(t *testing.T, env grpcEnv) {
	t.Helper()

	e2etest.WaitCondition(t, "gRPC OTLP summary", func() bool {
		_, _, body := e2etest.Get(t, env.collectorSummary, nil)

		return containsAll(body,
			"gRPC /e2e.Force/Unary",
			"gRPC primary /e2e.Force/Unary",
			"gRPC shadow /e2e.Force/Unary",
			expectedTraceID,
			"doppelgaenger_ingress_requests",
			"doppelgaenger_backend_requests",
			"doppelgaenger_comparisons",
		)
	})
}

func runProbe(t *testing.T, grpcProbe string, args ...string) string {
	t.Helper()

	return e2etest.RunCommand(t, "", nil, grpcProbe, args...)
}

func containsAll(text string, fragments ...string) bool {
	for _, fragment := range fragments {
		if !strings.Contains(text, fragment) {
			return false
		}
	}

	return true
}

func stopProcesses(t *testing.T, processes grpcProcesses) {
	t.Helper()

	processes.doppel.Stop(t)
	processes.shadow.Stop(t)
	processes.primary.Stop(t)
	processes.collector.Stop(t)
}
