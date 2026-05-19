//go:build e2e

package http_e2e

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"doppelgaenger/contrib/e2e/internal/e2etest"
)

const smokeHost = "login.example.test"

func TestHTTPUpstreamProtocolSmokeE2E(t *testing.T) {
	root := e2etest.RepoRoot(t)
	runDir := t.TempDir()
	binDir := filepath.Join(runDir, "bin")
	doppel := e2etest.Build(t, root, binDir, "doppelgaenger", ".")

	tests := []struct {
		name     string
		protocol string
		want     string
	}{
		{name: "auto negotiates HTTP/2", protocol: "auto", want: "HTTP/2.0"},
		{name: "http1 disables HTTP/2", protocol: "http1", want: "HTTP/1.1"},
		{name: "http2 requires HTTP/2", protocol: "http2", want: "HTTP/2.0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Upstream-Proto", r.Proto)
				w.Header().Set("X-Upstream-Host", r.Host)
				w.Header().Set("X-Forwarded-Host", r.Header.Get("X-Forwarded-Host"))
				w.Header().Set("X-Forwarded-Proto", r.Header.Get("X-Forwarded-Proto"))
				w.Header().Set("X-Forwarded-For", r.Header.Get("X-Forwarded-For"))
				w.Header().Set("X-Real-IP", r.Header.Get("X-Real-IP"))
				w.WriteHeader(http.StatusNoContent)
			}))
			upstream.EnableHTTP2 = true
			upstream.StartTLS()
			defer upstream.Close()

			env := newProtocolSmokeEnv(t, tt.protocol, upstream.URL)
			process := e2etest.Start(t, "doppelgaenger-http-protocol-"+tt.protocol, env.doppelLog, nil, doppel, "--config", env.proxyConfig)
			defer process.Stop(t)

			e2etest.WaitHTTP(t, e2etest.AddrURL(env.proxyAddr, "/ready"))

			status, header := protocolSmokeRequest(t, e2etest.AddrURL(env.proxyAddr, "/oidc/authorize"))
			if status != http.StatusNoContent {
				t.Fatalf("expected HTTP 204, got %d", status)
			}

			if got := header.Get("X-Upstream-Proto"); got != tt.want {
				t.Fatalf("expected upstream protocol %s, got %s", tt.want, got)
			}

			if got := header.Get("X-Upstream-Host"); got != smokeHost {
				t.Fatalf("expected upstream host %q, got %q", smokeHost, got)
			}

			if got := header.Get("X-Forwarded-Host"); got != smokeHost {
				t.Fatalf("expected X-Forwarded-Host %q, got %q", smokeHost, got)
			}

			if got := header.Get("X-Forwarded-Proto"); got != "https" {
				t.Fatalf("expected X-Forwarded-Proto https, got %q", got)
			}

			if got := header.Get("X-Forwarded-For"); got == "" {
				t.Fatalf("expected X-Forwarded-For to be set")
			}

			if got := header.Get("X-Real-IP"); got == "" {
				t.Fatalf("expected X-Real-IP to be set")
			}
		})
	}
}

func TestHTTPRedirectSmokeE2E(t *testing.T) {
	root := e2etest.RepoRoot(t)
	runDir := t.TempDir()
	binDir := filepath.Join(runDir, "bin")
	doppel := e2etest.Build(t, root, binDir, "doppelgaenger", ".")

	var followed bool
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ready":
			w.WriteHeader(http.StatusNoContent)
		case "/oidc/authorize":
			http.Redirect(w, r, "/oidc/authorize/de", http.StatusFound)
		case "/oidc/authorize/de":
			followed = true
			w.WriteHeader(http.StatusUnauthorized)
		default:
			t.Fatalf("unexpected upstream path %q", r.URL.Path)
		}
	}))
	defer upstream.Close()

	env := newProtocolSmokeEnv(t, "http1", upstream.URL)
	process := e2etest.Start(t, "doppelgaenger-http-redirect", env.doppelLog, nil, doppel, "--config", env.proxyConfig)
	defer process.Stop(t)

	e2etest.WaitHTTP(t, e2etest.AddrURL(env.proxyAddr, "/ready"))

	status, header := redirectSmokeRequest(t, e2etest.AddrURL(env.proxyAddr, "/oidc/authorize"))
	if status != http.StatusFound {
		t.Fatalf("expected HTTP 302, got %d", status)
	}

	if got := header.Get("Location"); got != "/oidc/authorize/de" {
		t.Fatalf("expected Location /oidc/authorize/de, got %q", got)
	}

	if followed {
		t.Fatalf("expected upstream redirect not to be followed by proxy")
	}
}

type protocolSmokeEnv struct {
	proxyAddr   string
	proxyConfig string
	doppelLog   string
}

func newProtocolSmokeEnv(t *testing.T, protocol, upstreamURL string) protocolSmokeEnv {
	t.Helper()

	runDir := t.TempDir()
	env := protocolSmokeEnv{
		proxyAddr:   e2etest.FreeAddr(t),
		proxyConfig: filepath.Join(runDir, "doppelgaenger.yaml"),
		doppelLog:   filepath.Join(runDir, "doppelgaenger.log"),
	}

	e2etest.WriteFile(t, env.proxyConfig, fmt.Sprintf(`protocol: http
listen_addr: "%s"
primary_base_urls:
  - "%s"
shadow_base_urls:
  - "%s"
shadow_sample_percent: 0
shadow_force_header: ""
shadow_rps: 0
shadow_timeout: "500ms"
insecure_upstream: true
upstream_http_protocol: "%s"
forward_response_headers:
  - "X-Upstream-Proto"
  - "X-Upstream-Host"
  - "X-Forwarded-Host"
  - "X-Forwarded-Proto"
  - "X-Forwarded-For"
  - "X-Real-IP"
path_rules:
  - name: oidc-smoke
    match: "^/oidc/.*$"
    shadow: never
    compare: off
log_json: true
`, env.proxyAddr, upstreamURL, upstreamURL, protocol))

	return env
}

func protocolSmokeRequest(t *testing.T, url string) (int, http.Header) {
	t.Helper()

	return runProtocolSmokeRequest(t, url, nil)
}

func redirectSmokeRequest(t *testing.T, url string) (int, http.Header) {
	t.Helper()

	return runProtocolSmokeRequest(t, url, func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	})
}

func runProtocolSmokeRequest(t *testing.T, url string, checkRedirect func(*http.Request, []*http.Request) error) (int, http.Header) {
	t.Helper()

	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("create smoke request: %v", err)
	}
	request.Host = smokeHost
	request.Header.Set("X-Forwarded-Proto", "https")

	client := http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: checkRedirect,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		},
	}

	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("run smoke request: %v", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()

	return response.StatusCode, response.Header.Clone()
}
