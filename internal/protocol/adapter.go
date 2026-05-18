// Package protocol contains shared protocol runner and adapter contracts.
package protocol

import "context"

// TestSession represents one stateful primary or shadow protocol exchange.
type TestSession interface {
	Send(event Event) error
	Receive() (Response, error)
	Close() error
}

// Adapter opens protocol-specific sessions for primary and shadow targets.
type Adapter interface {
	Protocol() string
	NewSession(ctx context.Context, target Target) (TestSession, error)
}

// Comparator compares primary and shadow protocol responses.
type Comparator interface {
	Compare(primary, shadow Response) (CompareResult, error)
}
