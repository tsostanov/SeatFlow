package booking

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	pb "github.com/tsostanov/SeatFlow/gen/booking/v1"
	"github.com/tsostanov/SeatFlow/internal/platform"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Service struct {
	pb.UnimplementedBookingServiceServer
	inventory     pb.InventoryServiceClient
	payment       pb.PaymentServiceClient
	notifications pb.NotificationServiceClient
	ttl           time.Duration
}

func New(inventory pb.InventoryServiceClient, payment pb.PaymentServiceClient, notifications pb.NotificationServiceClient, ttl time.Duration) *Service {
	return &Service{inventory: inventory, payment: payment, notifications: notifications, ttl: ttl}
}

func (s *Service) Create(ctx context.Context, r *pb.CreateBookingRequest) (*pb.Booking, error) {
	key, err := uuid.Parse(r.IdempotencyKey)
	if r.EventId <= 0 || r.SeatId <= 0 || err != nil || key == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "positive event_id/seat_id and nonzero UUID Idempotency-Key required")
	}
	result, err := s.inventory.Reserve(ctx, &pb.ReserveRequest{
		EventId: r.EventId, SeatId: r.SeatId, BookingId: uuid.NewString(),
		IdempotencyKey: key.String(), TtlSeconds: int64(s.ttl / time.Second),
	})
	if err == nil && result.Status == "RESERVED" {
		s.notify(ctx, result.Id, "RESERVED", "Booking reserved; complete payment before it expires.")
	}
	return result, err
}

func (s *Service) Get(ctx context.Context, r *pb.BookingRequest) (*pb.Booking, error) {
	return s.inventory.GetBooking(ctx, r)
}

func (s *Service) History(ctx context.Context, r *pb.BookingRequest) (*pb.BookingHistoryResponse, error) {
	return s.inventory.GetBookingHistory(ctx, r)
}

func (s *Service) Cancel(ctx context.Context, r *pb.BookingRequest) (*pb.Booking, error) {
	result, err := s.inventory.Release(ctx, r)
	if err == nil && result.Status == "CANCELLED" {
		s.notify(ctx, result.Id, "CANCELLED", "Booking cancelled; the seat is available again.")
	}
	return result, err
}

func (s *Service) Checkout(ctx context.Context, r *pb.CheckoutRequest) (*pb.Booking, error) {
	if r.PaymentResult != "success" && r.PaymentResult != "fail" {
		return nil, status.Error(codes.InvalidArgument, "payment_result must be success or fail")
	}
	request := &pb.BookingRequest{BookingId: r.BookingId}
	b, err := s.inventory.GetBooking(ctx, request)
	if err != nil {
		return nil, err
	}
	// Once confirmed, retries cannot turn a successful purchase into a failure.
	if b.Status == "SOLD" {
		s.notify(ctx, b.Id, "SOLD", "Payment accepted; your ticket is confirmed.")
		return b, nil
	}
	if b.Status != "RESERVED" {
		return nil, platform.Error(codes.FailedPrecondition, "booking is "+b.Status, "BOOKING_NOT_PAYABLE", map[string]string{"status": b.Status})
	}
	payment, err := s.payment.Process(ctx, &pb.PaymentRequest{
		BookingId: b.Id, AmountMinor: b.PriceMinor, Currency: b.Currency, PaymentResult: r.PaymentResult,
	})
	if err != nil {
		return nil, err
	}
	if payment.Status == "DECLINED" {
		return nil, platform.Error(codes.FailedPrecondition, "demo payment declined; reservation remains until expiry", "PAYMENT_DECLINED", nil)
	}
	if payment.Status != "SUCCEEDED" {
		return nil, status.Errorf(codes.FailedPrecondition, "payment is %s", payment.Status)
	}
	result, err := s.inventory.Confirm(ctx, request)
	if err != nil {
		// A timeout may hide a committed confirmation. Reconcile before compensating
		// so a sold seat is never paired with a refunded payment.
		reconcileCtx, stopReconcile := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		current, reconcileErr := s.inventory.GetBooking(reconcileCtx, request)
		stopReconcile()
		if reconcileErr == nil && current.Status == "SOLD" {
			s.notify(ctx, current.Id, "SOLD", "Payment accepted; your ticket is confirmed.")
			return current, nil
		}
		if reconcileErr != nil {
			slog.ErrorContext(ctx, "booking reconciliation failed", "booking_id", b.Id, "error", reconcileErr)
			return nil, err
		}
		compensation, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if _, refundErr := s.payment.Refund(compensation, &pb.PaymentLookupRequest{BookingId: b.Id}); refundErr != nil {
			slog.ErrorContext(ctx, "payment compensation failed", "booking_id", b.Id, "error", refundErr)
		}
		return nil, err
	}
	s.notify(ctx, result.Id, "SOLD", "Payment accepted; your ticket is confirmed.")
	return result, nil
}

func (s *Service) notify(ctx context.Context, bookingID, eventType, message string) {
	if _, err := s.notifications.Send(ctx, &pb.NotificationRequest{BookingId: bookingID, EventType: eventType, Message: message}); err != nil {
		// Notifications do not roll back a completed booking transition.
		slog.WarnContext(ctx, "notification delivery failed", "booking_id", bookingID, "event_type", eventType, "error", err)
	}
}
