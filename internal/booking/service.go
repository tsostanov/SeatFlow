package booking

import (
	"context"
	"time"

	"github.com/google/uuid"
	pb "github.com/tsostanov/SeatFlow/gen/booking/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Service struct {
	pb.UnimplementedBookingServiceServer
	inventory pb.InventoryServiceClient
	ttl       time.Duration
}

func New(inventory pb.InventoryServiceClient, ttl time.Duration) *Service {
	return &Service{inventory: inventory, ttl: ttl}
}

func (s *Service) Create(ctx context.Context, r *pb.CreateBookingRequest) (*pb.Booking, error) {
	key, err := uuid.Parse(r.IdempotencyKey)
	if r.EventId <= 0 || r.SeatId <= 0 || err != nil || key == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "positive event_id/seat_id and nonzero UUID Idempotency-Key required")
	}
	return s.inventory.Reserve(ctx, &pb.ReserveRequest{
		EventId: r.EventId, SeatId: r.SeatId, BookingId: uuid.NewString(),
		IdempotencyKey: key.String(), TtlSeconds: int64(s.ttl / time.Second),
	})
}

func (s *Service) Get(ctx context.Context, r *pb.BookingRequest) (*pb.Booking, error) {
	return s.inventory.GetBooking(ctx, r)
}

func (s *Service) Cancel(ctx context.Context, r *pb.BookingRequest) (*pb.Booking, error) {
	return s.inventory.Release(ctx, r)
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
		return b, nil
	}
	if b.Status != "RESERVED" {
		return nil, status.Errorf(codes.FailedPrecondition, "booking is %s", b.Status)
	}
	if r.PaymentResult == "fail" {
		return nil, status.Error(codes.FailedPrecondition, "demo payment declined; reservation remains until expiry")
	}
	return s.inventory.Confirm(ctx, request)
}
