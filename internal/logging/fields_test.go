package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

const testAuthUserField = "Auth-User"
const testClientIDField = "X-Client-ID"

func TestEmptyTraceID(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		var output bytes.Buffer

		logger := slog.New(NewHandler(&output, jsonOutput, "info"))
		logger.Info("empty", "trace_id", "")

		if strings.Contains(output.String(), "trace_id") {
			t.Fatalf("empty trace ID logged: %s", output.String())
		}

		logger.Info("valid", "trace_id", "trace-value")

		if !strings.Contains(output.String(), "trace-value") {
			t.Fatal("valid trace ID missing")
		}
	}
}

func TestSelectedFields(t *testing.T) {
	fields := SelectedFields("request_headers", []string{testAuthUserField, testClientIDField, "Missing"}, func(key string) []string {
		return map[string][]string{testAuthUserField: {"alice"}, testClientIDField: {"one", "two"}}[key]
	})

	var output bytes.Buffer
	slog.New(NewHandler(&output, true, "info")).Info("request", fields)

	for _, want := range []string{`"auth_user":"alice"`, `"x_client_id":["one","two"]`} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %s in %s", want, output.String())
		}
	}

	if strings.Contains(output.String(), "missing") {
		t.Fatal("absent field logged")
	}
}
