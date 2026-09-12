package platform

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/metadata"
)

func TestRequestIDContextAndGRPCMetadata(t *testing.T) {
	const supplied = "C73A33C3-8DB7-4AC4-80D1-10A04705DA7E"
	ctx, id := ContextWithRequestID(context.Background(), supplied)
	if want := "c73a33c3-8db7-4ac4-80d1-10a04705da7e"; id != want || RequestID(ctx) != want {
		t.Fatalf("request ID=%q context=%q", id, RequestID(ctx))
	}

	outgoing := outgoingRequestContext(ctx)
	outgoingMetadata, ok := metadata.FromOutgoingContext(outgoing)
	if !ok {
		t.Fatal("outgoing metadata is missing")
	}
	values := outgoingMetadata.Get(requestIDMetadataKey)
	if len(values) != 1 || values[0] != id {
		t.Fatalf("outgoing metadata=%v", values)
	}

	incoming := metadata.NewIncomingContext(context.Background(), metadata.Pairs(requestIDMetadataKey, supplied))
	incoming, id = incomingRequestContext(incoming)
	if id != RequestID(incoming) || id != "c73a33c3-8db7-4ac4-80d1-10a04705da7e" {
		t.Fatalf("incoming request ID=%q context=%q", id, RequestID(incoming))
	}
}

func TestInvalidRequestIDIsReplaced(t *testing.T) {
	ctx, id := ContextWithRequestID(context.Background(), "invalid\nvalue")
	if _, err := uuid.Parse(id); err != nil {
		t.Fatalf("generated request ID is invalid: %q", id)
	}
	if RequestID(ctx) != id {
		t.Fatalf("context request ID=%q want=%q", RequestID(ctx), id)
	}
}
