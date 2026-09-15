package config

import "testing"

func TestGRPCCallerAuthFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		auth  GRPCCallerAuthConfig
		tls   GRPCTLSConfig
		valid bool
	}{
		{name: "missing"},
		{name: "legacy", auth: GRPCCallerAuthConfig{AllowUnauthenticated: true}, valid: true},
		{name: "conflicting", auth: GRPCCallerAuthConfig{AllowUnauthenticated: true, Mode: testCallerMTLSMode}},
		{name: "mtls-without-tls", auth: GRPCCallerAuthConfig{Mode: testCallerMTLSMode}},
		{name: testCallerMTLSMode, auth: GRPCCallerAuthConfig{Mode: testCallerMTLSMode}, tls: GRPCTLSConfig{Enabled: true, ClientCA: "ca.pem", RequireClientCert: true}, valid: true},
		{name: "unknown", auth: GRPCCallerAuthConfig{Mode: "typo"}},
		{name: "incomplete", auth: GRPCCallerAuthConfig{Mode: "introspection"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{GRPCBackendOIDCAuth: GRPCBackendOIDCAuthConfig{Enabled: true}, GRPCCallerAuth: tc.auth, GRPCTLS: tc.tls}
			if err := ValidateGRPCCallerAuth(cfg); (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

func TestLoadCallerMethodPreservesCase(t *testing.T) {
	cfg := loadTestConfig(t, []byte(`
protocol: grpc
shadow_sample_percent: 0
primary_grpc_targets:
  - address: localhost:9443
grpc_caller_auth:
  mode: introspection
  introspection_endpoint: https://issuer.example/introspect
  issuer: https://issuer.example
  audience: proxy
  client_id: proxy
  client_secret_env: TEST_SECRET
  method_scopes:
    - method: /example.Identity/LookupIdentity
      scopes: [identity:lookup]
`))
	if len(cfg.GRPCCallerAuth.MethodScopes) != 1 || cfg.GRPCCallerAuth.MethodScopes[0].Method != "/example.Identity/LookupIdentity" {
		t.Fatalf("method casing lost: %+v", cfg.GRPCCallerAuth.MethodScopes)
	}
}

const testCallerMTLSMode = "mtls"
