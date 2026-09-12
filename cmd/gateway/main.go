package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	pb "github.com/tsostanov/SeatFlow/gen/booking/v1"
	"github.com/tsostanov/SeatFlow/internal/gateway"
	"github.com/tsostanov/SeatFlow/internal/platform"
	"google.golang.org/grpc"
)

func main() {
	if err := run(); err != nil {
		slog.Error("gateway stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	inventory, err := platform.Connect(platform.Env("INVENTORY_ADDR", "localhost:50051"))
	if err != nil {
		return err
	}
	defer inventory.Close()
	booking, err := platform.Connect(platform.Env("BOOKING_ADDR", "localhost:50052"))
	if err != nil {
		return err
	}
	defer booking.Close()
	payment, err := platform.Connect(platform.Env("PAYMENT_ADDR", "localhost:50053"))
	if err != nil {
		return err
	}
	defer payment.Close()
	notifications, err := platform.Connect(platform.Env("NOTIFICATION_ADDR", "localhost:50054"))
	if err != nil {
		return err
	}
	defer notifications.Close()
	handler := gateway.New(pb.NewBookingServiceClient(booking), pb.NewInventoryServiceClient(inventory), pb.NewPaymentServiceClient(payment), pb.NewNotificationServiceClient(notifications), func(ctx context.Context) error {
		for _, conn := range []*grpc.ClientConn{inventory, booking, payment, notifications} {
			if err := platform.CheckConnection(ctx, conn); err != nil {
				return err
			}
		}
		return nil
	})
	server := &http.Server{
		Addr:              platform.Env("HTTP_ADDR", "127.0.0.1:8080"),
		Handler:           handler,
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
			return
		}
		shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		server.Shutdown(shutdown)
	}()
	slog.Info("HTTP listening", "address", server.Addr)
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
