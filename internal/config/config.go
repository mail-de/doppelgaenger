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

	"doppelgaenger/internal/logging"
	"doppelgaenger/internal/mapping"
)

const (
	protocolHTTP   = "http"
	protocolMilter = "milter"
	protocolGRPC   = "grpc"
	grpcHTTPScheme = "https"

	compareModeHeader = "header"
	compareModeJSON   = "json"
	compareModeHTML   = "html"

	grpcCompareModeStatus               = "status"
	grpcCompareModeStatusMetadata       = "status_metadata"
	grpcCompareModeMessageCount         = "message_count"
	grpcCompareModeMessageHash          = "message_hash"
	grpcMetadataStatus                  = "grpc-status"
	grpcMetadataMessage                 = "grpc-message"
	grpcOIDCAuthMethodAuto              = "auto"
	grpcOIDCAuthMethodClientSecretPost  = "client_secret_post"
	grpcOIDCAuthMethodClientSecretBasic = "client_secret_basic"
	tlsMinVersionDefault                = "1.2"

	pathRuleShadowInherit = "inherit"
	pathRuleShadowAuto    = "auto"
	pathRuleShadowNever   = "never"
	pathRuleShadowAlways  = "always"

	pathRuleCompareInherit = "inherit"
	pathRuleCompareOn      = "on"
	pathRuleCompareOff     = "off"

	grpcRuleShadowInherit = pathRuleShadowInherit
	grpcRuleShadowAuto    = pathRuleShadowAuto
	grpcRuleShadowNever   = pathRuleShadowNever
	grpcRuleShadowAlways  = pathRuleShadowAlways

	grpcRuleCompareInherit = pathRuleCompareInherit
	grpcRuleCompareOn      = pathRuleCompareOn
	grpcRuleCompareOff     = pathRuleCompareOff

	selectionRoundRobin   = "round_robin"
	selectionSourceIPHash = "source_ip_hash"

	upstreamHTTPProtocolAuto  = "auto"
	upstreamHTTPProtocolHTTP1 = "http1"
	upstreamHTTPProtocolHTTP2 = "http2"

	headerAuthStatus      = "Auth-Status"
	headerAuthServer      = "Auth-Server"
	headerAuthPort        = "Auth-Port"
	headerAuthUser        = "Auth-User"
	headerAuthError       = "Auth-Error"
	headerSessionID       = "X-Session-ID"
	headerLocation        = "Location"
	headerSetCookie       = "Set-Cookie"
	headerContentType     = "Content-Type"
	headerCacheControl    = "Cache-Control"
	headerPragma          = "Pragma"
	headerExpires         = "Expires"
	headerWWWAuthenticate = "WWW-Authenticate"
	headerContentEncoding = "Content-Encoding"
	headerVary            = "Vary"
)

// Config holds all configuration settings for the proxy.
type Config struct {
	// Protocol selects the proxy protocol (http, grpc, or milter).
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

	// GRPCListenAddr is the TCP address for the gRPC proxy listener.
	// Applies to: gRPC protocol.
	GRPCListenAddr string `mapstructure:"grpc_listen_addr"`

	// GRPCTLS controls inbound gRPC TLS and mTLS.
	// Applies to: gRPC protocol.
	GRPCTLS GRPCTLSConfig `mapstructure:"grpc_tls"`

	// PrimaryGRPCSelectionMode controls primary gRPC target selection.
	// Supported: round_robin, source_ip_hash.
	PrimaryGRPCSelectionMode string `mapstructure:"primary_grpc_selection_mode"`

	// ShadowGRPCSelectionMode controls shadow gRPC target selection.
	// Supported: round_robin, source_ip_hash.
	ShadowGRPCSelectionMode string `mapstructure:"shadow_grpc_selection_mode"`

	// PrimaryGRPCTargets is the list of primary gRPC backends.
	// Applies to: gRPC protocol.
	PrimaryGRPCTargets []GRPCTarget `mapstructure:"primary_grpc_targets"`

	// ShadowGRPCTargets is the list of shadow gRPC backends.
	// Applies to: gRPC protocol.
	ShadowGRPCTargets []GRPCTarget `mapstructure:"shadow_grpc_targets"`

	// GRPCShadowTimeout limits the lifetime of shadow gRPC work.
	// Applies to: gRPC protocol.
	GRPCShadowTimeout time.Duration `mapstructure:"grpc_shadow_timeout"`

	// GRPCShadowForceMetadata forces gRPC shadowing when present and non-empty.
	// Applies to: gRPC protocol.
	GRPCShadowForceMetadata string `mapstructure:"grpc_shadow_force_metadata"`

	// GRPCShadowQueueSize bounds queued request messages for shadow forwarding.
	// Applies to: gRPC protocol.
	GRPCShadowQueueSize int `mapstructure:"grpc_shadow_queue_size"`

	// GRPCMaxReceiveMessageBytes limits inbound gRPC receive message size.
	// Applies to: gRPC protocol.
	GRPCMaxReceiveMessageBytes int `mapstructure:"grpc_max_receive_message_bytes"`

	// GRPCMaxSendMessageBytes limits outbound gRPC send message size.
	// Applies to: gRPC protocol.
	GRPCMaxSendMessageBytes int `mapstructure:"grpc_max_send_message_bytes"`

	// GRPCCompareMode selects the default gRPC comparison mode.
	// Applies to: gRPC protocol.
	GRPCCompareMode string `mapstructure:"grpc_compare_mode"`

	// GRPCCompareMetadata lists metadata keys compared by default.
	// Applies to: gRPC protocol.
	GRPCCompareMetadata []string `mapstructure:"grpc_compare_metadata"`

	// GRPCRules define ordered gRPC service/method shadow and comparison policy.
	// Applies to: gRPC protocol.
	GRPCRules []GRPCRule `mapstructure:"grpc_rules"`

	// GRPCCallerAuth verifies inbound callers before backend credentials are used.
	// Applies to: gRPC protocol.
	GRPCCallerAuth GRPCCallerAuthConfig `mapstructure:"grpc_caller_auth"`

	// GRPCBackendOIDCAuth configures service-to-service OIDC authorization for primary gRPC upstream calls.
	GRPCBackendOIDCAuth GRPCBackendOIDCAuthConfig `mapstructure:"grpc_backend_oidc_auth"`

	// TLSCertFile path to the HTTP listener TLS certificate file.
	TLSCertFile string `mapstructure:"tls_cert_file"`

	// TLSKeyFile path to the HTTP listener TLS key file.
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

	// RootCAPath is the path to a common CA certificate for HTTP upstream backends.
	RootCAPath string `mapstructure:"root_ca"`

	// PrimaryRootCA path to the CA certificate specifically for the primary HTTP backend.
	PrimaryRootCA string `mapstructure:"primary_root_ca"`

	// ShadowRootCA path to the CA certificate specifically for the shadow HTTP backend.
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

	// CompareMode selects the comparison mode (header, json, html).
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

	// LogOnlyOnDiff controls whether proxy result logs are emitted only for differences or comparison errors.
	LogOnlyOnDiff bool `mapstructure:"log_only_on_diff"`

	// LogLevel sets the minimum severity; none disables application logs.
	LogLevel string `mapstructure:"log_level"`

	// LogJSON controls whether the logger should output JSON.
	LogJSON bool `mapstructure:"log_json"`

	// InsecureUpstream allows insecure TLS connections to HTTP backends.
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
	Name                  string            `mapstructure:"name"`
	Methods               []string          `mapstructure:"methods"`
	Match                 string            `mapstructure:"match"`
	Shadow                string            `mapstructure:"shadow"`
	Compare               string            `mapstructure:"compare"`
	CompareMode           string            `mapstructure:"compare_mode"`
	CompareHeaders        []string          `mapstructure:"compare_headers"`
	PrimaryRequestHeaders map[string]string `mapstructure:"primary_request_headers"`
	ShadowRequestHeaders  map[string]string `mapstructure:"shadow_request_headers"`
}

// GRPCTLSConfig configures inbound gRPC TLS and mTLS.
type GRPCTLSConfig struct {
	Enabled           bool   `mapstructure:"enabled"`
	Cert              string `mapstructure:"cert"`
	Key               string `mapstructure:"key"`
	ClientCA          string `mapstructure:"client_ca"`
	RequireClientCert bool   `mapstructure:"require_client_cert"`
	MinTLSVersion     string `mapstructure:"min_tls_version"`
}

// GRPCBackendOIDCAuthConfig configures client-credentials Bearer tokens for primary gRPC backend calls.
type GRPCBackendOIDCAuthConfig struct {
	CAFile           string        `mapstructure:"ca_file"`
	ServerName       string        `mapstructure:"server_name"`
	MinTLSVersion    string        `mapstructure:"min_tls_version"`
	Enabled          bool          `mapstructure:"enabled"`
	ConfigurationURI string        `mapstructure:"configuration_uri"`
	TokenEndpoint    string        `mapstructure:"token_endpoint"`
	ClientID         string        `mapstructure:"client_id"`
	ClientSecret     string        `mapstructure:"client_secret"`
	ClientSecretEnv  string        `mapstructure:"client_secret_env"`
	AuthMethod       string        `mapstructure:"auth_method"`
	Scopes           []string      `mapstructure:"scopes"`
	Timeout          time.Duration `mapstructure:"timeout"`
	RefreshSkew      time.Duration `mapstructure:"refresh_skew"`
	InsecureTLS      bool          `mapstructure:"insecure_tls"`
}

// GRPCTarget describes one upstream gRPC backend.
type GRPCTarget struct {
	Name      string        `mapstructure:"name"`
	Address   string        `mapstructure:"address"`
	Authority string        `mapstructure:"authority"`
	TLS       GRPCTargetTLS `mapstructure:"tls"`
}

// GRPCTargetTLS configures TLS for one upstream gRPC target.
type GRPCTargetTLS struct {
	Enabled            bool   `mapstructure:"enabled"`
	RootCA             string `mapstructure:"root_ca"`
	ServerName         string `mapstructure:"server_name"`
	InsecureSkipVerify bool   `mapstructure:"insecure_skip_verify"`
	ClientCert         string `mapstructure:"client_cert"`
	ClientKey          string `mapstructure:"client_key"`
}

// GRPCRule describes service/method-specific gRPC shadow and comparison policy.
type GRPCRule struct {
	Name            string            `mapstructure:"name"`
	Service         string            `mapstructure:"service"`
	Methods         []string          `mapstructure:"methods"`
	Shadow          string            `mapstructure:"shadow"`
	Compare         string            `mapstructure:"compare"`
	CompareMode     string            `mapstructure:"compare_mode"`
	CompareMetadata []string          `mapstructure:"compare_metadata"`
	PrimaryMetadata map[string]string `mapstructure:"primary_metadata"`
	ShadowMetadata  map[string]string `mapstructure:"shadow_metadata"`
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
	setGRPCDefaults(v)
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

func setGRPCDefaults(v *viper.Viper) {
	v.SetDefault("grpc_listen_addr", ":9444")
	v.SetDefault("grpc_tls.enabled", false)
	v.SetDefault("grpc_tls.cert", "")
	v.SetDefault("grpc_tls.key", "")
	v.SetDefault("grpc_tls.client_ca", "")
	v.SetDefault("grpc_tls.require_client_cert", false)
	v.SetDefault("grpc_tls.min_tls_version", tlsMinVersionDefault)
	v.SetDefault("primary_grpc_selection_mode", selectionRoundRobin)
	v.SetDefault("shadow_grpc_selection_mode", selectionRoundRobin)
	v.SetDefault("primary_grpc_targets", []GRPCTarget{})
	v.SetDefault("shadow_grpc_targets", []GRPCTarget{})
	v.SetDefault("grpc_shadow_timeout", 500*time.Millisecond)
	v.SetDefault("grpc_shadow_force_metadata", "")
	v.SetDefault("grpc_shadow_queue_size", 128)
	v.SetDefault("grpc_max_receive_message_bytes", 4*1024*1024)
	v.SetDefault("grpc_max_send_message_bytes", 4*1024*1024)
	v.SetDefault("grpc_compare_mode", grpcCompareModeStatus)
	v.SetDefault("grpc_compare_metadata", []string{grpcMetadataStatus, grpcMetadataMessage})
	v.SetDefault("grpc_rules", []GRPCRule{})
	v.SetDefault("grpc_caller_auth.mode", "")
	v.SetDefault("grpc_caller_auth.allow_unauthenticated", false)
	v.SetDefault("grpc_caller_auth.timeout", 2*time.Second)
	v.SetDefault("grpc_caller_auth.introspection_cache_ttl", 30*time.Second)
	v.SetDefault("grpc_caller_auth.introspection_cache_max_entries", 10000)
	v.SetDefault("grpc_caller_auth.introspection_max_concurrent", DefaultGRPCIntrospectionMaxConcurrent)
	v.SetDefault("grpc_backend_oidc_auth.enabled", false)
	v.SetDefault("grpc_backend_oidc_auth.auth_method", grpcOIDCAuthMethodAuto)
	v.SetDefault("grpc_backend_oidc_auth.timeout", 5*time.Second)
	v.SetDefault("grpc_backend_oidc_auth.refresh_skew", 30*time.Second)
	v.SetDefault("grpc_backend_oidc_auth.insecure_tls", false)
}

func setCompareDefaults(v *viper.Viper) {
	v.SetDefault("compare_mode", compareModeHeader)
	v.SetDefault("compare_json_strict", false)
	v.SetDefault("compare_html_threshold", 0.99)
	v.SetDefault("log_session_only_on_diff", true)
	v.SetDefault("log_only_on_diff", false)
}

func setRuntimeDefaults(v *viper.Viper) {
	v.SetDefault("log_json", true)
	v.SetDefault("log_level", "info")
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
		headerSessionID,
		headerLocation,
		headerSetCookie,
		headerContentType,
		headerCacheControl,
		headerPragma,
		headerExpires,
		headerWWWAuthenticate,
		headerContentEncoding,
		headerVary,
	})
	v.SetDefault("compare_headers", []string{
		headerAuthStatus,
		headerAuthServer,
		headerAuthPort,
		headerAuthUser,
		headerAuthError,
		headerSessionID,
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
	if _, err := logging.ParseLevel(cfg.LogLevel); err != nil {
		return err
	}

	cfg.LogLevel = strings.ToLower(strings.TrimSpace(cfg.LogLevel))
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}

	if err := normalizeProtocol(cfg); err != nil {
		return err
	}

	if err := validateBackendLists(*cfg); err != nil {
		return err
	}

	cfg.PrimarySelectionMode = normalizeSelectionMode(cfg.PrimarySelectionMode)
	cfg.ShadowSelectionMode = normalizeSelectionMode(cfg.ShadowSelectionMode)

	normalizeShadowConfig(cfg)

	if err := normalizeGRPCConfig(cfg); err != nil {
		return err
	}

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

	if protocol != protocolHTTP && protocol != protocolMilter && protocol != protocolGRPC {
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

func normalizeGRPCConfig(cfg *Config) error {
	if cfg.Protocol != protocolGRPC {
		return nil
	}

	if err := normalizeGRPCListenerAndTLS(cfg); err != nil {
		return err
	}

	if err := normalizeGRPCSelectionModes(cfg); err != nil {
		return err
	}

	if err := normalizeGRPCRuntimeConfig(cfg); err != nil {
		return err
	}

	if err := normalizeGRPCMetadataConfig(cfg); err != nil {
		return err
	}

	if err := normalizeGRPCTargetAndRuleConfig(cfg); err != nil {
		return err
	}

	if err := normalizeGRPCBackendOIDCAuthConfig(cfg); err != nil {
		return err
	}

	if err := ValidateGRPCCallerAuth(*cfg); err != nil {
		return err
	}

	if cfg.GRPCCallerAuth.Mode == grpcCallerIntrospectionMode {
		a := cfg.GRPCCallerAuth
		_, err := AuthClientTLSConfig(a.CAFile, a.ServerName, a.MinTLSVersion)

		return err
	}

	return nil
}

func normalizeGRPCListenerAndTLS(cfg *Config) error {
	cfg.GRPCListenAddr = strings.TrimSpace(cfg.GRPCListenAddr)
	if cfg.GRPCListenAddr == "" {
		return errors.New("grpc_listen_addr must not be empty when protocol is grpc")
	}

	return normalizeGRPCTLS(&cfg.GRPCTLS)
}

func normalizeGRPCSelectionModes(cfg *Config) error {
	var err error
	if cfg.PrimaryGRPCSelectionMode, err = normalizeGRPCSelectionMode(cfg.PrimaryGRPCSelectionMode, "primary_grpc_selection_mode"); err != nil {
		return err
	}

	if cfg.ShadowGRPCSelectionMode, err = normalizeGRPCSelectionMode(cfg.ShadowGRPCSelectionMode, "shadow_grpc_selection_mode"); err != nil {
		return err
	}

	return nil
}

func normalizeGRPCRuntimeConfig(cfg *Config) error {
	if cfg.GRPCShadowTimeout <= 0 {
		cfg.GRPCShadowTimeout = 500 * time.Millisecond
	}

	if cfg.GRPCShadowQueueSize <= 0 {
		cfg.GRPCShadowQueueSize = 128
	}

	if cfg.GRPCMaxReceiveMessageBytes < 0 {
		return errors.New("grpc_max_receive_message_bytes must not be negative")
	}

	if cfg.GRPCMaxSendMessageBytes < 0 {
		return errors.New("grpc_max_send_message_bytes must not be negative")
	}

	if cfg.GRPCMaxReceiveMessageBytes == 0 {
		cfg.GRPCMaxReceiveMessageBytes = 4 * 1024 * 1024
	}

	if cfg.GRPCMaxSendMessageBytes == 0 {
		cfg.GRPCMaxSendMessageBytes = 4 * 1024 * 1024
	}

	return nil
}

func normalizeGRPCMetadataConfig(cfg *Config) error {
	var err error

	cfg.GRPCShadowForceMetadata = strings.TrimSpace(cfg.GRPCShadowForceMetadata)
	if cfg.GRPCShadowForceMetadata != "" {
		if cfg.GRPCShadowForceMetadata, err = normalizeGRPCMetadataKey(cfg.GRPCShadowForceMetadata); err != nil {
			return fmt.Errorf("grpc_shadow_force_metadata: %w", err)
		}
	}

	if cfg.GRPCCompareMode, err = normalizeGRPCCompareMode(cfg.GRPCCompareMode, false); err != nil {
		return fmt.Errorf("grpc_compare_mode: %w", err)
	}

	if cfg.GRPCCompareMetadata, err = normalizeGRPCMetadataKeys(cfg.GRPCCompareMetadata); err != nil {
		return fmt.Errorf("grpc_compare_metadata: %w", err)
	}

	return nil
}

func normalizeGRPCBackendOIDCAuthConfig(cfg *Config) error {
	token := &cfg.GRPCBackendOIDCAuth
	normalizeGRPCBackendOIDCAuthDefaults(token)

	if !token.Enabled {
		return nil
	}

	if _, err := AuthClientTLSConfig(token.CAFile, token.ServerName, token.MinTLSVersion); err != nil {
		return err
	}

	if token.InsecureTLS && (token.CAFile != "" || token.ServerName != "") {
		return errors.New("grpc_backend_oidc_auth: insecure_tls cannot be combined with ca_file or server_name")
	}

	if err := validateGRPCBackendOIDCAuthEndpoints(token); err != nil {
		return err
	}

	if token.ClientID == "" {
		return errors.New("grpc_backend_oidc_auth.client_id is required when grpc_backend_oidc_auth.enabled is true")
	}

	if err := validateGRPCBackendOIDCAuthSecrets(token); err != nil {
		return err
	}

	if err := validateGRPCBackendOIDCAuthMethod(token.AuthMethod); err != nil {
		return err
	}

	return validateGRPCBackendOIDCAuthMetadataConflict(cfg)
}

func normalizeGRPCBackendOIDCAuthDefaults(token *GRPCBackendOIDCAuthConfig) {
	token.ConfigurationURI = strings.TrimSpace(token.ConfigurationURI)
	token.TokenEndpoint = strings.TrimSpace(token.TokenEndpoint)
	token.ClientID = strings.TrimSpace(token.ClientID)
	token.ClientSecret = strings.TrimSpace(token.ClientSecret)
	token.ClientSecretEnv = strings.TrimSpace(token.ClientSecretEnv)
	token.AuthMethod = strings.ToLower(strings.TrimSpace(token.AuthMethod))

	if token.AuthMethod == "" {
		token.AuthMethod = grpcOIDCAuthMethodAuto
	}

	token.Scopes = normalizeStringList(token.Scopes)

	if token.Timeout <= 0 {
		token.Timeout = 5 * time.Second
	}

	if token.RefreshSkew <= 0 {
		token.RefreshSkew = 30 * time.Second
	}
}

func validateGRPCBackendOIDCAuthEndpoints(token *GRPCBackendOIDCAuthConfig) error {
	if token.ConfigurationURI == "" && token.TokenEndpoint == "" {
		return errors.New("grpc_backend_oidc_auth.configuration_uri or token_endpoint is required when grpc_backend_oidc_auth.enabled is true")
	}

	if err := validateGRPCBackendOIDCAuthURL("configuration_uri", token.ConfigurationURI); err != nil {
		return err
	}

	return validateGRPCBackendOIDCAuthURL("token_endpoint", token.TokenEndpoint)
}

func validateGRPCBackendOIDCAuthURL(field string, value string) error {
	if value == "" {
		return nil
	}

	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("grpc_backend_oidc_auth.%s must be an absolute URL: %q", field, value)
	}

	if parsed.Scheme != grpcHTTPScheme && parsed.Scheme != protocolHTTP {
		return fmt.Errorf("grpc_backend_oidc_auth.%s has unsupported scheme %q", field, parsed.Scheme)
	}

	return nil
}

func validateGRPCBackendOIDCAuthSecrets(token *GRPCBackendOIDCAuthConfig) error {
	if token.ClientSecret == "" && token.ClientSecretEnv == "" {
		return errors.New("grpc_backend_oidc_auth.client_secret or client_secret_env is required when grpc_backend_oidc_auth.enabled is true")
	}

	if token.ClientSecret != "" && token.ClientSecretEnv != "" {
		return errors.New("grpc_backend_oidc_auth must not set both client_secret and client_secret_env")
	}

	return nil
}

func validateGRPCBackendOIDCAuthMethod(method string) error {
	switch method {
	case grpcOIDCAuthMethodAuto, grpcOIDCAuthMethodClientSecretPost, grpcOIDCAuthMethodClientSecretBasic:
		return nil
	default:
		return fmt.Errorf("grpc_backend_oidc_auth.auth_method has unsupported value %q", method)
	}
}

func validateGRPCBackendOIDCAuthMetadataConflict(cfg *Config) error {
	for index, rule := range cfg.GRPCRules {
		if _, ok := rule.PrimaryMetadata["authorization"]; ok {
			return fmt.Errorf("grpc_rules[%d].primary_metadata.authorization conflicts with grpc_backend_oidc_auth", index)
		}
	}

	return nil
}

func normalizeGRPCTargetAndRuleConfig(cfg *Config) error {
	if err := normalizeGRPCTargets(cfg.PrimaryGRPCTargets, "primary_grpc_targets"); err != nil {
		return err
	}

	if len(cfg.PrimaryGRPCTargets) == 0 {
		return errors.New("at least one primary gRPC target must be configured via primary_grpc_targets")
	}

	if err := normalizeGRPCTargets(cfg.ShadowGRPCTargets, "shadow_grpc_targets"); err != nil {
		return err
	}

	if err := normalizeGRPCRules(cfg); err != nil {
		return err
	}

	if grpcShadowTargetsRequired(*cfg) && len(cfg.ShadowGRPCTargets) == 0 {
		return errors.New("at least one shadow gRPC target must be configured when gRPC shadowing can be enabled")
	}

	return nil
}

func normalizeGRPCTLS(cfg *GRPCTLSConfig) error {
	cfg.Cert = strings.TrimSpace(cfg.Cert)
	cfg.Key = strings.TrimSpace(cfg.Key)
	cfg.ClientCA = strings.TrimSpace(cfg.ClientCA)
	cfg.MinTLSVersion = strings.TrimSpace(cfg.MinTLSVersion)

	if cfg.MinTLSVersion == "" {
		cfg.MinTLSVersion = tlsMinVersionDefault
	}

	if _, err := ResolveTLSMinVersion(cfg.MinTLSVersion); err != nil {
		return fmt.Errorf("grpc_tls: %w", err)
	}

	if cfg.RequireClientCert && cfg.ClientCA == "" {
		return errors.New("grpc_tls requires client_ca when require_client_cert is true")
	}

	if cfg.RequireClientCert && !cfg.Enabled {
		return errors.New("grpc_tls require_client_cert requires enabled TLS")
	}

	if !cfg.Enabled {
		return nil
	}

	if cfg.Cert == "" || cfg.Key == "" {
		return errors.New("grpc_tls requires cert and key when enabled")
	}

	return nil
}

func normalizeGRPCSelectionMode(raw string, field string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(raw))
	switch mode {
	case "":
		return selectionRoundRobin, nil
	case selectionRoundRobin, selectionSourceIPHash:
		return mode, nil
	default:
		return "", fmt.Errorf("%s must be one of round_robin or source_ip_hash", field)
	}
}

func normalizeGRPCTargets(targets []GRPCTarget, field string) error {
	for i := range targets {
		targets[i].Name = strings.TrimSpace(targets[i].Name)
		targets[i].Address = strings.TrimSpace(targets[i].Address)
		targets[i].Authority = strings.TrimSpace(targets[i].Authority)

		if targets[i].Address == "" {
			return fmt.Errorf("%s[%d].address is required", field, i)
		}

		if err := normalizeGRPCTargetTLS(&targets[i].TLS, fmt.Sprintf("%s[%d].tls", field, i)); err != nil {
			return err
		}
	}

	return nil
}

func normalizeGRPCTargetTLS(cfg *GRPCTargetTLS, field string) error {
	cfg.RootCA = strings.TrimSpace(cfg.RootCA)
	cfg.ServerName = strings.TrimSpace(cfg.ServerName)
	cfg.ClientCert = strings.TrimSpace(cfg.ClientCert)
	cfg.ClientKey = strings.TrimSpace(cfg.ClientKey)

	if cfg.ClientCert == "" && cfg.ClientKey == "" {
		return nil
	}

	if !cfg.Enabled {
		return fmt.Errorf("%s client_cert/client_key require enabled TLS", field)
	}

	if cfg.ClientCert == "" || cfg.ClientKey == "" {
		return fmt.Errorf("%s client_cert and client_key must be configured together", field)
	}

	return nil
}

func normalizeGRPCRules(cfg *Config) error {
	if cfg.GRPCRules == nil {
		cfg.GRPCRules = []GRPCRule{}

		return nil
	}

	for i := range cfg.GRPCRules {
		if err := normalizeGRPCRule(&cfg.GRPCRules[i], i); err != nil {
			return err
		}
	}

	return nil
}

func normalizeGRPCRule(rule *GRPCRule, index int) error {
	rule.Name = strings.TrimSpace(rule.Name)
	rule.Service = strings.TrimSpace(rule.Service)

	if rule.Service == "" {
		return fmt.Errorf("grpc_rules[%d].service is required", index)
	}

	if err := normalizeGRPCRuleMethods(rule, index); err != nil {
		return err
	}

	var err error
	if rule.Shadow, err = normalizeGRPCRuleShadow(rule.Shadow); err != nil {
		return fmt.Errorf("grpc_rules[%d].shadow: %w", index, err)
	}

	if rule.Compare, err = normalizeGRPCRuleCompare(rule.Compare); err != nil {
		return fmt.Errorf("grpc_rules[%d].compare: %w", index, err)
	}

	if rule.CompareMode, err = normalizeGRPCCompareMode(rule.CompareMode, true); err != nil {
		return fmt.Errorf("grpc_rules[%d].compare_mode: %w", index, err)
	}

	if rule.CompareMetadata, err = normalizeGRPCMetadataKeys(rule.CompareMetadata); err != nil {
		return fmt.Errorf("grpc_rules[%d].compare_metadata: %w", index, err)
	}

	if rule.PrimaryMetadata, err = normalizeGRPCOverlayMetadataMap(rule.PrimaryMetadata); err != nil {
		return fmt.Errorf("grpc_rules[%d].primary_metadata: %w", index, err)
	}

	if rule.ShadowMetadata, err = normalizeGRPCOverlayMetadataMap(rule.ShadowMetadata); err != nil {
		return fmt.Errorf("grpc_rules[%d].shadow_metadata: %w", index, err)
	}

	return nil
}

func normalizeGRPCRuleMethods(rule *GRPCRule, index int) error {
	if rule.Methods == nil {
		return nil
	}

	normalized := make([]string, 0, len(rule.Methods))
	for _, raw := range rule.Methods {
		method := strings.TrimSpace(raw)
		if method == "" || strings.Contains(method, "/") {
			return fmt.Errorf("grpc_rules[%d].methods contains invalid method %q", index, raw)
		}

		normalized = append(normalized, method)
	}

	rule.Methods = normalized

	return nil
}

func normalizeGRPCRuleShadow(raw string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(raw))
	switch mode {
	case "":
		return grpcRuleShadowInherit, nil
	case grpcRuleShadowInherit, grpcRuleShadowAuto, grpcRuleShadowNever, grpcRuleShadowAlways:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported value %q (allowed: inherit, auto, never, always)", raw)
	}
}

func normalizeGRPCRuleCompare(raw string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(raw))
	switch mode {
	case "":
		return grpcRuleCompareInherit, nil
	case grpcRuleCompareInherit, grpcRuleCompareOn, grpcRuleCompareOff:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported value %q (allowed: inherit, on, off)", raw)
	}
}

func normalizeGRPCCompareMode(raw string, allowEmpty bool) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(raw))
	switch mode {
	case "":
		if allowEmpty {
			return "", nil
		}

		return grpcCompareModeStatus, nil
	case grpcCompareModeStatus, grpcCompareModeStatusMetadata, grpcCompareModeMessageCount, grpcCompareModeMessageHash:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported value %q (allowed: status, status_metadata, message_count, message_hash)", raw)
	}
}

func normalizeGRPCMetadataKeys(names []string) ([]string, error) {
	if names == nil {
		return nil, nil
	}

	normalized := make([]string, 0, len(names))

	seen := make(map[string]struct{}, len(names))
	for _, raw := range names {
		name, err := normalizeGRPCMetadataKey(raw)
		if err != nil {
			return nil, err
		}

		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("duplicate gRPC metadata key %q", name)
		}

		seen[name] = struct{}{}
		normalized = append(normalized, name)
	}

	return normalized, nil
}

func normalizeStringList(values []string) []string {
	if values == nil {
		return nil
	}

	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			normalized = append(normalized, value)
		}
	}

	return normalized
}

func normalizeGRPCOverlayMetadataMap(values map[string]string) (map[string]string, error) {
	if values == nil {
		return nil, nil
	}

	normalized := make(map[string]string, len(values))
	for raw, value := range values {
		name, err := normalizeGRPCMetadataKey(raw)
		if err != nil {
			return nil, err
		}

		if err := validateGRPCOverlayMetadataKey(name); err != nil {
			return nil, err
		}

		if _, exists := normalized[name]; exists {
			return nil, fmt.Errorf("duplicate gRPC metadata key %q", name)
		}

		normalized[name] = value
	}

	return normalized, nil
}

func normalizeGRPCMetadataKey(raw string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(raw))
	if key == "" {
		return "", errors.New("gRPC metadata key must not be empty")
	}

	for _, r := range key {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}

		return "", fmt.Errorf("invalid gRPC metadata key %q", raw)
	}

	return key, nil
}

func validateGRPCOverlayMetadataKey(key string) error {
	if strings.Contains(key, ":") {
		return fmt.Errorf("reserved gRPC metadata key %q", key)
	}

	if key == "content-type" || key == "te" {
		return fmt.Errorf("reserved gRPC metadata key %q", key)
	}

	if strings.HasPrefix(key, "grpc-") {
		return fmt.Errorf("reserved gRPC metadata key %q", key)
	}

	if strings.HasSuffix(key, "-bin") {
		return fmt.Errorf("binary gRPC metadata overlay %q is not supported", key)
	}

	return nil
}

func grpcShadowTargetsRequired(cfg Config) bool {
	globalShadow := cfg.ShadowSamplePercent > 0 || cfg.GRPCShadowForceMetadata != ""
	if len(cfg.GRPCRules) == 0 {
		return globalShadow
	}

	required := false

	for _, rule := range cfg.GRPCRules {
		switch rule.Shadow {
		case grpcRuleShadowAlways:
			required = true
		case grpcRuleShadowAuto, grpcRuleShadowInherit:
			if globalShadow {
				required = true
			}
		}

		if rule.Service == "*" && len(rule.Methods) == 0 {
			return required
		}
	}

	return required
}

func normalizeCompareConfig(cfg *Config) {
	mode := strings.ToLower(strings.TrimSpace(cfg.CompareMode))
	switch mode {
	case "", compareModeHeader:
		mode = compareModeHeader
	case compareModeJSON, compareModeHTML:
	default:
		mode = compareModeHeader
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
		if err := validatePathRule(&cfg.PathRules[i], i); err != nil {
			return err
		}
	}

	return nil
}

func validatePathRule(rule *PathRule, index int) error {
	rule.Name = strings.TrimSpace(rule.Name)
	rule.Match = strings.TrimSpace(rule.Match)

	if rule.Match == "" {
		return fmt.Errorf("path_rules[%d].match is required", index)
	}

	if _, err := regexp.Compile(rule.Match); err != nil {
		return fmt.Errorf("path_rules[%d].match is invalid: %w", index, err)
	}

	if err := normalizePathRuleModes(rule, index); err != nil {
		return err
	}

	return normalizePathRuleHeaders(rule, index)
}

func normalizePathRuleModes(rule *PathRule, index int) error {
	var err error

	if rule.Methods, err = normalizePathRuleMethods(rule.Methods); err != nil {
		return fmt.Errorf("path_rules[%d].methods: %w", index, err)
	}

	if rule.Shadow, err = normalizePathRuleShadow(rule.Shadow); err != nil {
		return fmt.Errorf("path_rules[%d].shadow: %w", index, err)
	}

	if rule.Compare, err = normalizePathRuleCompare(rule.Compare); err != nil {
		return fmt.Errorf("path_rules[%d].compare: %w", index, err)
	}

	if rule.CompareMode, err = normalizePathRuleCompareMode(rule.CompareMode); err != nil {
		return fmt.Errorf("path_rules[%d].compare_mode: %w", index, err)
	}

	return nil
}

func normalizePathRuleHeaders(rule *PathRule, index int) error {
	var err error

	if rule.CompareHeaders, err = normalizeHTTPHeaderNames(rule.CompareHeaders); err != nil {
		return fmt.Errorf("path_rules[%d].compare_headers: %w", index, err)
	}

	if rule.PrimaryRequestHeaders, err = normalizeHTTPHeaderMap(rule.PrimaryRequestHeaders); err != nil {
		return fmt.Errorf("path_rules[%d].primary_request_headers: %w", index, err)
	}

	if rule.ShadowRequestHeaders, err = normalizeHTTPHeaderMap(rule.ShadowRequestHeaders); err != nil {
		return fmt.Errorf("path_rules[%d].shadow_request_headers: %w", index, err)
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
	case "":
		return mode, nil
	case compareModeHeader:
		return compareModeHeader, nil
	case compareModeJSON, compareModeHTML:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported value %q (allowed: header, json, html)", raw)
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

func normalizeHTTPHeaderMap(values map[string]string) (map[string]string, error) {
	if values == nil {
		return nil, nil
	}

	normalized := make(map[string]string, len(values))
	for raw, value := range values {
		name := strings.TrimSpace(raw)
		canonical := http.CanonicalHeaderKey(name)

		if canonical == "" || !httpguts.ValidHeaderFieldName(canonical) {
			return nil, fmt.Errorf("invalid HTTP header name %q", raw)
		}

		if _, exists := normalized[canonical]; exists {
			return nil, fmt.Errorf("duplicate HTTP header name %q", canonical)
		}

		normalized[canonical] = value
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
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "1.2", "tls1.2":
		return tls.VersionTLS12, nil
	case "1.3", "tls1.3":
		return tls.VersionTLS13, nil
	default:
		return 0, fmt.Errorf("unsupported min_tls_version %q (allowed: 1.2, TLS1.2, 1.3, TLS1.3)", value)
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
