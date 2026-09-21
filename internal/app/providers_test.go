package app

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"doppelgaenger/internal/logging"
)

func TestNewHandlerJSON(t *testing.T) {
	var buf bytes.Buffer

	logger := slog.New(logging.NewHandler(&buf, true, "info"))

	logger.Info("hello", "key", "value")

	var payload map[string]any
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("expected JSON output, got error: %v", err)
	}

	if payload["msg"] != "hello" {
		t.Fatalf("expected msg to be logged, got %v", payload["msg"])
	}

	if payload["key"] != "value" {
		t.Fatalf("expected key to be logged, got %v", payload["key"])
	}
}

func TestNewHandlerText(t *testing.T) {
	var buf bytes.Buffer

	logger := slog.New(logging.NewHandler(&buf, false, "info"))

	logger.Info("hello", "key", "value")

	output := buf.String()
	if !strings.Contains(output, "msg=hello") {
		t.Fatalf("expected text output to contain msg, got %q", output)
	}

	if !strings.Contains(output, "key=value") {
		t.Fatalf("expected text output to contain key, got %q", output)
	}
}
