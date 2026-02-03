// main.go
//
// Auth reverse proxy with:
// - Gin inbound
// - Primary + Shadow backends via net/http
// - Separate worker pools with buffered queues per backend
// - Primary responds immediately, shadow awaited with deadline for logging only
// - NGINX mail-auth style: backend defines response headers, proxy forwards whitelisted headers
// - Structured logging (slog) with key/value, diff details tell you which headers differ
// - Shadow sampling + token-bucket rate limit
// - Correlation ID: ensure X-Request-ID exists, forward to both backends, log it
// - HTTPS + HTTP/2 inbound (ALPN) and outbound (ForceAttemptHTTP2)
// - Optional custom Root CA PEM for outbound TLS (PRIMARY/SHADOW)
// - Optional graceful shutdown
//
// Build/run:
//   go mod init example.com/authproxy
//   go get github.com/gin-gonic/gin
//   go run .
//
// Env:
//   LISTEN=:8443
//   TLS_CERT=/path/cert.pem
//   TLS_KEY=/path/key.pem
//
//   PRIMARY=https://127.0.0.1:9001
//   SHADOW=https://127.0.0.1:9002
//   ROOT_CA=/path/internal-ca.pem          # optional, shared for both upstreams
//   PRIMARY_ROOT_CA=/path/ca1.pem          # optional, overrides ROOT_CA
//   SHADOW_ROOT_CA=/path/ca2.pem           # optional, overrides ROOT_CA
//
//   SHADOW_TIMEOUT=150ms
//   SHADOW_SAMPLE_PERCENT=5
//   SHADOW_FORCE_HEADER=X-Shadow
//   SHADOW_RPS=200
//   SHADOW_BURST=400
//
//   PRIMARY_WORKERS=32
//   SHADOW_WORKERS=16
//   PRIMARY_QUEUE=4096
//   SHADOW_QUEUE=4096
//
//   LOG_SESSION_ONLY_ON_DIFF=true
//
// Notes:
// - Inbound HTTP/2: works automatically with ListenAndServeTLS (ALPN).
// - Outbound HTTP/2: works automatically over TLS when ForceAttemptHTTP2=true.
// - If you want h2c (HTTP/2 cleartext) to backends: extra work; not included.

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
)

// --- Version ----------------------------------------------------------------

var version = "dev"

// --- Configuration ----------------------------------------------------------

// Config holds all configuration settings for the proxy.
type Config struct {
	// PrimaryBaseURL is the base URL for the primary backend that provides client responses.
	PrimaryBaseURL *url.URL

	// ShadowBaseURL is the base URL for the shadow backend where traffic is mirrored.
	ShadowBaseURL *url.URL

	// ShadowRPS defines the maximum requests per second for the shadow backend (0 disables the limit).
	ShadowRPS float64

	// MaxBackendBodyBytes limits the size of the request body sent to backends.
	MaxBackendBodyBytes int64

	// ListenAddr is the address the proxy listens on (e.g., ":8443").
	ListenAddr string

	// TLSCertFile path to the TLS certificate file for the proxy server.
	TLSCertFile string

	// TLSKeyFile path to the TLS key file for the proxy server.
	TLSKeyFile string

	// ShadowForceHeader is the header name that (if present) forces shadowing.
	ShadowForceHeader string

	// RootCAPath is the path to a common CA certificate for all upstream backends.
	RootCAPath string

	// PrimaryRootCA path to the CA certificate specifically for the primary backend.
	PrimaryRootCA string

	// ShadowRootCA path to the CA certificate specifically for the shadow backend.
	ShadowRootCA string

	// PrimaryWorkers number of parallel workers for the primary backend.
	PrimaryWorkers int

	// ShadowWorkers number of parallel workers for the shadow backend.
	ShadowWorkers int

	// PrimaryQueueLen maximum queue size for primary requests.
	PrimaryQueueLen int

	// ShadowQueueLen maximum queue size for shadow requests.
	ShadowQueueLen int

	// ShadowSamplePercent percentage of traffic mirrored to the shadow backend (0-100).
	ShadowSamplePercent int

	// ShadowBurst maximum number of tokens in the token bucket (burst capacity).
	ShadowBurst int

	// ShadowTimeout time limit for requests to the shadow backend.
	ShadowTimeout time.Duration

	// ForwardResponseHeaders list of headers passed from the primary backend to the client.
	ForwardResponseHeaders []string

	// CompareHeaders list of headers compared between primary and shadow backends.
	CompareHeaders []string

	// LogSessionOnlyOnDiff controls whether session headers are logged only when there are differences.
	LogSessionOnlyOnDiff bool

	// InsecureUpstream allows insecure TLS connections (no verification) to the backends.
	InsecureUpstream bool
}

// parseURL parses a string as a URL and ensures that scheme and host are present.
func parseURL(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil {
		return nil, err
	}

	if u.Scheme == "" || u.Host == "" {
		return nil, errors.New("base url must include scheme and host, e.g. https://127.0.0.1:9001")
	}

	return u, nil
}

// getenv reads an environment variable or returns a default value.
func getenv(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}

	return v
}

// getenvInt reads an environment variable as an integer or returns a default value.
func getenvInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}

	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}

	return n
}

// getenvFloat reads an environment variable as a float64 or returns a default value.
func getenvFloat(key string, def float64) float64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}

	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}

	return f
}

// getenvBool reads an environment variable as a boolean (supports various formats like true, 1, yes).
func getenvBool(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}

	switch strings.ToLower(v) {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return def
	}
}

// getenvDuration reads an environment variable as a time duration.
func getenvDuration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}

	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}

	return d
}

// loadConfig loads the configuration from environment variables and sets default values.
func loadConfig() (Config, error) {
	primaryURL, err := parseURL(getenv("PRIMARY", "https://127.0.0.1:9001"))
	if err != nil {
		return Config{}, fmt.Errorf("invalid PRIMARY URL: %w", err)
	}

	shadowURL, err := parseURL(getenv("SHADOW", "https://127.0.0.1:9002"))
	if err != nil {
		return Config{}, fmt.Errorf("invalid SHADOW URL: %w", err)
	}

	cfg := Config{
		ListenAddr: getenv("LISTEN", ":8443"),

		TLSCertFile: getenv("TLS_CERT", ""),
		TLSKeyFile:  getenv("TLS_KEY", ""),

		PrimaryBaseURL: primaryURL,
		ShadowBaseURL:  shadowURL,

		PrimaryWorkers:  getenvInt("PRIMARY_WORKERS", 32),
		ShadowWorkers:   getenvInt("SHADOW_WORKERS", 16),
		PrimaryQueueLen: getenvInt("PRIMARY_QUEUE", 4096),
		ShadowQueueLen:  getenvInt("SHADOW_QUEUE", 4096),

		ShadowTimeout: getenvDuration("SHADOW_TIMEOUT", 150*time.Millisecond),

		ShadowSamplePercent: getenvInt("SHADOW_SAMPLE_PERCENT", 5),
		ShadowForceHeader:   getenv("SHADOW_FORCE_HEADER", "X-Shadow"),

		ShadowRPS:   getenvFloat("SHADOW_RPS", 200),
		ShadowBurst: getenvInt("SHADOW_BURST", 400),

		ForwardResponseHeaders: []string{
			"Auth-Status",
			"Auth-Server",
			"Auth-Port",
			"Auth-User",
			"Auth-Pass",
			"Auth-Error",
			"Auth-Wait",
			"Auth-Protocol",
			"X-Nauthilus-Session",
		},
		CompareHeaders: []string{
			"Auth-Status",
			"Auth-Server",
			"Auth-Port",
			"Auth-User",
			"Auth-Error",
			"X-Nauthilus-Session",
		},

		LogSessionOnlyOnDiff: getenvBool("LOG_SESSION_ONLY_ON_DIFF", true),
		MaxBackendBodyBytes:  32 * 1024,

		RootCAPath:       getenv("ROOT_CA", ""),
		PrimaryRootCA:    getenv("PRIMARY_ROOT_CA", ""),
		ShadowRootCA:     getenv("SHADOW_ROOT_CA", ""),
		InsecureUpstream: getenvBool("INSECURE_UPSTREAM", false),
	}

	if cfg.ShadowSamplePercent < 0 {
		cfg.ShadowSamplePercent = 0
	}

	if cfg.ShadowSamplePercent > 100 {
		cfg.ShadowSamplePercent = 100
	}

	if cfg.ShadowBurst < 1 && cfg.ShadowRPS > 0 {
		cfg.ShadowBurst = 1
	}

	return cfg, nil
}

// --- Token bucket rate limiter ---------------------------------------------

// TokenBucket implements a simple token bucket algorithm for rate limiting.
type TokenBucket struct {
	last   time.Time
	rate   float64
	burst  float64
	tokens float64
	mu     sync.Mutex
}

// NewTokenBucket creates a new TokenBucket with the specified rate and burst capacity.
func NewTokenBucket(rate float64, burst int) *TokenBucket {
	tb := &TokenBucket{
		rate:  rate,
		burst: float64(burst),
		last:  time.Now(),
	}
	tb.tokens = tb.burst

	return tb
}

// Allow checks if a request is allowed under the rate limit.
func (tb *TokenBucket) Allow() bool {
	if tb == nil || tb.rate <= 0 {
		return true
	}

	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tb.last).Seconds()
	tb.last = now

	tb.tokens += elapsed * tb.rate
	if tb.tokens > tb.burst {
		tb.tokens = tb.burst
	}

	if tb.tokens >= 1.0 {
		tb.tokens -= 1.0

		return true
	}

	return false
}

// --- Worker pool ------------------------------------------------------------

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
	// baseURL is the target base URL for this request.
	baseURL *url.URL

	// ctx is the context for the request.
	ctx context.Context

	// responseCh is the channel where the result is returned.
	responseCh chan BackendResult

	// kind indicates whether it is a primary or shadow request.
	kind BackendKind

	// method is the HTTP method (GET, POST, etc.).
	method string

	// path is the requested path.
	path string

	// rawQuery contains the URL query parameters.
	rawQuery string

	// remoteAddr is the IP address of the original client.
	remoteAddr string

	// header contains the copied HTTP headers of the request.
	header http.Header

	// body contains the request body.
	body []byte

	// requestID is the unique ID of the request.
	requestID uint64
}

// BackendResult contains the result of a backend request.
type BackendResult struct {
	// header contains the response headers from the backend.
	header http.Header

	// body contains the response body from the backend.
	body []byte

	// err stores any errors that occurred during the request.
	err error

	// proto indicates the HTTP protocol used (e.g., "HTTP/1.1").
	proto string

	// duration indicates the duration of the request.
	duration time.Duration

	// requestID refers to the original request ID.
	requestID uint64

	// kind indicates the type of the backend.
	kind BackendKind

	// status is the HTTP status code of the response.
	status int
}

// BackendPool manages a group of workers and a queue for backend requests.
type BackendPool struct {
	base   *url.URL
	client *http.Client
	q      chan WorkItem
	kind   BackendKind
}

// readCertPoolFromPEM reads a PEM file and creates an x509.CertPool from it.
func readCertPoolFromPEM(path string) (*x509.CertPool, error) {
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	pool := x509.NewCertPool()
	if ok := pool.AppendCertsFromPEM(pemBytes); !ok {
		return nil, fmt.Errorf("failed to parse CA PEM: %s", path)
	}

	return pool, nil
}

// newHTTPClient creates a configured http.Client for backend requests.
func newHTTPClient(upstreamTLS *tls.Config) *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true, // allow outbound HTTP/2 over TLS
		MaxIdleConns:          1024,
		MaxIdleConnsPerHost:   256,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       upstreamTLS, // nil ok for http://
	}

	return &http.Client{Transport: transport}
}

// NewBackendPool initializes a BackendPool and starts the specified number of worker goroutines.
func NewBackendPool(kind BackendKind, base *url.URL, upstreamTLS *tls.Config, workers, queueLen int, maxBody int64) *BackendPool {
	p := &BackendPool{
		kind:   kind,
		base:   base,
		client: newHTTPClient(upstreamTLS),
		q:      make(chan WorkItem, queueLen),
	}

	for i := 0; i < workers; i++ {
		go func() {
			for item := range p.q {
				res := p.do(item, maxBody)
				item.responseCh <- res
			}
		}()
	}

	return p
}

// Enqueue adds a request to the pool's queue or returns an error if the queue is full.
func (p *BackendPool) Enqueue(item WorkItem) error {
	select {
	case p.q <- item:
		return nil
	default:
		return fmt.Errorf("%s backend queue full", p.kind)
	}
}

// do performs the actual HTTP request to the backend.
func (p *BackendPool) do(item WorkItem, maxBody int64) BackendResult {
	start := time.Now()

	u := *item.baseURL
	u.Path = singleJoiningSlash(item.baseURL.Path, item.path)
	u.RawQuery = item.rawQuery

	req, err := http.NewRequestWithContext(item.ctx, item.method, u.String(), bytes.NewReader(item.body))
	if err != nil {
		return BackendResult{kind: item.kind, err: err, duration: time.Since(start), requestID: item.requestID}
	}

	req.Header = cloneHeader(item.header)
	req.Host = item.baseURL.Host

	resp, err := p.client.Do(req)
	if err != nil {
		return BackendResult{kind: item.kind, err: err, duration: time.Since(start), requestID: item.requestID}
	}

	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	var body []byte
	if maxBody > 0 {
		body, _ = io.ReadAll(io.LimitReader(resp.Body, maxBody))
	} else {
		body, _ = io.ReadAll(resp.Body)
	}

	return BackendResult{
		kind:      item.kind,
		status:    resp.StatusCode,
		header:    cloneHeader(resp.Header),
		body:      body,
		err:       nil,
		duration:  time.Since(start),
		requestID: item.requestID,
		proto:     resp.Proto,
	}
}

// singleJoiningSlash joins two URL paths ensuring a single slash between them.
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

// cloneHeader creates a deep copy of an http.Header.
func cloneHeader(h http.Header) http.Header {
	cp := make(http.Header, len(h))
	for k, vv := range h {
		nv := make([]string, len(vv))
		copy(nv, vv)
		cp[k] = nv
	}

	return cp
}

// --- Compare helpers --------------------------------------------------------

// HeaderDiff represents a difference in headers between primary and shadow.
type HeaderDiff struct {
	Key     string `json:"key"`
	Primary string `json:"primary"`
	Shadow  string `json:"shadow"`
}

// compareHeadersDetailed compares specified headers between primary and shadow responses.
func compareHeadersDetailed(primary http.Header, shadow http.Header, keys []string, includeEmpty bool) (p map[string]string, s map[string]string, diffs []HeaderDiff, diff bool) {
	p = make(map[string]string, len(keys))
	s = make(map[string]string, len(keys))
	diffs = make([]HeaderDiff, 0, len(keys))

	for _, key := range keys {
		ck := http.CanonicalHeaderKey(key)
		pv := strings.Join(primary.Values(ck), ",")
		sv := strings.Join(shadow.Values(ck), ",")

		if !includeEmpty && pv == "" && sv == "" {
			continue
		}

		p[ck] = pv
		s[ck] = sv
		if pv != sv {
			diff = true
			diffs = append(diffs, HeaderDiff{Key: ck, Primary: pv, Shadow: sv})
		}
	}

	return
}

// writeSelectedHeaders writes only the allowed headers from src to w.
func writeSelectedHeaders(w http.ResponseWriter, src http.Header, allow []string) {
	allowSet := make(map[string]struct{}, len(allow))
	for _, k := range allow {
		allowSet[http.CanonicalHeaderKey(k)] = struct{}{}
	}

	for k, vv := range src {
		ck := http.CanonicalHeaderKey(k)
		if _, ok := allowSet[ck]; !ok {
			continue
		}

		w.Header().Del(ck)
		for _, v := range vv {
			w.Header().Add(ck, v)
		}
	}
}

// errString returns a string representation of an error, specifically handling context errors.
func errString(err error) string {
	if err == nil {
		return ""
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}

	if errors.Is(err, context.Canceled) {
		return "canceled"
	}

	return err.Error()
}

// clientIP extracts the client IP address from the request, considering X-Forwarded-For.
func clientIP(r *http.Request) string {
	xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}

	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}

	return r.RemoteAddr
}

// ensureRequestID ensures that a request ID exists in the header, generating one if necessary.
func ensureRequestID(h http.Header) (rid string, generated bool) {
	// If client already provides it, trust it.
	rid = strings.TrimSpace(h.Get("X-Request-Id"))
	if rid == "" {
		rid = strings.TrimSpace(h.Get("X-Request-ID"))
	}

	if rid != "" {
		return rid, false
	}

	// Generate a short-ish ID; you can swap to ULID/UUID if you want.
	// Keep it simple and dependency-free.
	rid = fmt.Sprintf("%d-%d", time.Now().UnixNano(), rand.Int63())
	h.Set("X-Request-ID", rid)

	return rid, true
}

// --- Main -------------------------------------------------------------------

var globalReqID atomic.Uint64

func main() {
	// Structured logger (JSON)
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	// Seed RNG for sampling + request-id generation.
	rand.New(rand.NewSource(time.Now().UnixNano()))

	// --- Upstream TLS config (optional CA bundle) ----------------------------

	buildUpstreamTLS := func(caPath string) (*tls.Config, error) {
		if cfg.InsecureUpstream {
			return &tls.Config{
				MinVersion:         tls.VersionTLS12,
				InsecureSkipVerify: true, // avoid unless in lab
			}, nil
		}

		// If no CA override, nil is fine; system roots used automatically.
		if caPath == "" {
			return &tls.Config{
				MinVersion: tls.VersionTLS12,
			}, nil
		}

		pool, err := readCertPoolFromPEM(caPath)
		if err != nil {
			return nil, err
		}

		return &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    pool,
		}, nil
	}

	primaryCA := cfg.PrimaryRootCA
	if primaryCA == "" {
		primaryCA = cfg.RootCAPath
	}

	shadowCA := cfg.ShadowRootCA
	if shadowCA == "" {
		shadowCA = cfg.RootCAPath
	}

	primaryTLS, err := buildUpstreamTLS(primaryCA)
	if err != nil {
		slog.Error("primary CA", "err", err)
		os.Exit(1)
	}

	shadowTLS, err := buildUpstreamTLS(shadowCA)
	if err != nil {
		slog.Error("shadow CA", "err", err)
		os.Exit(1)
	}

	primaryPool := NewBackendPool(BackendPrimary, cfg.PrimaryBaseURL, primaryTLS, cfg.PrimaryWorkers, cfg.PrimaryQueueLen, cfg.MaxBackendBodyBytes)
	shadowPool := NewBackendPool(BackendShadow, cfg.ShadowBaseURL, shadowTLS, cfg.ShadowWorkers, cfg.ShadowQueueLen, cfg.MaxBackendBodyBytes)

	var shadowLimiter *TokenBucket
	if cfg.ShadowRPS > 0 {
		shadowLimiter = NewTokenBucket(cfg.ShadowRPS, cfg.ShadowBurst)
	}

	// --- Gin / router ---------------------------------------------------------

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	r.Any("/*path", func(c *gin.Context) {
		reqID := globalReqID.Add(1)

		// Read request body once.
		var body []byte
		if c.Request.Body != nil {
			b, err := io.ReadAll(c.Request.Body)
			if err != nil {
				c.AbortWithStatus(http.StatusBadRequest)

				return
			}

			body = b
		}

		method := c.Request.Method
		path := c.Param("path")
		rawQuery := c.Request.URL.RawQuery
		hdr := cloneHeader(c.Request.Header)
		remoteAddr := clientIP(c.Request)

		// Ensure correlation id for both upstream calls + logging.
		corrID, corrGenerated := ensureRequestID(hdr)

		// Set X-Forwarded-Proto for downstreams (good hygiene).
		// If NGINX already sets it, we keep it.
		if hdr.Get("X-Forwarded-Proto") == "" {
			// For inbound TLS we will always be https here.
			hdr.Set("X-Forwarded-Proto", "https")
		}

		primaryCh := make(chan BackendResult, 1)
		shadowCh := make(chan BackendResult, 1)

		// --- Shadow decision (sampling or forced header) --------------------

		forcedShadow := false
		if cfg.ShadowForceHeader != "" && c.Request.Header.Get(cfg.ShadowForceHeader) != "" {
			forcedShadow = true
		}

		sampledShadow := false
		if cfg.ShadowSamplePercent >= 100 {
			sampledShadow = true
		} else if cfg.ShadowSamplePercent > 0 {
			if rand.Intn(100) < cfg.ShadowSamplePercent {
				sampledShadow = true
			}
		}

		doShadow := forcedShadow || sampledShadow

		// Rate limit shadow (but forcedShadow wins)
		if doShadow && !forcedShadow && shadowLimiter != nil && !shadowLimiter.Allow() {
			doShadow = false
		}

		// Enqueue shadow (best effort)
		shadowStarted := false
		var shadowCtx context.Context
		var shadowCancel context.CancelFunc

		if doShadow {
			shadowCtx, shadowCancel = context.WithTimeout(c.Request.Context(), cfg.ShadowTimeout)
			defer shadowCancel()

			shadowItem := WorkItem{
				kind:       BackendShadow,
				baseURL:    cfg.ShadowBaseURL,
				method:     method,
				path:       path,
				rawQuery:   rawQuery,
				header:     hdr,
				body:       body,
				remoteAddr: remoteAddr,
				requestID:  reqID,
				ctx:        shadowCtx,
				responseCh: shadowCh,
			}

			if err := shadowPool.Enqueue(shadowItem); err == nil {
				shadowStarted = true
			}
		}

		// Enqueue primary (must succeed)
		primaryItem := WorkItem{
			kind:       BackendPrimary,
			baseURL:    cfg.PrimaryBaseURL,
			method:     method,
			path:       path,
			rawQuery:   rawQuery,
			header:     hdr,
			body:       body,
			remoteAddr: remoteAddr,
			requestID:  reqID,
			ctx:        c.Request.Context(),
			responseCh: primaryCh,
		}

		if err := primaryPool.Enqueue(primaryItem); err != nil {
			slog.Error("primary_enqueue_failed",
				"req_id", reqID,
				"x_request_id", corrID,
				"remote", remoteAddr,
				"method", method,
				"path", path,
				"err", err.Error(),
			)
			c.AbortWithStatus(http.StatusServiceUnavailable)

			return
		}

		// Wait for primary
		primaryRes := <-primaryCh
		if primaryRes.err != nil {
			slog.Error("primary_request_failed",
				"req_id", reqID,
				"x_request_id", corrID,
				"remote", remoteAddr,
				"method", method,
				"path", path,
				"err", primaryRes.err.Error(),
				"dur_ms", primaryRes.duration.Milliseconds(),
			)
			c.AbortWithStatus(http.StatusBadGateway)

			return
		}

		// Respond to client from primary immediately:
		writeSelectedHeaders(c.Writer, primaryRes.header, cfg.ForwardResponseHeaders)
		// Always include request id to client to correlate.
		c.Writer.Header().Set("X-Request-ID", corrID)
		c.Status(primaryRes.status)

		if len(primaryRes.body) > 0 {
			_, _ = c.Writer.Write(primaryRes.body)
		}

		// Collect shadow result (bounded)
		var shadowRes BackendResult
		shadowOK := false
		shadowErr := ""

		if shadowStarted {
			select {
			case shadowRes = <-shadowCh:
				shadowOK = shadowRes.err == nil
				shadowErr = errString(shadowRes.err)
			case <-shadowCtx.Done():
				shadowRes = BackendResult{
					kind:      BackendShadow,
					err:       shadowCtx.Err(),
					duration:  cfg.ShadowTimeout,
					requestID: reqID,
				}
				shadowOK = false
				shadowErr = errString(shadowRes.err)
			}
		} else if doShadow {
			shadowErr = "shadow_not_started"
		}

		// Compare headers (includeEmpty=false keeps logs small)
		pKV, sKV, diffs, hasDiff := compareHeadersDetailed(primaryRes.header, shadowRes.header, cfg.CompareHeaders, false)

		// Optional session logging control
		if cfg.LogSessionOnlyOnDiff && !hasDiff && !forcedShadow {
			delete(pKV, "X-Nauthilus-Session")
			delete(sKV, "X-Nauthilus-Session")

			filtered := make([]HeaderDiff, 0, len(diffs))

			for _, d := range diffs {
				if d.Key == "X-Nauthilus-Session" {
					continue
				}

				filtered = append(filtered, d)
			}

			diffs = filtered
		}

		// Single structured log line
		slog.Info("auth_proxy",
			"req_id", reqID,
			"x_request_id", corrID,
			"x_request_id_generated", corrGenerated,
			"remote", remoteAddr,
			"method", method,
			"path", path,
			"query", rawQuery,

			"shadow_enabled", doShadow,
			"shadow_forced", forcedShadow,
			"shadow_started", shadowStarted,

			"primary_status", primaryRes.status,
			"primary_proto", primaryRes.proto,
			"primary_dur_ms", primaryRes.duration.Milliseconds(),
			"primary_headers", pKV,

			"shadow_ok", shadowOK,
			"shadow_status", shadowRes.status,
			"shadow_proto", shadowRes.proto,
			"shadow_dur_ms", shadowRes.duration.Milliseconds(),
			"shadow_err", shadowErr,
			"shadow_headers", sKV,

			"diff", hasDiff,
			"diffs", diffs,
		)
	})

	// --- HTTP server (TLS -> HTTP/2 automatically) ---------------------------

	if cfg.TLSCertFile == "" || cfg.TLSKeyFile == "" {
		slog.Error("TLS_CERT and TLS_KEY must be set for HTTPS/HTTP2 inbound")
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		// (optional) If you want to cap request bodies:
		// MaxHeaderBytes: 1 << 20,
		// ReadTimeout/WriteTimeout are tricky for long-lived clients; for auth they’re fine.
	}

	// Graceful shutdown
	go func() {
		// start server
		slog.Info("httpproxy starting", "version", version)
		slog.Info("listening", "addr", "https://"+cfg.ListenAddr, "proto", "HTTP/2 via ALPN")
		slog.Info("backends", "primary", cfg.PrimaryBaseURL.String(), "shadow", cfg.ShadowBaseURL.String())

		if cfg.RootCAPath != "" || cfg.PrimaryRootCA != "" || cfg.ShadowRootCA != "" {
			slog.Info("upstream CA",
				"root", cfg.RootCAPath,
				"primary", cfg.PrimaryRootCA,
				"shadow", cfg.ShadowRootCA,
				"insecure", cfg.InsecureUpstream,
			)
		}

		if err := srv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	// Wait for SIGINT/SIGTERM
	stop := make(chan os.Signal, 2)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_ = srv.Shutdown(ctx)
}

// --- end --------------------------------------------------------------------
