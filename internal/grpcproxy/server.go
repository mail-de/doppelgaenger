package grpcproxy

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"sync"

	"go.uber.org/fx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"doppelgaenger/internal/app"
	"doppelgaenger/internal/config"
	"doppelgaenger/internal/health"
	"doppelgaenger/internal/logging"
)

// Server owns the gRPC listener lifecycle.
type Server struct {
	Addr    string
	Handler *Handler
	Pools   *TargetPools
	logger  *slog.Logger

	listener   net.Listener
	grpcServer *grpc.Server
	mu         sync.Mutex
}

// NewServer constructs the gRPC proxy lifecycle.
func NewServer(cfg config.Config, handler *Handler, pools *TargetPools, logger *slog.Logger) *Server {
	return &Server{
		Addr:    cfg.GRPCListenAddr,
		Handler: handler,
		Pools:   pools,
		logger:  logger,
	}
}

// RegisterHooks wires the gRPC proxy into the Fx lifecycle.
func RegisterHooks(
	lc fx.Lifecycle,
	cfg config.Config,
	srv *Server,
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

			return stopServer(ctx, srv)
		},
	})
}

func startServer(
	cfg config.Config,
	srv *Server,
	logger *slog.Logger,
	version app.Version,
	shutdowner fx.Shutdowner,
	healthState *health.State,
) error {
	if cfg.Protocol != ProtocolName {
		return nil
	}

	if srv == nil || srv.Handler == nil {
		return errors.New("grpc proxy server not configured")
	}

	if srv.Addr == "" {
		return errors.New("grpc_listen_addr must not be empty")
	}

	logger.Log(context.Background(), logging.LevelNotice, "grpcproxy starting", "version", string(version))
	logger.Log(context.Background(), logging.LevelNotice, "listening", "addr", srv.Addr, "protocol", ProtocolName)

	listener, err := startListener(srv.Addr, logger)
	if err != nil {
		return err
	}

	grpcServer, err := newGRPCServer(cfg, srv.Handler)
	if err != nil {
		_ = listener.Close()

		return err
	}

	srv.mu.Lock()
	srv.listener = listener
	srv.grpcServer = grpcServer
	srv.mu.Unlock()

	healthState.MarkReady()

	go serveGRPCAsync(grpcServer, listener, logger, shutdowner, healthState)

	return nil
}

func startListener(addr string, logger *slog.Logger) (net.Listener, error) {
	listener, activated, err := resolveListener(addr)
	if err != nil {
		return nil, err
	}

	if activated {
		logger.Log(context.Background(), logging.LevelNotice, "socket activation enabled", "protocol", ProtocolName)

		return listener, nil
	}

	return net.Listen("tcp", addr)
}

func stopServer(ctx context.Context, srv *Server) error {
	if srv == nil {
		return nil
	}

	listener, grpcServer, pools := detachServerState(srv)

	var stopErr error
	if grpcServer != nil {
		stopErr = errors.Join(stopErr, stopGRPCServer(ctx, grpcServer))
	} else if listener != nil {
		stopErr = errors.Join(stopErr, listener.Close())
	}

	if pools != nil {
		stopErr = errors.Join(stopErr, pools.Close())
	}

	return stopErr
}

func detachServerState(srv *Server) (net.Listener, *grpc.Server, *TargetPools) {
	srv.mu.Lock()
	defer srv.mu.Unlock()

	listener := srv.listener
	grpcServer := srv.grpcServer
	pools := srv.Pools
	srv.listener = nil
	srv.grpcServer = nil
	srv.Pools = nil

	return listener, grpcServer, pools
}

func newGRPCServer(cfg config.Config, handler *Handler) (*grpc.Server, error) {
	if err := config.ValidateGRPCCallerAuth(cfg); err != nil {
		return nil, err
	}

	if handler.callerHTTPError != nil {
		return nil, handler.callerHTTPError
	}

	if cfg.GRPCCallerAuth.AllowUnauthenticated && handler.logger != nil {
		handler.logger.Warn("grpc caller authentication disabled: reachable callers can use backend service privileges")
	}

	options := []grpc.ServerOption{
		grpc.ForceServerCodec(rawCodec{}),
		grpc.UnknownServiceHandler(handler.Handle),
	}

	options = appendSizeOptions(options, cfg)

	creds, err := inboundCredentials(cfg.GRPCTLS)
	if err != nil {
		return nil, err
	}

	if creds != nil {
		options = append(options, grpc.Creds(creds))
	}

	return grpc.NewServer(options...), nil
}

func appendSizeOptions(options []grpc.ServerOption, cfg config.Config) []grpc.ServerOption {
	if cfg.GRPCMaxReceiveMessageBytes > 0 {
		options = append(options, grpc.MaxRecvMsgSize(cfg.GRPCMaxReceiveMessageBytes))
	}

	if cfg.GRPCMaxSendMessageBytes > 0 {
		options = append(options, grpc.MaxSendMsgSize(cfg.GRPCMaxSendMessageBytes))
	}

	return options
}

func inboundCredentials(cfg config.GRPCTLSConfig) (credentials.TransportCredentials, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	certificate, err := tls.LoadX509KeyPair(cfg.Cert, cfg.Key)
	if err != nil {
		return nil, err
	}

	minVersion, err := config.ResolveTLSMinVersion(cfg.MinTLSVersion)
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   minVersion,
		NextProtos:   []string{"h2"},
	}

	if cfg.ClientCA != "" {
		pool, poolErr := certPoolFromFile(cfg.ClientCA)
		if poolErr != nil {
			return nil, poolErr
		}

		tlsConfig.ClientCAs = pool
	}

	if cfg.RequireClientCert {
		tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
	}

	return credentials.NewTLS(tlsConfig), nil
}

func serveGRPCAsync(
	grpcServer *grpc.Server,
	listener net.Listener,
	logger *slog.Logger,
	shutdowner fx.Shutdowner,
	healthState *health.State,
) {
	err := grpcServer.Serve(listener)
	if err == nil || errors.Is(err, grpc.ErrServerStopped) || errors.Is(err, net.ErrClosed) {
		return
	}

	healthState.MarkNotReady()
	logger.Error("grpcproxy failed", "err", err)

	_ = shutdowner.Shutdown()
}

func stopGRPCServer(ctx context.Context, grpcServer *grpc.Server) error {
	done := make(chan struct{})

	go func() {
		grpcServer.GracefulStop()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		grpcServer.Stop()

		return ctx.Err()
	}
}

func resolveListener(expectedAddr string) (net.Listener, bool, error) {
	listeners, err := app.ActivatedListeners()
	if err != nil {
		return nil, false, err
	}

	if len(listeners) == 0 {
		return nil, false, nil
	}

	listener, activated, pickErr := app.PickActivatedListener(listeners, ProtocolName, expectedAddr)
	if pickErr != nil {
		return nil, false, pickErr
	}

	return listener, activated, nil
}
