//go:build e2e

package docker_e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"doppelgaenger/contrib/e2e/internal/e2etest"
)

func TestReleaseUploadsEveryPackage(t *testing.T) {
	script := packageUploadScript(t)
	for _, test := range []struct {
		name        string
		packages    []string
		missing     bool
		wantSuccess bool
	}{
		{"main and debug RPM", []string{"main.rpm", "debug.rpm"}, false, true},
		{"DEB", []string{"main.deb"}, false, true},
		{"missing SBOM", []string{"main.rpm"}, true, false},
		{"no packages", nil, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			preparePackageUpload(t, root, test.packages, test.missing)
			extension := "rpm"
			if len(test.packages) > 0 {
				extension = strings.TrimPrefix(filepath.Ext(test.packages[0]), ".")
			}
			cmd := exec.Command("bash", "-euo", "pipefail", "-c", script)
			cmd.Dir = root
			capture := filepath.Join(root, "uploaded")
			cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+":"+os.Getenv("PATH"), "PKG_EXTENSION="+extension, "GITHUB_REF_NAME=v1.2.3", "GITHUB_REPOSITORY=example/proxy", "GH_CAPTURE="+capture)
			output, err := cmd.CombinedOutput()
			if (err == nil) != test.wantSuccess {
				t.Fatalf("unexpected upload status: %v: %s", err, output)
			}
			if !test.wantSuccess {
				if _, err := os.Stat(capture); !os.IsNotExist(err) {
					t.Fatal("incomplete packages reached upload")
				}
				return
			}
			uploaded, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range test.packages {
				for _, asset := range []string{"./" + name, "./" + name + ".sha256", "dist/sbom/" + name + ".spdx.json"} {
					if !strings.Contains(string(uploaded), asset+"\n") {
						t.Fatalf("missing asset %s: %s", asset, uploaded)
					}
				}
			}
		})
	}
}

func packageUploadScript(t *testing.T) string {
	t.Helper()
	raw := readRepoFile(t, e2etest.RepoRoot(t), ".github/workflows/build-stable.yaml")
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string
				Run  string
			}
		}
	}
	if err := yaml.Unmarshal([]byte(raw), &workflow); err != nil {
		t.Fatal(err)
	}
	for _, step := range workflow.Jobs["upload-linux-packages"].Steps {
		if step.Name == "Upload package assets" {
			return step.Run
		}
	}
	t.Fatal("package upload step missing")
	return ""
}

func preparePackageUpload(t *testing.T, root string, packages []string, missing bool) {
	t.Helper()
	for _, dir := range []string{"bin", "dist/sbom"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	mock := "#!/bin/bash\nprintf '%s\\n' \"$@\" >\"$GH_CAPTURE\"\n"
	if err := os.WriteFile(filepath.Join(root, "bin/gh"), []byte(mock), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range packages {
		files := []string{name, name + ".sha256"}
		if !missing {
			files = append(files, "dist/sbom/"+name+".spdx.json")
		}
		for _, file := range files {
			if err := os.WriteFile(filepath.Join(root, file), []byte("fixture"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
