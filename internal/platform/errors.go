package platform

import (
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Error attaches a stable machine-readable reason to a gRPC status. HTTP and
// native gRPC clients can branch on the reason without parsing human text.
func Error(code codes.Code, message, reason string, metadata map[string]string) error {
	base := status.New(code, message)
	withDetails, err := base.WithDetails(&errdetails.ErrorInfo{
		Reason: reason, Domain: "seatflow.local", Metadata: metadata,
	})
	if err != nil {
		return base.Err()
	}
	return withDetails.Err()
}
