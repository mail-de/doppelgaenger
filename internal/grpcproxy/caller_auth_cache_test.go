package grpcproxy

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"doppelgaenger/internal/config"
)

const (
	testCacheTTL        = 30 * time.Second
	testCacheMaxEntries = 16
	testCacheTokenCount = 8
	testCacheWaiters    = 16
	testCacheAbandoned  = 200

	testFlightTokenFirst  = "first-token"
	testFlightTokenSecond = "second-token"
	testFlightTokenThird  = "third-token"
)

func closeOnce(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func waitForCondition(t *testing.T, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}

		time.Sleep(time.Millisecond)
	}
}

func flightWaiters(h *Handler, token string) int {
	flights := &h.callerRuntime().flights
	flights.mu.Lock()
	defer flights.mu.Unlock()

	flight, ok := flights.pending[digestCallerToken(token)]
	if !ok {
		return 0
	}

	return flight.waiters
}

func cachedEntry(c *introspectionCache, key callerTokenDigest) (cachedIntrospection, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[key]

	return entry, ok
}

func withIntrospectionCache(ttl time.Duration, maxEntries int) func(*config.GRPCCallerAuthConfig) {
	return func(a *config.GRPCCallerAuthConfig) {
		a.IntrospectionCacheTTL = ttl
		a.IntrospectionCacheMaxEntries = maxEntries
	}
}

func authenticateTestCaller(h *Handler, token string, method string) error {
	return h.authenticateCaller(callerContext(context.Background(), token), method)
}

func validTestClaims(expires time.Time) introspectionClaims {
	return introspectionClaims{Active: true, Issuer: testCallerIssuer, Audience: json.RawMessage(`"` + testCallerAudience + `"`), Scope: testCallerScope, Expires: expires.Unix()}
}

func TestCallerIntrospectionCacheHit(t *testing.T) {
	t.Setenv(testCallerSecretEnv, testCallerSecret)

	calls := &atomic.Int64{}
	handler := newIntrospectionHandler(t, calls, respondIntrospectionClaims(nil), withIntrospectionCache(testCacheTTL, testCacheMaxEntries))

	for range 3 {
		if err := authenticateTestCaller(handler, testCallerToken, testFullMethodUnary); err != nil {
			t.Fatal(err)
		}
	}

	// Method and scope rules are enforced against the cached claims on every call.
	assertCallerAuthError(t, authenticateTestCaller(handler, testCallerToken, testFullMethodBidiStream), codes.PermissionDenied, callerCauseMethod, 0)

	if calls.Load() != 1 {
		t.Fatalf("expected one introspection, got %d", calls.Load())
	}

	if _, ok := cachedEntry(handler.callerAuth.cache, digestCallerToken(testCallerToken)); !ok {
		t.Fatal("cache is not keyed by the token digest")
	}
}

func TestCallerIntrospectionCacheDisabled(t *testing.T) {
	t.Setenv(testCallerSecretEnv, testCallerSecret)

	calls := &atomic.Int64{}
	handler := newIntrospectionHandler(t, calls, respondIntrospectionClaims(nil), withIntrospectionCache(0, testCacheMaxEntries))

	for range 2 {
		if err := authenticateTestCaller(handler, testCallerToken, testFullMethodUnary); err != nil {
			t.Fatal(err)
		}
	}

	if calls.Load() != 2 || handler.callerAuth.cache != nil {
		t.Fatalf("disabled cache reused a result: %d introspections", calls.Load())
	}
}

func TestCallerIntrospectionCacheSkipsNegativeResults(t *testing.T) {
	t.Setenv(testCallerSecretEnv, testCallerSecret)

	for _, tc := range []struct {
		name    string
		respond introspectionResponder
	}{
		{string(callerCauseInactive), respondIntrospectionClaims(map[string]any{testClaimActive: false})},
		{testCaseIssuer, respondIntrospectionClaims(map[string]any{testClaimIssuer: testCallerOtherIssuer})},
		{testCallerExpired, respondIntrospectionClaims(map[string]any{testClaimExpiry: time.Now().Add(-time.Minute).Unix()})},
		{testCaseNotBefore, respondIntrospectionClaims(map[string]any{testClaimNotBefore: time.Now().Add(time.Minute).Unix()})},
		{testCallerUnavailable, respondIntrospectionStatus(http.StatusServiceUnavailable)},
		{testCallerMalformed, respondIntrospectionBody("not-json")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := &atomic.Int64{}
			handler := newIntrospectionHandler(t, calls, tc.respond, withIntrospectionCache(testCacheTTL, testCacheMaxEntries))

			for range 2 {
				if err := authenticateTestCaller(handler, testCallerToken, testFullMethodUnary); err == nil {
					t.Fatal("negative result accepted")
				}
			}

			if calls.Load() != 2 || handler.callerAuth.cache.len() != 0 {
				t.Fatalf("negative result cached: %d introspections, %d entries", calls.Load(), handler.callerAuth.cache.len())
			}
		})
	}
}

func TestCallerIntrospectionCacheBoundedByTokenExpiry(t *testing.T) {
	t.Setenv(testCallerSecretEnv, testCallerSecret)

	expires := time.Now().Add(2 * time.Second)
	handler := newIntrospectionHandler(t, nil, respondIntrospectionClaims(map[string]any{testClaimExpiry: expires.Unix()}), withIntrospectionCache(testCacheTTL, testCacheMaxEntries))

	if err := authenticateTestCaller(handler, testCallerToken, testFullMethodUnary); err != nil {
		t.Fatal(err)
	}

	entry, ok := cachedEntry(handler.callerAuth.cache, digestCallerToken(testCallerToken))
	if !ok || entry.expires.After(time.Unix(expires.Unix(), 0)) {
		t.Fatalf("cache entry outlives token expiry: %+v", entry)
	}
}

// A cached entry whose claims are no longer valid (for example after clock
// adjustments) must be rejected at use time without trusting the entry.
func TestCallerIntrospectionCacheRevalidatesClaims(t *testing.T) {
	t.Setenv(testCallerSecretEnv, testCallerSecret)

	calls := &atomic.Int64{}
	handler := newIntrospectionHandler(t, calls, respondIntrospectionClaims(nil), withIntrospectionCache(testCacheTTL, testCacheMaxEntries))
	runtime := handler.callerRuntime()
	now := time.Now()

	for _, tc := range []struct {
		name   string
		claims introspectionClaims
	}{
		{testCallerExpired, validTestClaims(now.Add(-time.Second))},
		{testCaseNotBefore, func() introspectionClaims {
			claims := validTestClaims(now.Add(time.Minute))
			claims.NotBefore = now.Add(time.Minute).Unix()

			return claims
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime.cache.mu.Lock()
			runtime.cache.entries[digestCallerToken(testCallerToken)] = cachedIntrospection{claims: tc.claims, expires: now.Add(time.Minute)}
			runtime.cache.mu.Unlock()

			assertCallerAuthError(t, authenticateTestCaller(handler, testCallerToken, testFullMethodUnary), codes.Unauthenticated, callerCauseClaims, 0)
		})
	}

	if calls.Load() != 0 {
		t.Fatal("cached entry was bypassed")
	}
}

func TestIntrospectionCacheExpiry(t *testing.T) {
	now := time.Now()
	key := digestCallerToken(testCallerToken)

	cache := newIntrospectionCache(time.Minute, testCacheMaxEntries)
	cache.put(key, validTestClaims(now.Add(time.Hour)), now)

	if _, ok := cache.get(key, now.Add(time.Minute-time.Nanosecond)); !ok {
		t.Fatal("entry expired before TTL")
	}

	if _, ok := cache.get(key, now.Add(time.Minute)); ok {
		t.Fatal("entry survived TTL")
	}

	cache.put(key, validTestClaims(now.Add(10*time.Second)), now)

	if _, ok := cache.get(key, now.Add(10*time.Second)); ok {
		t.Fatal("entry survived token expiry")
	}

	cache.put(key, validTestClaims(now), now)

	if cache.len() != 0 {
		t.Fatal("expired token was cached")
	}

	var disabled *introspectionCache
	disabled.put(key, validTestClaims(now.Add(time.Hour)), now)

	if _, ok := disabled.get(key, now); ok || newIntrospectionCache(0, testCacheMaxEntries) != nil || newIntrospectionCache(time.Minute, 0) != nil {
		t.Fatal("disabled cache stored a result")
	}
}

func TestIntrospectionCacheMaxEntries(t *testing.T) {
	now := time.Now()
	cache := newIntrospectionCache(time.Minute, 2)
	shortLived := digestCallerToken("short")
	longLived := digestCallerToken("long")

	cache.put(shortLived, validTestClaims(now.Add(time.Second)), now)
	cache.put(longLived, validTestClaims(now.Add(time.Hour)), now)

	// Expired entries are evicted before live ones.
	later := now.Add(2 * time.Second)
	cache.put(digestCallerToken("third"), validTestClaims(now.Add(time.Hour)), later)

	if _, ok := cache.get(longLived, later); !ok || cache.len() != 2 {
		t.Fatalf("expired entry was not evicted first: %d entries", cache.len())
	}

	for i := range testCacheTokenCount {
		cache.put(digestCallerToken(fmt.Sprint("token-", i)), validTestClaims(now.Add(time.Hour)), later)
	}

	if cache.len() != 2 {
		t.Fatalf("cache exceeded max entries: %d", cache.len())
	}
}

func TestIntrospectionFlightsCollapseConcurrentRequests(t *testing.T) {
	var (
		flights introspectionFlights
		fetches atomic.Int64
	)

	release := make(chan struct{})
	fetch := func(context.Context) introspectionResult {
		fetches.Add(1)

		<-release

		return introspectionResult{claims: validTestClaims(time.Now().Add(time.Minute))}
	}

	key := digestCallerToken(testCallerToken)

	first, shared, err := flights.join(context.Background(), key, fetch)
	if err != nil || shared {
		t.Fatalf("first join: shared=%v err=%v", shared, err)
	}

	for range testCacheWaiters {
		flight, shared, err := flights.join(context.Background(), key, fetch)
		if err != nil || !shared || flight != first {
			t.Fatal("concurrent request started a second flight")
		}
	}

	close(release)
	<-first.done

	for range testCacheWaiters + 1 {
		flights.leave(key, first)
	}

	if fetches.Load() != 1 || first.result.err != nil || !first.result.claims.Active {
		t.Fatalf("unexpected flight result: fetches=%d err=%v", fetches.Load(), first.result.err)
	}

	assertFreshFlight(t, &flights, key, first)
}

func assertFreshFlight(t *testing.T, flights *introspectionFlights, key callerTokenDigest, finished *introspectionFlight) {
	t.Helper()

	next, _, _ := flights.join(context.Background(), key, func(context.Context) introspectionResult {
		return introspectionResult{err: errors.New("next")}
	})
	<-next.done
	flights.leave(key, next)

	if next == finished || next.result.err == nil || flights.running() != 0 {
		t.Fatal("finished flight was reused")
	}
}

func TestIntrospectionFlightCanceledWhenAllWaitersLeave(t *testing.T) {
	flights := introspectionFlights{slots: make(chan struct{}, 1)}
	key := digestCallerToken(testCallerToken)
	aborted := make(chan struct{})

	flight, _, err := flights.join(context.Background(), key, func(ctx context.Context) introspectionResult {
		<-ctx.Done()
		close(aborted)

		return introspectionResult{err: ctx.Err()}
	})
	if err != nil {
		t.Fatal(err)
	}

	if joined, shared, _ := flights.join(context.Background(), key, nil); joined != flight || !shared {
		t.Fatal("second waiter did not join")
	}

	flights.leave(key, flight)

	select {
	case <-aborted:
		t.Fatal("flight canceled while a waiter remained")
	default:
	}

	flights.leave(key, flight)
	<-aborted
	<-flight.done

	if flights.running() != 0 || len(flights.slots) != 0 {
		t.Fatalf("abandoned flight kept state: running=%d slots=%d", flights.running(), len(flights.slots))
	}
}

func TestCallerIntrospectionAbandonedFlightsStop(t *testing.T) {
	t.Setenv(testCallerSecretEnv, testCallerSecret)

	var active atomic.Int64

	handler := newIntrospectionHandler(t, nil, func(w http.ResponseWriter, r *http.Request) {
		// Reading the body lets the server observe the client's cancellation.
		_ = r.ParseForm()

		active.Add(1)
		defer active.Add(-1)

		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
			respondIntrospectionClaims(nil)(w, r)
		}
	}, withIntrospectionCache(testCacheTTL, testCacheMaxEntries))

	var wg sync.WaitGroup

	for i := range testCacheAbandoned {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
			defer cancel()

			err := handler.authenticateCaller(callerContext(ctx, fmt.Sprint(testCallerToken, i)), testFullMethodUnary)
			if code := status.Code(err); code != codes.DeadlineExceeded && code != codes.Unavailable {
				t.Errorf("unexpected result: %v", err)
			}
		})
	}

	wg.Wait()

	waitForCondition(t, func() bool { return handler.callerAuth.flights.running() == 0 && active.Load() == 0 })
}

func TestCallerIntrospectionOverload(t *testing.T) {
	t.Setenv(testCallerSecretEnv, testCallerSecret)

	arrived, release := make(chan struct{}, testCacheWaiters), make(chan struct{})
	handler := newIntrospectionHandler(t, nil, func(w http.ResponseWriter, r *http.Request) {
		arrived <- struct{}{}

		<-release

		respondIntrospectionClaims(nil)(w, r)
	}, func(a *config.GRPCCallerAuthConfig) { a.IntrospectionMaxConcurrent = 2 })
	t.Cleanup(func() { closeOnce(release) })

	results := make(chan error, 3)

	for _, token := range []string{testFlightTokenFirst, testFlightTokenSecond} {
		go func() { results <- authenticateTestCaller(handler, token, testFullMethodUnary) }()

		<-arrived
	}

	// A new token beyond the limit fails fast; the same token joins its flight.
	assertCallerAuthError(t, authenticateTestCaller(handler, testFlightTokenThird, testFullMethodUnary), codes.Unavailable, callerCauseOverload, 0)

	go func() { results <- authenticateTestCaller(handler, testFlightTokenFirst, testFullMethodUnary) }()

	waitForCondition(t, func() bool { return flightWaiters(handler, testFlightTokenFirst) == 2 })
	closeOnce(release)

	for range 3 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestCallerIntrospectionSharedFailure(t *testing.T) {
	t.Setenv(testCallerSecretEnv, testCallerSecret)

	calls := &atomic.Int64{}
	arrived, release := make(chan struct{}, 1), make(chan struct{})
	handler := newIntrospectionHandler(t, calls, func(w http.ResponseWriter, _ *http.Request) {
		arrived <- struct{}{}

		<-release

		w.WriteHeader(http.StatusServiceUnavailable)
	}, withIntrospectionCache(testCacheTTL, testCacheMaxEntries))
	t.Cleanup(func() { closeOnce(release) })

	results := make(chan error, testCacheWaiters)

	for range testCacheWaiters {
		go func() { results <- authenticateTestCaller(handler, testCallerToken, testFullMethodUnary) }()
	}

	<-arrived
	waitForCondition(t, func() bool { return flightWaiters(handler, testCallerToken) == testCacheWaiters })
	closeOnce(release)

	seen := map[*callerAuthError]bool{}

	for range testCacheWaiters {
		err := <-results
		assertCallerAuthError(t, err, codes.Unavailable, callerCauseHTTPStatus, http.StatusServiceUnavailable)

		seen[asCallerAuthError(err)] = true
	}

	if calls.Load() != 1 || len(seen) != testCacheWaiters || handler.callerAuth.cache.len() != 0 {
		t.Fatalf("shared failure: calls=%d distinct errors=%d cached=%d", calls.Load(), len(seen), handler.callerAuth.cache.len())
	}
}

func TestCallerIntrospectionConcurrentCallersShareRequest(t *testing.T) {
	t.Setenv(testCallerSecretEnv, testCallerSecret)

	calls := &atomic.Int64{}
	arrived, release := make(chan struct{}, testCacheWaiters), make(chan struct{})
	handler := newIntrospectionHandler(t, calls, func(w http.ResponseWriter, r *http.Request) {
		arrived <- struct{}{}

		<-release

		respondIntrospectionClaims(nil)(w, r)
	}, withIntrospectionCache(testCacheTTL, testCacheMaxEntries))
	t.Cleanup(func() { closeOnce(release) })

	results := make(chan error, testCacheWaiters)
	canceledCtx, cancel := context.WithCancel(context.Background())
	canceled := make(chan error, 1)

	go func() {
		canceled <- handler.authenticateCaller(callerContext(canceledCtx, testCallerToken), testFullMethodUnary)
	}()

	<-arrived

	for range testCacheWaiters {
		go func() { results <- authenticateTestCaller(handler, testCallerToken, testFullMethodUnary) }()
	}

	waitForCondition(t, func() bool { return flightWaiters(handler, testCallerToken) == testCacheWaiters+1 })

	// One waiter giving up must not fail the shared request for the others.
	cancel()
	assertCallerAuthError(t, <-canceled, codes.Canceled, callerCauseCanceled, 0)
	closeOnce(release)

	for range testCacheWaiters {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}

	if calls.Load() != 1 {
		t.Fatalf("expected one shared introspection, got %d", calls.Load())
	}
}

func TestCallerIntrospectionConcurrentTokens(t *testing.T) {
	t.Setenv(testCallerSecretEnv, testCallerSecret)

	handler := newIntrospectionHandler(t, nil, respondIntrospectionClaims(nil), withIntrospectionCache(testCacheTTL, testCacheTokenCount/2))

	var wg sync.WaitGroup

	errs := make(chan error, testCacheWaiters*testCacheTokenCount)

	for i := range testCacheWaiters * testCacheTokenCount {
		wg.Go(func() {
			errs <- authenticateTestCaller(handler, fmt.Sprint(testCallerToken, i%testCacheTokenCount), testFullMethodUnary)
		})
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	if entries := handler.callerAuth.cache.len(); entries > testCacheTokenCount/2 {
		t.Fatalf("cache exceeded max entries: %d", entries)
	}
}

// BenchmarkIntrospectionCachePutFull measures inserts into a full cache, the
// steady state for callers that present a fresh token on every request.
func BenchmarkIntrospectionCachePutFull(b *testing.B) {
	const entries = 10000

	now := time.Now()
	cache := newIntrospectionCache(time.Hour, entries)
	claims := validTestClaims(now.Add(2 * time.Hour))

	for i := range entries {
		cache.put(digestCallerToken(fmt.Sprint("seed-", i)), claims, now)
	}

	b.ResetTimer()

	for i := range b.N {
		var key callerTokenDigest

		binary.LittleEndian.PutUint64(key[:], uint64(i)+1)
		cache.put(key, claims, now)
	}
}
