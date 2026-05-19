// Package pathrules resolves HTTP path-specific shadow and comparison policy.
package pathrules

import (
	"fmt"
	"regexp"
	"strings"

	"doppelgaenger/internal/config"
)

// ShadowMode describes how a matched path rule affects shadowing.
type ShadowMode string

const (
	// ShadowModeInherit preserves the global shadow decision.
	ShadowModeInherit ShadowMode = "inherit"
	// ShadowModeAuto allows normal sampling and force-header behavior.
	ShadowModeAuto ShadowMode = "auto"
	// ShadowModeNever disables shadowing for a matched path rule.
	ShadowModeNever ShadowMode = "never"
	// ShadowModeAlways starts shadowing without sampling, subject to runtime limits.
	ShadowModeAlways ShadowMode = "always"
)

// CompareDecision describes how a matched path rule affects comparison.
type CompareDecision string

const (
	// CompareDecisionInherit preserves the global comparison decision.
	CompareDecisionInherit CompareDecision = "inherit"
	// CompareDecisionOn enables comparison when shadowing runs.
	CompareDecisionOn CompareDecision = "on"
	// CompareDecisionOff disables comparison for a matched path rule.
	CompareDecisionOff CompareDecision = "off"
)

const (
	// SkipReasonPathRule indicates that a matched path rule disabled work.
	SkipReasonPathRule = "path_rule"
	// SkipReasonPathUnmatched indicates that configured path rules had no match.
	SkipReasonPathUnmatched = "path_unmatched"
)

// Decision is the deterministic policy result for one HTTP method and path.
type Decision struct {
	RuleName              string
	ShadowMode            ShadowMode
	CompareDecision       CompareDecision
	CompareMode           string
	CompareHeaders        []string
	CompareHeadersSet     bool
	PrimaryRequestHeaders map[string]string
	ShadowRequestHeaders  map[string]string
	ShadowSkipReason      string
	CompareSkipReason     string
}

// Resolver resolves the first configured path rule matching an HTTP method and inbound path.
type Resolver struct {
	rules []compiledRule
}

type compiledRule struct {
	name                  string
	methods               map[string]struct{}
	pattern               *regexp.Regexp
	shadow                ShadowMode
	compare               CompareDecision
	compareMode           string
	compareHeaders        []string
	compareHeadersSet     bool
	primaryRequestHeaders map[string]string
	shadowRequestHeaders  map[string]string
}

// NewResolver compiles path rule regular expressions once for deterministic request-time lookup.
func NewResolver(rules []config.PathRule) (*Resolver, error) {
	compiled := make([]compiledRule, 0, len(rules))
	for i, rule := range rules {
		pattern, err := regexp.Compile(rule.Match)
		if err != nil {
			return nil, fmt.Errorf("path rule %d regex: %w", i, err)
		}

		compiled = append(compiled, compiledRule{
			name:                  pathRuleName(i, rule.Name),
			methods:               compileMethods(rule.Methods),
			pattern:               pattern,
			shadow:                shadowMode(rule.Shadow),
			compare:               compareDecision(rule.Compare),
			compareMode:           strings.TrimSpace(rule.CompareMode),
			compareHeaders:        cloneStrings(rule.CompareHeaders),
			compareHeadersSet:     rule.CompareHeaders != nil,
			primaryRequestHeaders: cloneStringMap(rule.PrimaryRequestHeaders),
			shadowRequestHeaders:  cloneStringMap(rule.ShadowRequestHeaders),
		})
	}

	return &Resolver{rules: compiled}, nil
}

// Resolve returns the first matching rule decision. Method filters and paths are evaluated in order.
func (r *Resolver) Resolve(method, path string) Decision {
	if r == nil || len(r.rules) == 0 {
		return Decision{
			ShadowMode:      ShadowModeInherit,
			CompareDecision: CompareDecisionInherit,
		}
	}

	normalizedMethod := strings.ToUpper(strings.TrimSpace(method))
	for _, rule := range r.rules {
		if !rule.matchesMethod(normalizedMethod) || !rule.pattern.MatchString(path) {
			continue
		}

		return rule.decision()
	}

	return Decision{
		ShadowMode:        ShadowModeNever,
		CompareDecision:   CompareDecisionOff,
		ShadowSkipReason:  SkipReasonPathUnmatched,
		CompareSkipReason: SkipReasonPathUnmatched,
	}
}

func (r compiledRule) matchesMethod(method string) bool {
	if len(r.methods) == 0 {
		return true
	}

	_, ok := r.methods[method]

	return ok
}

func (r compiledRule) decision() Decision {
	decision := Decision{
		RuleName:              r.name,
		ShadowMode:            r.shadow,
		CompareDecision:       r.compare,
		CompareMode:           r.compareMode,
		CompareHeaders:        cloneStrings(r.compareHeaders),
		CompareHeadersSet:     r.compareHeadersSet,
		PrimaryRequestHeaders: cloneStringMap(r.primaryRequestHeaders),
		ShadowRequestHeaders:  cloneStringMap(r.shadowRequestHeaders),
	}

	if decision.ShadowMode == ShadowModeNever {
		decision.ShadowSkipReason = SkipReasonPathRule
		decision.CompareSkipReason = SkipReasonPathRule
	}

	if decision.CompareDecision == CompareDecisionOff {
		decision.CompareSkipReason = SkipReasonPathRule
	}

	return decision
}

func pathRuleName(index int, name string) string {
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
		compiled[strings.ToUpper(strings.TrimSpace(method))] = struct{}{}
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
