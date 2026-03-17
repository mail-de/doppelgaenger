package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/mitchellh/mapstructure"
	"github.com/spf13/viper"

	"doppelgaenger/internal/mapping"
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
	v.SetDefault("protocol", "http")
	v.SetDefault("listen_addr", ":8080")
	v.SetDefault("tls_cert_file", "")
	v.SetDefault("tls_key_file", "")
	v.SetDefault("milter_listen_addr", ":9999")
	v.SetDefault("primary_milter_addr", "127.0.0.1:9997")
	v.SetDefault("shadow_milter_addr", "127.0.0.1:9998")
	v.SetDefault("milter_timeout", 2*time.Second)
	v.SetDefault("primary_base_urls", []string{"https://127.0.0.1:9001"})
	v.SetDefault("primary_selection_mode", "round_robin")
	v.SetDefault("shadow_base_urls", []string{"https://127.0.0.1:9002"})
	v.SetDefault("shadow_selection_mode", "round_robin")
	v.SetDefault("shadow_timeout", 150*time.Millisecond)
	v.SetDefault("shadow_sample_percent", 5)
	v.SetDefault("shadow_force_header", "X-Shadow")
	v.SetDefault("primary_request_headers", map[string]string{})
	v.SetDefault("shadow_request_headers", map[string]string{})
	v.SetDefault("shadow_rps", 200.0)
	v.SetDefault("shadow_burst", 400)
	v.SetDefault("compare_mode", "nginx")
	v.SetDefault("compare_json_strict", false)
	v.SetDefault("compare_html_threshold", 0.99)
	v.SetDefault("log_session_only_on_diff", true)
	v.SetDefault("log_json", true)
	v.SetDefault("max_backend_body_bytes", 32*1024)
	v.SetDefault("upstream_http_dial_timeout", 2*time.Second)
	v.SetDefault("upstream_http_tls_handshake_timeout", 5*time.Second)
	v.SetDefault("upstream_http_response_header_timeout", 5*time.Second)
	v.SetDefault("upstream_http_max_idle_conns", 1024)
	v.SetDefault("upstream_http_max_idle_conns_per_host", 256)
	v.SetDefault("upstream_http_max_conns_per_host", 0)
	v.SetDefault("root_ca", "")
	v.SetDefault("primary_root_ca", "")
	v.SetDefault("shadow_root_ca", "")
	v.SetDefault("insecure_upstream", false)
	v.SetDefault("run_as_user", "")
	v.SetDefault("run_as_group", "")
	v.SetDefault("chroot", "")
	v.SetDefault("forward_response_headers", []string{
		"Auth-Status",
		"Auth-Server",
		"Auth-Port",
		"Auth-User",
		"Auth-Pass",
		"Auth-Error",
		"Auth-Wait",
		"Auth-Protocol",
		"X-Nauthilus-Session",
	})
	v.SetDefault("compare_headers", []string{
		"Auth-Status",
		"Auth-Server",
		"Auth-Port",
		"Auth-User",
		"Auth-Error",
		"X-Nauthilus-Session",
	})
	v.SetDefault("path_mapping", mapping.Config{
		Mode:  "direct",
		Rules: []mapping.Rule{},
	})
}

func validate(cfg *Config) error {
	protocol := strings.ToLower(strings.TrimSpace(cfg.Protocol))
	if protocol == "" {
		protocol = "http"
	}
	if protocol != "http" && protocol != "milter" {
		return fmt.Errorf("invalid protocol: %s", cfg.Protocol)
	}
	cfg.Protocol = protocol

	if cfg.Protocol == "http" && len(cfg.PrimaryBaseURLs) == 0 {
		return errors.New("at least one primary backend must be configured via primary_base_urls")
	}

	if cfg.Protocol == "http" && len(cfg.ShadowBaseURLs) == 0 {
		return errors.New("at least one shadow backend must be configured via shadow_base_urls")
	}

	cfg.PrimarySelectionMode = normalizeSelectionMode(cfg.PrimarySelectionMode)
	cfg.ShadowSelectionMode = normalizeSelectionMode(cfg.ShadowSelectionMode)

	if cfg.ShadowSamplePercent < 0 {
		cfg.ShadowSamplePercent = 0
	}
	if cfg.ShadowSamplePercent > 100 {
		cfg.ShadowSamplePercent = 100
	}
	if cfg.ShadowBurst < 1 && cfg.ShadowRPS > 0 {
		cfg.ShadowBurst = 1
	}
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

	mode := strings.ToLower(strings.TrimSpace(cfg.CompareMode))
	switch mode {
	case "", "nginx", "header", "json", "html":
		if mode == "" {
			mode = "nginx"
		}
	case "nxinx":
		mode = "nginx"
	default:
		mode = "nginx"
	}
	cfg.CompareMode = mode

	if cfg.HTMLSimilarityThreshold < 0 {
		cfg.HTMLSimilarityThreshold = 0
	}
	if cfg.HTMLSimilarityThreshold > 1 {
		cfg.HTMLSimilarityThreshold = 1
	}

	cfg.RunAsUser = strings.TrimSpace(cfg.RunAsUser)
	cfg.RunAsGroup = strings.TrimSpace(cfg.RunAsGroup)
	cfg.ChrootDir = strings.TrimSpace(cfg.ChrootDir)

	return nil
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
	case "", "round_robin", "source_ip_hash":
		if normalized == "" {
			return "round_robin"
		}
		return normalized
	default:
		return "round_robin"
	}
}
