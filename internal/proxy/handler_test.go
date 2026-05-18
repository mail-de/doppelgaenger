package proxy

import (
	"math/rand"
	"net/http"
	"testing"
)

func TestEnsureRequestIDPreservesHeader(t *testing.T) {
	h := &Handler{rng: rand.New(rand.NewSource(1))}
	header := http.Header{}
	header.Set("X-Request-ID", "existing")

	id, generated := h.ensureRequestID(header)
	if generated {
		t.Fatalf("expected existing request ID to be reused")
	}

	if id != "existing" {
		t.Fatalf("expected request ID to be preserved, got %q", id)
	}
}

func TestEnsureRequestIDGenerates(t *testing.T) {
	h := &Handler{rng: rand.New(rand.NewSource(1))}
	header := http.Header{}

	id, generated := h.ensureRequestID(header)
	if !generated {
		t.Fatalf("expected request ID to be generated")
	}

	if id == "" {
		t.Fatalf("expected generated request ID to be non-empty")
	}

	if header.Get("X-Request-ID") == "" {
		t.Fatalf("expected request ID to be set in header")
	}
}
