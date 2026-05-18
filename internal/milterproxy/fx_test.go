package milterproxy_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"go.uber.org/fx"

	"doppelgaenger/internal/app"
	"doppelgaenger/internal/config"
	"doppelgaenger/internal/milterproxy"
	"doppelgaenger/internal/protocol"
	"doppelgaenger/internal/ratelimit"
)

type stubAdapter struct{}

func (stubAdapter) Protocol() string {
	return "stub"
}

func (stubAdapter) NewSession(_ context.Context, _ protocol.Target) (protocol.TestSession, error) {
	return nil, nil
}

type stubLimiter struct{}

func (stubLimiter) Allow() bool {
	return true
}

func TestMilterProxyFxWiring(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))
	app := fx.New(
		fx.Supply(
			config.Config{Protocol: "http"},
			logger,
			app.Version("test"),
			protocol.Runner{},
		),
		fx.Provide(
			func() protocol.Adapter { return stubAdapter{} },
			func() ratelimit.Limiter { return stubLimiter{} },
			milterproxy.NewHandler,
			milterproxy.NewServer,
		),
		fx.Invoke(milterproxy.RegisterHooks),
	)

	startCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := app.Start(startCtx); err != nil {
		t.Fatalf("fx start failed: %v", err)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()

	if err := app.Stop(stopCtx); err != nil {
		t.Fatalf("fx stop failed: %v", err)
	}
}
