package main

import (
	"log/slog"
	"net/http"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"httpproxy/internal/app"
	"httpproxy/internal/fakeserver"
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
			fakeserver.Load,
			fx.Annotate(fakeserver.NewHandler, fx.As(new(http.Handler))),
			fakeserver.NewServer,
		),
		fx.Invoke(fakeserver.RegisterHooks),
	).Run()
}
