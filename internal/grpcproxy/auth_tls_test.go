package grpcproxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"doppelgaenger/internal/config"
)

const authTLS13 = "1.3"

const authTLSSecretEnv = "AUTH_TLS_SECRET"

const authTLSServerName = "issuer.example.test"

func TestAuthClientsVerifiedTLS(t *testing.T) {
	server, caFile := authTLSServer(t, 0)
	t.Setenv(authTLSSecretEnv, "test-secret")

	for _, tc := range []struct {
		name, ca, serverName, minimum string
		wantError                     bool
	}{
		{"trusted-dns-certificate", caFile, authTLSServerName, "1.2", false},
		{"tls13", caFile, authTLSServerName, authTLS13, false},
		{"unknown-ca", "", authTLSServerName, "", true},
		{"wrong-name", caFile, "wrong.example.test", "", true},
		{"ip-name", caFile, "", "", true},
		{"missing-ca", filepath.Join(t.TempDir(), "missing.pem"), authTLSServerName, "", true},
		{"invalid-version", caFile, authTLSServerName, "1.1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{
				GRPCCallerAuth: config.GRPCCallerAuthConfig{
					Mode: callerIntrospectionMode, IntrospectionEndpoint: server.URL + "/introspect",
					ClientID: testCallerAudience, ClientSecretEnv: authTLSSecretEnv,
					CAFile: tc.ca, ServerName: tc.serverName, MinTLSVersion: tc.minimum,
				},
				GRPCBackendOIDCAuth: config.GRPCBackendOIDCAuthConfig{
					Enabled: true, ConfigurationURI: server.URL + testOIDCDiscoveryPath,
					ClientID: testCallerAudience, ClientSecretEnv: authTLSSecretEnv,
					CAFile: tc.ca, ServerName: tc.serverName, MinTLSVersion: tc.minimum,
				},
			}
			handler := NewHandler(cfg, nil, nil, nil, nil, nil)

			_, err := handler.introspectCaller(context.Background(), "caller-token")
			if (err != nil) != tc.wantError {
				t.Fatalf("introspection error = %v", err)
			}

			_, err = handler.primaryBearer.Authorization(context.Background())
			if (err != nil) != tc.wantError {
				t.Fatalf("discovery/token error = %v", err)
			}
		})
	}
}

func authTLSServer(t *testing.T, maximum uint16) (*httptest.Server, string) {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}

	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}

	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{authTLSServerName},
		NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}

	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0600); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewUnstartedServer(http.HandlerFunc(authTLSResponse))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, MaxVersion: maximum, Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER}, PrivateKey: caKey}}}
	server.StartTLS()
	t.Cleanup(server.Close)

	return server, caFile
}

func authTLSResponse(w http.ResponseWriter, r *http.Request) {
	if r.TLS.ServerName != authTLSServerName {
		http.Error(w, "missing SNI", http.StatusBadRequest)
		return
	}

	switch r.URL.Path {
	case testOIDCDiscoveryPath:
		_, _ = io.WriteString(w, `{"token_endpoint":"https://`+r.Host+testOIDCTokenPath+`"}`)
	case testOIDCTokenPath:
		_, _ = io.WriteString(w, `{"access_token":"test-token","token_type":"Bearer","expires_in":300}`)
	default:
		_, _ = io.WriteString(w, `{"active":true}`)
	}
}

func TestAuthClientsRejectTLSBelowMinimum(t *testing.T) {
	server, caFile := authTLSServer(t, tls.VersionTLS12)
	t.Setenv(authTLSSecretEnv, "test-secret")
	a := config.GRPCCallerAuthConfig{CAFile: caFile, ServerName: authTLSServerName,
		MinTLSVersion: authTLS13, IntrospectionEndpoint: server.URL, ClientSecretEnv: authTLSSecretEnv}
	handler := &Handler{cfg: config.Config{GRPCCallerAuth: a}}

	_, err := handler.introspectCaller(context.Background(), "caller-token")
	if err == nil {
		t.Fatal("caller accepted TLS below configured minimum")
	}

	source := newBearerTokenSource(config.Config{GRPCBackendOIDCAuth: config.GRPCBackendOIDCAuthConfig{
		Enabled: true, CAFile: caFile, ServerName: authTLSServerName, MinTLSVersion: a.MinTLSVersion,
		TokenEndpoint: server.URL + testOIDCTokenPath, ClientSecretEnv: authTLSSecretEnv,
	}}, nil)

	_, err = source.Authorization(context.Background())
	if err == nil {
		t.Fatal("backend accepted TLS below configured minimum")
	}
}
