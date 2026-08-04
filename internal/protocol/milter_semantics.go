package protocol

const milterMacroCommand = 'D'

// MilterCommandExpectsResponse reports whether an MTA command starts a
// request/reply turn. SMFIC_MACRO only supplies state for the next command and
// compliant Milters do not reply to it.
func MilterCommandExpectsResponse(command byte) bool {
	return command != milterMacroCommand
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
