package notification

import (
	"context"
	_ "embed"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pb "github.com/tsostanov/SeatFlow/gen/booking/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

//go:embed schema.sql
var schema string

type Service struct {
	pb.UnimplementedNotificationServiceServer
	db *pgxpool.Pool
}

func New(ctx context.Context, db *pgxpool.Pool) (*Service, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(82714003)"); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, schema); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &Service{db: db}, nil
}

func databaseError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return status.Error(codes.NotFound, "notification not found")
	}
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, "request cancelled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "request timed out")
	}
	slog.Error("notification database operation failed", "error", err)
	return status.Error(codes.Internal, "notification database operation failed")
}

func bookingID(value string) (string, bool) {
	id, err := uuid.Parse(value)
	return id.String(), err == nil && id != uuid.Nil
}

func scan(row pgx.Row) (*pb.Notification, error) {
	result := &pb.Notification{}
	var created time.Time
	if err := row.Scan(&result.BookingId, &result.EventType, &result.Message, &created); err != nil {
		return nil, databaseError(err)
	}
	result.CreatedAt = created.UTC().Format(time.RFC3339Nano)
	return result, nil
}

const columns = `booking_id::text, event_type, message, created_at`

func (s *Service) Send(ctx context.Context, r *pb.NotificationRequest) (*pb.Notification, error) {
	id, valid := bookingID(r.BookingId)
	message := strings.TrimSpace(r.Message)
	if !valid || (r.EventType != "RESERVED" && r.EventType != "SOLD" && r.EventType != "CANCELLED") || message == "" || len(message) > 500 {
		return nil, status.Error(codes.InvalidArgument, "booking_id, supported event_type and message up to 500 bytes required")
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO notifications(booking_id,event_type,message)
 VALUES($1,$2,$3) ON CONFLICT (booking_id,event_type) DO NOTHING`, id, r.EventType, message); err != nil {
		return nil, databaseError(err)
	}
	return scan(s.db.QueryRow(ctx, "SELECT "+columns+" FROM notifications WHERE booking_id=$1 AND event_type=$2", id, r.EventType))
}

func (s *Service) List(ctx context.Context, r *pb.BookingRequest) (*pb.ListNotificationsResponse, error) {
	id, valid := bookingID(r.BookingId)
	if !valid {
		return nil, status.Error(codes.InvalidArgument, "booking_id must be a nonzero UUID")
	}
	rows, err := s.db.Query(ctx, "SELECT "+columns+" FROM notifications WHERE booking_id=$1 ORDER BY created_at,event_type", id)
	if err != nil {
		return nil, databaseError(err)
	}
	defer rows.Close()
	result := &pb.ListNotificationsResponse{}
	for rows.Next() {
		notification, err := scan(rows)
		if err != nil {
			return nil, err
		}
		result.Notifications = append(result.Notifications, notification)
	}
	if rows.Err() != nil {
		return nil, databaseError(rows.Err())
	}
	return result, nil
}
