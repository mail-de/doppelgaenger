package protocol

import (
	"context"
	"net/http"
	"testing"

	"doppelgaenger/internal/backend"
)

type mockPool struct {
	lastItem backend.WorkItem
}

func (m *mockPool) Enqueue(item backend.WorkItem) error {
	m.lastItem = item
	// Close response channel to avoid leaks, though not strictly necessary for this test
	go func() {
		item.ResponseCh <- backend.BackendResult{Status: 200}
	}()
	return nil
}

func TestHTTPAdapterUsesMappedPaths(t *testing.T) {
	primaryPool := &mockPool{}
	shadowPool := &mockPool{}
	adapter := HTTPAdapter{
		PrimaryPool: primaryPool,
		ShadowPool:  shadowPool,
	}

	event := Event{
		Path:        "/original",
		PrimaryPath: "/p-rewrite",
		ShadowPath:  "/s-rewrite",
		Method:      "GET",
		Header:      http.Header{},
	}

	// Test primary session
	ps, err := adapter.NewSession(context.Background(), TargetPrimary)
	if err != nil {
		t.Fatalf("failed to create primary session: %v", err)
	}
	if err := ps.Send(event); err != nil {
		t.Fatalf("failed to send event to primary: %v", err)
	}
	if primaryPool.lastItem.Path != "/p-rewrite" {
		t.Errorf("expected primary path to be %q, got %q", "/p-rewrite", primaryPool.lastItem.Path)
	}

	// Test shadow session
	ss, err := adapter.NewSession(context.Background(), TargetShadow)
	if err != nil {
		t.Fatalf("failed to create shadow session: %v", err)
	}
	if err := ss.Send(event); err != nil {
		t.Fatalf("failed to send event to shadow: %v", err)
	}
	if shadowPool.lastItem.Path != "/s-rewrite" {
		t.Errorf("expected shadow path to be %q, got %q", "/s-rewrite", shadowPool.lastItem.Path)
	}
}

func TestHTTPAdapterFallsBackToOriginalPath(t *testing.T) {
	primaryPool := &mockPool{}
	adapter := HTTPAdapter{PrimaryPool: primaryPool}

	event := Event{
		Path:   "/original",
		Method: "GET",
		Header: http.Header{},
	}

	ps, _ := adapter.NewSession(context.Background(), TargetPrimary)
	_ = ps.Send(event)

	if primaryPool.lastItem.Path != "/original" {
		t.Errorf("expected original path %q, got %q", "/original", primaryPool.lastItem.Path)
	}
}

func TestHTTPAdapterAddsConfiguredHeadersPerTarget(t *testing.T) {
	primaryPool := &mockPool{}
	shadowPool := &mockPool{}
	adapter := HTTPAdapter{
		PrimaryPool:           primaryPool,
		ShadowPool:            shadowPool,
		PrimaryRequestHeaders: map[string]string{"X-Backend": "primary", "X-Only-Primary": "yes"},
		ShadowRequestHeaders:  map[string]string{"X-Backend": "shadow", "X-Only-Shadow": "yes"},
	}

	event := Event{
		Path:   "/any",
		Method: "GET",
		Header: http.Header{"X-Backend": {"incoming"}, "X-Original": {"keep"}},
	}

	ps, err := adapter.NewSession(context.Background(), TargetPrimary)
	if err != nil {
		t.Fatalf("failed to create primary session: %v", err)
	}
	if err := ps.Send(event); err != nil {
		t.Fatalf("failed to send event to primary: %v", err)
	}

	if got := primaryPool.lastItem.Header.Get("X-Backend"); got != "primary" {
		t.Fatalf("expected primary header override %q, got %q", "primary", got)
	}
	if got := primaryPool.lastItem.Header.Get("X-Only-Primary"); got != "yes" {
		t.Fatalf("expected primary-only header %q, got %q", "yes", got)
	}
	if got := primaryPool.lastItem.Header.Get("X-Only-Shadow"); got != "" {
		t.Fatalf("did not expect shadow-only header on primary request, got %q", got)
	}

	ss, err := adapter.NewSession(context.Background(), TargetShadow)
	if err != nil {
		t.Fatalf("failed to create shadow session: %v", err)
	}
	if err := ss.Send(event); err != nil {
		t.Fatalf("failed to send event to shadow: %v", err)
	}

	if got := shadowPool.lastItem.Header.Get("X-Backend"); got != "shadow" {
		t.Fatalf("expected shadow header override %q, got %q", "shadow", got)
	}
	if got := shadowPool.lastItem.Header.Get("X-Only-Shadow"); got != "yes" {
		t.Fatalf("expected shadow-only header %q, got %q", "yes", got)
	}
	if got := shadowPool.lastItem.Header.Get("X-Only-Primary"); got != "" {
		t.Fatalf("did not expect primary-only header on shadow request, got %q", got)
	}

	if got := event.Header.Get("X-Backend"); got != "incoming" {
		t.Fatalf("expected original event header to stay unchanged, got %q", got)
	}
}
