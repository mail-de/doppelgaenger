// Package main starts the Doppelgaenger proxy application.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/pflag"
	"go.uber.org/dig"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"doppelgaenger/internal/app"
	"doppelgaenger/internal/compare"
	"doppelgaenger/internal/config"
	"doppelgaenger/internal/grpcproxy"
	"doppelgaenger/internal/health"
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

	var logger *slog.Logger

	if code := runApp(newApp(&logger), &logger, os.Stderr); code != 0 {
		os.Exit(code)
	}
}

// runApp starts the application, waits until it is asked to stop, and shuts
// it down. It returns the process exit code.
//
// Fx reports a failed start or stop only to its event logger, which is
// disabled here to keep the dependency wiring out of the logs. Without this
// function the process would end with exit code 1 and no word about why.
func runApp(application *fx.App, logger **slog.Logger, stderr io.Writer) int {
	startCtx, cancelStart := context.WithTimeout(context.Background(), application.StartTimeout())
	defer cancelStart()

	if err := application.Start(startCtx); err != nil {
		reportFailure(*logger, stderr, "startup failed", err)

		return 1
	}

	signal := <-application.Wait()

	stopCtx, cancelStop := context.WithTimeout(context.Background(), application.StopTimeout())
	defer cancelStop()

	if err := application.Stop(stopCtx); err != nil {
		reportFailure(*logger, stderr, "shutdown failed", err)

		return 1
	}

	return signal.ExitCode
}

// reportFailure tells the operator why the application could not start or
// stop. It uses the application logger, so the record has the configured
// format. Without a logger the configuration itself could not be loaded, and
// the reason goes to stderr as plain text.
//
// Only the cause is reported. The dependency path that Fx wraps around it
// names internal constructors and says nothing an operator can act on.
func reportFailure(logger *slog.Logger, stderr io.Writer, message string, err error) {
	cause := dig.RootCause(err)

	if logger != nil {
		logger.Error(message, "err", cause)

		return
	}

	_, _ = fmt.Fprintf(stderr, "doppelgaenger: %s: %v\n", message, cause)
}

// newApp wires the application. logger receives the application logger as
// soon as the configuration is loaded, so that a later startup failure can be
// reported through it.
func newApp(logger **slog.Logger) *fx.App {
	return fx.New(
		fx.WithLogger(func() fxevent.Logger {
			return fxevent.NopLogger
		}),
		fx.Supply(app.Version(version)),
		fx.Provide(
			app.NewLogger,
			config.Load,
			health.New,
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
		fx.Populate(logger),
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
