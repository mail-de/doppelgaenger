package protocol

import (
	"context"
	"errors"

	"doppelgaenger/internal/backend"
	"doppelgaenger/internal/headers"
)

const protocolHTTP = "http"

// HTTPAdapter opens HTTP sessions against primary and shadow backends.
type HTTPAdapter struct {
	PrimaryRequester      backend.Requester
	ShadowRequester       backend.Requester
	PrimaryRequestHeaders map[string]string
	ShadowRequestHeaders  map[string]string
}

// Protocol reports the adapter protocol name.
func (a HTTPAdapter) Protocol() string {
	return protocolHTTP
}

// NewSession creates a new HTTP backend session for the requested target.
func (a HTTPAdapter) NewSession(ctx context.Context, target Target) (TestSession, error) {
	var (
		kind                     backend.Kind
		requester                backend.Requester
		configuredRequestHeaders map[string]string
	)

	switch target {
	case TargetPrimary:
		kind = backend.BackendPrimary
		requester = a.PrimaryRequester
		configuredRequestHeaders = a.PrimaryRequestHeaders
	case TargetShadow:
		kind = backend.BackendShadow
		requester = a.ShadowRequester
		configuredRequestHeaders = a.ShadowRequestHeaders
	default:
		return nil, errors.New("unknown target")
	}

	if requester == nil {
		return nil, errors.New("missing backend requester")
	}

	return &httpSession{requester: requester, ctx: ctx, kind: kind, configuredRequestHeaders: configuredRequestHeaders}, nil
}

type httpSession struct {
	requester                backend.Requester
	ctx                      context.Context
	kind                     backend.Kind
	result                   *backend.Result
	configuredRequestHeaders map[string]string
}

func (s *httpSession) Send(event Event) error {
	if s.result != nil {
		return errors.New("request already sent")
	}

	path := event.Path
	if s.kind == backend.BackendPrimary && event.PrimaryPath != "" {
		path = event.PrimaryPath
	}

	if s.kind == backend.BackendShadow && event.ShadowPath != "" {
		path = event.ShadowPath
	}

	headersToSend := headers.Clone(event.Header)
	for name, value := range s.configuredRequestHeaders {
		headersToSend.Set(name, value)
	}

	headers.RemoveHopByHop(headersToSend)

	item := backend.Request{
		Kind:       s.kind,
		Method:     event.Method,
		Path:       path,
		RawQuery:   event.RawQuery,
		Host:       event.Host,
		Header:     headersToSend,
		Body:       event.Body,
		RemoteAddr: event.RemoteAddr,
		RequestID:  event.RequestID,
		Ctx:        s.ctx,
	}

	result := s.requester.Do(item)
	s.result = &result

	return nil
}

func (s *httpSession) Receive() (Response, error) {
	if s.result == nil {
		return Response{Err: errors.New("request not sent")}, errors.New("request not sent")
	}

	res := *s.result

	return Response{
		Proto:    res.Proto,
		Selected: res.Selected,
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
