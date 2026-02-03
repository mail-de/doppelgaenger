package protocol

import (
	"context"
	"errors"

	"httpproxy/internal/backend"
)

type HTTPAdapter struct {
	PrimaryPool backend.Pool
	ShadowPool  backend.Pool
}

func (a HTTPAdapter) Protocol() string {
	return "http"
}

func (a HTTPAdapter) NewSession(ctx context.Context, target Target) (TestSession, error) {
	var pool backend.Pool
	var kind backend.BackendKind
	if target == TargetPrimary {
		pool = a.PrimaryPool
		kind = backend.BackendPrimary
	} else if target == TargetShadow {
		pool = a.ShadowPool
		kind = backend.BackendShadow
	} else {
		return nil, errors.New("unknown target")
	}

	return &httpSession{pool: pool, ctx: ctx, kind: kind}, nil
}

type httpSession struct {
	pool  backend.Pool
	ctx   context.Context
	kind  backend.BackendKind
	resCh chan backend.BackendResult
}

func (s *httpSession) Send(event Event) error {
	if s.resCh != nil {
		return errors.New("request already sent")
	}

	s.resCh = make(chan backend.BackendResult, 1)
	item := backend.WorkItem{
		Kind:       s.kind,
		Method:     event.Method,
		Path:       event.Path,
		RawQuery:   event.RawQuery,
		Header:     event.Header,
		Body:       event.Body,
		RemoteAddr: event.RemoteAddr,
		RequestID:  event.RequestID,
		Ctx:        s.ctx,
		ResponseCh: s.resCh,
	}

	return s.pool.Enqueue(item)
}

func (s *httpSession) Receive() (Response, error) {
	if s.resCh == nil {
		return Response{Err: errors.New("request not sent")}, errors.New("request not sent")
	}

	res := <-s.resCh
	return Response{
		Proto:    res.Proto,
		Status:   res.Status,
		Header:   res.Header,
		Body:     res.Body,
		Duration: res.Duration,
		Err:      res.Err,
		Decision: DecisionUnknown,
	}, res.Err
}

func (s *httpSession) Close() error {
	return nil
}
