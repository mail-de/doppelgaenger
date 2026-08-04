package grpcproxy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"doppelgaenger/internal/config"
)

const (
	testAccessToken          = "access-token"
	testBasicToken           = "basic-token"
	testDirectToken          = "direct-token"
	testEnvSecret            = "env-secret"
	testExpectedBearerToken  = "Bearer access-token"
	testOIDCDiscoveryPath    = "/.well-known/openid-configuration"
	testOIDCTokenPath        = "/oidc/token"
	testServiceClient        = "service-client"
	testServiceSecret        = "service-secret"
	testAuthMethodBasic      = "client_secret_basic"
	testAuthMethodPost       = "client_secret_post"
	testClientSecretFormKey  = "client_secret"
	testOIDCSecretEnv        = "OIDC_CLIENT_SECRET"
	testTokenContentTypeForm = "application/x-www-form-urlencoded"
)

func TestClientCredentialsTokenSourceUsesClientSecretPostAndCachesToken(t *testing.T) {
	var requests atomic.Int64

	server := startClientSecretPostOIDCServer(t, &requests)
	defer server.Close()

	source := newTestTokenSource(server, config.GRPCBackendOIDCAuthConfig{
		ConfigurationURI: server.URL + testOIDCDiscoveryPath,
		ClientID:         testServiceClient,
		ClientSecret:     testServiceSecret,
		AuthMethod:       testAuthMethodPost,
		Scopes:           []string{"example:authenticate", "example:list_accounts"},
	})

	authorization, err := source.Authorization(context.Background())
	if err != nil {
		t.Fatalf("expected authorization to succeed, got %v", err)
	}

	if authorization != testExpectedBearerToken {
		t.Fatalf("expected bearer authorization, got %q", authorization)
	}

	authorization, err = source.Authorization(context.Background())
	if err != nil {
		t.Fatalf("expected cached authorization to succeed, got %v", err)
	}

	if authorization != testExpectedBearerToken {
		t.Fatalf("expected cached bearer authorization, got %q", authorization)
	}

	if got := requests.Load(); got != 1 {
		t.Fatalf("expected token endpoint to be called once, got %d", got)
	}
}

func startClientSecretPostOIDCServer(t *testing.T, requests *atomic.Int64) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case testOIDCDiscoveryPath:
			_ = json.NewEncoder(w).Encode(discoveryResponse{TokenEndpoint: "http://" + r.Host + testOIDCTokenPath})
			return
		case testOIDCTokenPath:
			requests.Add(1)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}

		if r.Method != http.MethodPost {
			t.Fatalf("expected POST token request, got %s", r.Method)
		}

		if got := r.Header.Get("Content-Type"); got != testTokenContentTypeForm {
			t.Fatalf("expected form content type, got %q", got)
		}

		if err := r.ParseForm(); err != nil {
			t.Fatalf("failed to parse token request form: %v", err)
		}

		assertFormValue(t, r.Form, oidcGrantTypeClientCredentialsKey, oidcGrantTypeClientCredentials)
		assertFormValue(t, r.Form, "client_id", testServiceClient)
		assertFormValue(t, r.Form, testClientSecretFormKey, testServiceSecret)
		assertFormValue(t, r.Form, "scope", "example:authenticate example:list_accounts")

		if _, _, ok := r.BasicAuth(); ok {
			t.Fatalf("did not expect basic auth for %s", testAuthMethodPost)
		}

		_ = json.NewEncoder(w).Encode(tokenEndpointResponse{
			AccessToken: testAccessToken,
			TokenType:   "Bearer",
			ExpiresIn:   3600,
		})
	}))

	return server
}

func TestClientCredentialsTokenSourceUsesClientSecretBasic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == testOIDCDiscoveryPath {
			_ = json.NewEncoder(w).Encode(discoveryResponse{TokenEndpoint: "http://" + r.Host + testOIDCTokenPath})
			return
		}

		if r.URL.Path != testOIDCTokenPath {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}

		username, password, ok := r.BasicAuth()
		if !ok {
			t.Fatalf("expected %s authorization", testAuthMethodBasic)
		}

		if username != testServiceClient || password != testServiceSecret {
			t.Fatalf("unexpected basic auth credentials %q/%q", username, password)
		}

		expectedAuthorization := "Basic " + base64.StdEncoding.EncodeToString([]byte(testServiceClient+":"+testServiceSecret))
		if got := r.Header.Get("Authorization"); got != expectedAuthorization {
			t.Fatalf("unexpected authorization header %q", got)
		}

		if err := r.ParseForm(); err != nil {
			t.Fatalf("failed to parse token request form: %v", err)
		}

		if got := r.Form.Get(testClientSecretFormKey); got != "" {
			t.Fatalf("did not expect %s in form for %s, got %q", testClientSecretFormKey, testAuthMethodBasic, got)
		}

		_ = json.NewEncoder(w).Encode(tokenEndpointResponse{AccessToken: testBasicToken, ExpiresIn: 3600})
	}))
	defer server.Close()

	source := newTestTokenSource(server, config.GRPCBackendOIDCAuthConfig{
		ConfigurationURI: server.URL + testOIDCDiscoveryPath,
		ClientID:         testServiceClient,
		ClientSecret:     testServiceSecret,
		AuthMethod:       testAuthMethodBasic,
	})

	authorization, err := source.Authorization(context.Background())
	if err != nil {
		t.Fatalf("expected authorization to succeed, got %v", err)
	}

	if authorization != "Bearer "+testBasicToken {
		t.Fatalf("expected bearer authorization, got %q", authorization)
	}
}

func TestClientCredentialsTokenSourceUsesConfiguredTokenEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != testOIDCTokenPath {
			t.Fatalf("expected direct token endpoint request, got %s", r.URL.Path)
		}

		_ = json.NewEncoder(w).Encode(tokenEndpointResponse{AccessToken: testDirectToken, ExpiresIn: 3600})
	}))
	defer server.Close()

	source := newTestTokenSource(server, config.GRPCBackendOIDCAuthConfig{
		ConfigurationURI: server.URL + testOIDCDiscoveryPath,
		TokenEndpoint:    server.URL + testOIDCTokenPath,
		ClientID:         testServiceClient,
		ClientSecret:     testServiceSecret,
		AuthMethod:       testAuthMethodPost,
	})

	authorization, err := source.Authorization(context.Background())
	if err != nil {
		t.Fatalf("expected authorization to succeed, got %v", err)
	}

	if authorization != "Bearer "+testDirectToken {
		t.Fatalf("expected bearer authorization from direct endpoint, got %q", authorization)
	}
}

func TestClientCredentialsTokenSourceReadsSecretFromEnv(t *testing.T) {
	t.Setenv(testOIDCSecretEnv, testEnvSecret)

	source := &clientCredentialsTokenSource{
		cfg: config.GRPCBackendOIDCAuthConfig{
			ClientID:        testServiceClient,
			ClientSecretEnv: testOIDCSecretEnv,
			AuthMethod:      testAuthMethodPost,
		},
	}

	form, secret, err := source.tokenRequestForm()
	if err != nil {
		t.Fatalf("expected token request form, got %v", err)
	}

	if secret != testEnvSecret {
		t.Fatalf("expected secret from env, got %q", secret)
	}

	assertFormValue(t, form, testClientSecretFormKey, testEnvSecret)
}

func newTestTokenSource(server *httptest.Server, cfg config.GRPCBackendOIDCAuthConfig) *clientCredentialsTokenSource {
	cfg.Timeout = time.Second
	cfg.RefreshSkew = time.Second

	return &clientCredentialsTokenSource{
		cfg:    cfg,
		client: server.Client(),
		now:    time.Now,
	}
}

func assertFormValue(t *testing.T, form url.Values, key string, expected string) {
	t.Helper()

	if got := form.Get(key); got != expected {
		t.Fatalf("expected form %s=%q, got %q", key, expected, got)
	}
}
