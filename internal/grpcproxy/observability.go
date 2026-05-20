package grpcproxy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"doppelgaenger/internal/observability"
)

type grpcRPCContext struct {
	reqID         uint64
	traceID       string
	remote        string
	fullMethod    string
	started       time.Time
	outcome       string
	spanErr       error
	primaryStatus codes.Code
	shadowEnabled bool
	shadowStarted bool
}

type wrappedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s wrappedServerStream) Context() context.Context {
	return s.ctx
}

func (h *Handler) newRPCContext(ctx context.Context, reqID uint64, fullMethod string, started time.Time) *grpcRPCContext {
	rpcCtx := &grpcRPCContext{
		reqID:         reqID,
		fullMethod:    fullMethod,
		started:       started,
		outcome:       observability.OutcomeOK,
		primaryStatus: codes.OK,
	}

	if peerInfo, ok := peer.FromContext(ctx); ok && peerInfo.Addr != nil {
		rpcCtx.remote = peerInfo.Addr.String()
	}

	return rpcCtx
}

func (h *Handler) startGRPCObservation(ctx context.Context, rpcCtx *grpcRPCContext) (context.Context, string, func()) {
	if ctx == nil {
		ctx = context.Background()
	}

	if h == nil || h.observability == nil {
		return ctx, "", func() {}
	}

	incoming, _ := metadata.FromIncomingContext(ctx)
	requestCtx := h.observability.ExtractGRPCContext(ctx, incoming)
	requestCtx, span := h.observability.StartSpanWithKind(requestCtx,
		fmt.Sprintf("gRPC %s", rpcCtx.fullMethod),
		trace.SpanKindServer,
		attribute.String(observability.LabelProtocol, ProtocolName),
		attribute.String(observability.LabelMethod, rpcCtx.fullMethod),
		attribute.String("rpc.system", "grpc"),
		attribute.String("rpc.method", rpcCtx.fullMethod),
		attribute.String("client.address", rpcCtx.remote),
		attribute.Int64("doppelgaenger.request_id", int64(rpcCtx.reqID)),
	)
	traceID := observability.TraceIDFromContext(requestCtx)

	finish := func() {
		span.SetAttributes(
			attribute.String("rpc.grpc.status_code", rpcCtx.primaryStatus.String()),
			attribute.String(observability.LabelOutcome, rpcCtx.outcome),
			attribute.String(observability.LabelShadow, observability.BoolLabel(rpcCtx.shadowEnabled)),
			attribute.String(observability.LabelShadowStarted, observability.BoolLabel(rpcCtx.shadowStarted)),
		)

		if rpcCtx.spanErr != nil || rpcCtx.primaryStatus != codes.OK {
			description := rpcCtx.primaryStatus.String()
			if rpcCtx.spanErr != nil {
				description = rpcCtx.spanErr.Error()
			}

			span.SetStatus(otelcodes.Error, description)
		}

		h.observability.ObserveIngressRequest(
			requestCtx,
			ProtocolName,
			rpcCtx.fullMethod,
			rpcCtx.outcome,
			observability.BoolLabel(rpcCtx.shadowEnabled),
			observability.BoolLabel(rpcCtx.shadowStarted),
			time.Since(rpcCtx.started),
		)
		h.observability.EndSpan(span, rpcCtx.spanErr)
	}

	return requestCtx, traceID, finish
}

func (h *Handler) startBackendSpan(ctx context.Context, targetLabel string, fullMethod string, target *Target) (context.Context, trace.Span) {
	if ctx == nil {
		ctx = context.Background()
	}

	if h == nil || h.observability == nil {
		return ctx, nil
	}

	address := ""
	if target != nil {
		address = target.Address
	}

	return h.observability.StartSpanWithKind(ctx,
		fmt.Sprintf("gRPC %s %s", targetLabel, fullMethod),
		trace.SpanKindClient,
		attribute.String(observability.LabelProtocol, ProtocolName),
		attribute.String(observability.LabelTarget, targetLabel),
		attribute.String(observability.LabelMethod, fullMethod),
		attribute.String("rpc.system", "grpc"),
		attribute.String("rpc.method", fullMethod),
		attribute.String("server.address", address),
	)
}

func (h *Handler) finishBackendObservation(ctx context.Context, span trace.Span, targetLabel string, fullMethod string, result grpcStreamResult, err error) {
	if h == nil || h.observability == nil {
		return
	}

	statusLabel := grpcStatusLabel(result.StatusCode)
	resultLabel := grpcBackendMetricResult(result, err)

	spanErr := err
	if spanErr == nil && result.Err != "" {
		spanErr = errors.New(result.Err)
	}

	if span != nil {
		span.SetAttributes(
			attribute.String("rpc.grpc.status_code", statusLabel),
			attribute.String(observability.LabelStatus, statusLabel),
			attribute.String(observability.LabelResult, resultLabel),
		)

		if spanErr != nil || result.StatusCode != codes.OK {
			description := statusLabel
			if spanErr != nil {
				description = spanErr.Error()
			}

			span.SetStatus(otelcodes.Error, description)
		}
	}

	h.observability.ObserveBackendRequest(ctx, ProtocolName, targetLabel, fullMethod, statusLabel, resultLabel, result.Duration)
	h.observability.EndSpan(span, spanErr)
}

func (h *Handler) finishShadowObservation(ctx context.Context, fullMethod string, result grpcStreamResult) {
	if h == nil || h.observability == nil || !result.Started {
		return
	}

	h.observability.ObserveBackendRequest(
		ctx,
		ProtocolName,
		"shadow",
		fullMethod,
		grpcStatusLabel(result.StatusCode),
		grpcBackendMetricResult(result, nil),
		result.Duration,
	)
}

func (h *Handler) finishComparisonObservation(ctx context.Context, result grpcCompareResult) {
	if h == nil || h.observability == nil {
		return
	}

	h.observability.ObserveComparison(ctx, ProtocolName, grpcComparisonMetricResult(result))
}

func grpcIngressOutcome(err error) string {
	if err == nil {
		return observability.OutcomeOK
	}

	if status.Code(err) == codes.DeadlineExceeded {
		return observability.OutcomeTimeout
	}

	if _, ok := status.FromError(err); ok {
		return observability.OutcomePrimaryError
	}

	return observability.OutcomeError
}

func grpcStatusLabel(code codes.Code) string {
	return code.String()
}

func grpcBackendMetricResult(result grpcStreamResult, err error) string {
	switch result.SkipReason {
	case shadowSkipReasonQueueFull:
		return observability.ResultQueueFull
	case shadowSkipReasonTimeout:
		return observability.ResultTimeout
	}

	if err != nil || result.Err != "" {
		if result.StatusCode != codes.OK && result.Complete {
			return observability.ResultStatusCode
		}

		return observability.ResultError
	}

	if result.StatusCode != codes.OK {
		return observability.ResultStatusCode
	}

	return observability.ResultOK
}

func grpcComparisonMetricResult(result grpcCompareResult) string {
	switch result.Outcome {
	case grpcCompareOutcomeSame:
		return observability.ResultSame
	case grpcCompareOutcomeDiff:
		return observability.ResultDiff
	case grpcCompareOutcomeError:
		return observability.ResultError
	case grpcCompareOutcomeSkipped:
		return observability.ResultSkipped
	default:
		return observability.ResultError
	}
}
