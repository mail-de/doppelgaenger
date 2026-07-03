package grpcproxy

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"doppelgaenger/internal/app"
	"doppelgaenger/internal/config"
	"doppelgaenger/internal/health"
	"go.uber.org/fx"
)

const grpcServerTestAddr = "127.0.0.1:0"
const grpcServerHTTPProtocol = "http"

func TestStartServerNoopsWhenProtocolIsNotGRPC(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))
	srv := NewServer(config.Config{Protocol: grpcServerHTTPProtocol, GRPCListenAddr: grpcServerTestAddr}, nil, nil, logger)
	healthState := health.New(config.Config{Protocol: grpcServerHTTPProtocol})

	if err := startServer(config.Config{Protocol: grpcServerHTTPProtocol}, srv, logger, app.Version("test"), noopShutdowner{}, healthState); err != nil {
		t.Fatalf("expected non-grpc start to no-op, got error: %v", err)
	}

	if srv.listener != nil {
		t.Fatalf("expected non-grpc start to leave listener nil")
	}

	if healthState.Snapshot().Ready {
		t.Fatalf("expected non-grpc start to leave health not ready")
	}
}

func TestStartServerOpensAndClosesListener(t *testing.T) {
	t.Setenv("LISTEN_PID", "")
	t.Setenv("LISTEN_FDS", "")
	t.Setenv("LISTEN_FDNAMES", "")

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))
	cfg := config.Config{Protocol: ProtocolName, GRPCListenAddr: grpcServerTestAddr}
	srv := NewServer(cfg, &Handler{}, &TargetPools{}, logger)
	healthState := health.New(cfg)

	if err := startServer(cfg, srv, logger, app.Version("test"), noopShutdowner{}, healthState); err != nil {
		t.Fatalf("expected grpc skeleton to start, got error: %v", err)
	}

	if srv.listener == nil {
		t.Fatalf("expected grpc skeleton to own a listener")
	}

	if !healthState.Snapshot().Ready {
		t.Fatalf("expected grpc start to mark health ready")
	}

	if err := stopServer(context.Background(), srv); err != nil {
		t.Fatalf("expected grpc skeleton to stop cleanly, got error: %v", err)
	}

	if srv.listener != nil {
		t.Fatalf("expected grpc skeleton listener to be cleared after stop")
	}
}

type noopShutdowner struct{}

func (noopShutdowner) Shutdown(...fx.ShutdownOption) error {
	return nil
}
