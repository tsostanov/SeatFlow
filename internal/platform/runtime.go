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
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

const defaultServiceConfig = `{
  "methodConfig": [{
    "name": [{}],
    "waitForReady": true,
    "retryPolicy": {
      "maxAttempts": 3,
      "initialBackoff": "0.05s",
      "maxBackoff": "0.25s",
      "backoffMultiplier": 2,
      "retryableStatusCodes": ["UNAVAILABLE"]
    }
  }]
}`

func Env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func Connect(address string) (*grpc.ClientConn, error) {
	return grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultServiceConfig(defaultServiceConfig),
		grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			ctx = outgoingRequestContext(ctx)
			ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			return invoke(ctx, method, req, reply, cc, opts...)
		}),
		grpc.WithStreamInterceptor(func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
			return streamer(outgoingRequestContext(ctx), desc, cc, method, opts...)
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

func (h *healthService) state(ctx context.Context) healthpb.HealthCheckResponse_ServingStatus {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if h.check(ctx) != nil {
		return healthpb.HealthCheckResponse_NOT_SERVING
	}
	return healthpb.HealthCheckResponse_SERVING
}

func (h *healthService) Check(ctx context.Context, r *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	if r.Service != "" {
		return nil, status.Error(codes.NotFound, "unknown service")
	}
	return &healthpb.HealthCheckResponse{Status: h.state(ctx)}, nil
}

func (h *healthService) List(ctx context.Context, _ *healthpb.HealthListRequest) (*healthpb.HealthListResponse, error) {
	return &healthpb.HealthListResponse{Statuses: map[string]*healthpb.HealthCheckResponse{
		"": {Status: h.state(ctx)},
	}}, nil
}

func (h *healthService) Watch(r *healthpb.HealthCheckRequest, stream grpc.ServerStreamingServer[healthpb.HealthCheckResponse]) error {
	if r.Service != "" {
		if err := stream.Send(&healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVICE_UNKNOWN}); err != nil {
			return err
		}
		<-stream.Context().Done()
		return stream.Context().Err()
	}
	current := h.state(stream.Context())
	if err := stream.Send(&healthpb.HealthCheckResponse{Status: current}); err != nil {
		return err
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case <-ticker.C:
			next := h.state(stream.Context())
			if next != current {
				if err := stream.Send(&healthpb.HealthCheckResponse{Status: next}); err != nil {
					return err
				}
				current = next
			}
		}
	}
}

func ServeGRPC(ctx context.Context, address string, register func(*grpc.Server), check func(context.Context) error) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx, requestID := incomingRequestContext(ctx)
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		start := time.Now()
		result, err := handler(ctx, req)
		slog.InfoContext(ctx, "grpc request", "request_id", requestID, "method", info.FullMethod, "code", status.Code(err).String(), "duration", time.Since(start))
		return result, err
	}), grpc.StreamInterceptor(func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, requestID := incomingRequestContext(stream.Context())
		wrapped := &contextServerStream{ServerStream: stream, ctx: ctx}
		start := time.Now()
		err := handler(srv, wrapped)
		slog.InfoContext(ctx, "grpc stream", "request_id", requestID, "method", info.FullMethod, "code", status.Code(err).String(), "duration", time.Since(start))
		return err
	}))
	register(server)
	healthpb.RegisterHealthServer(server, &healthService{check: check})
	if Env("GRPC_REFLECTION", "true") == "true" {
		reflection.Register(server)
	}
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

type contextServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *contextServerStream) Context() context.Context { return s.ctx }
