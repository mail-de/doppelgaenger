package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"go.uber.org/fx"

	"httpproxy/internal/app"
	"httpproxy/internal/config"
)

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
func RegisterHooks(lc fx.Lifecycle, cfg config.Config, srv *http.Server, logger *slog.Logger, version app.Version, shutdowner fx.Shutdowner) {
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			if cfg.Protocol != "http" {
				return nil
			}
			if cfg.TLSCertFile == "" || cfg.TLSKeyFile == "" {
				return errors.New("TLS_CERT and TLS_KEY must be set for HTTPS/HTTP2 inbound")
			}
			logger.Info("httpproxy starting", "version", string(version))
			logger.Info("listening", "addr", "https://"+cfg.ListenAddr, "proto", "HTTP/2 via ALPN")
			logger.Info("backends", "primary", cfg.PrimaryBaseURL.String(), "shadow", cfg.ShadowBaseURL.String())

			if cfg.RootCAPath != "" || cfg.PrimaryRootCA != "" || cfg.ShadowRootCA != "" {
				logger.Info("upstream CA",
					"root", cfg.RootCAPath,
					"primary", cfg.PrimaryRootCA,
					"shadow", cfg.ShadowRootCA,
					"insecure", cfg.InsecureUpstream,
				)
			}

			go func() {
				if err := srv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile); err != nil && !errors.Is(err, http.ErrServerClosed) {
					logger.Error("server failed", "err", err)
					_ = shutdowner.Shutdown()
				}
			}()

			return nil
		},
		OnStop: func(ctx context.Context) error {
			shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			return srv.Shutdown(shutdownCtx)
		},
	})
}
