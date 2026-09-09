package platform

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

func Env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func Connect(address string) (*grpc.ClientConn, error) {
	return grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			return invoke(ctx, method, req, reply, cc, opts...)
		}))
}

func CheckConnection(ctx context.Context, conn *grpc.ClientConn) error {
	result, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		return err
	}
	if result.Status != healthpb.HealthCheckResponse_SERVING {
		return errors.New("dependency is not ready")
	}
	return nil
}

type healthService struct {
	healthpb.UnimplementedHealthServer
	check func(context.Context) error
}

func (h *healthService) Check(ctx context.Context, r *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	if r.Service != "" {
		return nil, status.Error(codes.NotFound, "unknown service")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	state := healthpb.HealthCheckResponse_SERVING
	if h.check(ctx) != nil {
		state = healthpb.HealthCheckResponse_NOT_SERVING
	}
	return &healthpb.HealthCheckResponse{Status: state}, nil
}

func ServeGRPC(ctx context.Context, address string, register func(*grpc.Server), check func(context.Context) error) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		start := time.Now()
		result, err := handler(ctx, req)
		slog.Info("grpc request", "method", info.FullMethod, "code", status.Code(err).String(), "duration", time.Since(start))
		return result, err
	}))
	register(server)
	healthpb.RegisterHealthServer(server, &healthService{check: check})
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
			return
		}
		timer := time.AfterFunc(5*time.Second, server.Stop)
		defer timer.Stop()
		server.GracefulStop()
	}()
	slog.Info("grpc listening", "address", address)
	return server.Serve(listener)
}
