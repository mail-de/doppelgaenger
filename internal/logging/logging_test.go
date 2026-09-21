package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

const testLevelNone = "none"

func TestHandlerLevels(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		for _, name := range []string{"debug", "info", "notice", "warn", "error", testLevelNone} {
			t.Run(name, func(t *testing.T) {
				var output bytes.Buffer

				logger := slog.New(NewHandler(&output, jsonOutput, name)).With("component", "test").WithGroup("details")

				threshold, err := ParseLevel(name)
				if err != nil {
					t.Fatal(err)
				}

				for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo, LevelNotice, slog.LevelWarn, slog.LevelError, 100} {
					output.Reset()
					logger.Log(context.Background(), level, "event")

					want := name != testLevelNone && level >= threshold
					if (output.Len() > 0) != want {
						t.Fatalf("%s level %v: output %q", name, level, output.String())
					}

					if want && level == LevelNotice && !strings.Contains(output.String(), "NOTICE") {
						t.Fatalf("missing NOTICE: %s", output.String())
					}
				}
			})
		}
	}
}

func TestParseLevel(t *testing.T) {
	for _, name := range []string{"", " INFO ", "NoTiCe"} {
		if _, err := ParseLevel(name); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := ParseLevel("verbose"); err == nil {
		t.Fatal("invalid level accepted")
	}
}

func TestResultLevel(t *testing.T) {
	for _, tc := range []struct {
		diff, failed, primaryFailed bool
		want                        slog.Level
	}{
		{false, false, false, slog.LevelInfo},
		{true, false, false, LevelNotice},
		{true, true, false, slog.LevelWarn},
		{true, true, true, slog.LevelError},
	} {
		if got := ResultLevel(tc.diff, tc.failed, tc.primaryFailed); got != tc.want {
			t.Fatalf("got %v, want %v", got, tc.want)
		}
	}
}
