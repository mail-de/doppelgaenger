// Package health exposes the local process readiness state.
package health

import (
	"encoding/json"
	"net/http"
	"sync/atomic"

	"doppelgaenger/internal/config"
)

const (
	// Path is the unauthenticated local health endpoint exposed on the
	// observability HTTP server.
	Path = "/healthz"

	statusOK       = "ok"
	statusNotReady = "not_ready"
)

// State tracks whether the configured proxy listener reached serving state.
type State struct {
	protocol string

	ready        atomic.Bool
	shuttingDown atomic.Bool
}

// Snapshot is the serializable health state.
type Snapshot struct {
	Status       string `json:"status"`
	Protocol     string `json:"protocol,omitempty"`
	Ready        bool   `json:"ready"`
	ShuttingDown bool   `json:"shutting_down"`
}

// New constructs a process health state from the effective configuration.
func New(cfg config.Config) *State {
	return &State{protocol: cfg.Protocol}
}

// MarkReady reports that the active proxy listener has been initialized.
func (s *State) MarkReady() {
	if s == nil {
		return
	}

	s.shuttingDown.Store(false)
	s.ready.Store(true)
}

// MarkNotReady reports that the active proxy listener is not serving.
func (s *State) MarkNotReady() {
	if s == nil {
		return
	}

	s.ready.Store(false)
}

// MarkShuttingDown reports that the process is leaving service.
func (s *State) MarkShuttingDown() {
	if s == nil {
		return
	}

	s.shuttingDown.Store(true)
	s.ready.Store(false)
}

// Snapshot returns the current process health state.
func (s *State) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{
			Status:       statusNotReady,
			Ready:        false,
			ShuttingDown: false,
		}
	}

	ready := s.ready.Load()
	shuttingDown := s.shuttingDown.Load()

	if ready && !shuttingDown {
		return Snapshot{
			Status:       statusOK,
			Protocol:     s.protocol,
			Ready:        true,
			ShuttingDown: false,
		}
	}

	return Snapshot{
		Status:       statusNotReady,
		Protocol:     s.protocol,
		Ready:        false,
		ShuttingDown: shuttingDown,
	}
}

// Handler serves a minimal readiness response without checking backends.
func (s *State) Handler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)

		return
	}

	snapshot := s.Snapshot()
	statusCode := http.StatusServiceUnavailable

	if snapshot.Ready {
		statusCode = http.StatusOK
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(statusCode)

	if r.Method == http.MethodHead {
		return
	}

	_ = json.NewEncoder(w).Encode(snapshot)
}
