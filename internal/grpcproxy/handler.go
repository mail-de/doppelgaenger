package grpcproxy

import (
	"context"
	"log/slog"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"doppelgaenger/internal/config"
	"doppelgaenger/internal/observability"
	"doppelgaenger/internal/ratelimit"
)

// Handler forwards unknown gRPC services to the selected primary target.
type Handler struct {
	cfg           config.Config
	resolver      *Resolver
	pools         *TargetPools
	logger        *slog.Logger
	shadowLimiter ratelimit.Limiter
	observability *observability.Observability
	callOptions   []grpc.CallOption
	requestID     atomic.Uint64
	randMu        sync.Mutex
	rng           *rand.Rand
}

// NewHandler constructs the generic gRPC unknown-service handler.
func NewHandler(
	cfg config.Config,
	resolver *Resolver,
	pools *TargetPools,
	logger *slog.Logger,
	shadowLimiter ratelimit.Limiter,
	obs *observability.Observability,
) *Handler {
	return &Handler{
		cfg:           cfg,
		resolver:      resolver,
		pools:         pools,
		logger:        logger,
		shadowLimiter: shadowLimiter,
		observability: obs,
		callOptions:   defaultCallOptions(cfg),
		rng:           rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Handle is used as grpc.UnknownServiceHandler and receives every unregistered RPC.
func (h *Handler) Handle(_ any, stream grpc.ServerStream) error {
	fullMethod, ok := grpc.MethodFromServerStream(stream)
	if !ok {
		return status.Error(codes.Internal, "gRPC full method is unavailable")
	}

	rpcCtx, requestCtx, finishObservation := h.prepareRPCContext(stream, fullMethod)
	defer finishObservation()

	wrappedStream := wrappedServerStream{ServerStream: stream, ctx: requestCtx}

	decision, err := h.resolveDecision(fullMethod, rpcCtx)
	if err != nil {
		return err
	}

	target := h.pickPrimaryTarget(wrappedStream.Context(), fullMethod, decision)
	if target == nil {
		return missingPrimaryTargetError(rpcCtx)
	}

	shadowDecision, shadow := h.prepareShadow(wrappedStream.Context(), fullMethod, decision, rpcCtx)
	primaryCtx, primarySpan := h.startBackendSpan(wrappedStream.Context(), "primary", fullMethod, target)

	upstreamCtx, cancel := context.WithCancel(outgoingContext(primaryCtx, decision.PrimaryMetadata, h.observability))
	defer cancel()

	primary, err := target.Conn.NewStream(upstreamCtx, genericStreamDesc(), fullMethod, h.callOptions...)
	if err != nil {
		return h.handlePrimaryStreamCreateError(wrappedStream.Context(), primaryCtx, primarySpan, fullMethod, rpcCtx, decision, target, shadowDecision, shadow, err)
	}

	primaryResult, primaryErr := forwardPrimaryStream(wrappedStream, primary, cancel, shadow)
	h.finishRPCResult(wrappedStream.Context(), primaryCtx, primarySpan, fullMethod, rpcCtx, decision, target, shadowDecision, shadow, primaryResult, primaryErr)

	return primaryErr
}

func (h *Handler) prepareRPCContext(stream grpc.ServerStream, fullMethod string) (*grpcRPCContext, context.Context, func()) {
	rpcCtx := h.newRPCContext(stream.Context(), h.requestID.Add(1), fullMethod, time.Now())
	requestCtx, traceID, finishObservation := h.startGRPCObservation(stream.Context(), rpcCtx)
	rpcCtx.traceID = traceID

	return rpcCtx, requestCtx, finishObservation
}

func (h *Handler) resolveDecision(fullMethod string, rpcCtx *grpcRPCContext) (Decision, error) {
	decision, err := h.resolve(fullMethod)
	if err == nil {
		return decision, nil
	}

	rpcCtx.outcome = observability.OutcomeBadRequest
	rpcCtx.spanErr = err
	rpcCtx.primaryStatus = codes.Unimplemented

	if _, ok := status.FromError(err); ok {
		return Decision{}, err
	}

	return Decision{}, status.Errorf(codes.Unimplemented, "unsupported gRPC method: %v", err)
}

func missingPrimaryTargetError(rpcCtx *grpcRPCContext) error {
	err := status.Error(codes.Unavailable, "no primary gRPC target available")
	rpcCtx.outcome = observability.OutcomeError
	rpcCtx.spanErr = err
	rpcCtx.primaryStatus = codes.Unavailable

	return err
}

func (h *Handler) prepareShadow(
	ctx context.Context,
	fullMethod string,
	decision Decision,
	rpcCtx *grpcRPCContext,
) (grpcShadowPolicyDecision, *shadowForwarder) {
	shadowDecision := h.shouldShadow(ctx, decision)
	shadowTarget := h.pickShadowTarget(ctx, fullMethod, decision, shadowDecision)
	rpcCtx.shadowEnabled = shadowDecision.doShadow
	rpcCtx.shadowStarted = shadowTarget != nil && shadowTarget.Conn != nil

	return shadowDecision, h.startShadow(ctx, fullMethod, decision, shadowDecision, shadowTarget)
}

func (h *Handler) handlePrimaryStreamCreateError(
	streamCtx context.Context,
	primaryCtx context.Context,
	primarySpan trace.Span,
	fullMethod string,
	rpcCtx *grpcRPCContext,
	decision Decision,
	target *Target,
	shadowDecision grpcShadowPolicyDecision,
	shadow *shadowForwarder,
	err error,
) error {
	shadow.Close()

	primaryErr := primaryStreamError("create primary stream", err)
	primaryResult := grpcStreamResult{MessageHash: emptyMessageHash()}
	primaryResult.setError(primaryErr)

	h.finishRPCResult(streamCtx, primaryCtx, primarySpan, fullMethod, rpcCtx, decision, target, shadowDecision, shadow, primaryResult, primaryErr)

	return primaryErr
}

func (h *Handler) finishRPCResult(
	streamCtx context.Context,
	primaryCtx context.Context,
	primarySpan trace.Span,
	fullMethod string,
	rpcCtx *grpcRPCContext,
	decision Decision,
	target *Target,
	shadowDecision grpcShadowPolicyDecision,
	shadow *shadowForwarder,
	primaryResult grpcStreamResult,
	primaryErr error,
) {
	shadowResult := shadow.Wait(h.cfg.GRPCShadowTimeout)
	compareResult := h.compareRPCResult(decision, shadowDecision, primaryResult, shadowResult)
	h.finishBackendObservation(primaryCtx, primarySpan, "primary", fullMethod, primaryResult, primaryErr)
	h.finishShadowObservation(streamCtx, fullMethod, shadowResult)
	h.finishComparisonObservation(streamCtx, compareResult)

	rpcCtx.outcome = grpcIngressOutcome(primaryErr)
	rpcCtx.spanErr = primaryErr
	rpcCtx.primaryStatus = primaryResult.StatusCode
	rpcCtx.shadowStarted = shadowResult.Started
	h.logRPCResult(rpcCtx, fullMethod, decision, target, shadowDecision, primaryResult, shadowResult, compareResult)
}

func (h *Handler) resolve(fullMethod string) (Decision, error) {
	if h == nil || h.resolver == nil {
		return Decision{}, status.Error(codes.Internal, "gRPC resolver not configured")
	}

	return h.resolver.Resolve(fullMethod)
}

func (h *Handler) pickPrimaryTarget(ctx context.Context, fullMethod string, decision Decision) *Target {
	if h == nil || h.pools == nil || h.pools.Primary == nil {
		return nil
	}

	info := RequestInfo{
		FullMethod: fullMethod,
		Service:    decision.Service,
		Method:     decision.Method,
	}

	if peerInfo, ok := peer.FromContext(ctx); ok && peerInfo.Addr != nil {
		info.PeerAddr = peerInfo.Addr.String()
		info.PeerIP = parsePeerIP(info.PeerAddr)
	}

	return h.pools.Primary.Pick(info)
}

func genericStreamDesc() *grpc.StreamDesc {
	return &grpc.StreamDesc{
		ServerStreams: true,
		ClientStreams: true,
	}
}

type grpcShadowPolicyDecision struct {
	doShadow   bool
	forced     bool
	skipReason string
}

func (h *Handler) shouldShadow(ctx context.Context, decision Decision) grpcShadowPolicyDecision {
	forcedShadow := h.forcedShadow(ctx)

	if decision.ShadowMode == ShadowModeNever {
		reason := decision.ShadowSkipReason
		if reason == "" {
			reason = SkipReasonGRPCRule
		}

		return grpcShadowPolicyDecision{forced: forcedShadow, skipReason: reason}
	}

	doShadow := false

	switch decision.ShadowMode {
	case ShadowModeAlways:
		doShadow = true
	case ShadowModeAuto, ShadowModeInherit:
		doShadow = forcedShadow || h.sampleShadow()
	default:
		doShadow = forcedShadow || h.sampleShadow()
	}

	if doShadow && !forcedShadow && h.shadowLimiter != nil && !h.shadowLimiter.Allow() {
		return grpcShadowPolicyDecision{forced: forcedShadow, skipReason: shadowSkipReasonRateLimited}
	}

	return grpcShadowPolicyDecision{doShadow: doShadow, forced: forcedShadow}
}

func (h *Handler) forcedShadow(ctx context.Context) bool {
	if h == nil || h.cfg.GRPCShadowForceMetadata == "" {
		return false
	}

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return false
	}

	for _, value := range md.Get(h.cfg.GRPCShadowForceMetadata) {
		if value != "" {
			return true
		}
	}

	return false
}

func (h *Handler) sampleShadow() bool {
	if h == nil {
		return false
	}

	if h.cfg.ShadowSamplePercent >= 100 {
		return true
	}

	if h.cfg.ShadowSamplePercent <= 0 {
		return false
	}

	return h.randIntn(100) < h.cfg.ShadowSamplePercent
}

func (h *Handler) randIntn(n int) int {
	h.randMu.Lock()
	defer h.randMu.Unlock()

	if h.rng == nil {
		h.rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}

	return h.rng.Intn(n)
}

func (h *Handler) pickShadowTarget(ctx context.Context, fullMethod string, decision Decision, shadow grpcShadowPolicyDecision) *Target {
	if !shadow.doShadow || h == nil || h.pools == nil || h.pools.Shadow == nil {
		return nil
	}

	info := RequestInfo{
		FullMethod: fullMethod,
		Service:    decision.Service,
		Method:     decision.Method,
	}

	if peerInfo, ok := peer.FromContext(ctx); ok && peerInfo.Addr != nil {
		info.PeerAddr = peerInfo.Addr.String()
		info.PeerIP = parsePeerIP(info.PeerAddr)
	}

	return h.pools.Shadow.Pick(info)
}

func (h *Handler) startShadow(
	ctx context.Context,
	fullMethod string,
	decision Decision,
	shadow grpcShadowPolicyDecision,
	target *Target,
) *shadowForwarder {
	if !shadow.doShadow {
		return newCompletedShadowForwarder(grpcStreamResult{SkipReason: shadow.skipReason})
	}

	if target == nil || target.Conn == nil {
		return newCompletedShadowForwarder(grpcStreamResult{
			SkipReason: shadowSkipReasonTargetUnavailable,
			Err:        shadowSkipReasonTargetUnavailable,
		})
	}

	return newShadowForwarder(ctx, target, fullMethod, decision.ShadowMetadata, h.cfg, h.callOptions, h.observability)
}
