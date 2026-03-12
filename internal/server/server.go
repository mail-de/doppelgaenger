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
			logger.Info("backends", "primary", stringifyURLs(cfg.PrimaryBaseURLs), "shadow", cfg.ShadowBaseURL.String())

			if cfg.RootCAPath != "" || cfg.PrimaryRootCA != "" || cfg.ShadowRootCA != "" {
				logger.Info("upstream CA",
					"root", cfg.RootCAPath,
					"primary", cfg.PrimaryRootCA,
					"shadow", cfg.ShadowRootCA,
					"insecure", cfg.InsecureUpstream,
				)
			}

			listener, activated, err := resolveListener(cfg.ListenAddr)
			if err != nil {
				return err
			}
			if activated {
				logger.Info("socket activation enabled", "protocol", "http")
				if useTLS {
					certificate, certErr := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
					if certErr != nil {
						return certErr
					}
					tlsConfig := &tls.Config{Certificates: []tls.Certificate{certificate}}
					listener = tls.NewListener(listener, tlsConfig)
				}
			}

			go func() {
				var err error
				if listener != nil {
					err = srv.Serve(listener)
				} else if useTLS {
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

	listener, activated, pickErr := app.PickActivatedListener(listeners, "http", expectedAddr)
	if pickErr != nil {
		return nil, false, pickErr
	}

	return listener, activated, nil
}
