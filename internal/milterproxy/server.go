package milterproxy

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"

	"go.uber.org/fx"

	"httpproxy/internal/app"
	"httpproxy/internal/config"
)

type Server struct {
	Addr    string
	Handler *Handler
	logger  *slog.Logger

	listener net.Listener
	mu       sync.Mutex
}

func NewServer(cfg config.Config, handler *Handler, logger *slog.Logger) *Server {
	return &Server{
		Addr:    cfg.MilterListenAddr,
		Handler: handler,
		logger:  logger,
	}
}

func RegisterHooks(lc fx.Lifecycle, cfg config.Config, srv *Server, logger *slog.Logger, version app.Version, shutdowner fx.Shutdowner) {
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			if cfg.Protocol != "milter" {
				return nil
			}
			if srv == nil || srv.Handler == nil {
				return errors.New("milter server not configured")
			}
			if srv.Addr == "" {
				return errors.New("MILTER_LISTEN must be set for milter mode")
			}
			logger.Info("milterproxy starting", "version", string(version))
			logger.Info("listening", "addr", srv.Addr, "protocol", cfg.Protocol)

			listener, err := net.Listen("tcp", srv.Addr)
			if err != nil {
				return err
			}
			srv.mu.Lock()
			srv.listener = listener
			srv.mu.Unlock()

			go func() {
				if err := srv.serve(); err != nil && !errors.Is(err, net.ErrClosed) {
					logger.Error("milterproxy failed", "err", err)
					_ = shutdowner.Shutdown()
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			srv.mu.Lock()
			listener := srv.listener
			srv.mu.Unlock()
			if listener == nil {
				return nil
			}
			if deadlineSetter, ok := listener.(interface{ SetDeadline(time.Time) error }); ok {
				_ = deadlineSetter.SetDeadline(time.Now().Add(2 * time.Second))
			}
			return listener.Close()
		},
	})
}

func (s *Server) serve() error {
	for {
		s.mu.Lock()
		listener := s.listener
		s.mu.Unlock()
		if listener == nil {
			return net.ErrClosed
		}
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		go s.Handler.HandleConn(conn)
	}
}
