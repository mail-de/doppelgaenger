//go:build e2e

// Package e2etest contains helpers for blackbox E2E tests.
package e2etest

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const waitTimeout = 15 * time.Second

// Process wraps a started E2E process.
type Process struct {
	Name string

	cmd      *exec.Cmd
	done     chan error
	log      *os.File
	stopOnce sync.Once
}

// PortOnly returns the port part of host:port, or the original value on parse errors.
func PortOnly(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}

	return port
}

// RepoRoot returns the repository root by walking up to go.mod.
func RepoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("repository root not found from %s", dir)
		}

		dir = parent
	}
}

// FreeAddr returns a currently free loopback TCP address.
func FreeAddr(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate TCP port: %v", err)
	}

	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close port probe: %v", err)
	}

	return addr
}

// Build builds one package target to a binary path.
func Build(t *testing.T, root, binDir, name, target string) string {
	t.Helper()

	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("create binary directory: %v", err)
	}

	path := filepath.Join(binDir, name)
	cmd := exec.Command("go", "build", "-mod=vendor", "-o", path, target)
	cmd.Dir = root

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build %s: %v\n%s", target, err, string(output))
	}

	return path
}

// WriteFile writes an E2E runtime file.
func WriteFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// Start starts a long-running E2E process and writes combined logs to logPath.
func Start(t *testing.T, name, logPath string, env []string, binary string, args ...string) *Process {
	t.Helper()

	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create %s log: %v", name, err)
	}

	cmd := exec.Command(binary, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("start %s: %v", name, err)
	}

	process := &Process{
		Name: name,
		cmd:  cmd,
		done: make(chan error, 1),
		log:  logFile,
	}

	go func() {
		process.done <- cmd.Wait()
	}()

	return process
}

// Stop terminates a process and waits for it to exit.
func (p *Process) Stop(t *testing.T) {
	t.Helper()

	if p == nil || p.cmd.Process == nil {
		return
	}

	p.stopOnce.Do(func() {
		_ = p.cmd.Process.Signal(os.Interrupt)
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
			_ = p.cmd.Process.Kill()
			<-p.done
		}

		if p.log != nil {
			_ = p.log.Close()
		}
	})
}

// WaitHTTP waits until an HTTP endpoint returns a 2xx response.
func WaitHTTP(t *testing.T, url string) {
	t.Helper()

	client := http.Client{Timeout: time.Second}
	WaitCondition(t, "HTTP "+url, func() bool {
		resp, err := client.Get(url)
		if err != nil {
			return false
		}
		defer resp.Body.Close()

		return resp.StatusCode >= 200 && resp.StatusCode < 300
	})
}

// WaitTCP waits until a TCP listener accepts connections.
func WaitTCP(t *testing.T, addr string) {
	t.Helper()

	WaitCondition(t, "TCP "+addr, func() bool {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return false
		}

		_ = conn.Close()

		return true
	})
}

// WaitFileContains waits until path contains all fragments.
func WaitFileContains(t *testing.T, path string, fragments ...string) {
	t.Helper()

	WaitCondition(t, "file "+path, func() bool {
		content, err := os.ReadFile(path)
		if err != nil {
			return false
		}

		text := string(content)
		for _, fragment := range fragments {
			if !strings.Contains(text, fragment) {
				return false
			}
		}

		return true
	})
}

// MustFileNotContain fails when path contains fragment.
func MustFileNotContain(t *testing.T, path, fragment string) {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	if strings.Contains(string(content), fragment) {
		t.Fatalf("expected %s not to contain %q", path, fragment)
	}
}

// WaitCondition waits for a condition to become true.
func WaitCondition(t *testing.T, label string, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}

		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %s", label)
}

// Get reads one HTTP endpoint and returns status, headers, and body.
func Get(t *testing.T, url string, headers map[string]string) (int, http.Header, string) {
	t.Helper()

	return Request(t, http.MethodGet, url, "", headers)
}

// Request sends one HTTP request and returns status, headers, and body.
func Request(t *testing.T, method, url, requestBody string, headers map[string]string) (int, http.Header, string) {
	t.Helper()

	request, err := http.NewRequest(method, url, strings.NewReader(requestBody))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}

	for key, value := range headers {
		request.Header.Set(key, value)
	}

	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}

	return response.StatusCode, response.Header.Clone(), string(responseBody)
}

// MustContain fails the test if text does not contain fragment.
func MustContain(t *testing.T, text, fragment string) {
	t.Helper()

	if !strings.Contains(text, fragment) {
		t.Fatalf("expected %q to contain %q", text, fragment)
	}
}

// RunCommand runs a short command and returns combined output.
func RunCommand(t *testing.T, dir string, env []string, binary string, args ...string) string {
	t.Helper()

	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run %s %s: %v\n%s", binary, strings.Join(args, " "), err, string(output))
	}

	return string(output)
}

// AddrURL returns a URL for a loopback address and path.
func AddrURL(addr, path string) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	return fmt.Sprintf("http://%s%s", addr, path)
}
