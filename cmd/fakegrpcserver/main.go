// Package main starts a generic fake gRPC backend used by blackbox tests.
package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	modeAuto              = "auto"
	modeUnaryEcho         = "unary-echo"
	modeServerStreamCount = "server-stream-count"
	modeClientStreamCount = "client-stream-count"
	modeBidiEcho          = "bidi-echo"
)

type rawMessage []byte

type rawCodec struct{}

func (rawCodec) Name() string {
	return "proto"
}

func (rawCodec) Marshal(v any) ([]byte, error) {
	switch msg := v.(type) {
	case rawMessage:
		return []byte(msg), nil
	case *rawMessage:
		if msg == nil {
			return nil, fmt.Errorf("unsupported nil raw message")
		}

		return []byte(*msg), nil
	default:
		return nil, fmt.Errorf("unsupported raw message type %T", v)
	}
}

func (rawCodec) Unmarshal(data []byte, v any) error {
	msg, ok := v.(*rawMessage)
	if !ok {
		return fmt.Errorf("unsupported raw message target %T", v)
	}

	*msg = append((*msg)[:0], data...)

	return nil
}

type stringSlice []string

func (s *stringSlice) String() string {
	return strings.Join(*s, ",")
}

func (s *stringSlice) Set(value string) error {
	*s = append(*s, value)

	return nil
}

type config struct {
	addr              string
	mode              string
	status            codes.Code
	statusMessage     string
	statusMetadataKey string
	delayMetadataKey  string
	responsePrefix    string
	streamCount       int
	header            metadata.MD
	trailer           metadata.MD
	logMetadataKeys   []string
	tlsCert           string
	tlsKey            string
	clientCA          string
	requireClientCert bool
}

type fakeFlagValues struct {
	headers      stringSlice
	trailers     stringSlice
	metadataKeys stringSlice
}

type server struct {
	cfg config
	mu  sync.Mutex
	enc *json.Encoder
}

type logEntry struct {
	Event                string              `json:"event"`
	FullMethod           string              `json:"full_method"`
	Service              string              `json:"service"`
	Method               string              `json:"method"`
	Mode                 string              `json:"mode"`
	Metadata             map[string][]string `json:"metadata"`
	TraceMetadata        map[string][]string `json:"trace_metadata"`
	MessageCount         int                 `json:"message_count"`
	ResponseMessageCount int                 `json:"response_message_count"`
	Status               string              `json:"status"`
	StatusMessage        string              `json:"status_message,omitempty"`
}

func main() {
	cfg, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(2)
	}

	listener, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen %s: %v\n", cfg.addr, err)
		os.Exit(1)
	}

	options := []grpc.ServerOption{
		grpc.ForceServerCodec(rawCodec{}),
		grpc.UnknownServiceHandler((&server{cfg: cfg, enc: json.NewEncoder(os.Stdout)}).handle),
	}

	creds, err := serverCredentials(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "configure TLS: %v\n", err)
		os.Exit(1)
	}

	if creds != nil {
		options = append(options, grpc.Creds(creds))
	}

	grpcServer := grpc.NewServer(options...)

	fmt.Fprintf(os.Stderr, "fakegrpcserver listening on %s mode=%s\n", listener.Addr().String(), cfg.mode)

	if err := grpcServer.Serve(listener); err != nil {
		fmt.Fprintf(os.Stderr, "serve gRPC: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags(args []string) (config, error) {
	cfg := config{status: codes.OK, responsePrefix: "", streamCount: 3}
	values := fakeFlagValues{}
	fs := flag.NewFlagSet("fakegrpcserver", flag.ContinueOnError)

	registerFakeFlags(fs, &cfg, &values)

	if err := fs.Parse(args); err != nil {
		return config{}, err
	}

	return normalizeFakeConfig(cfg, values)
}

func registerFakeFlags(fs *flag.FlagSet, cfg *config, values *fakeFlagValues) {
	fs.StringVar(&cfg.addr, "listen", "127.0.0.1:9445", "listen address")
	fs.StringVar(&cfg.mode, "mode", modeAuto, "mode: auto, unary-echo, server-stream-count, client-stream-count, bidi-echo")
	fs.Func("status-code", "gRPC status code name or number", func(value string) error {
		code, err := parseCode(value)
		if err != nil {
			return err
		}

		cfg.status = code

		return nil
	})
	fs.StringVar(&cfg.statusMessage, "status-message", "", "gRPC status message")
	fs.StringVar(&cfg.statusMetadataKey, "status-metadata-key", "x-fake-status", "metadata key that overrides the configured status")
	fs.StringVar(&cfg.delayMetadataKey, "delay-metadata-key", "x-fake-delay", "metadata key with a Go duration to sleep before responding")
	fs.StringVar(&cfg.responsePrefix, "response-prefix", "", "prefix added to response payloads")
	fs.IntVar(&cfg.streamCount, "stream-count", 3, "number of server-stream responses")
	fs.Var(&values.headers, "header", "response header metadata in key=value form")
	fs.Var(&values.trailers, "trailer", "response trailer metadata in key=value form")
	fs.Var(&values.metadataKeys, "log-metadata-key", "request metadata key to include in JSONL logs")
	fs.StringVar(&cfg.tlsCert, "tls-cert", "", "server TLS certificate")
	fs.StringVar(&cfg.tlsKey, "tls-key", "", "server TLS key")
	fs.StringVar(&cfg.clientCA, "client-ca", "", "client CA for mTLS")
	fs.BoolVar(&cfg.requireClientCert, "require-client-cert", false, "require and verify client certificates")
}

func normalizeFakeConfig(cfg config, values fakeFlagValues) (config, error) {
	mode := strings.ToLower(strings.TrimSpace(cfg.mode))
	switch mode {
	case modeAuto, modeUnaryEcho, modeServerStreamCount, modeClientStreamCount, modeBidiEcho:
		cfg.mode = mode
	default:
		return config{}, fmt.Errorf("unsupported mode %q", cfg.mode)
	}

	if cfg.streamCount < 0 {
		return config{}, fmt.Errorf("stream-count must not be negative")
	}

	headerMD, err := parseMetadataPairs(values.headers)
	if err != nil {
		return config{}, fmt.Errorf("header: %w", err)
	}

	trailerMD, err := parseMetadataPairs(values.trailers)
	if err != nil {
		return config{}, fmt.Errorf("trailer: %w", err)
	}

	cfg.header = headerMD
	cfg.trailer = trailerMD
	cfg.logMetadataKeys = normalizeKeys(values.metadataKeys)

	return cfg, nil
}

func (s *server) handle(_ any, stream grpc.ServerStream) error {
	fullMethod, _ := grpc.MethodFromServerStream(stream)
	requestMD, _ := metadata.FromIncomingContext(stream.Context())
	code := s.statusFor(requestMD)
	delay := s.delayFor(requestMD)
	result := s.newLogEntry(fullMethod, requestMD, code)

	defer func() {
		s.writeLog(result)
	}()

	if len(s.cfg.header) > 0 {
		if err := stream.SendHeader(cloneMetadata(s.cfg.header)); err != nil {
			return err
		}
	}

	if delay > 0 {
		time.Sleep(delay)
	}

	requests, responses, err := s.handleMode(stream, result.Mode, code, result.StatusMessage)
	result.MessageCount = requests
	result.ResponseMessageCount = responses

	return err
}

func (s *server) newLogEntry(fullMethod string, requestMD metadata.MD, code codes.Code) logEntry {
	service, method := splitFullMethod(fullMethod)
	statusMessage := s.cfg.statusMessage

	if statusMessage == "" {
		statusMessage = code.String()
	}

	return logEntry{
		Event:         "fake_grpc",
		FullMethod:    fullMethod,
		Service:       service,
		Method:        method,
		Mode:          s.modeFor(method),
		Metadata:      metadataExcerpt(requestMD, s.cfg.logMetadataKeys),
		TraceMetadata: metadataExcerpt(requestMD, []string{"traceparent", "tracestate", "x-trace-id"}),
		Status:        code.String(),
		StatusMessage: statusMessage,
	}
}

func (s *server) handleMode(stream grpc.ServerStream, mode string, code codes.Code, statusMessage string) (int, int, error) {
	switch mode {
	case modeServerStreamCount:
		return s.serverStreamCount(stream, code, statusMessage)
	case modeClientStreamCount:
		return s.clientStreamCount(stream, code, statusMessage)
	case modeBidiEcho:
		return s.bidiEcho(stream, code, statusMessage)
	default:
		return s.unaryEcho(stream, code, statusMessage)
	}
}

func (s *server) unaryEcho(stream grpc.ServerStream, code codes.Code, message string) (int, int, error) {
	var req rawMessage
	if err := stream.RecvMsg(&req); err != nil {
		return 0, 0, err
	}

	s.setTrailer(stream)

	if code != codes.OK {
		return 1, 0, status.Error(code, message)
	}

	if err := stream.SendMsg(rawMessage(s.cfg.responsePrefix + string(req))); err != nil {
		return 1, 0, err
	}

	return 1, 1, nil
}

func (s *server) serverStreamCount(stream grpc.ServerStream, code codes.Code, message string) (int, int, error) {
	var seed rawMessage
	if err := stream.RecvMsg(&seed); err != nil {
		return 0, 0, err
	}

	s.setTrailer(stream)

	if code != codes.OK {
		return 1, 0, status.Error(code, message)
	}

	for i := 1; i <= s.cfg.streamCount; i++ {
		payload := fmt.Sprintf("%s%d:%s", s.cfg.responsePrefix, i, string(seed))
		if err := stream.SendMsg(rawMessage(payload)); err != nil {
			return 1, i - 1, err
		}
	}

	return 1, s.cfg.streamCount, nil
}

func (s *server) clientStreamCount(stream grpc.ServerStream, code codes.Code, message string) (int, int, error) {
	requests, err := receiveAll(stream)
	if err != nil {
		return requests, 0, err
	}

	s.setTrailer(stream)

	if code != codes.OK {
		return requests, 0, status.Error(code, message)
	}

	if err := stream.SendMsg(rawMessage(fmt.Sprintf("%scount:%d", s.cfg.responsePrefix, requests))); err != nil {
		return requests, 0, err
	}

	return requests, 1, nil
}

func (s *server) bidiEcho(stream grpc.ServerStream, code codes.Code, message string) (int, int, error) {
	requests := 0
	responses := 0

	for {
		var req rawMessage
		if err := stream.RecvMsg(&req); err != nil {
			if err == io.EOF {
				s.setTrailer(stream)

				if code != codes.OK {
					return requests, responses, status.Error(code, message)
				}

				return requests, responses, nil
			}

			return requests, responses, err
		}

		requests++

		if code == codes.OK {
			if err := stream.SendMsg(rawMessage(s.cfg.responsePrefix + string(req))); err != nil {
				return requests, responses, err
			}

			responses++
		}
	}
}

func receiveAll(stream grpc.ServerStream) (int, error) {
	count := 0

	for {
		var req rawMessage
		if err := stream.RecvMsg(&req); err != nil {
			if err == io.EOF {
				return count, nil
			}

			return count, err
		}

		count++
	}
}

func (s *server) modeFor(method string) string {
	if s.cfg.mode != modeAuto {
		return s.cfg.mode
	}

	switch {
	case strings.Contains(strings.ToLower(method), "serverstream"):
		return modeServerStreamCount
	case strings.Contains(strings.ToLower(method), "clientstream"):
		return modeClientStreamCount
	case strings.Contains(strings.ToLower(method), "bidi"):
		return modeBidiEcho
	default:
		return modeUnaryEcho
	}
}

func (s *server) statusFor(md metadata.MD) codes.Code {
	if s.cfg.statusMetadataKey == "" {
		return s.cfg.status
	}

	values := md.Get(strings.ToLower(s.cfg.statusMetadataKey))
	if len(values) == 0 || values[0] == "" {
		return s.cfg.status
	}

	code, err := parseCode(values[0])
	if err != nil {
		return codes.InvalidArgument
	}

	return code
}

func (s *server) delayFor(md metadata.MD) time.Duration {
	if s.cfg.delayMetadataKey == "" {
		return 0
	}

	values := md.Get(strings.ToLower(s.cfg.delayMetadataKey))
	if len(values) == 0 || values[0] == "" {
		return 0
	}

	delay, err := time.ParseDuration(values[0])
	if err != nil {
		return 0
	}

	return delay
}

func (s *server) setTrailer(stream grpc.ServerStream) {
	if len(s.cfg.trailer) == 0 {
		return
	}

	stream.SetTrailer(cloneMetadata(s.cfg.trailer))
}

func (s *server) writeLog(entry logEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()

	_ = s.enc.Encode(entry)
}

func parseCode(value string) (codes.Code, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return codes.OK, nil
	}

	if n, err := strconv.Atoi(trimmed); err == nil {
		code := codes.Code(n)
		if code.String() == "Code("+trimmed+")" {
			return codes.OK, fmt.Errorf("unsupported gRPC status code %q", value)
		}

		return code, nil
	}

	for code := codes.OK; code <= codes.Unauthenticated; code++ {
		if strings.EqualFold(code.String(), trimmed) {
			return code, nil
		}
	}

	return codes.OK, fmt.Errorf("unsupported gRPC status code %q", value)
}

func parseMetadataPairs(values []string) (metadata.MD, error) {
	md := metadata.MD{}

	for _, value := range values {
		key, item, ok := strings.Cut(value, "=")
		if !ok {
			return nil, fmt.Errorf("%q must use key=value", value)
		}

		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			return nil, fmt.Errorf("%q has an empty key", value)
		}

		md.Append(key, item)
	}

	return md, nil
}

func normalizeKeys(keys []string) []string {
	normalized := make([]string, 0, len(keys))

	for _, key := range keys {
		key = strings.ToLower(strings.TrimSpace(key))
		if key != "" {
			normalized = append(normalized, key)
		}
	}

	return normalized
}

func metadataExcerpt(md metadata.MD, keys []string) map[string][]string {
	out := map[string][]string{}

	for _, key := range keys {
		values := md.Get(key)
		if len(values) == 0 {
			continue
		}

		out[key] = append([]string(nil), values...)
	}

	return out
}

func cloneMetadata(md metadata.MD) metadata.MD {
	if len(md) == 0 {
		return nil
	}

	out := metadata.MD{}
	for key, values := range md {
		out[key] = append([]string(nil), values...)
	}

	return out
}

func splitFullMethod(fullMethod string) (string, string) {
	service, method, ok := strings.Cut(strings.TrimPrefix(fullMethod, "/"), "/")
	if !ok {
		return "", ""
	}

	return service, method
}

func serverCredentials(cfg config) (credentials.TransportCredentials, error) {
	if cfg.tlsCert == "" && cfg.tlsKey == "" {
		if cfg.clientCA != "" || cfg.requireClientCert {
			return nil, fmt.Errorf("mTLS requires -tls-cert and -tls-key")
		}

		return nil, nil
	}

	if cfg.tlsCert == "" || cfg.tlsKey == "" {
		return nil, fmt.Errorf("TLS requires both -tls-cert and -tls-key")
	}

	certificate, err := tls.LoadX509KeyPair(cfg.tlsCert, cfg.tlsKey)
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"h2"},
	}

	if cfg.clientCA != "" {
		caBytes, err := os.ReadFile(cfg.clientCA)
		if err != nil {
			return nil, err
		}

		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caBytes) {
			return nil, fmt.Errorf("failed to parse client CA PEM")
		}

		tlsConfig.ClientCAs = pool
	}

	if cfg.requireClientCert {
		tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
	}

	return credentials.NewTLS(tlsConfig), nil
}
