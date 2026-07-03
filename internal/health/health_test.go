package health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"doppelgaenger/internal/config"
)

const testProtocolGRPC = "grpc"

func TestHandlerReportsReadinessLifecycle(t *testing.T) {
	state := New(config.Config{Protocol: testProtocolGRPC})

	assertHealth(t, state, http.StatusServiceUnavailable, statusNotReady, false, false)

	state.MarkReady()
	assertHealth(t, state, http.StatusOK, statusOK, true, false)

	state.MarkShuttingDown()
	assertHealth(t, state, http.StatusServiceUnavailable, statusNotReady, false, true)
}

func TestHandlerRejectsUnsupportedMethods(t *testing.T) {
	state := New(config.Config{Protocol: "grpc"})

	w := httptest.NewRecorder()
	state.Handler(w, httptest.NewRequest(http.MethodPost, Path, nil))

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status 405, got %d", w.Code)
	}
}

func assertHealth(t *testing.T, state *State, wantCode int, wantStatus string, wantReady bool, wantShutdown bool) {
	t.Helper()

	w := httptest.NewRecorder()
	state.Handler(w, httptest.NewRequest(http.MethodGet, Path, nil))

	if w.Code != wantCode {
		t.Fatalf("expected status code %d, got %d", wantCode, w.Code)
	}

	var got Snapshot
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode health response: %v", err)
	}

	if got.Status != wantStatus {
		t.Fatalf("expected status %q, got %q", wantStatus, got.Status)
	}

	if got.Protocol != testProtocolGRPC {
		t.Fatalf("expected protocol %s, got %q", testProtocolGRPC, got.Protocol)
	}

	if got.Ready != wantReady {
		t.Fatalf("expected ready=%t, got %t", wantReady, got.Ready)
	}

	if got.ShuttingDown != wantShutdown {
		t.Fatalf("expected shutting_down=%t, got %t", wantShutdown, got.ShuttingDown)
	}
}
