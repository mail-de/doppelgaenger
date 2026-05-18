// Package tlsutil builds TLS configuration for upstream connections.
package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// BuildUpstreamTLS creates a TLS config for upstream connections.
func BuildUpstreamTLS(insecure bool, caPath string) (*tls.Config, error) {
	if insecure {
		return &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true,
		}, nil
	}

	if caPath == "" {
		return &tls.Config{MinVersion: tls.VersionTLS12}, nil
	}

	pool, err := readCertPoolFromPEM(caPath)
	if err != nil {
		return nil, err
	}

	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    pool,
	}, nil
}

func readCertPoolFromPEM(path string) (*x509.CertPool, error) {
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	pool := x509.NewCertPool()
	if ok := pool.AppendCertsFromPEM(pemBytes); !ok {
		return nil, fmt.Errorf("failed to parse CA PEM: %s", path)
	}

	return pool, nil
}
