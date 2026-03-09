package proxy

import (
	"doppelgaenger/internal/config"
	"doppelgaenger/internal/mapping"
)

// NewPathMapper builds a path mapper from the proxy configuration.
func NewPathMapper(cfg config.Config) (mapping.PathMapper, error) {
	return mapping.NewPathMapper(cfg.PathMapping)
}
