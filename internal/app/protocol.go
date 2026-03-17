package app

import (
	"crypto/tls"
	"fmt"

	"go.uber.org/fx"

	"doppelgaenger/internal/backend"
	"doppelgaenger/internal/compare"
	"doppelgaenger/internal/config"
	"doppelgaenger/internal/protocol"
)

type ProtocolAdapterDeps struct {
	fx.In
	Config     config.Config
	PrimaryTLS *tls.Config `name:"primaryTLS"`
	ShadowTLS  *tls.Config `name:"shadowTLS"`
}

func NewProtocolAdapter(deps ProtocolAdapterDeps) (protocol.ProtocolAdapter, error) {
	switch deps.Config.Protocol {
	case "http":
		primarySelector := backend.NewSelector(deps.Config.PrimarySelectionMode)
		shadowSelector := backend.NewSelector(deps.Config.ShadowSelectionMode)
		clientCfg := backend.HTTPClientConfig{
			DialTimeout:           deps.Config.UpstreamHTTPDialTimeout,
			TLSHandshakeTimeout:   deps.Config.UpstreamHTTPTLSHandshakeTimeout,
			ResponseHeaderTimeout: deps.Config.UpstreamHTTPResponseHeaderTimeout,
			MaxIdleConns:          deps.Config.UpstreamHTTPMaxIdleConns,
			MaxIdleConnsPerHost:   deps.Config.UpstreamHTTPMaxIdleConnsPerHost,
			MaxConnsPerHost:       deps.Config.UpstreamHTTPMaxConnsPerHost,
		}

		return protocol.HTTPAdapter{
			PrimaryRequester:      backend.NewRequester(backend.BackendPrimary, deps.Config.PrimaryBaseURLs, primarySelector, deps.PrimaryTLS, deps.Config.MaxBackendBodyBytes, clientCfg),
			ShadowRequester:       backend.NewRequester(backend.BackendShadow, deps.Config.ShadowBaseURLs, shadowSelector, deps.ShadowTLS, deps.Config.MaxBackendBodyBytes, clientCfg),
			PrimaryRequestHeaders: deps.Config.PrimaryRequestHeaders,
			ShadowRequestHeaders:  deps.Config.ShadowRequestHeaders,
		}, nil
	case "milter":
		return protocol.MilterAdapter{
			PrimaryAddr: deps.Config.PrimaryMilterAddr,
			ShadowAddr:  deps.Config.ShadowMilterAddr,
			Timeout:     deps.Config.MilterTimeout,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported protocol: %s", deps.Config.Protocol)
	}
}

type ProtocolComparatorDeps struct {
	fx.In
	Config         config.Config
	HTTPComparator compare.Comparator
}

func NewProtocolComparator(deps ProtocolComparatorDeps) (protocol.Comparator, error) {
	switch deps.Config.Protocol {
	case "http":
		return protocol.HTTPComparator{Comparator: deps.HTTPComparator}, nil
	case "milter":
		return protocol.MilterComparator{}, nil
	default:
		return nil, fmt.Errorf("unsupported protocol: %s", deps.Config.Protocol)
	}
}

type ProtocolRunnerDeps struct {
	fx.In
	Config     config.Config
	Comparator protocol.Comparator
}

func NewProtocolRunner(deps ProtocolRunnerDeps) protocol.Runner {
	timeout := deps.Config.ShadowTimeout
	if deps.Config.Protocol == "milter" {
		timeout = deps.Config.MilterTimeout
	}
	return protocol.Runner{
		Comparator:    deps.Comparator,
		ShadowTimeout: timeout,
	}
}
