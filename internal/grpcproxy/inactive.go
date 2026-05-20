package grpcproxy

import (
	"context"
	"errors"

	"doppelgaenger/internal/protocol"
)

// InactiveAdapter satisfies inactive HTTP/Milter wiring while gRPC uses its own lifecycle.
type InactiveAdapter struct{}

// NewInactiveAdapter returns a protocol adapter that should never be called at runtime.
func NewInactiveAdapter() InactiveAdapter {
	return InactiveAdapter{}
}

// Protocol reports the selected protocol name.
func (InactiveAdapter) Protocol() string {
	return ProtocolName
}

// NewSession fails loudly if an inactive HTTP/Milter path is accidentally used in gRPC mode.
func (InactiveAdapter) NewSession(_ context.Context, _ protocol.Target) (protocol.TestSession, error) {
	return nil, errors.New("gRPC mode uses the dedicated grpcproxy lifecycle")
}

// InactiveComparator satisfies inactive HTTP/Milter runner wiring in gRPC mode.
type InactiveComparator struct{}

// NewInactiveComparator returns a no-op comparator for inactive protocol runners.
func NewInactiveComparator() InactiveComparator {
	return InactiveComparator{}
}

// Compare is a no-op because gRPC comparison is handled by the dedicated gRPC path.
func (InactiveComparator) Compare(_, _ protocol.Response) (protocol.CompareResult, error) {
	return protocol.CompareResult{}, nil
}
