package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadReadsConfigFile(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	configContent := []byte(`
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
`)
	if err := os.WriteFile(configPath, configContent, 0o600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	t.Setenv("CONFIG_FILE", configPath)
	loaded, err := Load()
	if err != nil {
		t.Fatalf("expected config to load, got error: %v", err)
	}

	if len(loaded.PrimaryBaseURLs) != 2 {
		t.Fatalf("expected 2 primary URLs, got %d", len(loaded.PrimaryBaseURLs))
	}
	if loaded.PrimarySelectionMode != "source_ip_hash" {
		t.Fatalf("expected primary_selection_mode source_ip_hash, got %q", loaded.PrimarySelectionMode)
	}
	if len(loaded.ShadowBaseURLs) != 2 {
		t.Fatalf("expected 2 shadow URLs, got %d", len(loaded.ShadowBaseURLs))
	}
	if loaded.ShadowSelectionMode != "source_ip_hash" {
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
	if loaded.RunAsUser != "nobody" {
		t.Fatalf("expected run_as_user to be trimmed, got %q", loaded.RunAsUser)
	}
	if loaded.RunAsGroup != "33" {
		t.Fatalf("expected run_as_group to be trimmed, got %q", loaded.RunAsGroup)
	}
	if loaded.ChrootDir != "/var/empty" {
		t.Fatalf("expected chroot to be trimmed, got %q", loaded.ChrootDir)
	}
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

func TestLoadRequiresPrimaryAndShadowBaseURLs(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	configContent := []byte(`
protocol: http
primary_base_urls: []
shadow_base_urls: []
`)
	if err := os.WriteFile(configPath, configContent, 0o600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	t.Setenv("CONFIG_FILE", configPath)
	_, err := Load()
	if err == nil {
		t.Fatalf("expected config loading to fail for empty backend lists")
	}
}
