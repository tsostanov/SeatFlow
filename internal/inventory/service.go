package inventory

import (
	"context"
	_ "embed"
	"errors"
	"log/slog"
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

//go:embed seed.sql
var seed string

type Service struct {
	pb.UnimplementedInventoryServiceServer
	db *pgxpool.Pool
}

func New(ctx context.Context, db *pgxpool.Pool, seedDemo bool) (*Service, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// Serialize initialization when several replicas start together.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(82714001)"); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, schema); err != nil {
		return nil, err
	}
	if seedDemo {
		if _, err = tx.Exec(ctx, seed); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &Service{db: db}, nil
}

func dbError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return status.Error(codes.NotFound, "resource not found")
	}
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, "request cancelled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "request timed out")
	}
	slog.Error("database operation failed", "error", err)
	return status.Error(codes.Internal, "database operation failed")
}

func validID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil
}

const bookingColumns = `id::text, event_id, seat_id,
 CASE WHEN status = 'RESERVED' AND expires_at <= clock_timestamp() THEN 'EXPIRED' ELSE status END,
 expires_at, price_minor, currency, created_at`

func scanBooking(row pgx.Row) (*pb.Booking, error) {
	b := &pb.Booking{}
	var expires, created time.Time
	err := row.Scan(&b.Id, &b.EventId, &b.SeatId, &b.Status, &expires, &b.PriceMinor, &b.Currency, &created)
	if err != nil {
		return nil, dbError(err)
	}
	b.ExpiresAt, b.CreatedAt = expires.UTC().Format(time.RFC3339Nano), created.UTC().Format(time.RFC3339Nano)
	return b, nil
}

func (s *Service) ListEvents(ctx context.Context, _ *pb.Empty) (*pb.ListEventsResponse, error) {
	rows, err := s.db.Query(ctx, "SELECT id, title, venue, starts_at, price_minor, currency FROM events ORDER BY starts_at, id")
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	result := &pb.ListEventsResponse{}
	for rows.Next() {
		e := &pb.Event{}
		var starts time.Time
		if err := rows.Scan(&e.Id, &e.Title, &e.Venue, &starts, &e.PriceMinor, &e.Currency); err != nil {
			return nil, dbError(err)
		}
		e.StartsAt = starts.UTC().Format(time.RFC3339)
		result.Events = append(result.Events, e)
	}
	if rows.Err() != nil {
		return nil, dbError(rows.Err())
	}
	return result, nil
}

func (s *Service) GetAvailability(ctx context.Context, r *pb.AvailabilityRequest) (*pb.AvailabilityResponse, error) {
	if r.EventId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "event_id must be positive")
	}
	var exists bool
	if err := s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM events WHERE id=$1)", r.EventId).Scan(&exists); err != nil {
		return nil, dbError(err)
	}
	if !exists {
		return nil, status.Error(codes.NotFound, "event not found")
	}
	rows, err := s.db.Query(ctx, `SELECT s.id FROM seats s WHERE s.event_id=$1 AND NOT EXISTS (
 SELECT 1 FROM bookings b WHERE b.event_id=s.event_id AND b.seat_id=s.id
 AND (b.status='SOLD' OR (b.status='RESERVED' AND b.expires_at > clock_timestamp()))) ORDER BY s.id`, r.EventId)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	result := &pb.AvailabilityResponse{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, dbError(err)
		}
		result.AvailableSeatIds = append(result.AvailableSeatIds, id)
	}
	if rows.Err() != nil {
		return nil, dbError(rows.Err())
	}
	return result, nil
}

func (s *Service) Reserve(ctx context.Context, r *pb.ReserveRequest) (*pb.Booking, error) {
	if r.EventId <= 0 || r.SeatId <= 0 || !validID(r.BookingId) || !validID(r.IdempotencyKey) || r.TtlSeconds < 1 || r.TtlSeconds > 86400 {
		return nil, status.Error(codes.InvalidArgument, "positive event_id/seat_id, UUID identifiers and TTL 1..86400 required")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, dbError(err)
	}
	defer tx.Rollback(ctx)
	// Serialize the same request even if a client changes the requested seat.
	key := uuid.MustParse(r.IdempotencyKey).String()
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", key); err != nil {
		return nil, dbError(err)
	}
	existing, err := scanBooking(tx.QueryRow(ctx, "SELECT "+bookingColumns+" FROM bookings WHERE idempotency_key=$1", key))
	if err == nil {
		if existing.EventId != r.EventId || existing.SeatId != r.SeatId {
			return nil, status.Error(codes.AlreadyExists, "idempotency key was used with another request")
		}
		return existing, nil
	}
	if status.Code(err) != codes.NotFound {
		return nil, err
	}
	var seat int64
	if err = tx.QueryRow(ctx, "SELECT id FROM seats WHERE event_id=$1 AND id=$2 FOR UPDATE", r.EventId, r.SeatId).Scan(&seat); err != nil {
		return nil, dbError(err)
	}
	// Expire under the same seat lock before the partial unique index is checked.
	if _, err = tx.Exec(ctx, `UPDATE bookings SET status='EXPIRED' WHERE event_id=$1 AND seat_id=$2
 AND status='RESERVED' AND expires_at <= clock_timestamp()`, r.EventId, r.SeatId); err != nil {
		return nil, dbError(err)
	}
	var taken bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM bookings WHERE event_id=$1 AND seat_id=$2 AND status IN ('RESERVED','SOLD'))", r.EventId, r.SeatId).Scan(&taken); err != nil {
		return nil, dbError(err)
	}
	if taken {
		return nil, status.Error(codes.AlreadyExists, "seat is unavailable")
	}
	b, err := scanBooking(tx.QueryRow(ctx, `INSERT INTO bookings(id, idempotency_key, event_id, seat_id, status, expires_at, price_minor, currency)
 SELECT $1,$2,$3,$4,'RESERVED',clock_timestamp()+make_interval(secs => $5),price_minor,currency FROM events WHERE id=$3
 RETURNING `+bookingColumns, r.BookingId, key, r.EventId, r.SeatId, float64(r.TtlSeconds)))
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, dbError(err)
	}
	return b, nil
}

func (s *Service) GetBooking(ctx context.Context, r *pb.BookingRequest) (*pb.Booking, error) {
	if !validID(r.BookingId) {
		return nil, status.Error(codes.InvalidArgument, "booking_id must be a nonzero UUID")
	}
	return scanBooking(s.db.QueryRow(ctx, "SELECT "+bookingColumns+" FROM bookings WHERE id=$1", r.BookingId))
}

func (s *Service) Release(ctx context.Context, r *pb.BookingRequest) (*pb.Booking, error) {
	return s.transition(ctx, r, "CANCELLED")
}

func (s *Service) Confirm(ctx context.Context, r *pb.BookingRequest) (*pb.Booking, error) {
	return s.transition(ctx, r, "SOLD")
}

func (s *Service) transition(ctx context.Context, r *pb.BookingRequest, target string) (*pb.Booking, error) {
	if !validID(r.BookingId) {
		return nil, status.Error(codes.InvalidArgument, "booking_id must be a nonzero UUID")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, dbError(err)
	}
	defer tx.Rollback(ctx)
	var eventID, seatID int64
	if err = tx.QueryRow(ctx, "SELECT event_id, seat_id FROM bookings WHERE id=$1", r.BookingId).Scan(&eventID, &seatID); err != nil {
		return nil, dbError(err)
	}
	if err = tx.QueryRow(ctx, "SELECT id FROM seats WHERE event_id=$1 AND id=$2 FOR UPDATE", eventID, seatID).Scan(&seatID); err != nil {
		return nil, dbError(err)
	}
	// UPDATE obtains the booking lock and evaluates expiry after any wait.
	_, err = tx.Exec(ctx, `UPDATE bookings SET status=CASE WHEN expires_at <= clock_timestamp() THEN 'EXPIRED' ELSE $2 END
 WHERE id=$1 AND status='RESERVED'`, r.BookingId, target)
	if err != nil {
		return nil, dbError(err)
	}
	b, err := scanBooking(tx.QueryRow(ctx, "SELECT "+bookingColumns+" FROM bookings WHERE id=$1", r.BookingId))
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, dbError(err)
	}
	if b.Status != target && !(target == "CANCELLED" && b.Status == "EXPIRED") {
		return nil, status.Errorf(codes.FailedPrecondition, "booking is %s", b.Status)
	}
	return b, nil
}

func (s *Service) Expire(ctx context.Context) (int64, error) {
	result, err := s.db.Exec(ctx, "UPDATE bookings SET status='EXPIRED' WHERE status='RESERVED' AND expires_at <= clock_timestamp()")
	return result.RowsAffected(), err
}

func (s *Service) RunExpiryWorker(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			job, cancel := context.WithTimeout(ctx, 5*time.Second)
			n, err := s.Expire(job)
			cancel()
			if err != nil && ctx.Err() == nil {
				slog.Error("expiry worker failed", "error", err)
			}
			if n > 0 {
				slog.Info("bookings expired", "count", n)
			}
		}
	}
}
