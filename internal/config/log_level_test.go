package config

import "testing"

const testLogLevelInfo = "info"

func TestLoadLogLevel(t *testing.T) {
	for _, level := range []string{"debug", testLogLevelInfo, "notice", "warn", "error", "none"} {
		t.Run(level, func(t *testing.T) {
			cfg := loadTestConfig(t, []byte("log_level: "+level+"\n"))
			if cfg.LogLevel != level {
				t.Fatalf("got %q", cfg.LogLevel)
			}
		})
	}

	if cfg := loadTestConfig(t, []byte("{}")); cfg.LogLevel != testLogLevelInfo {
		t.Fatalf("default = %q", cfg.LogLevel)
	}
}

func TestValidateInvalidLogLevel(t *testing.T) {
	cfg := Config{LogLevel: "verbose"}
	if err := validate(&cfg); err == nil {
		t.Fatal("invalid log level accepted")
	}
}
