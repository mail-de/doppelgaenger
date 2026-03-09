package mapping

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Config describes the path mapping configuration.
type Config struct {
	Mode  string `mapstructure:"mode"`
	Rules []Rule `mapstructure:"rules"`
}

// Rule describes a single rewrite rule for primary and shadow paths.
type Rule struct {
	Match   string `mapstructure:"match"`
	Primary string `mapstructure:"primary"`
	Shadow  string `mapstructure:"shadow"`
}

// PathMapper maps an incoming path to primary and shadow paths.
type PathMapper interface {
	Map(path string) (string, string, error)
}

// DirectMapper returns the incoming path for both backends.
type DirectMapper struct{}

// Map returns the same path for primary and shadow requests.
func (DirectMapper) Map(path string) (string, string, error) {
	return path, path, nil
}

// RegexMapper applies regex rewrite rules to incoming paths.
type RegexMapper struct {
	rules []compiledRule
}

// compiledRule describes a single compiled rewrite rule.
type compiledRule struct {
	pattern *regexp.Regexp
	primary string
	shadow  string
}

// NewPathMapper builds a path mapper from configuration.
func NewPathMapper(cfg Config) (PathMapper, error) {
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	if mode == "" {
		mode = "direct"
	}

	switch mode {
	case "direct":
		return DirectMapper{}, nil
	case "rewrite":
		return newRegexMapper(cfg)
	default:
		return nil, fmt.Errorf("unsupported path mapping mode: %s", cfg.Mode)
	}
}

func newRegexMapper(cfg Config) (PathMapper, error) {
	compiled := make([]compiledRule, 0, len(cfg.Rules))
	for _, rule := range cfg.Rules {
		if strings.TrimSpace(rule.Match) == "" {
			return nil, errors.New("path mapping rule match is required")
		}
		pattern, err := regexp.Compile(rule.Match)
		if err != nil {
			return nil, fmt.Errorf("invalid path mapping rule regex: %w", err)
		}
		compiled = append(compiled, compiledRule{
			pattern: pattern,
			primary: rule.Primary,
			shadow:  rule.Shadow,
		})
	}
	return RegexMapper{rules: compiled}, nil
}

// Map applies the first matching rewrite rule, falling back to the original path.
func (m RegexMapper) Map(path string) (string, string, error) {
	primary := path
	shadow := path
	for _, rule := range m.rules {
		if !rule.pattern.MatchString(path) {
			continue
		}
		if rule.primary != "" {
			primary = rule.pattern.ReplaceAllString(path, rule.primary)
		}
		if rule.shadow != "" {
			shadow = rule.pattern.ReplaceAllString(path, rule.shadow)
		}
		break
	}
	return primary, shadow, nil
}
