package grpcproxy

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"doppelgaenger/internal/config"
)

const (
	testShadowForceMetadata = "x-shadow"
	testShadowTimeout       = 750 * time.Millisecond
	testOverlayBackendKey   = "X-Backend"
	testOverlayYesValue     = "yes"
)

func TestShadowDecisionSamplingZeroWithoutForceSkipsShadow(t *testing.T) {
	handler := testShadowDecisionHandler(config.Config{ShadowSamplePercent: 0}, nil)

	decision := handler.shouldShadow(context.Background(), Decision{ShadowMode: ShadowModeInherit})
	if decision.doShadow {
		t.Fatalf("expected sampling 0 without force to skip shadow")
	}
}

func TestShadowDecisionSamplingHundredStartsShadow(t *testing.T) {
	handler := testShadowDecisionHandler(config.Config{ShadowSamplePercent: 100}, nil)

	decision := handler.shouldShadow(context.Background(), Decision{ShadowMode: ShadowModeInherit})
	if !decision.doShadow {
		t.Fatalf("expected sampling 100 to start shadow")
	}
}

func TestShadowDecisionForceMetadataStartsAllowedRule(t *testing.T) {
	handler := testShadowDecisionHandler(config.Config{
		ShadowSamplePercent:     0,
		GRPCShadowForceMetadata: testShadowForceMetadata,
	}, nil)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(testShadowForceMetadata, "yes"))

	decision := handler.shouldShadow(ctx, Decision{ShadowMode: ShadowModeAuto})
	if !decision.doShadow || !decision.forced {
		t.Fatalf("expected force metadata to start forced shadow, got %#v", decision)
	}
}

func TestShadowDecisionNeverBlocksForceMetadata(t *testing.T) {
	handler := testShadowDecisionHandler(config.Config{
		ShadowSamplePercent:     100,
		GRPCShadowForceMetadata: testShadowForceMetadata,
	}, nil)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(testShadowForceMetadata, "yes"))

	decision := handler.shouldShadow(ctx, Decision{ShadowMode: ShadowModeNever})
	if decision.doShadow {
		t.Fatalf("expected shadow never to block forced shadow")
	}

	if !decision.forced {
		t.Fatalf("expected force metadata detection to remain visible")
	}
}

func TestShadowDecisionRateLimiterBlocksNonForcedShadow(t *testing.T) {
	handler := testShadowDecisionHandler(config.Config{ShadowSamplePercent: 100}, staticLimiter(false))

	decision := handler.shouldShadow(context.Background(), Decision{ShadowMode: ShadowModeAlways})
	if decision.doShadow {
		t.Fatalf("expected rate limiter to block non-forced shadow")
	}

	if decision.skipReason != shadowSkipReasonRateLimited {
		t.Fatalf("expected rate_limited reason, got %q", decision.skipReason)
	}
}

func TestShadowTargetMissingKeepsPrimaryResponseAndLogsReason(t *testing.T) {
	primaryService := newTestPrimaryService()
	primaryConn := newTestClientConn(t, startTestPrimary(t, primaryService))
	logger, logs := newCaptureLogger()
	handler := testProxyHandlerWithPools(
		testShadowConfig(100),
		catchAllShadowResolver(t),
		&TargetPools{
			Primary: testTargetPool("primary", primaryConn),
		},
		logger,
		nil,
	)
	conn := newTestClientConn(t, startTestProxyWithHandler(t, handler))

	var response rawMessage
	if err := conn.Invoke(context.Background(), testFullMethodUnary, rawMessage("request"), &response); err != nil {
		t.Fatalf("expected primary response despite missing shadow target, got %v", err)
	}

	assertRawMessage(t, response, "primary:request")
	assertLogField(t, logs.last(), "shadow_skip_reason", shadowSkipReasonTargetUnavailable)
	assertLogField(t, logs.last(), "shadow_started", "false")
}

func TestShadowStreamCreationFailureKeepsPrimaryResponseAndLogsError(t *testing.T) {
	primaryService := newTestPrimaryService()
	primaryConn := newTestClientConn(t, startTestPrimary(t, primaryService))

	shadowConn := newTestClientConn(t, startTestPrimary(t, newTestPrimaryService()))
	if err := shadowConn.Close(); err != nil {
		t.Fatalf("failed to close shadow connection: %v", err)
	}

	logger, logs := newCaptureLogger()
	handler := testProxyHandlerWithPools(
		testShadowConfig(100),
		catchAllShadowResolver(t),
		&TargetPools{
			Primary: testTargetPool("primary", primaryConn),
			Shadow:  testTargetPool("shadow", shadowConn),
		},
		logger,
		nil,
	)
	conn := newTestClientConn(t, startTestProxyWithHandler(t, handler))

	var response rawMessage
	if err := conn.Invoke(context.Background(), testFullMethodUnary, rawMessage("request"), &response); err != nil {
		t.Fatalf("expected primary response despite shadow stream creation failure, got %v", err)
	}

	assertRawMessage(t, response, "primary:request")

	record := logs.last()
	if record["shadow_err"] == "" {
		t.Fatalf("expected visible shadow error, got log record %#v", record)
	}

	if record["shadow_started"] != "false" {
		t.Fatalf("expected shadow_started=false, got log record %#v", record)
	}
}

func TestShadowBackendErrorStatusIsCollectedWithoutChangingPrimaryStatus(t *testing.T) {
	primaryService := newTestPrimaryService()
	shadowService := newTestPrimaryService()
	shadowService.unaryErr = status.Error(codes.ResourceExhausted, "shadow exhausted")
	conn, logs := startPrimaryShadowProxy(t, primaryService, shadowService, testShadowConfig(100))

	var response rawMessage
	if err := conn.Invoke(context.Background(), testFullMethodUnary, rawMessage("request"), &response); err != nil {
		t.Fatalf("expected primary response despite shadow status error, got %v", err)
	}

	assertRawMessage(t, response, "primary:request")
	assertLogField(t, logs.last(), "primary_status", codes.OK.String())
	assertLogField(t, logs.last(), "shadow_status", codes.ResourceExhausted.String())
}

func TestMetadataOverlaysAreBackendSpecificAndImmutable(t *testing.T) {
	primaryService := newTestPrimaryService()
	shadowService := newTestPrimaryService()
	primaryOverlay := map[string]string{
		testOverlayBackendKey: testPrimaryTargetName,
		"X-Primary-Only":      testOverlayYesValue,
	}
	shadowOverlay := map[string]string{
		testOverlayBackendKey: "shadow",
		"X-Shadow-Only":       testOverlayYesValue,
	}
	resolver := mustResolver(t, []config.GRPCRule{
		{
			Service:         "*",
			Shadow:          string(ShadowModeAlways),
			Compare:         string(CompareDecisionOn),
			PrimaryMetadata: primaryOverlay,
			ShadowMetadata:  shadowOverlay,
		},
	})
	conn, _ := startPrimaryShadowProxyWithResolver(t, primaryService, shadowService, testShadowConfig(100), resolver)

	baseCtx, cancel := context.WithTimeout(context.Background(), testShortTimeout)
	t.Cleanup(cancel)

	incoming := metadata.Pairs(
		"X-Backend", "incoming",
		testMetadataTraceparent, testTraceparentValue,
		testMetadataCustom, testCustomValue,
	)
	ctx := metadata.NewOutgoingContext(baseCtx, incoming)

	var response rawMessage
	if err := conn.Invoke(ctx, testFullMethodUnary, rawMessage("request"), &response); err != nil {
		t.Fatalf("expected primary response with metadata overlays, got %v", err)
	}

	primaryMetadata := primaryService.lastMetadata()
	shadowMetadata := shadowService.lastMetadata()

	assertMetadataValue(t, primaryMetadata, "x-backend", "primary")
	assertMetadataValue(t, primaryMetadata, "x-primary-only", testOverlayYesValue)
	assertMetadataAbsent(t, primaryMetadata, "x-shadow-only")
	assertMetadataValue(t, shadowMetadata, "x-backend", "shadow")
	assertMetadataValue(t, shadowMetadata, "x-shadow-only", testOverlayYesValue)
	assertMetadataAbsent(t, shadowMetadata, "x-primary-only")
	assertMetadataValue(t, primaryMetadata, testMetadataTraceparent, testTraceparentValue)
	assertMetadataValue(t, shadowMetadata, testMetadataTraceparent, testTraceparentValue)

	if incoming.Get("x-backend")[0] != "incoming" {
		t.Fatalf("expected incoming metadata to remain unchanged, got %#v", incoming)
	}

	if _, ok := primaryOverlay["x-backend"]; ok {
		t.Fatalf("expected primary overlay config map not to be normalized in place, got %#v", primaryOverlay)
	}

	if _, ok := shadowOverlay["x-backend"]; ok {
		t.Fatalf("expected shadow overlay config map not to be normalized in place, got %#v", shadowOverlay)
	}
}

func TestCompareOffLogsSkipped(t *testing.T) {
	primaryService := newTestPrimaryService()
	shadowService := newTestPrimaryService()
	resolver := mustResolver(t, []config.GRPCRule{
		{
			Service: "*",
			Shadow:  string(ShadowModeAlways),
			Compare: string(CompareDecisionOff),
		},
	})
	conn, logs := startPrimaryShadowProxyWithResolver(t, primaryService, shadowService, testShadowConfig(100), resolver)

	var response rawMessage
	if err := conn.Invoke(context.Background(), testFullMethodUnary, rawMessage("request"), &response); err != nil {
		t.Fatalf("expected primary response with compare off, got %v", err)
	}

	record := logs.last()
	assertLogField(t, record, "compare_outcome", grpcCompareOutcomeSkipped)
	assertLogField(t, record, "compare_skip_reason", SkipReasonGRPCRule)
	assertLogField(t, record, "shadow_started", "true")
}

func TestNoShadowLogsCompareSkipped(t *testing.T) {
	primaryService := newTestPrimaryService()
	primaryConn := newTestClientConn(t, startTestPrimary(t, primaryService))
	logger, logs := newCaptureLogger()
	handler := testProxyHandlerWithPools(
		testShadowConfig(0),
		&Resolver{},
		&TargetPools{Primary: testTargetPool("primary", primaryConn)},
		logger,
		nil,
	)
	conn := newTestClientConn(t, startTestProxyWithHandler(t, handler))

	var response rawMessage
	if err := conn.Invoke(context.Background(), testFullMethodUnary, rawMessage("request"), &response); err != nil {
		t.Fatalf("expected primary response with no shadow, got %v", err)
	}

	record := logs.last()
	assertLogField(t, record, "compare_outcome", grpcCompareOutcomeSkipped)
	assertLogField(t, record, "compare_skip_reason", grpcCompareSkipReasonNoShadow)
	assertLogField(t, record, "shadow_started", "false")
}

func TestShadowTimeoutLogsCompareError(t *testing.T) {
	primaryService := newTestPrimaryService()
	shadowService := newTestPrimaryService()
	shadowService.unaryDelay = 200 * time.Millisecond
	cfg := testShadowConfig(100)
	cfg.GRPCShadowTimeout = 20 * time.Millisecond
	conn, logs := startPrimaryShadowProxy(t, primaryService, shadowService, cfg)

	var response rawMessage
	if err := conn.Invoke(context.Background(), testFullMethodUnary, rawMessage("request"), &response); err != nil {
		t.Fatalf("expected primary response despite shadow timeout, got %v", err)
	}

	record := logs.last()
	assertLogField(t, record, "shadow_skip_reason", shadowSkipReasonTimeout)
	assertLogField(t, record, "compare_outcome", grpcCompareOutcomeError)
	assertLogFieldContains(t, record, "compare_err", shadowSkipReasonTimeout)
}

func TestShadowQueueFullIsVisibleAndOfferDoesNotBlock(t *testing.T) {
	shadow := &shadowForwarder{
		queue: make(chan rawMessage, 1),
		done:  make(chan struct{}),
	}
	shadow.Offer(rawMessage("first"))

	start := time.Now()

	shadow.Offer(rawMessage("second"))

	elapsed := time.Since(start)
	if elapsed > 50*time.Millisecond {
		t.Fatalf("expected queue-full offer to return immediately, took %s", elapsed)
	}

	result := shadow.snapshot()
	if result.SkipReason != shadowSkipReasonQueueFull {
		t.Fatalf("expected queue_full reason, got %#v", result)
	}
}

func TestShadowServerStreamingDoesNotDelayPrimaryMessages(t *testing.T) {
	primaryService := newTestPrimaryService()
	shadowService := newTestPrimaryService()
	shadowService.serverStreamDelay = 150 * time.Millisecond
	conn, logs := startPrimaryShadowProxy(t, primaryService, shadowService, testShadowConfig(100))
	stream := newClientStream(t, conn, testFullMethodServerStream, &grpc.StreamDesc{ServerStreams: true})

	sendAndClose(t, stream, "seed")

	start := time.Now()

	assertRawMessage(t, recvMessage(t, stream), testServerStreamOne)

	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("expected primary server-streaming response before shadow delay, took %s", elapsed)
	}

	messages := recvAllMessages(t, stream)
	assertMessages(t, messages, []string{testServerStreamTwo, testServerStreamThree})
	assertLogField(t, logs.last(), "shadow_messages", "3")
}

func TestShadowClientStreamingReceivesRequestMessages(t *testing.T) {
	primaryService := newTestPrimaryService()
	shadowService := newTestPrimaryService()
	conn, _ := startPrimaryShadowProxy(t, primaryService, shadowService, testShadowConfig(100))
	stream := newClientStream(t, conn, testFullMethodClientStream, &grpc.StreamDesc{ClientStreams: true})

	sendMessage(t, stream, "first")
	sendMessage(t, stream, "second")
	closeSend(t, stream)
	assertRawMessage(t, recvMessage(t, stream), "client-count:2")
	assertEOF(t, stream)

	assertPrimaryRecorded(t, primaryService.clientStreamRequests(), "first", "second")
	assertPrimaryRecorded(t, shadowService.clientStreamRequests(), "first", "second")
}

func TestShadowBidirectionalStreamingReceivesRequestMessages(t *testing.T) {
	primaryService := newTestPrimaryService()
	shadowService := newTestPrimaryService()
	conn, _ := startPrimaryShadowProxy(t, primaryService, shadowService, testShadowConfig(100))
	stream := newClientStream(t, conn, testFullMethodBidiStream, genericStreamDesc())

	sendMessage(t, stream, "alpha")
	assertRawMessage(t, recvMessage(t, stream), "bidi:alpha")
	sendMessage(t, stream, "beta")
	assertRawMessage(t, recvMessage(t, stream), "bidi:beta")
	closeSend(t, stream)
	assertEOF(t, stream)

	assertPrimaryRecorded(t, primaryService.bidiRequests(), "alpha", "beta")
	assertPrimaryRecorded(t, shadowService.bidiRequests(), "alpha", "beta")
}

func testShadowDecisionHandler(cfg config.Config, limiter interface{ Allow() bool }) *Handler {
	return NewHandler(cfg, &Resolver{}, nil, discardLogger(), limiter, nil)
}

func testShadowConfig(samplePercent int) config.Config {
	return config.Config{
		ShadowSamplePercent: samplePercent,
		GRPCShadowTimeout:   testShadowTimeout,
		GRPCShadowQueueSize: 8,
	}
}

func catchAllShadowResolver(t *testing.T) *Resolver {
	t.Helper()

	return mustResolver(t, []config.GRPCRule{
		{Service: "*", Shadow: string(ShadowModeAlways)},
	})
}

func startPrimaryShadowProxy(
	t *testing.T,
	primaryService *testPrimaryService,
	shadowService *testPrimaryService,
	cfg config.Config,
) (*grpc.ClientConn, *captureLogHandler) {
	t.Helper()

	return startPrimaryShadowProxyWithResolver(t, primaryService, shadowService, cfg, catchAllShadowResolver(t))
}

func startPrimaryShadowProxyWithResolver(
	t *testing.T,
	primaryService *testPrimaryService,
	shadowService *testPrimaryService,
	cfg config.Config,
	resolver *Resolver,
) (*grpc.ClientConn, *captureLogHandler) {
	t.Helper()

	primaryConn := newTestClientConn(t, startTestPrimary(t, primaryService))
	shadowConn := newTestClientConn(t, startTestPrimary(t, shadowService))
	logger, logs := newCaptureLogger()
	handler := testProxyHandlerWithPools(
		cfg,
		resolver,
		&TargetPools{
			Primary: testTargetPool("primary", primaryConn),
			Shadow:  testTargetPool("shadow", shadowConn),
		},
		logger,
		nil,
	)
	conn := newTestClientConn(t, startTestProxyWithHandler(t, handler))

	return conn, logs
}

func testProxyHandlerWithPools(
	cfg config.Config,
	resolver *Resolver,
	pools *TargetPools,
	logger *slog.Logger,
	limiter interface{ Allow() bool },
) *Handler {
	return NewHandler(cfg, resolver, pools, logger, limiter, nil)
}

func testTargetPool(name string, conn *grpc.ClientConn) *TargetPool {
	return &TargetPool{
		mode: selectionModeRoundRobin,
		targets: []*Target{
			{Name: name, Conn: conn},
		},
	}
}

type staticLimiter bool

func (l staticLimiter) Allow() bool {
	return bool(l)
}

type captureLogHandler struct {
	mu      sync.Mutex
	records []map[string]string
}

func newCaptureLogger() (*slog.Logger, *captureLogHandler) {
	handler := &captureLogHandler{}

	return slog.New(handler), handler
}

func (h *captureLogHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (h *captureLogHandler) Handle(_ context.Context, record slog.Record) error {
	values := map[string]string{
		"msg":   record.Message,
		"level": record.Level.String(),
	}
	record.Attrs(func(attr slog.Attr) bool {
		values[attr.Key] = fmt.Sprint(attr.Value.Any())

		return true
	})

	h.mu.Lock()
	defer h.mu.Unlock()

	h.records = append(h.records, values)

	return nil
}

func (h *captureLogHandler) WithAttrs([]slog.Attr) slog.Handler {
	return h
}

func (h *captureLogHandler) WithGroup(string) slog.Handler {
	return h
}

func (h *captureLogHandler) last() map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.records) == 0 {
		return nil
	}

	record := make(map[string]string, len(h.records[len(h.records)-1]))
	for key, value := range h.records[len(h.records)-1] {
		record[key] = value
	}

	return record
}

func assertLogField(t *testing.T, record map[string]string, key string, expected string) {
	t.Helper()

	if record == nil {
		t.Fatalf("expected log record with %s=%q, got nil", key, expected)
	}

	if actual := record[key]; actual != expected {
		t.Fatalf("expected log field %s=%q, got %q in %#v", key, expected, actual, record)
	}
}

func assertLogFieldContains(t *testing.T, record map[string]string, key string, expected string) {
	t.Helper()

	if record == nil {
		t.Fatalf("expected log record with %s containing %q, got nil", key, expected)
	}

	if actual := record[key]; !strings.Contains(actual, expected) {
		t.Fatalf("expected log field %s to contain %q, got %q in %#v", key, expected, actual, record)
	}
}
