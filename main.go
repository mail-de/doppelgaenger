package main

import (
	"log/slog"
	"net/http"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"httpproxy/internal/app"
	"httpproxy/internal/config"
	"httpproxy/internal/proxy"
	"httpproxy/internal/server"
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
			proxy.NewHandler,
			fx.Annotate(proxy.NewRouter, fx.As(new(http.Handler))),
			server.NewServer,
		),
		fx.Invoke(server.RegisterHooks),
	).Run()
}
