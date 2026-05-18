package proxy

import (
	"doppelgaenger/internal/config"
	"doppelgaenger/internal/mapping"
	"doppelgaenger/internal/pathrules"
)

// NewPathMapper builds a path mapper from the proxy configuration.
func NewPathMapper(cfg config.Config) (mapping.PathMapper, error) {
	return mapping.NewPathMapper(cfg.PathMapping)
}

// NewPathRuleResolver builds the HTTP path-rule resolver from configuration.
func NewPathRuleResolver(cfg config.Config) (*pathrules.Resolver, error) {
	return pathrules.NewResolver(cfg.PathRules)
}
