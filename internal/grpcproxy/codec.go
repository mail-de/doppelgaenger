package grpcproxy

import "fmt"

type rawMessage []byte

type rawCodec struct{}

func (rawCodec) Name() string {
	return "proto"
}

func (rawCodec) Marshal(v any) ([]byte, error) {
	switch msg := v.(type) {
	case rawMessage:
		return []byte(msg), nil
	case *rawMessage:
		if msg == nil {
			return nil, fmt.Errorf("unsupported nil raw message")
		}

		return []byte(*msg), nil
	default:
		return nil, fmt.Errorf("unsupported raw message type %T", v)
	}
}

func (rawCodec) Unmarshal(data []byte, v any) error {
	msg, ok := v.(*rawMessage)
	if !ok {
		return fmt.Errorf("unsupported raw message target %T", v)
	}

	*msg = append((*msg)[:0], data...)

	return nil
}
