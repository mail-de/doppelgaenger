package protocol

import (
	"errors"

	"doppelgaenger/internal/backend"
	"doppelgaenger/internal/compare"
)

type HTTPComparator struct {
	Comparator compare.Comparator
}

func (c HTTPComparator) Compare(primary, shadow Response) (CompareResult, error) {
	if c.Comparator == nil {
		return CompareResult{}, errors.New("missing http comparator")
	}

	primaryResult := backend.BackendResult{
		Header:   primary.Header,
		Body:     primary.Body,
		Err:      primary.Err,
		Proto:    primary.Proto,
		Duration: primary.Duration,
		Status:   primary.Status,
	}
	shadowResult := backend.BackendResult{
		Header:   shadow.Header,
		Body:     shadow.Body,
		Err:      shadow.Err,
		Proto:    shadow.Proto,
		Duration: shadow.Duration,
		Status:   shadow.Status,
	}

	result, err := c.Comparator.Compare(primaryResult, shadowResult)
	return CompareResult{
		Mode:           result.Mode,
		Diff:           result.Diff,
		HeaderDiff:     result.HeaderDiff,
		HeaderPrimary:  result.HeaderPrimary,
		HeaderShadow:   result.HeaderShadow,
		HeaderDiffs:    result.HeaderDiffs,
		BodyDiff:       result.BodyDiff,
		JSONDiffs:      result.JSONDiffs,
		HTMLSimilarity: result.HTMLSimilarity,
	}, err
}
