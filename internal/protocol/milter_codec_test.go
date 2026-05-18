package protocol

import (
	"bytes"
	"testing"
)

func TestMilterFrameRoundTrip(t *testing.T) {
	payload := []byte("payload")
	frame := encodeMilterFrame('c', payload)

	parsed, err := readMilterFrame(bytes.NewReader(frame))
	if err != nil {
		t.Fatalf("expected frame to parse, got error: %v", err)
	}

	if parsed.Command != 'c' {
		t.Fatalf("expected command 'c', got %q", parsed.Command)
	}

	if !bytes.Equal(parsed.Payload, payload) {
		t.Fatalf("expected payload %q, got %q", payload, parsed.Payload)
	}

	if !bytes.Equal(parsed.Raw, frame) {
		t.Fatalf("expected raw frame to match")
	}
}
