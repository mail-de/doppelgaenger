package config

import (
	"testing"
	"time"
)

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

func TestLoadCallerIntrospectionCacheDefaults(t *testing.T) {
	cfg := loadTestConfig(t, []byte(testCallerIntrospectionYAML))
	if cfg.GRPCCallerAuth.Timeout != 2*time.Second {
		t.Fatalf("unexpected timeout default: %s", cfg.GRPCCallerAuth.Timeout)
	}

	if cfg.GRPCCallerAuth.IntrospectionMaxConcurrent != DefaultGRPCIntrospectionMaxConcurrent {
		t.Fatalf("unexpected concurrency default: %d", cfg.GRPCCallerAuth.IntrospectionMaxConcurrent)
	}

	if cfg.GRPCCallerAuth.IntrospectionCacheTTL != 30*time.Second || cfg.GRPCCallerAuth.IntrospectionCacheMaxEntries != 10000 {
		t.Fatalf("unexpected cache defaults: ttl=%s max=%d", cfg.GRPCCallerAuth.IntrospectionCacheTTL, cfg.GRPCCallerAuth.IntrospectionCacheMaxEntries)
	}

	cfg = loadTestConfig(t, []byte(testCallerIntrospectionYAML+"  introspection_cache_ttl: 0s\n"))
	if cfg.GRPCCallerAuth.IntrospectionCacheTTL != 0 {
		t.Fatalf("cache could not be disabled: %s", cfg.GRPCCallerAuth.IntrospectionCacheTTL)
	}
}

func TestGRPCCallerIntrospectionCacheValidation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		ttl        time.Duration
		maxEntries int
		valid      bool
	}{
		{"enabled", time.Minute, 1, true},
		{"disabled", 0, 0, true},
		{"negative-ttl", -time.Second, 1, false},
		{"negative-max", time.Minute, -1, false},
		{"enabled-without-capacity", time.Minute, 0, false},
		{"above-cap", time.Minute, MaxGRPCIntrospectionCacheEntries + 1, false},
		{"at-cap", time.Minute, MaxGRPCIntrospectionCacheEntries, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := testIntrospectionAuth()
			auth.IntrospectionCacheTTL, auth.IntrospectionCacheMaxEntries = tc.ttl, tc.maxEntries

			if err := ValidateGRPCCallerAuth(Config{GRPCCallerAuth: auth}); (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

const testCallerIntrospectionYAML = `
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
`

const testCallerMTLSMode = "mtls"

const testCallerClient = "proxy"

func TestGRPCCallerIntrospectionConcurrency(t *testing.T) {
	a := testIntrospectionAuth()
	if a.EffectiveIntrospectionMaxConcurrent() != DefaultGRPCIntrospectionMaxConcurrent {
		t.Fatal("zero limit does not use the default")
	}

	a.IntrospectionMaxConcurrent = -1
	if ValidateGRPCCallerAuth(Config{GRPCCallerAuth: a}) == nil {
		t.Fatal("negative limit accepted")
	}

	a.IntrospectionMaxConcurrent = 3
	if ValidateGRPCCallerAuth(Config{GRPCCallerAuth: a}) != nil || a.EffectiveIntrospectionMaxConcurrent() != 3 {
		t.Fatal("explicit limit rejected")
	}
}

func testIntrospectionAuth() GRPCCallerAuthConfig {
	return GRPCCallerAuthConfig{
		Mode: grpcCallerIntrospectionMode, IntrospectionEndpoint: "https://issuer.example/introspect", Issuer: "https://issuer.example",
		Audience: testCallerClient, ClientID: testCallerClient, ClientSecretEnv: "TEST_SECRET",
	}
}
