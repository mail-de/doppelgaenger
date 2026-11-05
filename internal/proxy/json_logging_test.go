package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"doppelgaenger/internal/config"
)

func TestHTTPJSONCorrelation(t *testing.T) {
	for _, shadow := range []bool{false, true} {
		harness := newRuntimeHarness(t, config.Config{ShadowSamplePercent: 100, PathRules: []config.PathRule{{Name: "json-fields", Match: testAuthPathRegex, Compare: "off"}}, LogFields: config.LogFieldsConfig{
			RequestJSON: map[string]string{"auth_user": "/username"}, ResponseJSON: map[string]string{"session_id": "/session/id"},
		}}, nil)
		if !shadow {
			harness.handler.cfg.ShadowSamplePercent = 0
		}

		harness.adapter.primaryResponse.Body = []byte(`{"session":{"id":"primary-json"},"private":"hidden"}`)
		harness.adapter.shadowResponse.Body = []byte(`{"session":{"id":"shadow-json"},"private":"hidden"}`)
		body := `{"username":"alice","password":"request-secret"}`
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, testAuthPath, strings.NewReader(body))
		c.Params = gin.Params{{Key: testPathParamKey, Value: testAuthPath}}
		harness.handler.Handle(c)
		fields := waitForRuntimeLog(t, harness.logs)
		assertJSONLogGroup(t, fields, "request_json", "auth_user=alice")
		assertJSONLogGroup(t, fields, "primary_response_json", "session_id=primary-json")

		if shadow {
			assertJSONLogGroup(t, fields, "shadow_response_json", "session_id=shadow-json")
		}

		if w.Body.String() != string(harness.adapter.primaryResponse.Body) {
			t.Fatal("logging changed the response body")
		}

		event, ok := harness.adapter.primaryEvent()
		if !ok || string(event.Body) != body {
			t.Fatal("logging changed the request body")
		}
	}
}

func assertJSONLogGroup(t *testing.T, fields map[string]any, key, want string) {
	t.Helper()

	value, ok := fields[key].(string)
	if !ok || !strings.Contains(value, want) || strings.Contains(value, "hidden") || strings.Contains(value, "secret") {
		t.Fatalf("unexpected JSON log group %q: %#v", key, fields[key])
	}
}
