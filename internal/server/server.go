// Package server wires the HTTP listener into the application lifecycle.
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.uber.org/fx"

	"doppelgaenger/internal/app"
	"doppelgaenger/internal/config"
	"doppelgaenger/internal/health"
)

const protocolHTTP = "http"

const (
	protoHTTP11   = "HTTP/1.1"
	protoHTTP2TLS = "HTTP/2 via ALPN"
)

func resolveInboundMode(listenAddr, tlsCertFile, tlsKeyFile string) (addr string, proto string, useTLS bool, err error) {
	switch {
	case tlsCertFile == "" && tlsKeyFile == "":
		return "http://" + listenAddr, protoHTTP11, false, nil
	case tlsCertFile != "" && tlsKeyFile != "":
		return "https://" + listenAddr, protoHTTP2TLS, true, nil
	default:
		missing := "tls_key_file"
		if strings.TrimSpace(tlsCertFile) == "" {
			missing = "tls_cert_file"
		}

		return "", "", false, errors.New("incomplete TLS configuration: missing " + missing)
	}
}

// NewServer constructs the HTTP server.
func NewServer(cfg config.Config, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

// RegisterHooks wires the server into the Fx lifecycle.
func RegisterHooks(
	lc fx.Lifecycle,
	cfg config.Config,
	srv *http.Server,
	logger *slog.Logger,
	version app.Version,
	shutdowner fx.Shutdowner,
	healthState *health.State,
) {
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			return startServer(cfg, srv, logger, version, shutdowner, healthState)
		},
		OnStop: func(ctx context.Context) error {
			healthState.MarkShuttingDown()

			shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()

			return srv.Shutdown(shutdownCtx)
		},
	})
}

func startServer(
	cfg config.Config,
	srv *http.Server,
	logger *slog.Logger,
	version app.Version,
	shutdowner fx.Shutdowner,
	healthState *health.State,
) error {
	if cfg.Protocol != protocolHTTP {
		return nil
	}

	listenAddr, proto, useTLS, err := resolveInboundMode(cfg.ListenAddr, cfg.TLSCertFile, cfg.TLSKeyFile)
	if err != nil {
		return err
	}

	logger.Info("doppelgaenger starting", "version", string(version))
	logger.Info("listening", "addr", listenAddr, "proto", proto)
	logger.Info("backends", "primary", stringifyURLs(cfg.PrimaryBaseURLs), "shadow", stringifyURLs(cfg.ShadowBaseURLs))
	logUpstreamCA(cfg, logger)

	listener, err := resolveHTTPListener(cfg, useTLS, logger)
	if err != nil {
		return err
	}

	healthState.MarkReady()

	go serveHTTP(srv, listener, useTLS, cfg, logger, shutdowner, healthState)

	return nil
}

func logUpstreamCA(cfg config.Config, logger *slog.Logger) {
	if cfg.RootCAPath == "" && cfg.PrimaryRootCA == "" && cfg.ShadowRootCA == "" {
		return
	}

	logger.Info("upstream CA",
		"root", cfg.RootCAPath,
		"primary", cfg.PrimaryRootCA,
		"shadow", cfg.ShadowRootCA,
		"insecure", cfg.InsecureUpstream,
	)
}

func resolveHTTPListener(cfg config.Config, useTLS bool, logger *slog.Logger) (net.Listener, error) {
	listener, activated, err := resolveListener(cfg.ListenAddr)
	if err != nil {
		return nil, err
	}

	if !activated {
		return listener, nil
	}

	logger.Info("socket activation enabled", "protocol", protocolHTTP)

	if !useTLS {
		return listener, nil
	}

	certificate, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{Certificates: []tls.Certificate{certificate}}

	return tls.NewListener(listener, tlsConfig), nil
}

func serveHTTP(
	srv *http.Server,
	listener net.Listener,
	useTLS bool,
	cfg config.Config,
	logger *slog.Logger,
	shutdowner fx.Shutdowner,
	healthState *health.State,
) {
	err := listenAndServeHTTP(srv, listener, useTLS, cfg)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		healthState.MarkNotReady()
		logger.Error("server failed", "err", err)

		_ = shutdowner.Shutdown()
	}
}

func listenAndServeHTTP(srv *http.Server, listener net.Listener, useTLS bool, cfg config.Config) error {
	if listener != nil {
		return srv.Serve(listener)
	}

	if useTLS {
		return srv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
	}

	return srv.ListenAndServe()
}

func stringifyURLs(urls []*url.URL) []string {
	out := make([]string, 0, len(urls))
	for _, u := range urls {
		if u == nil {
			continue
		}

		out = append(out, u.String())
	}

	return out
}

func resolveListener(expectedAddr string) (net.Listener, bool, error) {
	listeners, err := app.ActivatedListeners()
	if err != nil {
		return nil, false, err
	}

	if len(listeners) == 0 {
		return nil, false, nil
	}

	listener, activated, pickErr := app.PickActivatedListener(listeners, protocolHTTP, expectedAddr)
	if pickErr != nil {
		return nil, false, pickErr
	}

	return listener, activated, nil
}
