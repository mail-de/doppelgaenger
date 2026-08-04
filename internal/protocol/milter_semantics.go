package protocol

// The supported no-reply profile uses SMFIP_NR_* semantics for these
// transaction commands. They update per-message state, but do not open a
// request/reply turn. EOM is deliberately absent: it returns zero or more
// actions and a disposition.
const (
	milterAbortCommand   = 'A'
	milterBodyCommand    = 'B'
	milterConnectCommand = 'C'
	milterMacroCommand   = 'D'
	milterHeloCommand    = 'H'
	milterQuitNCCommand  = 'K'
	milterHeaderCommand  = 'L'
	milterMailCommand    = 'M'
	milterEOHCommand     = 'N'
	milterQuitCommand    = 'Q'
	milterRcptCommand    = 'R'
	milterDataCommand    = 'T'
	milterUnknownCommand = 'U'
)

// MilterCommandExpectsResponse reports whether an MTA command starts a
// request/reply turn. The current profile applies no-reply semantics to all
// state-carrying MTA transaction commands below. It expects replies to option
// negotiation and EOM; EOM can contain action frames followed by a final
// disposition. Unknown commands remain request/reply turns so an unsupported
// protocol extension fails closed instead of desynchronizing the session.
func MilterCommandExpectsResponse(command byte) bool {
	switch command {
	case milterAbortCommand,
		milterBodyCommand,
		milterConnectCommand,
		milterMacroCommand,
		milterHeloCommand,
		milterQuitNCCommand,
		milterHeaderCommand,
		milterMailCommand,
		milterEOHCommand,
		milterQuitCommand,
		milterRcptCommand,
		milterDataCommand,
		milterUnknownCommand:
		return false
	default:
		return true
	}
}

func eventExpectsResponse(event Event) bool {
	if event.Kind != protocolMilter {
		return true
	}

	command := event.Meta["command"]
	if command == "" {
		return true
	}

	return MilterCommandExpectsResponse(command[0])
}
