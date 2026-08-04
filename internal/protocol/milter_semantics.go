package protocol

// Rspamd 4.1.1 negotiates SMFIP_NR_* for these transaction commands. They
// update its per-message state, but do not open a request/reply turn. EOM is
// deliberately absent: it returns zero or more actions and a disposition.
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
// request/reply turn. Rspamd 4.1.1 negotiates no-reply semantics for all
// state-carrying MTA transaction commands below. It replies to option
// negotiation and to EOM, which can contain action frames followed by a final
// disposition. Unknown commands remain request/reply turns so that an
// unsupported protocol extension fails closed instead of desynchronizing the
// session.
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
