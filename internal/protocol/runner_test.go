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
	receives int
}

func (s *fakeSession) Send(event Event) error {
	s.last = event
	return s.sendErr
}

func (s *fakeSession) Receive() (Response, error) {
	s.receives++

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

func TestRunnerNoReplyCommandsSkipReceiveForPrimaryAndShadow(t *testing.T) {
	noReplyCommands := []byte{'A', 'B', 'C', 'D', 'H', 'K', 'L', 'M', 'N', 'Q', 'R', 'T', 'U'}

	for _, command := range noReplyCommands {
		t.Run(string(command), func(t *testing.T) {
			primary := &fakeSession{response: Response{Raw: []byte("primary")}}
			shadow := &fakeSession{response: Response{Raw: []byte("shadow")}}
			runner := Runner{Comparator: MilterComparator{}}
			event := Event{Kind: protocolMilter, Meta: map[string]string{"command": string(command)}}

			result := runner.RunEvent(context.Background(), primary, shadow, event)

			if primary.receives != 0 {
				t.Fatalf("primary received %d reply reads for no-reply command %q", primary.receives, command)
			}

			if shadow.receives != 0 {
				t.Fatalf("shadow received %d reply reads for no-reply command %q", shadow.receives, command)
			}

			if !result.ShadowOK || !result.ShadowStarted {
				t.Fatalf("expected no-reply shadow command %q to complete: %+v", command, result)
			}
		})
	}
}
