// Package logging defines application log levels and structured handlers.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// LevelNotice identifies significant normal events between info and warning.
const LevelNotice = slog.Level(2)

// ParseLevel validates a configured minimum severity. Empty means info.
func ParseLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info", "none":
		return slog.LevelInfo, nil
	case "notice":
		return LevelNotice, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log_level %q: expected debug, info, notice, warn, error, or none", value)
	}
}

// NewHandler creates a handler for validated configuration. None disables every record.
func NewHandler(writer io.Writer, useJSON bool, name string) slog.Handler {
	if strings.EqualFold(strings.TrimSpace(name), "none") {
		return slog.DiscardHandler
	}

	level, _ := ParseLevel(name)

	options := &slog.HandlerOptions{Level: level, ReplaceAttr: replaceLevel}
	if useJSON {
		return slog.NewJSONHandler(writer, options)
	}

	return slog.NewTextHandler(writer, options)
}

func replaceLevel(groups []string, attr slog.Attr) slog.Attr {
	if len(groups) == 0 && attr.Key == slog.LevelKey && attr.Value.Any() == LevelNotice {
		return slog.String(slog.LevelKey, "NOTICE")
	}

	return attr
}

// ResultLevel assigns severity without treating an expected comparison difference as a failure.
func ResultLevel(diff, failed, primaryFailed bool) slog.Level {
	switch {
	case primaryFailed:
		return slog.LevelError
	case failed:
		return slog.LevelWarn
	case diff:
		return LevelNotice
	default:
		return slog.LevelInfo
	}
}
