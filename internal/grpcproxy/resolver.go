// Package grpcproxy contains the gRPC proxy lifecycle and deterministic rule resolver.
package grpcproxy

import (
	"errors"
	"fmt"
	"strings"

	"doppelgaenger/internal/config"
)

// ProtocolName is the configuration value that activates the gRPC proxy.
const ProtocolName = "grpc"

// ShadowMode describes how a matched gRPC rule affects shadowing.
type ShadowMode string

const (
	// ShadowModeInherit preserves the global shadow decision.
	ShadowModeInherit ShadowMode = "inherit"
	// ShadowModeAuto allows normal sampling and force-metadata behavior.
	ShadowModeAuto ShadowMode = "auto"
	// ShadowModeNever disables shadowing for a matched gRPC rule.
	ShadowModeNever ShadowMode = "never"
	// ShadowModeAlways starts shadowing without sampling, subject to runtime limits.
	ShadowModeAlways ShadowMode = "always"
)

// CompareDecision describes how a matched gRPC rule affects comparison.
type CompareDecision string

const (
	// CompareDecisionInherit preserves the global comparison decision.
	CompareDecisionInherit CompareDecision = "inherit"
	// CompareDecisionOn enables comparison when shadowing runs.
	CompareDecisionOn CompareDecision = "on"
	// CompareDecisionOff disables comparison for a matched gRPC rule.
	CompareDecisionOff CompareDecision = "off"
)

const (
	// SkipReasonGRPCRule indicates that a matched gRPC rule disabled work.
	SkipReasonGRPCRule = "grpc_rule"
	// SkipReasonGRPCRuleUnmatched indicates that configured gRPC rules had no match.
	SkipReasonGRPCRuleUnmatched = "grpc_rule_unmatched"
)

// Decision is the deterministic policy result for one gRPC service and method.
type Decision struct {
	RuleName           string
	Service            string
	Method             string
	ShadowMode         ShadowMode
	CompareDecision    CompareDecision
	CompareMode        string
	CompareMetadata    []string
	CompareMetadataSet bool
	PrimaryMetadata    map[string]string
	ShadowMetadata     map[string]string
	ShadowSkipReason   string
	CompareSkipReason  string
}

// Resolver resolves the first configured gRPC rule matching a full method.
type Resolver struct {
	rules []compiledRule
}

type compiledRule struct {
	name               string
	service            string
	methods            map[string]struct{}
	shadow             ShadowMode
	compare            CompareDecision
	compareMode        string
	compareMetadata    []string
	compareMetadataSet bool
	primaryMetadata    map[string]string
	shadowMetadata     map[string]string
}

// NewConfiguredResolver builds a resolver from the loaded application config.
func NewConfiguredResolver(cfg config.Config) (*Resolver, error) {
	return NewResolver(cfg.GRPCRules)
}

// NewResolver compiles gRPC rules once for deterministic request-time lookup.
func NewResolver(rules []config.GRPCRule) (*Resolver, error) {
	compiled := make([]compiledRule, 0, len(rules))
	for i, rule := range rules {
		service := strings.TrimSpace(rule.Service)
		if service == "" {
			return nil, fmt.Errorf("grpc rule %d service is required", i)
		}

		compiled = append(compiled, compiledRule{
			name:               ruleName(i, rule.Name),
			service:            service,
			methods:            compileMethods(rule.Methods),
			shadow:             shadowMode(rule.Shadow),
			compare:            compareDecision(rule.Compare),
			compareMode:        strings.TrimSpace(rule.CompareMode),
			compareMetadata:    cloneStrings(rule.CompareMetadata),
			compareMetadataSet: rule.CompareMetadata != nil,
			primaryMetadata:    cloneStringMap(rule.PrimaryMetadata),
			shadowMetadata:     cloneStringMap(rule.ShadowMetadata),
		})
	}

	return &Resolver{rules: compiled}, nil
}

// Resolve returns the first matching rule decision for a full gRPC method.
func (r *Resolver) Resolve(fullMethod string) (Decision, error) {
	service, method, err := SplitFullMethod(fullMethod)
	if err != nil {
		return Decision{}, err
	}

	if r == nil || len(r.rules) == 0 {
		return Decision{
			Service:         service,
			Method:          method,
			ShadowMode:      ShadowModeInherit,
			CompareDecision: CompareDecisionInherit,
		}, nil
	}

	for _, rule := range r.rules {
		if !rule.matches(service, method) {
			continue
		}

		return rule.decision(service, method), nil
	}

	return Decision{
		Service:           service,
		Method:            method,
		ShadowMode:        ShadowModeNever,
		CompareDecision:   CompareDecisionOff,
		ShadowSkipReason:  SkipReasonGRPCRuleUnmatched,
		CompareSkipReason: SkipReasonGRPCRuleUnmatched,
	}, nil
}

// SplitFullMethod separates a canonical /package.Service/Method gRPC method.
func SplitFullMethod(fullMethod string) (string, string, error) {
	trimmed := strings.TrimSpace(fullMethod)
	if !strings.HasPrefix(trimmed, "/") {
		return "", "", errors.New("gRPC full method must start with '/'")
	}

	service, method, ok := strings.Cut(strings.TrimPrefix(trimmed, "/"), "/")
	if !ok || service == "" || method == "" || strings.Contains(method, "/") {
		return "", "", fmt.Errorf("invalid gRPC full method %q", fullMethod)
	}

	return service, method, nil
}

func (r compiledRule) matches(service, method string) bool {
	if r.service != "*" && r.service != service {
		return false
	}

	if len(r.methods) == 0 {
		return true
	}

	_, ok := r.methods[method]

	return ok
}

func (r compiledRule) decision(service, method string) Decision {
	decision := Decision{
		RuleName:           r.name,
		Service:            service,
		Method:             method,
		ShadowMode:         r.shadow,
		CompareDecision:    r.compare,
		CompareMode:        r.compareMode,
		CompareMetadata:    cloneStrings(r.compareMetadata),
		CompareMetadataSet: r.compareMetadataSet,
		PrimaryMetadata:    cloneStringMap(r.primaryMetadata),
		ShadowMetadata:     cloneStringMap(r.shadowMetadata),
	}

	if decision.ShadowMode == ShadowModeNever {
		decision.ShadowSkipReason = SkipReasonGRPCRule
		decision.CompareSkipReason = SkipReasonGRPCRule
	}

	if decision.CompareDecision == CompareDecisionOff {
		decision.CompareSkipReason = SkipReasonGRPCRule
	}

	return decision
}

func ruleName(index int, name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed != "" {
		return trimmed
	}

	return fmt.Sprintf("rule[%d]", index)
}

func compileMethods(methods []string) map[string]struct{} {
	if len(methods) == 0 {
		return nil
	}

	compiled := make(map[string]struct{}, len(methods))
	for _, method := range methods {
		compiled[strings.TrimSpace(method)] = struct{}{}
	}

	return compiled
}

func shadowMode(raw string) ShadowMode {
	switch ShadowMode(strings.TrimSpace(raw)) {
	case ShadowModeAuto:
		return ShadowModeAuto
	case ShadowModeNever:
		return ShadowModeNever
	case ShadowModeAlways:
		return ShadowModeAlways
	default:
		return ShadowModeInherit
	}
}

func compareDecision(raw string) CompareDecision {
	switch CompareDecision(strings.TrimSpace(raw)) {
	case CompareDecisionOn:
		return CompareDecisionOn
	case CompareDecisionOff:
		return CompareDecisionOff
	default:
		return CompareDecisionInherit
	}
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}

	cloned := make([]string, len(values))
	copy(cloned, values)

	return cloned
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}

	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}

	return cloned
}
