package protocol

import (
	"context"
	"net/http"
	"time"

	"doppelgaenger/internal/compare"
	"doppelgaenger/internal/headers"
)

// Target identifies which backend side a session talks to.
type Target string

const (
	// TargetPrimary identifies the primary backend side.
	TargetPrimary Target = "primary"
	// TargetShadow identifies the shadow backend side.
	TargetShadow = "shadow"
)

// Decision describes a normalized Milter response decision.
type Decision string

const (
	// DecisionAccept accepts the SMTP transaction.
	DecisionAccept Decision = "accept"
	// DecisionReject rejects the SMTP transaction.
	DecisionReject Decision = "reject"
	// DecisionTempfail temporarily fails the SMTP transaction.
	DecisionTempfail Decision = "tempfail"
	// DecisionDiscard discards the SMTP transaction.
	DecisionDiscard Decision = "discard"
	// DecisionContinue continues SMTP processing.
	DecisionContinue Decision = "continue"
	// DecisionUnknown captures unknown or non-terminal decisions.
	DecisionUnknown Decision = "unknown"
)

// Action describes a normalized Milter action.
type Action struct {
	Type  string
	Name  string
	Value string
}

// ActionDiff describes whether an action is present in primary and shadow responses.
type ActionDiff struct {
	Action  Action
	Primary bool
	Shadow  bool
}

// Event describes one proxy event passed through a protocol runner.
type Event struct {
	Ctx                   context.Context
	Kind                  string
	Method                string
	Path                  string
	PrimaryPath           string
	ShadowPath            string
	RawQuery              string
	Host                  string
	Header                http.Header
	Body                  []byte
	RemoteAddr            string
	RequestID             uint64
	PrimaryRequestHeaders map[string]string
	ShadowRequestHeaders  map[string]string
	Payload               []byte
	Meta                  map[string]string
}

// Response describes one primary or shadow protocol response.
type Response struct {
	Proto    string
	Selected string
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

// CompareResult captures protocol-level comparison details.
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
