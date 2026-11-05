package config

import "testing"

func TestLoadLogFields(t *testing.T) {
	cfg := loadTestConfig(t, []byte(`log_fields:
  request_headers: [Auth-User, X-Client-ID]
  response_headers: [X-Session-ID]
  request_metadata: [x-client-id]
  response_metadata: [x-session-id]
`))
	if len(cfg.LogFields.RequestHeaders) != 2 || len(cfg.LogFields.ResponseHeaders) != 1 || len(cfg.LogFields.RequestMetadata) != 1 || len(cfg.LogFields.ResponseMetadata) != 1 {
		t.Fatalf("allowlists lost: %#v", cfg.LogFields)
	}
}

func TestValidateLogFields(t *testing.T) {
	for _, cfg := range []LogFieldsConfig{
		{RequestHeaders: []string{"Bad Header"}},
		{ResponseHeaders: []string{"X-ID", "x_id"}},
		{RequestMetadata: []string{"Uppercase"}},
		{ResponseMetadata: []string{"payload-bin"}},
		{ResponseMetadata: []string{""}},
	} {
		if err := validateLogFields(cfg); err == nil {
			t.Fatalf("invalid allowlist accepted: %#v", cfg)
		}
	}
}
