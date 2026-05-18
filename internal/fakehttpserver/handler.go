package fakehttpserver

import (
	"log/slog"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"doppelgaenger/internal/headers"
)

// Handler handles incoming fake server requests.
type Handler struct {
	cfg    Config
	logger *slog.Logger
	rng    *rand.Rand
	mu     sync.Mutex
}

// NewHandler constructs the fake server handler.
func NewHandler(cfg Config, logger *slog.Logger) *Handler {
	return &Handler{
		cfg:    cfg,
		logger: logger,
		rng:    rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// ServeHTTP processes a single request.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	reqHeaders := headers.Clone(r.Header)
	reqContentType := r.Header.Get("Content-Type")
	reqContentLength := r.ContentLength

	if reqContentLength < 0 {
		if raw := r.Header.Get("Content-Length"); raw != "" {
			if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil {
				reqContentLength = parsed
			}
		}
	}

	for _, key := range h.cfg.EchoHeaders {
		ck := http.CanonicalHeaderKey(key)

		value := strings.Join(r.Header.Values(ck), ",")
		if value == "" {
			continue
		}

		w.Header().Set(ck, value)
	}

	if h.cfg.Mode == modeRandom {
		for _, key := range h.cfg.RandomHeaders {
			if h.shouldRandomize() {
				ck := http.CanonicalHeaderKey(key)
				w.Header().Set(ck, h.randomValue())
			}
		}
	}

	for key, value := range h.cfg.ResponseHeaders {
		w.Header().Set(key, value)
	}

	body := []byte("ok")

	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}

	if w.Header().Get("Content-Length") == "" {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)

	h.logResponse(w, r, reqHeaders, reqContentType, reqContentLength, len(body))
}

func (h *Handler) logResponse(w http.ResponseWriter, r *http.Request, reqHeaders http.Header, reqContentType string, reqContentLength int64, bodyLength int) {
	respHeaders := headers.Clone(w.Header())
	respContentType := w.Header().Get("Content-Type")
	respContentLength := int64(bodyLength)

	h.logger.Info(
		"fake response",
		"method", r.Method,
		"path", r.URL.Path,
		"query", r.URL.RawQuery,
		"mode", h.cfg.Mode,
		"request_headers", reqHeaders,
		"response_headers", respHeaders,
		"request_content_type", reqContentType,
		"request_content_length", reqContentLength,
		"response_content_type", respContentType,
		"response_content_length", respContentLength,
	)
}

func (h *Handler) shouldRandomize() bool {
	if h.cfg.RandomChance <= 0 {
		return false
	}

	if h.cfg.RandomChance >= 100 {
		return true
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	return h.rng.Intn(100) < h.cfg.RandomChance
}

func (h *Handler) randomValue() string {
	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.cfg.RandomValues) == 0 {
		return randomDefaultValue
	}

	return h.cfg.RandomValues[h.rng.Intn(len(h.cfg.RandomValues))]
}
