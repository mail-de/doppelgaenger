package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"

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
	help := flag.Bool("help", false, "show help")
	flag.BoolVar(help, "h", false, "show help")
	configPath := flag.String("config", "", "path to YAML config file")
	flag.StringVar(configPath, "c", "", "path to YAML config file")
	flag.Parse()

	if *help {
		fmt.Fprintf(os.Stdout, "Usage: %s [--help|-h] [--config|-c <path>]\n", os.Args[0])
		fmt.Fprintln(os.Stdout, "HTTP Shadow Proxy")
		fmt.Fprintln(os.Stdout, "Configuration lookup order:")
		fmt.Fprintln(os.Stdout, "  1) --config / -c")
		fmt.Fprintln(os.Stdout, "  2) CONFIG_FILE environment variable")
		fmt.Fprintln(os.Stdout, "  3) ./config.yaml")
		fmt.Fprintln(os.Stdout, "  4) /etc/doppelgaenger/config.yaml")
		return
	}

	if *configPath != "" {
		if err := os.Setenv("CONFIG_FILE", *configPath); err != nil {
			fmt.Fprintf(os.Stderr, "failed to set CONFIG_FILE: %v\n", err)
			os.Exit(1)
		}
	}

	fx.New(
		fx.WithLogger(func() fxevent.Logger {
			return fxevent.NopLogger
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
			proxy.NewPathMapper,
			proxy.NewHandler,
			fx.Annotate(proxy.NewRouter, fx.As(new(http.Handler))),
			server.NewServer,
			milterproxy.NewHandler,
			milterproxy.NewServer,
		),
		fx.Invoke(server.RegisterHooks),
		fx.Invoke(milterproxy.RegisterHooks),
		fx.Invoke(app.RegisterReloadHook),
		fx.Invoke(app.ApplyRuntimeSecurity),
	).Run()
}
