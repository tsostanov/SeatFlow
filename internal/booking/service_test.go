package booking

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	pb "github.com/tsostanov/SeatFlow/gen/booking/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type inventoryStub struct {
	pb.InventoryServiceClient
	get        []*pb.Booking
	confirm    *pb.Booking
	confirmErr error
	getCalls   int
}

func (s *inventoryStub) GetBooking(context.Context, *pb.BookingRequest, ...grpc.CallOption) (*pb.Booking, error) {
	result := s.get[s.getCalls]
	s.getCalls++
	return result, nil
}

func (s *inventoryStub) Confirm(context.Context, *pb.BookingRequest, ...grpc.CallOption) (*pb.Booking, error) {
	return s.confirm, s.confirmErr
}

type paymentStub struct {
	pb.PaymentServiceClient
	status  string
	refunds int
}

func (s *paymentStub) Process(context.Context, *pb.PaymentRequest, ...grpc.CallOption) (*pb.Payment, error) {
	return &pb.Payment{Status: s.status}, nil
}

func (s *paymentStub) Refund(context.Context, *pb.PaymentLookupRequest, ...grpc.CallOption) (*pb.Payment, error) {
	s.refunds++
	return &pb.Payment{Status: "REFUNDED"}, nil
}

type notificationStub struct {
	pb.NotificationServiceClient
	events []string
}

func (s *notificationStub) Send(_ context.Context, r *pb.NotificationRequest, _ ...grpc.CallOption) (*pb.Notification, error) {
	s.events = append(s.events, r.EventType)
	return &pb.Notification{BookingId: r.BookingId, EventType: r.EventType}, nil
}

func booking(statusValue string) *pb.Booking {
	return &pb.Booking{Id: uuid.NewString(), EventId: 1, SeatId: 1, Status: statusValue, PriceMinor: 150000, Currency: "RUB"}
}

func TestCheckoutRefundsWhenConfirmationDidNotCommit(t *testing.T) {
	reserved := booking("RESERVED")
	cancelled := &pb.Booking{Id: reserved.Id, Status: "CANCELLED"}
	inventory := &inventoryStub{get: []*pb.Booking{reserved, cancelled}, confirmErr: status.Error(codes.FailedPrecondition, "booking is CANCELLED")}
	payment := &paymentStub{status: "SUCCEEDED"}
	service := New(inventory, payment, &notificationStub{}, time.Minute)

	_, err := service.Checkout(context.Background(), &pb.CheckoutRequest{BookingId: reserved.Id, PaymentResult: "success"})
	if status.Code(err) != codes.FailedPrecondition || payment.refunds != 1 {
		t.Fatalf("error=%v refunds=%d", err, payment.refunds)
	}
}

func TestCheckoutReconcilesCommittedConfirmationBeforeRefund(t *testing.T) {
	reserved := booking("RESERVED")
	sold := &pb.Booking{Id: reserved.Id, Status: "SOLD"}
	inventory := &inventoryStub{get: []*pb.Booking{reserved, sold}, confirmErr: status.Error(codes.DeadlineExceeded, "response lost")}
	payment := &paymentStub{status: "SUCCEEDED"}
	notifications := &notificationStub{}
	service := New(inventory, payment, notifications, time.Minute)

	result, err := service.Checkout(context.Background(), &pb.CheckoutRequest{BookingId: reserved.Id, PaymentResult: "success"})
	if err != nil || result.Status != "SOLD" || payment.refunds != 0 || len(notifications.events) != 1 || notifications.events[0] != "SOLD" {
		t.Fatalf("result=%v error=%v refunds=%d notifications=%v", result, err, payment.refunds, notifications.events)
	}
}

func TestCheckoutLeavesReservationAfterDecline(t *testing.T) {
	reserved := booking("RESERVED")
	inventory := &inventoryStub{get: []*pb.Booking{reserved}}
	payment := &paymentStub{status: "DECLINED"}
	service := New(inventory, payment, &notificationStub{}, time.Minute)

	_, err := service.Checkout(context.Background(), &pb.CheckoutRequest{BookingId: reserved.Id, PaymentResult: "fail"})
	if status.Code(err) != codes.FailedPrecondition || inventory.getCalls != 1 || payment.refunds != 0 {
		t.Fatalf("error=%v getCalls=%d refunds=%d", err, inventory.getCalls, payment.refunds)
	}
}
