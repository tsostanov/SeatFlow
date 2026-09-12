package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	pb "github.com/tsostanov/SeatFlow/gen/booking/v1"
	"github.com/tsostanov/SeatFlow/internal/booking"
	"github.com/tsostanov/SeatFlow/internal/platform"
	"google.golang.org/grpc"
)

func main() {
	if err := run(); err != nil {
		slog.Error("booking stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ttl, err := time.ParseDuration(platform.Env("BOOKING_TTL", "10m"))
	if err != nil || ttl < time.Second || ttl > 24*time.Hour || ttl%time.Second != 0 {
		return fmt.Errorf("BOOKING_TTL must be a whole number of seconds from 1s to 24h")
	}
	inventoryConn, err := platform.Connect(platform.Env("INVENTORY_ADDR", "localhost:50051"))
	if err != nil {
		return err
	}
	defer inventoryConn.Close()
	paymentConn, err := platform.Connect(platform.Env("PAYMENT_ADDR", "localhost:50053"))
	if err != nil {
		return err
	}
	defer paymentConn.Close()
	notificationConn, err := platform.Connect(platform.Env("NOTIFICATION_ADDR", "localhost:50054"))
	if err != nil {
		return err
	}
	defer notificationConn.Close()
	svc := booking.New(pb.NewInventoryServiceClient(inventoryConn), pb.NewPaymentServiceClient(paymentConn), pb.NewNotificationServiceClient(notificationConn), ttl)
	return platform.ServeGRPC(ctx, platform.Env("GRPC_ADDR", "127.0.0.1:50052"), func(s *grpc.Server) { pb.RegisterBookingServiceServer(s, svc) }, func(ctx context.Context) error {
		for _, conn := range []*grpc.ClientConn{inventoryConn, paymentConn, notificationConn} {
			if err := platform.CheckConnection(ctx, conn); err != nil {
				return err
			}
		}
		return nil
	})
}
