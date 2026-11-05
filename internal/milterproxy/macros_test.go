package milterproxy

import (
	"doppelgaenger/internal/config"
	"doppelgaenger/internal/protocol"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

const testMacroQueueID = "queue_id"
const testMacroFirst = "first"

func TestMacroLifecycle(t *testing.T) {
	state := newMacroState([]config.MilterMacro{{Name: testMacroQueueID, Macro: "i"}}, 42)
	send := func(c byte, p string) string {
		attrs := state.observe(protocol.MilterFrame{Command: c, Payload: []byte(p)})
		for _, a := range attrs {
			if a.Key == "milter_macros" {
				for _, v := range a.Value.Group() {
					if v.Key == testMacroQueueID {
						return v.Value.String()
					}
				}
			}
		}

		return ""
	}
	send('D', "Mi\x00first\x00")

	if got := send('M', ""); got != testMacroFirst {
		t.Fatal(got)
	}

	if got := send('E', ""); got != testMacroFirst {
		t.Fatal(got)
	}

	if got := send('M', ""); got != "" {
		t.Fatal("stale message", got)
	}

	send('D', "Ri\x00recipient\x00")

	if got := send('R', ""); got != "recipient" {
		t.Fatal(got)
	}

	if got := send('R', ""); got != "" {
		t.Fatal("stale recipient", got)
	}

	send('D', "Mi\x00incomplete")

	if got := send('M', ""); got != "" {
		t.Fatal("malformed accepted", got)
	}
}

func TestMacroScopesAndSnapshots(t *testing.T) {
	state := newMacroState([]config.MilterMacro{{Name: testMacroQueueID, Macro: "i"}, {Name: "peer", Macro: "{client_addr}"}}, 7)
	frame := func(c byte, p string) []slog.Attr {
		return state.observe(protocol.MilterFrame{Command: c, Payload: []byte(p)})
	}
	frame('D', "C{client_addr}\x00192.0.2.1\x00")
	frame('C', "")
	frame('D', "Mi\x00message-one\x00")
	snapshot := frame('M', "")
	frame('A', "")
	frame('D', "Mi\x00message-two\x00")
	frame('M', "")

	encoded := fmt.Sprint(snapshot)
	if !strings.Contains(encoded, "message-one") || strings.Contains(encoded, "message-two") {
		t.Fatal("snapshot mutated", encoded)
	}

	frame('D', "M")
	frame('M', "")

	if got := fmt.Sprint(frame('R', "")); strings.Contains(got, "message-two") || !strings.Contains(got, "192.0.2.1") {
		t.Fatal(got)
	}

	frame('K', "")

	if got := fmt.Sprint(frame('C', "")); strings.Contains(got, "192.0.2.1") {
		t.Fatal("stale connection", got)
	}
}
