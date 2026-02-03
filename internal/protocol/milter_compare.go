package protocol

import "bytes"

type MilterComparator struct{}

func (c MilterComparator) Compare(primary, shadow Response) (CompareResult, error) {
	result := CompareResult{Mode: "milter"}
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
