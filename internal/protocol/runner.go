package protocol

import (
	"context"
	"errors"
	"time"
)

type Runner struct {
	Comparator    Comparator
	ShadowTimeout time.Duration
}

type RunResult struct {
	Primary       Response
	Shadow        Response
	ShadowStarted bool
	ShadowOK      bool
	ShadowErr     string
	Compare       CompareResult
	CompareErr    error
}

func (r Runner) RunEvent(ctx context.Context, primary TestSession, shadow TestSession, event Event) RunResult {
	result := RunResult{}
	if primary == nil {
		result.CompareErr = errors.New("primary session missing")
		return result
	}

	if err := primary.Send(event); err != nil {
		result.Primary.Err = err
		result.CompareErr = err
		return result
	}

	primaryRes, err := primary.Receive()
	if err != nil && primaryRes.Err == nil {
		primaryRes.Err = err
	}
	result.Primary = primaryRes

	if shadow == nil {
		return result
	}

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
	go func() {
		if err := shadow.Send(event); err != nil {
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

	if r.Comparator != nil {
		compareResult, err := r.Comparator.Compare(result.Primary, result.Shadow)
		result.Compare = compareResult
		result.CompareErr = err
	}

	return result
}
