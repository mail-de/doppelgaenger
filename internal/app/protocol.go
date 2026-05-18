// Package app wires application dependencies and runtime lifecycle hooks.
package app

import (
	"crypto/tls"
	"fmt"

	"go.uber.org/fx"

	"doppelgaenger/internal/backend"
	"doppelgaenger/internal/compare"
	"doppelgaenger/internal/config"
	"doppelgaenger/internal/observability"
	"doppelgaenger/internal/protocol"
)

const (
	protocolHTTP   = "http"
	protocolMilter = "milter"
)

// ProtocolAdapterDeps contains dependencies for constructing the active protocol adapter.
type ProtocolAdapterDeps struct {
	fx.In
	Config        config.Config
	PrimaryTLS    *tls.Config                  `name:"primaryTLS"`
	ShadowTLS     *tls.Config                  `name:"shadowTLS"`
	Observability *observability.Observability `optional:"true"`
}

// NewProtocolAdapter constructs the protocol adapter selected by configuration.
func NewProtocolAdapter(deps ProtocolAdapterDeps) (protocol.Adapter, error) {
	switch deps.Config.Protocol {
	case protocolHTTP:
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
			PrimaryRequester:      backend.NewRequester(backend.BackendPrimary, deps.Config.PrimaryBaseURLs, primarySelector, deps.PrimaryTLS, deps.Config.MaxBackendBodyBytes, clientCfg, deps.Observability),
			ShadowRequester:       backend.NewRequester(backend.BackendShadow, deps.Config.ShadowBaseURLs, shadowSelector, deps.ShadowTLS, deps.Config.MaxBackendBodyBytes, clientCfg, deps.Observability),
			PrimaryRequestHeaders: deps.Config.PrimaryRequestHeaders,
			ShadowRequestHeaders:  deps.Config.ShadowRequestHeaders,
		}, nil
	case protocolMilter:
		return protocol.MilterAdapter{
			PrimaryAddr:   deps.Config.PrimaryMilterAddr,
			ShadowAddr:    deps.Config.ShadowMilterAddr,
			Timeout:       deps.Config.MilterTimeout,
			Observability: deps.Observability,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported protocol: %s", deps.Config.Protocol)
	}
}

// ProtocolComparatorDeps contains dependencies for constructing the active comparator.
type ProtocolComparatorDeps struct {
	fx.In
	Config         config.Config
	HTTPComparator compare.Comparator
}

// NewProtocolComparator constructs the comparator selected by configuration.
func NewProtocolComparator(deps ProtocolComparatorDeps) (protocol.Comparator, error) {
	switch deps.Config.Protocol {
	case protocolHTTP:
		return protocol.HTTPComparator{Comparator: deps.HTTPComparator}, nil
	case protocolMilter:
		return protocol.MilterComparator{}, nil
	default:
		return nil, fmt.Errorf("unsupported protocol: %s", deps.Config.Protocol)
	}
}

// ProtocolRunnerDeps contains dependencies for constructing the protocol runner.
type ProtocolRunnerDeps struct {
	fx.In
	Config     config.Config
	Comparator protocol.Comparator
}

// NewProtocolRunner constructs the protocol runner used by HTTP and Milter flows.
func NewProtocolRunner(deps ProtocolRunnerDeps) protocol.Runner {
	timeout := deps.Config.ShadowTimeout
	if deps.Config.Protocol == protocolMilter {
		timeout = deps.Config.MilterTimeout
	}

	return protocol.Runner{
		Comparator:    deps.Comparator,
		ShadowTimeout: timeout,
	}
}
