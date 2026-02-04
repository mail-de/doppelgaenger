package app

import (
	"fmt"

	"go.uber.org/fx"

	"doppelgaenger/internal/backend"
	"doppelgaenger/internal/compare"
	"doppelgaenger/internal/config"
	"doppelgaenger/internal/protocol"
)

type ProtocolAdapterDeps struct {
	fx.In
	Config      config.Config
	PrimaryPool backend.Pool `name:"primaryPool"`
	ShadowPool  backend.Pool `name:"shadowPool"`
}

func NewProtocolAdapter(deps ProtocolAdapterDeps) (protocol.ProtocolAdapter, error) {
	switch deps.Config.Protocol {
	case "http":
		return protocol.HTTPAdapter{PrimaryPool: deps.PrimaryPool, ShadowPool: deps.ShadowPool}, nil
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
