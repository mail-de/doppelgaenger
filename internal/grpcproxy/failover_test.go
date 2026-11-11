package grpcproxy

import (
	"context"
	"errors"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"doppelgaenger/internal/config"
	"doppelgaenger/internal/failover"
)

const (
	testDeadTargetName   = "dead"
	testLiveTargetName   = "live"
	testFirstTargetName  = "target-first"
	testSecondTargetName = "target-second"
)

func testGRPCFailoverPolicy() failover.Policy {
	return failover.Policy{
		Enabled:           true,
		BackoffInitial:    10 * time.Millisecond,
		BackoffMultiplier: 2,
		BackoffMax:        50 * time.Millisecond,
		MaxWait:           5 * time.Second,
		AttemptTimeout:    time.Second,
	}
}

// closedGRPCAddr returns an address whose listener was just closed, so the
// channel fails to connect and NewStream reports Unavailable.
func closedGRPCAddr(t *testing.T) string {
	t.Helper()

	listener := newLocalListener(t)
	addr := listener.Addr().String()
	_ = listener.Close()

	return addr
}

// blackholeGRPCAddr returns an address that accepts TCP connections but never
// speaks HTTP/2, so stream creation blocks until the attempt timeout fires.
func blackholeGRPCAddr(t *testing.T) string {
	t.Helper()

	listener := newLocalListener(t)

	var (
		mu    sync.Mutex
		conns []net.Conn
	)

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			mu.Lock()

			conns = append(conns, conn)

			mu.Unlock()
		}
	}()

	t.Cleanup(func() {
		_ = listener.Close()

		mu.Lock()
		defer mu.Unlock()

		for _, conn := range conns {
			_ = conn.Close()
		}
	})

	return listener.Addr().String()
}

func failoverPool(policy failover.Policy, targets ...*Target) *TargetPool {
	pool := &TargetPool{mode: selectionModeRoundRobin, targets: targets}
	pool.setFailoverPolicy(policy)

	return pool
}

func startFailoverProxy(t *testing.T, pools *TargetPools, cfg config.Config) (*grpc.ClientConn, *captureLogHandler) {
	t.Helper()

	logger, logs := newCaptureLogger()
	handler := testProxyHandlerWithPools(cfg, catchAllShadowResolver(t), pools, logger, nil)

	return newTestClientConn(t, startTestProxyWithHandler(t, handler)), logs
}

func (h *captureLogHandler) find(msg string) []map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()

	var found []map[string]string

	for _, record := range h.records {
		if record["msg"] == msg {
			found = append(found, record)
		}
	}

	return found
}

func lastRecord(t *testing.T, logs *captureLogHandler, msg string) map[string]string {
	t.Helper()

	records := logs.find(msg)
	if len(records) == 0 {
		t.Fatalf("no %q log record", msg)
	}

	return records[len(records)-1]
}

func TestClassifyStreamCreateError(t *testing.T) {
	unavailable := status.Error(codes.Unavailable, "down")

	tests := []struct {
		name      string
		err       error
		timedOut  bool
		parentErr error
		want      failover.Outcome
	}{
		{name: "success", want: failover.OutcomeDone},
		{name: "unavailable status", err: unavailable, want: failover.OutcomeRetry},
		{name: "unavailable after parent ended", err: unavailable, parentErr: context.Canceled, want: failover.OutcomeStop},
		{name: "attempt timeout cancel", err: status.Error(codes.Canceled, "x"), timedOut: true, want: failover.OutcomeRetry},
		{name: "attempt timeout deadline", err: status.Error(codes.DeadlineExceeded, "x"), timedOut: true, want: failover.OutcomeRetry},
		{name: "closed client conn", err: status.Error(codes.Canceled, "grpc: the client connection is closing"), want: failover.OutcomeStop},
		{name: "internal", err: status.Error(codes.Internal, "x"), want: failover.OutcomeStop},
		{name: "resource exhausted", err: status.Error(codes.ResourceExhausted, "x"), want: failover.OutcomeStop},
		{name: "permission denied", err: status.Error(codes.PermissionDenied, "x"), want: failover.OutcomeStop},
		{name: "plain error", err: errors.New("x"), want: failover.OutcomeStop},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyStreamCreateError(tt.err, tt.timedOut, tt.parentErr); got != tt.want {
				t.Fatalf("classifyStreamCreateError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPrimaryFailoverUnaryUsesNextTarget(t *testing.T) {
	service := newTestPrimaryService()
	deadConn := newTestClientConn(t, closedGRPCAddr(t))
	liveConn := newTestClientConn(t, startTestPrimary(t, service))

	pools := &TargetPools{Primary: failoverPool(testGRPCFailoverPolicy(),
		&Target{Name: testDeadTargetName, Conn: deadConn},
		&Target{Name: testLiveTargetName, Conn: liveConn},
	)}
	conn, logs := startFailoverProxy(t, pools, config.Config{})

	var response rawMessage
	if err := conn.Invoke(outgoingTestContext(t), testFullMethodUnary, rawMessage("request"), &response); err != nil {
		t.Fatalf("expected failover to succeed, got %v", err)
	}

	assertRawMessage(t, response, "primary:request")
	assertPrimaryRecorded(t, service.unaryRequests(), "request")

	record := lastRecord(t, logs, "grpc_proxy")
	assertLogField(t, record, "primary_selected", testLiveTargetName)
	assertLogField(t, record, "primary_attempts", "2")
	assertLogField(t, record, "primary_failover_from", testDeadTargetName)

	attempt := lastRecord(t, logs, "backend_attempt_failed")
	assertLogField(t, attempt, "backend", "primary")
	assertLogField(t, attempt, "selected", testDeadTargetName)
	assertLogField(t, attempt, "level", "WARN")
}

func TestPrimaryFailoverBidirectionalStream(t *testing.T) {
	service := newTestPrimaryService()
	pools := &TargetPools{Primary: failoverPool(testGRPCFailoverPolicy(),
		&Target{Name: testDeadTargetName, Conn: newTestClientConn(t, closedGRPCAddr(t))},
		&Target{Name: testLiveTargetName, Conn: newTestClientConn(t, startTestPrimary(t, service))},
	)}
	conn, _ := startFailoverProxy(t, pools, config.Config{})

	stream := newClientStream(t, conn, testFullMethodBidiStream, genericStreamDesc())
	sendMessage(t, stream, "alpha")
	assertRawMessage(t, recvMessage(t, stream), "bidi:alpha")
	closeSend(t, stream)
	assertEOF(t, stream)

	assertPrimaryRecorded(t, service.bidiRequests(), "alpha")
}

func TestPrimaryFailoverExhaustedReturnsUnavailable(t *testing.T) {
	policy := testGRPCFailoverPolicy()
	policy.Retries = 1
	policy.Cooldown = 0

	pools := &TargetPools{Primary: failoverPool(policy,
		&Target{Name: "dead-a", Conn: newTestClientConn(t, closedGRPCAddr(t))},
		&Target{Name: "dead-b", Conn: newTestClientConn(t, closedGRPCAddr(t))},
	)}
	conn, logs := startFailoverProxy(t, pools, config.Config{})

	var response rawMessage

	err := conn.Invoke(outgoingTestContext(t), testFullMethodUnary, rawMessage("request"), &response)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %v", err)
	}

	record := lastRecord(t, logs, "grpc_proxy")
	assertLogField(t, record, "primary_attempts", "4")

	if got := len(logs.find("backend_attempt_failed")); got != 4 {
		t.Fatalf("attempt failure records = %d, want 4", got)
	}
}

func TestPrimaryFailoverDoesNotRetryStatusErrors(t *testing.T) {
	first := newTestPrimaryService()
	second := newTestPrimaryService()

	pools := &TargetPools{Primary: failoverPool(testGRPCFailoverPolicy(),
		&Target{Name: testFirstTargetName, Conn: newTestClientConn(t, startTestPrimary(t, first))},
		&Target{Name: testSecondTargetName, Conn: newTestClientConn(t, startTestPrimary(t, second))},
	)}
	conn, logs := startFailoverProxy(t, pools, config.Config{})

	var response rawMessage

	err := conn.Invoke(outgoingTestContext(t), testFullMethodUnaryError, rawMessage("request"), &response)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}

	assertPrimaryRecorded(t, first.unaryRequests(), "request")

	if got := second.unaryRequests(); len(got) != 0 {
		t.Fatalf("status error was failed over: %v", got)
	}

	if got := logs.find("backend_attempt_failed"); len(got) != 0 {
		t.Fatalf("unexpected attempt failures: %v", got)
	}
}

func TestPrimaryFailoverDoesNotRetryUnavailableAfterStreamStarted(t *testing.T) {
	first := newTestPrimaryService()
	first.unaryErr = status.Error(codes.Unavailable, "draining")
	second := newTestPrimaryService()

	pools := &TargetPools{Primary: failoverPool(testGRPCFailoverPolicy(),
		&Target{Name: testFirstTargetName, Conn: newTestClientConn(t, startTestPrimary(t, first))},
		&Target{Name: testSecondTargetName, Conn: newTestClientConn(t, startTestPrimary(t, second))},
	)}
	conn, _ := startFailoverProxy(t, pools, config.Config{})

	var response rawMessage

	err := conn.Invoke(outgoingTestContext(t), testFullMethodUnary, rawMessage("request"), &response)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable from the handler, got %v", err)
	}

	assertPrimaryRecorded(t, first.unaryRequests(), "request")

	if got := second.unaryRequests(); len(got) != 0 {
		t.Fatalf("handler-level Unavailable was repeated on another target: %v", got)
	}
}

func TestPrimaryFailoverAttemptTimeoutSkipsBlackhole(t *testing.T) {
	policy := testGRPCFailoverPolicy()
	policy.AttemptTimeout = 200 * time.Millisecond

	service := newTestPrimaryService()
	pools := &TargetPools{Primary: failoverPool(policy,
		&Target{Name: "blackhole", Conn: newTestClientConn(t, blackholeGRPCAddr(t))},
		&Target{Name: testLiveTargetName, Conn: newTestClientConn(t, startTestPrimary(t, service))},
	)}
	conn, logs := startFailoverProxy(t, pools, config.Config{})

	started := time.Now()

	var response rawMessage
	if err := conn.Invoke(outgoingTestContext(t), testFullMethodUnary, rawMessage("request"), &response); err != nil {
		t.Fatalf("expected failover past blackhole, got %v", err)
	}

	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("attempt timeout not applied, took %s", elapsed)
	}

	assertPrimaryRecorded(t, service.unaryRequests(), "request")
	assertLogField(t, lastRecord(t, logs, "grpc_proxy"), "primary_selected", testLiveTargetName)
}

func TestPrimaryFailoverAttemptTimeoutDoesNotLimitStream(t *testing.T) {
	policy := testGRPCFailoverPolicy()
	policy.AttemptTimeout = 100 * time.Millisecond

	service := newTestPrimaryService()
	service.unaryDelay = 300 * time.Millisecond

	pools := &TargetPools{Primary: failoverPool(policy,
		&Target{Name: testLiveTargetName, Conn: newTestClientConn(t, startTestPrimary(t, service))},
		&Target{Name: "other", Conn: newTestClientConn(t, startTestPrimary(t, newTestPrimaryService()))},
	)}
	conn, _ := startFailoverProxy(t, pools, config.Config{})

	var response rawMessage
	if err := conn.Invoke(outgoingTestContext(t), testFullMethodUnary, rawMessage("slow"), &response); err != nil {
		t.Fatalf("attempt timeout must not cancel an established stream: %v", err)
	}

	assertRawMessage(t, response, "primary:slow")
}

func TestPrimaryFailoverDoesNotRetryClosedClientConn(t *testing.T) {
	closedConn := newTestClientConn(t, closedGRPCAddr(t))
	_ = closedConn.Close()

	second := newTestPrimaryService()
	pools := &TargetPools{Primary: failoverPool(testGRPCFailoverPolicy(),
		&Target{Name: "closed", Conn: closedConn},
		&Target{Name: testSecondTargetName, Conn: newTestClientConn(t, startTestPrimary(t, second))},
	)}

	// Pin the first choice to the closed connection; TRANSIENT_FAILURE
	// ordering does not apply to a shut-down channel.
	opened := pools.Primary.openStream(context.Background(), targetSelection{index: 0}, testFullMethodUnary, nil, nil)
	defer opened.release()

	if status.Code(opened.err) != codes.Canceled || opened.stats.Attempts != 1 {
		t.Fatalf("closed connection was retried: err=%v attempts=%d", opened.err, opened.stats.Attempts)
	}
}

func TestPrimaryFailoverDisabledKeepsSingleAttempt(t *testing.T) {
	service := newTestPrimaryService()
	pools := &TargetPools{Primary: failoverPool(failover.Policy{Enabled: false, Retries: 3},
		&Target{Name: testDeadTargetName, Conn: newTestClientConn(t, closedGRPCAddr(t))},
		&Target{Name: testLiveTargetName, Conn: newTestClientConn(t, startTestPrimary(t, service))},
	)}
	conn, logs := startFailoverProxy(t, pools, config.Config{})

	var response rawMessage

	err := conn.Invoke(outgoingTestContext(t), testFullMethodUnary, rawMessage("request"), &response)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable without failover, got %v", err)
	}

	if got := service.unaryRequests(); len(got) != 0 {
		t.Fatalf("disabled policy failed over: %v", got)
	}

	record := lastRecord(t, logs, "grpc_proxy")
	if _, ok := record["primary_attempts"]; ok {
		t.Fatalf("disabled policy must not add attempt fields: %v", record)
	}
}

func TestShadowFailoverUsesNextShadowTarget(t *testing.T) {
	primary := newTestPrimaryService()
	shadow := newTestPrimaryService()

	pools := &TargetPools{
		Primary: testTargetPool(testPrimaryTargetName, newTestClientConn(t, startTestPrimary(t, primary))),
		Shadow: failoverPool(testGRPCFailoverPolicy(),
			&Target{Name: "shadow-dead", Conn: newTestClientConn(t, closedGRPCAddr(t))},
			&Target{Name: "shadow-live", Conn: newTestClientConn(t, startTestPrimary(t, shadow))},
		),
	}
	conn, logs := startFailoverProxy(t, pools, testShadowConfig(100))

	var response rawMessage
	if err := conn.Invoke(outgoingTestContext(t), testFullMethodUnary, rawMessage("request"), &response); err != nil {
		t.Fatalf("primary call failed: %v", err)
	}

	assertRawMessage(t, response, "primary:request")
	assertPrimaryRecorded(t, shadow.unaryRequests(), "request")

	record := lastRecord(t, logs, "grpc_proxy")
	assertLogField(t, record, "shadow_selected", "shadow-live")
	assertLogField(t, record, "shadow_attempts", "2")
	assertLogField(t, record, "shadow_failover_from", "shadow-dead")

	attempt := lastRecord(t, logs, "backend_attempt_failed")
	assertLogField(t, attempt, "backend", "shadow")
	assertLogField(t, attempt, "level", "DEBUG")
}

func TestShadowFailoverExhaustedDoesNotAffectPrimary(t *testing.T) {
	primary := newTestPrimaryService()

	pools := &TargetPools{
		Primary: testTargetPool(testPrimaryTargetName, newTestClientConn(t, startTestPrimary(t, primary))),
		Shadow: failoverPool(testGRPCFailoverPolicy(),
			&Target{Name: "shadow-dead-a", Conn: newTestClientConn(t, closedGRPCAddr(t))},
			&Target{Name: "shadow-dead-b", Conn: newTestClientConn(t, closedGRPCAddr(t))},
		),
	}
	conn, logs := startFailoverProxy(t, pools, testShadowConfig(100))

	var response rawMessage
	if err := conn.Invoke(outgoingTestContext(t), testFullMethodUnary, rawMessage("request"), &response); err != nil {
		t.Fatalf("shadow failure changed primary result: %v", err)
	}

	assertRawMessage(t, response, "primary:request")

	record := lastRecord(t, logs, "grpc_proxy")
	assertLogField(t, record, "shadow_started", "false")
	assertLogField(t, record, "primary_status", codes.OK.String())
}

func TestTargetPoolSourceIPHashFailoverOrderIsStable(t *testing.T) {
	pool := &TargetPool{
		mode:    selectionModeSourceIPHash,
		targets: []*Target{{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"}},
	}
	info := RequestInfo{PeerAddr: "198.51.100.7:5000"}

	first, selection := pool.selectTarget(info)
	again, selectionAgain := pool.selectTarget(info)

	if first != again || selection != selectionAgain || !selection.hashed {
		t.Fatalf("source IP selection not stable: %+v vs %+v", selection, selectionAgain)
	}

	order := failover.Order(selection.index, len(pool.targets), selection.hashed, selection.seed)
	if order[0] != selection.index || !slices.Equal(order, failover.Order(selection.index, len(pool.targets), true, selection.seed)) {
		t.Fatalf("unexpected failover order %v", order)
	}

	if pool.Pick(info) != first {
		t.Fatal("Pick must keep returning the hashed target")
	}
}
