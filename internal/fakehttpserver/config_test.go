package fakehttpserver

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadReadsConfigFile(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "fakehttpserver.yaml")
	configContent := []byte(`
listen_addr: ":9100"
mode: random
echo_headers:
  - "X-Test"
random_chance: 42
log_json: false
`)
	if err := os.WriteFile(configPath, configContent, 0o600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	t.Setenv("CONFIG_FILE", configPath)
	loaded, err := Load()
	if err != nil {
		t.Fatalf("expected config to load, got error: %v", err)
	}
	if loaded.ListenAddr != ":9100" {
		t.Fatalf("expected listen address to be set, got %q", loaded.ListenAddr)
	}
	if loaded.Mode != "random" {
		t.Fatalf("expected mode random, got %q", loaded.Mode)
	}
	if len(loaded.RandomHeaders) != 1 || loaded.RandomHeaders[0] != "X-Test" {
		t.Fatalf("expected random headers to inherit echo headers")
	}
	if loaded.RandomChance != 42 {
		t.Fatalf("expected random chance 42, got %d", loaded.RandomChance)
	}
	if loaded.LogJSON {
		t.Fatalf("expected log_json to be false")
	}
}
