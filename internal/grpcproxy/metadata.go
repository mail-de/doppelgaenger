package grpcproxy

import (
	"context"
	"strings"

	"google.golang.org/grpc/metadata"

	"doppelgaenger/internal/observability"
)

func outgoingContext(ctx context.Context, overlay map[string]string, obs *observability.Observability) context.Context {
	return metadata.NewOutgoingContext(ctx, outgoingMetadata(ctx, overlay, obs))
}

func outgoingMetadata(ctx context.Context, overlay map[string]string, obs *observability.Observability) metadata.MD {
	incoming, _ := metadata.FromIncomingContext(ctx)
	out := cloneIncomingMetadata(incoming)

	for key, value := range overlay {
		normalized := normalizeMetadataKey(key)
		if normalized == "" || isReservedOutgoingMetadata(normalized) {
			continue
		}

		out.Set(normalized, value)
	}

	if obs != nil {
		obs.InjectGRPCTraceContext(ctx, out)
	}

	return out
}

func cloneIncomingMetadata(in metadata.MD) metadata.MD {
	out := metadata.MD{}

	for key, values := range in {
		normalized := normalizeMetadataKey(key)
		if normalized == "" {
			continue
		}

		out.Append(normalized, values...)
	}

	return out
}

func cloneMetadata(in metadata.MD) metadata.MD {
	if len(in) == 0 {
		return nil
	}

	out := metadata.MD{}

	for key, values := range in {
		normalized := normalizeMetadataKey(key)
		if normalized == "" {
			continue
		}

		out.Append(normalized, values...)
	}

	return out
}

func normalizeMetadataKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

func isReservedOutgoingMetadata(key string) bool {
	return strings.HasPrefix(key, ":") ||
		key == "content-type" ||
		key == "te" ||
		strings.HasPrefix(key, "grpc-") ||
		strings.HasSuffix(key, "-bin")
}
