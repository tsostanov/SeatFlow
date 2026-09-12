package platform

import (
	"context"
	"errors"
	"testing"

	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func TestHealthListReportsDependencyState(t *testing.T) {
	healthy := &healthService{check: func(context.Context) error { return nil }}
	result, err := healthy.List(context.Background(), &healthpb.HealthListRequest{})
	if err != nil || result.Statuses[""].Status != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("healthy result=%v error=%v", result, err)
	}

	unhealthy := &healthService{check: func(context.Context) error { return errors.New("down") }}
	result, err = unhealthy.List(context.Background(), &healthpb.HealthListRequest{})
	if err != nil || result.Statuses[""].Status != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("unhealthy result=%v error=%v", result, err)
	}
}

func TestConnectAcceptsRetryServiceConfig(t *testing.T) {
	connection, err := Connect("passthrough:///not-running")
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
}
