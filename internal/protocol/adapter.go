package protocol

import "context"

type TestSession interface {
	Send(event Event) error
	Receive() (Response, error)
	Close() error
}

type ProtocolAdapter interface {
	Protocol() string
	NewSession(ctx context.Context, target Target) (TestSession, error)
}

type Comparator interface {
	Compare(primary, shadow Response) (CompareResult, error)
}
