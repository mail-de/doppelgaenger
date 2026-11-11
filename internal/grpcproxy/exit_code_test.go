package grpcproxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"go.uber.org/fx"
	"google.golang.org/grpc"

	"doppelgaenger/internal/config"
	"doppelgaenger/internal/health"
)

// brokenListener fails every Accept, like a listener whose socket is gone.
type brokenListener struct{}

func (brokenListener) Accept() (net.Conn, error) { return nil, errors.New("accept failed") }
func (brokenListener) Close() error              { return nil }
func (brokenListener) Addr() net.Addr            { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

// A server that stops serving because of an error is a failure. The process
// must end with a non-zero exit code.
func TestServeGRPCFailureExitsNonZero(t *testing.T) {
	var shutdowner fx.Shutdowner

	application := fx.New(fx.NopLogger, fx.Populate(&shutdowner))
	if err := application.Start(context.Background()); err != nil {
		t.Fatalf("start test application: %v", err)
	}

	signals := application.Wait()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	serveGRPCAsync(grpc.NewServer(), brokenListener{}, logger, shutdowner, health.New(config.Config{Protocol: ProtocolName}))

	select {
	case signal := <-signals:
		if signal.ExitCode != 1 {
			t.Fatalf("exit code = %d, want 1", signal.ExitCode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a failed gRPC server did not shut the application down")
	}
}
