package config

import (
	"fmt"
	"strings"

	"doppelgaenger/internal/logging"
)

// ProtobufLogRule limits scalar capture to one exact full RPC method.
type ProtobufLogRule struct {
	Method   string                  `mapstructure:"method"`
	Request  []logging.ProtobufField `mapstructure:"request"`
	Response []logging.ProtobufField `mapstructure:"response"`
}

// MilterMacro names one opt-in macro and its normalized log alias.
type MilterMacro struct {
	Name  string `mapstructure:"name"`
	Macro string `mapstructure:"macro"`
}

// ProtobufRule returns the allowlist for an exact full method, or no capture.
func (cfg LogFieldsConfig) ProtobufRule(method string) ProtobufLogRule {
	for _, rule := range cfg.GRPCMessages {
		if rule.Method == method {
			return rule
		}
	}

	return ProtobufLogRule{}
}

func validateProtocolLogFields(cfg LogFieldsConfig) error {
	if len(cfg.GRPCMessages) > 128 || len(cfg.MilterMacros) > 32 {
		return fmt.Errorf("too many protocol log field selections")
	}

	seen := map[string]bool{}

	for _, rule := range cfg.GRPCMessages {
		parts := strings.Split(rule.Method, "/")
		if len(parts) != 3 || parts[0] != "" || parts[1] == "" || parts[2] == "" || strings.ContainsAny(rule.Method, "* \t\n") || seen[rule.Method] {
			return fmt.Errorf("invalid or duplicate full gRPC method %q", rule.Method)
		}

		seen[rule.Method] = true

		for _, fields := range [][]logging.ProtobufField{rule.Request, rule.Response} {
			if err := logging.ValidateProtobufFields(fields); err != nil {
				return fmt.Errorf("log_fields.grpc_messages %s: %w", rule.Method, err)
			}
		}
	}

	return validateMilterMacros(cfg.MilterMacros)
}

func validateMilterMacros(fields []MilterMacro) error {
	seen := map[string]bool{}

	for _, f := range fields {
		key := logging.FieldKey(f.Name)
		if !logging.ValidFieldAlias(f.Name) || seen[key] {
			return fmt.Errorf("invalid or duplicate Milter macro alias %q", f.Name)
		}

		seen[key] = true

		name := strings.TrimSuffix(strings.TrimPrefix(f.Macro, "{"), "}")
		if len(name) == 0 || len(name) > 64 || !validMacroName(name) {
			return fmt.Errorf("invalid Milter macro %q", f.Macro)
		}

		if strings.HasPrefix(f.Macro, "{") != strings.HasSuffix(f.Macro, "}") {
			return fmt.Errorf("unbalanced Milter macro %q", f.Macro)
		}
	}

	return nil
}

func validMacroName(name string) bool {
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}

	return true
}
