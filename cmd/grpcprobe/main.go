// Package main provides a protobuf-free gRPC probe for Doppelgaenger E2E tests.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	probeUnary        = "unary"
	probeServerStream = "server-stream"
	probeClientStream = "client-stream"
	probeBidi         = "bidi"
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
	addr               string
	method             string
	mode               string
	payloads           []string
	metadata           metadata.MD
	expectStatus       codes.Code
	expectStatusSet    bool
	expectCount        int
	expectCountSet     bool
	expectMessages     []string
	printMetadata      bool
	timeout            time.Duration
	tlsEnabled         bool
	rootCA             string
	serverName         string
	insecureSkipVerify bool
}

type probeFlagValues struct {
	metadataPairs    stringSlice
	payloads         stringSlice
	expectedMessages stringSlice
}

type summary struct {
	Method   string              `json:"method"`
	Mode     string              `json:"mode"`
	Status   string              `json:"status"`
	Messages []string            `json:"messages"`
	Headers  map[string][]string `json:"headers,omitempty"`
	Trailers map[string][]string `json:"trailers,omitempty"`
}

func main() {
	cfg, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(2)
	}

	result, err := run(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintf(os.Stderr, "write result: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags(args []string) (config, error) {
	cfg := config{expectStatus: codes.OK, timeout: 5 * time.Second}
	values := probeFlagValues{}
	fs := flag.NewFlagSet("grpcprobe", flag.ContinueOnError)

	registerProbeFlags(fs, &cfg, &values)

	if err := fs.Parse(args); err != nil {
		return config{}, err
	}

	return normalizeProbeConfig(cfg, values, args, fs)
}

func registerProbeFlags(fs *flag.FlagSet, cfg *config, values *probeFlagValues) {
	fs.StringVar(&cfg.addr, "addr", "127.0.0.1:9444", "gRPC address")
	fs.StringVar(&cfg.method, "method", "", "full gRPC method, for example /pkg.Service/Method")
	fs.StringVar(&cfg.mode, "mode", probeUnary, "mode: unary, server-stream, client-stream, bidi")
	fs.Var(&values.payloads, "payload", "raw payload; may be repeated")
	fs.Var(&values.metadataPairs, "metadata", "metadata in key=value form; may be repeated")
	fs.Func("expect-status", "expected gRPC status code name or number", func(value string) error {
		code, err := parseCode(value)
		if err != nil {
			return err
		}

		cfg.expectStatus = code
		cfg.expectStatusSet = true

		return nil
	})
	fs.IntVar(&cfg.expectCount, "expect-count", 0, "expected response message count")
	fs.Var(&values.expectedMessages, "expect-message", "expected response message; may be repeated")
	fs.BoolVar(&cfg.printMetadata, "print-metadata", false, "include response headers and trailers in JSON output")
	fs.DurationVar(&cfg.timeout, "timeout", 5*time.Second, "RPC timeout")
	fs.BoolVar(&cfg.tlsEnabled, "tls", false, "use TLS")
	fs.StringVar(&cfg.rootCA, "root-ca", "", "TLS root CA")
	fs.StringVar(&cfg.serverName, "server-name", "", "TLS server name")
	fs.BoolVar(&cfg.insecureSkipVerify, "insecure-skip-verify", false, "skip TLS verification")
}

func normalizeProbeConfig(cfg config, values probeFlagValues, args []string, fs *flag.FlagSet) (config, error) {
	cfg.payloads = append([]string(nil), values.payloads...)
	cfg.expectMessages = append([]string(nil), values.expectedMessages...)
	cfg.expectCountSet = fs.Lookup("expect-count").Value.String() != "0" || containsArg(args, "-expect-count")

	mode := strings.ToLower(strings.TrimSpace(cfg.mode))
	switch mode {
	case probeUnary, probeServerStream, probeClientStream, probeBidi:
		cfg.mode = mode
	default:
		return config{}, fmt.Errorf("unsupported mode %q", cfg.mode)
	}

	if strings.TrimSpace(cfg.addr) == "" {
		return config{}, fmt.Errorf("addr must not be empty")
	}

	if !strings.HasPrefix(cfg.method, "/") {
		return config{}, fmt.Errorf("method must be a full gRPC method starting with /")
	}

	md, err := parseMetadataPairs(values.metadataPairs)
	if err != nil {
		return config{}, err
	}

	cfg.metadata = md

	if len(cfg.payloads) == 0 {
		cfg.payloads = []string{""}
	}

	if cfg.timeout <= 0 {
		return config{}, fmt.Errorf("timeout must be positive")
	}

	return cfg, nil
}

func run(cfg config) (summary, error) {
	creds, err := clientCredentials(cfg)
	if err != nil {
		return summary{}, err
	}

	conn, err := grpc.NewClient(
		cfg.addr,
		grpc.WithTransportCredentials(creds),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(rawCodec{})),
	)
	if err != nil {
		return summary{}, err
	}
	defer func() {
		_ = conn.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()

	if len(cfg.metadata) > 0 {
		ctx = metadata.NewOutgoingContext(ctx, cfg.metadata)
	}

	result, err := call(ctx, conn, cfg)
	if err != nil {
		return summary{}, err
	}

	if err := checkExpectations(cfg, result); err != nil {
		return summary{}, err
	}

	return result, nil
}

func call(ctx context.Context, conn *grpc.ClientConn, cfg config) (summary, error) {
	switch cfg.mode {
	case probeUnary:
		return callUnary(ctx, conn, cfg)
	case probeServerStream:
		return callStream(ctx, conn, cfg, &grpc.StreamDesc{ServerStreams: true, ClientStreams: true})
	case probeClientStream:
		return callStream(ctx, conn, cfg, &grpc.StreamDesc{ClientStreams: true})
	case probeBidi:
		return callStream(ctx, conn, cfg, &grpc.StreamDesc{ServerStreams: true, ClientStreams: true})
	default:
		return summary{}, fmt.Errorf("unsupported mode %q", cfg.mode)
	}
}

func callUnary(ctx context.Context, conn *grpc.ClientConn, cfg config) (summary, error) {
	var header metadata.MD

	var trailer metadata.MD

	var response rawMessage

	err := conn.Invoke(
		ctx,
		cfg.method,
		rawMessage(cfg.payloads[0]),
		&response,
		grpc.Header(&header),
		grpc.Trailer(&trailer),
		grpc.ForceCodec(rawCodec{}),
	)

	code := status.Code(err)
	messages := []string{}

	if err == nil {
		messages = append(messages, string(response))
	}

	result := summary{
		Method:   cfg.method,
		Mode:     cfg.mode,
		Status:   code.String(),
		Messages: messages,
	}

	if cfg.printMetadata {
		result.Headers = cloneMetadata(header)
		result.Trailers = cloneMetadata(trailer)
	}

	return result, nil
}

func callStream(ctx context.Context, conn *grpc.ClientConn, cfg config, desc *grpc.StreamDesc) (summary, error) {
	stream, err := conn.NewStream(ctx, desc, cfg.method, grpc.ForceCodec(rawCodec{}))
	if err != nil {
		return summary{}, err
	}

	for _, payload := range cfg.payloads {
		if err := stream.SendMsg(rawMessage(payload)); err != nil {
			return summary{}, err
		}
	}

	if err := stream.CloseSend(); err != nil {
		return summary{}, err
	}

	header := metadata.MD{}

	if cfg.printMetadata {
		if h, err := stream.Header(); err == nil {
			header = h
		}
	}

	messages := []string{}
	finalCode := codes.OK

	for {
		var response rawMessage

		err := stream.RecvMsg(&response)
		if err == nil {
			messages = append(messages, string(response))

			continue
		}

		if err == io.EOF {
			finalCode = codes.OK
			break
		}

		finalCode = status.Code(err)

		break
	}

	result := summary{
		Method:   cfg.method,
		Mode:     cfg.mode,
		Status:   finalCode.String(),
		Messages: messages,
	}
	if cfg.printMetadata {
		result.Headers = cloneMetadata(header)
		result.Trailers = cloneMetadata(stream.Trailer())
	}

	return result, nil
}

func checkExpectations(cfg config, result summary) error {
	if cfg.expectStatusSet && result.Status != cfg.expectStatus.String() {
		return fmt.Errorf("expected status %s, got %s", cfg.expectStatus.String(), result.Status)
	}

	if cfg.expectCountSet && len(result.Messages) != cfg.expectCount {
		return fmt.Errorf("expected %d response messages, got %d: %q", cfg.expectCount, len(result.Messages), result.Messages)
	}

	if len(cfg.expectMessages) > 0 {
		if len(cfg.expectMessages) != len(result.Messages) {
			return fmt.Errorf("expected messages %q, got %q", cfg.expectMessages, result.Messages)
		}

		for i := range cfg.expectMessages {
			if cfg.expectMessages[i] != result.Messages[i] {
				return fmt.Errorf("expected messages %q, got %q", cfg.expectMessages, result.Messages)
			}
		}
	}

	return nil
}

func parseMetadataPairs(values []string) (metadata.MD, error) {
	md := metadata.MD{}

	for _, value := range values {
		key, item, ok := strings.Cut(value, "=")
		if !ok {
			return nil, fmt.Errorf("metadata %q must use key=value", value)
		}

		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			return nil, fmt.Errorf("metadata %q has an empty key", value)
		}

		md.Append(key, item)
	}

	return md, nil
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

func cloneMetadata(md metadata.MD) map[string][]string {
	if len(md) == 0 {
		return nil
	}

	out := map[string][]string{}
	for key, values := range md {
		out[key] = append([]string(nil), values...)
	}

	return out
}

func clientCredentials(cfg config) (credentials.TransportCredentials, error) {
	if !cfg.tlsEnabled {
		return insecure.NewCredentials(), nil
	}

	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         cfg.serverName,
		InsecureSkipVerify: cfg.insecureSkipVerify,
	}

	if cfg.rootCA != "" {
		caBytes, err := os.ReadFile(cfg.rootCA)
		if err != nil {
			return nil, err
		}

		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caBytes) {
			return nil, fmt.Errorf("failed to parse root CA PEM")
		}

		tlsConfig.RootCAs = pool
	}

	return credentials.NewTLS(tlsConfig), nil
}

func containsArg(args []string, name string) bool {
	for _, arg := range args {
		if arg == name || strings.HasPrefix(arg, name+"=") {
			return true
		}
	}

	return false
}
