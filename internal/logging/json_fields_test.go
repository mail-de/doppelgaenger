package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"reflect"
	"testing"
)

const testJSONClientKey = "client_id"
const testJSONEnabledKey = "enabled"
const testLogUsername = "alice"

const testJSONFirstKey = "first"
const testJSONLargeKey = "large"
const testJSONZeroKey = "zero"
const testJSONEscapedKey = "escaped"
const testJSONUserKey = "auth_user"
const testJSONRequestGroup = "request_json"

func TestSelectedJSONScalars(t *testing.T) {
	fields := map[string]string{
		"Auth-User": "/username", testJSONClientKey: "/client/id", testJSONFirstKey: "/items/0/id",
		testJSONLargeKey: "/number", testJSONEnabledKey: "/enabled", testJSONZeroKey: "/zero", testJSONEscapedKey: "/a~1b/~0key",
		"missing": "/absent", "empty": "/empty", "nil": "/nil", "object": "/client", "array": "/items",
	}
	body := []byte(`{"username":"alice","password":"secret","client":{"id":"client-value"},"items":[{"id":"first-value"}],"number":9007199254740993,"enabled":false,"zero":0,"a/b":{"~key":"escaped-value"},"empty":"","nil":null}`)
	got := captureJSONFields(t, SelectedJSONFields(testJSONRequestGroup, fields, body))

	want := map[string]any{testJSONUserKey: testLogUsername, testJSONClientKey: "client-value", testJSONFirstKey: "first-value", testJSONLargeKey: json.Number("9007199254740993"), testJSONEnabledKey: false, testJSONZeroKey: json.Number("0"), testJSONEscapedKey: "escaped-value"}
	if !reflect.DeepEqual(got[testJSONRequestGroup], want) {
		t.Fatalf("unexpected fields: %#v", got)
	}
}

func TestSelectedJSONInvalidOrMissing(t *testing.T) {
	for _, body := range []string{"", `{"id":`, `{"id":"one"} {"id":"two"}`, `{"id":"one"} trailing`, `null`, `[]`} {
		got := captureJSONFields(t, SelectedJSONFields(testJSONRequestGroup, map[string]string{"id": "/id"}, []byte(body)))
		if _, exists := got[testJSONRequestGroup]; exists {
			t.Fatalf("unexpected fields for %q: %#v", body, got)
		}
	}
}

func TestJSONPointerArrayIndices(t *testing.T) {
	for _, pointer := range []string{"/items/01", "/items/-", "/items/-1", "/items/99", "/items/999999999999999999999999", "items/0", "/items/~2"} {
		got := captureJSONFields(t, SelectedJSONFields(testJSONRequestGroup, map[string]string{"item": pointer}, []byte(`{"items":["value"]}`)))
		if _, exists := got[testJSONRequestGroup]; exists {
			t.Fatalf("invalid pointer resolved: %s", pointer)
		}
	}
}

func captureJSONFields(t *testing.T, attr slog.Attr) map[string]any {
	t.Helper()

	var output bytes.Buffer
	slog.New(NewHandler(&output, true, "info")).Info("fields", attr)
	decoder := json.NewDecoder(&output)
	decoder.UseNumber()

	var record map[string]any
	if err := decoder.Decode(&record); err != nil {
		t.Fatal(err)
	}

	return record
}
