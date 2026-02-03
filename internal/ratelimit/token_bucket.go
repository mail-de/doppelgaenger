package ratelimit

import (
	"sync"
	"time"
)

// Limiter defines the contract for rate limiting.
type Limiter interface {
	Allow() bool
}

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
