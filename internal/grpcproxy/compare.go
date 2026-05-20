package grpcproxy

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const (
	grpcCompareModeStatus         = "status"
	grpcCompareModeStatusMetadata = "status_metadata"
	grpcCompareModeMessageCount   = "message_count"
	grpcCompareModeMessageHash    = "message_hash"

	grpcMetadataStatus  = "grpc-status"
	grpcMetadataMessage = "grpc-message"
	grpcMetadataHeader  = "header"
	grpcMetadataTrailer = "trailer"

	grpcCompareOutcomeSame    = "same"
	grpcCompareOutcomeDiff    = "diff"
	grpcCompareOutcomeSkipped = "skipped"
	grpcCompareOutcomeError   = "error"

	grpcCompareSkipReasonNoShadow = "no_shadow"
)

type grpcCompareResult struct {
	Enabled      bool
	Outcome      string
	Mode         string
	MetadataKeys []string
	Diff         bool
	Diffs        []string
	Err          string
	SkipReason   string
}

func (h *Handler) compareRPCResult(
	decision Decision,
	shadowDecision grpcShadowPolicyDecision,
	primaryResult grpcStreamResult,
	shadowResult grpcStreamResult,
) grpcCompareResult {
	mode, metadataKeys := h.resolveCompareConfig(decision)

	if decision.CompareDecision == CompareDecisionOff {
		reason := decision.CompareSkipReason
		if reason == "" {
			reason = SkipReasonGRPCRule
		}

		return skippedGRPCCompareResult(mode, metadataKeys, reason)
	}

	if !shadowDecision.doShadow {
		reason := shadowResult.SkipReason
		if reason == "" {
			reason = grpcCompareSkipReasonNoShadow
		}

		return skippedGRPCCompareResult(mode, metadataKeys, reason)
	}

	if err := incompleteShadowCompareError(shadowResult); err != "" {
		return errorGRPCCompareResult(mode, metadataKeys, err)
	}

	return compareGRPCResults(mode, metadataKeys, primaryResult, shadowResult)
}

func (h *Handler) resolveCompareConfig(decision Decision) (string, []string) {
	mode := ""
	if h != nil {
		mode = h.cfg.GRPCCompareMode
	}

	if decision.CompareDecision == CompareDecisionOn && decision.CompareMode != "" {
		mode = decision.CompareMode
	}

	mode = normalizeGRPCCompareMode(mode)

	var metadataKeys []string
	if decision.CompareMetadataSet {
		metadataKeys = decision.CompareMetadata
	} else if h != nil {
		metadataKeys = h.cfg.GRPCCompareMetadata
	}

	return mode, normalizeCompareMetadataKeys(metadataKeys)
}

func compareGRPCResults(
	mode string,
	metadataKeys []string,
	primaryResult grpcStreamResult,
	shadowResult grpcStreamResult,
) grpcCompareResult {
	mode = normalizeGRPCCompareMode(mode)
	result := grpcCompareResult{
		Enabled:      true,
		Outcome:      grpcCompareOutcomeSame,
		Mode:         mode,
		MetadataKeys: normalizeCompareMetadataKeys(metadataKeys),
	}

	switch mode {
	case grpcCompareModeStatus:
		result.addStatusDiff(primaryResult, shadowResult)
	case grpcCompareModeStatusMetadata:
		result.addStatusDiff(primaryResult, shadowResult)
		result.addMetadataDiffs(primaryResult, shadowResult)
	case grpcCompareModeMessageCount:
		result.addStatusDiff(primaryResult, shadowResult)
		result.addMessageCountDiff(primaryResult, shadowResult)
	case grpcCompareModeMessageHash:
		result.addStatusDiff(primaryResult, shadowResult)
		result.addMessageHashDiff(primaryResult, shadowResult)
	default:
		return errorGRPCCompareResult(mode, metadataKeys, fmt.Sprintf("unsupported gRPC compare mode %q", mode))
	}

	if len(result.Diffs) > 0 {
		result.Diff = true
		result.Outcome = grpcCompareOutcomeDiff
	}

	return result
}

func (r *grpcCompareResult) addStatusDiff(primaryResult, shadowResult grpcStreamResult) {
	if primaryResult.StatusCode == shadowResult.StatusCode {
		return
	}

	r.Diffs = append(r.Diffs, fmt.Sprintf(
		"status primary=%s shadow=%s",
		primaryResult.StatusCode.String(),
		shadowResult.StatusCode.String(),
	))
}

func (r *grpcCompareResult) addMetadataDiffs(primaryResult, shadowResult grpcStreamResult) {
	for _, key := range r.MetadataKeys {
		for _, location := range []string{grpcMetadataHeader, grpcMetadataTrailer} {
			primaryValues := compareMetadataValues(primaryResult, location, key)

			shadowValues := compareMetadataValues(shadowResult, location, key)
			if equalStringSlices(primaryValues, shadowValues) {
				continue
			}

			r.Diffs = append(r.Diffs, fmt.Sprintf(
				"%s_metadata:%s primary=%q shadow=%q",
				location,
				key,
				primaryValues,
				shadowValues,
			))
		}
	}
}

func (r *grpcCompareResult) addMessageCountDiff(primaryResult, shadowResult grpcStreamResult) {
	if primaryResult.MessageCount == shadowResult.MessageCount {
		return
	}

	r.Diffs = append(r.Diffs, fmt.Sprintf(
		"message_count primary=%d shadow=%d",
		primaryResult.MessageCount,
		shadowResult.MessageCount,
	))
}

func (r *grpcCompareResult) addMessageHashDiff(primaryResult, shadowResult grpcStreamResult) {
	primaryHash := streamMessageHash(primaryResult)

	shadowHash := streamMessageHash(shadowResult)
	if primaryHash == shadowHash {
		return
	}

	r.Diffs = append(r.Diffs, fmt.Sprintf(
		"message_hash primary=%s shadow=%s",
		primaryHash,
		shadowHash,
	))
}

func skippedGRPCCompareResult(mode string, metadataKeys []string, reason string) grpcCompareResult {
	if reason == "" {
		reason = grpcCompareSkipReasonNoShadow
	}

	return grpcCompareResult{
		Outcome:      grpcCompareOutcomeSkipped,
		Mode:         normalizeGRPCCompareMode(mode),
		MetadataKeys: normalizeCompareMetadataKeys(metadataKeys),
		SkipReason:   reason,
	}
}

func errorGRPCCompareResult(mode string, metadataKeys []string, err string) grpcCompareResult {
	return grpcCompareResult{
		Enabled:      true,
		Outcome:      grpcCompareOutcomeError,
		Mode:         normalizeGRPCCompareMode(mode),
		MetadataKeys: normalizeCompareMetadataKeys(metadataKeys),
		Err:          err,
	}
}

func incompleteShadowCompareError(shadowResult grpcStreamResult) string {
	if shadowResult.SkipReason != "" {
		return fmt.Sprintf("shadow incomplete: %s", shadowResult.SkipReason)
	}

	if !shadowResult.Started {
		return "shadow incomplete: not_started"
	}

	if !shadowResult.Complete {
		return "shadow incomplete: no_final_status"
	}

	return ""
}

func compareMetadataValues(result grpcStreamResult, location string, key string) []string {
	normalized := normalizeMetadataKey(key)
	if location == grpcMetadataTrailer {
		switch normalized {
		case grpcMetadataStatus:
			return []string{strconv.Itoa(int(result.StatusCode))}
		case grpcMetadataMessage:
			if result.StatusMessage == "" {
				return nil
			}

			return []string{result.StatusMessage}
		}
	}

	md := result.Header
	if location == grpcMetadataTrailer {
		md = result.Trailer
	}

	values := md.Get(normalized)
	if len(values) == 0 {
		return nil
	}

	cloned := make([]string, len(values))
	copy(cloned, values)

	return cloned
}

func normalizeGRPCCompareMode(mode string) string {
	normalized := strings.ToLower(strings.TrimSpace(mode))
	if normalized == "" {
		return grpcCompareModeStatus
	}

	return normalized
}

func normalizeCompareMetadataKeys(keys []string) []string {
	if len(keys) == 0 {
		if keys == nil {
			return nil
		}

		return []string{}
	}

	seen := make(map[string]struct{}, len(keys))

	normalized := make([]string, 0, len(keys))
	for _, key := range keys {
		key = normalizeMetadataKey(key)
		if key == "" {
			continue
		}

		if _, ok := seen[key]; ok {
			continue
		}

		seen[key] = struct{}{}
		normalized = append(normalized, key)
	}

	sort.Strings(normalized)

	return normalized
}

func streamMessageHash(result grpcStreamResult) string {
	if result.MessageHash != "" {
		return result.MessageHash
	}

	return emptyMessageHash()
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}

	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}

	return true
}
