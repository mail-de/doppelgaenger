package grpcproxy

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// callerAuthCause is a bounded, token-free classification used in logs and spans.
type callerAuthCause string

const (
	callerCauseMissingBearer   callerAuthCause = "missing_bearer"
	callerCauseMalformedBearer callerAuthCause = "malformed_bearer"
	callerCauseMTLS            callerAuthCause = "mtls"
	callerCauseInactive        callerAuthCause = "inactive"
	callerCauseClaims          callerAuthCause = "claims"
	callerCauseScope           callerAuthCause = "scope"
	callerCauseMethod          callerAuthCause = "method"
	callerCauseTransport       callerAuthCause = "transport"
	callerCauseTimeout         callerAuthCause = "timeout"
	callerCauseHTTPStatus      callerAuthCause = "http_status"
	callerCauseResponse        callerAuthCause = "response"
	callerCauseOverload        callerAuthCause = "overload"
	callerCauseConfig          callerAuthCause = "config"
	callerCauseCanceled        callerAuthCause = "canceled"
	callerCauseDeadline        callerAuthCause = "deadline"
)

// Ingress outcomes separate caller faults from proxy-side authentication failures.
const (
	callerAuthRejected    = "caller_auth_rejected"
	callerAuthUnavailable = "caller_auth_unavailable"
	callerAuthCanceled    = "caller_auth_canceled"
)

const (
	callerMessageUnauthenticated = "caller authentication failed"
	callerMessageUnavailable     = "caller authentication unavailable"
	callerMessageCanceled        = "caller authentication canceled"
	callerMessageDeadline        = "caller authentication deadline exceeded"
	callerMessageMethod          = "caller method not permitted"
	callerMessageScope           = "caller scope not permitted"
)

// callerAuthError carries the gRPC status returned to the caller and an
// operator-facing classification. Neither field may contain token material or
// client secrets; detail is a fixed description or a transport error that never
// includes request bodies or URL credentials.
type callerAuthError struct {
	code       codes.Code
	cause      callerAuthCause
	message    string
	detail     string
	httpStatus int
	err        error
}

func (e *callerAuthError) Error() string {
	return e.message + ": " + string(e.cause)
}

func (e *callerAuthError) Unwrap() error {
	return e.err
}

// GRPCStatus exposes only the generic caller-facing message.
func (e *callerAuthError) GRPCStatus() *status.Status {
	return status.New(e.code, e.message)
}

func (e *callerAuthError) outcome() string {
	switch e.code {
	case codes.Unavailable, codes.Internal:
		return callerAuthUnavailable
	case codes.Canceled, codes.DeadlineExceeded:
		return callerAuthCanceled
	default:
		return callerAuthRejected
	}
}

func (e *callerAuthError) logLevel() slog.Level {
	switch {
	case e.cause == callerCauseConfig:
		return slog.LevelError
	case e.code == codes.Canceled || e.code == codes.DeadlineExceeded:
		return slog.LevelInfo
	default:
		return slog.LevelWarn
	}
}

func callerRejected(cause callerAuthCause) error {
	return &callerAuthError{code: codes.Unauthenticated, cause: cause, message: callerMessageUnauthenticated}
}

func callerDenied(cause callerAuthCause, message string) error {
	return &callerAuthError{code: codes.PermissionDenied, cause: cause, message: message}
}

func callerUnavailable(cause callerAuthCause, detail string, err error) *callerAuthError {
	return &callerAuthError{code: codes.Unavailable, cause: cause, message: callerMessageUnavailable, detail: detail, err: err}
}

// callerMisconfigured reports proxy-side faults as Unavailable: the caller is not
// at fault, and the proxy already reports other missing dependencies this way.
func callerMisconfigured(detail string, err error) error {
	return callerUnavailable(callerCauseConfig, detail, err)
}

// callerHTTPStatusError classifies a non-200 introspection response. A plain
// 400 can be triggered by caller-supplied token content, so only an RFC 6749
// client error marks it as proxy misconfiguration.
func callerHTTPStatusError(statusCode int, oauthError string) error {
	cause, detail := callerCauseHTTPStatus, "introspection endpoint returned an unexpected status"

	switch {
	case statusCode == http.StatusBadRequest && (oauthError == "invalid_client" || oauthError == "unauthorized_client"):
		cause, detail = callerCauseConfig, "introspection endpoint rejected the proxy client ("+oauthError+")"
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		cause, detail = callerCauseConfig, "introspection endpoint rejected the proxy client credentials"
	}

	authErr := callerUnavailable(cause, detail, nil)
	authErr.httpStatus = statusCode

	return authErr
}

func callerOverloaded() error {
	return callerUnavailable(callerCauseOverload, "concurrent introspection limit reached", nil)
}

// cloneCallerAuthError gives each waiter of a shared flight its own error value;
// the wrapped cause is never modified and may be shared.
func cloneCallerAuthError(err error) error {
	var authErr *callerAuthError
	if !errors.As(err, &authErr) {
		return err
	}

	clone := *authErr

	return &clone
}

// technical reports whether the failure lies with the proxy or the issuer
// rather than with the caller.
func (e *callerAuthError) technical() bool {
	return e.code == codes.Unavailable || e.code == codes.Internal
}

// callerLogInterval bounds repeated logs for one technical cause; the ingress
// and introspection metrics carry the full rate.
const callerLogInterval = 10 * time.Second

// logKey groups technical rejections for log rate limiting. Configuration
// faults are keyed by their fixed detail so that distinct misconfigurations
// are each reported; other causes use the cause alone because their details
// can contain unbounded values such as ephemeral ports.
func (e *callerAuthError) logKey() string {
	if e.cause == callerCauseConfig {
		return string(e.cause) + "\x00" + e.detail
	}

	return string(e.cause)
}

// callerLogLimiter emits at most one technical rejection log per key and
// interval and reports how many were suppressed since the last one.
type callerLogLimiter struct {
	mu         sync.Mutex
	last       map[string]time.Time
	suppressed map[string]int
}

func (l *callerLogLimiter) allow(cause string, now time.Time) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.last == nil {
		l.last = make(map[string]time.Time)
		l.suppressed = make(map[string]int)
	}

	if last, ok := l.last[cause]; ok && now.Sub(last) < callerLogInterval {
		l.suppressed[cause]++

		return false, 0
	}

	suppressed := l.suppressed[cause]
	l.last[cause] = now
	l.suppressed[cause] = 0

	return true, suppressed
}

// callerContextError maps a finished caller context; nil means the caller is still waiting.
func callerContextError(ctx context.Context) error {
	err := ctx.Err()
	if err == nil {
		return nil
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return &callerAuthError{code: codes.DeadlineExceeded, cause: callerCauseDeadline, message: callerMessageDeadline, err: err}
	}

	return &callerAuthError{code: codes.Canceled, cause: callerCauseCanceled, message: callerMessageCanceled, err: err}
}

// introspectionTransportError classifies errors of the detached introspection request.
func introspectionTransportError(ctx context.Context, err error) error {
	var netErr net.Error

	// Only an abandoned flight is canceled; no caller receives this error, but
	// its span should not report a timeout.
	if errors.Is(ctx.Err(), context.Canceled) {
		return callerUnavailable(callerCauseCanceled, "introspection abandoned by all callers", err)
	}

	if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil || (errors.As(err, &netErr) && netErr.Timeout()) {
		return callerUnavailable(callerCauseTimeout, "introspection request timed out", err)
	}

	return callerUnavailable(callerCauseTransport, err.Error(), err)
}

func asCallerAuthError(err error) *callerAuthError {
	var authErr *callerAuthError
	if errors.As(err, &authErr) {
		return authErr
	}

	return callerUnavailable(callerCauseConfig, "unclassified caller authentication error", err)
}
