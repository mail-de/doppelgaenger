package app

import (
	"crypto/tls"
	"io"
	"log/slog"
	"os"

	"go.uber.org/fx"

	"doppelgaenger/internal/backend"
	"doppelgaenger/internal/config"
	"doppelgaenger/internal/ratelimit"
	"doppelgaenger/internal/tlsutil"
)

// Version is the application version string.
type Version string

// NewLogger builds the structured logger and sets it as default.
func NewLogger(cfg config.Config) *slog.Logger {
	logger := slog.New(newHandler(os.Stdout, cfg.UseJSONLogger()))
	slog.SetDefault(logger)

	return logger
}

func newHandler(writer io.Writer, useJSON bool) slog.Handler {
	options := &slog.HandlerOptions{Level: slog.LevelInfo}
	if useJSON {
		return slog.NewJSONHandler(writer, options)
	}

	return slog.NewTextHandler(writer, options)
}

// UpstreamTLSOut provides TLS configs for primary and shadow backends.
type UpstreamTLSOut struct {
	fx.Out
	Primary *tls.Config `name:"primaryTLS"`
	Shadow  *tls.Config `name:"shadowTLS"`
}

// UpstreamTLSIn wires named TLS configs.
type UpstreamTLSIn struct {
	fx.In
	Primary *tls.Config `name:"primaryTLS"`
	Shadow  *tls.Config `name:"shadowTLS"`
}

// BackendPoolsOut provides primary/shadow pools.
type BackendPoolsOut struct {
	fx.Out
	Primary backend.Pool `name:"primaryPool"`
	Shadow  backend.Pool `name:"shadowPool"`
}

// NewUpstreamTLS creates TLS configs for both upstreams.
func NewUpstreamTLS(cfg config.Config) (UpstreamTLSOut, error) {
	primaryCA := cfg.PrimaryRootCA
	if primaryCA == "" {
		primaryCA = cfg.RootCAPath
	}

	shadowCA := cfg.ShadowRootCA
	if shadowCA == "" {
		shadowCA = cfg.RootCAPath
	}

	primaryTLS, err := tlsutil.BuildUpstreamTLS(cfg.InsecureUpstream, primaryCA)
	if err != nil {
		return UpstreamTLSOut{}, err
	}

	shadowTLS, err := tlsutil.BuildUpstreamTLS(cfg.InsecureUpstream, shadowCA)
	if err != nil {
		return UpstreamTLSOut{}, err
	}

	return UpstreamTLSOut{Primary: primaryTLS, Shadow: shadowTLS}, nil
}

// NewBackendPools builds the backend pools for primary and shadow.
func NewBackendPools(cfg config.Config, tls UpstreamTLSIn) BackendPoolsOut {
	primarySelector := backend.NewSelector(cfg.PrimarySelectionMode)
	shadowSelector := backend.NewSelector(cfg.ShadowSelectionMode)

	return BackendPoolsOut{
		Primary: backend.NewPool(backend.BackendPrimary, cfg.PrimaryBaseURLs, primarySelector, tls.Primary, cfg.PrimaryWorkers, cfg.PrimaryQueueLen, cfg.MaxBackendBodyBytes),
		Shadow:  backend.NewPool(backend.BackendShadow, cfg.ShadowBaseURLs, shadowSelector, tls.Shadow, cfg.ShadowWorkers, cfg.ShadowQueueLen, cfg.MaxBackendBodyBytes),
	}
}

// NewShadowLimiter creates the rate limiter for shadow traffic.
func NewShadowLimiter(cfg config.Config) ratelimit.Limiter {
	if cfg.ShadowRPS <= 0 {
		return nil
	}

	return ratelimit.NewTokenBucket(cfg.ShadowRPS, cfg.ShadowBurst)
}
