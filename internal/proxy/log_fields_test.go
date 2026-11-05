package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"

	"doppelgaenger/internal/config"
	"doppelgaenger/internal/logging"
	"doppelgaenger/internal/protocol"
)

const testLogAuthUser = "Auth-User"
const testLogUser = "alice"

func TestHTTPLogCorrelationFields(t *testing.T) {
	for _, shadow := range []bool{false, true} {
		harness := newRuntimeHarness(t, config.Config{
			ShadowSamplePercent: 100,
			LogFields:           config.LogFieldsConfig{RequestHeaders: []string{testLogAuthUser}, ResponseHeaders: []string{headerSessionID}},
		}, nil)
		if !shadow {
			harness.handler.cfg.ShadowSamplePercent = 0
		}

		harness.adapter.primaryResponse.Header.Set(headerSessionID, "primary-session")
		harness.adapter.shadowResponse.Header.Set(headerSessionID, "shadow-session")
		performRuntimeRequest(t, harness.handler, http.MethodGet, testAuthPath, http.Header{testLogAuthUser: {testLogUser}, "Auth-Pass": {"secret"}})

		fields := waitForRuntimeLog(t, harness.logs)
		for group, want := range map[string]string{"request_headers": testLogUser, "primary_response_headers": "primary-session"} {
			encoded, err := json.Marshal(fields[group])
			if err != nil {
				t.Fatal(err)
			}

			if !bytes.Contains(encoded, []byte(want)) {
				t.Fatalf("missing %s: %s", group, encoded)
			}
		}

		encoded, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}

		if bytes.Contains(encoded, []byte("secret")) || (bytes.Contains(encoded, []byte("shadow-session")) != shadow) {
			t.Fatalf("wrong field selection: %s", encoded)
		}
	}
}

func TestHTTPResponseFieldsIndependentOfComparison(t *testing.T) {
	var output bytes.Buffer

	h := &Handler{cfg: config.Config{LogSessionOnlyOnDiff: true, LogFields: config.LogFieldsConfig{ResponseHeaders: []string{headerSessionID}}}, logger: slog.New(logging.NewHandler(&output, true, "info"))}
	header := make(http.Header)
	header.Set(headerSessionID, "session-value")
	h.logHTTPResult(httpLogPayload{ctx: context.Background(), primaryRes: protocol.Response{Header: header}})

	if !bytes.Contains(output.Bytes(), []byte(`"x_session_id":"session-value"`)) {
		t.Fatalf("correlation missing: %s", output.String())
	}
}
