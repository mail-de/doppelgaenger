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

	e2etest.MustContain(t, dockerfile, "EXPOSE 8080 8443 9444 9464 9999")
	e2etest.MustContain(t, compose, `"8443:8443"`)
	e2etest.MustContain(t, compose, "./config.docker.yaml:/etc/doppelgaenger/config.yaml:ro")
	e2etest.MustContain(t, config, `listen_addr: ":8443"`)
}

func TestContainerImagesIncludeProjectLicense(t *testing.T) {
	root := e2etest.RepoRoot(t)
	license := readRepoFile(t, root, "LICENSE")

	e2etest.MustContain(t, license, "MIT License")
	e2etest.MustContain(t, license, "Copyright (c) 2026 mail.de GmbH")

	for _, name := range []string{"Dockerfile", "Dockerfile.faker"} {
		t.Run(name, func(t *testing.T) {
			dockerfile := readRepoFile(t, root, name)
			e2etest.MustContain(t, dockerfile, `org.opencontainers.image.authors="Christian Rößner <c.roessner@team.mail.de>"`)
			e2etest.MustContain(t, dockerfile, `org.opencontainers.image.licenses="MIT"`)
			e2etest.MustContain(t, dockerfile, "COPY --chmod=0444 LICENSE /app/LICENSE")
			e2etest.MustContain(t, dockerfile, "USER 10001:10001")
			e2etest.MustContain(t, dockerfile, "ARG RUNTIME_IMAGE=scratch")
		})
	}
}

func readRepoFile(t *testing.T, root, name string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}

	return string(content)
}
