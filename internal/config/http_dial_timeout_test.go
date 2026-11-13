package config

import (
	"testing"
	"time"
)

func TestHTTPDialTimeoutOverrides(t *testing.T) {
	for _, tc := range []struct {
		name    string
		yaml    string
		primary time.Duration
		shadow  time.Duration
	}{
		{name: "defaults", primary: 2 * time.Second, shadow: 2 * time.Second},
		{name: "legacy shared", yaml: "upstream_http_dial_timeout: 750ms", primary: 750 * time.Millisecond, shadow: 750 * time.Millisecond},
		{name: "independent", yaml: "upstream_http_dial_timeout: 3s\nprimary_upstream_http_dial_timeout: 2s\nshadow_upstream_http_dial_timeout: 200ms", primary: 2 * time.Second, shadow: 200 * time.Millisecond},
		{name: "primary only", yaml: "upstream_http_dial_timeout: 750ms\nprimary_upstream_http_dial_timeout: 2s", primary: 2 * time.Second, shadow: 750 * time.Millisecond},
		{name: "shadow only", yaml: "upstream_http_dial_timeout: 750ms\nshadow_upstream_http_dial_timeout: 200ms", primary: 750 * time.Millisecond, shadow: 200 * time.Millisecond},
		{name: "nonpositive inherit", yaml: "upstream_http_dial_timeout: 750ms\nprimary_upstream_http_dial_timeout: 0s\nshadow_upstream_http_dial_timeout: -1s", primary: 750 * time.Millisecond, shadow: 750 * time.Millisecond},
		{name: "invalid shared normalizes first", yaml: "upstream_http_dial_timeout: -1s\nprimary_upstream_http_dial_timeout: -1s\nshadow_upstream_http_dial_timeout: 0s", primary: 2 * time.Second, shadow: 2 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := loadTestConfig(t, []byte(tc.yaml+"\n"))
			if cfg.PrimaryUpstreamHTTPDialTimeout != tc.primary || cfg.ShadowUpstreamHTTPDialTimeout != tc.shadow {
				t.Fatalf("dial timeouts primary=%s shadow=%s; want %s / %s", cfg.PrimaryUpstreamHTTPDialTimeout, cfg.ShadowUpstreamHTTPDialTimeout, tc.primary, tc.shadow)
			}
		})
	}
}
