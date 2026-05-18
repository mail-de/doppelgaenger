// Package main starts the fake HTTP backend server.
package main

import (
	"net/http"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"doppelgaenger/internal/app"
	"doppelgaenger/internal/fakehttpserver"
)

var version = "dev"

func main() {
	fx.New(
		fx.WithLogger(func() fxevent.Logger {
			return fxevent.NopLogger
		}),
		fx.Supply(app.Version(version)),
		fx.Provide(
			fakehttpserver.Load,
			fakehttpserver.NewLogger,
			fx.Annotate(fakehttpserver.NewHandler, fx.As(new(http.Handler))),
			fakehttpserver.NewServer,
		),
		fx.Invoke(fakehttpserver.RegisterHooks),
	).Run()
}
