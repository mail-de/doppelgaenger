package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"go.uber.org/fx"

	"doppelgaenger/internal/config"
	"doppelgaenger/internal/health"
)

// A listener that cannot be opened is a failure. The process must end with a
// non-zero exit code, or a service manager takes the exit for a clean stop.
func TestServeHTTPFailureExitsNonZero(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	t.Cleanup(func() { _ = occupied.Close() })

	var shutdowner fx.Shutdowner

	application := fx.New(fx.NopLogger, fx.Populate(&shutdowner))
	if err := application.Start(context.Background()); err != nil {
		t.Fatalf("start test application: %v", err)
	}

	signals := application.Wait()
	cfg := config.Config{Protocol: protocolHTTP, ListenAddr: occupied.Addr().String()}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	serveHTTP(&http.Server{Addr: cfg.ListenAddr, ReadHeaderTimeout: time.Second}, nil, false, cfg, logger, shutdowner, health.New(cfg))

	select {
	case signal := <-signals:
		if signal.ExitCode != 1 {
			t.Fatalf("exit code = %d, want 1", signal.ExitCode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a failed HTTP server did not shut the application down")
	}
}
