package grpcproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"doppelgaenger/internal/config"
)

const (
	bearerAuthHeader                  = "authorization"
	formContentType                   = "application/x-www-form-urlencoded"
	oidcAuthMethodAuto                = "auto"
	oidcAuthMethodClientSecretBasic   = "client_secret_basic"
	oidcGrantTypeClientCredentials    = "client_credentials"
	oidcGrantTypeClientCredentialsKey = "grant_type"
	tokenTypeBearer                   = "bearer"
)

// BearerTokenSource returns an Authorization metadata value for primary gRPC calls.
type BearerTokenSource interface {
	Authorization(ctx context.Context) (string, error)
}

type clientCredentialsTokenSource struct {
	cfg       config.GRPCBackendOIDCAuthConfig
	client    *http.Client
	initError error
	logger    *slog.Logger
	now       func() time.Time

	mu        sync.Mutex
	token     string
	expiresAt time.Time
	tokenURL  string
}

type tokenEndpointResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
}

type discoveryResponse struct {
	TokenEndpoint string `json:"token_endpoint"`
}

func newBearerTokenSource(cfg config.Config, logger *slog.Logger) BearerTokenSource {
	if !cfg.GRPCBackendOIDCAuth.Enabled {
		return nil
	}

	a := cfg.GRPCBackendOIDCAuth
	tlsConfig, initError := config.AuthClientTLSConfig(a.CAFile, a.ServerName, a.MinTLSVersion)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig

	if a.InsecureTLS {
		if a.CAFile != "" || a.ServerName != "" {
			initError = errors.New("grpc_backend_oidc_auth: insecure_tls cannot be combined with ca_file or server_name")
		}

		if tlsConfig != nil {
			tlsConfig.InsecureSkipVerify = true
		} // Explicit legacy opt-in.

		if logger != nil {
			logger.Warn("grpc_backend_oidc_auth.insecure_tls disables certificate and hostname verification; use ca_file and server_name")
		}
	}

	return &clientCredentialsTokenSource{
		cfg:       cfg.GRPCBackendOIDCAuth,
		initError: initError,
		client: &http.Client{
			Transport: transport,
		},
		logger: logger,
		now:    time.Now,
	}
}

func (s *clientCredentialsTokenSource) Authorization(ctx context.Context) (string, error) {
	if s.initError != nil {
		return "", s.initError
	}

	token, err := s.accessToken(ctx)
	if err != nil {
		return "", err
	}

	return "Bearer " + token, nil
}

func (s *clientCredentialsTokenSource) accessToken(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cachedTokenValid() {
		return s.token, nil
	}

	token, expiresAt, err := s.fetchToken(ctx)
	if err != nil {
		return "", err
	}

	s.token = token
	s.expiresAt = expiresAt

	return s.token, nil
}

func (s *clientCredentialsTokenSource) cachedTokenValid() bool {
	if s.token == "" || s.expiresAt.IsZero() {
		return false
	}

	return s.now().Add(s.cfg.RefreshSkew).Before(s.expiresAt)
}

func (s *clientCredentialsTokenSource) fetchToken(ctx context.Context) (string, time.Time, error) {
	timeout := s.cfg.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	tokenURL, err := s.discoverTokenURL(ctx)
	if err != nil {
		return "", time.Time{}, err
	}

	form, secret, err := s.tokenRequestForm()
	if err != nil {
		return "", time.Time{}, err
	}

	request, err := s.newTokenRequest(ctx, tokenURL, form, secret)
	if err != nil {
		return "", time.Time{}, err
	}

	response, err := s.client.Do(request)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("request token endpoint: %w", err)
	}
	defer s.closeResponseBody(response)

	decoded, err := decodeTokenResponse(response)
	if err != nil {
		return "", time.Time{}, err
	}

	token := strings.TrimSpace(decoded.AccessToken)
	if token == "" {
		return "", time.Time{}, errors.New("token response did not include access_token")
	}

	tokenType := strings.ToLower(strings.TrimSpace(decoded.TokenType))
	if tokenType != "" && tokenType != tokenTypeBearer {
		return "", time.Time{}, fmt.Errorf("token response used unsupported token_type %q", decoded.TokenType)
	}

	expiresAt := s.tokenExpiresAt(decoded.ExpiresIn)
	if s.logger != nil {
		s.logger.Debug("refreshed grpc bearer token", "expires_at", expiresAt)
	}

	return token, expiresAt, nil
}

func (s *clientCredentialsTokenSource) newTokenRequest(ctx context.Context, tokenURL string, form url.Values, secret string) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create token request: %w", err)
	}

	request.Header.Set("Content-Type", formContentType)
	request.Header.Set("Accept", "application/json")
	request.Header.Del("Authorization")

	if s.effectiveAuthMethod() == oidcAuthMethodClientSecretBasic {
		request.SetBasicAuth(s.cfg.ClientID, secret)
	}

	return request, nil
}

func decodeTokenResponse(response *http.Response) (tokenEndpointResponse, error) {
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return tokenEndpointResponse{}, fmt.Errorf("token endpoint returned HTTP %d", response.StatusCode)
	}

	var decoded tokenEndpointResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return tokenEndpointResponse{}, fmt.Errorf("decode token response: %w", err)
	}

	return decoded, nil
}

func (s *clientCredentialsTokenSource) discoverTokenURL(ctx context.Context) (string, error) {
	if s.cfg.TokenEndpoint != "" {
		return s.cfg.TokenEndpoint, nil
	}

	if s.tokenURL != "" {
		return s.tokenURL, nil
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.ConfigurationURI, nil)
	if err != nil {
		return "", fmt.Errorf("create discovery request: %w", err)
	}

	request.Header.Set("Accept", "application/json")

	response, err := s.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("request OIDC discovery document: %w", err)
	}
	defer s.closeResponseBody(response)

	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OIDC discovery returned HTTP %d", response.StatusCode)
	}

	var decoded discoveryResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return "", fmt.Errorf("decode OIDC discovery response: %w", err)
	}

	tokenURL := strings.TrimSpace(decoded.TokenEndpoint)
	if tokenURL == "" {
		return "", errors.New("OIDC discovery response did not include token_endpoint")
	}

	s.tokenURL = tokenURL

	return tokenURL, nil
}

func (s *clientCredentialsTokenSource) tokenRequestForm() (url.Values, string, error) {
	secret := s.cfg.ClientSecret
	if s.cfg.ClientSecretEnv != "" {
		secret = os.Getenv(s.cfg.ClientSecretEnv)
	}

	if strings.TrimSpace(secret) == "" {
		return nil, "", errors.New("grpc bearer token client secret is empty")
	}

	form := url.Values{}
	form.Set(oidcGrantTypeClientCredentialsKey, oidcGrantTypeClientCredentials)

	if len(s.cfg.Scopes) > 0 {
		form.Set("scope", strings.Join(s.cfg.Scopes, " "))
	}

	if s.effectiveAuthMethod() == oidcAuthMethodClientSecretBasic {
		return form, secret, nil
	}

	form.Set("client_id", s.cfg.ClientID)
	form.Set("client_secret", secret)

	return form, secret, nil
}

func (s *clientCredentialsTokenSource) effectiveAuthMethod() string {
	if s.cfg.AuthMethod == "" || s.cfg.AuthMethod == oidcAuthMethodAuto {
		if s.cfg.ClientSecret != "" || s.cfg.ClientSecretEnv != "" {
			return oidcAuthMethodClientSecretBasic
		}
	}

	return s.cfg.AuthMethod
}

func (s *clientCredentialsTokenSource) tokenExpiresAt(expiresIn int64) time.Time {
	if expiresIn <= 0 {
		expiresIn = 60
	}

	return s.now().Add(time.Duration(expiresIn) * time.Second)
}

func (s *clientCredentialsTokenSource) closeResponseBody(response *http.Response) {
	if err := response.Body.Close(); err != nil && s.logger != nil {
		s.logger.Debug("failed to close OIDC response body", "error", err)
	}
}
