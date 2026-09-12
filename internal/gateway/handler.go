package gateway

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	pb "github.com/tsostanov/SeatFlow/gen/booking/v1"
	"github.com/tsostanov/SeatFlow/internal/platform"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

//go:embed web/*
var web embed.FS

type Handler struct {
	booking       pb.BookingServiceClient
	inventory     pb.InventoryServiceClient
	payment       pb.PaymentServiceClient
	notifications pb.NotificationServiceClient
	ready         func(context.Context) error
	metrics       *httpMetrics
}

func New(booking pb.BookingServiceClient, inventory pb.InventoryServiceClient, payment pb.PaymentServiceClient, notifications pb.NotificationServiceClient, ready func(context.Context) error) http.Handler {
	h := &Handler{booking: booking, inventory: inventory, payment: payment, notifications: notifications, ready: ready, metrics: newHTTPMetrics()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		data, _ := web.ReadFile("web/index.html")
		w.Write(data)
	})
	for name, contentType := range map[string]string{
		"app.js":                  "text/javascript; charset=utf-8",
		"booking-session.mjs":     "text/javascript; charset=utf-8",
		"seat-recommendation.mjs": "text/javascript; charset=utf-8",
		"ticket-calendar.mjs":     "text/javascript; charset=utf-8",
		"ticket-share.mjs":        "text/javascript; charset=utf-8",
		"styles.css":              "text/css; charset=utf-8",
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
	mux.HandleFunc("GET /metrics", h.metrics.serveHTTP)
	mux.HandleFunc("GET /api/events", h.events)
	mux.HandleFunc("GET /api/events/{event}/seats", h.seats)
	mux.HandleFunc("POST /api/bookings", h.create)
	mux.HandleFunc("GET /api/bookings/{id}", h.get)
	mux.HandleFunc("GET /api/bookings/{id}/history", h.history)
	mux.HandleFunc("GET /api/bookings/{id}/payment", h.paymentDetails)
	mux.HandleFunc("GET /api/bookings/{id}/notifications", h.notificationList)
	mux.HandleFunc("DELETE /api/bookings/{id}", h.cancel)
	mux.HandleFunc("POST /api/bookings/{id}/checkout", h.checkout)
	root := http.NewServeMux()
	root.HandleFunc("GET /api/events/{event}/seats/stream", func(w http.ResponseWriter, r *http.Request) {
		observed := &statusResponseWriter{ResponseWriter: w}
		started := time.Now()
		h.seatStream(observed, r)
		duration := time.Since(started)
		h.metrics.observe(r.Method, "/api/events/{event}/seats/stream", observed.statusCode(), duration)
		logHTTPRequest(r.Context(), r.Method, "/api/events/{event}/seats/stream", observed.statusCode(), duration)
	})
	root.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		request := r.WithContext(ctx)
		observed := &statusResponseWriter{ResponseWriter: w}
		started := time.Now()
		mux.ServeHTTP(observed, request)
		duration := time.Since(started)
		route := request.Pattern
		if _, path, found := strings.Cut(route, " "); found {
			route = path
		}
		if route == "" {
			route = "unmatched"
		}
		if request.Pattern != "GET /metrics" {
			h.metrics.observe(r.Method, route, observed.statusCode(), duration)
		}
		logHTTPRequest(request.Context(), r.Method, route, observed.statusCode(), duration)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, requestID := platform.ContextWithRequestID(r.Context(), r.Header.Get(platform.RequestIDHeader))
		w.Header().Set(platform.RequestIDHeader, requestID)
		w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; img-src 'self' data:; object-src 'none'; script-src 'self'; style-src 'self'")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(), geolocation=(), microphone=(), payment=()")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")
		root.ServeHTTP(w, r.WithContext(ctx))
	})
}

func logHTTPRequest(ctx context.Context, method, route string, statusCode int, duration time.Duration) {
	slog.InfoContext(ctx, "http request",
		"request_id", platform.RequestID(ctx),
		"method", method,
		"route", route,
		"status", statusCode,
		"duration", duration,
	)
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
	payload := map[string]string{"error": message, "code": status.Code(err).String()}
	for _, detail := range status.Convert(err).Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok && info.Reason != "" {
			payload["reason"] = info.Reason
			break
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(payload)
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
	id, ok := eventID(w, r)
	if !ok {
		return
	}
	result, err := h.inventory.GetAvailability(r.Context(), &pb.AvailabilityRequest{EventId: id})
	respond(w, result, err)
}

func eventID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("event"), 10, 64)
	if err != nil || id <= 0 {
		fail(w, status.Error(codes.InvalidArgument, "event must be a positive integer"))
		return 0, false
	}
	return id, true
}

func (h *Handler) seatStream(w http.ResponseWriter, r *http.Request) {
	id, ok := eventID(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		fail(w, status.Error(codes.Internal, "streaming is unavailable"))
		return
	}

	stream, err := h.inventory.WatchAvailability(r.Context(), &pb.AvailabilityRequest{EventId: id})
	if err != nil {
		fail(w, err)
		return
	}
	initial, err := stream.Recv()
	if err != nil {
		respond(w, initial, err)
		return
	}
	closeMetric := h.metrics.openStream()
	defer closeMetric()

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Time{})

	var previous []byte
	send := func(value *pb.AvailabilityResponse) error {
		data, err := (protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}).Marshal(value)
		if err != nil {
			return err
		}
		if bytes.Equal(data, previous) {
			return nil
		}
		if err := controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil && err != http.ErrNotSupported {
			return err
		}
		if _, err := fmt.Fprintf(w, "event: seats\ndata: %s\n\n", data); err != nil {
			return err
		}
		flusher.Flush()
		_ = controller.SetWriteDeadline(time.Time{})
		previous = append(previous[:0], data...)
		return nil
	}
	if err := send(initial); err != nil {
		return
	}

	type availabilityUpdate struct {
		value *pb.AvailabilityResponse
		err   error
	}
	updates := make(chan availabilityUpdate, 1)
	go func() {
		for {
			value, err := stream.Recv()
			select {
			case updates <- availabilityUpdate{value: value, err: err}:
			case <-r.Context().Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case update := <-updates:
			if update.err != nil || send(update.value) != nil {
				return
			}
		case <-keepAlive.C:
			if _, err := io.WriteString(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
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

func (h *Handler) history(w http.ResponseWriter, r *http.Request) {
	result, err := h.booking.History(r.Context(), &pb.BookingRequest{BookingId: r.PathValue("id")})
	respond(w, result, err)
}

func (h *Handler) paymentDetails(w http.ResponseWriter, r *http.Request) {
	result, err := h.payment.Get(r.Context(), &pb.PaymentLookupRequest{BookingId: r.PathValue("id")})
	respond(w, result, err)
}

func (h *Handler) notificationList(w http.ResponseWriter, r *http.Request) {
	if _, err := h.booking.Get(r.Context(), &pb.BookingRequest{BookingId: r.PathValue("id")}); err != nil {
		fail(w, err)
		return
	}
	result, err := h.notifications.List(r.Context(), &pb.BookingRequest{BookingId: r.PathValue("id")})
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
