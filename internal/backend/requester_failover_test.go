package backend

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"doppelgaenger/internal/failover"
)

const testIdempotencyKeyHeader = "Idempotency-Key"

type fixedSelector int

func (s fixedSelector) Select(Request, int) int {
	return int(s)
}

type recordingClock struct {
	mu     sync.Mutex
	sleeps []time.Duration
}

func (c *recordingClock) Now() time.Time {
	return time.Now()
}

func (c *recordingClock) Sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	c.sleeps = append(c.sleeps, d)
	c.mu.Unlock()

	return ctx.Err()
}

func testFailoverPolicy() failover.Policy {
	return failover.Policy{
		Enabled:           true,
		BackoffInitial:    10 * time.Millisecond,
		BackoffMultiplier: 2,
		BackoffMax:        100 * time.Millisecond,
		MaxWait:           5 * time.Second,
		AttemptTimeout:    time.Second,
	}
}

// closedBackendURL returns a URL whose port was just released, so connecting
// to it is refused before any request byte can be written.
func closedBackendURL(t *testing.T) *url.URL {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	addr := listener.Addr().String()
	_ = listener.Close()

	return mustParseURL(t, "http://"+addr)
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()

	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url %q: %v", raw, err)
	}

	return parsed
}

type countingBackend struct {
	server *httptest.Server
	hits   atomic.Int64
	bodies chan string
}

func newCountingBackend(t *testing.T, status int) *countingBackend {
	t.Helper()

	backend := &countingBackend{bodies: make(chan string, 64)}
	backend.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backend.hits.Add(1)

		body, _ := io.ReadAll(r.Body)
		backend.bodies <- string(body)

		w.WriteHeader(status)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(backend.server.Close)

	return backend
}

func (b *countingBackend) url(t *testing.T) *url.URL {
	t.Helper()

	return mustParseURL(t, b.server.URL)
}

func postRequest(ctx context.Context) Request {
	return Request{
		Ctx:        ctx,
		Kind:       BackendPrimary,
		Method:     http.MethodPost,
		Path:       "/submit",
		RemoteAddr: "192.0.2.10:4711",
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Body:       []byte("payload"),
		RequestID:  1,
	}
}

func TestClassifyHTTPAttempt(t *testing.T) {
	someErr := errors.New("boom")
	dialed := connState{getConn: true}
	connected := connState{getConn: true, gotConn: true, everGotConn: true}
	redialedAfterConnection := connState{getConn: true, everGotConn: true}

	tests := []struct {
		name       string
		err        error
		conn       connState
		replayable bool
		parentErr  error
		want       failover.Outcome
	}{
		{name: "success", conn: connected, want: failover.OutcomeDone},
		{name: "connect failure", err: someErr, conn: dialed, want: failover.OutcomeRetry},
		{name: "connect failure replayable", err: someErr, conn: dialed, replayable: true, want: failover.OutcomeRetry},
		{name: "local error before connecting", err: someErr, want: failover.OutcomeStop},
		{name: "failure after connection", err: someErr, conn: connected, want: failover.OutcomeStop},
		{name: "redial refused after unwritten try", err: someErr, conn: redialedAfterConnection, want: failover.OutcomeRetry},
		{name: "redial refused after replayable try", err: someErr, conn: redialedAfterConnection, replayable: true, want: failover.OutcomeStop},
		{name: "parent context ended", err: someErr, conn: dialed, parentErr: context.Canceled, want: failover.OutcomeStop},
		{name: "parent deadline before connection", err: someErr, conn: dialed, parentErr: context.DeadlineExceeded, want: failover.OutcomeStop},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyHTTPAttempt(tt.err, tt.conn, tt.replayable, tt.parentErr); got != tt.want {
				t.Fatalf("classifyHTTPAttempt() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTransportReplayable(t *testing.T) {
	tests := []struct {
		method string
		header http.Header
		want   bool
	}{
		{method: http.MethodGet, want: true},
		{method: http.MethodHead, want: true},
		{method: http.MethodOptions, want: true},
		{method: http.MethodTrace, want: true},
		{method: http.MethodPost, want: false},
		{method: http.MethodPut, want: false},
		{method: http.MethodPost, header: http.Header{testIdempotencyKeyHeader: {"k"}}, want: true},
		{method: http.MethodPatch, header: http.Header{"x-idempotency-key": {"k"}}, want: true},
	}

	for _, tt := range tests {
		req := &http.Request{Method: tt.method, Header: tt.header}
		if got := transportReplayable(req); got != tt.want {
			t.Fatalf("transportReplayable(%s, %v) = %v, want %v", tt.method, tt.header, got, tt.want)
		}
	}
}

// keepAliveDyingBackend answers the first request on a keep-alive connection,
// then reads the second request completely, closes its listener so new
// connections are refused, and drops the connection without answering. This
// is a backend that received a request and died while processing it.
type keepAliveDyingBackend struct {
	listener net.Listener
	seen     atomic.Int64
}

func newKeepAliveDyingBackend(t *testing.T) *keepAliveDyingBackend {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	backend := &keepAliveDyingBackend{listener: listener}

	t.Cleanup(func() { _ = listener.Close() })

	go backend.serve()

	return backend
}

func (b *keepAliveDyingBackend) serve() {
	conn, err := b.listener.Accept()
	if err != nil {
		return
	}

	defer func() { _ = conn.Close() }()

	reader := bufio.NewReader(conn)

	for {
		req, err := http.ReadRequest(reader)
		if err != nil {
			return
		}

		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()

		if b.seen.Add(1) == 1 {
			_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"))

			continue
		}

		_ = b.listener.Close()

		return
	}
}

func TestRequesterFailoverNeverMovesDeliveredReplayableRequest(t *testing.T) {
	tests := []struct {
		name   string
		method string
		header http.Header
	}{
		{name: "GET", method: http.MethodGet},
		{name: "POST", method: http.MethodPost},
		{name: "POST with Idempotency-Key", method: http.MethodPost, header: http.Header{testIdempotencyKeyHeader: {"abc"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dying := newKeepAliveDyingBackend(t)
			other := newCountingBackend(t, http.StatusOK)

			requester := NewRequester(BackendPrimary, []*url.URL{mustParseURL(t, "http://"+dying.listener.Addr().String()), other.url(t)}, fixedSelector(0), nil, 0,
				HTTPClientConfig{Protocol: HTTPProtocolHTTP1}, nil, WithFailover(testFailoverPolicy()))

			item := Request{Ctx: context.Background(), Kind: BackendPrimary, Method: tt.method, Path: "/x", Header: tt.header}

			if warmup := requester.Do(item); warmup.Err != nil || warmup.Status != http.StatusOK {
				t.Fatalf("warmup failed: err=%v status=%d", warmup.Err, warmup.Status)
			}

			result := requester.Do(item)

			if dying.seen.Load() != 2 {
				t.Fatalf("dying backend did not receive the second request: seen=%d", dying.seen.Load())
			}

			if other.hits.Load() != 0 {
				t.Fatalf("request delivered to the dying backend was executed again elsewhere: attempts=%d err=%v", result.Attempts, result.Err)
			}

			if result.Err == nil || result.Attempts != 1 {
				t.Fatalf("expected a final error after one attempt, got err=%v attempts=%d", result.Err, result.Attempts)
			}
		})
	}
}

func TestRequesterFailoverLocalRequestErrorIsNotRetried(t *testing.T) {
	live := newCountingBackend(t, http.StatusOK)
	policy := testFailoverPolicy()
	policy.Retries = 2

	requester := NewRequester(BackendPrimary, []*url.URL{live.url(t), live.url(t)}, fixedSelector(0), nil, 0, HTTPClientConfig{}, nil,
		WithFailover(policy))

	item := postRequest(context.Background())
	item.Header = http.Header{"Bad Header": {"x"}}

	result := requester.Do(item)
	if result.Err == nil || result.Attempts != 1 || live.hits.Load() != 0 {
		t.Fatalf("local request error was retried: err=%v attempts=%d hits=%d", result.Err, result.Attempts, live.hits.Load())
	}
}

func TestRequesterFailoverSendsPOSTToNextBackendExactlyOnce(t *testing.T) {
	dead := closedBackendURL(t)
	live := newCountingBackend(t, http.StatusOK)

	requester := NewRequester(BackendPrimary, []*url.URL{dead, live.url(t)}, fixedSelector(0), nil, 0, HTTPClientConfig{}, nil,
		WithFailover(testFailoverPolicy()))

	result := requester.Do(postRequest(context.Background()))
	if result.Err != nil {
		t.Fatalf("expected failover to succeed, got %v", result.Err)
	}

	if result.Status != http.StatusOK || result.Attempts != 2 {
		t.Fatalf("unexpected result status=%d attempts=%d", result.Status, result.Attempts)
	}

	if result.Selected != live.url(t).Redacted() || result.FailoverFrom != dead.Redacted() {
		t.Fatalf("unexpected selected=%q failover_from=%q", result.Selected, result.FailoverFrom)
	}

	if hits := live.hits.Load(); hits != 1 {
		t.Fatalf("live backend hits = %d, want 1", hits)
	}

	if body := <-live.bodies; body != "payload" {
		t.Fatalf("live backend body = %q, want full replayed body", body)
	}
}

func TestRequesterFailoverExhaustsRoundsWithBackoff(t *testing.T) {
	policy := testFailoverPolicy()
	policy.Retries = 2
	policy.Cooldown = 0
	clock := &recordingClock{}

	requester := NewRequester(BackendPrimary, []*url.URL{closedBackendURL(t), closedBackendURL(t)}, fixedSelector(0), nil, 0, HTTPClientConfig{}, nil,
		WithFailover(policy), withFailoverClock(clock))

	result := requester.Do(postRequest(context.Background()))
	if result.Err == nil {
		t.Fatal("expected all attempts to fail")
	}

	if result.Attempts != 6 {
		t.Fatalf("attempts = %d, want 6", result.Attempts)
	}

	clock.mu.Lock()
	defer clock.mu.Unlock()

	if len(clock.sleeps) != 2 || clock.sleeps[0] > clock.sleeps[1] {
		t.Fatalf("unexpected backoff sleeps %v", clock.sleeps)
	}
}

func TestRequesterFailoverDoesNotRepeatAfterConnection(t *testing.T) {
	var hijackHits atomic.Int64

	hijacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hijackHits.Add(1)

		_, _ = io.ReadAll(r.Body)

		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	t.Cleanup(hijacker.Close)

	live := newCountingBackend(t, http.StatusOK)

	requester := NewRequester(BackendPrimary, []*url.URL{mustParseURL(t, hijacker.URL), live.url(t)}, fixedSelector(0), nil, 0, HTTPClientConfig{}, nil,
		WithFailover(testFailoverPolicy()))

	result := requester.Do(postRequest(context.Background()))
	if result.Err == nil {
		t.Fatal("expected the closed connection to fail the request")
	}

	if result.Attempts != 1 || live.hits.Load() != 0 || hijackHits.Load() != 1 {
		t.Fatalf("request was repeated: attempts=%d live=%d hijacker=%d", result.Attempts, live.hits.Load(), hijackHits.Load())
	}
}

func TestRequesterFailoverDoesNotRetryStatusCodes(t *testing.T) {
	unavailable := newCountingBackend(t, http.StatusServiceUnavailable)
	live := newCountingBackend(t, http.StatusOK)

	requester := NewRequester(BackendPrimary, []*url.URL{unavailable.url(t), live.url(t)}, fixedSelector(0), nil, 0, HTTPClientConfig{}, nil,
		WithFailover(testFailoverPolicy()))

	result := requester.Do(postRequest(context.Background()))
	if result.Err != nil || result.Status != http.StatusServiceUnavailable || result.Attempts != 1 {
		t.Fatalf("unexpected result err=%v status=%d attempts=%d", result.Err, result.Status, result.Attempts)
	}

	if live.hits.Load() != 0 {
		t.Fatal("503 must not fail over")
	}
}

func TestRequesterFailoverDoesNotRetryResponseHeaderTimeout(t *testing.T) {
	release := make(chan struct{})

	var slowHits atomic.Int64

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		slowHits.Add(1)

		select {
		case <-release:
		case <-time.After(2 * time.Second):
		}

		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(slow.Close)
	// Cleanups run in reverse order: release the handler before closing the server.
	t.Cleanup(func() { close(release) })

	live := newCountingBackend(t, http.StatusOK)

	requester := NewRequester(BackendPrimary, []*url.URL{mustParseURL(t, slow.URL), live.url(t)}, fixedSelector(0), nil, 0,
		HTTPClientConfig{ResponseHeaderTimeout: 50 * time.Millisecond}, nil, WithFailover(testFailoverPolicy()))

	result := requester.Do(postRequest(context.Background()))
	if result.Err == nil {
		t.Fatal("expected response header timeout")
	}

	if result.Attempts != 1 || live.hits.Load() != 0 || slowHits.Load() != 1 {
		t.Fatalf("timeout after send was retried: attempts=%d live=%d slow=%d", result.Attempts, live.hits.Load(), slowHits.Load())
	}
}

func TestRequesterFailoverStopsOnCancelledContext(t *testing.T) {
	live := newCountingBackend(t, http.StatusOK)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	requester := NewRequester(BackendPrimary, []*url.URL{closedBackendURL(t), live.url(t)}, fixedSelector(0), nil, 0, HTTPClientConfig{}, nil,
		WithFailover(testFailoverPolicy()))

	result := requester.Do(postRequest(ctx))
	if result.Err == nil || result.Attempts != 1 || live.hits.Load() != 0 {
		t.Fatalf("cancelled request was retried: err=%v attempts=%d live=%d", result.Err, result.Attempts, live.hits.Load())
	}
}

func TestRequesterFailoverHTTP2(t *testing.T) {
	var liveHits atomic.Int64

	live := newHTTP2TLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		liveHits.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	t.Cleanup(live.Close)

	dead := closedBackendURL(t)
	dead.Scheme = "https"

	requester := NewRequester(BackendPrimary, []*url.URL{dead, mustParseURL(t, live.URL)}, fixedSelector(0), serverTLSConfig(t, live), 0,
		HTTPClientConfig{Protocol: HTTPProtocolHTTP2}, nil, WithFailover(testFailoverPolicy()))

	result := requester.Do(postRequest(context.Background()))
	if result.Err != nil || result.Proto != protoHTTP2 || result.Attempts != 2 || liveHits.Load() != 1 {
		t.Fatalf("unexpected HTTP/2 failover result err=%v proto=%q attempts=%d hits=%d", result.Err, result.Proto, result.Attempts, liveHits.Load())
	}
}

func TestRequesterFailoverHTTP2DoesNotRepeatAfterStreamReset(t *testing.T) {
	var abortHits atomic.Int64

	aborting := newHTTP2TLSServer(t, func(_ http.ResponseWriter, r *http.Request) {
		abortHits.Add(1)

		_, _ = io.ReadAll(r.Body)

		panic(http.ErrAbortHandler)
	})
	t.Cleanup(aborting.Close)

	var liveHits atomic.Int64

	live := newHTTP2TLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		liveHits.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	t.Cleanup(live.Close)

	tlsConfig := serverTLSConfig(t, aborting)
	tlsConfig.RootCAs.AddCert(live.Certificate())

	requester := NewRequester(BackendPrimary, []*url.URL{mustParseURL(t, aborting.URL), mustParseURL(t, live.URL)}, fixedSelector(0), tlsConfig, 0,
		HTTPClientConfig{Protocol: HTTPProtocolHTTP2}, nil, WithFailover(testFailoverPolicy()))

	result := requester.Do(postRequest(context.Background()))
	if result.Err == nil {
		t.Fatal("expected stream reset to fail the request")
	}

	if result.Attempts != 1 || liveHits.Load() != 0 || abortHits.Load() != 1 {
		t.Fatalf("reset stream was failed over: attempts=%d live=%d aborting=%d", result.Attempts, liveHits.Load(), abortHits.Load())
	}
}

func TestRequesterFailoverDisabledKeepsSingleAttempt(t *testing.T) {
	live := newCountingBackend(t, http.StatusOK)

	requester := NewRequester(BackendPrimary, []*url.URL{closedBackendURL(t), live.url(t)}, fixedSelector(0), nil, 0, HTTPClientConfig{}, nil,
		WithFailover(failover.Policy{Enabled: false, Retries: 3}))

	result := requester.Do(postRequest(context.Background()))
	if result.Err == nil || result.Attempts != 1 || result.FailoverFrom != "" || live.hits.Load() != 0 {
		t.Fatalf("disabled policy changed behavior: err=%v attempts=%d from=%q live=%d", result.Err, result.Attempts, result.FailoverFrom, live.hits.Load())
	}
}

func TestRequesterFailoverSourceIPHashKeepsAffinity(t *testing.T) {
	live := []*countingBackend{newCountingBackend(t, http.StatusOK), newCountingBackend(t, http.StatusOK), newCountingBackend(t, http.StatusOK)}
	dead := closedBackendURL(t)

	const deadIndex = 1

	bases := []*url.URL{live[0].url(t), dead, live[2].url(t)}
	selector := SourceIPHashSelector{}
	policy := testFailoverPolicy()
	policy.Cooldown = 0

	requester := NewRequester(BackendPrimary, bases, selector, nil, 0, HTTPClientConfig{}, nil, WithFailover(policy))

	affected, unaffected := clientsForBackend(t, selector, len(bases), deadIndex)

	wantFallback := selector.Order(Request{RemoteAddr: affected}, deadIndex, len(bases))[1]

	for range 3 {
		item := postRequest(context.Background())
		item.RemoteAddr = affected

		result := requester.Do(item)
		if result.Err != nil || result.Attempts != 2 || result.Selected != bases[wantFallback].Redacted() {
			t.Fatalf("affected client: err=%v attempts=%d selected=%q want %q", result.Err, result.Attempts, result.Selected, bases[wantFallback].Redacted())
		}
	}

	item := postRequest(context.Background())
	item.RemoteAddr = unaffected
	wantIndex := selector.Select(item, len(bases))

	result := requester.Do(item)
	if result.Err != nil || result.Attempts != 1 || result.Selected != bases[wantIndex].Redacted() {
		t.Fatalf("unaffected client moved: err=%v attempts=%d selected=%q", result.Err, result.Attempts, result.Selected)
	}
}

// clientsForBackend returns one client address hashed to index and one hashed elsewhere.
func clientsForBackend(t *testing.T, selector SourceIPHashSelector, backendCount, index int) (string, string) {
	t.Helper()

	affected, unaffected := "", ""

	for i := range 256 {
		remote := fmt.Sprintf("198.51.100.%d:1234", i)

		switch {
		case selector.Select(Request{RemoteAddr: remote}, backendCount) == index:
			if affected == "" {
				affected = remote
			}
		case unaffected == "":
			unaffected = remote
		}
	}

	if affected == "" || unaffected == "" {
		t.Fatal("could not find test client addresses")
	}

	return affected, unaffected
}

func TestRequesterFailoverCooldownSkipsFailedBackend(t *testing.T) {
	live := newCountingBackend(t, http.StatusOK)
	policy := testFailoverPolicy()
	policy.Cooldown = time.Minute

	requester := NewRequester(BackendPrimary, []*url.URL{closedBackendURL(t), live.url(t)}, fixedSelector(0), nil, 0, HTTPClientConfig{}, nil,
		WithFailover(policy))

	first := requester.Do(postRequest(context.Background()))
	if first.Err != nil || first.Attempts != 2 {
		t.Fatalf("first request: err=%v attempts=%d", first.Err, first.Attempts)
	}

	second := requester.Do(postRequest(context.Background()))
	if second.Err != nil || second.Attempts != 1 || second.FailoverFrom != "" {
		t.Fatalf("cooled-down backend was tried again: err=%v attempts=%d from=%q", second.Err, second.Attempts, second.FailoverFrom)
	}
}

func TestRequesterFailoverConcurrentRequests(t *testing.T) {
	live := newCountingBackend(t, http.StatusOK)
	live.bodies = make(chan string, 256)

	policy := testFailoverPolicy()
	policy.Cooldown = time.Millisecond

	requester := NewRequester(BackendPrimary, []*url.URL{closedBackendURL(t), live.url(t)}, &RoundRobinSelector{}, nil, 0, HTTPClientConfig{}, nil,
		WithFailover(policy))

	var wg sync.WaitGroup

	errs := make(chan error, 64)

	for range 64 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			if result := requester.Do(postRequest(context.Background())); result.Err != nil {
				errs <- result.Err
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent request failed: %v", err)
	}

	if hits := live.hits.Load(); hits != 64 {
		t.Fatalf("live hits = %d, want 64", hits)
	}
}
