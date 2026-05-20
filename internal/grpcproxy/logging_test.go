package grpcproxy

import (
	"testing"

	"google.golang.org/grpc/codes"

	"doppelgaenger/internal/config"
)

func TestLogRPCResultLogOnlyOnDiffSuppressesCleanComparison(t *testing.T) {
	logger, logs := newCaptureLogger()
	handler := &Handler{
		cfg:    config.Config{LogOnlyOnDiff: true},
		logger: logger,
	}

	handler.logRPCResult(
		&grpcRPCContext{reqID: 1, remote: testRemoteAddr},
		testFullMethodUnary,
		Decision{Service: testServiceName, Method: testMethodUnary},
		&Target{Name: testPrimaryTargetName},
		grpcShadowPolicyDecision{doShadow: true},
		grpcStreamResult{StatusCode: codes.OK},
		grpcStreamResult{Started: true, StatusCode: codes.OK},
		grpcCompareResult{Enabled: true, Outcome: grpcCompareOutcomeSame, Mode: grpcCompareModeStatus},
	)

	if record := logs.last(); record != nil {
		t.Fatalf("expected clean gRPC comparison log to be suppressed, got %#v", record)
	}
}

func TestLogRPCResultLogOnlyOnDiffKeepsDiff(t *testing.T) {
	logger, logs := newCaptureLogger()
	handler := &Handler{
		cfg:    config.Config{LogOnlyOnDiff: true},
		logger: logger,
	}

	handler.logRPCResult(
		&grpcRPCContext{reqID: 1, remote: testRemoteAddr},
		testFullMethodUnary,
		Decision{Service: testServiceName, Method: testMethodUnary},
		&Target{Name: testPrimaryTargetName},
		grpcShadowPolicyDecision{doShadow: true},
		grpcStreamResult{StatusCode: codes.OK},
		grpcStreamResult{Started: true, StatusCode: codes.PermissionDenied},
		grpcCompareResult{
			Enabled: true,
			Outcome: grpcCompareOutcomeDiff,
			Mode:    grpcCompareModeStatus,
			Diff:    true,
			Diffs:   []string{"status: OK != PermissionDenied"},
		},
	)

	record := logs.last()
	assertLogField(t, record, "msg", "grpc_proxy")
	assertLogField(t, record, "diff", "true")
	assertLogField(t, record, "compare_outcome", grpcCompareOutcomeDiff)
}
