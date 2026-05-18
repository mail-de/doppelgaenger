package protocol

import (
	"context"
	"errors"
)

// ErrString normalizes an error into a log-friendly string.
func ErrString(err error) string {
	if err == nil {
		return ""
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}

	if errors.Is(err, context.Canceled) {
		return "canceled"
	}

	return err.Error()
}
