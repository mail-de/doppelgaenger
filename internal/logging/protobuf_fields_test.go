package logging

import (
	"google.golang.org/protobuf/encoding/protowire"
	"testing"
)

const testProtobufAlias = "id"

func wireString(n protowire.Number, value string) []byte {
	return protowire.AppendString(protowire.AppendTag(nil, n, protowire.BytesType), value)
}

func TestProtobufSelection(t *testing.T) {
	fields := []ProtobufField{{Name: "Session-ID", Path: []int{3}, Type: protobufString}}

	payload := append(wireString(2, "secret"), wireString(3, "session")...)
	capture := NewProtobufCapture("response", fields)
	capture.Observe(payload)
	payload[len(payload)-1] = 'X'

	capture.Observe(wireString(3, "later"))

	attrs := capture.Attributes()
	if got := attrs[0].Value.Group()[0]; got.Key != "session_id" || got.Value.String() != "session" {
		t.Fatalf("unexpected selection: %v", attrs)
	}
}

func TestProtobufRejectsAmbiguityAndMalformedSuffix(t *testing.T) {
	fields := []ProtobufField{{Name: testProtobufAlias, Path: []int{3}, Type: protobufString}}
	for _, payload := range [][]byte{append(wireString(3, "a"), wireString(3, "b")...), append(wireString(3, "a"), 0xff)} {
		capture := NewProtobufCapture("response", fields)
		capture.Observe(payload)

		if len(capture.Attributes()[0].Value.Group()) != 0 {
			t.Fatal("published ambiguous or malformed data")
		}
	}
}

func TestProtobufNestedAndAbsent(t *testing.T) {
	fields := []ProtobufField{{Name: testProtobufAlias, Path: []int{1, 2}, Type: protobufString}}
	capture := NewProtobufCapture("request", fields)
	capture.Observe(wireString(1, string(wireString(2, "nested"))))

	if got := capture.Attributes()[0].Value.Group()[0].Value.String(); got != "nested" {
		t.Fatal(got)
	}

	absent := NewProtobufCapture("request", fields)
	if got := absent.Attributes()[1].Value.String(); got != "not_seen" {
		t.Fatal(got)
	}
}

func TestProtobufScalarTypesAndLimits(t *testing.T) {
	for _, tc := range []struct {
		kind string
		wire uint64
		want string
	}{
		{protobufBool, 0, "false"}, {"int64", ^uint64(0), "-1"}, {"uint64", 123, "123"}, {"sint64", 3, "-2"},
	} {
		capture := NewProtobufCapture("response", []ProtobufField{{Name: testProtobufAlias, Path: []int{1}, Type: tc.kind}})
		capture.Observe(protowire.AppendVarint(protowire.AppendTag(nil, 1, protowire.VarintType), tc.wire))

		if got := capture.Attributes()[0].Value.Group()[0].Value.String(); got != tc.want {
			t.Errorf("%s: %s", tc.kind, got)
		}
	}

	for _, payload := range [][]byte{wireString(1, string(make([]byte, maxSelectedValue+1))), make([]byte, maxProtobufMessage+1), wireString(1, "\xff")} {
		capture := NewProtobufCapture("response", []ProtobufField{{Name: testProtobufAlias, Path: []int{1}, Type: protobufString}})
		capture.Observe(payload)

		if len(capture.Attributes()[0].Value.Group()) != 0 {
			t.Fatal("published oversized/invalid value")
		}
	}
}

func TestProtobufConcurrentSnapshot(t *testing.T) {
	capture := NewProtobufCapture("request", []ProtobufField{{Name: testProtobufAlias, Path: []int{1}, Type: protobufString}})

	done := make(chan struct{})
	go func() {
		defer close(done)

		for range 100 {
			capture.Attributes()
		}
	}()

	capture.Observe(wireString(1, "value"))
	<-done

	if capture.Attributes()[1].Value.String() != "captured" {
		t.Fatal("capture missing")
	}
}
