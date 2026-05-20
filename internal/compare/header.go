package compare

import (
	"log/slog"

	"doppelgaenger/internal/backend"
)

type headerComparator struct {
	baseComparator
	logger *slog.Logger
}

func (c *headerComparator) Compare(primary, shadow backend.Result) (Result, error) {
	return c.CompareWithHeaders(primary, shadow, nil)
}

func (c *headerComparator) CompareWithHeaders(primary, shadow backend.Result, compareHeaders []string) (Result, error) {
	pKV, sKV, diffs, hasDiff := c.compareHeaders(primary.Header, shadow.Header, compareHeaders)

	return Result{
		Mode:          ModeHeader,
		Diff:          hasDiff,
		HeaderDiff:    hasDiff,
		HeaderPrimary: pKV,
		HeaderShadow:  sKV,
		HeaderDiffs:   diffs,
		BodyDiff:      false,
	}, nil
}
