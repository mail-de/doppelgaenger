package ratelimit

import (
	"testing"
	"time"
)

func TestTokenBucketAllow(t *testing.T) {
	tb := NewTokenBucket(1, 1)

	if !tb.Allow() {
		t.Fatalf("expected first token to be allowed")
	}

	if tb.Allow() {
		t.Fatalf("expected token bucket to be empty")
	}

	time.Sleep(1100 * time.Millisecond)

	if !tb.Allow() {
		t.Fatalf("expected token bucket to refill")
	}
}
