package grpcproxy

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"sync/atomic"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"doppelgaenger/internal/config"
)

// Target is one long-lived gRPC upstream connection.
type Target struct {
	Name      string
	Address   string
	Authority string
	Conn      *grpc.ClientConn
}

// TargetPool owns the selectable targets for one backend class.
type TargetPool struct {
	targets []*Target
	mode    string
	next    atomic.Uint64
}

// TargetPools groups primary and shadow upstream pools.
type TargetPools struct {
	Primary *TargetPool
	Shadow  *TargetPool
}

// NewTargetPools creates long-lived upstream connections for gRPC mode.
func NewTargetPools(cfg config.Config) (*TargetPools, error) {
	if cfg.Protocol != ProtocolName {
		return &TargetPools{}, nil
	}

	primary, err := newTargetPool(cfg.PrimaryGRPCTargets, cfg.PrimaryGRPCSelectionMode, cfg)
	if err != nil {
		return nil, fmt.Errorf("primary gRPC targets: %w", err)
	}

	shadow, err := newTargetPool(cfg.ShadowGRPCTargets, cfg.ShadowGRPCSelectionMode, cfg)
	if err != nil {
		_ = primary.Close()

		return nil, fmt.Errorf("shadow gRPC targets: %w", err)
	}

	return &TargetPools{Primary: primary, Shadow: shadow}, nil
}

func newTargetPool(targets []config.GRPCTarget, mode string, cfg config.Config) (*TargetPool, error) {
	pool := &TargetPool{mode: mode}

	for i, targetCfg := range targets {
		target, err := newTarget(i, targetCfg, cfg)
		if err != nil {
			_ = pool.Close()

			return nil, err
		}

		pool.targets = append(pool.targets, target)
	}

	return pool, nil
}

func newTarget(index int, targetCfg config.GRPCTarget, cfg config.Config) (*Target, error) {
	creds, err := targetCredentials(targetCfg.TLS)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", targetName(index, targetCfg), err)
	}

	options := []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithDefaultCallOptions(defaultCallOptions(cfg)...),
	}
	if targetCfg.Authority != "" {
		options = append(options, grpc.WithAuthority(targetCfg.Authority))
	}

	conn, err := grpc.NewClient(targetCfg.Address, options...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", targetName(index, targetCfg), err)
	}

	return &Target{
		Name:      targetName(index, targetCfg),
		Address:   targetCfg.Address,
		Authority: targetCfg.Authority,
		Conn:      conn,
	}, nil
}

func targetCredentials(cfg config.GRPCTargetTLS) (credentials.TransportCredentials, error) {
	if !cfg.Enabled {
		return insecure.NewCredentials(), nil
	}

	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         cfg.ServerName,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	}

	if cfg.RootCA != "" {
		pool, err := certPoolFromFile(cfg.RootCA)
		if err != nil {
			return nil, err
		}

		tlsConfig.RootCAs = pool
	}

	if cfg.ClientCert != "" || cfg.ClientKey != "" {
		certificate, err := tls.LoadX509KeyPair(cfg.ClientCert, cfg.ClientKey)
		if err != nil {
			return nil, err
		}

		tlsConfig.Certificates = []tls.Certificate{certificate}
	}

	return credentials.NewTLS(tlsConfig), nil
}

func targetName(index int, targetCfg config.GRPCTarget) string {
	if targetCfg.Name != "" {
		return targetCfg.Name
	}

	return fmt.Sprintf("target[%d]", index)
}

func defaultCallOptions(cfg config.Config) []grpc.CallOption {
	options := []grpc.CallOption{grpc.ForceCodec(rawCodec{})}
	if cfg.GRPCMaxReceiveMessageBytes > 0 {
		options = append(options, grpc.MaxCallRecvMsgSize(cfg.GRPCMaxReceiveMessageBytes))
	}

	if cfg.GRPCMaxSendMessageBytes > 0 {
		options = append(options, grpc.MaxCallSendMsgSize(cfg.GRPCMaxSendMessageBytes))
	}

	return options
}

// Close closes all target connections in every pool.
func (p *TargetPools) Close() error {
	if p == nil {
		return nil
	}

	return errors.Join(p.Primary.Close(), p.Shadow.Close())
}

// Close closes all target connections in the pool.
func (p *TargetPool) Close() error {
	if p == nil {
		return nil
	}

	var closeErr error

	for _, target := range p.targets {
		if target == nil || target.Conn == nil {
			continue
		}

		closeErr = errors.Join(closeErr, target.Conn.Close())
	}

	return closeErr
}

func certPoolFromFile(path string) (*x509.CertPool, error) {
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	pool := x509.NewCertPool()
	if ok := pool.AppendCertsFromPEM(pemBytes); !ok {
		return nil, fmt.Errorf("failed to parse CA PEM: %s", path)
	}

	return pool, nil
}
