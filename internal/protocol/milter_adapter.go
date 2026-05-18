package protocol

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"doppelgaenger/internal/observability"
)

const protocolMilter = "milter"

// MilterAdapter opens Milter sessions against primary and shadow backends.
type MilterAdapter struct {
	PrimaryAddr   string
	ShadowAddr    string
	Timeout       time.Duration
	Observability *observability.Observability
}

// Protocol reports the adapter protocol name.
func (a MilterAdapter) Protocol() string {
	return protocolMilter
}

// NewSession creates a new Milter backend session for the requested target.
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

	dialCtx, dialSpan := a.startDialSpan(ctx, target, addr)
	dialStart := time.Now()
	conn, err := dialer.DialContext(dialCtx, "tcp", addr)
	a.finishDialObservation(dialCtx, dialSpan, target, time.Since(dialStart), err)

	if err != nil {
		return nil, err
	}

	return &milterSession{
		conn:          conn,
		timeout:       a.Timeout,
		addr:          addr,
		target:        target,
		ctx:           dialCtx,
		observability: a.Observability,
	}, nil
}

func (a MilterAdapter) startDialSpan(ctx context.Context, target Target, addr string) (context.Context, trace.Span) {
	if ctx == nil {
		ctx = context.Background()
	}

	if a.Observability == nil {
		return ctx, nil
	}

	return a.Observability.StartSpanWithKind(ctx,
		fmt.Sprintf("milter %s connect", target),
		trace.SpanKindClient,
		attribute.String(observability.LabelProtocol, protocolMilter),
		attribute.String(observability.LabelTarget, string(target)),
		attribute.String(observability.LabelMethod, "connect"),
		attribute.String("server.address", addr),
	)
}

func (a MilterAdapter) finishDialObservation(ctx context.Context, span trace.Span, target Target, duration time.Duration, err error) {
	if a.Observability == nil {
		return
	}

	status := observability.ResultOK
	result := observability.ResultOK

	if err != nil {
		status = observability.StatusError
		result = observability.ResultError
	}

	a.Observability.ObserveBackendRequest(ctx, protocolMilter, string(target), "connect", status, result, duration)
	a.Observability.EndSpan(span, err)
}

type milterSession struct {
	conn          net.Conn
	timeout       time.Duration
	addr          string
	target        Target
	ctx           context.Context
	observability *observability.Observability

	span      trace.Span
	spanCtx   context.Context
	spanStart time.Time
	command   string
}

func (s *milterSession) Send(event Event) error {
	if len(event.Payload) == 0 {
		return errors.New("missing milter payload")
	}

	ctx := event.Ctx
	if ctx == nil {
		ctx = s.ctx
	}

	if ctx == nil {
		ctx = context.Background()
	}

	command := event.Meta["command"]
	if command == "" {
		command = "unknown"
	}

	s.command = command
	s.spanStart = time.Now()

	if s.observability != nil {
		s.spanCtx, s.span = s.observability.StartSpanWithKind(ctx,
			fmt.Sprintf("milter %s %s", s.target, command),
			trace.SpanKindClient,
			attribute.String(observability.LabelProtocol, protocolMilter),
			attribute.String(observability.LabelTarget, string(s.target)),
			attribute.String(observability.LabelMethod, command),
		)
	} else {
		s.spanCtx = ctx
	}

	if s.timeout > 0 {
		_ = s.conn.SetWriteDeadline(time.Now().Add(s.timeout))
	}

	_, err := s.conn.Write(event.Payload)
	if err != nil {
		s.finishObservation(observability.StatusError, observability.ResultError, err)
	}

	return err
}

func (s *milterSession) Receive() (Response, error) {
	if s.timeout > 0 {
		_ = s.conn.SetReadDeadline(time.Now().Add(s.timeout))
	}

	frame, err := readMilterFrame(s.conn)
	if err != nil {
		duration := time.Since(s.spanStart)
		s.finishObservation(observability.StatusError, observability.ResultError, err)

		return Response{Err: err, Selected: s.addr, Duration: duration}, err
	}

	decision := milterDecision(frame.Command)
	duration := time.Since(s.spanStart)
	s.finishObservation(string(decision), observability.ResultOK, nil)

	return Response{
		Proto:    protocolMilter,
		Decision: decision,
		Raw:      frame.Raw,
		Selected: s.addr,
		Duration: duration,
	}, nil
}

func (s *milterSession) Close() error {
	return s.conn.Close()
}

func (s *milterSession) finishObservation(status, result string, err error) {
	if s.observability == nil {
		return
	}

	ctx := s.spanCtx
	if ctx == nil {
		ctx = context.Background()
	}

	duration := time.Duration(0)
	if !s.spanStart.IsZero() {
		duration = time.Since(s.spanStart)
	}

	s.observability.ObserveBackendRequest(ctx, protocolMilter, string(s.target), s.command, status, result, duration)
	s.observability.EndSpan(s.span, err)
	s.span = nil
	s.spanCtx = nil
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
