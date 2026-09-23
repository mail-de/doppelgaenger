package grpcproxy

import (
	"context"
	"crypto/sha256"
	"sync"
	"time"

	"go.opentelemetry.io/otel/trace"
)

// callerTokenDigest is the only token-derived value retained after a request;
// the raw token is never used as a cache or flight key.
type callerTokenDigest [sha256.Size]byte

func digestCallerToken(token string) callerTokenDigest {
	return sha256.Sum256([]byte(token))
}

type cachedIntrospection struct {
	claims  introspectionClaims
	expires time.Time
}

// introspectionCache holds positive introspection results only. A nil cache is
// disabled and safe to use.
type introspectionCache struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxEntries int
	entries    map[callerTokenDigest]cachedIntrospection
}

func newIntrospectionCache(ttl time.Duration, maxEntries int) *introspectionCache {
	if ttl <= 0 || maxEntries <= 0 {
		return nil
	}

	return &introspectionCache{ttl: ttl, maxEntries: maxEntries, entries: make(map[callerTokenDigest]cachedIntrospection)}
}

func (c *introspectionCache) get(key callerTokenDigest, now time.Time) (introspectionClaims, bool) {
	if c == nil {
		return introspectionClaims{}, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[key]
	if !ok {
		return introspectionClaims{}, false
	}

	if !now.Before(entry.expires) {
		delete(c.entries, key)

		return introspectionClaims{}, false
	}

	return entry.claims, true
}

// put stores claims until min(ttl, exp); callers must only pass validated claims.
func (c *introspectionCache) put(key callerTokenDigest, claims introspectionClaims, now time.Time) {
	if c == nil {
		return
	}

	expires := now.Add(c.ttl)
	if tokenExpiry := time.Unix(claims.Expires, 0); tokenExpiry.Before(expires) {
		expires = tokenExpiry
	}

	if !now.Before(expires) {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.entries[key]; !exists && len(c.entries) >= c.maxEntries {
		c.evictLocked(now)
	}

	c.entries[key] = cachedIntrospection{claims: claims, expires: expires}
}

// introspectionEvictionSample bounds the work of one eviction; a full cache
// costs O(sample) per insert instead of a scan over every entry.
const introspectionEvictionSample = 8

// evictLocked samples a few entries (map iteration starts at a random
// position), drops the expired ones and, if the cache is still full, the
// sampled entry that expires first. Only positive results are stored, so
// filling the cache requires valid tokens.
func (c *introspectionCache) evictLocked(now time.Time) {
	var (
		victim        callerTokenDigest
		victimExpires time.Time
		found         bool
		sampled       int
	)

	for key, entry := range c.entries {
		sampled++

		if !now.Before(entry.expires) {
			delete(c.entries, key)
		} else if !found || entry.expires.Before(victimExpires) {
			victim, victimExpires, found = key, entry.expires, true
		}

		if sampled >= introspectionEvictionSample {
			break
		}
	}

	if len(c.entries) >= c.maxEntries && found {
		delete(c.entries, victim)
	}
}

func (c *introspectionCache) len() int {
	if c == nil {
		return 0
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.entries)
}

// introspectionResult is shared read-only by every waiter of a flight.
type introspectionResult struct {
	claims introspectionClaims
	err    error
	span   trace.SpanContext
	// cached reports that the flight was answered by the cache without a request.
	cached bool
}

type introspectionFlight struct {
	done   chan struct{}
	cancel context.CancelFunc
	// waiters is guarded by introspectionFlights.mu.
	waiters int
	result  introspectionResult
}

// introspectionFlights collapses concurrent introspections of the same token
// into one request and bounds the number of requests in progress. A flight
// runs detached from any single caller, so one canceled caller does not fail
// the others; it is canceled once its last waiter has left.
type introspectionFlights struct {
	mu      sync.Mutex
	pending map[callerTokenDigest]*introspectionFlight
	// slots limits concurrent introspection requests; nil means unlimited.
	slots chan struct{}
}

// join registers the caller as a waiter of the pending flight for key or
// starts a new one. shared reports whether an existing flight was joined.
// Every successful join must be paired with leave.
func (g *introspectionFlights) join(parent context.Context, key callerTokenDigest, fetch func(context.Context) introspectionResult) (*introspectionFlight, bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if flight, ok := g.pending[key]; ok {
		flight.waiters++

		return flight, true, nil
	}

	if g.slots != nil {
		select {
		case g.slots <- struct{}{}:
		default:
			return nil, false, callerOverloaded()
		}
	}

	if g.pending == nil {
		g.pending = make(map[callerTokenDigest]*introspectionFlight)
	}

	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	flight := &introspectionFlight{done: make(chan struct{}), cancel: cancel, waiters: 1}
	g.pending[key] = flight

	go g.run(ctx, key, flight, fetch)

	return flight, false, nil
}

func (g *introspectionFlights) run(ctx context.Context, key callerTokenDigest, flight *introspectionFlight, fetch func(context.Context) introspectionResult) {
	defer func() {
		g.mu.Lock()
		g.forgetLocked(key, flight)
		g.mu.Unlock()

		flight.cancel()

		if g.slots != nil {
			<-g.slots
		}

		close(flight.done)
	}()

	flight.result = fetch(ctx)
}

// leave drops one waiter. When the last waiter leaves an unfinished flight,
// the flight is canceled and forgotten so later callers start a fresh one.
func (g *introspectionFlights) leave(key callerTokenDigest, flight *introspectionFlight) {
	g.mu.Lock()
	defer g.mu.Unlock()

	flight.waiters--
	if flight.waiters > 0 {
		return
	}

	select {
	case <-flight.done:
		return
	default:
	}

	g.forgetLocked(key, flight)
	flight.cancel()
}

func (g *introspectionFlights) forgetLocked(key callerTokenDigest, flight *introspectionFlight) {
	if g.pending[key] == flight {
		delete(g.pending, key)
	}
}

func (g *introspectionFlights) running() int {
	g.mu.Lock()
	defer g.mu.Unlock()

	return len(g.pending)
}
