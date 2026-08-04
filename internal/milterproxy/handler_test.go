package milterproxy

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"doppelgaenger/internal/config"
	"doppelgaenger/internal/observability"
	"doppelgaenger/internal/protocol"
)

const (
	milterTestShadowDelay  = 200 * time.Millisecond
	milterTestPrimaryWait  = 100 * time.Millisecond
	milterTestShadowTarget = "shadow"
)

type testMilterAdapter struct {
	primary protocol.TestSession
	shadow  protocol.TestSession
}

func (a testMilterAdapter) Protocol() string {
	return protocolMilter
}

func (a testMilterAdapter) NewSession(_ context.Context, target protocol.Target) (protocol.TestSession, error) {
	if target == protocol.TargetPrimary {
		return a.primary, nil
	}

	return a.shadow, nil
}

type testPrimaryMilterSession struct{}

func (testPrimaryMilterSession) Send(protocol.Event) error {
	return nil
}

func (testPrimaryMilterSession) Receive() (protocol.Response, error) {
	return protocol.Response{
		Decision: protocol.DecisionAccept,
		Raw:      testMilterFrame('a', nil),
		Selected: "primary",
	}, nil
}

func (testPrimaryMilterSession) Close() error {
	return nil
}

type testShadowMilterSession struct {
	delay       time.Duration
	waitContext bool
	done        chan struct{}

	mu  sync.Mutex
	ctx context.Context
}

func (s *testShadowMilterSession) Send(event protocol.Event) error {
	s.mu.Lock()
	s.ctx = event.Ctx
	s.mu.Unlock()

	return nil
}

func (s *testShadowMilterSession) Receive() (protocol.Response, error) {
	s.mu.Lock()
	ctx := s.ctx
	s.mu.Unlock()

	if s.waitContext {
		<-ctx.Done()
		close(s.done)

		return protocol.Response{Err: ctx.Err(), Selected: milterTestShadowTarget}, ctx.Err()
	}

	time.Sleep(s.delay)
	close(s.done)

	return protocol.Response{
		Decision: protocol.DecisionReject,
		Raw:      testMilterFrame('r', nil),
		Selected: milterTestShadowTarget,
	}, nil
}

func (s *testShadowMilterSession) Close() error {
	return nil
}

func TestHandleConnReturnsPrimaryBeforeShadowCompletes(t *testing.T) {
	tests := []struct {
		name         string
		shadow       *testShadowMilterSession
		timeout      time.Duration
		metricResult string
		waitFailure  string
	}{
		{
			name:         "slow shadow",
			shadow:       &testShadowMilterSession{delay: milterTestShadowDelay, done: make(chan struct{})},
			timeout:      500 * time.Millisecond,
			metricResult: observability.ResultDiff,
			waitFailure:  "timed out waiting for delayed shadow comparison",
		},
		{
			name:         "missing shadow response",
			shadow:       &testShadowMilterSession{waitContext: true, done: make(chan struct{})},
			timeout:      40 * time.Millisecond,
			metricResult: observability.ResultError,
			waitFailure:  "timed out waiting for shadow timeout",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			obs := newMilterTestObservability(t)
			handler := newMilterTestHandler(obs, testMilterAdapter{
				primary: testPrimaryMilterSession{},
				shadow:  test.shadow,
			}, test.timeout)

			assertMilterPrimaryResponseDoesNotWaitForShadow(t, handler)

			select {
			case <-test.shadow.done:
			case <-time.After(time.Second):
				t.Fatal(test.waitFailure)
			}

			assertMilterComparisonMetric(t, obs, test.metricResult)
		})
	}
}

func TestHandleConnCompletesRspamdCompatibleEOMReplySequence(t *testing.T) {
	backendAddr, backendDone := startRspamdCompatibleMilter(t)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for Milter proxy: %v", err)
	}

	handler := newMilterTestHandler(nil, protocol.MilterAdapter{PrimaryAddr: backendAddr}, time.Second)
	proxyDone := make(chan struct{})

	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			handler.HandleConn(conn)
		}

		close(proxyDone)
	}()

	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial Milter proxy: %v", err)
	}

	defer func() {
		_ = client.Close()
		_ = listener.Close()

		select {
		case <-proxyDone:
		case <-time.After(time.Second):
			t.Error("timed out waiting for Milter proxy shutdown")
		}

		select {
		case <-backendDone:
		case <-time.After(time.Second):
			t.Error("timed out waiting for Rspamd-compatible backend shutdown")
		}
	}()

	for _, request := range []protocol.MilterFrame{
		{Command: 'O', Payload: testMilterOptionNegotiationPayload()},
		{Command: 'C', Payload: []byte("smtp.example.test\x000\x00")},
		{Command: 'H', Payload: []byte("client.example.test\x00")},
		{Command: 'M', Payload: []byte("<sender@example.test>\x00")},
		{Command: 'R', Payload: []byte("<recipient@example.test>\x00")},
		{Command: 'L', Payload: []byte("Subject\x00Milter EOM regression\x00")},
		{Command: 'N'},
		{Command: 'B', Payload: []byte("message body\r\n")},
	} {
		writeMilterTestFrame(t, client, request.Command, request.Payload)
		assertMilterTestReply(t, client, expectedMilterReply(request.Command))
	}

	writeMilterTestFrame(t, client, 'E', nil)
	assertMilterTestReply(t, client, 'h')
	assertMilterTestReply(t, client, 'c')
}

func startRspamdCompatibleMilter(t *testing.T) (string, <-chan struct{}) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for Rspamd-compatible Milter: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			_ = listener.Close()
		}()

		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() {
			_ = conn.Close()
		}()

		for {
			frame, readErr := protocol.ReadFrame(conn)
			if readErr != nil {
				return
			}

			if frame.Command == 'E' {
				_, _ = conn.Write(testMilterFrame('h', []byte("X-Rspamd-Test\x00passed\x00")))
				_, _ = conn.Write(testMilterFrame('c', nil))

				continue
			}

			_, _ = conn.Write(testMilterFrame(expectedMilterReply(frame.Command), testMilterReplyPayload(frame.Command)))
		}
	}()

	return listener.Addr().String(), done
}

func expectedMilterReply(command byte) byte {
	if command == 'O' {
		return 'O'
	}

	return 'c'
}

func testMilterReplyPayload(command byte) []byte {
	if command == 'O' {
		return testMilterOptionNegotiationPayload()
	}

	return nil
}

func testMilterOptionNegotiationPayload() []byte {
	payload := make([]byte, 12)
	binary.BigEndian.PutUint32(payload, 6)

	return payload
}

func writeMilterTestFrame(t *testing.T, conn net.Conn, command byte, payload []byte) {
	t.Helper()

	if _, err := conn.Write(testMilterFrame(command, payload)); err != nil {
		t.Fatalf("write Milter %q frame: %v", command, err)
	}
}

func assertMilterTestReply(t *testing.T, conn net.Conn, command byte) {
	t.Helper()

	if err := conn.SetReadDeadline(time.Now().Add(milterTestPrimaryWait)); err != nil {
		t.Fatalf("set Milter reply deadline: %v", err)
	}

	frame, err := protocol.ReadFrame(conn)
	if err != nil {
		t.Fatalf("read Milter %q reply: %v", command, err)
	}

	if frame.Command != command {
		t.Fatalf("expected Milter reply %q, got %q", command, frame.Command)
	}
}

func newMilterTestHandler(obs *observability.Observability, adapter protocol.Adapter, timeout time.Duration) *Handler {
	return &Handler{
		cfg:     config.Config{ShadowSamplePercent: 100},
		adapter: adapter,
		runner: protocol.Runner{
			Comparator:    protocol.MilterComparator{},
			ShadowTimeout: timeout,
		},
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		observability: obs,
		rng:           newLockedRand(),
	}
}

func newMilterTestObservability(t *testing.T) *observability.Observability {
	t.Helper()

	obs, err := observability.New(config.Config{
		Observability: config.ObservabilityConfig{PrometheusEnabled: true},
	}, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new observability: %v", err)
	}

	return obs
}

func assertMilterPrimaryResponseDoesNotWaitForShadow(t *testing.T, handler *Handler) {
	t.Helper()

	server, client := net.Pipe()
	finished := make(chan struct{})

	go func() {
		handler.HandleConn(server)
		close(finished)
	}()

	defer func() {
		_ = client.Close()

		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("timed out waiting for Milter connection shutdown")
		}
	}()

	start := time.Now()

	if _, err := client.Write(testMilterFrame('c', []byte("test"))); err != nil {
		t.Fatalf("write request frame: %v", err)
	}

	if err := client.SetReadDeadline(time.Now().Add(milterTestPrimaryWait)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}

	response, err := protocol.ReadFrame(client)
	if err != nil {
		t.Fatalf("read primary response: %v", err)
	}

	if elapsed := time.Since(start); elapsed >= milterTestPrimaryWait {
		t.Fatalf("primary Milter response waited for shadow: %s", elapsed)
	}

	if response.Command != 'a' {
		t.Fatalf("expected primary accept frame, got %q", response.Command)
	}
}

func assertMilterComparisonMetric(t *testing.T, obs *observability.Observability, result string) {
	t.Helper()

	w := httptest.NewRecorder()
	obs.PrometheusHandler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))

	want := `doppelgaenger_comparisons_total{protocol="milter",result="` + result + `"} 1`
	if !strings.Contains(w.Body.String(), want) {
		t.Fatalf("expected comparison metric %q, got:\n%s", want, w.Body.String())
	}
}

func testMilterFrame(command byte, payload []byte) []byte {
	frame := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(frame, uint32(1+len(payload)))
	frame[4] = command
	copy(frame[5:], payload)

	return frame
}
