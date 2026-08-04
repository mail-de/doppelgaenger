//go:build e2e

package milter_e2e

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"doppelgaenger/contrib/e2e/internal/e2etest"
	"doppelgaenger/internal/protocol"
)

const rspamdMilterImage = "rspamd/rspamd:4.1.1"

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

func TestRspamdMilterReplySemanticsE2E(t *testing.T) {
	requireLocalRspamdImage(t)

	root := e2etest.RepoRoot(t)
	runDir := t.TempDir()
	binDir := filepath.Join(runDir, "bin")
	doppel := e2etest.Build(t, root, binDir, "doppelgaenger", ".")
	primaryAddr := e2etest.FreeAddr(t)
	shadowAddr := e2etest.FreeAddr(t)
	proxyAddr := e2etest.FreeAddr(t)

	startRspamdMilter(t, runDir, "primary", primaryAddr)
	startRspamdMilter(t, runDir, "shadow", shadowAddr)
	configPath := filepath.Join(runDir, "doppelgaenger-rspamd-milter.yaml")
	logPath := filepath.Join(runDir, "doppelgaenger-rspamd-milter.log")
	e2etest.WriteFile(t, configPath, fmt.Sprintf(`protocol: milter
milter_listen_addr: "%s"
primary_milter_addr: "%s"
shadow_milter_addr: "%s"
milter_timeout: "15s"
shadow_sample_percent: 100
shadow_rps: 0
compare_mode: "header"
log_json: true
`, proxyAddr, primaryAddr, shadowAddr))

	doppelProcess := e2etest.Start(t, "doppelgaenger-rspamd-milter", logPath, nil, doppel, "--config", configPath)
	defer doppelProcess.Stop(t)
	e2etest.WaitTCP(t, proxyAddr)

	runRspamdMilterTransaction(t, proxyAddr)
	e2etest.WaitFileContains(t, logPath, `"command":"E"`, `"shadow_ok":true`)
}

func requireLocalRspamdImage(t *testing.T) {
	t.Helper()

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker is unavailable; skipping real Rspamd Milter E2E")
	}

	if output, err := exec.Command("docker", "image", "inspect", rspamdMilterImage).CombinedOutput(); err != nil {
		t.Skipf("local %s image is unavailable; skipping real Rspamd Milter E2E: %s", rspamdMilterImage, strings.TrimSpace(string(output)))
	}
}

func startRspamdMilter(t *testing.T, runDir, role, addr string) {
	t.Helper()

	configDir := filepath.Join(runDir, "rspamd-"+role)
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("create Rspamd Milter config directory: %v", err)
	}
	e2etest.WriteFile(t, filepath.Join(configDir, "worker-proxy.inc"), `bind_socket = "0.0.0.0:11332";
milter = true;
timeout = 30s;
upstream "local" {
  default = true;
  hosts = "localhost";
}
`)

	name := fmt.Sprintf("doppelgaenger-e2e-rspamd-%s-%d", role, time.Now().UnixNano())
	args := []string{
		"run", "--detach", "--rm", "--name", name,
		"--publish", addr + ":11332",
		"--volume", configDir + ":/etc/rspamd/local.d:ro",
		rspamdMilterImage,
	}
	output, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("start real Rspamd Milter %s: %v\n%s", role, err, string(output))
	}

	t.Cleanup(func() {
		output, err := exec.Command("docker", "rm", "--force", name).CombinedOutput()
		if err != nil {
			t.Logf("remove real Rspamd Milter %s: %v: %s", role, err, strings.TrimSpace(string(output)))
		}
	})
	e2etest.WaitTCP(t, addr)
}

func runRspamdMilterTransaction(t *testing.T, addr string) {
	t.Helper()

	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("dial Doppelgaenger Rspamd Milter: %v", err)
	}
	defer func() { _ = conn.Close() }()

	writeRspamdMilterFrame(t, conn, 'O', rspamdOptionNegotiationPayload())
	assertRspamdMilterReply(t, conn, 'O', time.Second)

	for _, frame := range []protocol.MilterFrame{
		{Command: 'D', Payload: []byte("Cj\x00mail.example.test\x00")},
		{Command: 'C', Payload: rspamdConnectPayload()},
		{Command: 'H', Payload: []byte("client.example.test\x00")},
		{Command: 'M', Payload: []byte("<sender@example.test>\x00")},
		{Command: 'R', Payload: []byte("<recipient@example.test>\x00")},
		{Command: 'L', Payload: []byte("Subject\x00Rspamd Milter E2E\x00")},
		{Command: 'N'},
		{Command: 'B', Payload: []byte("message body\r\n")},
	} {
		writeRspamdMilterFrame(t, conn, frame.Command, frame.Payload)
		assertRspamdMilterNoReply(t, conn, frame.Command)
	}

	writeRspamdMilterFrame(t, conn, 'E', nil)
	assertRspamdMilterTerminalReply(t, conn)
}

func rspamdOptionNegotiationPayload() []byte {
	payload := make([]byte, 12)
	binary.BigEndian.PutUint32(payload, 6)

	return payload
}

func rspamdConnectPayload() []byte {
	payload := append([]byte("client.example.test\x00"), '4')
	port := make([]byte, 2)
	binary.BigEndian.PutUint16(port, 25)
	payload = append(payload, port...)

	return append(payload, []byte("127.0.0.1\x00")...)
}

func writeRspamdMilterFrame(t *testing.T, conn net.Conn, command byte, payload []byte) {
	t.Helper()

	frame := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(frame, uint32(1+len(payload)))
	frame[4] = command
	copy(frame[5:], payload)
	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("write Rspamd Milter %q frame: %v", command, err)
	}
}

func assertRspamdMilterReply(t *testing.T, conn net.Conn, expected byte, timeout time.Duration) {
	t.Helper()

	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatalf("set Rspamd Milter reply deadline: %v", err)
	}

	frame, err := protocol.ReadFrame(conn)
	if err != nil {
		t.Fatalf("read Rspamd Milter %q reply: %v", expected, err)
	}

	if frame.Command != expected {
		t.Fatalf("expected Rspamd Milter reply %q, got %q", expected, frame.Command)
	}
}

func assertRspamdMilterNoReply(t *testing.T, conn net.Conn, command byte) {
	t.Helper()

	if err := conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		t.Fatalf("set Rspamd Milter no-reply deadline: %v", err)
	}

	_, err := protocol.ReadFrame(conn)
	if err == nil {
		t.Fatalf("expected no Rspamd Milter reply for %q", command)
	}

	if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("expected no-reply timeout for Rspamd Milter %q, got %v", command, err)
	}
}

func assertRspamdMilterTerminalReply(t *testing.T, conn net.Conn) {
	t.Helper()

	if err := conn.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatalf("set Rspamd Milter EOM deadline: %v", err)
	}

	for {
		frame, err := protocol.ReadFrame(conn)
		if err != nil {
			t.Fatalf("read Rspamd Milter EOM reply: %v", err)
		}

		switch frame.Command {
		case 'a', 'c', 'd', 'r', 't':
			return
		}
	}
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
