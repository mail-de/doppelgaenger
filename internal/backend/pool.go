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

	"httpproxy/internal/headers"
)

// BackendKind distinguishes between primary and shadow backends.
type BackendKind string

const (
	// BackendPrimary refers to the main backend.
	BackendPrimary BackendKind = "primary"

	// BackendShadow refers to the shadow backend for traffic mirroring.
	BackendShadow BackendKind = "shadow"
)

// WorkItem represents a single proxy request to be processed by a worker.
type WorkItem struct {
	Ctx        context.Context
	ResponseCh chan BackendResult
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
	Status    int
}

// HTTPClient is the minimal interface required from http.Client.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// Pool manages a group of workers and a queue for backend requests.
type Pool interface {
	Enqueue(item WorkItem) error
}

type pool struct {
	base    *url.URL
	client  HTTPClient
	q       chan WorkItem
	kind    BackendKind
	maxBody int64
}

// NewPool initializes a backend Pool and starts the specified number of worker goroutines.
func NewPool(kind BackendKind, base *url.URL, upstreamTLS *tls.Config, workers, queueLen int, maxBody int64) Pool {
	p := &pool{
		kind:    kind,
		base:    base,
		client:  newHTTPClient(upstreamTLS),
		q:       make(chan WorkItem, queueLen),
		maxBody: maxBody,
	}

	for i := 0; i < workers; i++ {
		go func() {
			for item := range p.q {
				res := p.do(item)
				item.ResponseCh <- res
			}
		}()
	}

	return p
}

// Enqueue adds a request to the pool's queue or returns an error if the queue is full.
func (p *pool) Enqueue(item WorkItem) error {
	select {
	case p.q <- item:
		return nil
	default:
		return fmt.Errorf("%s backend queue full", p.kind)
	}
}

func (p *pool) do(item WorkItem) BackendResult {
	start := time.Now()

	u := *p.base
	u.Path = singleJoiningSlash(p.base.Path, item.Path)
	u.RawQuery = item.RawQuery

	req, err := http.NewRequestWithContext(item.Ctx, item.Method, u.String(), bytes.NewReader(item.Body))
	if err != nil {
		return BackendResult{Kind: item.Kind, Err: err, Duration: time.Since(start), RequestID: item.RequestID}
	}

	req.Header = headers.Clone(item.Header)
	req.Host = p.base.Host

	resp, err := p.client.Do(req)
	if err != nil {
		return BackendResult{Kind: item.Kind, Err: err, Duration: time.Since(start), RequestID: item.RequestID}
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
	}
}

func newHTTPClient(upstreamTLS *tls.Config) *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          1024,
		MaxIdleConnsPerHost:   256,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       upstreamTLS,
	}

	return &http.Client{Transport: transport}
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
