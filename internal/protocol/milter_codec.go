package protocol

import (
	"encoding/binary"
	"errors"
	"io"
)

// MilterFrame contains one decoded Milter frame.
type MilterFrame struct {
	Command byte
	Payload []byte
	Raw     []byte
}

// ReadFrame reads and decodes one Milter frame from reader.
func ReadFrame(reader io.Reader) (MilterFrame, error) {
	return readMilterFrame(reader)
}

func readMilterFrame(reader io.Reader) (MilterFrame, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(reader, lenBuf[:]); err != nil {
		return MilterFrame{}, err
	}

	frameLen := binary.BigEndian.Uint32(lenBuf[:])
	if frameLen < 1 {
		return MilterFrame{}, errors.New("milter frame too short")
	}

	payload := make([]byte, frameLen)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return MilterFrame{}, err
	}

	frame := MilterFrame{
		Command: payload[0],
		Payload: payload[1:],
	}
	frame.Raw = encodeMilterFrame(frame.Command, frame.Payload)

	return frame, nil
}

func encodeMilterFrame(command byte, payload []byte) []byte {
	frameLen := 1 + len(payload)
	buffer := make([]byte, 4+frameLen)
	binary.BigEndian.PutUint32(buffer[:4], uint32(frameLen))
	buffer[4] = command
	copy(buffer[5:], payload)

	return buffer
}
