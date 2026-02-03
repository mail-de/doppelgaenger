package main

import (
	"go.uber.org/fx"

	"httpproxy/internal/app"
	"httpproxy/internal/config"
	"httpproxy/internal/proxy"
	"httpproxy/internal/server"
)

var version = "dev"

func main() {
	fx.New(
		fx.Supply(app.Version(version)),
		fx.Provide(
			app.NewLogger,
			config.Load,
			app.NewUpstreamTLS,
			app.NewBackendPools,
			app.NewShadowLimiter,
			proxy.NewHandler,
			proxy.NewRouter,
			server.NewServer,
		),
		fx.Invoke(server.RegisterHooks),
	).Run()
}
