package proxy

import (
	"context"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"doppelgaenger/internal/config"
	"doppelgaenger/internal/mapping"
	"doppelgaenger/internal/protocol"
)

const (
	testHTTPProto         = "HTTP/1.1"
	testAuthStatus        = "Auth-Status"
	testAuthPath          = "/auth"
	testAuthPathRegex     = "^/auth$"
	testHeaderOK          = "OK"
	testPathParamKey      = "path"
	testShadowTarget      = "shadow"
	testPrimaryTarget     = "primary"
	testShadowForceHeader = "X-Shadow"
)

type shadowOutcome struct {
	canceled bool
}

type asyncTestAdapter struct {
	shadowDelay time.Duration
	shadowDone  chan shadowOutcome
}

func (a *asyncTestAdapter) Protocol() string {
	return protocolHTTP
}

func (a *asyncTestAdapter) NewSession(ctx context.Context, target protocol.Target) (protocol.TestSession, error) {
	return &asyncTestSession{
		target:      target,
		ctx:         ctx,
		shadowDelay: a.shadowDelay,
		shadowDone:  a.shadowDone,
	}, nil
}

type asyncTestSession struct {
	target      protocol.Target
	ctx         context.Context
	shadowDelay time.Duration
	shadowDone  chan shadowOutcome
}

func (s *asyncTestSession) Send(_ protocol.Event) error {
	return nil
}

func (s *asyncTestSession) Receive() (protocol.Response, error) {
	if s.target == protocol.TargetShadow {
		select {
		case <-time.After(s.shadowDelay):
			if s.shadowDone != nil {
				s.shadowDone <- shadowOutcome{canceled: false}
			}

			return protocol.Response{Status: 204, Proto: testHTTPProto, Selected: testShadowTarget}, nil
		case <-s.ctx.Done():
			if s.shadowDone != nil {
				s.shadowDone <- shadowOutcome{canceled: true}
			}

			return protocol.Response{Err: s.ctx.Err(), Selected: testShadowTarget}, s.ctx.Err()
		}
	}

	return protocol.Response{
		Status:   200,
		Proto:    testHTTPProto,
		Selected: testPrimaryTarget,
		Header:   http.Header{testAuthStatus: []string{testHeaderOK}},
		Body:     []byte("ok"),
	}, nil
}

func (s *asyncTestSession) Close() error {
	return nil
}

func newAsyncTestHandler(adapter protocol.Adapter, timeout time.Duration) *Handler {
	return &Handler{
		cfg: config.Config{
			ShadowSamplePercent:    100,
			ShadowTimeout:          timeout,
			ForwardResponseHeaders: []string{testAuthStatus},
			CompareMode:            "header",
		},
		adapter:    adapter,
		runner:     protocol.Runner{ShadowTimeout: timeout},
		pathMapper: mapping.DirectMapper{},
		logger:     slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})),
		rng:        rand.New(rand.NewSource(1)),
	}
}

func TestHandleReturnsBeforeShadowCompletes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	shadowDone := make(chan shadowOutcome, 1)
	handler := newAsyncTestHandler(&asyncTestAdapter{
		shadowDelay: 120 * time.Millisecond,
		shadowDone:  shadowDone,
	}, 500*time.Millisecond)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, testAuthPath, nil)
	c.Request = req
	c.Params = gin.Params{{Key: testPathParamKey, Value: testAuthPath}}

	start := time.Now()

	handler.Handle(c)

	elapsed := time.Since(start)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	if elapsed >= 80*time.Millisecond {
		t.Fatalf("expected response to return before shadow finished, got %s", elapsed)
	}

	select {
	case outcome := <-shadowDone:
		if outcome.canceled {
			t.Fatalf("expected shadow request to finish normally")
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for shadow completion")
	}
}

func TestHandleShadowIsDetachedFromRequestContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	shadowDone := make(chan shadowOutcome, 1)
	handler := newAsyncTestHandler(&asyncTestAdapter{
		shadowDelay: 120 * time.Millisecond,
		shadowDone:  shadowDone,
	}, 500*time.Millisecond)

	reqCtx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, testAuthPath, nil).WithContext(reqCtx)
	c.Request = req
	c.Params = gin.Params{{Key: testPathParamKey, Value: testAuthPath}}

	handler.Handle(c)
	cancelReq()

	select {
	case outcome := <-shadowDone:
		if outcome.canceled {
			t.Fatalf("expected shadow context to remain active after request context cancellation")
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for shadow completion")
	}
}
