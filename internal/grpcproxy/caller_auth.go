package grpcproxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	"doppelgaenger/internal/config"
)

const callerIntrospectionMode = "introspection"

const (
	defaultCallerIntrospectionTimeout = 2 * time.Second
	maxIntrospectionResponseBytes     = 65536
	// maxBearerTokenBytes rejects oversized Bearer values before hashing or
	// forwarding them; real access tokens are far smaller.
	maxBearerTokenBytes = 8192
	// maxIntrospectionErrorBytes bounds the error body read to classify HTTP 400.
	maxIntrospectionErrorBytes = 4096
)

// Bounded introspection results for metrics and span attributes.
const (
	callerLookupHit      = "hit"
	callerLookupMiss     = "miss"
	callerLookupShared   = "shared"
	callerLookupError    = "error"
	callerLookupCanceled = "canceled"

	callerLookupAttribute = "doppelgaenger.caller_auth.introspection"
)

// callerAuthRuntime is initialized once per handler so that every RPC reuses
// the same HTTP client, positive cache and flight group.
type callerAuthRuntime struct {
	once      sync.Once
	client    *http.Client
	clientErr error
	cache     *introspectionCache
	flights   introspectionFlights
	logs      callerLogLimiter
}

// No redirects: even same-origin redirects could disclose the submitted token.
func newCallerHTTPClient(a config.GRPCCallerAuthConfig) (*http.Client, error) {
	tlsConfig, err := config.AuthClientTLSConfig(a.CAFile, a.ServerName, a.MinTLSVersion)
	if err != nil {
		return nil, err
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	// Keep one idle connection per permitted concurrent introspection; the
	// net/http default of two forces reconnects and TLS handshakes under load.
	transport.MaxIdleConnsPerHost = a.EffectiveIntrospectionMaxConcurrent()

	return &http.Client{Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func (h *Handler) callerRuntime() *callerAuthRuntime {
	r := &h.callerAuth
	r.once.Do(func() {
		a := h.cfg.GRPCCallerAuth
		r.client, r.clientErr = h.callerHTTP, h.callerHTTPError

		if r.client == nil && r.clientErr == nil {
			r.client, r.clientErr = newCallerHTTPClient(a)
		}

		r.cache = newIntrospectionCache(a.IntrospectionCacheTTL, a.IntrospectionCacheMaxEntries)
		r.flights.slots = make(chan struct{}, a.EffectiveIntrospectionMaxConcurrent())
	})

	return r
}

func (h *Handler) authenticateCaller(ctx context.Context, method string) error {
	if err := config.ValidateGRPCCallerAuth(h.cfg); err != nil {
		return callerMisconfigured("invalid grpc_caller_auth configuration: "+err.Error(), err)
	}

	a := h.cfg.GRPCCallerAuth
	if a.AllowUnauthenticated || a.Mode == "" {
		return nil
	}

	if a.Mode == "mtls" {
		return authenticateMTLS(ctx)
	}

	token, err := callerBearerToken(ctx)
	if err != nil {
		return err
	}

	claims, err := h.introspectCaller(ctx, token)
	if err != nil {
		return err
	}

	return claims.authorize(a, method, time.Now().Unix())
}

func callerBearerToken(ctx context.Context) (string, error) {
	md, _ := metadata.FromIncomingContext(ctx)

	values := md.Get(bearerAuthHeader)
	if len(values) == 0 {
		return "", callerRejected(callerCauseMissingBearer)
	}

	if len(values) != 1 {
		return "", callerRejected(callerCauseMalformedBearer)
	}

	parts := strings.Split(values[0], " ")
	if len(parts) != 2 || !strings.EqualFold(parts[0], tokenTypeBearer) || !validBearerValue(parts[1]) {
		return "", callerRejected(callerCauseMalformedBearer)
	}

	return parts[1], nil
}

func validBearerValue(token string) bool {
	if token == "" || len(token) > maxBearerTokenBytes {
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
		return callerRejected(callerCauseMTLS)
	}

	info, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(info.State.VerifiedChains) == 0 || len(info.State.VerifiedChains[0]) == 0 {
		return callerRejected(callerCauseMTLS)
	}

	return nil
}

type introspectionClaims struct {
	Active    bool            `json:"active"`
	Issuer    string          `json:"iss"`
	Audience  json.RawMessage `json:"aud"`
	Scope     string          `json:"scope"`
	Expires   int64           `json:"exp"`
	NotBefore int64           `json:"nbf"`
}

// introspectCaller returns claims from the positive cache or from one shared
// introspection request. Returned claims still need authorize.
func (h *Handler) introspectCaller(ctx context.Context, token string) (introspectionClaims, error) {
	runtime := h.callerRuntime()
	if runtime.clientErr != nil {
		return introspectionClaims{}, callerMisconfigured("introspection HTTP client unavailable: "+runtime.clientErr.Error(), runtime.clientErr)
	}

	key := digestCallerToken(token)
	if claims, ok := runtime.cache.get(key, time.Now()); ok {
		h.recordCallerLookup(ctx, callerLookupHit, trace.SpanContext{})

		return claims, nil
	}

	if err := callerContextError(ctx); err != nil {
		h.recordCallerLookup(ctx, callerLookupCanceled, trace.SpanContext{})

		return introspectionClaims{}, err
	}

	flight, shared, err := runtime.flights.join(ctx, key, func(flightCtx context.Context) introspectionResult {
		return h.runIntrospectionFlight(flightCtx, runtime, key, token)
	})
	if err != nil {
		h.recordCallerLookup(ctx, callerLookupError, trace.SpanContext{})

		return introspectionClaims{}, err
	}

	defer runtime.flights.leave(key, flight)

	select {
	case <-flight.done:
		result := flight.result

		lookup := callerLookupMiss
		if shared {
			lookup = callerLookupShared
		}

		switch {
		case result.err != nil:
			lookup = callerLookupError
		case result.cached:
			lookup = callerLookupHit
		}

		h.recordCallerLookup(ctx, lookup, result.span)

		return result.claims, cloneCallerAuthError(result.err)
	case <-ctx.Done():
		h.recordCallerLookup(ctx, callerLookupCanceled, trace.SpanContext{})

		return introspectionClaims{}, callerContextError(ctx)
	}
}

// runIntrospectionFlight performs one introspection on behalf of all waiters
// and caches a positive result.
func (h *Handler) runIntrospectionFlight(ctx context.Context, runtime *callerAuthRuntime, key callerTokenDigest, token string) introspectionResult {
	if claims, ok := runtime.cache.get(key, time.Now()); ok {
		return introspectionResult{claims: claims, cached: true}
	}

	h.observability.AddCallerIntrospectionFlights(ctx, 1)
	defer h.observability.AddCallerIntrospectionFlights(ctx, -1)

	parent := trace.SpanFromContext(ctx).SpanContext()
	ctx, span := h.observability.StartSpanWithKind(ctx, "gRPC caller introspection", trace.SpanKindClient)
	// With tracing disabled the caller's own span is returned; never end it here.
	created := span.SpanContext().IsValid() && span.SpanContext().SpanID() != parent.SpanID()

	claims, err := h.fetchIntrospection(ctx, runtime.client, token)
	if err == nil && claims.validate(h.cfg.GRPCCallerAuth, time.Now().Unix()) == nil {
		runtime.cache.put(key, claims, time.Now())
	}

	result := introspectionResult{claims: claims, err: err}
	if !created {
		return result
	}

	if err != nil {
		span.SetAttributes(attribute.String(callerAuthCauseAttribute, string(asCallerAuthError(err).cause)))
	}

	h.observability.EndSpan(span, err)
	result.span = span.SpanContext()

	return result
}

// recordCallerLookup counts the lookup and links the caller's span to the
// introspection span that answered it.
func (h *Handler) recordCallerLookup(ctx context.Context, result string, introspection trace.SpanContext) {
	h.observability.ObserveCallerIntrospection(ctx, result)

	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attribute.String(callerLookupAttribute, result))

	if introspection.IsValid() && introspection.SpanID() != span.SpanContext().SpanID() {
		span.AddLink(trace.Link{SpanContext: introspection})
	}
}

func (h *Handler) fetchIntrospection(ctx context.Context, client *http.Client, token string) (introspectionClaims, error) {
	a := h.cfg.GRPCCallerAuth

	secret := os.Getenv(a.ClientSecretEnv)
	if strings.TrimSpace(secret) == "" {
		return introspectionClaims{}, callerMisconfigured("introspection client secret environment variable "+a.ClientSecretEnv+" is empty", nil)
	}

	timeout := a.Timeout
	if timeout <= 0 {
		timeout = defaultCallerIntrospectionTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	form := url.Values{"token": {token}, "token_type_hint": {"access_token"}}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.IntrospectionEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return introspectionClaims{}, callerMisconfigured("invalid introspection request", nil)
	}

	req.Header.Set("Content-Type", formContentType)
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(url.QueryEscape(a.ClientID), url.QueryEscape(secret))

	response, err := client.Do(req)
	if err != nil {
		return introspectionClaims{}, introspectionTransportError(ctx, err)
	}

	defer func() {
		// Drain a bounded remainder so the connection can be reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxIntrospectionResponseBytes))
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return introspectionClaims{}, callerHTTPStatusError(response.StatusCode, introspectionOAuthError(response))
	}

	return decodeIntrospection(ctx, response.Body)
}

// introspectionOAuthError extracts the RFC 6749 error code of a 400 response;
// it is only compared against fixed values and never logged.
func introspectionOAuthError(response *http.Response) string {
	if response.StatusCode != http.StatusBadRequest {
		return ""
	}

	var body struct {
		Error string `json:"error"`
	}

	if json.NewDecoder(io.LimitReader(response.Body, maxIntrospectionErrorBytes)).Decode(&body) != nil {
		return ""
	}

	return body.Error
}

func decodeIntrospection(ctx context.Context, body io.Reader) (introspectionClaims, error) {
	payload, err := io.ReadAll(io.LimitReader(body, maxIntrospectionResponseBytes+1))
	if err != nil {
		return introspectionClaims{}, introspectionTransportError(ctx, err)
	}

	if len(payload) > maxIntrospectionResponseBytes {
		return introspectionClaims{}, callerUnavailable(callerCauseResponse, "introspection response exceeds size limit", nil)
	}

	var claims introspectionClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return introspectionClaims{}, callerUnavailable(callerCauseResponse, "introspection response is not valid JSON", err)
	}

	return claims, nil
}

// validate checks token validity independent of the called method. It runs
// again on every cache hit so that exp and nbf are enforced at use time.
func (c introspectionClaims) validate(a config.GRPCCallerAuthConfig, now int64) error {
	if !c.Active {
		return callerRejected(callerCauseInactive)
	}

	if c.Issuer != a.Issuer || c.Expires <= now || c.NotBefore > now || !c.hasAudience(a.Audience) {
		return callerRejected(callerCauseClaims)
	}

	return nil
}

func (c introspectionClaims) authorize(a config.GRPCCallerAuthConfig, method string, now int64) error {
	if err := c.validate(a, now); err != nil {
		return err
	}

	scopes := strings.Fields(c.Scope)

	required := append([]string(nil), a.RequiredScopes...)
	if len(a.MethodScopes) > 0 {
		index := slices.IndexFunc(a.MethodScopes, func(rule config.GRPCCallerMethodScopes) bool { return rule.Method == method })
		if index < 0 {
			return callerDenied(callerCauseMethod, callerMessageMethod)
		}

		required = append(required, a.MethodScopes[index].Scopes...)
	}

	for _, scope := range required {
		if !slices.Contains(scopes, scope) {
			return callerDenied(callerCauseScope, callerMessageScope)
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
	authErr := asCallerAuthError(err)
	rpcCtx.outcome = authErr.outcome()
	rpcCtx.primaryStatus = authErr.code
	rpcCtx.callerAuthCause = string(authErr.cause)
	rpcCtx.spanErr = authErr

	if h.logger == nil {
		return authErr
	}

	attrs := []any{"reason", authErr.code.String(), "cause", string(authErr.cause)}

	if authErr.technical() {
		allowed, suppressed := h.callerAuth.logs.allow(authErr.logKey(), time.Now())
		if !allowed {
			return authErr
		}

		if suppressed > 0 {
			attrs = append(attrs, "suppressed", suppressed)
		}
	}

	if authErr.httpStatus != 0 {
		attrs = append(attrs, "http_status", authErr.httpStatus)
	}

	if authErr.detail != "" {
		attrs = append(attrs, "detail", authErr.detail)
	}

	h.logger.Log(context.Background(), authErr.logLevel(), "grpc caller rejected", attrs...)

	return authErr
}
