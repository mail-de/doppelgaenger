package compare

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"httpproxy/internal/backend"
	"httpproxy/internal/config"
	"httpproxy/internal/headers"
)

const (
	ModeNginx = "nginx"
	ModeJSON  = "json"
	ModeHTML  = "html"
)

// Comparator compares primary and shadow responses.
type Comparator interface {
	Compare(primary, shadow backend.BackendResult) (Result, error)
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

func (b baseComparator) compareHeaders(primary http.Header, shadow http.Header) (map[string]string, map[string]string, []headers.HeaderDiff, bool) {
	return headers.CompareDetailed(primary, shadow, b.cfg.CompareHeaders, false)
}
