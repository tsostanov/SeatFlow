package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	pb "github.com/tsostanov/SeatFlow/gen/booking/v1"
	"github.com/tsostanov/SeatFlow/internal/inventory"
	"github.com/tsostanov/SeatFlow/internal/platform"
	"google.golang.org/grpc"
)

func main() {
	if err := run(); err != nil {
		slog.Error("inventory stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	startup, stop := context.WithTimeout(ctx, 15*time.Second)
	defer stop()
	db, err := pgxpool.New(startup, platform.Env("DATABASE_URL", "postgres://booking:booking@localhost:5432/booking?sslmode=disable"))
	if err != nil {
		return err
	}
	defer db.Close()
	svc, err := inventory.New(startup, db, platform.Env("SEED_DEMO", "true") == "true")
	if err != nil {
		return err
	}
	go svc.RunExpiryWorker(ctx, time.Second)
	return platform.ServeGRPC(ctx, platform.Env("GRPC_ADDR", "127.0.0.1:50051"), func(s *grpc.Server) { pb.RegisterInventoryServiceServer(s, svc) }, db.Ping)
}
