package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

const (
	pathRuleTestMethodPost  = "POST"
	pathRuleTestAuthMatch   = "^/api/v1/auth/json$"
	pathRuleTestAuthHeader  = "Authorization"
	pathRuleTestPrimaryAuth = "Basic primary-token"
	pathRuleTestShadowAuth  = "Basic metrics-token"
	pathRuleTestRouteValue  = "metrics"
)

func TestLoadReadsConfigFile(t *testing.T) {
	loaded := loadTestConfig(t, []byte(`
protocol: http
primary_base_urls:
  - "https://primary-a.example.com"
  - "https://primary-b.example.com"
primary_selection_mode: "source_ip_hash"
shadow_base_urls:
  - "https://shadow-a.example.com"
  - "https://shadow-b.example.com"
shadow_selection_mode: "source_ip_hash"
shadow_sample_percent: 20
log_json: false
path_mapping:
  mode: rewrite
  rules:
    - match: "^/v1/(.*)$"
      primary: "/api/$1"
      shadow: "/shadow/$1"
run_as_user: " nobody "
run_as_group: " 33 "
chroot: " /var/empty "
observability:
  prometheus_enabled: false
  otel_sample_ratio: 0.0
  trace_id_header: "x-trace-id"
`))
	assertBackendConfig(t, loaded)
	assertRuntimeConfig(t, loaded)
	assertHTTPDefaults(t, loaded)
	assertObservabilityConfig(t, loaded)
}

func loadTestConfig(t *testing.T, configContent []byte) Config {
	t.Helper()

	tempDir := t.TempDir()

	configPath := filepath.Join(tempDir, "config.yaml")
	if err := os.WriteFile(configPath, configContent, 0o600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	t.Setenv("CONFIG_FILE", configPath)

	loaded, err := Load()
	if err != nil {
		t.Fatalf("expected config to load, got error: %v", err)
	}

	return loaded
}

func assertBackendConfig(t *testing.T, loaded Config) {
	t.Helper()

	if len(loaded.PrimaryBaseURLs) != 2 {
		t.Fatalf("expected 2 primary URLs, got %d", len(loaded.PrimaryBaseURLs))
	}

	if loaded.PrimarySelectionMode != selectionSourceIPHash {
		t.Fatalf("expected primary_selection_mode source_ip_hash, got %q", loaded.PrimarySelectionMode)
	}

	if len(loaded.ShadowBaseURLs) != 2 {
		t.Fatalf("expected 2 shadow URLs, got %d", len(loaded.ShadowBaseURLs))
	}

	if loaded.ShadowSelectionMode != selectionSourceIPHash {
		t.Fatalf("expected shadow_selection_mode source_ip_hash, got %q", loaded.ShadowSelectionMode)
	}

	if loaded.ShadowSamplePercent != 20 {
		t.Fatalf("expected shadow sample percent 20, got %d", loaded.ShadowSamplePercent)
	}

	if loaded.LogJSON {
		t.Fatalf("expected log_json to be false")
	}

	if loaded.PathMapping.Mode != "rewrite" {
		t.Fatalf("expected path mapping mode rewrite, got %q", loaded.PathMapping.Mode)
	}
}

func assertRuntimeConfig(t *testing.T, loaded Config) {
	t.Helper()

	if loaded.RunAsUser != "nobody" {
		t.Fatalf("expected run_as_user to be trimmed, got %q", loaded.RunAsUser)
	}

	if loaded.RunAsGroup != "33" {
		t.Fatalf("expected run_as_group to be trimmed, got %q", loaded.RunAsGroup)
	}

	if loaded.ChrootDir != "/var/empty" {
		t.Fatalf("expected chroot to be trimmed, got %q", loaded.ChrootDir)
	}
}

func assertHTTPDefaults(t *testing.T, loaded Config) {
	t.Helper()

	if loaded.UpstreamHTTPDialTimeout != 2*time.Second {
		t.Fatalf("expected upstream_http_dial_timeout default 2s, got %s", loaded.UpstreamHTTPDialTimeout)
	}

	if loaded.UpstreamHTTPTLSHandshakeTimeout != 5*time.Second {
		t.Fatalf("expected upstream_http_tls_handshake_timeout default 5s, got %s", loaded.UpstreamHTTPTLSHandshakeTimeout)
	}

	if loaded.UpstreamHTTPResponseHeaderTimeout != 5*time.Second {
		t.Fatalf("expected upstream_http_response_header_timeout default 5s, got %s", loaded.UpstreamHTTPResponseHeaderTimeout)
	}

	if loaded.UpstreamHTTPMaxIdleConns != 1024 {
		t.Fatalf("expected upstream_http_max_idle_conns default 1024, got %d", loaded.UpstreamHTTPMaxIdleConns)
	}

	if loaded.UpstreamHTTPMaxIdleConnsPerHost != 256 {
		t.Fatalf("expected upstream_http_max_idle_conns_per_host default 256, got %d", loaded.UpstreamHTTPMaxIdleConnsPerHost)
	}

	if loaded.UpstreamHTTPMaxConnsPerHost != 0 {
		t.Fatalf("expected upstream_http_max_conns_per_host default 0, got %d", loaded.UpstreamHTTPMaxConnsPerHost)
	}

	if loaded.UpstreamHTTPProtocol != upstreamHTTPProtocolAuto {
		t.Fatalf("expected upstream_http_protocol default auto, got %q", loaded.UpstreamHTTPProtocol)
	}

	for _, header := range []string{
		headerLocation,
		headerSetCookie,
		headerContentType,
		headerCacheControl,
		headerPragma,
		headerExpires,
		headerWWWAuthenticate,
		headerContentEncoding,
		headerVary,
	} {
		if !slices.Contains(loaded.ForwardResponseHeaders, header) {
			t.Fatalf("expected default forward_response_headers to include %s, got %#v", header, loaded.ForwardResponseHeaders)
		}
	}
}

func assertObservabilityConfig(t *testing.T, loaded Config) {
	t.Helper()

	if loaded.Observability.PrometheusEnabled {
		t.Fatalf("expected prometheus to be disabled")
	}

	if loaded.Observability.OTelSampleRatio == nil {
		t.Fatalf("expected otel_sample_ratio to be decoded")
	}

	if *loaded.Observability.OTelSampleRatio != 0.0 {
		t.Fatalf("expected otel_sample_ratio 0.0, got %f", *loaded.Observability.OTelSampleRatio)
	}

	if loaded.Observability.TraceIDHeader != "X-Trace-Id" {
		t.Fatalf("expected canonical trace_id_header, got %q", loaded.Observability.TraceIDHeader)
	}
}

func TestLoadRequiresPrimaryAndShadowBaseURLs(t *testing.T) {
	expectLoadFailure(t, []byte(`
protocol: http
primary_base_urls: []
shadow_base_urls: []
`), "expected config loading to fail for empty backend lists")
}

func TestLoadNormalizesUpstreamHTTPProtocol(t *testing.T) {
	loaded := loadTestConfig(t, []byte(`
protocol: http
upstream_http_protocol: " HTTP1 "
`))

	if loaded.UpstreamHTTPProtocol != upstreamHTTPProtocolHTTP1 {
		t.Fatalf("expected upstream_http_protocol http1, got %q", loaded.UpstreamHTTPProtocol)
	}
}

func TestLoadRejectsInvalidUpstreamHTTPProtocol(t *testing.T) {
	expectLoadFailure(t, []byte(`
protocol: http
upstream_http_protocol: spdy
`), "expected config loading to fail for invalid upstream_http_protocol")
}

func TestLoadRejectsIncompleteObservabilityConfig(t *testing.T) {
	expectLoadFailure(t, []byte(`
protocol: http
primary_base_urls:
  - "https://primary.example.com"
shadow_base_urls:
  - "https://shadow.example.com"
observability:
  otel_enabled: true
  otel_traces_enabled: true
`), "expected config loading to fail without otel_exporter_otlp_endpoint")
}

func TestLoadPathRules(t *testing.T) {
	loaded := loadTestConfig(t, []byte(`
protocol: http
path_rules:
  - name: " auth-json "
    methods: ["post"]
    match: "^/api/v1/auth/json$"
    shadow: auto
    compare: on
    compare_mode: json
    compare_headers:
      - auth-status
      - Auth-Error
    primary_request_headers:
      authorization: "Basic primary-token"
      X-Primary-Route: "metrics"
    shadow_request_headers:
      authorization: "Basic metrics-token"
      X-Shadow-Route: "metrics"
`))

	if len(loaded.PathRules) != 1 {
		t.Fatalf("expected 1 path rule, got %d", len(loaded.PathRules))
	}

	rule := loaded.PathRules[0]
	assertLoadedPathRuleBasics(t, rule)
	assertLoadedPathRuleCompareHeaders(t, rule)
	assertLoadedPathRuleRequestHeaders(t, rule)
}

func assertLoadedPathRuleBasics(t *testing.T, rule PathRule) {
	t.Helper()

	if rule.Name != "auth-json" {
		t.Fatalf("expected trimmed path rule name, got %q", rule.Name)
	}

	if len(rule.Methods) != 1 || rule.Methods[0] != pathRuleTestMethodPost {
		t.Fatalf("expected method %s, got %#v", pathRuleTestMethodPost, rule.Methods)
	}

	if rule.Match != pathRuleTestAuthMatch {
		t.Fatalf("expected match regex to load, got %q", rule.Match)
	}

	if rule.Shadow != pathRuleShadowAuto {
		t.Fatalf("expected shadow auto, got %q", rule.Shadow)
	}

	if rule.Compare != pathRuleCompareOn {
		t.Fatalf("expected compare on, got %q", rule.Compare)
	}

	if rule.CompareMode != compareModeJSON {
		t.Fatalf("expected compare mode json, got %q", rule.CompareMode)
	}
}

func assertLoadedPathRuleCompareHeaders(t *testing.T, rule PathRule) {
	t.Helper()

	expectedHeaders := []string{"Auth-Status", "Auth-Error"}
	if len(rule.CompareHeaders) != len(expectedHeaders) {
		t.Fatalf("expected compare headers %#v, got %#v", expectedHeaders, rule.CompareHeaders)
	}

	for i, expected := range expectedHeaders {
		if rule.CompareHeaders[i] != expected {
			t.Fatalf("expected compare header %d to be %q, got %q", i, expected, rule.CompareHeaders[i])
		}
	}
}

func assertLoadedPathRuleRequestHeaders(t *testing.T, rule PathRule) {
	t.Helper()

	if rule.PrimaryRequestHeaders[pathRuleTestAuthHeader] != pathRuleTestPrimaryAuth {
		t.Fatalf("expected canonical Authorization primary request header, got %#v", rule.PrimaryRequestHeaders)
	}

	if rule.PrimaryRequestHeaders["X-Primary-Route"] != pathRuleTestRouteValue {
		t.Fatalf("expected X-Primary-Route primary request header, got %#v", rule.PrimaryRequestHeaders)
	}

	if rule.ShadowRequestHeaders[pathRuleTestAuthHeader] != pathRuleTestShadowAuth {
		t.Fatalf("expected canonical Authorization shadow request header, got %#v", rule.ShadowRequestHeaders)
	}

	if rule.ShadowRequestHeaders["X-Shadow-Route"] != pathRuleTestRouteValue {
		t.Fatalf("expected X-Shadow-Route shadow request header, got %#v", rule.ShadowRequestHeaders)
	}
}

func TestLoadPathRulesRejectsInvalidRuleShape(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{
			name: "invalid regex",
			yaml: `
path_rules:
  - match: "["
`,
		},
		{
			name: "missing match",
			yaml: `
path_rules:
  - name: no-match
`,
		},
	}

	assertPathRuleLoadFailures(t, tests)
}

func TestLoadPathRulesRejectsInvalidRuleValues(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{
			name: "invalid shadow",
			yaml: `
path_rules:
  - match: "^/api$"
    shadow: off
`,
		},
		{
			name: "invalid compare",
			yaml: `
path_rules:
  - match: "^/api$"
    compare: yes
`,
		},
		{
			name: "invalid compare mode",
			yaml: `
path_rules:
  - match: "^/api$"
    compare_mode: xml
`,
		},
		{
			name: "invalid HTTP method",
			yaml: `
path_rules:
  - methods: ["GET /bad"]
    match: "^/api$"
`,
		},
	}

	assertPathRuleLoadFailures(t, tests)
}

func TestLoadPathRulesRejectsInvalidRuleHeaders(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{
			name: "invalid compare header",
			yaml: `
path_rules:
  - match: "^/api$"
    compare_headers: ["bad header"]
`,
		},
		{
			name: "invalid shadow request header",
			yaml: `
path_rules:
  - match: "^/api$"
    shadow_request_headers:
      "bad header": value
`,
		},
		{
			name: "invalid primary request header",
			yaml: `
path_rules:
  - match: "^/api$"
    primary_request_headers:
      "bad header": value
`,
		},
	}

	assertPathRuleLoadFailures(t, tests)
}

func assertPathRuleLoadFailures(t *testing.T, tests []struct {
	name string
	yaml string
}) {
	t.Helper()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expectLoadFailure(t, []byte("protocol: http\n"+tt.yaml), "expected invalid path rule to fail config loading")
		})
	}
}

func TestLoadPathRulesMethodsAreNormalized(t *testing.T) {
	loaded := loadTestConfig(t, []byte(`
protocol: http
path_rules:
  - methods: ["get", "Post", "PATCH"]
    match: "^/api$"
`))

	expected := []string{"GET", "POST", "PATCH"}
	if len(loaded.PathRules[0].Methods) != len(expected) {
		t.Fatalf("expected methods %#v, got %#v", expected, loaded.PathRules[0].Methods)
	}

	for i, method := range expected {
		if loaded.PathRules[0].Methods[i] != method {
			t.Fatalf("expected method %d to be %q, got %q", i, method, loaded.PathRules[0].Methods[i])
		}
	}
}

func TestLoadPathRulesDefaultModes(t *testing.T) {
	loaded := loadTestConfig(t, []byte(`
protocol: http
path_rules:
  - match: "^/api$"
`))

	rule := loaded.PathRules[0]
	if rule.Shadow != pathRuleShadowInherit {
		t.Fatalf("expected default shadow inherit, got %q", rule.Shadow)
	}

	if rule.Compare != pathRuleCompareInherit {
		t.Fatalf("expected default compare inherit, got %q", rule.Compare)
	}

	if rule.CompareMode != "" {
		t.Fatalf("expected empty compare_mode to preserve global fallback, got %q", rule.CompareMode)
	}
}

func TestLoadPathRulesCompareHeadersPresence(t *testing.T) {
	loaded := loadTestConfig(t, []byte(`
protocol: http
path_rules:
  - name: omitted
    match: "^/omitted$"
  - name: empty
    match: "^/empty$"
    compare_headers: []
`))

	if loaded.PathRules[0].CompareHeaders != nil {
		t.Fatalf("expected omitted compare_headers to stay nil, got %#v", loaded.PathRules[0].CompareHeaders)
	}

	if loaded.PathRules[1].CompareHeaders == nil {
		t.Fatalf("expected explicit empty compare_headers to be distinguishable from omitted")
	}

	if len(loaded.PathRules[1].CompareHeaders) != 0 {
		t.Fatalf("expected explicit empty compare_headers to have length 0, got %#v", loaded.PathRules[1].CompareHeaders)
	}
}

func TestLoadPathRulesEmptyOrAbsentPreservesDefaults(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{
			name: "absent",
			yaml: "protocol: http\n",
		},
		{
			name: "empty",
			yaml: `
protocol: http
path_rules: []
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loaded := loadTestConfig(t, []byte(tt.yaml))
			if len(loaded.PathRules) != 0 {
				t.Fatalf("expected no path rules, got %#v", loaded.PathRules)
			}

			if loaded.CompareMode != compareModeNginx {
				t.Fatalf("expected existing compare_mode default, got %q", loaded.CompareMode)
			}
		})
	}
}

func expectLoadFailure(t *testing.T, configContent []byte, message string) {
	t.Helper()

	tempDir := t.TempDir()

	configPath := filepath.Join(tempDir, "config.yaml")
	if err := os.WriteFile(configPath, configContent, 0o600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	t.Setenv("CONFIG_FILE", configPath)

	_, err := Load()
	if err == nil {
		t.Fatal(message)
	}
}
