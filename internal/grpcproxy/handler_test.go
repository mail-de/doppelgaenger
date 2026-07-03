package grpcproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"doppelgaenger/internal/config"
)

const (
	testServiceName            = "test.Proxy"
	testMethodUnary            = "Unary"
	testFullMethodUnary        = "/test.Proxy/Unary"
	testFullMethodUnaryError   = "/test.Proxy/UnaryError"
	testFullMethodServerStream = "/test.Proxy/ServerStream"
	testFullMethodClientStream = "/test.Proxy/ClientStream"
	testFullMethodBidiStream   = "/test.Proxy/BidiStream"
	testMetadataAuthorization  = "authorization"
	testMetadataRoute          = "x-route"
	testMetadataTraceparent    = "traceparent"
	testMetadataCustom         = "x-custom"
	testPrimaryTargetName      = "primary"
	testPrimaryHeaderKey       = "primary-header"
	testPrimaryTrailerKey      = "primary-trailer"
	testPrimaryHeaderValue     = "seen"
	testPrimaryTrailerValue    = "done"
	testAuthorizationValue     = "Bearer primary"
	testStaticAuthorization    = "Basic static"
	testDynamicAuthorization   = "Bearer dynamic"
	testTraceparentValue       = "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"
	testCustomValue            = "custom-value"
	testServerStreamOne        = "server:one"
	testServerStreamTwo        = "server:two"
	testServerStreamThree      = "server:three"
	testRemoteAddr             = "127.0.0.1:12345"
	testShortTimeout           = 5 * time.Second
)

func TestPrimaryOnlyUnaryForwardsRequestResponseMetadataAndStatusOK(t *testing.T) {
	service, conn := startPrimaryAndProxy(t)
	ctx := outgoingTestContext(t)

	var (
		header   metadata.MD
		trailer  metadata.MD
		response rawMessage
	)

	err := conn.Invoke(
		ctx,
		testFullMethodUnary,
		rawMessage("request"),
		&response,
		grpc.Header(&header),
		grpc.Trailer(&trailer),
	)
	if err != nil {
		t.Fatalf("expected unary call through proxy to succeed, got %v", err)
	}

	assertRawMessage(t, response, "primary:request")
	assertMetadataValue(t, header, testPrimaryHeaderKey, testPrimaryHeaderValue)
	assertMetadataValue(t, trailer, testPrimaryTrailerKey, testPrimaryTrailerValue)
	assertPrimaryRecorded(t, service.unaryRequests(), "request")
	assertIncomingMetadata(t, service.lastMetadata())
}

func TestPrimaryOnlyUnaryReturnsPrimaryStatusError(t *testing.T) {
	_, conn := startPrimaryAndProxy(t)

	var response rawMessage

	err := conn.Invoke(context.Background(), testFullMethodUnaryError, rawMessage("request"), &response)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected primary PermissionDenied status, got err=%v code=%s", err, status.Code(err))
	}
}

func TestPrimaryOnlyServerStreamingForwardsMessages(t *testing.T) {
	_, conn := startPrimaryAndProxy(t)
	stream := newClientStream(t, conn, testFullMethodServerStream, &grpc.StreamDesc{ServerStreams: true})

	sendAndClose(t, stream, "seed")

	messages := recvAllMessages(t, stream)
	assertMessages(t, messages, []string{testServerStreamOne, testServerStreamTwo, testServerStreamThree})
}

func TestPrimaryOnlyClientStreamingForwardsMessages(t *testing.T) {
	service, conn := startPrimaryAndProxy(t)
	stream := newClientStream(t, conn, testFullMethodClientStream, &grpc.StreamDesc{ClientStreams: true})

	sendMessage(t, stream, "first")
	sendMessage(t, stream, "second")
	closeSend(t, stream)

	response := recvMessage(t, stream)
	assertRawMessage(t, response, "client-count:2")
	assertEOF(t, stream)
	assertPrimaryRecorded(t, service.clientStreamRequests(), "first", "second")
}

func TestPrimaryOnlyBidirectionalStreamingForwardsMessages(t *testing.T) {
	service, conn := startPrimaryAndProxy(t)
	stream := newClientStream(t, conn, testFullMethodBidiStream, genericStreamDesc())

	sendMessage(t, stream, "alpha")
	assertRawMessage(t, recvMessage(t, stream), "bidi:alpha")
	sendMessage(t, stream, "beta")
	assertRawMessage(t, recvMessage(t, stream), "bidi:beta")
	closeSend(t, stream)
	assertEOF(t, stream)
	assertPrimaryRecorded(t, service.bidiRequests(), "alpha", "beta")
}

func TestOutgoingMetadataPreservesSelectedKeys(t *testing.T) {
	service, conn := startPrimaryAndProxy(t)
	ctx := outgoingTestContext(t)

	var response rawMessage
	if err := conn.Invoke(ctx, testFullMethodUnary, rawMessage("request"), &response); err != nil {
		t.Fatalf("expected unary call through proxy to succeed, got %v", err)
	}

	assertIncomingMetadata(t, service.lastMetadata())
}

func TestPrimaryMetadataAddsBearerWithoutMutatingDecision(t *testing.T) {
	decision := Decision{
		PrimaryMetadata: map[string]string{
			testMetadataRoute:         testPrimaryTargetName,
			testMetadataAuthorization: testStaticAuthorization,
		},
	}
	handler := &Handler{
		primaryBearer: staticBearerTokenSource{authorization: testDynamicAuthorization},
	}

	metadataOverlay, err := handler.primaryMetadata(context.Background(), decision)
	if err != nil {
		t.Fatalf("expected primary metadata to succeed, got %v", err)
	}

	if metadataOverlay[testMetadataAuthorization] != testDynamicAuthorization {
		t.Fatalf("expected dynamic bearer authorization, got %#v", metadataOverlay)
	}

	if metadataOverlay[testMetadataRoute] != testPrimaryTargetName {
		t.Fatalf("expected existing primary metadata to be preserved, got %#v", metadataOverlay)
	}

	if decision.PrimaryMetadata[testMetadataAuthorization] != testStaticAuthorization {
		t.Fatalf("expected decision primary metadata not to be mutated, got %#v", decision.PrimaryMetadata)
	}
}

func TestPrimaryMetadataReturnsTokenSourceError(t *testing.T) {
	expectedErr := errors.New("token endpoint unavailable")
	handler := &Handler{primaryBearer: staticBearerTokenSource{err: expectedErr}}

	_, err := handler.primaryMetadata(context.Background(), Decision{})
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected token source error, got %v", err)
	}
}

type testPrimaryService struct {
	mu                   sync.Mutex
	metadataSeen         []metadata.MD
	unarySeen            []rawMessage
	clientStreamSeen     []rawMessage
	bidiSeen             []rawMessage
	serverStreamMessages []rawMessage
	serverStreamDelay    time.Duration
	unaryDelay           time.Duration
	unaryResponsePrefix  string
	headerValue          string
	trailerValue         string
	unaryErr             error
}

type staticBearerTokenSource struct {
	authorization string
	err           error
}

func (s staticBearerTokenSource) Authorization(context.Context) (string, error) {
	if s.err != nil {
		return "", s.err
	}

	return s.authorization, nil
}

func newTestPrimaryService() *testPrimaryService {
	return &testPrimaryService{
		serverStreamMessages: []rawMessage{
			rawMessage(testServerStreamOne),
			rawMessage(testServerStreamTwo),
			rawMessage(testServerStreamThree),
		},
		unaryResponsePrefix: "primary:",
		headerValue:         testPrimaryHeaderValue,
		trailerValue:        testPrimaryTrailerValue,
	}
}

func (s *testPrimaryService) handleUnary(ctx context.Context, req rawMessage) (rawMessage, error) {
	s.recordMetadata(ctx)
	s.recordUnary(req)

	if s.unaryDelay > 0 {
		time.Sleep(s.unaryDelay)
	}

	if s.unaryErr != nil {
		return nil, s.unaryErr
	}

	_ = grpc.SetHeader(ctx, metadata.Pairs(testPrimaryHeaderKey, s.headerValue))
	if err := grpc.SetTrailer(ctx, metadata.Pairs(testPrimaryTrailerKey, s.trailerValue)); err != nil {
		return nil, err
	}

	return rawMessage(s.unaryResponsePrefix + string(req)), nil
}

func (s *testPrimaryService) handleUnaryError(ctx context.Context, req rawMessage) (rawMessage, error) {
	s.recordMetadata(ctx)
	s.recordUnary(req)

	return nil, status.Error(codes.PermissionDenied, "primary denied")
}

func (s *testPrimaryService) recordMetadata(ctx context.Context) {
	md, _ := metadata.FromIncomingContext(ctx)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.metadataSeen = append(s.metadataSeen, cloneMetadata(md))
}

func (s *testPrimaryService) recordUnary(msg rawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.unarySeen = append(s.unarySeen, cloneRawMessage(msg))
}

func (s *testPrimaryService) recordClientStream(msg rawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.clientStreamSeen = append(s.clientStreamSeen, cloneRawMessage(msg))
}

func (s *testPrimaryService) recordBidi(msg rawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.bidiSeen = append(s.bidiSeen, cloneRawMessage(msg))
}

func (s *testPrimaryService) lastMetadata() metadata.MD {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.metadataSeen) == 0 {
		return nil
	}

	return cloneMetadata(s.metadataSeen[len(s.metadataSeen)-1])
}

func (s *testPrimaryService) unaryRequests() []rawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()

	return cloneTestMessages(s.unarySeen)
}

func (s *testPrimaryService) clientStreamRequests() []rawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()

	return cloneTestMessages(s.clientStreamSeen)
}

func (s *testPrimaryService) bidiRequests() []rawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()

	return cloneTestMessages(s.bidiSeen)
}

func cloneTestMessages(messages []rawMessage) []rawMessage {
	out := make([]rawMessage, 0, len(messages))
	for _, msg := range messages {
		out = append(out, cloneRawMessage(msg))
	}

	return out
}

type testPrimaryServiceInterface interface{}

var testServiceDesc = grpc.ServiceDesc{
	ServiceName: testServiceName,
	HandlerType: (*testPrimaryServiceInterface)(nil),
	Methods: []grpc.MethodDesc{
		{MethodName: testMethodUnary, Handler: unaryTestHandler},
		{MethodName: "UnaryError", Handler: unaryErrorTestHandler},
	},
	Streams: []grpc.StreamDesc{
		{StreamName: "ServerStream", Handler: serverStreamTestHandler, ServerStreams: true},
		{StreamName: "ClientStream", Handler: clientStreamTestHandler, ClientStreams: true},
		{StreamName: "BidiStream", Handler: bidiStreamTestHandler, ServerStreams: true, ClientStreams: true},
	},
}

//nolint:revive // grpc.MethodDesc handlers require this signature.
func unaryTestHandler(srv any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
	var req rawMessage
	if err := dec(&req); err != nil {
		return nil, err
	}

	return srv.(*testPrimaryService).handleUnary(ctx, req)
}

//nolint:revive // grpc.MethodDesc handlers require this signature.
func unaryErrorTestHandler(srv any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
	var req rawMessage
	if err := dec(&req); err != nil {
		return nil, err
	}

	return srv.(*testPrimaryService).handleUnaryError(ctx, req)
}

func serverStreamTestHandler(srv any, stream grpc.ServerStream) error {
	service := srv.(*testPrimaryService)
	service.recordMetadata(stream.Context())

	var req rawMessage
	if err := stream.RecvMsg(&req); err != nil {
		return err
	}

	_ = stream.SendHeader(metadata.Pairs(testPrimaryHeaderKey, service.headerValue))
	for _, msg := range service.serverStreamMessages {
		if service.serverStreamDelay > 0 {
			time.Sleep(service.serverStreamDelay)
		}

		if err := stream.SendMsg(cloneRawMessage(msg)); err != nil {
			return err
		}
	}

	stream.SetTrailer(metadata.Pairs(testPrimaryTrailerKey, service.trailerValue))

	return nil
}

func clientStreamTestHandler(srv any, stream grpc.ServerStream) error {
	service := srv.(*testPrimaryService)
	service.recordMetadata(stream.Context())

	for {
		var msg rawMessage
		if err := stream.RecvMsg(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return stream.SendMsg(rawMessage(fmt.Sprintf("client-count:%d", len(service.clientStreamRequests()))))
			}

			return err
		}

		service.recordClientStream(msg)
	}
}

func bidiStreamTestHandler(srv any, stream grpc.ServerStream) error {
	service := srv.(*testPrimaryService)
	service.recordMetadata(stream.Context())

	for {
		var msg rawMessage
		if err := stream.RecvMsg(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}

			return err
		}

		service.recordBidi(msg)

		if err := stream.SendMsg(rawMessage("bidi:" + string(msg))); err != nil {
			return err
		}
	}
}

func startPrimaryAndProxy(t *testing.T) (*testPrimaryService, *grpc.ClientConn) {
	t.Helper()

	service := newTestPrimaryService()
	primaryAddr := startTestPrimary(t, service)
	primaryConn := newTestClientConn(t, primaryAddr)
	proxyAddr := startTestProxy(t, primaryConn)
	proxyConn := newTestClientConn(t, proxyAddr)

	return service, proxyConn
}

func startTestPrimary(t *testing.T, service *testPrimaryService) string {
	t.Helper()

	listener := newLocalListener(t)
	server := grpc.NewServer(grpc.ForceServerCodec(rawCodec{}))
	server.RegisterService(&testServiceDesc, service)

	go serveTestGRPC(t, server, listener)

	t.Cleanup(func() {
		server.Stop()

		_ = listener.Close()
	})

	return listener.Addr().String()
}

func startTestProxy(t *testing.T, primaryConn *grpc.ClientConn) string {
	t.Helper()

	handler := testProxyHandler(primaryConn)

	return startTestProxyWithHandler(t, handler)
}

func startTestProxyWithHandler(t *testing.T, handler *Handler) string {
	t.Helper()

	listener := newLocalListener(t)
	server := grpc.NewServer(
		grpc.ForceServerCodec(rawCodec{}),
		grpc.UnknownServiceHandler(handler.Handle),
	)

	go serveTestGRPC(t, server, listener)

	t.Cleanup(func() {
		server.Stop()

		_ = listener.Close()
	})

	return listener.Addr().String()
}

func testProxyHandler(primaryConn *grpc.ClientConn) *Handler {
	resolver := &Resolver{}
	pools := &TargetPools{
		Primary: &TargetPool{
			mode: selectionModeRoundRobin,
			targets: []*Target{
				{Name: testPrimaryTargetName, Conn: primaryConn},
			},
		},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))

	return NewHandler(config.Config{ShadowSamplePercent: 0}, resolver, pools, logger, nil, nil)
}

func newLocalListener(t *testing.T) net.Listener {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on loopback: %v", err)
	}

	return listener
}

func serveTestGRPC(t *testing.T, server *grpc.Server, listener net.Listener) {
	t.Helper()

	if err := server.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		t.Errorf("test gRPC server failed: %v", err)
	}
}

func newTestClientConn(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()

	conn, err := grpc.NewClient(
		addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(rawCodec{})),
	)
	if err != nil {
		t.Fatalf("failed to create gRPC client for %s: %v", addr, err)
	}

	t.Cleanup(func() {
		_ = conn.Close()
	})

	return conn
}

func newClientStream(t *testing.T, conn *grpc.ClientConn, fullMethod string, desc *grpc.StreamDesc) grpc.ClientStream {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), testShortTimeout)
	t.Cleanup(cancel)

	stream, err := conn.NewStream(ctx, desc, fullMethod, grpc.ForceCodec(rawCodec{}))
	if err != nil {
		t.Fatalf("failed to create stream %s: %v", fullMethod, err)
	}

	return stream
}

func outgoingTestContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), testShortTimeout)
	t.Cleanup(cancel)

	return metadata.AppendToOutgoingContext(
		ctx,
		testMetadataAuthorization, testAuthorizationValue,
		testMetadataTraceparent, testTraceparentValue,
		testMetadataCustom, testCustomValue,
	)
}

func sendAndClose(t *testing.T, stream grpc.ClientStream, message string) {
	t.Helper()

	sendMessage(t, stream, message)
	closeSend(t, stream)
}

func sendMessage(t *testing.T, stream grpc.ClientStream, message string) {
	t.Helper()

	if err := stream.SendMsg(rawMessage(message)); err != nil {
		t.Fatalf("failed to send message %q: %v", message, err)
	}
}

func closeSend(t *testing.T, stream grpc.ClientStream) {
	t.Helper()

	if err := stream.CloseSend(); err != nil {
		t.Fatalf("failed to close send: %v", err)
	}
}

func recvMessage(t *testing.T, stream grpc.ClientStream) rawMessage {
	t.Helper()

	var msg rawMessage
	if err := stream.RecvMsg(&msg); err != nil {
		t.Fatalf("failed to receive message: %v", err)
	}

	return msg
}

func recvAllMessages(t *testing.T, stream grpc.ClientStream) []rawMessage {
	t.Helper()

	var messages []rawMessage

	for {
		var msg rawMessage

		err := stream.RecvMsg(&msg)
		if errors.Is(err, io.EOF) {
			return messages
		}

		if err != nil {
			t.Fatalf("failed to receive stream message: %v", err)
		}

		messages = append(messages, cloneRawMessage(msg))
	}
}

func assertEOF(t *testing.T, stream grpc.ClientStream) {
	t.Helper()

	var msg rawMessage
	if err := stream.RecvMsg(&msg); !errors.Is(err, io.EOF) {
		t.Fatalf("expected stream EOF, got msg=%q err=%v", string(msg), err)
	}
}

func assertRawMessage(t *testing.T, actual rawMessage, expected string) {
	t.Helper()

	if string(actual) != expected {
		t.Fatalf("expected message %q, got %q", expected, string(actual))
	}
}

func assertMessages(t *testing.T, actual []rawMessage, expected []string) {
	t.Helper()

	if len(actual) != len(expected) {
		t.Fatalf("expected %d messages, got %d: %#v", len(expected), len(actual), actual)
	}

	for i, msg := range actual {
		assertRawMessage(t, msg, expected[i])
	}
}

func assertPrimaryRecorded(t *testing.T, actual []rawMessage, expected ...string) {
	t.Helper()

	if len(actual) != len(expected) {
		t.Fatalf("expected primary to record %d messages, got %d: %#v", len(expected), len(actual), actual)
	}

	for i, msg := range actual {
		assertRawMessage(t, msg, expected[i])
	}
}

func assertMetadataValue(t *testing.T, md metadata.MD, key string, expected string) {
	t.Helper()

	values := md.Get(key)
	if len(values) != 1 || values[0] != expected {
		t.Fatalf("expected metadata %s=%q, got %#v", key, expected, values)
	}
}

func assertMetadataAbsent(t *testing.T, md metadata.MD, key string) {
	t.Helper()

	if values := md.Get(key); len(values) != 0 {
		t.Fatalf("expected metadata %s to be absent, got %#v", key, values)
	}
}

func assertIncomingMetadata(t *testing.T, md metadata.MD) {
	t.Helper()

	assertMetadataValue(t, md, testMetadataAuthorization, testAuthorizationValue)
	assertMetadataValue(t, md, testMetadataTraceparent, testTraceparentValue)
	assertMetadataValue(t, md, testMetadataCustom, testCustomValue)
}
