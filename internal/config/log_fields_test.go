package config

import (
	"os"
	"path/filepath"
	"testing"
)

const testJSONAliasID = "X-ID"
const testJSONKeyID = "id"
const testJSONPointerID = "/id"

const testJSONNormalizedAliasID = "x_id"
const testJSONCaseCollision = `log_fields: {request_json: {ID: $.a, id: $.b}}`
const testJSONNumericAlias = `log_fields: {request_json: {1: $.a}}`

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
		{ResponseHeaders: []string{testJSONAliasID, testJSONNormalizedAliasID}},
		{RequestMetadata: []string{"Uppercase"}},
		{ResponseMetadata: []string{"payload-bin"}},
		{ResponseMetadata: []string{""}},
	} {
		if err := validateLogFields(cfg); err == nil {
			t.Fatalf("invalid allowlist accepted: %#v", cfg)
		}
	}
}

func TestLoadJSONLogFields(t *testing.T) {
	cfg := loadTestConfig(t, []byte(`log_fields:
  request_json:
    auth_user: /username
    client_id: /client/id
  response_json:
    session_id: /session/id
`))
	if cfg.LogFields.RequestJSON["auth_user"] != "/username" || cfg.LogFields.ResponseJSON["session_id"] != "/session/id" {
		t.Fatalf("JSON fields lost: %#v", cfg.LogFields)
	}
}

func TestValidateJSONLogFields(t *testing.T) {
	for _, fields := range []map[string]string{
		{testJSONKeyID: ""}, {testJSONKeyID: testJSONKeyID}, {testJSONKeyID: "/bad~2"}, {testJSONKeyID: "/bad~"},
		{"": testJSONPointerID}, {"Bad Key": testJSONPointerID}, {testJSONAliasID: "/one", testJSONNormalizedAliasID: "/two"},
	} {
		if err := validateLogFields(LogFieldsConfig{RequestJSON: fields}); err == nil {
			t.Fatalf("invalid JSON field configuration accepted: %#v", fields)
		}

		if err := validateLogFields(LogFieldsConfig{ResponseJSON: fields}); err == nil {
			t.Fatalf("invalid response JSON field configuration accepted: %#v", fields)
		}
	}
}

func TestLoadJSONAliasSpelling(t *testing.T) {
	cfg := loadTestConfig(t, []byte(`log_fields:
  request_json: &fields
    Client.ID: $.client.id
    User-Name: $.username
  response_json:
    <<: *fields
    session_id: $.session.id
`))
	if cfg.LogFields.RequestJSON["Client.ID"] != "$.client.id" || cfg.LogFields.ResponseJSON["User-Name"] != "$.username" {
		t.Fatalf("alias spelling or YAML merge lost: %#v", cfg.LogFields)
	}
}

func TestDecodeJSONLogFieldsRejectsAmbiguousAliases(t *testing.T) {
	for _, body := range []string{
		testJSONCaseCollision,
		testJSONNumericAlias,
		`log_fields: {request_json: {id: 1}}`,
		`log_fields: {request_json: {id: null}}`,
	} {
		var cfg LogFieldsConfig

		err := decodeJSONLogFields([]byte(body), &cfg)
		if err == nil {
			err = validateLogFields(cfg)
		}

		if err == nil {
			t.Fatalf("ambiguous or coerced aliases accepted: %s", body)
		}
	}
}

func TestLoadRejectsAmbiguousJSONAliases(t *testing.T) {
	for _, raw := range []string{
		testJSONCaseCollision,
		`log_fields: {request_json: {id: $.a}, REQUEST_JSON: {other: $.b}}`,
		`log_fields: {request_json: {id: $.a}}
LOG_FIELDS: {response_json: {other: $.b}}`,
		testJSONNumericAlias,
		`log_fields: {request_json: {id: false}}`,
	} {
		file := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(file, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}

		t.Setenv("CONFIG_FILE", file)

		if _, err := Load(); err == nil {
			t.Fatalf("ambiguous aliases loaded: %s", raw)
		}
	}
}
