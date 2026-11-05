package milterproxy

import (
	"log/slog"
	"strings"
	"unicode/utf8"

	"doppelgaenger/internal/config"
	"doppelgaenger/internal/logging"
	"doppelgaenger/internal/protocol"
)

type macroContextKey struct{}

// macroState is confined to the connection reader. Snapshots own their values.
type macroState struct {
	fields     []config.MilterMacro
	pending    map[byte]map[string]string
	active     map[byte]map[string]string
	connection uint64
	message    uint64
}

func newMacroState(fields []config.MilterMacro, id uint64) *macroState {
	return &macroState{fields: fields, connection: id, pending: map[byte]map[string]string{}, active: map[byte]map[string]string{}}
}

func (s *macroState) observe(frame protocol.MilterFrame) []slog.Attr {
	if frame.Command == 'D' {
		s.define(frame.Payload)
		return s.snapshot()
	}

	switch frame.Command {
	case 'C':
		pending := s.pending['C']
		s.active = map[byte]map[string]string{}
		s.pending = map[byte]map[string]string{'C': pending}
	case 'H':
		delete(s.active, 'H')
		s.clearMessage()
	case 'M':
		pending := s.pending['M']
		s.clearMessage()
		s.pending['M'] = pending
		s.message++
	}

	if strings.ContainsRune("CHMRTNE", rune(frame.Command)) {
		s.active[frame.Command] = s.pending[frame.Command]
		delete(s.pending, frame.Command)
	}

	attrs := s.snapshot()

	switch frame.Command {
	case 'E', 'A':
		s.clearMessage()
	case 'K', 'Q':
		s.active = map[byte]map[string]string{}
		s.pending = map[byte]map[string]string{}
	}

	return attrs
}

func (s *macroState) clearActiveMessage() {
	for _, stage := range []byte("MRTNE") {
		delete(s.active, stage)
	}
}

func (s *macroState) clearMessage() {
	s.clearActiveMessage()

	for _, stage := range []byte("MRTNE") {
		delete(s.pending, stage)
	}
}

func (s *macroState) define(payload []byte) {
	if len(payload) == 0 {
		s.pending = map[byte]map[string]string{}
		s.active = map[byte]map[string]string{}

		return
	}

	stage := payload[0]
	delete(s.pending, stage)
	delete(s.active, stage)

	if !strings.ContainsRune("CHMRTNE", rune(stage)) {
		return
	}

	if len(payload) > 65536 {
		return
	}

	pairs := strings.Split(string(payload[1:]), "\x00")
	if pairs[len(pairs)-1] != "" || len(pairs)%2 != 1 {
		return
	}

	values := map[string]string{}
	total := 0

	for i := 0; i+1 < len(pairs); i += 2 {
		name := macroName(pairs[i])
		value := pairs[i+1]

		if name == "" {
			return
		}

		if !s.selected(name) || len(value) > 4096 || !utf8.ValidString(value) {
			continue
		}

		total += len(value)
		if total > 16384 {
			return
		}

		values[strings.Clone(name)] = strings.Clone(value)
	}

	s.pending[stage] = values
}

func macroName(name string) string { return strings.TrimSuffix(strings.TrimPrefix(name, "{"), "}") }

func (s *macroState) selected(name string) bool {
	for _, f := range s.fields {
		if macroName(f.Macro) == name {
			return true
		}
	}

	return false
}

func (s *macroState) snapshot() []slog.Attr {
	values := map[string]string{}

	for _, stage := range []byte("CHMRTNE") {
		for name, value := range s.active[stage] {
			values[strings.Clone(name)] = strings.Clone(value)
		}
	}

	attrs := make([]slog.Attr, 0, len(s.fields))
	total := 0

	for _, f := range s.fields {
		value := values[macroName(f.Macro)]
		if value == "" || total+len(value) > 16384 {
			continue
		}

		total += len(value)
		attrs = append(attrs, slog.String(logging.FieldKey(f.Name), value))
	}

	return []slog.Attr{slog.Uint64("milter_connection_id", s.connection), slog.Uint64("milter_message_number", s.message), {Key: "milter_macros", Value: slog.GroupValue(attrs...)}}
}
