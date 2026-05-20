package grpcproxy

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"doppelgaenger/internal/app"
	"doppelgaenger/internal/config"
	"go.uber.org/fx"
)

const grpcServerTestAddr = "127.0.0.1:0"
const grpcServerHTTPProtocol = "http"

func TestStartServerNoopsWhenProtocolIsNotGRPC(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))
	srv := NewServer(config.Config{Protocol: grpcServerHTTPProtocol, GRPCListenAddr: grpcServerTestAddr}, nil, nil, logger)

	if err := startServer(config.Config{Protocol: grpcServerHTTPProtocol}, srv, logger, app.Version("test"), noopShutdowner{}); err != nil {
		t.Fatalf("expected non-grpc start to no-op, got error: %v", err)
	}

	if srv.listener != nil {
		t.Fatalf("expected non-grpc start to leave listener nil")
	}
}

func TestStartServerOpensAndClosesListener(t *testing.T) {
	t.Setenv("LISTEN_PID", "")
	t.Setenv("LISTEN_FDS", "")
	t.Setenv("LISTEN_FDNAMES", "")

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))
	cfg := config.Config{Protocol: ProtocolName, GRPCListenAddr: grpcServerTestAddr}
	srv := NewServer(cfg, &Handler{}, &TargetPools{}, logger)

	if err := startServer(cfg, srv, logger, app.Version("test"), noopShutdowner{}); err != nil {
		t.Fatalf("expected grpc skeleton to start, got error: %v", err)
	}

	if srv.listener == nil {
		t.Fatalf("expected grpc skeleton to own a listener")
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
