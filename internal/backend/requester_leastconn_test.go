package backend

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"doppelgaenger/internal/failover"
)

// blockingBackend holds every request until release is closed.
type blockingBackend struct {
	server  *httptest.Server
	entered chan struct{}
	release chan struct{}
}

func newBlockingBackend(t *testing.T) *blockingBackend {
	t.Helper()

	backend := &blockingBackend{entered: make(chan struct{}, 64), release: make(chan struct{})}
	backend.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backend.entered <- struct{}{}

		select {
		case <-backend.release:
		case <-r.Context().Done():
		}

		w.WriteHeader(http.StatusOK)
	}))

	t.Cleanup(func() {
		backend.unblock()
		backend.server.Close()
	})

	return backend
}

func (b *blockingBackend) unblock() {
	select {
	case <-b.release:
	default:
		close(b.release)
	}
}

func (b *blockingBackend) waitEntered(t *testing.T) {
	t.Helper()

	select {
	case <-b.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not reach the blocking backend")
	}
}

func leastConnRequester(bases []*url.URL, opts ...RequesterOption) (Requester, *LeastConnSelector) {
	selector := &LeastConnSelector{}

	return NewRequester(BackendPrimary, bases, selector, nil, 0, HTTPClientConfig{}, nil, opts...), selector
}

func assertNoInFlight(t *testing.T, selector *LeastConnSelector, backendCount int) {
	t.Helper()

	for i := range backendCount {
		if got := selector.load.InFlight(i); got != 0 {
			t.Fatalf("backend %d still has %d requests in flight", i, got)
		}
	}
}

func TestNewSelectorLeastConn(t *testing.T) {
	if _, ok := NewSelector(" Least_Conn ").(*LeastConnSelector); !ok {
		t.Fatalf("NewSelector(least_conn) = %T, want *LeastConnSelector", NewSelector(SelectionLeastConn))
	}

	var _ AttemptTracker = &LeastConnSelector{}

	var _ FallbackOrderer = &LeastConnSelector{}
}

// An unbound selector has nothing to balance on and must still distribute.
func TestLeastConnSelectorUnboundFallsBackToRoundRobin(t *testing.T) {
	selector := &LeastConnSelector{}

	selector.Acquire(1)
	selector.Release(1)

	for _, want := range []int{0, 1, 2, 0} {
		if got := selector.Select(Request{}, 3); got != want {
			t.Fatalf("unbound Select() = %d, want %d", got, want)
		}
	}

	if got := selector.Order(Request{}, 1, 3); len(got) != 3 || got[0] != 1 {
		t.Fatalf("unbound Order() = %v, want ring order from 1", got)
	}
}

func TestRequesterLeastConnIdlePoolSpreads(t *testing.T) {
	backends := []*countingBackend{newCountingBackend(t, http.StatusOK), newCountingBackend(t, http.StatusOK), newCountingBackend(t, http.StatusOK)}
	requester, selector := leastConnRequester([]*url.URL{backends[0].url(t), backends[1].url(t), backends[2].url(t)})

	const requests = 300

	for _, backend := range backends {
		backend.bodies = make(chan string, requests)
	}

	for range requests {
		if result := requester.Do(postRequest(context.Background())); result.Err != nil || result.Attempts != 1 {
			t.Fatalf("request failed: err=%v attempts=%d", result.Err, result.Attempts)
		}
	}

	// Equal backends are chosen at random: 100 expected, 8 standard deviation.
	for i, backend := range backends {
		if hits := backend.hits.Load(); hits < 50 {
			t.Fatalf("backend %d hits = %d of %d, want about a third", i, hits, requests)
		}
	}

	assertNoInFlight(t, selector, len(backends))
}

// holdFirstBackends returns backends that answer at once, except that the
// very first request to any of them is held until release is closed. held
// receives the index of the backend that holds it.
func holdFirstBackends(t *testing.T, count int) (urls []*url.URL, hits []*atomic.Int64, held chan int, release chan struct{}) {
	t.Helper()

	var armed atomic.Bool

	armed.Store(true)

	held = make(chan int, 1)
	release = make(chan struct{})

	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})

	for i := range count {
		counter := &atomic.Int64{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			counter.Add(1)

			if armed.CompareAndSwap(true, false) {
				held <- i

				select {
				case <-release:
				case <-r.Context().Done():
				}
			}

			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(server.Close)

		urls = append(urls, mustParseURL(t, server.URL))
		hits = append(hits, counter)
	}

	return urls, hits, held, release
}

func TestRequesterLeastConnAvoidsBusyBackend(t *testing.T) {
	urls, hits, held, release := holdFirstBackends(t, 2)
	requester, selector := leastConnRequester(urls)

	pending := make(chan Result, 1)

	go func() {
		pending <- requester.Do(postRequest(context.Background()))
	}()

	var busy int

	select {
	case busy = <-held:
	case <-time.After(5 * time.Second):
		t.Fatal("first request did not reach a backend")
	}

	idle := 1 - busy

	if got := selector.load.InFlight(busy); got != 1 {
		t.Fatalf("busy backend in flight = %d, want 1", got)
	}

	for range 10 {
		result := requester.Do(postRequest(context.Background()))
		if result.Err != nil || result.Selected != urls[idle].Redacted() {
			t.Fatalf("request went to the busy backend: err=%v selected=%q", result.Err, result.Selected)
		}
	}

	close(release)

	if result := <-pending; result.Err != nil {
		t.Fatalf("held request failed: %v", result.Err)
	}

	if hits[busy].Load() != 1 || hits[idle].Load() != 10 {
		t.Fatalf("hits: busy=%d idle=%d, want 1 and 10", hits[busy].Load(), hits[idle].Load())
	}

	assertNoInFlight(t, selector, 2)
}

// With round robin the neighbour of an unreachable backend carries its share
// as well. least_conn must spread that share over all remaining backends.
func TestRequesterLeastConnFailoverSpreadsOverSurvivors(t *testing.T) {
	first := newCountingBackend(t, http.StatusOK)
	second := newCountingBackend(t, http.StatusOK)

	const requests = 200

	first.bodies = make(chan string, requests)
	second.bodies = make(chan string, requests)

	policy := testFailoverPolicy()
	policy.Cooldown = time.Minute

	requester, selector := leastConnRequester([]*url.URL{closedBackendURL(t), first.url(t), second.url(t)}, WithFailover(policy))

	failovers := 0

	for range requests {
		result := requester.Do(postRequest(context.Background()))
		if result.Err != nil {
			t.Fatalf("request failed despite failover: %v", result.Err)
		}

		if result.Attempts > 1 {
			failovers++
		}
	}

	// The unreachable backend is found once and then stays in cooldown.
	if failovers != 1 {
		t.Fatalf("requests that hit the unreachable backend = %d, want exactly one", failovers)
	}

	// Round robin would give its neighbour two thirds. Here: 100 expected
	// each, 7 standard deviation.
	firstHits, secondHits := first.hits.Load(), second.hits.Load()
	if firstHits+secondHits != requests || firstHits < 60 || secondHits < 60 {
		t.Fatalf("survivor hits = %d and %d, want an even split of %d", firstHits, secondHits, requests)
	}

	assertNoInFlight(t, selector, 3)
}

// Without the cooldown feedback an unreachable backend never has a request in
// flight and would be the first choice of every request.
func TestRequesterLeastConnAvoidsUnreachableBackendWithoutFailover(t *testing.T) {
	live := newCountingBackend(t, http.StatusOK)

	const requests = 100

	live.bodies = make(chan string, requests)

	var logs bytes.Buffer

	requester, selector := leastConnRequester([]*url.URL{closedBackendURL(t), live.url(t)},
		WithFailover(failover.Policy{Enabled: false, Cooldown: time.Minute}),
		WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))

	failed := 0

	for range requests {
		result := requester.Do(postRequest(context.Background()))
		if result.Attempts != 1 || result.FailoverFrom != "" {
			t.Fatalf("disabled failover must keep the single attempt: attempts=%d from=%q", result.Attempts, result.FailoverFrom)
		}

		if result.Err != nil {
			failed++
		}
	}

	// One request finds the backend unreachable; all later ones avoid it.
	if failed != 1 || live.hits.Load() != requests-1 {
		t.Fatalf("failed requests = %d, live hits = %d, want 1 and %d", failed, live.hits.Load(), requests-1)
	}

	// The failed attempt is the only sign that the backend left the selection.
	if got := strings.Count(logs.String(), "backend_attempt_failed"); got != 1 {
		t.Fatalf("attempt failure records = %d, want 1: %s", got, logs.String())
	}

	assertNoInFlight(t, selector, 2)
}

// resettingBackend accepts connections and drops every request without an
// answer, like a listener whose application behind it is gone.
func resettingBackendURL(t *testing.T) (*url.URL, *atomic.Int64) {
	t.Helper()

	var hits atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)

		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	t.Cleanup(server.Close)

	return mustParseURL(t, server.URL), &hits
}

// A backend that fails right after the connection was accepted has nothing in
// flight. Without a cooldown least_conn would send it almost every request.
func TestRequesterLeastConnAvoidsBackendThatDropsExchanges(t *testing.T) {
	broken, brokenHits := resettingBackendURL(t)
	live := newCountingBackend(t, http.StatusOK)

	const requests = 100

	live.bodies = make(chan string, requests)

	var logs bytes.Buffer

	requester, selector := leastConnRequester([]*url.URL{broken, live.url(t)},
		WithFailover(failover.Policy{Enabled: false, Cooldown: time.Minute}),
		WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))

	failed := 0

	for range requests {
		if result := requester.Do(postRequest(context.Background())); result.Err != nil {
			failed++
		}
	}

	if failed != 1 || brokenHits.Load() != 1 || live.hits.Load() != requests-1 {
		t.Fatalf("failed=%d broken hits=%d live hits=%d, want 1, 1 and %d", failed, brokenHits.Load(), live.hits.Load(), requests-1)
	}

	if got := strings.Count(logs.String(), "backend_attempt_failed"); got != 1 {
		t.Fatalf("attempt failure records = %d, want 1: %s", got, logs.String())
	}

	assertNoInFlight(t, selector, 2)
}

// A healthy backend closes idle keep-alive connections. A request written in
// that moment fails, but it must not take the backend out of the selection.
func TestRequesterLeastConnKeepsBackendAfterReusedConnectionDropped(t *testing.T) {
	flaky := newKeepAliveDyingBackend(t)
	base := mustParseURL(t, "http://"+flaky.listener.Addr().String())

	instance, selector := leastConnRequester([]*url.URL{base, base},
		WithFailover(failover.Policy{Enabled: false, Cooldown: time.Minute}))

	if result := instance.Do(postRequest(context.Background())); result.Err != nil {
		t.Fatalf("first request on a fresh connection failed: %v", result.Err)
	}

	// The second request reuses the connection, which the backend drops.
	if result := instance.Do(postRequest(context.Background())); result.Err == nil {
		t.Fatal("request on the dropped keep-alive connection must fail")
	}

	backendRequester, ok := instance.(*requester)
	if !ok {
		t.Fatalf("NewRequester() = %T, want *requester", instance)
	}

	health := backendRequester.failover.health
	if !health.Available(0) || !health.Available(1) {
		t.Fatal("a dropped keep-alive connection started a cooldown")
	}

	assertNoInFlight(t, selector, 2)
}

func TestBrokenExchange(t *testing.T) {
	const backendURL = "http://backend.example"

	connected := connState{getConn: true, gotConn: true, everGotConn: true}
	timeout := &url.Error{Op: http.MethodPost, URL: backendURL, Err: context.DeadlineExceeded}
	reset := &url.Error{Op: http.MethodPost, URL: backendURL, Err: errors.New("connection reset by peer")}

	tests := []struct {
		name      string
		err       error
		conn      connState
		parentErr error
		want      bool
	}{
		{name: "response received", conn: connected},
		{name: "reset on a fresh connection", err: reset, conn: connected, want: true},
		{name: "reset on a reused connection", err: reset, conn: connState{getConn: true, gotConn: true, everGotConn: true, reused: true}},
		{name: "replay could not reconnect", err: reset, conn: connState{getConn: true, everGotConn: true, reused: true}, want: true},
		{name: "timeout keeps the backend", err: timeout, conn: connected},
		{name: "caller gave up", err: reset, conn: connected, parentErr: context.Canceled},
		{name: "local request error", err: reset},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := brokenExchange(tt.err, tt.conn, tt.parentErr); got != tt.want {
				t.Fatalf("brokenExchange() = %v, want %v", got, tt.want)
			}
		})
	}
}

// When the cooldown ends and the backend is still unreachable, it is tried
// again, fails over, and returns to the cooldown. No request may fail.
func TestRequesterLeastConnReprobesAfterCooldown(t *testing.T) {
	live := newCountingBackend(t, http.StatusOK)
	live.bodies = make(chan string, 256)

	policy := testFailoverPolicy()
	policy.Cooldown = 10 * time.Millisecond

	requester, selector := leastConnRequester([]*url.URL{closedBackendURL(t), live.url(t)}, WithFailover(policy))

	probes := 0

	// Every round starts after the cooldown has ended. How often the backend
	// is probed depends on timing and chance, so only a lower bound holds.
	for range 12 {
		for range 6 {
			result := requester.Do(postRequest(context.Background()))
			if result.Err != nil {
				t.Fatalf("request failed despite failover: %v", result.Err)
			}

			if result.Attempts > 1 {
				probes++
			}
		}

		time.Sleep(2 * policy.Cooldown)
	}

	if probes < 2 {
		t.Fatalf("requests that probed the unreachable backend = %d, want it probed again after the cooldown", probes)
	}

	assertNoInFlight(t, selector, 2)
}

func TestRequesterLeastConnRetryOnlyKeepsSelectedBackend(t *testing.T) {
	live := newCountingBackend(t, http.StatusOK)
	live.bodies = make(chan string, 256)

	policy := testFailoverPolicy()
	policy.MaxTargets = 1
	policy.Retries = 2
	policy.Cooldown = time.Minute

	requester, selector := leastConnRequester([]*url.URL{closedBackendURL(t), live.url(t)},
		WithFailover(policy), withFailoverClock(&recordingClock{}))

	failed := 0

	for range 100 {
		result := requester.Do(postRequest(context.Background()))
		if result.Err == nil {
			continue
		}

		failed++

		if result.Attempts != 3 || result.FailoverFrom != "" {
			t.Fatalf("retry-only request moved to another backend: attempts=%d from=%q", result.Attempts, result.FailoverFrom)
		}
	}

	// The request that selected the unreachable backend retries it and fails;
	// afterwards the backend is in cooldown and no longer selected.
	if failed != 1 || live.hits.Load() != 99 {
		t.Fatalf("failed requests = %d, live hits = %d, want 1 and 99", failed, live.hits.Load())
	}

	assertNoInFlight(t, selector, 2)
}

func TestRequesterLeastConnReleasesSlotOfCancelledRequest(t *testing.T) {
	busy := newBlockingBackend(t)
	requester, selector := leastConnRequester([]*url.URL{mustParseURL(t, busy.server.URL), mustParseURL(t, busy.server.URL)})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Result, 1)

	go func() {
		done <- requester.Do(postRequest(ctx))
	}()

	busy.waitEntered(t)
	cancel()

	if result := <-done; result.Err == nil {
		t.Fatal("cancelled request must fail")
	}

	assertNoInFlight(t, selector, 2)
}

func TestRequesterLeastConnSingleBackendIsNotTracked(t *testing.T) {
	live := newCountingBackend(t, http.StatusOK)
	instance, selector := leastConnRequester([]*url.URL{live.url(t)})

	backendRequester, ok := instance.(*requester)
	if !ok {
		t.Fatalf("NewRequester() = %T, want *requester", instance)
	}

	if backendRequester.tracker != nil || backendRequester.failover.engaged || selector.load != nil {
		t.Fatal("a single backend must not be tracked")
	}

	if result := instance.Do(postRequest(context.Background())); result.Err != nil || result.Attempts != 1 {
		t.Fatalf("request failed: err=%v attempts=%d", result.Err, result.Attempts)
	}
}

func TestRequesterLeastConnConcurrentRequests(t *testing.T) {
	const (
		workers  = 48
		requests = 20
	)

	backends := []*countingBackend{newCountingBackend(t, http.StatusOK), newCountingBackend(t, http.StatusOK), newCountingBackend(t, http.StatusOK)}
	for _, backend := range backends {
		backend.bodies = make(chan string, workers*requests)
	}

	policy := testFailoverPolicy()
	policy.Cooldown = time.Minute

	requester, selector := leastConnRequester(
		[]*url.URL{backends[0].url(t), closedBackendURL(t), backends[1].url(t), backends[2].url(t)}, WithFailover(policy))

	var wg sync.WaitGroup

	errs := make(chan error, workers*requests)

	for range workers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for range requests {
				if result := requester.Do(postRequest(context.Background())); result.Err != nil {
					errs <- result.Err
				}
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent request failed: %v", err)
	}

	total := int64(0)

	for i, backend := range backends {
		hits := backend.hits.Load()
		if hits == 0 {
			t.Fatalf("backend %d received no request", i)
		}

		total += hits
	}

	if total != workers*requests {
		t.Fatalf("total hits = %d, want %d", total, workers*requests)
	}

	assertNoInFlight(t, selector, 4)
}
