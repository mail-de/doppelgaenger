package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadReadsConfigFile(t *testing.T) {
	loaded := loadTestConfig(t, []byte(`
protocol: http
primary_base_urls:
  - "https://primary-a.example.com"
  - "https://primary-b.example.com"
primary_selection_mode: "source_ip_hash"
shadow_base_urls:
  - "https://shadow-a.example.com"
  - "https://shadow-b.example.com"
shadow_selection_mode: "source_ip_hash"
shadow_sample_percent: 20
log_json: false
path_mapping:
  mode: rewrite
  rules:
    - match: "^/v1/(.*)$"
      primary: "/api/$1"
      shadow: "/shadow/$1"
run_as_user: " nobody "
run_as_group: " 33 "
chroot: " /var/empty "
observability:
  prometheus_enabled: false
  otel_sample_ratio: 0.0
  trace_id_header: "x-trace-id"
`))
	assertBackendConfig(t, loaded)
	assertRuntimeConfig(t, loaded)
	assertHTTPDefaults(t, loaded)
	assertObservabilityConfig(t, loaded)
}

func loadTestConfig(t *testing.T, configContent []byte) Config {
	t.Helper()

	tempDir := t.TempDir()

	configPath := filepath.Join(tempDir, "config.yaml")
	if err := os.WriteFile(configPath, configContent, 0o600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	t.Setenv("CONFIG_FILE", configPath)

	loaded, err := Load()
	if err != nil {
		t.Fatalf("expected config to load, got error: %v", err)
	}

	return loaded
}

func assertBackendConfig(t *testing.T, loaded Config) {
	t.Helper()

	if len(loaded.PrimaryBaseURLs) != 2 {
		t.Fatalf("expected 2 primary URLs, got %d", len(loaded.PrimaryBaseURLs))
	}

	if loaded.PrimarySelectionMode != selectionSourceIPHash {
		t.Fatalf("expected primary_selection_mode source_ip_hash, got %q", loaded.PrimarySelectionMode)
	}

	if len(loaded.ShadowBaseURLs) != 2 {
		t.Fatalf("expected 2 shadow URLs, got %d", len(loaded.ShadowBaseURLs))
	}

	if loaded.ShadowSelectionMode != selectionSourceIPHash {
		t.Fatalf("expected shadow_selection_mode source_ip_hash, got %q", loaded.ShadowSelectionMode)
	}

	if loaded.ShadowSamplePercent != 20 {
		t.Fatalf("expected shadow sample percent 20, got %d", loaded.ShadowSamplePercent)
	}

	if loaded.LogJSON {
		t.Fatalf("expected log_json to be false")
	}

	if loaded.PathMapping.Mode != "rewrite" {
		t.Fatalf("expected path mapping mode rewrite, got %q", loaded.PathMapping.Mode)
	}
}

func assertRuntimeConfig(t *testing.T, loaded Config) {
	t.Helper()

	if loaded.RunAsUser != "nobody" {
		t.Fatalf("expected run_as_user to be trimmed, got %q", loaded.RunAsUser)
	}

	if loaded.RunAsGroup != "33" {
		t.Fatalf("expected run_as_group to be trimmed, got %q", loaded.RunAsGroup)
	}

	if loaded.ChrootDir != "/var/empty" {
		t.Fatalf("expected chroot to be trimmed, got %q", loaded.ChrootDir)
	}
}

func assertHTTPDefaults(t *testing.T, loaded Config) {
	t.Helper()

	if loaded.UpstreamHTTPDialTimeout != 2*time.Second {
		t.Fatalf("expected upstream_http_dial_timeout default 2s, got %s", loaded.UpstreamHTTPDialTimeout)
	}

	if loaded.UpstreamHTTPTLSHandshakeTimeout != 5*time.Second {
		t.Fatalf("expected upstream_http_tls_handshake_timeout default 5s, got %s", loaded.UpstreamHTTPTLSHandshakeTimeout)
	}

	if loaded.UpstreamHTTPResponseHeaderTimeout != 5*time.Second {
		t.Fatalf("expected upstream_http_response_header_timeout default 5s, got %s", loaded.UpstreamHTTPResponseHeaderTimeout)
	}

	if loaded.UpstreamHTTPMaxIdleConns != 1024 {
		t.Fatalf("expected upstream_http_max_idle_conns default 1024, got %d", loaded.UpstreamHTTPMaxIdleConns)
	}

	if loaded.UpstreamHTTPMaxIdleConnsPerHost != 256 {
		t.Fatalf("expected upstream_http_max_idle_conns_per_host default 256, got %d", loaded.UpstreamHTTPMaxIdleConnsPerHost)
	}

	if loaded.UpstreamHTTPMaxConnsPerHost != 0 {
		t.Fatalf("expected upstream_http_max_conns_per_host default 0, got %d", loaded.UpstreamHTTPMaxConnsPerHost)
	}
}

func assertObservabilityConfig(t *testing.T, loaded Config) {
	t.Helper()

	if loaded.Observability.PrometheusEnabled {
		t.Fatalf("expected prometheus to be disabled")
	}

	if loaded.Observability.OTelSampleRatio == nil {
		t.Fatalf("expected otel_sample_ratio to be decoded")
	}

	if *loaded.Observability.OTelSampleRatio != 0.0 {
		t.Fatalf("expected otel_sample_ratio 0.0, got %f", *loaded.Observability.OTelSampleRatio)
	}

	if loaded.Observability.TraceIDHeader != "X-Trace-Id" {
		t.Fatalf("expected canonical trace_id_header, got %q", loaded.Observability.TraceIDHeader)
	}
}

func TestLoadRequiresPrimaryAndShadowBaseURLs(t *testing.T) {
	expectLoadFailure(t, []byte(`
protocol: http
primary_base_urls: []
shadow_base_urls: []
`), "expected config loading to fail for empty backend lists")
}

func TestLoadRejectsIncompleteObservabilityConfig(t *testing.T) {
	expectLoadFailure(t, []byte(`
protocol: http
primary_base_urls:
  - "https://primary.example.com"
shadow_base_urls:
  - "https://shadow.example.com"
observability:
  otel_enabled: true
  otel_traces_enabled: true
`), "expected config loading to fail without otel_exporter_otlp_endpoint")
}

func expectLoadFailure(t *testing.T, configContent []byte, message string) {
	t.Helper()

	tempDir := t.TempDir()

	configPath := filepath.Join(tempDir, "config.yaml")
	if err := os.WriteFile(configPath, configContent, 0o600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	t.Setenv("CONFIG_FILE", configPath)

	_, err := Load()
	if err == nil {
		t.Fatal(message)
	}
}
