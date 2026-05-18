package fakehttpserver

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"testing"
)

func TestHandlerLogsHeadersAndContentMetadata(t *testing.T) {
	var buf bytes.Buffer

	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg := Config{
		Mode:            modeEcho,
		EchoHeaders:     []string{headerXTest},
		ResponseHeaders: map[string]string{"X-Response": "ok"},
	}
	handler := NewHandler(cfg, logger)

	req := httptest.NewRequest("POST", "http://example.com/echo", bytes.NewBufferString("payload"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(headerXTest, "value")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	var payload map[string]any
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("expected JSON log output, got error: %v", err)
	}

	reqHeaders, ok := payload["request_headers"].(map[string]any)
	if !ok {
		t.Fatalf("expected request_headers in log output")
	}

	if _, ok := reqHeaders["Content-Type"]; !ok {
		t.Fatalf("expected Content-Type in request headers")
	}

	if _, ok := reqHeaders[headerXTest]; !ok {
		t.Fatalf("expected %s in request headers", headerXTest)
	}

	respHeaders, ok := payload["response_headers"].(map[string]any)
	if !ok {
		t.Fatalf("expected response_headers in log output")
	}

	if _, ok := respHeaders["Content-Type"]; !ok {
		t.Fatalf("expected Content-Type in response headers")
	}

	if _, ok := respHeaders["Content-Length"]; !ok {
		t.Fatalf("expected Content-Length in response headers")
	}

	if payload["request_content_type"] != "application/json" {
		t.Fatalf("expected request_content_type to be logged, got %v", payload["request_content_type"])
	}

	if payload["response_content_type"] == "" {
		t.Fatalf("expected response_content_type to be logged")
	}

	if payload["response_content_length"] != float64(2) {
		t.Fatalf("expected response_content_length to be 2, got %v", payload["response_content_length"])
	}
}
