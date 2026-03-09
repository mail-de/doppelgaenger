package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"go.uber.org/fx"

	"doppelgaenger/internal/app"
	"doppelgaenger/internal/config"
)

func resolveInboundMode(listenAddr, tlsCertFile, tlsKeyFile string) (addr string, proto string, useTLS bool, err error) {
	switch {
	case tlsCertFile == "" && tlsKeyFile == "":
		return "http://" + listenAddr, "HTTP/1.1", false, nil
	case tlsCertFile != "" && tlsKeyFile != "":
		return "https://" + listenAddr, "HTTP/2 via ALPN", true, nil
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
func RegisterHooks(lc fx.Lifecycle, cfg config.Config, srv *http.Server, logger *slog.Logger, version app.Version, shutdowner fx.Shutdowner) {
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			if cfg.Protocol != "http" {
				return nil
			}
			listenAddr, proto, useTLS, err := resolveInboundMode(cfg.ListenAddr, cfg.TLSCertFile, cfg.TLSKeyFile)
			if err != nil {
				return err
			}
			logger.Info("doppelgaenger starting", "version", string(version))
			logger.Info("listening", "addr", listenAddr, "proto", proto)
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
				var err error
				if useTLS {
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
