package fakehttpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"go.uber.org/fx"

	"doppelgaenger/internal/app"
)

// NewServer constructs the fake HTTP server.
func NewServer(cfg Config, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

// RegisterHooks wires the server into the Fx lifecycle.
func RegisterHooks(lc fx.Lifecycle, cfg Config, srv *http.Server, logger *slog.Logger, version app.Version, shutdowner fx.Shutdowner) {
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			logger.Info("fakehttpserver starting", "version", string(version))
			logger.Info("listening", "addr", cfg.ListenAddr, "mode", cfg.Mode)

			go func() {
				var err error
				if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
					err = srv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
				} else {
					err = srv.ListenAndServe()
				}
				if err != nil && !errors.Is(err, http.ErrServerClosed) {
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
