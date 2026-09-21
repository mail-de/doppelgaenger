package proxy

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"doppelgaenger/internal/logging"
	"doppelgaenger/internal/protocol"
)

const (
	testLevelNotice = "notice"
	testLevelWarn   = "warn"
)

func TestHTTPResultSeverity(t *testing.T) {
	for _, tc := range []struct {
		name, threshold, want string
		diff, failed          bool
	}{
		{"clean", testLevelNotice, "", false, false},
		{"diff", testLevelNotice, "NOTICE", true, false},
		{"filtered diff", testLevelWarn, "", true, false},
		{"failure", testLevelWarn, "WARN", false, true},
		{"filtered warning", "error", "", false, true},
		{"disabled", "none", "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer

			handler := &Handler{logger: slog.New(logging.NewHandler(&output, false, tc.threshold))}

			payload := httpLogPayload{ctx: context.Background(), compareResult: protocol.CompareResult{Diff: tc.diff}}
			if tc.failed {
				payload.compareErr = errors.New("comparison failed")
			}

			handler.logHTTPResult(payload)

			if tc.want == "" && output.Len() != 0 {
				t.Fatalf("unexpected output: %s", output.String())
			}

			if tc.want != "" && !strings.Contains(output.String(), "level="+tc.want) {
				t.Fatalf("wrong severity: %s", output.String())
			}
		})
	}
}
