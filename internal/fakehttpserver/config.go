package fakehttpserver

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"
)

// Config holds configuration settings for the fake server.
type Config struct {
	// ListenAddr is the address the fake server listens on.
	ListenAddr string `mapstructure:"listen_addr"`
	// TLSCertFile path to the TLS certificate file.
	TLSCertFile string `mapstructure:"tls_cert_file"`
	// TLSKeyFile path to the TLS key file.
	TLSKeyFile string `mapstructure:"tls_key_file"`
	// Mode server operation mode ("echo" or "random").
	Mode string `mapstructure:"mode"`
	// EchoHeaders list of request headers to echo back in the response.
	EchoHeaders []string `mapstructure:"echo_headers"`
	// ResponseHeaders static headers to include in every response.
	ResponseHeaders map[string]string `mapstructure:"response_headers"`
	// RandomChance probability (0-100) of randomizing a header value in "random" mode.
	RandomChance int `mapstructure:"random_chance"`
	// RandomHeaders headers eligible for randomization (defaults to echo_headers).
	RandomHeaders []string `mapstructure:"random_headers"`
	// RandomValues pool of values used for randomization.
	RandomValues []string `mapstructure:"random_values"`
	// LogJSON whether the logger should output JSON.
	LogJSON bool `mapstructure:"log_json"`
}

// UseJSONLogger reports whether the JSON logger is enabled.
func (c Config) UseJSONLogger() bool {
	return c.LogJSON
}

var defaultEchoHeaders = []string{
	"Auth-Status",
	"Auth-Server",
	"Auth-Port",
	"Auth-User",
	"Auth-Error",
	"X-Nauthilus-Session",
}

// Load loads the configuration from a YAML file using Viper.
func Load() (Config, error) {
	v := viper.New()
	configFile := strings.TrimSpace(os.Getenv("CONFIG_FILE"))
	if configFile != "" {
		v.SetConfigFile(configFile)
	} else {
		v.SetConfigName("fakehttpserver")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
		v.AddConfigPath("/etc/doppelgaenger")
	}

	setDefaults(v)
	if err := v.ReadInConfig(); err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if err := validate(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("listen_addr", ":9001")
	v.SetDefault("tls_cert_file", "")
	v.SetDefault("tls_key_file", "")
	v.SetDefault("mode", "echo")
	v.SetDefault("echo_headers", defaultEchoHeaders)
	v.SetDefault("response_headers", map[string]string{})
	v.SetDefault("random_chance", 10)
	v.SetDefault("random_headers", []string{})
	v.SetDefault("random_values", []string{"random"})
	v.SetDefault("log_json", true)
}

func validate(cfg *Config) error {
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	if mode != "echo" && mode != "random" {
		return fmt.Errorf("invalid mode: %s", cfg.Mode)
	}
	cfg.Mode = mode

	if len(cfg.EchoHeaders) == 0 {
		cfg.EchoHeaders = append([]string(nil), defaultEchoHeaders...)
	}
	if len(cfg.RandomHeaders) == 0 {
		cfg.RandomHeaders = append([]string(nil), cfg.EchoHeaders...)
	}
	if len(cfg.RandomValues) == 0 {
		cfg.RandomValues = []string{"random"}
	}

	if cfg.RandomChance < 0 {
		cfg.RandomChance = 0
	}
	if cfg.RandomChance > 100 {
		cfg.RandomChance = 100
	}

	for key := range cfg.ResponseHeaders {
		if strings.TrimSpace(key) == "" {
			return errors.New("response_headers contains an empty header name")
		}
	}

	return nil
}
