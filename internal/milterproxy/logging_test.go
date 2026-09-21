package milterproxy

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"doppelgaenger/internal/logging"
	"doppelgaenger/internal/protocol"
)

func TestFrameResultWarningSeverity(t *testing.T) {
	var output bytes.Buffer

	handler := &Handler{logger: slog.New(logging.NewHandler(&output, false, "warn"))}
	handler.logFrameResult(context.Background(), 1, "", "body", true, protocol.RunResult{ShadowErr: "transport failed"})

	if !strings.Contains(output.String(), "level=WARN") {
		t.Fatalf("shadow error missing: %s", output.String())
	}
}
