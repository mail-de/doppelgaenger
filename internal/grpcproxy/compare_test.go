package grpcproxy

import (
	"reflect"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"

	"doppelgaenger/internal/config"
)

func TestCompareStatusReportsSameAndDiff(t *testing.T) {
	primary := completedStreamResult(codes.OK)
	shadow := completedStreamResult(codes.OK)

	result := compareGRPCResults(grpcCompareModeStatus, nil, primary, shadow)
	assertCompareOutcome(t, result, grpcCompareOutcomeSame)

	shadow.StatusCode = codes.PermissionDenied
	result = compareGRPCResults(grpcCompareModeStatus, nil, primary, shadow)
	assertCompareOutcome(t, result, grpcCompareOutcomeDiff)
	assertCompareDiffContains(t, result, "status primary=OK shadow=PermissionDenied")
}

func TestCompareStatusMetadataDetectsTrailerDiff(t *testing.T) {
	primary := completedStreamResult(codes.OK)
	primary.Trailer = metadata.Pairs("x-session", "primary")
	shadow := completedStreamResult(codes.OK)
	shadow.Trailer = metadata.Pairs("x-session", "shadow")

	result := compareGRPCResults(grpcCompareModeStatusMetadata, []string{"x-session"}, primary, shadow)
	assertCompareOutcome(t, result, grpcCompareOutcomeDiff)
	assertCompareDiffContains(t, result, "trailer_metadata:x-session")
}

func TestCompareStatusMetadataDetectsHeaderDiff(t *testing.T) {
	primary := completedStreamResult(codes.OK)
	primary.Header = metadata.Pairs("x-route", "primary")
	shadow := completedStreamResult(codes.OK)
	shadow.Header = metadata.Pairs("x-route", "shadow")

	result := compareGRPCResults(grpcCompareModeStatusMetadata, []string{"x-route"}, primary, shadow)
	assertCompareOutcome(t, result, grpcCompareOutcomeDiff)
	assertCompareDiffContains(t, result, "header_metadata:x-route")
}

func TestCompareMessageCountDetectsDifferentStreamLengths(t *testing.T) {
	primary := completedStreamResult(codes.OK, "one", "two")
	shadow := completedStreamResult(codes.OK, "one")

	result := compareGRPCResults(grpcCompareModeMessageCount, nil, primary, shadow)
	assertCompareOutcome(t, result, grpcCompareOutcomeDiff)
	assertCompareDiffContains(t, result, "message_count primary=2 shadow=1")
}

func TestCompareMessageHashDetectsDifferentPayloadsWithSameCount(t *testing.T) {
	primary := completedStreamResult(codes.OK, "same-count", "primary")
	shadow := completedStreamResult(codes.OK, "same-count", "shadow")

	result := compareGRPCResults(grpcCompareModeMessageHash, nil, primary, shadow)
	assertCompareOutcome(t, result, grpcCompareOutcomeDiff)
	assertCompareDiffContains(t, result, "message_hash primary=")
}

func TestCompareMessageHashDoesNotStoreResponseMessages(t *testing.T) {
	resultType := reflect.TypeOf(grpcStreamResult{})
	rawMessageType := reflect.TypeOf(rawMessage{})
	rawMessageSliceType := reflect.SliceOf(rawMessageType)

	for i := 0; i < resultType.NumField(); i++ {
		field := resultType.Field(i)
		if field.Type == rawMessageType || field.Type == rawMessageSliceType {
			t.Fatalf("grpcStreamResult must keep only count/hash summaries, found field %s with type %s", field.Name, field.Type)
		}
	}
}

func TestCompareOffSkipsComparison(t *testing.T) {
	handler := NewHandler(config.Config{GRPCCompareMode: grpcCompareModeStatus}, nil, nil, discardLogger(), nil, nil)

	result := handler.compareRPCResult(
		Decision{CompareDecision: CompareDecisionOff, CompareSkipReason: SkipReasonGRPCRule},
		grpcShadowPolicyDecision{doShadow: true},
		completedStreamResult(codes.OK),
		completedStreamResult(codes.OK),
	)

	assertCompareOutcome(t, result, grpcCompareOutcomeSkipped)

	if result.SkipReason != SkipReasonGRPCRule {
		t.Fatalf("expected grpc_rule skip reason, got %#v", result)
	}
}

func TestCompareNoShadowSkipsComparison(t *testing.T) {
	handler := NewHandler(config.Config{GRPCCompareMode: grpcCompareModeStatus}, nil, nil, discardLogger(), nil, nil)

	result := handler.compareRPCResult(
		Decision{CompareDecision: CompareDecisionInherit},
		grpcShadowPolicyDecision{},
		completedStreamResult(codes.OK),
		grpcStreamResult{},
	)

	assertCompareOutcome(t, result, grpcCompareOutcomeSkipped)

	if result.SkipReason != grpcCompareSkipReasonNoShadow {
		t.Fatalf("expected no_shadow skip reason, got %#v", result)
	}
}

func TestCompareShadowTimeoutIsVisibleAsError(t *testing.T) {
	handler := NewHandler(config.Config{GRPCCompareMode: grpcCompareModeStatus}, nil, nil, discardLogger(), nil, nil)

	result := handler.compareRPCResult(
		Decision{CompareDecision: CompareDecisionInherit},
		grpcShadowPolicyDecision{doShadow: true},
		completedStreamResult(codes.OK),
		grpcStreamResult{Started: true, SkipReason: shadowSkipReasonTimeout},
	)

	assertCompareOutcome(t, result, grpcCompareOutcomeError)

	if !strings.Contains(result.Err, shadowSkipReasonTimeout) {
		t.Fatalf("expected timeout compare error, got %#v", result)
	}
}

func TestRuleCompareMetadataReplacesGlobalKeys(t *testing.T) {
	handler := NewHandler(
		config.Config{
			GRPCCompareMode:     grpcCompareModeStatusMetadata,
			GRPCCompareMetadata: []string{"x-global"},
		},
		nil,
		nil,
		discardLogger(),
		nil,
		nil,
	)
	primary := completedStreamResult(codes.OK)
	primary.Header = metadata.Pairs("x-global", "primary")
	shadow := completedStreamResult(codes.OK)
	shadow.Header = metadata.Pairs("x-global", "shadow")

	result := handler.compareRPCResult(
		Decision{
			CompareDecision:    CompareDecisionOn,
			CompareMetadata:    []string{},
			CompareMetadataSet: true,
		},
		grpcShadowPolicyDecision{doShadow: true},
		primary,
		shadow,
	)

	assertCompareOutcome(t, result, grpcCompareOutcomeSame)

	if len(result.MetadataKeys) != 0 {
		t.Fatalf("expected explicit empty rule compare_metadata to replace globals, got %#v", result.MetadataKeys)
	}
}

func completedStreamResult(code codes.Code, messages ...string) grpcStreamResult {
	hasher := newMessageHashRecorder()
	for _, message := range messages {
		hasher.Add(rawMessage(message))
	}

	return grpcStreamResult{
		Started:      true,
		Complete:     true,
		StatusCode:   code,
		MessageCount: len(messages),
		MessageHash:  hasher.Sum(),
	}
}

func assertCompareOutcome(t *testing.T, result grpcCompareResult, expected string) {
	t.Helper()

	if result.Outcome != expected {
		t.Fatalf("expected compare outcome %q, got %#v", expected, result)
	}
}

func assertCompareDiffContains(t *testing.T, result grpcCompareResult, expected string) {
	t.Helper()

	for _, diff := range result.Diffs {
		if strings.Contains(diff, expected) {
			return
		}
	}

	t.Fatalf("expected diff containing %q, got %#v", expected, result.Diffs)
}
