// Package config loads and validates Doppelgaenger configuration.
package config

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/http/httpguts"

	"github.com/mitchellh/mapstructure"
	"github.com/spf13/viper"

	"doppelgaenger/internal/mapping"
)

const (
	protocolHTTP   = "http"
	protocolMilter = "milter"

	compareModeNginx  = "nginx"
	compareModeHeader = "header"
	compareModeJSON   = "json"
	compareModeHTML   = "html"

	pathRuleShadowInherit = "inherit"
	pathRuleShadowAuto    = "auto"
	pathRuleShadowNever   = "never"
	pathRuleShadowAlways  = "always"

	pathRuleCompareInherit = "inherit"
	pathRuleCompareOn      = "on"
	pathRuleCompareOff     = "off"

	selectionRoundRobin   = "round_robin"
	selectionSourceIPHash = "source_ip_hash"

	upstreamHTTPProtocolAuto  = "auto"
	upstreamHTTPProtocolHTTP1 = "http1"
	upstreamHTTPProtocolHTTP2 = "http2"

	headerAuthStatus       = "Auth-Status"
	headerAuthServer       = "Auth-Server"
	headerAuthPort         = "Auth-Port"
	headerAuthUser         = "Auth-User"
	headerAuthError        = "Auth-Error"
	headerNauthilusSession = "X-Nauthilus-Session"
)

// Config holds all configuration settings for the proxy.
type Config struct {
	// Protocol selects the proxy protocol (http or milter).
	Protocol string `mapstructure:"protocol"`
	// PrimaryBaseURLs is the list of primary backends.
	// Applies to: HTTP protocol.
	PrimaryBaseURLs []*url.URL `mapstructure:"primary_base_urls"`

	// PrimarySelectionMode controls primary backend selection strategy.
	// Supported: round_robin, source_ip_hash.
	PrimarySelectionMode string `mapstructure:"primary_selection_mode"`

	// ShadowBaseURLs is the list of shadow backends.
	// Applies to: HTTP protocol.
	ShadowBaseURLs []*url.URL `mapstructure:"shadow_base_urls"`

	// ShadowSelectionMode controls shadow backend selection strategy.
	// Supported: round_robin, source_ip_hash.
	ShadowSelectionMode string `mapstructure:"shadow_selection_mode"`

	// ShadowRPS defines the maximum requests per second for the shadow backend (0 disables the limit).
	ShadowRPS float64 `mapstructure:"shadow_rps"`

	// MaxBackendBodyBytes limits the size of the request body sent to backends.
	// Applies to: HTTP protocol.
	MaxBackendBodyBytes int64 `mapstructure:"max_backend_body_bytes"`

	// UpstreamHTTPDialTimeout limits how long TCP connect attempts to HTTP backends may take.
	// Applies to: HTTP protocol.
	UpstreamHTTPDialTimeout time.Duration `mapstructure:"upstream_http_dial_timeout"`

	// UpstreamHTTPTLSHandshakeTimeout limits TLS handshake time to HTTPS backends.
	// Applies to: HTTP protocol.
	UpstreamHTTPTLSHandshakeTimeout time.Duration `mapstructure:"upstream_http_tls_handshake_timeout"`

	// UpstreamHTTPResponseHeaderTimeout limits time to receive upstream response headers.
	// Applies to: HTTP protocol.
	UpstreamHTTPResponseHeaderTimeout time.Duration `mapstructure:"upstream_http_response_header_timeout"`

	// UpstreamHTTPMaxIdleConns caps idle keep-alive connections across all upstream hosts.
	// Applies to: HTTP protocol.
	UpstreamHTTPMaxIdleConns int `mapstructure:"upstream_http_max_idle_conns"`

	// UpstreamHTTPMaxIdleConnsPerHost caps idle keep-alive connections per upstream host.
	// Applies to: HTTP protocol.
	UpstreamHTTPMaxIdleConnsPerHost int `mapstructure:"upstream_http_max_idle_conns_per_host"`

	// UpstreamHTTPMaxConnsPerHost caps total (active+idle+dialing) connections per upstream host.
	// Applies to: HTTP protocol. 0 means unlimited.
	UpstreamHTTPMaxConnsPerHost int `mapstructure:"upstream_http_max_conns_per_host"`

	// UpstreamHTTPProtocol controls upstream protocol negotiation: auto, http1, or http2.
	// Applies to: HTTP protocol.
	UpstreamHTTPProtocol string `mapstructure:"upstream_http_protocol"`

	// ListenAddr is the address the proxy listens on (e.g., ":8080").
	// Applies to: HTTP protocol.
	ListenAddr string `mapstructure:"listen_addr"`

	// MilterListenAddr is the TCP address for the Milter proxy listener.
	// Applies to: Milter protocol.
	MilterListenAddr string `mapstructure:"milter_listen_addr"`

	// PrimaryMilterAddr is the address of the primary Milter backend.
	// Applies to: Milter protocol.
	PrimaryMilterAddr string `mapstructure:"primary_milter_addr"`

	// ShadowMilterAddr is the address of the shadow Milter backend.
	// Applies to: Milter protocol.
	ShadowMilterAddr string `mapstructure:"shadow_milter_addr"`

	// MilterTimeout is the timeout for Milter upstream operations.
	// Applies to: Milter protocol.
	MilterTimeout time.Duration `mapstructure:"milter_timeout"`

	// TLSCertFile path to the TLS certificate file for the proxy server.
	TLSCertFile string `mapstructure:"tls_cert_file"`

	// TLSKeyFile path to the TLS key file for the proxy server.
	TLSKeyFile string `mapstructure:"tls_key_file"`

	// ShadowForceHeader is the header name that (if present) forces shadowing.
	// Applies to: HTTP protocol.
	ShadowForceHeader string `mapstructure:"shadow_force_header"`

	// PrimaryRequestHeaders are additional request headers added to primary backend calls.
	// Applies to: HTTP protocol.
	PrimaryRequestHeaders map[string]string `mapstructure:"primary_request_headers"`

	// ShadowRequestHeaders are additional request headers added to shadow backend calls.
	// Applies to: HTTP protocol.
	ShadowRequestHeaders map[string]string `mapstructure:"shadow_request_headers"`

	// RootCAPath is the path to a common CA certificate for all upstream backends.
	RootCAPath string `mapstructure:"root_ca"`

	// PrimaryRootCA path to the CA certificate specifically for the primary backend.
	PrimaryRootCA string `mapstructure:"primary_root_ca"`

	// ShadowRootCA path to the CA certificate specifically for the shadow backend.
	ShadowRootCA string `mapstructure:"shadow_root_ca"`

	// ShadowSamplePercent percentage of traffic mirrored to the shadow backend (0-100).
	ShadowSamplePercent int `mapstructure:"shadow_sample_percent"`

	// ShadowBurst maximum number of tokens in the token bucket (burst capacity).
	ShadowBurst int `mapstructure:"shadow_burst"`

	// ShadowTimeout time limit for requests to the shadow backend.
	// Applies to: HTTP protocol.
	ShadowTimeout time.Duration `mapstructure:"shadow_timeout"`

	// ForwardResponseHeaders list of headers passed from the primary backend to the client.
	// Applies to: HTTP protocol.
	ForwardResponseHeaders []string `mapstructure:"forward_response_headers"`

	// CompareHeaders list of headers compared between primary and shadow backends.
	CompareHeaders []string `mapstructure:"compare_headers"`

	// CompareMode selects the comparison mode (nginx, header, json, html).
	// Applies to: HTTP protocol.
	CompareMode string `mapstructure:"compare_mode"`

	// PathRules define ordered HTTP path-specific shadow and comparison policy.
	// Applies to: HTTP protocol.
	PathRules []PathRule `mapstructure:"path_rules"`

	// JSONStrict controls strict JSON comparison behavior.
	// Applies to: HTTP protocol.
	JSONStrict bool `mapstructure:"compare_json_strict"`

	// HTMLSimilarityThreshold defines the required similarity for HTML comparison (0.0-1.0).
	// Applies to: HTTP protocol.
	HTMLSimilarityThreshold float64 `mapstructure:"compare_html_threshold"`

	// LogSessionOnlyOnDiff controls whether session headers are logged only when there are differences.
	LogSessionOnlyOnDiff bool `mapstructure:"log_session_only_on_diff"`

	// LogJSON controls whether the logger should output JSON.
	LogJSON bool `mapstructure:"log_json"`

	// InsecureUpstream allows insecure TLS connections (no verification) to the backends.
	InsecureUpstream bool `mapstructure:"insecure_upstream"`

	// RunAsUser switches the process user after startup initialization.
	RunAsUser string `mapstructure:"run_as_user"`

	// RunAsGroup switches the process primary group after startup initialization.
	RunAsGroup string `mapstructure:"run_as_group"`

	// ChrootDir changes the process root directory before dropping privileges.
	ChrootDir string `mapstructure:"chroot"`

	// PathMapping controls how incoming request paths are mapped to backends.
	// Applies to: HTTP protocol.
	PathMapping mapping.Config `mapstructure:"path_mapping"`

	// Observability controls optional OpenMetrics and OpenTelemetry exporters.
	Observability ObservabilityConfig `mapstructure:"observability"`
}

// PathRule describes a path-specific HTTP shadow and comparison rule.
type PathRule struct {
	Name           string   `mapstructure:"name"`
	Methods        []string `mapstructure:"methods"`
	Match          string   `mapstructure:"match"`
	Shadow         string   `mapstructure:"shadow"`
	Compare        string   `mapstructure:"compare"`
	CompareMode    string   `mapstructure:"compare_mode"`
	CompareHeaders []string `mapstructure:"compare_headers"`
}

// ObservabilityConfig contains all opt-in metrics and tracing settings.
type ObservabilityConfig struct {
	PrometheusEnabled        bool              `mapstructure:"prometheus_enabled"`
	PrometheusAddress        string            `mapstructure:"prometheus_address"`
	PrometheusPort           int               `mapstructure:"prometheus_port"`
	PrometheusPath           string            `mapstructure:"prometheus_path"`
	PrometheusRuntimeMetrics bool              `mapstructure:"prometheus_runtime_metrics"`
	PrometheusHTTPAuthBasic  string            `mapstructure:"prometheus_http_auth_basic"`
	PrometheusTLS            PrometheusTLS     `mapstructure:"prometheus_tls"`
	OTelEnabled              bool              `mapstructure:"otel_enabled"`
	OTelTracesEnabled        bool              `mapstructure:"otel_traces_enabled"`
	OTelMetricsEnabled       bool              `mapstructure:"otel_metrics_enabled"`
	OTelServiceName          string            `mapstructure:"otel_service_name"`
	OTelServiceVersion       string            `mapstructure:"otel_service_version"`
	OTLPEndpoint             string            `mapstructure:"otel_exporter_otlp_endpoint"`
	OTLPHeaders              map[string]string `mapstructure:"otel_exporter_otlp_headers"`
	OTLPInsecure             bool              `mapstructure:"otel_exporter_otlp_insecure"`
	OTelSampleRatio          *float64          `mapstructure:"otel_sample_ratio"`
	TraceIDHeader            string            `mapstructure:"trace_id_header"`
}

// PrometheusTLS configures server-side TLS for the optional Prometheus endpoint.
type PrometheusTLS struct {
	Enabled    bool   `mapstructure:"enabled"`
	Cert       string `mapstructure:"cert"`
	Key        string `mapstructure:"key"`
	MinVersion string `mapstructure:"min_tls_version"`
}

// UseJSONLogger reports whether the JSON logger is enabled.
func (c Config) UseJSONLogger() bool {
	return c.LogJSON
}

// Load loads the configuration from a YAML file using Viper.
func Load() (Config, error) {
	v := viper.New()

	configFile := strings.TrimSpace(os.Getenv("CONFIG_FILE"))
	if configFile != "" {
		v.SetConfigFile(configFile)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
		v.AddConfigPath("/etc/doppelgaenger")
	}

	setDefaults(v)

	if err := v.ReadInConfig(); err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	var cfg Config

	decodeHook := mapstructure.ComposeDecodeHookFunc(
		mapstructure.StringToTimeDurationHookFunc(),
		decodeURLHook(),
	)
	if err := v.Unmarshal(&cfg, viper.DecodeHook(decodeHook)); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}

	if err := validate(&cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func setDefaults(v *viper.Viper) {
	setProtocolDefaults(v)
	setHTTPDefaults(v)
	setMilterDefaults(v)
	setCompareDefaults(v)
	setRuntimeDefaults(v)
	setHeaderDefaults(v)
	setPathMappingDefaults(v)
	setObservabilityDefaults(v)
}

func setProtocolDefaults(v *viper.Viper) {
	v.SetDefault("protocol", protocolHTTP)
}

func setHTTPDefaults(v *viper.Viper) {
	v.SetDefault("listen_addr", ":8080")
	v.SetDefault("tls_cert_file", "")
	v.SetDefault("tls_key_file", "")
	v.SetDefault("primary_base_urls", []string{"https://127.0.0.1:9001"})
	v.SetDefault("primary_selection_mode", selectionRoundRobin)
	v.SetDefault("shadow_base_urls", []string{"https://127.0.0.1:9002"})
	v.SetDefault("shadow_selection_mode", selectionRoundRobin)
	v.SetDefault("shadow_timeout", 150*time.Millisecond)
	v.SetDefault("shadow_sample_percent", 5)
	v.SetDefault("shadow_force_header", "X-Shadow")
	v.SetDefault("primary_request_headers", map[string]string{})
	v.SetDefault("shadow_request_headers", map[string]string{})
	v.SetDefault("shadow_rps", 200.0)
	v.SetDefault("shadow_burst", 400)
	v.SetDefault("max_backend_body_bytes", 32*1024)
	v.SetDefault("upstream_http_dial_timeout", 2*time.Second)
	v.SetDefault("upstream_http_tls_handshake_timeout", 5*time.Second)
	v.SetDefault("upstream_http_response_header_timeout", 5*time.Second)
	v.SetDefault("upstream_http_max_idle_conns", 1024)
	v.SetDefault("upstream_http_max_idle_conns_per_host", 256)
	v.SetDefault("upstream_http_max_conns_per_host", 0)
	v.SetDefault("upstream_http_protocol", upstreamHTTPProtocolAuto)
	v.SetDefault("path_rules", []PathRule{})
}

func setMilterDefaults(v *viper.Viper) {
	v.SetDefault("milter_listen_addr", ":9999")
	v.SetDefault("primary_milter_addr", "127.0.0.1:9997")
	v.SetDefault("shadow_milter_addr", "127.0.0.1:9998")
	v.SetDefault("milter_timeout", 2*time.Second)
}

func setCompareDefaults(v *viper.Viper) {
	v.SetDefault("compare_mode", compareModeNginx)
	v.SetDefault("compare_json_strict", false)
	v.SetDefault("compare_html_threshold", 0.99)
	v.SetDefault("log_session_only_on_diff", true)
}

func setRuntimeDefaults(v *viper.Viper) {
	v.SetDefault("log_json", true)
	v.SetDefault("root_ca", "")
	v.SetDefault("primary_root_ca", "")
	v.SetDefault("shadow_root_ca", "")
	v.SetDefault("insecure_upstream", false)
	v.SetDefault("run_as_user", "")
	v.SetDefault("run_as_group", "")
	v.SetDefault("chroot", "")
}

func setHeaderDefaults(v *viper.Viper) {
	v.SetDefault("forward_response_headers", []string{
		headerAuthStatus,
		headerAuthServer,
		headerAuthPort,
		headerAuthUser,
		"Auth-Pass",
		headerAuthError,
		"Auth-Wait",
		"Auth-Protocol",
		headerNauthilusSession,
		"Location",
		"Set-Cookie",
	})
	v.SetDefault("compare_headers", []string{
		headerAuthStatus,
		headerAuthServer,
		headerAuthPort,
		headerAuthUser,
		headerAuthError,
		headerNauthilusSession,
	})
}

func setPathMappingDefaults(v *viper.Viper) {
	v.SetDefault("path_mapping", mapping.Config{
		Mode:  "direct",
		Rules: []mapping.Rule{},
	})
}

func setObservabilityDefaults(v *viper.Viper) {
	v.SetDefault("observability.prometheus_enabled", false)
	v.SetDefault("observability.prometheus_address", "127.0.0.1")
	v.SetDefault("observability.prometheus_port", 9464)
	v.SetDefault("observability.prometheus_path", "/metrics")
	v.SetDefault("observability.prometheus_runtime_metrics", false)
	v.SetDefault("observability.prometheus_http_auth_basic", "")
	v.SetDefault("observability.prometheus_tls.enabled", false)
	v.SetDefault("observability.prometheus_tls.cert", "")
	v.SetDefault("observability.prometheus_tls.key", "")
	v.SetDefault("observability.prometheus_tls.min_tls_version", "1.2")
	v.SetDefault("observability.otel_enabled", false)
	v.SetDefault("observability.otel_traces_enabled", false)
	v.SetDefault("observability.otel_metrics_enabled", false)
	v.SetDefault("observability.otel_service_name", "doppelgaenger")
	v.SetDefault("observability.otel_service_version", "")
	v.SetDefault("observability.otel_exporter_otlp_endpoint", "")
	v.SetDefault("observability.otel_exporter_otlp_headers", map[string]string{})
	v.SetDefault("observability.otel_exporter_otlp_insecure", false)
	v.SetDefault("observability.otel_sample_ratio", 1.0)
	v.SetDefault("observability.trace_id_header", "X-Trace-ID")
}

func validate(cfg *Config) error {
	if err := normalizeProtocol(cfg); err != nil {
		return err
	}

	if err := validateBackendLists(*cfg); err != nil {
		return err
	}

	cfg.PrimarySelectionMode = normalizeSelectionMode(cfg.PrimarySelectionMode)
	cfg.ShadowSelectionMode = normalizeSelectionMode(cfg.ShadowSelectionMode)

	normalizeShadowConfig(cfg)

	if err := normalizeHTTPTransportConfig(cfg); err != nil {
		return err
	}

	normalizeCompareConfig(cfg)
	normalizeRuntimeConfig(cfg)

	if err := validatePathRules(cfg); err != nil {
		return err
	}

	return validateObservabilityConfig(&cfg.Observability)
}

func normalizeProtocol(cfg *Config) error {
	protocol := strings.ToLower(strings.TrimSpace(cfg.Protocol))
	if protocol == "" {
		protocol = protocolHTTP
	}

	if protocol != protocolHTTP && protocol != protocolMilter {
		return fmt.Errorf("invalid protocol: %s", cfg.Protocol)
	}

	cfg.Protocol = protocol

	return nil
}

func validateBackendLists(cfg Config) error {
	if cfg.Protocol != protocolHTTP {
		return nil
	}

	if len(cfg.PrimaryBaseURLs) == 0 {
		return errors.New("at least one primary backend must be configured via primary_base_urls")
	}

	if len(cfg.ShadowBaseURLs) == 0 {
		return errors.New("at least one shadow backend must be configured via shadow_base_urls")
	}

	return nil
}

func normalizeShadowConfig(cfg *Config) {
	if cfg.ShadowSamplePercent < 0 {
		cfg.ShadowSamplePercent = 0
	}

	if cfg.ShadowSamplePercent > 100 {
		cfg.ShadowSamplePercent = 100
	}

	if cfg.ShadowBurst < 1 && cfg.ShadowRPS > 0 {
		cfg.ShadowBurst = 1
	}
}

func normalizeHTTPTransportConfig(cfg *Config) error {
	if cfg.UpstreamHTTPDialTimeout <= 0 {
		cfg.UpstreamHTTPDialTimeout = 2 * time.Second
	}

	if cfg.UpstreamHTTPTLSHandshakeTimeout <= 0 {
		cfg.UpstreamHTTPTLSHandshakeTimeout = 5 * time.Second
	}

	if cfg.UpstreamHTTPResponseHeaderTimeout <= 0 {
		cfg.UpstreamHTTPResponseHeaderTimeout = 5 * time.Second
	}

	if cfg.UpstreamHTTPMaxIdleConns <= 0 {
		cfg.UpstreamHTTPMaxIdleConns = 1024
	}

	if cfg.UpstreamHTTPMaxIdleConnsPerHost <= 0 {
		cfg.UpstreamHTTPMaxIdleConnsPerHost = 256
	}

	if cfg.UpstreamHTTPMaxConnsPerHost < 0 {
		cfg.UpstreamHTTPMaxConnsPerHost = 0
	}

	protocol := strings.ToLower(strings.TrimSpace(cfg.UpstreamHTTPProtocol))
	if protocol == "" {
		protocol = upstreamHTTPProtocolAuto
	}

	switch protocol {
	case upstreamHTTPProtocolAuto, upstreamHTTPProtocolHTTP1, upstreamHTTPProtocolHTTP2:
		cfg.UpstreamHTTPProtocol = protocol
	default:
		return fmt.Errorf("invalid upstream_http_protocol: %s", cfg.UpstreamHTTPProtocol)
	}

	return nil
}

func normalizeCompareConfig(cfg *Config) {
	mode := strings.ToLower(strings.TrimSpace(cfg.CompareMode))
	switch mode {
	case "", compareModeNginx, compareModeHeader, compareModeJSON, compareModeHTML:
		if mode == "" {
			mode = compareModeNginx
		}
	case "nxinx":
		mode = compareModeNginx
	default:
		mode = compareModeNginx
	}

	cfg.CompareMode = mode

	if cfg.HTMLSimilarityThreshold < 0 {
		cfg.HTMLSimilarityThreshold = 0
	}

	if cfg.HTMLSimilarityThreshold > 1 {
		cfg.HTMLSimilarityThreshold = 1
	}
}

func normalizeRuntimeConfig(cfg *Config) {
	cfg.RunAsUser = strings.TrimSpace(cfg.RunAsUser)
	cfg.RunAsGroup = strings.TrimSpace(cfg.RunAsGroup)
	cfg.ChrootDir = strings.TrimSpace(cfg.ChrootDir)
}

func validatePathRules(cfg *Config) error {
	if cfg.PathRules == nil {
		cfg.PathRules = []PathRule{}

		return nil
	}

	for i := range cfg.PathRules {
		rule := &cfg.PathRules[i]

		rule.Name = strings.TrimSpace(rule.Name)
		rule.Match = strings.TrimSpace(rule.Match)

		if rule.Match == "" {
			return fmt.Errorf("path_rules[%d].match is required", i)
		}

		if _, err := regexp.Compile(rule.Match); err != nil {
			return fmt.Errorf("path_rules[%d].match is invalid: %w", i, err)
		}

		methods, err := normalizePathRuleMethods(rule.Methods)
		if err != nil {
			return fmt.Errorf("path_rules[%d].methods: %w", i, err)
		}

		shadow, err := normalizePathRuleShadow(rule.Shadow)
		if err != nil {
			return fmt.Errorf("path_rules[%d].shadow: %w", i, err)
		}

		compare, err := normalizePathRuleCompare(rule.Compare)
		if err != nil {
			return fmt.Errorf("path_rules[%d].compare: %w", i, err)
		}

		compareMode, err := normalizePathRuleCompareMode(rule.CompareMode)
		if err != nil {
			return fmt.Errorf("path_rules[%d].compare_mode: %w", i, err)
		}

		compareHeaders, err := normalizeHTTPHeaderNames(rule.CompareHeaders)
		if err != nil {
			return fmt.Errorf("path_rules[%d].compare_headers: %w", i, err)
		}

		rule.Methods = methods
		rule.Shadow = shadow
		rule.Compare = compare
		rule.CompareMode = compareMode
		rule.CompareHeaders = compareHeaders
	}

	return nil
}

func normalizePathRuleMethods(methods []string) ([]string, error) {
	if methods == nil {
		return nil, nil
	}

	normalized := make([]string, 0, len(methods))
	for _, raw := range methods {
		method := strings.ToUpper(strings.TrimSpace(raw))
		if method == "" || !httpguts.ValidHeaderFieldName(method) {
			return nil, fmt.Errorf("invalid HTTP method %q", raw)
		}

		normalized = append(normalized, method)
	}

	return normalized, nil
}

func normalizePathRuleShadow(raw string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(raw))
	switch mode {
	case "":
		return pathRuleShadowInherit, nil
	case pathRuleShadowInherit, pathRuleShadowAuto, pathRuleShadowNever, pathRuleShadowAlways:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported value %q (allowed: inherit, auto, never, always)", raw)
	}
}

func normalizePathRuleCompare(raw string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(raw))
	switch mode {
	case "":
		return pathRuleCompareInherit, nil
	case pathRuleCompareInherit, pathRuleCompareOn, pathRuleCompareOff:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported value %q (allowed: inherit, on, off)", raw)
	}
}

func normalizePathRuleCompareMode(raw string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(raw))
	switch mode {
	case "", compareModeNginx, compareModeHeader, compareModeJSON, compareModeHTML:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported value %q (allowed: nginx, header, json, html)", raw)
	}
}

func normalizeHTTPHeaderNames(names []string) ([]string, error) {
	if names == nil {
		return nil, nil
	}

	normalized := make([]string, 0, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		canonical := http.CanonicalHeaderKey(name)

		if canonical == "" || !httpguts.ValidHeaderFieldName(canonical) {
			return nil, fmt.Errorf("invalid HTTP header name %q", raw)
		}

		normalized = append(normalized, canonical)
	}

	return normalized, nil
}

func validateObservabilityConfig(cfg *ObservabilityConfig) error {
	normalizeObservabilityConfig(cfg)

	if err := validatePrometheusConfig(*cfg); err != nil {
		return err
	}

	if err := validateOTelConfig(*cfg); err != nil {
		return err
	}

	if err := validateTraceIDHeader(cfg); err != nil {
		return err
	}

	if cfg.OTLPHeaders == nil {
		cfg.OTLPHeaders = map[string]string{}
	}

	return nil
}

func normalizeObservabilityConfig(cfg *ObservabilityConfig) {
	cfg.PrometheusAddress = strings.TrimSpace(cfg.PrometheusAddress)
	cfg.PrometheusPath = strings.TrimSpace(cfg.PrometheusPath)
	cfg.PrometheusHTTPAuthBasic = strings.TrimSpace(cfg.PrometheusHTTPAuthBasic)
	cfg.PrometheusTLS.Cert = strings.TrimSpace(cfg.PrometheusTLS.Cert)
	cfg.PrometheusTLS.Key = strings.TrimSpace(cfg.PrometheusTLS.Key)
	cfg.PrometheusTLS.MinVersion = strings.TrimSpace(cfg.PrometheusTLS.MinVersion)
	cfg.OTelServiceName = strings.TrimSpace(cfg.OTelServiceName)
	cfg.OTelServiceVersion = strings.TrimSpace(cfg.OTelServiceVersion)
	cfg.OTLPEndpoint = strings.TrimSpace(cfg.OTLPEndpoint)
	cfg.TraceIDHeader = strings.TrimSpace(cfg.TraceIDHeader)
}

func validatePrometheusConfig(cfg ObservabilityConfig) error {
	if !cfg.PrometheusEnabled {
		return nil
	}

	if cfg.PrometheusAddress == "" {
		return errors.New("observability prometheus_address must not be empty when prometheus_enabled is true")
	}

	if cfg.PrometheusPort < 1 || cfg.PrometheusPort > 65535 {
		return errors.New("observability prometheus_port must be between 1 and 65535")
	}

	if !strings.HasPrefix(cfg.PrometheusPath, "/") {
		return errors.New("observability prometheus_path must start with '/'")
	}

	if cfg.PrometheusHTTPAuthBasic != "" {
		if _, _, err := SplitBasicAuthCredentials(cfg.PrometheusHTTPAuthBasic); err != nil {
			return fmt.Errorf("observability prometheus_http_auth_basic %w", err)
		}
	}

	return validatePrometheusTLS(cfg.PrometheusTLS)
}

func validatePrometheusTLS(cfg PrometheusTLS) error {
	if !cfg.Enabled {
		return nil
	}

	if cfg.Cert == "" || cfg.Key == "" {
		return errors.New("observability prometheus_tls requires cert and key when enabled")
	}

	if _, err := ResolveTLSMinVersion(cfg.MinVersion); err != nil {
		return fmt.Errorf("observability prometheus_tls: %w", err)
	}

	return nil
}

func validateOTelConfig(cfg ObservabilityConfig) error {
	if ratio := defaultedOTelSampleRatio(cfg); ratio < 0 || ratio > 1 {
		return errors.New("observability otel_sample_ratio must be between 0.0 and 1.0")
	}

	if !cfg.OTelEnabled {
		return nil
	}

	if !cfg.OTelTracesEnabled && !cfg.OTelMetricsEnabled {
		return errors.New("observability otel_enabled requires otel_traces_enabled or otel_metrics_enabled")
	}

	if cfg.OTLPEndpoint == "" {
		return errors.New("observability otel_exporter_otlp_endpoint is required when otel_enabled is true")
	}

	return nil
}

func validateTraceIDHeader(cfg *ObservabilityConfig) error {
	if cfg.TraceIDHeader == "" {
		return nil
	}

	canonical := http.CanonicalHeaderKey(cfg.TraceIDHeader)
	if canonical == "" || !httpguts.ValidHeaderFieldName(canonical) {
		return errors.New("observability trace_id_header must be a valid HTTP header name")
	}

	cfg.TraceIDHeader = canonical

	return nil
}

func defaultedOTelSampleRatio(cfg ObservabilityConfig) float64 {
	if cfg.OTelSampleRatio == nil {
		return 1.0
	}

	return *cfg.OTelSampleRatio
}

// SplitBasicAuthCredentials validates and separates the user:password form used for Basic auth.
func SplitBasicAuthCredentials(credentials string) (string, string, error) {
	username, password, ok := strings.Cut(credentials, ":")
	if !ok || username == "" || password == "" {
		return "", "", errors.New("must use non-empty user:password credentials")
	}

	return username, password, nil
}

// ResolveTLSMinVersion converts a config value into a crypto/tls version constant.
func ResolveTLSMinVersion(value string) (uint16, error) {
	switch value {
	case "", "1.2":
		return tls.VersionTLS12, nil
	case "1.3":
		return tls.VersionTLS13, nil
	default:
		return 0, fmt.Errorf("unsupported min_tls_version %q (allowed: 1.2, 1.3)", value)
	}
}

// parseURL parses a string as a URL and ensures that scheme and host are present.
func parseURL(s string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return nil, err
	}

	if u.Scheme == "" || u.Host == "" {
		return nil, errors.New("base url must include scheme and host, e.g. https://127.0.0.1:9001")
	}

	return u, nil
}

func decodeURLHook() mapstructure.DecodeHookFuncType {
	return func(from reflect.Type, to reflect.Type, data interface{}) (interface{}, error) {
		if from.Kind() != reflect.String {
			return data, nil
		}

		if to != reflect.TypeOf(&url.URL{}) {
			return data, nil
		}

		return parseURL(data.(string))
	}
}

func normalizeSelectionMode(mode string) string {
	normalized := strings.ToLower(strings.TrimSpace(mode))
	switch normalized {
	case "", selectionRoundRobin, selectionSourceIPHash:
		if normalized == "" {
			return selectionRoundRobin
		}

		return normalized
	default:
		return selectionRoundRobin
	}
}
