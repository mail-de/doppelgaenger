//go:build e2e

package http_e2e

import (
	"fmt"
	"net/http"
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

func TestHTTPObservabilityE2E(t *testing.T) {
	root := e2etest.RepoRoot(t)
	runDir := t.TempDir()
	binDir := filepath.Join(runDir, "bin")
	e2etest.WriteFile(t, filepath.Join(runDir, ".keep"), "")

	doppel := e2etest.Build(t, root, binDir, "doppelgaenger", ".")
	fakeHTTP := e2etest.Build(t, root, binDir, "fakehttpserver", "./cmd/fakehttpserver")
	collector := e2etest.Build(t, root, binDir, "fakeotlpcollector", "./cmd/fakeotlpcollector")

	env := newHTTPEnv(t, runDir)
	processes := startHTTPStack(t, env, doppel, fakeHTTP, collector)
	defer stopProcesses(t, processes)

	assertHTTPFlow(t, env)
	assertHTTPNoShadowFlow(t, env)
	assertHTTPBodyLimit(t, env)
	assertHTTPMetrics(t, env)
	processes.doppel.Stop(t)
	assertOTLPSummary(t, env)
}

type httpEnv struct {
	proxyAddr        string
	primaryAddr      string
	shadowAddr       string
	metricsAddr      string
	collectorAddr    string
	proxyConfig      string
	primaryConfig    string
	shadowConfig     string
	collectorLog     string
	primaryLog       string
	shadowLog        string
	doppelLog        string
	collectorHealth  string
	collectorSummary string
}

type httpProcesses struct {
	collector *e2etest.Process
	primary   *e2etest.Process
	shadow    *e2etest.Process
	doppel    *e2etest.Process
}

func newHTTPEnv(t *testing.T, runDir string) httpEnv {
	t.Helper()

	env := httpEnv{
		proxyAddr:     e2etest.FreeAddr(t),
		primaryAddr:   e2etest.FreeAddr(t),
		shadowAddr:    e2etest.FreeAddr(t),
		metricsAddr:   e2etest.FreeAddr(t),
		collectorAddr: e2etest.FreeAddr(t),
	}

	env.proxyConfig = filepath.Join(runDir, "doppelgaenger.yaml")
	env.primaryConfig = filepath.Join(runDir, "primary.yaml")
	env.shadowConfig = filepath.Join(runDir, "shadow.yaml")
	env.collectorLog = filepath.Join(runDir, "collector.jsonl")
	env.primaryLog = filepath.Join(runDir, "primary.log")
	env.shadowLog = filepath.Join(runDir, "shadow.log")
	env.doppelLog = filepath.Join(runDir, "doppelgaenger.log")
	env.collectorHealth = e2etest.AddrURL(env.collectorAddr, "/healthz")
	env.collectorSummary = e2etest.AddrURL(env.collectorAddr, "/summary")

	writeHTTPConfigs(t, env)

	return env
}

func writeHTTPConfigs(t *testing.T, env httpEnv) {
	t.Helper()

	e2etest.WriteFile(t, env.primaryConfig, fakeHTTPConfig(env.primaryAddr, "primary"))
	e2etest.WriteFile(t, env.shadowConfig, fakeHTTPConfig(env.shadowAddr, "shadow"))
	e2etest.WriteFile(t, env.proxyConfig, proxyHTTPConfig(env))
}

func fakeHTTPConfig(addr, backend string) string {
	return fmt.Sprintf(`listen_addr: "%s"
mode: "echo"
echo_headers:
  - "Traceparent"
  - "X-Trace-ID"
  - "X-E2E"
  - "X-Target"
response_headers:
  X-Backend: "%s"
log_json: true
`, addr, backend)
}

func proxyHTTPConfig(env httpEnv) string {
	return fmt.Sprintf(`protocol: http
listen_addr: "%s"
tls_cert_file: ""
tls_key_file: ""
primary_base_urls:
  - "http://%s"
shadow_base_urls:
  - "http://%s"
shadow_sample_percent: 0
shadow_force_header: "X-Shadow"
shadow_timeout: "2s"
shadow_rps: 0
max_backend_body_bytes: 8
primary_request_headers:
  X-Target: "primary-static"
shadow_request_headers:
  X-Target: "shadow-static"
forward_response_headers:
  - "X-Backend"
  - "X-Target"
compare_headers:
  - "X-Backend"
compare_mode: "header"
log_session_only_on_diff: false
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
  otel_service_name: "doppelgaenger-e2e-http"
  otel_exporter_otlp_endpoint: "http://%s"
  otel_exporter_otlp_insecure: true
  otel_sample_ratio: 1.0
  trace_id_header: "X-Trace-ID"
path_mapping:
  mode: rewrite
  rules:
    - match: "^/public/(.*)$"
      primary: "/primary/$1"
      shadow: "/shadow/$1"
`, env.proxyAddr, env.primaryAddr, env.shadowAddr, e2etest.PortOnly(env.metricsAddr), env.collectorAddr)
}

func startHTTPStack(t *testing.T, env httpEnv, doppel, fakeHTTP, collector string) httpProcesses {
	t.Helper()

	processes := httpProcesses{}
	processes.collector = e2etest.Start(t, "fakeotlpcollector", env.collectorLog, nil, collector, "-listen", env.collectorAddr)
	e2etest.WaitHTTP(t, env.collectorHealth)

	processes.primary = e2etest.Start(t, "fakehttp-primary", env.primaryLog, []string{"CONFIG_FILE=" + env.primaryConfig}, fakeHTTP)
	e2etest.WaitHTTP(t, e2etest.AddrURL(env.primaryAddr, "/healthz"))

	processes.shadow = e2etest.Start(t, "fakehttp-shadow", env.shadowLog, []string{"CONFIG_FILE=" + env.shadowConfig}, fakeHTTP)
	e2etest.WaitHTTP(t, e2etest.AddrURL(env.shadowAddr, "/healthz"))

	processes.doppel = e2etest.Start(t, "doppelgaenger-http", env.doppelLog, nil, doppel, "--config", env.proxyConfig)
	e2etest.WaitHTTP(t, e2etest.AddrURL(env.proxyAddr, "/ready"))
	e2etest.WaitHTTP(t, e2etest.AddrURL(env.metricsAddr, "/metrics"))

	return processes
}

func assertHTTPFlow(t *testing.T, env httpEnv) {
	t.Helper()

	status, header, body := e2etest.Get(t, e2etest.AddrURL(env.proxyAddr, "/public/e2e?from=e2e"), map[string]string{
		"Traceparent":  traceparent,
		"X-Shadow":     "force",
		"X-E2E":        "http",
		"X-Request-ID": "client-request",
	})
	if status != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d with body %q", status, body)
	}

	if got := header.Get("X-Trace-ID"); got != expectedTraceID {
		t.Fatalf("expected X-Trace-ID %q, got %q", expectedTraceID, got)
	}

	if got := header.Get("X-Request-ID"); got != "client-request" {
		t.Fatalf("expected preserved X-Request-ID, got %q", got)
	}

	if got := header.Get("X-Backend"); got != "primary" {
		t.Fatalf("expected primary response header, got %q", got)
	}

	if got := header.Get("X-Target"); got != "primary-static" {
		t.Fatalf("expected primary configured header echo, got %q", got)
	}

	e2etest.WaitFileContains(t, env.primaryLog, expectedTraceID, "Traceparent", "X-Trace-Id", "/primary/e2e", "from=e2e", "primary-static")
	e2etest.WaitFileContains(t, env.shadowLog, expectedTraceID, "Traceparent", "X-Trace-Id", "/shadow/e2e", "from=e2e", "shadow-static")
}

func assertHTTPNoShadowFlow(t *testing.T, env httpEnv) {
	t.Helper()

	status, header, body := e2etest.Get(t, e2etest.AddrURL(env.proxyAddr, "/public/no-shadow"), map[string]string{
		"X-E2E": "no-shadow",
	})
	if status != http.StatusOK {
		t.Fatalf("expected HTTP 200 for no-shadow request, got %d with body %q", status, body)
	}

	if got := header.Get("X-Request-ID"); got == "" {
		t.Fatalf("expected generated X-Request-ID")
	}

	e2etest.WaitFileContains(t, env.primaryLog, "/primary/no-shadow")
	time.Sleep(300 * time.Millisecond)
	e2etest.MustFileNotContain(t, env.shadowLog, "/shadow/no-shadow")
}

func assertHTTPBodyLimit(t *testing.T, env httpEnv) {
	t.Helper()

	status, _, _ := e2etest.Request(t, http.MethodPost, e2etest.AddrURL(env.proxyAddr, "/public/too-large"), "0123456789", nil)
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected HTTP 413 for oversized body, got %d", status)
	}

	time.Sleep(300 * time.Millisecond)
	e2etest.MustFileNotContain(t, env.primaryLog, "/primary/too-large")
	e2etest.MustFileNotContain(t, env.shadowLog, "/shadow/too-large")
}

func assertHTTPMetrics(t *testing.T, env httpEnv) {
	t.Helper()

	_, _, body := e2etest.Get(t, e2etest.AddrURL(env.metricsAddr, "/metrics"), nil)
	e2etest.MustContain(t, body, "doppelgaenger_ingress_requests_total")
	e2etest.MustContain(t, body, `protocol="http"`)
	e2etest.MustContain(t, body, `target="primary"`)
	e2etest.MustContain(t, body, `target="shadow"`)
	e2etest.MustContain(t, body, "doppelgaenger_comparisons_total")
	e2etest.MustContain(t, body, `result="diff"`)
	e2etest.MustContain(t, body, `result="skipped"`)
}

func assertOTLPSummary(t *testing.T, env httpEnv) {
	t.Helper()

	e2etest.WaitCondition(t, "HTTP OTLP summary", func() bool {
		_, _, body := e2etest.Get(t, env.collectorSummary, nil)

		return containsAll(body,
			"HTTP GET",
			"HTTP GET primary",
			"HTTP GET shadow",
			expectedTraceID,
			"doppelgaenger_ingress_requests",
			"doppelgaenger_backend_requests",
			"doppelgaenger_comparisons",
		)
	})
}

func containsAll(text string, fragments ...string) bool {
	for _, fragment := range fragments {
		if !contains(text, fragment) {
			return false
		}
	}

	return true
}

func contains(text, fragment string) bool {
	return strings.Contains(text, fragment)
}

func stopProcesses(t *testing.T, processes httpProcesses) {
	t.Helper()

	processes.doppel.Stop(t)
	processes.shadow.Stop(t)
	processes.primary.Stop(t)
	processes.collector.Stop(t)
}
