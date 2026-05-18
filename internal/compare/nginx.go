package compare

import (
	"log/slog"

	"doppelgaenger/internal/backend"
)

type nginxComparator struct {
	baseComparator
	logger *slog.Logger
}

func (c *nginxComparator) Compare(primary, shadow backend.Result) (Result, error) {
	pKV, sKV, diffs, hasDiff := c.compareHeaders(primary.Header, shadow.Header)

	return Result{
		Mode:          ModeNginx,
		Diff:          hasDiff,
		HeaderDiff:    hasDiff,
		HeaderPrimary: pKV,
		HeaderShadow:  sKV,
		HeaderDiffs:   diffs,
		BodyDiff:      false,
	}, nil
}
