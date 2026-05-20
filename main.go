// Package main starts the Doppelgaenger proxy application.
package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/pflag"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"doppelgaenger/internal/app"
	"doppelgaenger/internal/compare"
	"doppelgaenger/internal/config"
	"doppelgaenger/internal/grpcproxy"
	"doppelgaenger/internal/milterproxy"
	"doppelgaenger/internal/observability"
	"doppelgaenger/internal/protocol"
	"doppelgaenger/internal/proxy"
	"doppelgaenger/internal/server"
)

var version = "dev"

type cliOptions struct {
	help       bool
	version    bool
	configPath string
}

func main() {
	opts, err := parseCLI(os.Args[1:], os.Stderr)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)

		os.Exit(2)
	}

	if opts.help {
		printUsage(os.Stdout, os.Args[0])

		return
	}

	if opts.version {
		printVersion(os.Stdout, version)

		return
	}

	if opts.configPath != "" {
		if err := os.Setenv("CONFIG_FILE", opts.configPath); err != nil {
			fmt.Fprintf(os.Stderr, "failed to set CONFIG_FILE: %v\n", err)
			os.Exit(1)
		}
	}

	newApp().Run()
}

func newApp() *fx.App {
	return fx.New(
		fx.WithLogger(func() fxevent.Logger {
			return fxevent.NopLogger
		}),
		fx.Supply(app.Version(version)),
		fx.Provide(
			app.NewLogger,
			config.Load,
			func(cfg config.Config, version app.Version, logger *slog.Logger) (*observability.Observability, error) {
				return observability.New(cfg, string(version), logger)
			},
			app.NewUpstreamTLS,
			app.NewShadowLimiter,
			compare.NewComparator,
			compare.NewRegistry,
			newProtocolAdapter,
			newProtocolComparator,
			app.NewProtocolRunner,
			proxy.NewPathMapper,
			proxy.NewPathRuleResolver,
			proxy.NewHandler,
			fx.Annotate(proxy.NewRouter, fx.As(new(http.Handler))),
			server.NewServer,
			milterproxy.NewHandler,
			milterproxy.NewServer,
			grpcproxy.NewConfiguredResolver,
			grpcproxy.NewTargetPools,
			grpcproxy.NewHandler,
			grpcproxy.NewServer,
		),
		fx.Invoke(server.RegisterHooks),
		fx.Invoke(milterproxy.RegisterHooks),
		fx.Invoke(grpcproxy.RegisterHooks),
		fx.Invoke(observability.RegisterHooks),
		fx.Invoke(app.RegisterReloadHook),
		fx.Invoke(app.ApplyRuntimeSecurity),
	)
}

func newProtocolAdapter(deps app.ProtocolAdapterDeps) (protocol.Adapter, error) {
	if deps.Config.Protocol == grpcproxy.ProtocolName {
		return grpcproxy.NewInactiveAdapter(), nil
	}

	return app.NewProtocolAdapter(deps)
}

func newProtocolComparator(deps app.ProtocolComparatorDeps) (protocol.Comparator, error) {
	if deps.Config.Protocol == grpcproxy.ProtocolName {
		return grpcproxy.NewInactiveComparator(), nil
	}

	return app.NewProtocolComparator(deps)
}

func parseCLI(args []string, errorOutput io.Writer) (cliOptions, error) {
	opts := cliOptions{}

	flags := pflag.NewFlagSet("doppelgaenger", pflag.ContinueOnError)
	flags.SetOutput(errorOutput)
	flags.BoolVarP(&opts.help, "help", "h", false, "show help")
	flags.StringVarP(&opts.configPath, "config", "c", "", "path to YAML config file")
	flags.BoolVar(&opts.version, "version", false, "show version")

	if err := flags.Parse(args); err != nil {
		return cliOptions{}, err
	}

	if flags.NArg() > 0 {
		return cliOptions{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	return opts, nil
}

func printVersion(w io.Writer, value string) {
	_, _ = fmt.Fprintf(w, "doppelgaenger %s\n", value)
}

func printUsage(w io.Writer, name string) {
	_, _ = fmt.Fprintf(w, "Usage: %s [--help|-h] [--version] [--config|-c <path>]\n", name)
	_, _ = fmt.Fprintln(w, "Doppelgaenger Shadow Proxy")
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "Command-line options:")
	_, _ = fmt.Fprintln(w, "  --config, -c <path>  Path to the YAML config file")
	_, _ = fmt.Fprintln(w, "  --version           Print the build version and exit")
	_, _ = fmt.Fprintln(w, "  --help, -h          Show this help")
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "Configuration lookup order:")
	_, _ = fmt.Fprintln(w, "  1) --config / -c")
	_, _ = fmt.Fprintln(w, "  2) CONFIG_FILE environment variable")
	_, _ = fmt.Fprintln(w, "  3) ./config.yaml")
	_, _ = fmt.Fprintln(w, "  4) /etc/doppelgaenger/config.yaml")
}
