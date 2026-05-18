// Package main starts a small OTLP/HTTP collector used by E2E tests.
package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	collectormetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

type collectorState struct {
	mu             sync.Mutex
	traceRequests  int
	metricRequests int
	spans          map[string]int
	traceIDs       map[string]int
	metrics        map[string]int
}

type summary struct {
	TraceRequests  int      `json:"trace_requests"`
	MetricRequests int      `json:"metric_requests"`
	Spans          []string `json:"spans"`
	TraceIDs       []string `json:"trace_ids"`
	Metrics        []string `json:"metrics"`
}

type eventLogger struct {
	mu   sync.Mutex
	file *os.File
}

func main() {
	addr := flag.String("listen", "127.0.0.1:14318", "listen address")
	logFile := flag.String("log-file", "", "optional JSONL log file")

	flag.Parse()

	logger, err := newEventLogger(*logFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open log file: %v\n", err)
		os.Exit(1)
	}
	defer logger.close()

	state := newCollectorState()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthHandler)
	mux.HandleFunc("/summary", state.summaryHandler)
	mux.HandleFunc("/v1/traces", traceHandler(state, logger))
	mux.HandleFunc("/v1/metrics", metricHandler(state, logger))

	server := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "collector failed: %v\n", err)
		os.Exit(1)
	}
}

func newCollectorState() *collectorState {
	return &collectorState{
		spans:    map[string]int{},
		traceIDs: map[string]int{},
		metrics:  map[string]int{},
	}
}

func newEventLogger(path string) (*eventLogger, error) {
	if path == "" {
		return &eventLogger{}, nil
	}

	file, err := os.Create(path)
	if err != nil {
		return nil, err
	}

	return &eventLogger{file: file}, nil
}

func (l *eventLogger) close() {
	if l.file != nil {
		_ = l.file.Close()
	}
}

func (l *eventLogger) write(kind string, payload any) {
	if l.file == nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	entry := map[string]any{
		"kind":    kind,
		"payload": payload,
	}
	_ = json.NewEncoder(l.file).Encode(entry)
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *collectorState) summaryHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(s.snapshot()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func traceHandler(state *collectorState, logger *eventLogger) http.HandlerFunc {
	return collectionHandler("traces", logger, newTraceRequest, newTraceResponse, state.recordTraceMessage)
}

func metricHandler(state *collectorState, logger *eventLogger) http.HandlerFunc {
	return collectionHandler("metrics", logger, newMetricRequest, newMetricResponse, state.recordMetricMessage)
}

func collectionHandler(kind string, logger *eventLogger, newRequest, newResponse func() proto.Message, record func(proto.Message) []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		request := newRequest()
		if !readProto(w, r, request) {
			return
		}

		names := record(request)
		logger.write(kind, names)
		writeProto(w, newResponse())
	}
}

func newTraceRequest() proto.Message {
	return &collectortracepb.ExportTraceServiceRequest{}
}

func newTraceResponse() proto.Message {
	return &collectortracepb.ExportTraceServiceResponse{}
}

func newMetricRequest() proto.Message {
	return &collectormetricspb.ExportMetricsServiceRequest{}
}

func newMetricResponse() proto.Message {
	return &collectormetricspb.ExportMetricsServiceResponse{}
}

func (s *collectorState) recordTraceMessage(message proto.Message) []string {
	request, ok := message.(*collectortracepb.ExportTraceServiceRequest)
	if !ok {
		return nil
	}

	return s.recordTraces(request)
}

func (s *collectorState) recordMetricMessage(message proto.Message) []string {
	request, ok := message.(*collectormetricspb.ExportMetricsServiceRequest)
	if !ok {
		return nil
	}

	return s.recordMetrics(request)
}

func readProto(w http.ResponseWriter, r *http.Request, message proto.Message) bool {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return false
	}

	if err := proto.Unmarshal(body, message); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return false
	}

	return true
}

func writeProto(w http.ResponseWriter, message proto.Message) {
	body, err := proto.Marshal(message)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-protobuf")
	_, _ = w.Write(body)
}

func (s *collectorState) recordTraces(request *collectortracepb.ExportTraceServiceRequest) []string {
	names := collectSpanNames(request)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.traceRequests++
	for _, item := range names {
		s.spans[item.name]++
		s.traceIDs[item.traceID]++
	}

	return spanNames(names)
}

func collectSpanNames(request *collectortracepb.ExportTraceServiceRequest) []spanRecord {
	var records []spanRecord

	for _, resourceSpans := range request.GetResourceSpans() {
		for _, scopeSpans := range resourceSpans.GetScopeSpans() {
			for _, span := range scopeSpans.GetSpans() {
				records = append(records, spanRecord{
					name:    span.GetName(),
					traceID: hex.EncodeToString(span.GetTraceId()),
				})
			}
		}
	}

	return records
}

type spanRecord struct {
	name    string
	traceID string
}

func spanNames(records []spanRecord) []string {
	names := make([]string, 0, len(records))
	for _, record := range records {
		names = append(names, record.name)
	}

	return names
}

func (s *collectorState) recordMetrics(request *collectormetricspb.ExportMetricsServiceRequest) []string {
	names := collectMetricNames(request)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.metricRequests++
	for _, name := range names {
		s.metrics[name]++
	}

	return names
}

func collectMetricNames(request *collectormetricspb.ExportMetricsServiceRequest) []string {
	var names []string

	for _, resourceMetrics := range request.GetResourceMetrics() {
		for _, scopeMetrics := range resourceMetrics.GetScopeMetrics() {
			for _, metric := range scopeMetrics.GetMetrics() {
				names = append(names, metric.GetName())
			}
		}
	}

	return names
}

func (s *collectorState) snapshot() summary {
	s.mu.Lock()
	defer s.mu.Unlock()

	return summary{
		TraceRequests:  s.traceRequests,
		MetricRequests: s.metricRequests,
		Spans:          sortedKeys(s.spans),
		TraceIDs:       sortedKeys(s.traceIDs),
		Metrics:        sortedKeys(s.metrics),
	}
}

func sortedKeys(values map[string]int) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}
