// Package backend sends requests to configured primary and shadow HTTP backends.
package backend

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"doppelgaenger/internal/headers"
	"doppelgaenger/internal/observability"
)

const (
	defaultHTTPDialTimeout           = 2 * time.Second
	defaultHTTPTLSHandshakeTimeout   = 5 * time.Second
	defaultHTTPResponseHeaderTimeout = 5 * time.Second
	defaultHTTPMaxIdleConns          = 1024
	defaultHTTPMaxIdleConnsPerHost   = 256
)

// Kind distinguishes between primary and shadow backends.
type Kind string

const (
	// BackendPrimary refers to the main backend.
	BackendPrimary Kind = "primary"

	// BackendShadow refers to the shadow backend for traffic mirroring.
	BackendShadow Kind = "shadow"
)

// Request represents a single proxy request to be processed by a backend requester.
type Request struct {
	Ctx        context.Context
	Kind       Kind
	Method     string
	Path       string
	RawQuery   string
	RemoteAddr string
	Header     http.Header
	Body       []byte
	RequestID  uint64
}

// Result captures a backend response.
type Result struct {
	Header    http.Header
	Body      []byte
	Err       error
	Proto     string
	Duration  time.Duration
	RequestID uint64
	Kind      Kind
	Selected  string
	Status    int
}

// HTTPClient is the minimal interface required from http.Client.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// HTTPClientConfig controls transport behavior for backend HTTP requests.
type HTTPClientConfig struct {
	DialTimeout           time.Duration
	TLSHandshakeTimeout   time.Duration
	ResponseHeaderTimeout time.Duration
	MaxIdleConns          int
	MaxIdleConnsPerHost   int
	MaxConnsPerHost       int
}

// Requester executes backend requests directly.
type Requester interface {
	Do(item Request) Result
}

type requester struct {
	bases    []*url.URL
	selector Selector
	client   HTTPClient
	kind     Kind
	maxBody  int64
	obs      *observability.Observability
}

type backendObservation struct {
	err        error
	status     string
	result     string
	statusCode int
}

// NewRequester initializes a direct backend requester.
func NewRequester(kind Kind, bases []*url.URL, selector Selector, upstreamTLS *tls.Config, maxBody int64, clientCfg HTTPClientConfig, obs *observability.Observability) Requester {
	if len(bases) > 1 && selector == nil {
		selector = &RoundRobinSelector{}
	}

	clientCfg = normalizeHTTPClientConfig(clientCfg)

	return &requester{
		kind:     kind,
		bases:    bases,
		selector: selector,
		client:   newHTTPClient(upstreamTLS, clientCfg),
		maxBody:  maxBody,
		obs:      obs,
	}
}

func (p *requester) Do(item Request) Result {
	start := time.Now()

	ctx := requestContext(item)
	ctx, span := p.startBackendSpan(ctx, item)
	observation := backendObservation{
		status: observability.StatusError,
		result: observability.ResultError,
	}

	defer func() {
		p.finishBackendObservation(ctx, item, span, start, observation)
	}()

	base := p.selectBase(item)
	if base == nil {
		observation.err = fmt.Errorf("%s backend has no configured base URL", p.kind)

		return p.errorResult(item, "", observation.err, start)
	}

	req, selected, err := p.newBackendRequest(ctx, item, base, span)
	if err != nil {
		observation.err = err

		return p.errorResult(item, selected, err, start)
	}

	return p.executeRequest(req, item, selected, start, &observation)
}

func (p *requester) newBackendRequest(ctx context.Context, item Request, base *url.URL, span trace.Span) (*http.Request, string, error) {
	selected := base.Redacted()
	u := *base
	u.Path = singleJoiningSlash(base.Path, item.Path)
	u.RawQuery = item.RawQuery
	p.setBackendSpanAttributes(span, base, u)

	req, err := http.NewRequestWithContext(ctx, item.Method, u.String(), bytes.NewReader(item.Body))
	if err != nil {
		return nil, selected, err
	}

	req.Header = headers.Clone(item.Header)
	req.Host = base.Host

	if p.obs != nil {
		p.obs.InjectHTTPTraceContext(ctx, req.Header)
	}

	return req, selected, nil
}

func (p *requester) executeRequest(req *http.Request, item Request, selected string, start time.Time, observation *backendObservation) Result {
	resp, err := p.client.Do(req)
	if err != nil {
		observation.err = err

		return p.errorResult(item, selected, err, start)
	}

	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	body := p.readResponseBody(resp.Body)

	observation.statusCode = resp.StatusCode
	observation.status = observability.HTTPStatusClass(resp.StatusCode)
	observation.result = observability.ResultOK

	if resp.StatusCode >= http.StatusInternalServerError {
		observation.result = observability.ResultStatusCode
	}

	return Result{
		Kind:      item.Kind,
		Status:    resp.StatusCode,
		Header:    resp.Header,
		Body:      body,
		Err:       nil,
		Duration:  time.Since(start),
		RequestID: item.RequestID,
		Proto:     resp.Proto,
		Selected:  selected,
	}
}

func (p *requester) readResponseBody(body io.Reader) []byte {
	if p.maxBody > 0 {
		data, _ := io.ReadAll(io.LimitReader(body, p.maxBody))

		return data
	}

	data, _ := io.ReadAll(body)

	return data
}

func requestContext(item Request) context.Context {
	if item.Ctx != nil {
		return item.Ctx
	}

	return context.Background()
}

func (p *requester) errorResult(item Request, selected string, err error, start time.Time) Result {
	return Result{
		Kind:      item.Kind,
		Selected:  selected,
		Err:       err,
		Duration:  time.Since(start),
		RequestID: item.RequestID,
	}
}

func (p *requester) setBackendSpanAttributes(span trace.Span, base *url.URL, u url.URL) {
	if span == nil {
		return
	}

	span.SetAttributes(
		attribute.String("server.address", base.Hostname()),
		attribute.String("server.port", base.Port()),
		attribute.String("url.scheme", u.Scheme),
		attribute.String("url.path", u.EscapedPath()),
	)
}

func (p *requester) finishBackendObservation(ctx context.Context, item Request, span trace.Span, start time.Time, observation backendObservation) {
	if p.obs == nil {
		return
	}

	if span != nil {
		span.SetAttributes(
			attribute.Int("http.response.status_code", observation.statusCode),
			attribute.String(observability.LabelStatus, observation.status),
			attribute.String(observability.LabelResult, observation.result),
		)

		if observation.statusCode >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, http.StatusText(observation.statusCode))
		}
	}

	p.obs.ObserveBackendRequest(ctx, "http", string(p.kind), item.Method, observation.status, observation.result, time.Since(start))
	p.obs.EndSpan(span, observation.err)
}

func (p *requester) startBackendSpan(ctx context.Context, item Request) (context.Context, trace.Span) {
	if p.obs == nil {
		return ctx, nil
	}

	return p.obs.StartSpanWithKind(ctx,
		fmt.Sprintf("HTTP %s %s", item.Method, p.kind),
		trace.SpanKindClient,
		attribute.String(observability.LabelProtocol, "http"),
		attribute.String(observability.LabelTarget, string(p.kind)),
		attribute.String("http.request.method", item.Method),
		attribute.Int64("doppelgaenger.request_id", int64(item.RequestID)),
	)
}

func (p *requester) selectBase(item Request) *url.URL {
	switch len(p.bases) {
	case 0:
		return nil
	case 1:
		return p.bases[0]
	default:
		if p.selector == nil {
			return p.bases[0]
		}

		idx := p.selector.Select(item, len(p.bases))
		if idx < 0 || idx >= len(p.bases) {
			return p.bases[0]
		}

		return p.bases[idx]
	}
}

func newHTTPClient(upstreamTLS *tls.Config, cfg HTTPClientConfig) *http.Client {
	dialer := &net.Dialer{
		Timeout:   cfg.DialTimeout,
		KeepAlive: 30 * time.Second,
	}

	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          cfg.MaxIdleConns,
		MaxIdleConnsPerHost:   cfg.MaxIdleConnsPerHost,
		MaxConnsPerHost:       cfg.MaxConnsPerHost,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   cfg.TLSHandshakeTimeout,
		ResponseHeaderTimeout: cfg.ResponseHeaderTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       upstreamTLS,
	}

	return &http.Client{Transport: transport}
}

func normalizeHTTPClientConfig(cfg HTTPClientConfig) HTTPClientConfig {
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = defaultHTTPDialTimeout
	}

	if cfg.TLSHandshakeTimeout <= 0 {
		cfg.TLSHandshakeTimeout = defaultHTTPTLSHandshakeTimeout
	}

	if cfg.ResponseHeaderTimeout < 0 {
		cfg.ResponseHeaderTimeout = 0
	}

	if cfg.ResponseHeaderTimeout == 0 {
		cfg.ResponseHeaderTimeout = defaultHTTPResponseHeaderTimeout
	}

	if cfg.MaxIdleConns <= 0 {
		cfg.MaxIdleConns = defaultHTTPMaxIdleConns
	}

	if cfg.MaxIdleConnsPerHost <= 0 {
		cfg.MaxIdleConnsPerHost = defaultHTTPMaxIdleConnsPerHost
	}

	if cfg.MaxConnsPerHost < 0 {
		cfg.MaxConnsPerHost = 0
	}

	return cfg
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")

	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		if a == "" {
			return "/" + b
		}

		return a + "/" + b
	default:
		return a + b
	}
}
