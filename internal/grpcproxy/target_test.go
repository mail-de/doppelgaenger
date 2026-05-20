package grpcproxy

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"doppelgaenger/internal/config"
)

const (
	certificatePEMBlockType = "CERTIFICATE"
	privateKeyPEMBlockType  = "RSA PRIVATE KEY"
)

func TestTargetPoolSupportsTLSUpstream(t *testing.T) {
	service := newTestPrimaryService()
	certificate, caPEM := newTestCertificate(t)
	primaryAddr := startTestTLSPrimary(t, service, certificate)
	caPath := writeTestCA(t, caPEM)
	cfg := testTLSPoolConfig(primaryAddr, caPath)

	pools, err := NewTargetPools(cfg)
	if err != nil {
		t.Fatalf("expected TLS target pool to build, got %v", err)
	}

	t.Cleanup(func() {
		_ = pools.Close()
	})

	handler := NewHandler(cfg, &Resolver{}, pools, discardLogger(), nil, nil)
	proxyAddr := startTestProxyWithHandler(t, handler)
	conn := newTestClientConn(t, proxyAddr)
	ctx, cancel := context.WithTimeout(context.Background(), testShortTimeout)
	t.Cleanup(cancel)

	var response rawMessage
	if err := conn.Invoke(ctx, testFullMethodUnary, rawMessage("tls"), &response); err != nil {
		t.Fatalf("expected unary call through TLS upstream to succeed, got %v", err)
	}

	assertRawMessage(t, response, "primary:tls")
}

func TestTargetPoolSupportsMTLSUpstream(t *testing.T) {
	service := newTestPrimaryService()
	serverCert, caPEM, clientCertPEM, clientKeyPEM := newTestPKI(t)
	primaryAddr := startTestMTLSPrimary(t, service, serverCert, caPEM)
	caPath := writeTestPEM(t, "ca.pem", caPEM)
	clientCertPath := writeTestPEM(t, "client.pem", clientCertPEM)
	clientKeyPath := writeTestPEM(t, "client.key", clientKeyPEM)
	cfg := testTLSPoolConfig(primaryAddr, caPath)
	cfg.PrimaryGRPCTargets[0].TLS.ClientCert = clientCertPath
	cfg.PrimaryGRPCTargets[0].TLS.ClientKey = clientKeyPath

	pools, err := NewTargetPools(cfg)
	if err != nil {
		t.Fatalf("expected mTLS target pool to build, got %v", err)
	}

	t.Cleanup(func() {
		_ = pools.Close()
	})

	handler := NewHandler(cfg, &Resolver{}, pools, discardLogger(), nil, nil)
	proxyAddr := startTestProxyWithHandler(t, handler)
	conn := newTestClientConn(t, proxyAddr)
	ctx, cancel := context.WithTimeout(context.Background(), testShortTimeout)
	t.Cleanup(cancel)

	var response rawMessage
	if err := conn.Invoke(ctx, testFullMethodUnary, rawMessage("mtls"), &response); err != nil {
		t.Fatalf("expected unary call through mTLS upstream to succeed, got %v", err)
	}

	assertRawMessage(t, response, "primary:mtls")
}

func startTestTLSPrimary(t *testing.T, service *testPrimaryService, certificate tls.Certificate) string {
	t.Helper()

	listener := newLocalListener(t)
	server := grpc.NewServer(
		grpc.ForceServerCodec(rawCodec{}),
		grpc.Creds(credentials.NewTLS(&tls.Config{
			Certificates: []tls.Certificate{certificate},
			MinVersion:   tls.VersionTLS12,
		})),
	)
	server.RegisterService(&testServiceDesc, service)

	go serveTestGRPC(t, server, listener)

	t.Cleanup(func() {
		server.Stop()

		_ = listener.Close()
	})

	return listener.Addr().String()
}

func startTestMTLSPrimary(t *testing.T, service *testPrimaryService, certificate tls.Certificate, caPEM []byte) string {
	t.Helper()

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatalf("failed to parse test CA PEM")
	}

	listener := newLocalListener(t)
	server := grpc.NewServer(
		grpc.ForceServerCodec(rawCodec{}),
		grpc.Creds(credentials.NewTLS(&tls.Config{
			Certificates: []tls.Certificate{certificate},
			ClientCAs:    pool,
			ClientAuth:   tls.RequireAndVerifyClientCert,
			MinVersion:   tls.VersionTLS12,
		})),
	)
	server.RegisterService(&testServiceDesc, service)

	go serveTestGRPC(t, server, listener)

	t.Cleanup(func() {
		server.Stop()

		_ = listener.Close()
	})

	return listener.Addr().String()
}

func testTLSPoolConfig(primaryAddr string, caPath string) config.Config {
	return config.Config{
		Protocol:                 ProtocolName,
		PrimaryGRPCSelectionMode: selectionModeRoundRobin,
		PrimaryGRPCTargets: []config.GRPCTarget{
			{
				Name:    "tls-primary",
				Address: primaryAddr,
				TLS: config.GRPCTargetTLS{
					Enabled:    true,
					RootCA:     caPath,
					ServerName: "127.0.0.1",
				},
			},
		},
	}
}

func newTestCertificate(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()

	serverCert, caPEM, _, _ := newTestPKI(t)

	return serverCert, caPEM
}

func newTestPKI(t *testing.T) (tls.Certificate, []byte, []byte, []byte) {
	t.Helper()

	caKey := newTestPrivateKey(t)
	caTemplate := testCertificateTemplate(t, true)

	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("failed to create CA certificate: %v", err)
	}

	serverKey := newTestPrivateKey(t)
	serverTemplate := testCertificateTemplate(t, false)

	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("failed to create server certificate: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: certificatePEMBlockType, Bytes: serverDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: privateKeyPEMBlockType, Bytes: x509.MarshalPKCS1PrivateKey(serverKey)})

	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("failed to parse test server keypair: %v", err)
	}

	clientKey := newTestPrivateKey(t)
	clientTemplate := testCertificateTemplate(t, false)
	clientTemplate.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	clientTemplate.IPAddresses = nil

	clientDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, caTemplate, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("failed to create client certificate: %v", err)
	}

	clientCertPEM := pem.EncodeToMemory(&pem.Block{Type: certificatePEMBlockType, Bytes: clientDER})
	clientKeyPEM := pem.EncodeToMemory(&pem.Block{Type: privateKeyPEMBlockType, Bytes: x509.MarshalPKCS1PrivateKey(clientKey)})

	return certificate, pem.EncodeToMemory(&pem.Block{Type: certificatePEMBlockType, Bytes: caDER}), clientCertPEM, clientKeyPEM
}

func newTestPrivateKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate test key: %v", err)
	}

	return key
}

func testCertificateTemplate(t *testing.T, ca bool) *x509.Certificate {
	t.Helper()

	serial, err := rand.Int(rand.Reader, big.NewInt(1_000_000_000))
	if err != nil {
		t.Fatalf("failed to generate certificate serial: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "doppelgaenger-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}

	if ca {
		template.IsCA = true
		template.KeyUsage |= x509.KeyUsageCertSign
	} else {
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	}

	return template
}

func writeTestCA(t *testing.T, caPEM []byte) string {
	t.Helper()

	return writeTestPEM(t, "ca.pem", caPEM)
}

func writeTestPEM(t *testing.T, filename string, pemBytes []byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), filename)
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatalf("failed to write test PEM %s: %v", filename, err)
	}

	return path
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))
}
