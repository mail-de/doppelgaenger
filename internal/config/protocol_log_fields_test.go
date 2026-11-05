package config

import (
	"doppelgaenger/internal/logging"
	"testing"
)

const testProtocolMethod = "/a/b"
const testProtocolAlias = "user"

func TestLoadProtocolLogFields(t *testing.T) {
	cfg := loadTestConfig(t, []byte(`log_fields:
  grpc_messages:
    - method: /example.Auth/Check
      request:
        - {name: Client.IP, path: [1, 2], type: string}
      response:
        - {name: Session-ID, path: [3], type: string}
  milter_macros:
    - {name: Queue.ID, macro: i}
    - {name: Auth-User, macro: "{auth_authen}"}
`))

	rule := cfg.LogFields.ProtobufRule("/example.Auth/Check")
	if len(rule.Request) != 1 || rule.Request[0].Name != "Client.IP" || len(rule.Request[0].Path) != 2 || len(cfg.LogFields.MilterMacros) != 2 {
		t.Fatalf("lost selectors: %#v", cfg.LogFields)
	}

	if cfg.LogFields.ProtobufRule("/other.Auth/Check").Method != "" {
		t.Fatal("method matching must be exact")
	}
}

func TestRejectInvalidProtocolLogFields(t *testing.T) {
	for _, cfg := range []LogFieldsConfig{
		{GRPCMessages: []ProtobufLogRule{{Method: "*"}}},
		{GRPCMessages: []ProtobufLogRule{{Method: testProtocolMethod}, {Method: testProtocolMethod}}},
		{GRPCMessages: []ProtobufLogRule{{Method: testProtocolMethod, Request: []logging.ProtobufField{{Name: testProtocolAlias, Path: []int{0}, Type: "string"}}}}},
		{GRPCMessages: []ProtobufLogRule{{Method: testProtocolMethod, Response: []logging.ProtobufField{{Name: testProtocolAlias, Path: []int{1}, Type: "bytes"}}}}},
		{MilterMacros: []MilterMacro{{Name: "x-id", Macro: "i"}, {Name: "x_id", Macro: "j"}}},
		{MilterMacros: []MilterMacro{{Name: testJSONKeyID, Macro: "{broken"}}},
		{MilterMacros: []MilterMacro{{Name: testJSONKeyID, Macro: "bad macro"}}},
	} {
		if validateLogFields(cfg) == nil {
			t.Fatalf("accepted invalid selectors %#v", cfg)
		}
	}
}
