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

	if err := validateCallerMethods(a.MethodScopes); err != nil {
		return err
	}

	return validateCallerScopes(a.RequiredScopes)
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
