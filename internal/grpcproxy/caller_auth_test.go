package grpcproxy

import (
	"context"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"doppelgaenger/internal/config"
	"doppelgaenger/internal/observability"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The unknown-service boundary must reject before fetching a service token.
func TestBackendOIDCRequiresCallerBeforeForwarding(t *testing.T) {
	service := &testPrimaryService{}
	handler := testProxyHandler(newTestClientConn(t, startTestPrimary(t, service)))
	handler.cfg.GRPCBackendOIDCAuth.Enabled = true
	handler.primaryBearer = staticBearerTokenSource{authorization: testDynamicAuthorization}
	conn := newTestClientConn(t, startTestProxyWithHandler(t, handler))

	var response rawMessage

	err := conn.Invoke(context.Background(), testFullMethodUnary, rawMessage("request"), &response)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}

	if len(service.unaryRequests()) != 0 {
		t.Fatal("unauthenticated caller reached backend")
	}
}

func TestCallerIntrospection(t *testing.T) {
	t.Setenv("TEST_INTROSPECTION_SECRET", "test-secret")

	issuer := httptest.NewTLSServer(testIntrospectionEndpoint(t))
	defer issuer.Close()

	for _, tc := range []struct {
		name   string
		values []string
		code   codes.Code
	}{
		{testCallerMissing, nil, codes.Unauthenticated},
		{testCallerInvalid, []string{"Bearer invalid"}, codes.Unauthenticated},
		{testCallerExpired, []string{"Bearer expired"}, codes.Unauthenticated},
		{testCallerFuture, []string{"Bearer future"}, codes.Unauthenticated},
		{testCallerScopeKey, []string{"Bearer foreign-scope"}, codes.PermissionDenied},
		{"audience", []string{"Bearer foreign-audience"}, codes.Unauthenticated},
		{"issuer", []string{"Bearer foreign-issuer"}, codes.Unauthenticated},
		{"expiry-required", []string{"Bearer no-expiry"}, codes.Unauthenticated},
		{testCallerUnavailable, []string{"Bearer unavailable"}, codes.Unauthenticated},
		{testCallerMalformed, []string{"Bearer malformed"}, codes.Unauthenticated},
		{"duplicate", []string{testCallerBearer, testCallerBearer}, codes.Unauthenticated},
		{"combined", []string{"Bearer valid,Bearer valid"}, codes.Unauthenticated},
		{"empty", []string{"Bearer "}, codes.Unauthenticated},
		{"valid", []string{"bEaReR valid"}, codes.OK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runCallerIntrospectionCase(t, issuer, tc.values, tc.code)
		})
	}
}

func runCallerIntrospectionCase(t *testing.T, issuer *httptest.Server, values []string, want codes.Code) {
	t.Helper()

	service := &testPrimaryService{}
	handler := testProxyHandler(newTestClientConn(t, startTestPrimary(t, service)))
	handler.cfg.GRPCBackendOIDCAuth.Enabled = true
	handler.cfg.GRPCCallerAuth = testCallerConfig(issuer.URL)
	handler.callerHTTP = issuer.Client()
	calls := &atomic.Int64{}
	handler.primaryBearer = countingBearer{calls: calls}
	conn := newTestClientConn(t, startTestProxyWithHandler(t, handler))
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.MD{bearerAuthHeader: values})

	var response rawMessage

	err := conn.Invoke(ctx, testFullMethodUnary, rawMessage("request"), &response)
	if status.Code(err) != want {
		t.Fatalf("expected %s, got %v", want, err)
	}

	if want != codes.OK {
		if calls.Load() != 0 || len(service.unaryRequests()) != 0 {
			t.Fatal("rejected caller caused privileged work")
		}

		return
	}

	if calls.Load() != 1 || len(service.unaryRequests()) != 1 {
		t.Fatal("valid caller was not forwarded")
	}

	assertMetadataValue(t, service.lastMetadata(), bearerAuthHeader, testDynamicAuthorization)
}

func testCallerConfig(endpoint string) config.GRPCCallerAuthConfig {
	return config.GRPCCallerAuthConfig{Mode: callerIntrospectionMode, IntrospectionEndpoint: endpoint, Issuer: testCallerIssuer, Audience: testCallerAudience, ClientID: testCallerAudience, ClientSecretEnv: "TEST_INTROSPECTION_SECRET", MethodScopes: []config.GRPCCallerMethodScopes{{Method: testFullMethodUnary, Scopes: []string{testCallerScope}}}}
}

type countingBearer struct{ calls *atomic.Int64 }

func (b countingBearer) Authorization(context.Context) (string, error) {
	b.calls.Add(1)
	return testDynamicAuthorization, nil
}

func TestCallerLegacyPassthrough(t *testing.T) {
	service := &testPrimaryService{}
	handler := testProxyHandler(newTestClientConn(t, startTestPrimary(t, service)))
	handler.cfg.GRPCBackendOIDCAuth.Enabled = true
	handler.cfg.GRPCCallerAuth.AllowUnauthenticated = true
	handler.primaryBearer = staticBearerTokenSource{authorization: testDynamicAuthorization}
	logger, logs := newCaptureLogger()
	handler.logger = logger

	server, err := newGRPCServer(handler.cfg, handler)
	if err != nil {
		t.Fatal(err)
	}

	server.Stop()

	if len(logs.records) == 0 {
		t.Fatal("missing migration warning")
	}

	conn := newTestClientConn(t, startTestProxyWithHandler(t, handler))

	var response rawMessage
	if err := conn.Invoke(context.Background(), testFullMethodUnary, rawMessage("request"), &response); err != nil {
		t.Fatal(err)
	}

	assertMetadataValue(t, service.lastMetadata(), bearerAuthHeader, testDynamicAuthorization)
}

func TestCallerMTLSRequiresVerifiedChain(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state tls.ConnectionState
		want  codes.Code
	}{
		{testCallerMissing, tls.ConnectionState{}, codes.Unauthenticated},
		{"unverified", tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}, codes.Unauthenticated},
		{"verified", tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{}}}}, codes.OK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: tc.state}})
			if err := authenticateMTLS(ctx); status.Code(err) != tc.want {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestCallerMethodAllowlist(t *testing.T) {
	c := introspectionClaims{Active: true, Issuer: testCallerIssuer, Audience: json.RawMessage(`"proxy"`), Scope: testCallerScope, Expires: time.Now().Add(time.Minute).Unix()}

	a := testCallerConfig("https://issuer.example/introspect")
	if err := c.authorize(a, testFullMethodBidiStream, time.Now().Unix()); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("unlisted method accepted: %v", err)
	}
}

func testIntrospectionEndpoint(t *testing.T) http.Handler {
	t.Helper()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkIntrospectionCredentials(t, r)

		token := r.FormValue("token")
		claims := map[string]any{"active": true, "iss": testCallerIssuer, "aud": []string{testCallerAudience}, testCallerScopeKey: testCallerScope, "exp": time.Now().Add(time.Minute).Unix()}

		switch token {
		case testCallerInvalid:
			claims["active"] = false
		case testCallerExpired:
			claims["exp"] = time.Now().Add(-time.Minute).Unix()
		case testCallerFuture:
			claims["nbf"] = time.Now().Add(time.Minute).Unix()
		case "foreign-scope":
			claims[testCallerScopeKey] = "rpc:write"
		case "foreign-audience":
			claims["aud"] = "elsewhere"
		case "foreign-issuer":
			claims["iss"] = "https://other.example"
		case "no-expiry":
			delete(claims, "exp")
		case testCallerUnavailable:
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		case testCallerMalformed:
			_, _ = w.Write([]byte("not-json"))
			return
		}

		_ = json.NewEncoder(w).Encode(claims)
	})
}

const (
	testCallerAudience    = "proxy"
	testCallerIssuer      = "https://issuer.example"
	testCallerInvalid     = "invalid"
	testCallerExpired     = "expired"
	testCallerFuture      = "future"
	testCallerUnavailable = "unavailable"
	testCallerMalformed   = "malformed"
	testCallerMissing     = "missing"
	testCallerBearer      = "Bearer valid"
)

func TestCallerRejectsBeforeShadowAndStreaming(t *testing.T) {
	primary, shadow := &testPrimaryService{}, &testPrimaryService{}
	cfg := testShadowConfig(100)
	cfg.GRPCBackendOIDCAuth.Enabled = true

	conn, _ := startPrimaryShadowProxy(t, primary, shadow, cfg)
	for _, method := range []string{testFullMethodUnary, testFullMethodServerStream, testFullMethodClientStream, testFullMethodBidiStream} {
		stream := newClientStream(t, conn, method, genericStreamDesc())

		var response rawMessage

		err := stream.RecvMsg(&response)
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("method %s got %v", method, err)
		}
	}

	if len(primary.unaryRequests())+len(shadow.unaryRequests())+len(primary.bidiRequests())+len(shadow.bidiRequests()) != 0 {
		t.Fatal("rejected caller forwarded")
	}
}

const testCallerScopeKey = "scope"
const testCallerScope = "rpc:read"

func checkIntrospectionCredentials(t *testing.T, r *http.Request) {
	t.Helper()

	user, secret, ok := r.BasicAuth()
	if !ok || user != testCallerAudience || secret != "test-secret" {
		t.Error("missing introspection credentials")
	}
}

func TestCallerMTLSHandshake(t *testing.T) {
	cert, ca, clientPEM, keyPEM := newTestPKI(t)
	service := &testPrimaryService{}
	handler := testProxyHandler(newTestClientConn(t, startTestPrimary(t, service)))
	handler.cfg.GRPCBackendOIDCAuth.Enabled = true
	handler.cfg.GRPCCallerAuth.Mode = "mtls"
	handler.cfg.GRPCTLS = testCallerTLSConfig(t, cert, ca)
	handler.primaryBearer = staticBearerTokenSource{authorization: testDynamicAuthorization}

	server, err := newGRPCServer(handler.cfg, handler)
	if err != nil {
		t.Fatal(err)
	}

	listener := newLocalListener(t)
	go serveTestGRPC(t, server, listener)

	t.Cleanup(server.Stop)

	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca)

	clientCert, err := tls.X509KeyPair(clientPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}

	for _, hasCert := range []bool{false, true} {
		clientTLS := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
		if hasCert {
			clientTLS.Certificates = []tls.Certificate{clientCert}
		}

		conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(clientTLS)), grpc.WithDefaultCallOptions(grpc.ForceCodec(rawCodec{})))
		if err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), testShortTimeout)

		var response rawMessage

		err = conn.Invoke(ctx, testFullMethodUnary, rawMessage("request"), &response)

		cancel()

		_ = conn.Close()

		if (err == nil) != hasCert {
			t.Fatalf("client certificate=%v: %v", hasCert, err)
		}
	}

	if len(service.unaryRequests()) != 1 {
		t.Fatal("unexpected mTLS forwarding count")
	}

	assertMetadataValue(t, service.lastMetadata(), bearerAuthHeader, testDynamicAuthorization)
}

func TestCallerRejectionTelemetry(t *testing.T) {
	logger, logs := newCaptureLogger()

	obs, err := observability.New(config.Config{Observability: config.ObservabilityConfig{PrometheusEnabled: true}}, "test", logger)
	if err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(config.Config{GRPCBackendOIDCAuth: config.GRPCBackendOIDCAuthConfig{Enabled: true}}, nil, nil, logger, nil, obs)
	conn := newTestClientConn(t, startTestProxyWithHandler(t, handler))
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs(bearerAuthHeader, "Bearer never-log-this-token"))

	var response rawMessage
	if err := conn.Invoke(ctx, testFullMethodUnary, rawMessage("request"), &response); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}

	record := logs.last()
	assertLogField(t, record, "msg", "grpc caller rejected")
	assertLogField(t, record, "reason", codes.Unauthenticated.String())

	if strings.Contains(fmt.Sprint(record), "never-log-this-token") {
		t.Fatal("token leaked to log")
	}

	recorder := httptest.NewRecorder()
	obs.PrometheusHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body := recorder.Body.String()
	if !strings.Contains(body, `outcome="caller_auth_rejected"`) || !strings.Contains(body, "doppelgaenger_ingress_requests_total") {
		t.Fatal("missing rejection metric")
	}

	if strings.Contains(body, "never-log-this-token") {
		t.Fatal("token leaked to metric")
	}
}

func TestCallerIntrospectionTransportFailures(t *testing.T) {
	t.Setenv("TEST_INTROSPECTION_SECRET", "test-secret")

	var redirected atomic.Int64

	for _, kind := range []string{testCallerRedirect, testCallerOversized, shadowSkipReasonTimeout} {
		t.Run(kind, func(t *testing.T) {
			issuer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/leak" {
					redirected.Add(1)
					return
				}

				switch kind {
				case testCallerRedirect:
					http.Redirect(w, r, "/leak", http.StatusTemporaryRedirect)
				case testCallerOversized:
					_, _ = w.Write([]byte(strings.Repeat(" ", 65537)))
				case shadowSkipReasonTimeout:
					time.Sleep(100 * time.Millisecond)
				}
			}))
			defer issuer.Close()

			a := testCallerConfig(issuer.URL)
			a.Timeout = 20 * time.Millisecond
			handler := &Handler{cfg: config.Config{GRPCCallerAuth: a}, callerHTTP: issuer.Client()}
			handler.callerHTTP.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

			ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(bearerAuthHeader, testCallerBearer))
			if err := handler.authenticateCaller(ctx, testFullMethodUnary); status.Code(err) != codes.Unauthenticated {
				t.Fatalf("got %v", err)
			}
		})
	}

	if redirected.Load() != 0 {
		t.Fatal("introspection followed redirect")
	}
}

func testCallerTLSConfig(t *testing.T, cert tls.Certificate, ca []byte) config.GRPCTLSConfig {
	t.Helper()

	return config.GRPCTLSConfig{
		Enabled: true, RequireClientCert: true, ClientCA: writeTestCA(t, ca),
		Cert: writeTestPEM(t, "server.pem", pem.EncodeToMemory(&pem.Block{Type: certificatePEMBlockType, Bytes: cert.Certificate[0]})),
		Key:  writeTestPEM(t, "server-key.pem", pem.EncodeToMemory(&pem.Block{Type: privateKeyPEMBlockType, Bytes: x509.MarshalPKCS1PrivateKey(cert.PrivateKey.(*rsa.PrivateKey))})),
	}
}

const testCallerOversized = "oversized"

const testCallerRedirect = "redirect"
