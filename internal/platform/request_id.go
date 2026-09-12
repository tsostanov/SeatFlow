package platform

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc/metadata"
)

const RequestIDHeader = "X-Request-ID"

const requestIDMetadataKey = "x-request-id"

type requestIDContextKey struct{}

func ContextWithRequestID(ctx context.Context, candidate string) (context.Context, string) {
	parsed, err := uuid.Parse(candidate)
	if err != nil {
		parsed = uuid.New()
	}
	id := parsed.String()
	return context.WithValue(ctx, requestIDContextKey{}, id), id
}

func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDContextKey{}).(string)
	return id
}

func outgoingRequestContext(ctx context.Context) context.Context {
	if id := RequestID(ctx); id != "" {
		return metadata.AppendToOutgoingContext(ctx, requestIDMetadataKey, id)
	}
	return ctx
}

func incomingRequestContext(ctx context.Context) (context.Context, string) {
	candidate := ""
	if values := metadata.ValueFromIncomingContext(ctx, requestIDMetadataKey); len(values) > 0 {
		candidate = values[0]
	}
	return ContextWithRequestID(ctx, candidate)
}
