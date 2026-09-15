package config

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
)

// AuthClientTLSConfig builds verified TLS for caller introspection and backend OIDC.
// A supplied CA bundle replaces system roots; an empty path uses system roots.
func AuthClientTLSConfig(caFile, serverName, minimum string) (*tls.Config, error) {
	version, err := ResolveTLSMinVersion(minimum)
	if err != nil {
		return nil, err
	}

	result := &tls.Config{MinVersion: version, ServerName: serverName}
	if caFile == "" {
		return result, nil
	}

	data, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("auth TLS ca_file: %w", err)
	}

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(data) {
		return nil, errors.New("auth TLS ca_file must contain PEM certificates")
	}

	result.RootCAs = roots

	return result, nil
}
