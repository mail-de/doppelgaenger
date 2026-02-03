package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all configuration settings for the proxy.
type Config struct {
	// PrimaryBaseURL is the base URL for the primary backend that provides client responses.
	PrimaryBaseURL *url.URL

	// ShadowBaseURL is the base URL for the shadow backend where traffic is mirrored.
	ShadowBaseURL *url.URL

	// ShadowRPS defines the maximum requests per second for the shadow backend (0 disables the limit).
	ShadowRPS float64

	// MaxBackendBodyBytes limits the size of the request body sent to backends.
	MaxBackendBodyBytes int64

	// ListenAddr is the address the proxy listens on (e.g., ":8443").
	ListenAddr string

	// TLSCertFile path to the TLS certificate file for the proxy server.
	TLSCertFile string

	// TLSKeyFile path to the TLS key file for the proxy server.
	TLSKeyFile string

	// ShadowForceHeader is the header name that (if present) forces shadowing.
	ShadowForceHeader string

	// RootCAPath is the path to a common CA certificate for all upstream backends.
	RootCAPath string

	// PrimaryRootCA path to the CA certificate specifically for the primary backend.
	PrimaryRootCA string

	// ShadowRootCA path to the CA certificate specifically for the shadow backend.
	ShadowRootCA string

	// PrimaryWorkers number of parallel workers for the primary backend.
	PrimaryWorkers int

	// ShadowWorkers number of parallel workers for the shadow backend.
	ShadowWorkers int

	// PrimaryQueueLen maximum queue size for primary requests.
	PrimaryQueueLen int

	// ShadowQueueLen maximum queue size for shadow requests.
	ShadowQueueLen int

	// ShadowSamplePercent percentage of traffic mirrored to the shadow backend (0-100).
	ShadowSamplePercent int

	// ShadowBurst maximum number of tokens in the token bucket (burst capacity).
	ShadowBurst int

	// ShadowTimeout time limit for requests to the shadow backend.
	ShadowTimeout time.Duration

	// ForwardResponseHeaders list of headers passed from the primary backend to the client.
	ForwardResponseHeaders []string

	// CompareHeaders list of headers compared between primary and shadow backends.
	CompareHeaders []string

	// CompareMode selects the comparison mode (nginx, header, json, html).
	CompareMode string

	// JSONStrict controls strict JSON comparison behavior.
	JSONStrict bool

	// HTMLSimilarityThreshold defines the required similarity for HTML comparison (0.0-1.0).
	HTMLSimilarityThreshold float64

	// LogSessionOnlyOnDiff controls whether session headers are logged only when there are differences.
	LogSessionOnlyOnDiff bool

	// InsecureUpstream allows insecure TLS connections (no verification) to the backends.
	InsecureUpstream bool
}

// Load loads the configuration from environment variables and sets default values.
func Load() (Config, error) {
	primaryURL, err := parseURL(getenv("PRIMARY", "https://127.0.0.1:9001"))
	if err != nil {
		return Config{}, fmt.Errorf("invalid PRIMARY URL: %w", err)
	}

	shadowURL, err := parseURL(getenv("SHADOW", "https://127.0.0.1:9002"))
	if err != nil {
		return Config{}, fmt.Errorf("invalid SHADOW URL: %w", err)
	}

	cfg := Config{
		ListenAddr: getenv("LISTEN", ":8443"),

		TLSCertFile: getenv("TLS_CERT", ""),
		TLSKeyFile:  getenv("TLS_KEY", ""),

		PrimaryBaseURL: primaryURL,
		ShadowBaseURL:  shadowURL,

		PrimaryWorkers:  getenvInt("PRIMARY_WORKERS", 32),
		ShadowWorkers:   getenvInt("SHADOW_WORKERS", 16),
		PrimaryQueueLen: getenvInt("PRIMARY_QUEUE", 4096),
		ShadowQueueLen:  getenvInt("SHADOW_QUEUE", 4096),

		ShadowTimeout: getenvDuration("SHADOW_TIMEOUT", 150*time.Millisecond),

		ShadowSamplePercent: getenvInt("SHADOW_SAMPLE_PERCENT", 5),
		ShadowForceHeader:   getenv("SHADOW_FORCE_HEADER", "X-Shadow"),

		ShadowRPS:   getenvFloat("SHADOW_RPS", 200),
		ShadowBurst: getenvInt("SHADOW_BURST", 400),

		ForwardResponseHeaders: []string{
			"Auth-Status",
			"Auth-Server",
			"Auth-Port",
			"Auth-User",
			"Auth-Pass",
			"Auth-Error",
			"Auth-Wait",
			"Auth-Protocol",
			"X-Nauthilus-Session",
		},
		CompareHeaders: []string{
			"Auth-Status",
			"Auth-Server",
			"Auth-Port",
			"Auth-User",
			"Auth-Error",
			"X-Nauthilus-Session",
		},

		CompareMode:             getenv("COMPARE_MODE", "nginx"),
		JSONStrict:              getenvBool("COMPARE_JSON_STRICT", false),
		HTMLSimilarityThreshold: getenvFloat("COMPARE_HTML_THRESHOLD", 0.99),

		LogSessionOnlyOnDiff: getenvBool("LOG_SESSION_ONLY_ON_DIFF", true),
		MaxBackendBodyBytes:  32 * 1024,

		RootCAPath:       getenv("ROOT_CA", ""),
		PrimaryRootCA:    getenv("PRIMARY_ROOT_CA", ""),
		ShadowRootCA:     getenv("SHADOW_ROOT_CA", ""),
		InsecureUpstream: getenvBool("INSECURE_UPSTREAM", false),
	}

	if cfg.ShadowSamplePercent < 0 {
		cfg.ShadowSamplePercent = 0
	}

	if cfg.ShadowSamplePercent > 100 {
		cfg.ShadowSamplePercent = 100
	}

	if cfg.ShadowBurst < 1 && cfg.ShadowRPS > 0 {
		cfg.ShadowBurst = 1
	}

	mode := strings.ToLower(strings.TrimSpace(cfg.CompareMode))
	if mode == "" || mode == "header" || mode == "nxinx" {
		mode = "nginx"
	}
	cfg.CompareMode = mode

	if cfg.HTMLSimilarityThreshold < 0 {
		cfg.HTMLSimilarityThreshold = 0
	}
	if cfg.HTMLSimilarityThreshold > 1 {
		cfg.HTMLSimilarityThreshold = 1
	}

	return cfg, nil
}

// parseURL parses a string as a URL and ensures that scheme and host are present.
func parseURL(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil {
		return nil, err
	}

	if u.Scheme == "" || u.Host == "" {
		return nil, errors.New("base url must include scheme and host, e.g. https://127.0.0.1:9001")
	}

	return u, nil
}

// getenv reads an environment variable or returns a default value.
func getenv(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}

	return v
}

// getenvInt reads an environment variable as an integer or returns a default value.
func getenvInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}

	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}

	return n
}

// getenvFloat reads an environment variable as a float64 or returns a default value.
func getenvFloat(key string, def float64) float64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}

	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}

	return f
}

// getenvBool reads an environment variable as a boolean (supports various formats like true, 1, yes).
func getenvBool(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}

	switch strings.ToLower(v) {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return def
	}
}

// getenvDuration reads an environment variable as a time duration.
func getenvDuration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}

	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}

	return d
}
