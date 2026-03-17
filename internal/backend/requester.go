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

	"doppelgaenger/internal/headers"
)

const (
	defaultHTTPDialTimeout           = 2 * time.Second
	defaultHTTPTLSHandshakeTimeout   = 5 * time.Second
	defaultHTTPResponseHeaderTimeout = 5 * time.Second
	defaultHTTPMaxIdleConns          = 1024
	defaultHTTPMaxIdleConnsPerHost   = 256
)

// BackendKind distinguishes between primary and shadow backends.
type BackendKind string

const (
	// BackendPrimary refers to the main backend.
	BackendPrimary BackendKind = "primary"

	// BackendShadow refers to the shadow backend for traffic mirroring.
	BackendShadow BackendKind = "shadow"
)

// Request represents a single proxy request to be processed by a backend requester.
type Request struct {
	Ctx        context.Context
	Kind       BackendKind
	Method     string
	Path       string
	RawQuery   string
	RemoteAddr string
	Header     http.Header
	Body       []byte
	RequestID  uint64
}

// BackendResult captures a backend response.
type BackendResult struct {
	Header    http.Header
	Body      []byte
	Err       error
	Proto     string
	Duration  time.Duration
	RequestID uint64
	Kind      BackendKind
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
	Do(item Request) BackendResult
}

type requester struct {
	bases    []*url.URL
	selector Selector
	client   HTTPClient
	kind     BackendKind
	maxBody  int64
}

// NewRequester initializes a direct backend requester.
func NewRequester(kind BackendKind, bases []*url.URL, selector Selector, upstreamTLS *tls.Config, maxBody int64, clientCfg HTTPClientConfig) Requester {
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
	}
}

func (p *requester) Do(item Request) BackendResult {
	start := time.Now()

	base := p.selectBase(item)
	if base == nil {
		return BackendResult{
			Kind:      item.Kind,
			Err:       fmt.Errorf("%s backend has no configured base URL", p.kind),
			Duration:  time.Since(start),
			RequestID: item.RequestID,
		}
	}
	selected := base.Redacted()

	u := *base
	u.Path = singleJoiningSlash(base.Path, item.Path)
	u.RawQuery = item.RawQuery

	req, err := http.NewRequestWithContext(item.Ctx, item.Method, u.String(), bytes.NewReader(item.Body))
	if err != nil {
		return BackendResult{
			Kind:      item.Kind,
			Selected:  selected,
			Err:       err,
			Duration:  time.Since(start),
			RequestID: item.RequestID,
		}
	}

	req.Header = headers.Clone(item.Header)
	req.Host = base.Host

	resp, err := p.client.Do(req)
	if err != nil {
		return BackendResult{
			Kind:      item.Kind,
			Selected:  selected,
			Err:       err,
			Duration:  time.Since(start),
			RequestID: item.RequestID,
		}
	}

	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	var body []byte
	if p.maxBody > 0 {
		body, _ = io.ReadAll(io.LimitReader(resp.Body, p.maxBody))
	} else {
		body, _ = io.ReadAll(resp.Body)
	}

	return BackendResult{
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
