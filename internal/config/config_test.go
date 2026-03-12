package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadReadsConfigFile(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	configContent := []byte(`
protocol: http
primary_base_url: "https://primary.example.com"
primary_base_urls:
  - "https://primary-a.example.com"
  - "https://primary-b.example.com"
primary_selection_mode: "source_ip_hash"
shadow_base_url: "https://shadow.example.com"
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

	if loaded.PrimaryBaseURL == nil || loaded.PrimaryBaseURL.Host != "primary-a.example.com" {
		t.Fatalf("expected first primary URL to be selected")
	}
	if len(loaded.PrimaryBaseURLs) != 2 {
		t.Fatalf("expected 2 primary URLs, got %d", len(loaded.PrimaryBaseURLs))
	}
	if loaded.PrimarySelectionMode != "source_ip_hash" {
		t.Fatalf("expected primary_selection_mode source_ip_hash, got %q", loaded.PrimarySelectionMode)
	}
	if loaded.ShadowBaseURL == nil || loaded.ShadowBaseURL.Host != "shadow.example.com" {
		t.Fatalf("expected shadow URL to be parsed")
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
}

func TestLoadSupportsLegacySinglePrimaryURL(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	configContent := []byte(`
protocol: http
primary_base_url: "https://legacy-primary.example.com"
shadow_base_url: "https://shadow.example.com"
`)
	if err := os.WriteFile(configPath, configContent, 0o600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	t.Setenv("CONFIG_FILE", configPath)
	loaded, err := Load()
	if err != nil {
		t.Fatalf("expected config to load, got error: %v", err)
	}

	if len(loaded.PrimaryBaseURLs) != 1 {
		t.Fatalf("expected one derived primary URL, got %d", len(loaded.PrimaryBaseURLs))
	}
	if loaded.PrimaryBaseURLs[0].Host != "legacy-primary.example.com" {
		t.Fatalf("expected derived primary host legacy-primary.example.com, got %q", loaded.PrimaryBaseURLs[0].Host)
	}
	if loaded.PrimarySelectionMode != "round_robin" {
		t.Fatalf("expected default primary selection mode round_robin, got %q", loaded.PrimarySelectionMode)
	}
}
