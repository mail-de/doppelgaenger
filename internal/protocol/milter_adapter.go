package protocol

import (
	"context"
	"errors"
	"net"
	"time"
)

type MilterAdapter struct {
	PrimaryAddr string
	ShadowAddr  string
	Timeout     time.Duration
}

func (a MilterAdapter) Protocol() string {
	return "milter"
}

func (a MilterAdapter) NewSession(ctx context.Context, target Target) (TestSession, error) {
	addr := ""
	switch target {
	case TargetPrimary:
		addr = a.PrimaryAddr
	case TargetShadow:
		addr = a.ShadowAddr
	default:
		return nil, errors.New("unknown target")
	}
	if addr == "" {
		return nil, errors.New("missing milter address")
	}

	dialer := net.Dialer{}
	if a.Timeout > 0 {
		dialer.Timeout = a.Timeout
	}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}

	return &milterSession{conn: conn, timeout: a.Timeout}, nil
}

type milterSession struct {
	conn    net.Conn
	timeout time.Duration
}

func (s *milterSession) Send(event Event) error {
	if len(event.Payload) == 0 {
		return errors.New("missing milter payload")
	}
	if s.timeout > 0 {
		_ = s.conn.SetWriteDeadline(time.Now().Add(s.timeout))
	}
	_, err := s.conn.Write(event.Payload)
	return err
}

func (s *milterSession) Receive() (Response, error) {
	if s.timeout > 0 {
		_ = s.conn.SetReadDeadline(time.Now().Add(s.timeout))
	}
	frame, err := readMilterFrame(s.conn)
	if err != nil {
		return Response{Err: err}, err
	}

	decision := milterDecision(frame.Command)
	return Response{
		Proto:    "milter",
		Decision: decision,
		Raw:      frame.Raw,
	}, nil
}

func (s *milterSession) Close() error {
	return s.conn.Close()
}

func milterDecision(command byte) Decision {
	switch command {
	case 'a':
		return DecisionAccept
	case 'r':
		return DecisionReject
	case 't':
		return DecisionTempfail
	case 'd':
		return DecisionDiscard
	case 'c':
		return DecisionContinue
	default:
		return DecisionUnknown
	}
}
