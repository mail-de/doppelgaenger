// Package compare compares primary and shadow backend responses.
package compare

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"doppelgaenger/internal/backend"
	"doppelgaenger/internal/config"
	"doppelgaenger/internal/headers"
)

const (
	// ModeNginx compares response headers in the nginx auth-request style.
	ModeNginx = "nginx"
	// ModeJSON compares response bodies as JSON.
	ModeJSON = "json"
	// ModeHTML compares response bodies as HTML text.
	ModeHTML = "html"
)

// Comparator compares primary and shadow responses.
type Comparator interface {
	Compare(primary, shadow backend.Result) (Result, error)
}

type headerAwareComparator interface {
	Comparator
	CompareWithHeaders(primary, shadow backend.Result, compareHeaders []string) (Result, error)
}

// Registry keeps the reusable HTTP comparators available for per-request selection.
type Registry struct {
	defaultMode string
	comparators map[string]headerAwareComparator
}

// Result captures comparison details for logging.
type Result struct {
	Mode           string
	Diff           bool
	HeaderDiff     bool
	HeaderPrimary  map[string]string
	HeaderShadow   map[string]string
	HeaderDiffs    []headers.HeaderDiff
	BodyDiff       bool
	JSONDiffs      []JSONPathDiff
	HTMLSimilarity float64
}

// JSONPathDiff represents a JSON diff at a specific path.
type JSONPathDiff struct {
	Path    string `json:"path"`
	Primary string `json:"primary"`
	Shadow  string `json:"shadow"`
}

// NewComparator selects the comparator based on configuration.
func NewComparator(cfg config.Config, logger *slog.Logger) (Comparator, error) {
	mode := normalizeCompareMode(cfg.CompareMode)
	switch mode {
	case ModeNginx:
		return &nginxComparator{baseComparator: baseComparator{cfg: cfg}, logger: logger}, nil
	case ModeJSON:
		return newJSONComparator(cfg, logger), nil
	case ModeHTML:
		return &htmlComparator{baseComparator: baseComparator{cfg: cfg}, logger: logger}, nil
	default:
		return nil, fmt.Errorf("unsupported compare mode %q", mode)
	}
}

// NewRegistry builds all HTTP comparators once so requests can select the mode dynamically.
func NewRegistry(cfg config.Config, logger *slog.Logger) (*Registry, error) {
	comparators := map[string]headerAwareComparator{
		ModeNginx: &nginxComparator{baseComparator: baseComparator{cfg: cfg}, logger: logger},
		ModeJSON:  newJSONComparator(cfg, logger),
		ModeHTML:  &htmlComparator{baseComparator: baseComparator{cfg: cfg}, logger: logger},
	}

	defaultMode := normalizeCompareMode(cfg.CompareMode)
	if _, ok := comparators[defaultMode]; !ok {
		return nil, fmt.Errorf("unsupported compare mode %q", defaultMode)
	}

	return &Registry{defaultMode: defaultMode, comparators: comparators}, nil
}

// Compare selects the requested comparator and applies per-request header overrides.
func (r *Registry) Compare(mode string, compareHeaders []string, primary, shadow backend.Result) (Result, error) {
	if r == nil {
		return Result{}, fmt.Errorf("missing compare registry")
	}

	selectedMode := r.defaultMode
	if strings.TrimSpace(mode) != "" {
		selectedMode = normalizeCompareMode(mode)
	}

	comparator, ok := r.comparators[selectedMode]
	if !ok {
		return Result{}, fmt.Errorf("unsupported compare mode %q", selectedMode)
	}

	return comparator.CompareWithHeaders(primary, shadow, compareHeaders)
}

func normalizeCompareMode(raw string) string {
	mode := strings.ToLower(strings.TrimSpace(raw))
	switch mode {
	case "", "nxinx", "header":
		return ModeNginx
	default:
		return mode
	}
}

type baseComparator struct {
	cfg config.Config
}

func (b baseComparator) compareHeaders(primary http.Header, shadow http.Header, compareHeaders []string) (map[string]string, map[string]string, []headers.HeaderDiff, bool) {
	if compareHeaders == nil {
		compareHeaders = b.cfg.CompareHeaders
	}

	return headers.CompareDetailed(primary, shadow, compareHeaders, false)
}
