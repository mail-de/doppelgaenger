package fakeserver

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds configuration settings for the fake server.
type Config struct {
	ListenAddr      string
	TLSCertFile     string
	TLSKeyFile      string
	Mode            string
	EchoHeaders     []string
	ResponseHeaders map[string]string
	RandomChance    int
	RandomHeaders   []string
	RandomValues    []string
}

var defaultEchoHeaders = []string{
	"Auth-Status",
	"Auth-Server",
	"Auth-Port",
	"Auth-User",
	"Auth-Error",
	"X-Nauthilus-Session",
}

// Load loads the configuration from environment variables and sets defaults.
func Load() (Config, error) {
	mode := strings.ToLower(getenv("FAKE_MODE", "echo"))
	if mode != "echo" && mode != "random" {
		return Config{}, fmt.Errorf("invalid FAKE_MODE: %s", mode)
	}

	echoHeaders := parseList(getenv("FAKE_ECHO_HEADERS", ""))
	if len(echoHeaders) == 0 {
		echoHeaders = append([]string(nil), defaultEchoHeaders...)
	}

	randomHeaders := parseList(getenv("FAKE_RANDOM_HEADERS", ""))
	if len(randomHeaders) == 0 {
		randomHeaders = append([]string(nil), echoHeaders...)
	}

	randomValues := parseList(getenv("FAKE_RANDOM_VALUES", "random"))
	if len(randomValues) == 0 {
		randomValues = []string{"random"}
	}

	responseHeaders, err := parseHeaderMap(getenv("FAKE_RESPONSE_HEADERS", ""))
	if err != nil {
		return Config{}, err
	}

	chance := getenvInt("FAKE_RANDOM_CHANCE", 10)
	if chance < 0 {
		chance = 0
	}
	if chance > 100 {
		chance = 100
	}

	return Config{
		ListenAddr:      getenv("FAKE_LISTEN", ":9001"),
		TLSCertFile:     getenv("FAKE_TLS_CERT", ""),
		TLSKeyFile:      getenv("FAKE_TLS_KEY", ""),
		Mode:            mode,
		EchoHeaders:     echoHeaders,
		ResponseHeaders: responseHeaders,
		RandomChance:    chance,
		RandomHeaders:   randomHeaders,
		RandomValues:    randomValues,
	}, nil
}

func parseHeaderMap(raw string) (map[string]string, error) {
	items := parseList(raw)
	if len(items) == 0 {
		return map[string]string{}, nil
	}

	result := make(map[string]string, len(items))
	for _, item := range items {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) != 2 {
			return nil, errors.New("FAKE_RESPONSE_HEADERS must be in key=value form")
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key == "" {
			return nil, errors.New("FAKE_RESPONSE_HEADERS contains an empty header name")
		}
		result[key] = value
	}

	return result, nil
}

func parseList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	list := make([]string, 0, len(parts))
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item == "" {
			continue
		}
		list = append(list, item)
	}

	return list
}

// getenv reads an environment variable or returns a default value.
func getenv(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}

	return v
}

// getenvInt reads an environment variable or returns a default value.
func getenvInt(key string, def int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}

	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}

	return parsed
}
