package fakehttpserver

import (
	"io"
	"log/slog"
	"os"
)

// NewLogger builds the fake HTTP server logger.
func NewLogger(cfg Config) *slog.Logger {
	logger := slog.New(newLogHandler(os.Stdout, cfg.UseJSONLogger()))
	slog.SetDefault(logger)

	return logger
}

func newLogHandler(writer io.Writer, useJSON bool) slog.Handler {
	options := &slog.HandlerOptions{Level: slog.LevelInfo}
	if useJSON {
		return slog.NewJSONHandler(writer, options)
	}

	return slog.NewTextHandler(writer, options)
}
