package pathrules

import (
	"net/http"
	"testing"

	"doppelgaenger/internal/config"
)

const (
	resolverAPIMatch        = "^/api$"
	resolverRuleFirst       = "first"
	resolverRuleAll         = "all"
	resolverRuleAPI         = "api"
	resolverRulePost        = "post"
	resolverHeaderAuthState = "Auth-Status"
)

func TestResolverFirstMatchingRuleWins(t *testing.T) {
	resolver := mustResolver(t, []config.PathRule{
		{Name: resolverRuleFirst, Match: "^/api/.*", Shadow: string(ShadowModeAuto)},
		{Name: "second", Match: "^/api/users$", Shadow: string(ShadowModeAlways)},
	})

	decision := resolver.Resolve(http.MethodGet, "/api/users")
	if decision.RuleName != resolverRuleFirst {
		t.Fatalf("expected first matching rule to win, got %q", decision.RuleName)
	}

	if decision.ShadowMode != ShadowModeAuto {
		t.Fatalf("expected shadow auto from first rule, got %q", decision.ShadowMode)
	}
}

func TestResolverMethodFiltersWork(t *testing.T) {
	resolver := mustResolver(t, []config.PathRule{
		{Name: "get", Methods: []string{http.MethodGet}, Match: resolverAPIMatch, Shadow: string(ShadowModeAuto)},
		{Name: resolverRulePost, Methods: []string{http.MethodPost}, Match: resolverAPIMatch, Shadow: string(ShadowModeAlways)},
	})

	decision := resolver.Resolve("post", "/api")
	if decision.RuleName != resolverRulePost {
		t.Fatalf("expected POST method to match post rule, got %q", decision.RuleName)
	}

	if decision.ShadowMode != ShadowModeAlways {
		t.Fatalf("expected shadow always, got %q", decision.ShadowMode)
	}
}

func TestResolverOmittedMethodsMatchAllMethods(t *testing.T) {
	resolver := mustResolver(t, []config.PathRule{
		{Name: resolverRuleAll, Match: resolverAPIMatch, Compare: string(CompareDecisionOn)},
	})

	decision := resolver.Resolve("PATCH", "/api")
	if decision.RuleName != resolverRuleAll {
		t.Fatalf("expected omitted methods to match all methods, got %q", decision.RuleName)
	}

	if decision.CompareDecision != CompareDecisionOn {
		t.Fatalf("expected compare on, got %q", decision.CompareDecision)
	}
}

func TestResolverUsesFallbackRuleName(t *testing.T) {
	resolver := mustResolver(t, []config.PathRule{
		{Match: resolverAPIMatch, Shadow: string(ShadowModeAuto)},
	})

	decision := resolver.Resolve(http.MethodGet, "/api")
	if decision.RuleName != "rule[0]" {
		t.Fatalf("expected fallback rule name rule[0], got %q", decision.RuleName)
	}
}

func TestResolverUnmatchedPathIsPrimaryOnlyWhenRulesAreConfigured(t *testing.T) {
	resolver := mustResolver(t, []config.PathRule{
		{Name: resolverRuleAPI, Match: resolverAPIMatch, Shadow: string(ShadowModeAuto), Compare: string(CompareDecisionOn)},
	})

	decision := resolver.Resolve(http.MethodGet, "/other")
	if decision.ShadowMode != ShadowModeNever {
		t.Fatalf("expected unmatched configured path rules to disable shadow, got %q", decision.ShadowMode)
	}

	if decision.CompareDecision != CompareDecisionOff {
		t.Fatalf("expected unmatched configured path rules to disable compare, got %q", decision.CompareDecision)
	}

	if decision.ShadowSkipReason != SkipReasonPathUnmatched {
		t.Fatalf("expected shadow skip reason path_unmatched, got %q", decision.ShadowSkipReason)
	}

	if decision.CompareSkipReason != SkipReasonPathUnmatched {
		t.Fatalf("expected compare skip reason path_unmatched, got %q", decision.CompareSkipReason)
	}
}

func TestResolverNoRulesUsesGlobalBehavior(t *testing.T) {
	resolver := mustResolver(t, nil)

	decision := resolver.Resolve(http.MethodGet, "/other")
	if decision.ShadowMode != ShadowModeInherit {
		t.Fatalf("expected no rules to inherit global shadow behavior, got %q", decision.ShadowMode)
	}

	if decision.CompareDecision != CompareDecisionInherit {
		t.Fatalf("expected no rules to inherit global compare behavior, got %q", decision.CompareDecision)
	}

	if decision.ShadowSkipReason != "" || decision.CompareSkipReason != "" {
		t.Fatalf("expected no skip reasons, got shadow=%q compare=%q", decision.ShadowSkipReason, decision.CompareSkipReason)
	}
}

func TestResolverShadowNeverCarriesPathRuleSkipReason(t *testing.T) {
	resolver := mustResolver(t, []config.PathRule{
		{Name: "metrics", Match: "^/metrics$", Shadow: string(ShadowModeNever)},
	})

	decision := resolver.Resolve(http.MethodGet, "/metrics")
	if decision.ShadowMode != ShadowModeNever {
		t.Fatalf("expected shadow never, got %q", decision.ShadowMode)
	}

	if decision.ShadowSkipReason != SkipReasonPathRule {
		t.Fatalf("expected shadow skip reason path_rule, got %q", decision.ShadowSkipReason)
	}
}

func TestResolverCompareModeFallsBackWhenOmitted(t *testing.T) {
	resolver := mustResolver(t, []config.PathRule{
		{Name: resolverRuleAPI, Match: resolverAPIMatch, Compare: string(CompareDecisionOn)},
	})

	decision := resolver.Resolve(http.MethodGet, "/api")
	if decision.CompareMode != "" {
		t.Fatalf("expected omitted compare_mode to stay empty for later fallback, got %q", decision.CompareMode)
	}
}

func TestResolverCompareHeadersPresence(t *testing.T) {
	resolver := mustResolver(t, []config.PathRule{
		{Name: "omitted", Match: "^/omitted$"},
		{Name: "empty", Match: "^/empty$", CompareHeaders: []string{}},
	})

	omitted := resolver.Resolve(http.MethodGet, "/omitted")
	if omitted.CompareHeadersSet {
		t.Fatalf("expected omitted compare_headers to be unset")
	}

	empty := resolver.Resolve(http.MethodGet, "/empty")
	if !empty.CompareHeadersSet {
		t.Fatalf("expected explicit empty compare_headers to be set")
	}

	if len(empty.CompareHeaders) != 0 {
		t.Fatalf("expected explicit empty compare_headers to have length 0, got %#v", empty.CompareHeaders)
	}
}

func TestResolverPreservesCompareHeaders(t *testing.T) {
	resolver := mustResolver(t, []config.PathRule{
		{Name: "headers", Match: "^/headers$", CompareHeaders: []string{resolverHeaderAuthState}},
	})

	decision := resolver.Resolve(http.MethodGet, "/headers")
	if !decision.CompareHeadersSet {
		t.Fatalf("expected compare headers to be set")
	}

	if len(decision.CompareHeaders) != 1 || decision.CompareHeaders[0] != resolverHeaderAuthState {
		t.Fatalf("expected compare headers to be preserved, got %#v", decision.CompareHeaders)
	}
}

func mustResolver(t *testing.T, rules []config.PathRule) *Resolver {
	t.Helper()

	resolver, err := NewResolver(rules)
	if err != nil {
		t.Fatalf("expected resolver to build, got error: %v", err)
	}

	return resolver
}
