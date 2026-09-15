package grpcproxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"doppelgaenger/internal/config"
)

const callerIntrospectionMode = "introspection"

const callerAuthRejected = "caller_auth_rejected"

// No redirects: even same-origin redirects could disclose the submitted token.
func newCallerHTTPClient(a config.GRPCCallerAuthConfig) (*http.Client, error) {
	tlsConfig, err := config.AuthClientTLSConfig(a.CAFile, a.ServerName, a.MinTLSVersion)
	if err != nil {
		return nil, err
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig

	return &http.Client{Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func (h *Handler) authenticateCaller(ctx context.Context, method string) error {
	if config.ValidateGRPCCallerAuth(h.cfg) != nil {
		return callerUnauthenticated()
	}

	a := h.cfg.GRPCCallerAuth
	if a.AllowUnauthenticated || a.Mode == "" {
		return nil
	}

	if a.Mode == "mtls" {
		return authenticateMTLS(ctx)
	}

	md, _ := metadata.FromIncomingContext(ctx)

	values := md.Get(bearerAuthHeader)
	if len(values) != 1 {
		return callerUnauthenticated()
	}

	parts := strings.Split(values[0], " ")
	if len(parts) != 2 || !strings.EqualFold(parts[0], tokenTypeBearer) || !validBearerValue(parts[1]) {
		return callerUnauthenticated()
	}

	claims, err := h.introspectCaller(ctx, parts[1])
	if err != nil {
		return callerUnauthenticated()
	}

	return claims.authorize(a, method, time.Now().Unix())
}

func validBearerValue(token string) bool {
	if token == "" {
		return false
	}

	for _, c := range token {
		allowed := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~+/=", c)
		if !allowed {
			return false
		}
	}

	return true
}

func authenticateMTLS(ctx context.Context) error {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return callerUnauthenticated()
	}

	info, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(info.State.VerifiedChains) == 0 || len(info.State.VerifiedChains[0]) == 0 {
		return callerUnauthenticated()
	}

	return nil
}

func callerUnauthenticated() error {
	return status.Error(codes.Unauthenticated, "caller authentication failed")
}

type introspectionClaims struct {
	Active    bool            `json:"active"`
	Issuer    string          `json:"iss"`
	Audience  json.RawMessage `json:"aud"`
	Scope     string          `json:"scope"`
	Expires   int64           `json:"exp"`
	NotBefore int64           `json:"nbf"`
}

func (h *Handler) introspectCaller(ctx context.Context, token string) (introspectionClaims, error) {
	a := h.cfg.GRPCCallerAuth

	secret := os.Getenv(a.ClientSecretEnv)
	if strings.TrimSpace(secret) == "" {
		return introspectionClaims{}, errors.New("missing introspection credentials")
	}

	timeout := a.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	form := url.Values{"token": {token}, "token_type_hint": {"access_token"}}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.IntrospectionEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return introspectionClaims{}, err
	}

	req.Header.Set("Content-Type", formContentType)
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(url.QueryEscape(a.ClientID), url.QueryEscape(secret))

	if h.callerHTTPError != nil {
		return introspectionClaims{}, h.callerHTTPError
	}

	client := h.callerHTTP
	if client == nil {
		client, err = newCallerHTTPClient(a)
		if err != nil {
			return introspectionClaims{}, err
		}
		defer client.CloseIdleConnections()
	}

	response, err := client.Do(req)
	if err != nil {
		return introspectionClaims{}, err
	}

	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return introspectionClaims{}, errors.New("introspection unavailable")
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(body) > 65536 {
		return introspectionClaims{}, errors.New("invalid introspection response")
	}

	var claims introspectionClaims

	err = json.Unmarshal(body, &claims)

	return claims, err
}

func (c introspectionClaims) authorize(a config.GRPCCallerAuthConfig, method string, now int64) error {
	if !c.Active || c.Issuer != a.Issuer || c.Expires <= now || c.NotBefore > now || !c.hasAudience(a.Audience) {
		return callerUnauthenticated()
	}

	scopes := strings.Fields(c.Scope)

	required := append([]string(nil), a.RequiredScopes...)
	if len(a.MethodScopes) > 0 {
		index := slices.IndexFunc(a.MethodScopes, func(rule config.GRPCCallerMethodScopes) bool { return rule.Method == method })

		ok := index >= 0
		if !ok {
			return status.Error(codes.PermissionDenied, "caller method not permitted")
		}

		required = append(required, a.MethodScopes[index].Scopes...)
	}

	for _, scope := range required {
		if !slices.Contains(scopes, scope) {
			return status.Error(codes.PermissionDenied, "caller scope not permitted")
		}
	}

	return nil
}

func (c introspectionClaims) hasAudience(expected string) bool {
	var single string
	if json.Unmarshal(c.Audience, &single) == nil {
		return single == expected
	}

	var multiple []string

	return json.Unmarshal(c.Audience, &multiple) == nil && slices.Contains(multiple, expected)
}

func (h *Handler) rejectCaller(rpcCtx *grpcRPCContext, err error) error {
	rpcCtx.outcome = callerAuthRejected
	rpcCtx.primaryStatus = status.Code(err)

	rpcCtx.spanErr = err
	if h.logger != nil {
		h.logger.Warn("grpc caller rejected", "reason", status.Code(err).String())
	}

	return err
}
