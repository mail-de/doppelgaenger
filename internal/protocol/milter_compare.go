package protocol

import "bytes"

// MilterComparator compares raw Milter decisions and frames.
type MilterComparator struct{}

// Compare compares primary and shadow Milter responses.
func (c MilterComparator) Compare(primary, shadow Response) (CompareResult, error) {
	result := CompareResult{Mode: protocolMilter}
	if primary.Decision != shadow.Decision {
		result.DecisionDiff = true
	}

	if !bytes.Equal(primary.Raw, shadow.Raw) {
		result.Diff = true
	} else {
		result.Diff = result.DecisionDiff
	}

	return result, nil
}
