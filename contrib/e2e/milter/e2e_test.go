//go:build e2e

package milter_e2e

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"doppelgaenger/contrib/e2e/internal/e2etest"
)

func TestMilterObservabilityE2E(t *testing.T) {
	root := e2etest.RepoRoot(t)
	runDir := t.TempDir()
	binDir := filepath.Join(runDir, "bin")

	doppel := e2etest.Build(t, root, binDir, "doppelgaenger", ".")
	fakeMilter := e2etest.Build(t, root, binDir, "fakemilterserver", "./cmd/fakemilterserver")
	probe := e2etest.Build(t, root, binDir, "milterprobe", "./cmd/milterprobe")
	collector := e2etest.Build(t, root, binDir, "fakeotlpcollector", "./cmd/fakeotlpcollector")

	env := newMilterEnv(t, runDir)
	processes := startMilterStack(t, env, doppel, fakeMilter, collector)
	defer stopProcesses(t, processes)

	assertMilterFlow(t, env, probe)
	assertMilterMetrics(t, env)
	processes.doppel.Stop(t)
	assertOTLPSummary(t, env)
}

func TestMilterNoShadowE2E(t *testing.T) {
	root := e2etest.RepoRoot(t)
	runDir := t.TempDir()
	binDir := filepath.Join(runDir, "bin")

	doppel := e2etest.Build(t, root, binDir, "doppelgaenger", ".")
	fakeMilter := e2etest.Build(t, root, binDir, "fakemilterserver", "./cmd/fakemilterserver")
	probe := e2etest.Build(t, root, binDir, "milterprobe", "./cmd/milterprobe")
	collector := e2etest.Build(t, root, binDir, "fakeotlpcollector", "./cmd/fakeotlpcollector")

	env := newMilterEnvWithShadowPercent(t, runDir, 0)
	processes := startMilterStack(t, env, doppel, fakeMilter, collector)
	defer stopProcesses(t, processes)

	output := e2etest.RunCommand(t, "", nil, probe,
		"-addr", env.proxyAddr,
		"-command", "c",
		"-payload", "milter-no-shadow",
		"-expect-decision", "accept",
	)
	e2etest.MustContain(t, output, `"decision":"accept"`)
	e2etest.WaitFileContains(t, env.primaryLog, "bWlsdGVyLW5vLXNoYWRvdw==")
	e2etest.MustFileNotContain(t, env.shadowLog, "bWlsdGVyLW5vLXNoYWRvdw==")
}

type milterEnv struct {
	proxyAddr        string
	primaryAddr      string
	shadowAddr       string
	metricsAddr      string
	collectorAddr    string
	proxyConfig      string
	collectorLog     string
	primaryLog       string
	primaryProcLog   string
	shadowLog        string
	shadowProcLog    string
	doppelLog        string
	collectorHealth  string
	collectorSummary string
}

type milterProcesses struct {
	collector *e2etest.Process
	primary   *e2etest.Process
	shadow    *e2etest.Process
	doppel    *e2etest.Process
}

func newMilterEnv(t *testing.T, runDir string) milterEnv {
	t.Helper()

	return newMilterEnvWithShadowPercent(t, runDir, 100)
}

func newMilterEnvWithShadowPercent(t *testing.T, runDir string, shadowPercent int) milterEnv {
	t.Helper()

	env := milterEnv{
		proxyAddr:     e2etest.FreeAddr(t),
		primaryAddr:   e2etest.FreeAddr(t),
		shadowAddr:    e2etest.FreeAddr(t),
		metricsAddr:   e2etest.FreeAddr(t),
		collectorAddr: e2etest.FreeAddr(t),
	}

	env.proxyConfig = filepath.Join(runDir, "doppelgaenger-milter.yaml")
	env.collectorLog = filepath.Join(runDir, "collector.jsonl")
	env.primaryLog = filepath.Join(runDir, "primary-milter.jsonl")
	env.primaryProcLog = filepath.Join(runDir, "primary-milter.log")
	env.shadowLog = filepath.Join(runDir, "shadow-milter.jsonl")
	env.shadowProcLog = filepath.Join(runDir, "shadow-milter.log")
	env.doppelLog = filepath.Join(runDir, "doppelgaenger-milter.log")
	env.collectorHealth = e2etest.AddrURL(env.collectorAddr, "/healthz")
	env.collectorSummary = e2etest.AddrURL(env.collectorAddr, "/summary")

	e2etest.WriteFile(t, env.proxyConfig, proxyMilterConfig(env, shadowPercent))

	return env
}

func proxyMilterConfig(env milterEnv, shadowPercent int) string {
	return fmt.Sprintf(`protocol: milter
milter_listen_addr: "%s"
primary_milter_addr: "%s"
shadow_milter_addr: "%s"
milter_timeout: "2s"
shadow_sample_percent: %d
shadow_rps: 0
compare_mode: "header"
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
  otel_service_name: "doppelgaenger-e2e-milter"
  otel_exporter_otlp_endpoint: "http://%s"
  otel_exporter_otlp_insecure: true
  otel_sample_ratio: 1.0
  trace_id_header: "X-Trace-ID"
`, env.proxyAddr, env.primaryAddr, env.shadowAddr, shadowPercent, e2etest.PortOnly(env.metricsAddr), env.collectorAddr)
}

func startMilterStack(t *testing.T, env milterEnv, doppel, fakeMilter, collector string) milterProcesses {
	t.Helper()

	processes := milterProcesses{}
	processes.collector = e2etest.Start(t, "fakeotlpcollector", env.collectorLog, nil, collector, "-listen", env.collectorAddr)
	e2etest.WaitHTTP(t, env.collectorHealth)

	processes.primary = e2etest.Start(t, "fakemilter-primary", env.primaryProcLog, nil, fakeMilter,
		"-listen", env.primaryAddr,
		"-decision", "accept",
		"-log-file", env.primaryLog,
	)
	e2etest.WaitTCP(t, env.primaryAddr)

	processes.shadow = e2etest.Start(t, "fakemilter-shadow", env.shadowProcLog, nil, fakeMilter,
		"-listen", env.shadowAddr,
		"-decision", "reject",
		"-log-file", env.shadowLog,
	)
	e2etest.WaitTCP(t, env.shadowAddr)

	processes.doppel = e2etest.Start(t, "doppelgaenger-milter", env.doppelLog, nil, doppel, "--config", env.proxyConfig)
	e2etest.WaitTCP(t, env.proxyAddr)
	e2etest.WaitHTTP(t, e2etest.AddrURL(env.metricsAddr, "/metrics"))

	return processes
}

func assertMilterFlow(t *testing.T, env milterEnv, probe string) {
	t.Helper()

	const encodedPayload = "bWlsdGVyLWUyZQ=="

	output := e2etest.RunCommand(t, "", nil, probe,
		"-addr", env.proxyAddr,
		"-command", "c",
		"-payload", "milter-e2e",
		"-expect-decision", "accept",
	)
	e2etest.MustContain(t, output, `"decision":"accept"`)
	e2etest.WaitFileContains(t, env.primaryLog, `"command":"c"`, `"decision":"accept"`, encodedPayload)
	e2etest.WaitFileContains(t, env.shadowLog, `"command":"c"`, `"decision":"reject"`, encodedPayload)
}

func assertMilterMetrics(t *testing.T, env milterEnv) {
	t.Helper()

	_, _, body := e2etest.Get(t, e2etest.AddrURL(env.metricsAddr, "/metrics"), nil)
	e2etest.MustContain(t, body, "doppelgaenger_ingress_requests_total")
	e2etest.MustContain(t, body, `protocol="milter"`)
	e2etest.MustContain(t, body, `target="primary"`)
	e2etest.MustContain(t, body, `target="shadow"`)
	e2etest.MustContain(t, body, `result="diff"`)
}

func assertOTLPSummary(t *testing.T, env milterEnv) {
	t.Helper()

	e2etest.WaitCondition(t, "Milter OTLP summary", func() bool {
		_, _, body := e2etest.Get(t, env.collectorSummary, nil)

		return containsAll(body,
			"milter c",
			"milter primary c",
			"milter shadow c",
			"doppelgaenger_ingress_requests",
			"doppelgaenger_backend_requests",
			"doppelgaenger_comparisons",
		)
	})
}

func containsAll(text string, fragments ...string) bool {
	for _, fragment := range fragments {
		if !strings.Contains(text, fragment) {
			return false
		}
	}

	return true
}

func stopProcesses(t *testing.T, processes milterProcesses) {
	t.Helper()

	processes.doppel.Stop(t)
	processes.shadow.Stop(t)
	processes.primary.Stop(t)
	processes.collector.Stop(t)
}
