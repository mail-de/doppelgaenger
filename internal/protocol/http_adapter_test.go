package protocol

import (
	"context"
	"net/http"
	"testing"

	"doppelgaenger/internal/backend"
)

const (
	originalPath  = "/original"
	primaryPath   = "/p-rewrite"
	shadowPath    = "/s-rewrite"
	httpGetMethod = "GET"
	anyPath       = "/any"
	headerBackend = "X-Backend"
	headerConn    = "Connection"
	headerXHop    = "X-Hop"
	headerXKeep   = "X-Keep"
	valueIncoming = "incoming"
	valueOK       = "ok"
	valueYes      = "yes"
)

type mockRequester struct {
	lastItem backend.Request
}

func (m *mockRequester) Do(item backend.Request) backend.Result {
	m.lastItem = item
	return backend.Result{Status: 200}
}

func TestHTTPAdapterUsesMappedPaths(t *testing.T) {
	primaryRequester := &mockRequester{}
	shadowRequester := &mockRequester{}
	adapter := HTTPAdapter{
		PrimaryRequester: primaryRequester,
		ShadowRequester:  shadowRequester,
	}

	event := Event{
		Path:        originalPath,
		PrimaryPath: primaryPath,
		ShadowPath:  shadowPath,
		Method:      httpGetMethod,
		Host:        "login.example.test",
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

	if primaryRequester.lastItem.Path != primaryPath {
		t.Errorf("expected primary path to be %q, got %q", primaryPath, primaryRequester.lastItem.Path)
	}

	if primaryRequester.lastItem.Host != event.Host {
		t.Errorf("expected primary host to be %q, got %q", event.Host, primaryRequester.lastItem.Host)
	}

	// Test shadow session
	ss, err := adapter.NewSession(context.Background(), TargetShadow)
	if err != nil {
		t.Fatalf("failed to create shadow session: %v", err)
	}

	if err := ss.Send(event); err != nil {
		t.Fatalf("failed to send event to shadow: %v", err)
	}

	if shadowRequester.lastItem.Path != shadowPath {
		t.Errorf("expected shadow path to be %q, got %q", shadowPath, shadowRequester.lastItem.Path)
	}
}

func TestHTTPAdapterFallsBackToOriginalPath(t *testing.T) {
	primaryRequester := &mockRequester{}
	adapter := HTTPAdapter{PrimaryRequester: primaryRequester}

	event := Event{
		Path:   originalPath,
		Method: httpGetMethod,
		Header: http.Header{},
	}

	ps, _ := adapter.NewSession(context.Background(), TargetPrimary)
	_ = ps.Send(event)

	if primaryRequester.lastItem.Path != originalPath {
		t.Errorf("expected original path %q, got %q", originalPath, primaryRequester.lastItem.Path)
	}
}

func TestHTTPAdapterAddsConfiguredHeadersPerTarget(t *testing.T) {
	primaryRequester := &mockRequester{}
	shadowRequester := &mockRequester{}
	adapter := HTTPAdapter{
		PrimaryRequester:      primaryRequester,
		ShadowRequester:       shadowRequester,
		PrimaryRequestHeaders: map[string]string{headerBackend: string(TargetPrimary), "X-Only-Primary": valueYes},
		ShadowRequestHeaders:  map[string]string{headerBackend: string(TargetShadow), "X-Only-Shadow": valueYes},
	}

	event := Event{
		Path:   anyPath,
		Method: httpGetMethod,
		Header: http.Header{headerBackend: {valueIncoming}, "X-Original": {"keep"}},
	}

	ps, err := adapter.NewSession(context.Background(), TargetPrimary)
	if err != nil {
		t.Fatalf("failed to create primary session: %v", err)
	}

	if err := ps.Send(event); err != nil {
		t.Fatalf("failed to send event to primary: %v", err)
	}

	if got := primaryRequester.lastItem.Header.Get(headerBackend); got != string(TargetPrimary) {
		t.Fatalf("expected primary header override %q, got %q", string(TargetPrimary), got)
	}

	if got := primaryRequester.lastItem.Header.Get("X-Only-Primary"); got != valueYes {
		t.Fatalf("expected primary-only header %q, got %q", valueYes, got)
	}

	if got := primaryRequester.lastItem.Header.Get("X-Only-Shadow"); got != "" {
		t.Fatalf("did not expect shadow-only header on primary request, got %q", got)
	}

	ss, err := adapter.NewSession(context.Background(), TargetShadow)
	if err != nil {
		t.Fatalf("failed to create shadow session: %v", err)
	}

	if err := ss.Send(event); err != nil {
		t.Fatalf("failed to send event to shadow: %v", err)
	}

	if got := shadowRequester.lastItem.Header.Get(headerBackend); got != string(TargetShadow) {
		t.Fatalf("expected shadow header override %q, got %q", string(TargetShadow), got)
	}

	if got := shadowRequester.lastItem.Header.Get("X-Only-Shadow"); got != valueYes {
		t.Fatalf("expected shadow-only header %q, got %q", valueYes, got)
	}

	if got := shadowRequester.lastItem.Header.Get("X-Only-Primary"); got != "" {
		t.Fatalf("did not expect primary-only header on shadow request, got %q", got)
	}

	if got := event.Header.Get(headerBackend); got != valueIncoming {
		t.Fatalf("expected original event header to stay unchanged, got %q", got)
	}
}

func TestHTTPAdapterRemovesHopByHopHeaders(t *testing.T) {
	primaryRequester := &mockRequester{}
	adapter := HTTPAdapter{PrimaryRequester: primaryRequester}

	event := Event{
		Path:   anyPath,
		Method: httpGetMethod,
		Header: http.Header{
			headerConn:  {headerXHop},
			headerXHop:  {"drop"},
			headerXKeep: {valueOK},
		},
	}

	ps, err := adapter.NewSession(context.Background(), TargetPrimary)
	if err != nil {
		t.Fatalf("failed to create primary session: %v", err)
	}

	if err := ps.Send(event); err != nil {
		t.Fatalf("failed to send event to primary: %v", err)
	}

	if got := primaryRequester.lastItem.Header.Get(headerConn); got != "" {
		t.Fatalf("expected Connection to be removed, got %q", got)
	}

	if got := primaryRequester.lastItem.Header.Get(headerXHop); got != "" {
		t.Fatalf("expected X-Hop to be removed, got %q", got)
	}

	if got := primaryRequester.lastItem.Header.Get(headerXKeep); got != valueOK {
		t.Fatalf("expected X-Keep to be preserved, got %q", got)
	}
}
