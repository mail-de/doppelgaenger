package config

import (
	"errors"
	"net/url"
	"strings"
	"time"
)

const grpcCallerIntrospectionMode = "introspection"

// GRPCCallerAuthConfig protects the inbound RPC before any backend credentials are used.
type GRPCCallerAuthConfig struct {
	CAFile                string                   `mapstructure:"ca_file"`
	ServerName            string                   `mapstructure:"server_name"`
	MinTLSVersion         string                   `mapstructure:"min_tls_version"`
	Mode                  string                   `mapstructure:"mode"`
	AllowUnauthenticated  bool                     `mapstructure:"allow_unauthenticated"`
	IntrospectionEndpoint string                   `mapstructure:"introspection_endpoint"`
	Issuer                string                   `mapstructure:"issuer"`
	Audience              string                   `mapstructure:"audience"`
	ClientID              string                   `mapstructure:"client_id"`
	ClientSecretEnv       string                   `mapstructure:"client_secret_env"`
	RequiredScopes        []string                 `mapstructure:"required_scopes"`
	MethodScopes          []GRPCCallerMethodScopes `mapstructure:"method_scopes"`
	Timeout               time.Duration            `mapstructure:"timeout"`
	// IntrospectionCacheTTL bounds how long a positive introspection result is reused; zero disables caching.
	IntrospectionCacheTTL time.Duration `mapstructure:"introspection_cache_ttl"`
	// IntrospectionCacheMaxEntries bounds the number of cached token digests.
	IntrospectionCacheMaxEntries int `mapstructure:"introspection_cache_max_entries"`
	// IntrospectionMaxConcurrent bounds concurrent introspection requests; zero uses the default.
	IntrospectionMaxConcurrent int `mapstructure:"introspection_max_concurrent"`
}

// DefaultGRPCIntrospectionMaxConcurrent applies when introspection_max_concurrent is zero.
const DefaultGRPCIntrospectionMaxConcurrent = 64

// MaxGRPCIntrospectionCacheEntries caps the positive cache at about 300 MiB,
// assuming typical claims of at most 300 bytes per entry.
const MaxGRPCIntrospectionCacheEntries = 1000000

// EffectiveIntrospectionMaxConcurrent returns the concurrency limit after defaults.
func (a GRPCCallerAuthConfig) EffectiveIntrospectionMaxConcurrent() int {
	if a.IntrospectionMaxConcurrent <= 0 {
		return DefaultGRPCIntrospectionMaxConcurrent
	}

	return a.IntrospectionMaxConcurrent
}

// GRPCCallerMethodScopes maps an exact, case-sensitive RPC path to its required scopes.
type GRPCCallerMethodScopes struct {
	Method string   `mapstructure:"method"`
	Scopes []string `mapstructure:"scopes"`
}

// ValidateGRPCCallerAuth also guards callers constructing Config without the loader.
func ValidateGRPCCallerAuth(cfg Config) error {
	a := cfg.GRPCCallerAuth
	if a.AllowUnauthenticated {
		if a.Mode != "" {
			return errors.New("grpc_caller_auth: allow_unauthenticated cannot be combined with mode")
		}

		return nil
	}

	switch a.Mode {
	case "":
		if cfg.GRPCBackendOIDCAuth.Enabled {
			return errors.New("grpc_caller_auth.mode is required when grpc_backend_oidc_auth is enabled")
		}
	case "mtls":
		if !cfg.GRPCTLS.Enabled || !cfg.GRPCTLS.RequireClientCert || cfg.GRPCTLS.ClientCA == "" {
			return errors.New("grpc_caller_auth mtls requires grpc_tls enabled, client_ca and require_client_cert")
		}
	case grpcCallerIntrospectionMode:
		return validateGRPCIntrospection(a)
	default:
		return errors.New("grpc_caller_auth.mode must be introspection or mtls")
	}

	return nil
}

func validIntrospectionURL(endpoint string) bool {
	u, err := url.Parse(endpoint)
	return err == nil && u.Scheme == grpcHTTPScheme && u.Host != "" && u.User == nil && u.Fragment == "" && u.RawQuery == ""
}

func validateGRPCIntrospection(a GRPCCallerAuthConfig) error {
	if !validIntrospectionURL(a.IntrospectionEndpoint) {
		return errors.New("grpc_caller_auth.introspection_endpoint must be an HTTPS URL without credentials, query or fragment")
	}

	if a.Issuer == "" || a.Audience == "" || a.ClientID == "" || a.ClientSecretEnv == "" {
		return errors.New("grpc_caller_auth introspection requires issuer, audience, client_id and client_secret_env")
	}

	if _, err := ResolveTLSMinVersion(a.MinTLSVersion); err != nil {
		return err
	}

	if a.Timeout < 0 {
		return errors.New("grpc_caller_auth.timeout must not be negative")
	}

	if err := validateIntrospectionCache(a); err != nil {
		return err
	}

	if err := validateCallerMethods(a.MethodScopes); err != nil {
		return err
	}

	return validateCallerScopes(a.RequiredScopes)
}

func validateIntrospectionCache(a GRPCCallerAuthConfig) error {
	if a.IntrospectionCacheTTL < 0 {
		return errors.New("grpc_caller_auth.introspection_cache_ttl must not be negative")
	}

	if a.IntrospectionCacheMaxEntries < 0 || a.IntrospectionCacheMaxEntries > MaxGRPCIntrospectionCacheEntries {
		return errors.New("grpc_caller_auth.introspection_cache_max_entries must be between 0 and 1000000")
	}

	if a.IntrospectionMaxConcurrent < 0 {
		return errors.New("grpc_caller_auth.introspection_max_concurrent must not be negative")
	}

	if a.IntrospectionCacheTTL > 0 && a.IntrospectionCacheMaxEntries == 0 {
		return errors.New("grpc_caller_auth.introspection_cache_max_entries must be positive when introspection_cache_ttl is enabled")
	}

	return nil
}

func validateCallerMethods(methods []GRPCCallerMethodScopes) error {
	seen := map[string]bool{}

	for _, rule := range methods {
		method, scopes := rule.Method, rule.Scopes
		if seen[method] {
			return errors.New("grpc_caller_auth.method_scopes contains duplicate methods")
		}

		seen[method] = true

		parts := strings.Split(method, "/")
		if len(parts) != 3 || parts[0] != "" || parts[1] == "" || parts[2] == "" || len(scopes) == 0 {
			return errors.New("grpc_caller_auth.method_scopes requires exact /service/method keys and nonempty scope lists")
		}

		if err := validateCallerScopes(scopes); err != nil {
			return err
		}
	}

	return nil
}

func validateCallerScopes(scopes []string) error {
	for _, scope := range scopes {
		if scope == "" || strings.ContainsAny(scope, " \t\r\n") {
			return errors.New("grpc_caller_auth scopes must be nonempty individual scope names")
		}
	}

	return nil
}
