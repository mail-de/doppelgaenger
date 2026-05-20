package grpcproxy

import (
	"testing"

	"doppelgaenger/internal/config"
)

const (
	resolverFullMethodAuth       = "/pkg.Service/Auth"
	resolverFullMethodLookup     = "/pkg.Service/Lookup"
	resolverFullMethodOther      = "/pkg.Other/Auth"
	resolverService              = "pkg.Service"
	resolverMethodAuth           = "Auth"
	resolverRuleFirst            = "first"
	resolverRuleCatchAll         = "catch-all"
	resolverRulePrimaryOnly      = "primary-only"
	resolverPrimaryMetadataKey   = "authorization"
	resolverPrimaryMetadataValue = "Basic primary-token"
)

func TestResolverFirstMatchingRuleWins(t *testing.T) {
	resolver := mustResolver(t, []config.GRPCRule{
		{Name: resolverRuleFirst, Service: resolverService, Methods: []string{resolverMethodAuth}, Shadow: string(ShadowModeAuto)},
		{Name: "second", Service: resolverService, Methods: []string{resolverMethodAuth}, Shadow: string(ShadowModeAlways)},
	})

	decision := mustResolve(t, resolver, resolverFullMethodAuth)
	if decision.RuleName != resolverRuleFirst {
		t.Fatalf("expected first matching rule to win, got %q", decision.RuleName)
	}

	if decision.ShadowMode != ShadowModeAuto {
		t.Fatalf("expected shadow auto from first rule, got %q", decision.ShadowMode)
	}
}

func TestResolverCatchAll(t *testing.T) {
	resolver := mustResolver(t, []config.GRPCRule{
		{Name: resolverRuleCatchAll, Service: "*", Shadow: string(ShadowModeAlways), Compare: string(CompareDecisionOn)},
	})

	decision := mustResolve(t, resolver, resolverFullMethodOther)
	if decision.RuleName != resolverRuleCatchAll {
		t.Fatalf("expected catch-all rule to match, got %q", decision.RuleName)
	}

	if decision.Service != "pkg.Other" || decision.Method != "Auth" {
		t.Fatalf("expected full method to split into service/method, got service=%q method=%q", decision.Service, decision.Method)
	}

	if decision.ShadowMode != ShadowModeAlways {
		t.Fatalf("expected catch-all shadow always, got %q", decision.ShadowMode)
	}

	if decision.CompareDecision != CompareDecisionOn {
		t.Fatalf("expected catch-all compare on, got %q", decision.CompareDecision)
	}
}

func TestResolverNoMatchWithRulesIsPrimaryOnly(t *testing.T) {
	resolver := mustResolver(t, []config.GRPCRule{
		{Name: "auth", Service: resolverService, Methods: []string{resolverMethodAuth}, Shadow: string(ShadowModeAuto), Compare: string(CompareDecisionOn)},
	})

	decision := mustResolve(t, resolver, resolverFullMethodLookup)
	if decision.ShadowMode != ShadowModeNever {
		t.Fatalf("expected unmatched configured gRPC rules to disable shadow, got %q", decision.ShadowMode)
	}

	if decision.CompareDecision != CompareDecisionOff {
		t.Fatalf("expected unmatched configured gRPC rules to disable compare, got %q", decision.CompareDecision)
	}

	if decision.ShadowSkipReason != SkipReasonGRPCRuleUnmatched {
		t.Fatalf("expected unmatched shadow skip reason, got %q", decision.ShadowSkipReason)
	}
}

func TestResolverNoRulesUsesGlobalBehavior(t *testing.T) {
	resolver := mustResolver(t, nil)

	decision := mustResolve(t, resolver, resolverFullMethodOther)
	if decision.ShadowMode != ShadowModeInherit {
		t.Fatalf("expected no rules to inherit global shadow behavior, got %q", decision.ShadowMode)
	}

	if decision.CompareDecision != CompareDecisionInherit {
		t.Fatalf("expected no rules to inherit global compare behavior, got %q", decision.CompareDecision)
	}
}

func TestResolverShadowNeverIsVisible(t *testing.T) {
	resolver := mustResolver(t, []config.GRPCRule{
		{Name: resolverRulePrimaryOnly, Service: resolverService, Shadow: string(ShadowModeNever)},
	})

	decision := mustResolve(t, resolver, resolverFullMethodAuth)
	if decision.ShadowMode != ShadowModeNever {
		t.Fatalf("expected shadow never, got %q", decision.ShadowMode)
	}

	if decision.ShadowSkipReason != SkipReasonGRPCRule {
		t.Fatalf("expected grpc rule skip reason, got %q", decision.ShadowSkipReason)
	}
}

func TestResolverUsesFallbackRuleNameAndClonesMetadata(t *testing.T) {
	resolver := mustResolver(t, []config.GRPCRule{
		{
			Service:         resolverService,
			PrimaryMetadata: map[string]string{resolverPrimaryMetadataKey: resolverPrimaryMetadataValue},
		},
	})

	decision := mustResolve(t, resolver, resolverFullMethodAuth)
	if decision.RuleName != "rule[0]" {
		t.Fatalf("expected fallback rule name rule[0], got %q", decision.RuleName)
	}

	decision.PrimaryMetadata[resolverPrimaryMetadataKey] = "mutated"

	next := mustResolve(t, resolver, resolverFullMethodAuth)
	if next.PrimaryMetadata[resolverPrimaryMetadataKey] != resolverPrimaryMetadataValue {
		t.Fatalf("expected resolver to clone primary metadata, got %#v", next.PrimaryMetadata)
	}
}

func mustResolver(t *testing.T, rules []config.GRPCRule) *Resolver {
	t.Helper()

	resolver, err := NewResolver(rules)
	if err != nil {
		t.Fatalf("expected resolver to build, got error: %v", err)
	}

	return resolver
}

func mustResolve(t *testing.T, resolver *Resolver, fullMethod string) Decision {
	t.Helper()

	decision, err := resolver.Resolve(fullMethod)
	if err != nil {
		t.Fatalf("expected resolver to resolve, got error: %v", err)
	}

	return decision
}
