package payment

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
	"github.com/tsostanov/SeatFlow/internal/platform"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

//go:embed schema.sql
var schema string

type Service struct {
	pb.UnimplementedPaymentServiceServer
	db *pgxpool.Pool
}

func New(ctx context.Context, db *pgxpool.Pool) (*Service, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(82714002)"); err != nil {
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
		return status.Error(codes.NotFound, "payment not found")
	}
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, "request cancelled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "request timed out")
	}
	slog.Error("payment database operation failed", "error", err)
	return status.Error(codes.Internal, "payment database operation failed")
}

func validBookingID(value string) (string, bool) {
	id, err := uuid.Parse(value)
	return id.String(), err == nil && id != uuid.Nil
}

func validCurrency(value string) bool {
	if len(value) != 3 {
		return false
	}
	for _, character := range value {
		if character < 'A' || character > 'Z' {
			return false
		}
	}
	return true
}

func scan(row pgx.Row) (*pb.Payment, error) {
	result := &pb.Payment{}
	var processed time.Time
	if err := row.Scan(&result.BookingId, &result.AmountMinor, &result.Currency, &result.Status, &processed); err != nil {
		return nil, databaseError(err)
	}
	result.ProcessedAt = processed.UTC().Format(time.RFC3339Nano)
	return result, nil
}

const columns = `booking_id::text, amount_minor, currency, status, processed_at`

func (s *Service) Process(ctx context.Context, r *pb.PaymentRequest) (*pb.Payment, error) {
	bookingID, valid := validBookingID(r.BookingId)
	if !valid || r.AmountMinor <= 0 || !validCurrency(r.Currency) || (r.PaymentResult != "success" && r.PaymentResult != "fail") {
		return nil, status.Error(codes.InvalidArgument, "booking_id, positive amount, uppercase 3-letter currency and payment_result success/fail required")
	}
	currency := r.Currency
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, databaseError(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 1))", bookingID); err != nil {
		return nil, databaseError(err)
	}
	existing, err := scan(tx.QueryRow(ctx, "SELECT "+columns+" FROM payments WHERE booking_id=$1", bookingID))
	if err == nil {
		if existing.AmountMinor != r.AmountMinor || existing.Currency != currency {
			return nil, platform.Error(codes.AlreadyExists, "booking already has a payment with different amount or currency", "PAYMENT_PARAMETERS_MISMATCH", nil)
		}
		if existing.Status == "DECLINED" && r.PaymentResult == "success" {
			existing, err = scan(tx.QueryRow(ctx, `UPDATE payments SET status='SUCCEEDED',processed_at=clock_timestamp()
 WHERE booking_id=$1 RETURNING `+columns, bookingID))
		}
	} else if status.Code(err) == codes.NotFound {
		paymentStatus := "DECLINED"
		if r.PaymentResult == "success" {
			paymentStatus = "SUCCEEDED"
		}
		existing, err = scan(tx.QueryRow(ctx, `INSERT INTO payments(booking_id,amount_minor,currency,status)
 VALUES($1,$2,$3,$4) RETURNING `+columns, bookingID, r.AmountMinor, currency, paymentStatus))
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(err)
	}
	return existing, nil
}

func (s *Service) Get(ctx context.Context, r *pb.PaymentLookupRequest) (*pb.Payment, error) {
	bookingID, valid := validBookingID(r.BookingId)
	if !valid {
		return nil, status.Error(codes.InvalidArgument, "booking_id must be a nonzero UUID")
	}
	return scan(s.db.QueryRow(ctx, "SELECT "+columns+" FROM payments WHERE booking_id=$1", bookingID))
}

func (s *Service) Refund(ctx context.Context, r *pb.PaymentLookupRequest) (*pb.Payment, error) {
	bookingID, valid := validBookingID(r.BookingId)
	if !valid {
		return nil, status.Error(codes.InvalidArgument, "booking_id must be a nonzero UUID")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, databaseError(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 1))", bookingID); err != nil {
		return nil, databaseError(err)
	}
	result, err := scan(tx.QueryRow(ctx, `UPDATE payments SET status='REFUNDED',processed_at=clock_timestamp()
 WHERE booking_id=$1 AND status='SUCCEEDED' RETURNING `+columns, bookingID))
	if status.Code(err) == codes.NotFound {
		result, err = scan(tx.QueryRow(ctx, "SELECT "+columns+" FROM payments WHERE booking_id=$1", bookingID))
		if err == nil && result.Status == "DECLINED" {
			return nil, status.Error(codes.FailedPrecondition, "declined payment cannot be refunded")
		}
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(err)
	}
	return result, nil
}
