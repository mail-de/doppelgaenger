package protocol

import (
	"net/http"
	"time"

	"httpproxy/internal/compare"
	"httpproxy/internal/headers"
)

type Target string

const (
	TargetPrimary Target = "primary"
	TargetShadow  Target = "shadow"
)

type Decision string

const (
	DecisionAccept   Decision = "accept"
	DecisionReject   Decision = "reject"
	DecisionTempfail Decision = "tempfail"
	DecisionDiscard  Decision = "discard"
	DecisionContinue Decision = "continue"
	DecisionUnknown  Decision = "unknown"
)

type Action struct {
	Type  string
	Name  string
	Value string
}

type ActionDiff struct {
	Action  Action
	Primary bool
	Shadow  bool
}

type Event struct {
	Kind       string
	Method     string
	Path       string
	RawQuery   string
	Header     http.Header
	Body       []byte
	RemoteAddr string
	RequestID  uint64
	Payload    []byte
	Meta       map[string]string
}

type Response struct {
	Proto    string
	Status   int
	Header   http.Header
	Body     []byte
	Duration time.Duration
	Err      error
	Decision Decision
	Actions  []Action
	Raw      []byte
	RawTrace []string
}

type CompareResult struct {
	Mode           string
	Diff           bool
	HeaderDiff     bool
	HeaderPrimary  map[string]string
	HeaderShadow   map[string]string
	HeaderDiffs    []headers.HeaderDiff
	BodyDiff       bool
	JSONDiffs      []compare.JSONPathDiff
	HTMLSimilarity float64
	DecisionDiff   bool
	ActionDiffs    []ActionDiff
}
