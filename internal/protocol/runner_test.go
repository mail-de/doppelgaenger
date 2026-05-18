package protocol

import (
	"context"
	"errors"
	"testing"
)

const testEventKind = protocolHTTP

type fakeSession struct {
	sendErr  error
	recvErr  error
	response Response
	last     Event
}

func (s *fakeSession) Send(event Event) error {
	s.last = event
	return s.sendErr
}

func (s *fakeSession) Receive() (Response, error) {
	if s.recvErr != nil {
		return Response{Err: s.recvErr}, s.recvErr
	}

	return s.response, nil
}

func (s *fakeSession) Close() error {
	return nil
}

type fakeComparator struct {
	called bool
}

func (c *fakeComparator) Compare(_ Response, _ Response) (CompareResult, error) {
	c.called = true

	return CompareResult{Diff: true}, nil
}

func TestRunnerRunsComparator(t *testing.T) {
	primary := &fakeSession{response: Response{Status: 200}}
	shadow := &fakeSession{response: Response{Status: 200}}
	cmp := &fakeComparator{}
	runner := Runner{Comparator: cmp}

	result := runner.RunEvent(context.Background(), primary, shadow, Event{Kind: testEventKind})

	if !cmp.called {
		t.Fatalf("expected comparator to be called")
	}

	if !result.Compare.Diff {
		t.Fatalf("expected compare result to be returned")
	}
}

func TestRunnerShadowSendError(t *testing.T) {
	primary := &fakeSession{response: Response{Status: 200}}
	shadowErr := errors.New("boom")
	shadow := &fakeSession{sendErr: shadowErr}
	runner := Runner{}

	result := runner.RunEvent(context.Background(), primary, shadow, Event{Kind: testEventKind})

	if result.ShadowErr == "" {
		t.Fatalf("expected shadow error to be set")
	}
}
