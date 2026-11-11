package grpcproxy

import (
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"doppelgaenger/internal/config"
	"doppelgaenger/internal/failover"
)

func leastConnPool(policy failover.Policy, targets ...*Target) *TargetPool {
	pool := &TargetPool{mode: selectionModeLeastConn, targets: targets}
	pool.setFailoverPolicy(policy)

	return pool
}

// waitNoOpenStreams waits until the proxy has released every stream slot. The
// proxy finishes a call slightly after the client has seen its result.
func waitNoOpenStreams(t *testing.T, pool *TargetPool) {
	t.Helper()

	deadline := time.Now().Add(testShortTimeout)

	for {
		open := int64(0)
		for i := range pool.targets {
			open += pool.load.InFlight(i)
		}

		if open == 0 {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("%d streams are still counted as open", open)
		}

		time.Sleep(5 * time.Millisecond)
	}
}

func invokeUnary(t *testing.T, conn *grpc.ClientConn) error {
	t.Helper()

	var response rawMessage

	return conn.Invoke(outgoingTestContext(t), testFullMethodUnary, rawMessage("request"), &response)
}

func TestTargetPoolLeastConnSelection(t *testing.T) {
	pool := leastConnPool(failover.Policy{Enabled: true},
		&Target{Name: selectorTargetA}, &Target{Name: selectorTargetB}, &Target{Name: selectorTargetC})

	// Selection only hands out a placeholder without a name; the target is
	// chosen when the stream is opened.
	target, selection := pool.selectTarget(RequestInfo{})
	if target != pendingTarget || target.Name != "" || !selection.leastConn {
		t.Fatalf("selection = %+v %+v, want a deferred least_conn selection", target, selection)
	}

	if pool.Pick(RequestInfo{}) != pendingTarget {
		t.Fatal("Pick must return the placeholder for a least_conn pool")
	}

	pool.load.Acquire(0)
	pool.load.Acquire(0)
	pool.load.Acquire(2)

	// Targets without a connection count as failing, so the choice fails open
	// on load alone.
	selection.index = pool.load.Pick(pool.available)
	if order := pool.candidateOrder(selection); len(order) != 3 || order[0] != 1 || order[1] != 2 || order[2] != 0 {
		t.Fatalf("candidate order = %v, want [1 2 0]", order)
	}
}

// Calls can wait between selection and stream creation, for example for a
// token. The target must be chosen when the stream is opened; choosing it at
// selection would send every waiting call to the target that was least loaded
// before any of them started.
func TestLeastConnChoosesTargetWhenStreamOpens(t *testing.T) {
	service := newTestPrimaryService()
	addr := startTestPrimary(t, service)

	pool := leastConnPool(failover.Policy{Cooldown: time.Minute},
		&Target{Name: testFirstTargetName, Conn: newTestClientConn(t, addr)},
		&Target{Name: testSecondTargetName, Conn: newTestClientConn(t, addr)},
	)

	// The first target is busier, so the second one is the single best choice
	// for every call that selects now.
	pool.load.Acquire(0)
	pool.load.Acquire(0)

	const waiting = 6

	selections := make([]targetSelection, 0, waiting)

	for range waiting {
		_, selection := pool.selectTarget(RequestInfo{})
		selections = append(selections, selection)
	}

	for _, selection := range selections {
		opened := pool.openStream(outgoingTestContext(t), selection, testFullMethodBidiStream, nil, nil)
		if opened.err != nil {
			t.Fatalf("open stream: %v", opened.err)
		}

		defer opened.release()
	}

	// Two streams level the pool, the other four are shared.
	if first, second := pool.load.InFlight(0), pool.load.InFlight(1); first != 4 || second != 4 {
		t.Fatalf("open streams = %d and %d, want 4 and 4", first, second)
	}
}

func TestTargetPoolLeastConnSingleTargetIsNotTracked(t *testing.T) {
	pool := leastConnPool(failover.Policy{Cooldown: time.Minute}, &Target{Name: selectorTargetA})

	if pool.load != nil || pool.health != nil || pool.failoverEngaged() {
		t.Fatal("a single target must not be tracked")
	}

	if _, selection := pool.selectTarget(RequestInfo{}); selection.leastConn {
		t.Fatalf("single target selection = %+v, want plain selection", selection)
	}
}

func TestTargetPoolTrackStreamReleasesOnce(t *testing.T) {
	pool := leastConnPool(failover.Policy{}, &Target{Name: selectorTargetA}, &Target{Name: selectorTargetB})
	cancelled := 0

	release := pool.trackStream(1, func() { cancelled++ })
	if got := pool.load.InFlight(1); got != 1 {
		t.Fatalf("open streams = %d, want 1", got)
	}

	release()
	release()

	if got := pool.load.InFlight(1); got != 0 {
		t.Fatalf("open streams after release = %d, want 0", got)
	}

	if cancelled != 2 {
		t.Fatalf("attempt context cancelled %d times, want every release to cancel", cancelled)
	}
}

func TestLeastConnOpenStreamSteersCallsToOtherTarget(t *testing.T) {
	services := []*testPrimaryService{newTestPrimaryService(), newTestPrimaryService()}

	pool := leastConnPool(failover.Policy{Cooldown: time.Minute},
		&Target{Name: testFirstTargetName, Conn: newTestClientConn(t, startTestPrimary(t, services[0]))},
		&Target{Name: testSecondTargetName, Conn: newTestClientConn(t, startTestPrimary(t, services[1]))},
	)
	conn, _ := startFailoverProxy(t, &TargetPools{Primary: pool}, config.Config{})

	// The idle pool selects one of the targets; the stream stays open on it.
	stream := newClientStream(t, conn, testFullMethodBidiStream, genericStreamDesc())
	sendMessage(t, stream, "alpha")
	assertRawMessage(t, recvMessage(t, stream), "bidi:alpha")

	busy := 0
	if pool.load.InFlight(1) == 1 {
		busy = 1
	}

	idle := 1 - busy

	if pool.load.InFlight(busy) != 1 || pool.load.InFlight(idle) != 0 {
		t.Fatalf("open streams = %d and %d, want exactly one", pool.load.InFlight(0), pool.load.InFlight(1))
	}

	assertPrimaryRecorded(t, services[busy].bidiRequests(), "alpha")

	for range 6 {
		if err := invokeUnary(t, conn); err != nil {
			t.Fatalf("unary call failed: %v", err)
		}
	}

	if got := len(services[busy].unaryRequests()); got != 0 {
		t.Fatalf("busy target received %d unary calls, want 0", got)
	}

	if got := len(services[idle].unaryRequests()); got != 6 {
		t.Fatalf("idle target received %d unary calls, want 6", got)
	}

	closeSend(t, stream)
	assertEOF(t, stream)
	waitNoOpenStreams(t, pool)
}

// With round robin the neighbour of an unreachable target carries its share
// as well. least_conn must spread that share over all remaining targets.
func TestLeastConnFailoverSpreadsOverSurvivors(t *testing.T) {
	first := newTestPrimaryService()
	second := newTestPrimaryService()

	policy := testGRPCFailoverPolicy()
	policy.Cooldown = time.Minute

	pool := leastConnPool(policy,
		&Target{Name: testDeadTargetName, Conn: newTestClientConn(t, closedGRPCAddr(t))},
		&Target{Name: testFirstTargetName, Conn: newTestClientConn(t, startTestPrimary(t, first))},
		&Target{Name: testSecondTargetName, Conn: newTestClientConn(t, startTestPrimary(t, second))},
	)
	conn, logs := startFailoverProxy(t, &TargetPools{Primary: pool}, config.Config{})

	const calls = 80

	for range calls {
		if err := invokeUnary(t, conn); err != nil {
			t.Fatalf("call failed despite failover: %v", err)
		}
	}

	// The unreachable target is found once and then stays in cooldown.
	if got := len(logs.find("backend_attempt_failed")); got != 1 {
		t.Fatalf("calls that hit the unreachable target = %d, want exactly one", got)
	}

	// Round robin would give its neighbour two thirds. Here: 40 expected
	// each, 4.5 standard deviation.
	firstCalls, secondCalls := len(first.unaryRequests()), len(second.unaryRequests())
	if firstCalls+secondCalls != calls || firstCalls < 15 || secondCalls < 15 {
		t.Fatalf("survivor calls = %d and %d, want an even split of %d", firstCalls, secondCalls, calls)
	}

	waitNoOpenStreams(t, pool)
}

// Without the cooldown feedback an unreachable target never has an open
// stream and would be the first choice of every call.
func TestLeastConnAvoidsUnreachableTargetWithoutFailover(t *testing.T) {
	service := newTestPrimaryService()

	pool := leastConnPool(failover.Policy{Enabled: false, Cooldown: time.Minute, AttemptTimeout: time.Nanosecond},
		&Target{Name: testDeadTargetName, Conn: newTestClientConn(t, closedGRPCAddr(t))},
		&Target{Name: testLiveTargetName, Conn: newTestClientConn(t, startTestPrimary(t, service))},
	)
	conn, logs := startFailoverProxy(t, &TargetPools{Primary: pool}, config.Config{})

	const calls = 60

	failed := 0

	for range calls {
		err := invokeUnary(t, conn)
		if err == nil {
			continue
		}

		failed++

		if status.Code(err) != codes.Unavailable {
			t.Fatalf("disabled failover must keep the single attempt, got %v", err)
		}

		// The record names the target that was really tried.
		assertLogField(t, lastRecord(t, logs, "grpc_proxy"), "primary_selected", testDeadTargetName)
	}

	// One call finds the target unreachable; all later ones avoid it.
	if got := len(service.unaryRequests()); failed != 1 || got != calls-1 {
		t.Fatalf("failed calls = %d, live calls = %d, want 1 and %d", failed, got, calls-1)
	}

	// The failed attempt is the only sign that the target left the selection.
	if got := logs.find("backend_attempt_failed"); len(got) != 1 {
		t.Fatalf("attempt failure records = %d, want 1: %v", len(got), got)
	}

	record := lastRecord(t, logs, "grpc_proxy")
	assertLogField(t, record, "primary_selected", testLiveTargetName)

	if _, ok := record["primary_attempts"]; ok {
		t.Fatalf("single attempts must not add primary_attempts: %v", record)
	}

	waitNoOpenStreams(t, pool)
}

// The Shadow wait at the end of a call must not count as load on the Primary
// target.
func TestLeastConnPrimarySlotIsFreedBeforeShadowWait(t *testing.T) {
	primaryService := newTestPrimaryService()
	shadowService := newTestPrimaryService()
	shadowService.unaryDelay = 2 * time.Second

	primaryAddr := startTestPrimary(t, primaryService)
	primary := leastConnPool(failover.Policy{Cooldown: time.Minute},
		&Target{Name: testFirstTargetName, Conn: newTestClientConn(t, primaryAddr)},
		&Target{Name: testSecondTargetName, Conn: newTestClientConn(t, primaryAddr)},
	)
	pools := &TargetPools{
		Primary: primary,
		Shadow:  testTargetPool("shadow", newTestClientConn(t, startTestPrimary(t, shadowService))),
	}
	conn, _ := startFailoverProxy(t, pools, config.Config{GRPCShadowTimeout: testShortTimeout})

	done := make(chan error, 1)

	go func() {
		done <- invokeUnary(t, conn)
	}()

	waitNoOpenStreamsAfterPrimary(t, primary, primaryService)

	select {
	case err := <-done:
		t.Fatalf("call returned before the Shadow wait ended (err=%v); the test cannot observe the early release", err)
	default:
	}

	if err := <-done; err != nil {
		t.Fatalf("call failed: %v", err)
	}
}

func waitNoOpenStreamsAfterPrimary(t *testing.T, pool *TargetPool, service *testPrimaryService) {
	t.Helper()

	deadline := time.Now().Add(time.Second)

	for len(service.unaryRequests()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("Primary did not receive the call")
		}

		time.Sleep(time.Millisecond)
	}

	for pool.load.InFlight(0)+pool.load.InFlight(1) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("Primary slot is still held while the call waits for Shadow")
		}

		time.Sleep(time.Millisecond)
	}
}

func TestLeastConnShadowPoolReleasesSlots(t *testing.T) {
	primaryService := newTestPrimaryService()
	shadowFirst := newTestPrimaryService()
	shadowSecond := newTestPrimaryService()

	shadow := leastConnPool(failover.Policy{Cooldown: time.Minute},
		&Target{Name: testFirstTargetName, Conn: newTestClientConn(t, startTestPrimary(t, shadowFirst))},
		&Target{Name: testSecondTargetName, Conn: newTestClientConn(t, startTestPrimary(t, shadowSecond))},
	)
	pools := &TargetPools{
		Primary: testTargetPool("primary", newTestClientConn(t, startTestPrimary(t, primaryService))),
		Shadow:  shadow,
	}
	conn, logs := startFailoverProxy(t, pools, config.Config{GRPCShadowTimeout: testShortTimeout})

	const calls = 40

	for range calls {
		if err := invokeUnary(t, conn); err != nil {
			t.Fatalf("call failed: %v", err)
		}
	}

	waitNoOpenStreams(t, shadow)

	firstCalls, secondCalls := len(shadowFirst.unaryRequests()), len(shadowSecond.unaryRequests())
	if firstCalls+secondCalls != calls || firstCalls == 0 || secondCalls == 0 {
		t.Fatalf("shadow calls = %d and %d, want %d shared by both targets", firstCalls, secondCalls, calls)
	}

	// The record names the Shadow target that was really used.
	if selected := lastRecord(t, logs, "grpc_proxy")["shadow_selected"]; selected != testFirstTargetName && selected != testSecondTargetName {
		t.Fatalf("shadow_selected = %q, want one of the Shadow targets", selected)
	}
}

// A Shadow stream that runs into the Shadow timeout must give its slot back.
func TestLeastConnShadowTimeoutReleasesSlot(t *testing.T) {
	primaryService := newTestPrimaryService()
	shadowService := newTestPrimaryService()
	shadowService.unaryDelay = 400 * time.Millisecond

	shadowAddr := startTestPrimary(t, shadowService)
	shadow := leastConnPool(failover.Policy{Cooldown: time.Minute},
		&Target{Name: testFirstTargetName, Conn: newTestClientConn(t, shadowAddr)},
		&Target{Name: testSecondTargetName, Conn: newTestClientConn(t, shadowAddr)},
	)
	pools := &TargetPools{
		Primary: testTargetPool("primary", newTestClientConn(t, startTestPrimary(t, primaryService))),
		Shadow:  shadow,
	}
	conn, logs := startFailoverProxy(t, pools, config.Config{GRPCShadowTimeout: 50 * time.Millisecond})

	for range 3 {
		if err := invokeUnary(t, conn); err != nil {
			t.Fatalf("call failed: %v", err)
		}
	}

	assertLogField(t, lastRecord(t, logs, "grpc_proxy"), "shadow_skip_reason", shadowSkipReasonTimeout)
	waitNoOpenStreams(t, shadow)
}
