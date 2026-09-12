package platform

import (
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestErrorCarriesMachineReadableDetails(t *testing.T) {
	err := Error(codes.AlreadyExists, "seat is unavailable", "SEAT_UNAVAILABLE", map[string]string{"seat_id": "12"})
	value := status.Convert(err)
	if value.Code() != codes.AlreadyExists {
		t.Fatalf("code=%v", value.Code())
	}
	details := value.Details()
	if len(details) != 1 {
		t.Fatalf("details=%v", details)
	}
	info, ok := details[0].(*errdetails.ErrorInfo)
	if !ok || info.Reason != "SEAT_UNAVAILABLE" || info.Domain != "seatflow.local" || info.Metadata["seat_id"] != "12" {
		t.Fatalf("error info=%v", details[0])
	}
}
