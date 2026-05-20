package grpcproxy

import (
	"bytes"
	"strings"
	"testing"
)

func TestRawCodecRoundTripByteIdentical(t *testing.T) {
	codec := rawCodec{}
	original := rawMessage{0x00, 0x01, 0xff, 0x10, 0x42}

	marshaled, err := codec.Marshal(original)
	if err != nil {
		t.Fatalf("expected marshal to succeed, got %v", err)
	}

	var decoded rawMessage
	if err := codec.Unmarshal(marshaled, &decoded); err != nil {
		t.Fatalf("expected unmarshal to succeed, got %v", err)
	}

	if !bytes.Equal(original, decoded) {
		t.Fatalf("expected byte-identical roundtrip, got %x want %x", decoded, original)
	}
}

func TestRawCodecRejectsUnsupportedTypes(t *testing.T) {
	codec := rawCodec{}

	if _, err := codec.Marshal("not raw"); err == nil || !strings.Contains(err.Error(), "unsupported raw message type") {
		t.Fatalf("expected clear marshal error, got %v", err)
	}

	var target string
	if err := codec.Unmarshal([]byte("data"), &target); err == nil || !strings.Contains(err.Error(), "unsupported raw message target") {
		t.Fatalf("expected clear unmarshal error, got %v", err)
	}
}
