package logging

import "testing"

const testJSONSelectedKey = "selected"

func TestJSONPathSelection(t *testing.T) {
	body := []byte(`{"username":"value","client":{"id":"value"},"items":[{"id":"value"}],"a.b":"value","a'b":"value","a\"b":"value","a\\b":"value","é":"value"}`)
	for _, path := range []string{"$.username", "$.client.id", "$.items[0].id", "$['a.b']", `$["a.b"]`, `$['a\'b']`, `$["a\"b"]`, `$['a\\b']`, `$['é']`, `$["\u00e9"]`, "$[ 'username' ]"} {
		got := captureJSONFields(t, SelectedJSONFields(testJSONRequestGroup, map[string]string{testJSONSelectedKey: path}, body))

		group, ok := got[testJSONRequestGroup].(map[string]any)
		if !ok || group[testJSONSelectedKey] != "value" {
			t.Fatalf("selector %q failed: %#v", path, got)
		}
	}
}

func TestJSONPathInvalid(t *testing.T) {
	for _, path := range []string{"$", "$.items[*]", "$..id", "$.items[?(@.id)]", "$.items[0,1]", "$.items[0:1]", "$.items[-1]", "$[01]", "$[+1]", "$['id'", `$['id'junk]`, `$['bad\x20']`, "$.", "$[", "$['", "$['broken\\", "$.0", "$ id", "$.id ", "$[]"} {
		if err := ValidateJSONSelector(path); err == nil {
			t.Fatalf("invalid JSONPath accepted: %q", path)
		}
	}
}

func TestJSONPathMemberAndIndexTypes(t *testing.T) {
	for _, test := range []struct{ path, body string }{
		{`$.items['0']`, `{"items":["value"]}`}, {`$.items[0]`, `{"items":{"0":"value"}}`},
	} {
		got := captureJSONFields(t, SelectedJSONFields(testJSONRequestGroup, map[string]string{testJSONSelectedKey: test.path}, []byte(test.body)))
		if _, ok := got[testJSONRequestGroup]; ok {
			t.Fatalf("selector type mismatch logged a value: %#v", got)
		}
	}
}
