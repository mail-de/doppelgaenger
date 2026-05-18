package protocol

import "testing"

func TestMilterComparatorDecisionDiff(t *testing.T) {
	cmp := MilterComparator{}
	primary := Response{Decision: DecisionAccept, Raw: []byte("a")}
	shadow := Response{Decision: DecisionReject, Raw: []byte("a")}

	result, err := cmp.Compare(primary, shadow)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if !result.DecisionDiff {
		t.Fatalf("expected decision diff")
	}

	if !result.Diff {
		t.Fatalf("expected diff to be true")
	}
}

func TestMilterComparatorRawDiff(t *testing.T) {
	cmp := MilterComparator{}
	primary := Response{Decision: DecisionAccept, Raw: []byte("a")}
	shadow := Response{Decision: DecisionAccept, Raw: []byte("b")}

	result, err := cmp.Compare(primary, shadow)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if result.DecisionDiff {
		t.Fatalf("did not expect decision diff")
	}

	if !result.Diff {
		t.Fatalf("expected diff due to raw mismatch")
	}
}
