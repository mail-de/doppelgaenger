package app

import (
	"testing"
	"time"

	"doppelgaenger/internal/backend"
	"doppelgaenger/internal/config"
)

func TestHTTPClientConfigsIndependentDialTimeouts(t *testing.T) {
	cfg := config.Config{
		UpstreamHTTPDialTimeout:           3 * time.Second,
		PrimaryUpstreamHTTPDialTimeout:    2 * time.Second,
		ShadowUpstreamHTTPDialTimeout:     200 * time.Millisecond,
		UpstreamHTTPTLSHandshakeTimeout:   4 * time.Second,
		UpstreamHTTPResponseHeaderTimeout: 5 * time.Second,
		UpstreamHTTPMaxIdleConns:          1024,
		UpstreamHTTPMaxIdleConnsPerHost:   256,
		UpstreamHTTPMaxConnsPerHost:       8,
		UpstreamHTTPProtocol:              backend.HTTPProtocolHTTP1,
	}

	primary, shadow := httpClientConfigs(cfg)
	if primary.DialTimeout != 2*time.Second || shadow.DialTimeout != 200*time.Millisecond {
		t.Fatalf("dial deadlines mixed: primary=%s shadow=%s", primary.DialTimeout, shadow.DialTimeout)
	}

	if primary.TLSHandshakeTimeout != 4*time.Second || primary.ResponseHeaderTimeout != 5*time.Second || primary.MaxIdleConns != 1024 || primary.MaxIdleConnsPerHost != 256 || primary.MaxConnsPerHost != 8 || primary.Protocol != backend.HTTPProtocolHTTP1 {
		t.Fatalf("shared transport settings lost: %+v", primary)
	}

	shadow.DialTimeout = primary.DialTimeout
	if shadow != primary {
		t.Fatalf("non-dial transport settings differ: primary=%+v shadow=%+v", primary, shadow)
	}
}
