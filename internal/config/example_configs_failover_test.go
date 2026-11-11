package config

import (
	"path/filepath"
	"testing"
)

func TestExampleConfigsLoadWithFailoverBlocks(t *testing.T) {
	for _, name := range []string{"config.yaml", "config.docker.yaml"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("CONFIG_FILE", filepath.Join("..", "..", name))

			loaded, err := Load()
			if err != nil {
				t.Fatalf("load %s: %v", name, err)
			}

			if loaded.PrimaryFailover.Enabled || loaded.ShadowFailover.Enabled {
				t.Fatalf("%s must ship with failover disabled", name)
			}
		})
	}
}
