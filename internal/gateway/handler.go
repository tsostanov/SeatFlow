package gateway

import (
	"context"
	"embed"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	pb "github.com/tsostanov/SeatFlow/gen/booking/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

//go:embed web/*
var web embed.FS

type Handler struct {
	booking   pb.BookingServiceClient
	inventory pb.InventoryServiceClient
	ready     func(context.Context) error
}

func New(booking pb.BookingServiceClient, inventory pb.InventoryServiceClient, ready func(context.Context) error) http.Handler {
	h := &Handler{booking: booking, inventory: inventory, ready: ready}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		data, _ := web.ReadFile("web/index.html")
		w.Write(data)
	})
	for name, contentType := range map[string]string{
		"app.js":              "text/javascript; charset=utf-8",
		"booking-session.mjs": "text/javascript; charset=utf-8",
		"ticket-calendar.mjs": "text/javascript; charset=utf-8",
		"styles.css":          "text/css; charset=utf-8",
	} {
		mux.HandleFunc("GET /"+name, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", contentType)
			data, _ := web.ReadFile("web/" + name)
			w.Write(data)
		})
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := h.ready(r.Context()); err != nil {
			fail(w, status.Error(codes.Unavailable, "dependencies are not ready"))
			return
		}
		w.Write([]byte("ready\n"))
	})
	mux.HandleFunc("GET /api/events", h.events)
	mux.HandleFunc("GET /api/events/{event}/seats", h.seats)
	mux.HandleFunc("POST /api/bookings", h.create)
	mux.HandleFunc("GET /api/bookings/{id}", h.get)
	mux.HandleFunc("DELETE /api/bookings/{id}", h.cancel)
	mux.HandleFunc("POST /api/bookings/{id}/checkout", h.checkout)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

func respond(w http.ResponseWriter, message proto.Message, err error) {
	if err != nil {
		fail(w, err)
		return
	}
	data, err := (protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}).Marshal(message)
	if err != nil {
		fail(w, status.Error(codes.Internal, "response encoding failed"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func fail(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	switch status.Code(err) {
	case codes.InvalidArgument:
		code = http.StatusBadRequest
	case codes.NotFound:
		code = http.StatusNotFound
	case codes.AlreadyExists, codes.FailedPrecondition, codes.Aborted:
		code = http.StatusConflict
	case codes.Unauthenticated:
		code = http.StatusUnauthorized
	case codes.PermissionDenied:
		code = http.StatusForbidden
	case codes.ResourceExhausted:
		code = http.StatusTooManyRequests
	case codes.Unavailable:
		code = http.StatusServiceUnavailable
	case codes.DeadlineExceeded:
		code = http.StatusGatewayTimeout
	case codes.Canceled:
		code = http.StatusRequestTimeout
	}
	message := status.Convert(err).Message()
	if code >= 500 {
		message = http.StatusText(code)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message, "code": status.Code(err).String()})
}

func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		fail(w, status.Error(codes.InvalidArgument, "invalid JSON body"))
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		fail(w, status.Error(codes.InvalidArgument, "expected exactly one JSON object"))
		return false
	}
	return true
}

func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	result, err := h.inventory.ListEvents(r.Context(), &pb.Empty{})
	respond(w, result, err)
}

func (h *Handler) seats(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("event"), 10, 64)
	if err != nil || id <= 0 {
		fail(w, status.Error(codes.InvalidArgument, "event must be a positive integer"))
		return
	}
	result, err := h.inventory.GetAvailability(r.Context(), &pb.AvailabilityRequest{EventId: id})
	respond(w, result, err)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		EventID int64 `json:"event_id"`
		SeatID  int64 `json:"seat_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	result, err := h.booking.Create(r.Context(), &pb.CreateBookingRequest{EventId: body.EventID, SeatId: body.SeatID, IdempotencyKey: r.Header.Get("Idempotency-Key")})
	respond(w, result, err)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	result, err := h.booking.Get(r.Context(), &pb.BookingRequest{BookingId: r.PathValue("id")})
	respond(w, result, err)
}

func (h *Handler) cancel(w http.ResponseWriter, r *http.Request) {
	result, err := h.booking.Cancel(r.Context(), &pb.BookingRequest{BookingId: r.PathValue("id")})
	respond(w, result, err)
}

func (h *Handler) checkout(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PaymentResult string `json:"payment_result"`
	}
	if !decode(w, r, &body) {
		return
	}
	result, err := h.booking.Checkout(r.Context(), &pb.CheckoutRequest{BookingId: r.PathValue("id"), PaymentResult: body.PaymentResult})
	respond(w, result, err)
}
