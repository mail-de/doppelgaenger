//go:build e2e

package docker_e2e

import (
	"os"
	"path/filepath"
	"testing"

	"doppelgaenger/contrib/e2e/internal/e2etest"
)

func TestDockerRuntimeConfigIsConsistent(t *testing.T) {
	root := e2etest.RepoRoot(t)

	dockerfile := readRepoFile(t, root, "Dockerfile")
	compose := readRepoFile(t, root, "docker-compose.yml")
	config := readRepoFile(t, root, "config.docker.yaml")

	e2etest.MustContain(t, dockerfile, "EXPOSE 8443")
	e2etest.MustContain(t, compose, `"8443:8443"`)
	e2etest.MustContain(t, compose, "./config.docker.yaml:/app/config.yaml:ro")
	e2etest.MustContain(t, config, `listen_addr: ":8443"`)
}

func readRepoFile(t *testing.T, root, name string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}

	return string(content)
}
