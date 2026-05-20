package grpcproxy

import (
	"time"

	"google.golang.org/grpc/codes"
)

func (h *Handler) logRPCResult(
	rpcCtx *grpcRPCContext,
	fullMethod string,
	decision Decision,
	primaryTarget *Target,
	shadowDecision grpcShadowPolicyDecision,
	primaryResult grpcStreamResult,
	shadowResult grpcStreamResult,
	compareResult grpcCompareResult,
) {
	if h == nil || h.logger == nil {
		return
	}

	attrs := grpcBaseLogAttributes(rpcCtx, fullMethod, decision)
	attrs = append(attrs, grpcShadowDecisionLogAttributes(shadowDecision, shadowResult, decision)...)
	attrs = append(attrs, grpcPrimaryLogAttributes(primaryTarget, primaryResult)...)
	attrs = append(attrs, grpcShadowResultLogAttributes(shadowResult)...)
	attrs = append(attrs, grpcCompareLogAttributes(compareResult)...)
	h.logger.Info("grpc_proxy", attrs...)
}

func grpcBaseLogAttributes(rpcCtx *grpcRPCContext, fullMethod string, decision Decision) []any {
	return []any{
		"event", "grpc_proxy",
		"req_id", rpcCtx.reqID,
		"trace_id", rpcCtx.traceID,
		"remote", rpcCtx.remote,
		"protocol", ProtocolName,
		"full_method", fullMethod,
		"service", decision.Service,
		"method", decision.Method,
		"grpc_rule", decision.RuleName,
	}
}

func grpcShadowDecisionLogAttributes(
	shadowDecision grpcShadowPolicyDecision,
	shadowResult grpcStreamResult,
	decision Decision,
) []any {
	return []any{
		"shadow_enabled", shadowDecision.doShadow,
		"shadow_forced", shadowDecision.forced,
		"shadow_started", shadowResult.Started,
		"shadow_mode", string(decision.ShadowMode),
		"shadow_skip_reason", shadowResult.SkipReason,
	}
}

func grpcPrimaryLogAttributes(primaryTarget *Target, primaryResult grpcStreamResult) []any {
	primarySelected := ""
	if primaryTarget != nil {
		primarySelected = primaryTarget.Name
	}

	return []any{
		"primary_selected", primarySelected,
		"primary_status", primaryResult.StatusCode.String(),
		"primary_status_message", primaryResult.StatusMessage,
		"primary_messages", primaryResult.MessageCount,
		"primary_message_count", primaryResult.MessageCount,
		"primary_message_hash", streamMessageHash(primaryResult),
		"primary_dur_ms", durationMillis(primaryResult.Duration),
	}
}

func grpcShadowResultLogAttributes(shadowResult grpcStreamResult) []any {
	shadowStatus := shadowResult.StatusCode
	if !shadowResult.Started && shadowStatus == codes.OK && shadowResult.Err != "" {
		shadowStatus = codes.Unavailable
	}

	return []any{
		"shadow_selected", shadowResult.Selected,
		"shadow_status", shadowStatus.String(),
		"shadow_status_message", shadowResult.StatusMessage,
		"shadow_messages", shadowResult.MessageCount,
		"shadow_message_count", shadowResult.MessageCount,
		"shadow_message_hash", streamMessageHash(shadowResult),
		"shadow_dur_ms", durationMillis(shadowResult.Duration),
		"shadow_err", shadowResult.Err,
	}
}

func grpcCompareLogAttributes(compareResult grpcCompareResult) []any {
	return []any{
		"compare_mode", compareResult.Mode,
		"compare_enabled", compareResult.Enabled,
		"compare_outcome", compareResult.Outcome,
		"compare_skip_reason", compareResult.SkipReason,
		"diff", compareResult.Diff,
		"diffs", compareResult.Diffs,
		"compare_err", compareResult.Err,
	}
}

func durationMillis(duration time.Duration) int64 {
	if duration <= 0 {
		return 0
	}

	return duration.Milliseconds()
}
