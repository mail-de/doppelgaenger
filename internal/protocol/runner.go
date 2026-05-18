package protocol

import (
	"context"
	"errors"
	"time"
)

// Runner coordinates primary, shadow, and comparison work for one event.
type Runner struct {
	Comparator    Comparator
	ShadowTimeout time.Duration
}

// RunResult captures the outcome of a primary and optional shadow run.
type RunResult struct {
	Primary       Response
	Shadow        Response
	ShadowStarted bool
	ShadowOK      bool
	ShadowErr     string
	Compare       CompareResult
	CompareErr    error
}

// RunEvent sends one event to primary and optional shadow sessions.
func (r Runner) RunEvent(ctx context.Context, primary TestSession, shadow TestSession, event Event) RunResult {
	result := RunResult{}
	if !r.runPrimary(primary, event, &result) {
		return result
	}

	if shadow == nil {
		return result
	}

	r.runShadow(ctx, shadow, event, &result)
	r.compareResponses(&result)

	return result
}

func (r Runner) runPrimary(primary TestSession, event Event, result *RunResult) bool {
	if primary == nil {
		result.CompareErr = errors.New("primary session missing")

		return false
	}

	if err := primary.Send(event); err != nil {
		result.Primary.Err = err
		result.CompareErr = err

		return false
	}

	primaryRes, err := primary.Receive()
	if err != nil && primaryRes.Err == nil {
		primaryRes.Err = err
	}

	result.Primary = primaryRes

	return true
}

func (r Runner) runShadow(ctx context.Context, shadow TestSession, event Event, result *RunResult) {
	result.ShadowStarted = true
	shadowCtx := ctx

	var cancel context.CancelFunc
	if r.ShadowTimeout > 0 {
		shadowCtx, cancel = context.WithTimeout(ctx, r.ShadowTimeout)
	}

	if cancel != nil {
		defer cancel()
	}

	shadowCh := make(chan Response, 1)
	errCh := make(chan error, 1)
	shadowEvent := event

	shadowEvent.Ctx = shadowCtx

	go func() {
		if err := shadow.Send(shadowEvent); err != nil {
			errCh <- err

			return
		}

		resp, err := shadow.Receive()
		if err != nil && resp.Err == nil {
			resp.Err = err
		}

		shadowCh <- resp
	}()

	select {
	case resp := <-shadowCh:
		result.Shadow = resp
		result.ShadowOK = resp.Err == nil
		result.ShadowErr = ErrString(resp.Err)
	case err := <-errCh:
		result.Shadow.Err = err
		result.ShadowOK = false
		result.ShadowErr = ErrString(err)
	case <-shadowCtx.Done():
		result.Shadow.Err = shadowCtx.Err()
		result.ShadowOK = false
		result.ShadowErr = ErrString(shadowCtx.Err())
	}
}

func (r Runner) compareResponses(result *RunResult) {
	if r.Comparator == nil {
		return
	}

	compareResult, err := r.Comparator.Compare(result.Primary, result.Shadow)
	result.Compare = compareResult
	result.CompareErr = err
}
