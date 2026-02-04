package main

import (
	"log/slog"
	"net/http"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"doppelgaenger/internal/app"
	"doppelgaenger/internal/compare"
	"doppelgaenger/internal/config"
	"doppelgaenger/internal/milterproxy"
	"doppelgaenger/internal/proxy"
	"doppelgaenger/internal/server"
)

var version = "dev"

func main() {
	fx.New(
		fx.WithLogger(func(logger *slog.Logger) fxevent.Logger {
			return &fxevent.SlogLogger{Logger: logger}
		}),
		fx.Supply(app.Version(version)),
		fx.Provide(
			app.NewLogger,
			config.Load,
			app.NewUpstreamTLS,
			app.NewBackendPools,
			app.NewShadowLimiter,
			compare.NewComparator,
			app.NewProtocolAdapter,
			app.NewProtocolComparator,
			app.NewProtocolRunner,
			proxy.NewHandler,
			fx.Annotate(proxy.NewRouter, fx.As(new(http.Handler))),
			server.NewServer,
			milterproxy.NewHandler,
			milterproxy.NewServer,
		),
		fx.Invoke(server.RegisterHooks),
		fx.Invoke(milterproxy.RegisterHooks),
	).Run()
}
