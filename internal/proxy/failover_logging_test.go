package proxy

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"doppelgaenger/internal/protocol"
)

const (
	testPrimaryAttemptsField = "primary_attempts"
	testFailoverFromBackend  = "http://primary-a.example.test"
)

func TestFailoverLogFieldsOnlyAppearAfterExtraAttempts(t *testing.T) {
	tests := []struct {
		name     string
		response protocol.Response
		want     map[string]any
	}{
		{name: "not reported", response: protocol.Response{}, want: map[string]any{}},
		{name: "single attempt", response: protocol.Response{Attempts: 1}, want: map[string]any{}},
		{name: "retry on same backend", response: protocol.Response{Attempts: 3}, want: map[string]any{testPrimaryAttemptsField: float64(3)}},
		{
			name:     "failover",
			response: protocol.Response{Attempts: 2, FailoverFrom: testFailoverFromBackend},
			want:     map[string]any{testPrimaryAttemptsField: float64(2), "primary_failover_from": testFailoverFromBackend},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer

			logger := slog.New(slog.NewJSONHandler(&out, nil))
			logger.With(failoverLogFields("primary", tt.response)).Info("auth_proxy")

			var record map[string]any
			if err := json.Unmarshal(out.Bytes(), &record); err != nil {
				t.Fatalf("decode log record: %v", err)
			}

			for _, key := range []string{"time", "level", "msg"} {
				delete(record, key)
			}

			if len(record) != len(tt.want) {
				t.Fatalf("log fields = %v, want %v", record, tt.want)
			}

			for key, value := range tt.want {
				if record[key] != value {
					t.Fatalf("log field %s = %v, want %v", key, record[key], value)
				}
			}
		})
	}
}
