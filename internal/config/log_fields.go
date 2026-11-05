package config

import (
	"fmt"
	"strings"

	"golang.org/x/net/http/httpguts"

	"doppelgaenger/internal/logging"
)

// LogFieldsConfig defines opt-in field allowlists, independently of comparison.
type LogFieldsConfig struct {
	RequestJSON      map[string]string `mapstructure:"-"`
	ResponseJSON     map[string]string `mapstructure:"-"`
	RequestHeaders   []string          `mapstructure:"request_headers"`
	ResponseHeaders  []string          `mapstructure:"response_headers"`
	RequestMetadata  []string          `mapstructure:"request_metadata"`
	ResponseMetadata []string          `mapstructure:"response_metadata"`
}

func validateLogFields(cfg LogFieldsConfig) error {
	for _, list := range []struct {
		name     string
		fields   []string
		metadata bool
	}{
		{"request_headers", cfg.RequestHeaders, false},
		{"response_headers", cfg.ResponseHeaders, false},
		{"request_metadata", cfg.RequestMetadata, true},
		{"response_metadata", cfg.ResponseMetadata, true},
	} {
		seen := make(map[string]bool)

		for _, field := range list.fields {
			if !httpguts.ValidHeaderFieldName(field) || (list.metadata && !validLogMetadataKey(field)) {
				return fmt.Errorf("log_fields.%s: invalid field %q", list.name, field)
			}

			key := logging.FieldKey(field)
			if seen[key] {
				return fmt.Errorf("log_fields.%s: duplicate normalized key %q", list.name, key)
			}

			seen[key] = true
		}
	}

	return validateJSONLogFields(cfg)
}

func validLogMetadataKey(key string) bool {
	if strings.HasSuffix(key, "-bin") {
		return false
	}

	for _, r := range key {
		valid := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.'

		if !valid {
			return false
		}
	}

	return true
}

func validateJSONLogFields(cfg LogFieldsConfig) error {
	for _, list := range []struct {
		name   string
		fields map[string]string
	}{
		{logRequestJSON, cfg.RequestJSON}, {logResponseJSON, cfg.ResponseJSON},
	} {
		seen := make(map[string]bool)

		for name, pointer := range list.fields {
			key := logging.FieldKey(name)
			if !httpguts.ValidHeaderFieldName(name) || seen[key] {
				return fmt.Errorf("log_fields.%s: invalid or duplicate normalized key %q", list.name, name)
			}

			if err := logging.ValidateJSONSelector(pointer); err != nil {
				return fmt.Errorf("log_fields.%s.%s: %w", list.name, name, err)
			}

			seen[key] = true
		}
	}

	return nil
}
