package config

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthTLSDefaultsAndInvalidBundle(t *testing.T) {
	cfg, err := AuthClientTLSConfig("", "", "")
	if err != nil {
		t.Fatal(err)
	}

	if cfg.RootCAs != nil || cfg.MinVersion != tls.VersionTLS12 || cfg.InsecureSkipVerify {
		t.Fatal("expected verified TLS 1.2 with system roots")
	}

	path := filepath.Join(t.TempDir(), "invalid.pem")
	if err := os.WriteFile(path, []byte("not a certificate"), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := AuthClientTLSConfig(path, "", ""); err == nil {
		t.Fatal("accepted invalid CA bundle")
	}
}

func TestLoadAuthTLSOptions(t *testing.T) {
	cfg := loadTestConfig(t, []byte(authTLSConfigYAML))

	caller, backend := cfg.GRPCCallerAuth, cfg.GRPCBackendOIDCAuth
	if caller.ServerName != "issuer.example.test" || caller.MinTLSVersion != "1.3" || caller.CAFile != "" {
		t.Fatalf("caller TLS options lost: %+v", caller)
	}

	if backend.ServerName != caller.ServerName || backend.MinTLSVersion != caller.MinTLSVersion || backend.CAFile != "" {
		t.Fatal("backend TLS options lost")
	}

	for _, extra := range []string{"  min_tls_version: '1.1'", "  ca_file: /does-not-exist/ca.pem", "  insecure_tls: true"} {
		base := strings.ReplaceAll(authTLSConfigYAML, "  min_tls_version: \"1.3\"\n", "")
		base = strings.ReplaceAll(base, "  ca_file: \"\"\n", "")
		expectLoadFailure(t, []byte(base+"\n"+extra), "invalid backend TLS configuration accepted")
	}
}

const authTLSConfigYAML = `
protocol: grpc
shadow_sample_percent: 0
primary_grpc_targets:
  - address: localhost:9443
grpc_caller_auth:
  mode: introspection
  introspection_endpoint: https://127.0.0.1:9443/introspect
  issuer: https://issuer.example.test
  audience: proxy
  client_id: proxy
  client_secret_env: TEST_SECRET
  ca_file: ""
  server_name: issuer.example.test
  min_tls_version: "1.3"
grpc_backend_oidc_auth:
  enabled: true
  token_endpoint: https://127.0.0.1:9443/token
  client_id: proxy
  client_secret_env: TEST_SECRET
  ca_file: ""
  server_name: issuer.example.test
  min_tls_version: "1.3"
`

func TestLoadCallerRejectsInvalidTLSOptions(t *testing.T) {
	for _, replacement := range []struct{ old, value string }{
		{`  ca_file: ""`, `  ca_file: /does-not-exist/caller-ca.pem`},
		{`  min_tls_version: "1.3"`, `  min_tls_version: "1.1"`},
	} {
		content := strings.Replace(authTLSConfigYAML, replacement.old, replacement.value, 1)
		expectLoadFailure(t, []byte(content), "invalid caller TLS configuration accepted")
	}
}
