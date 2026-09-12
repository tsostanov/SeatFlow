package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	pb "github.com/tsostanov/SeatFlow/gen/booking/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type availabilityClient struct {
	pb.InventoryServiceClient
	responses chan *pb.AvailabilityResponse
}

func (c *availabilityClient) GetAvailability(ctx context.Context, _ *pb.AvailabilityRequest, _ ...grpc.CallOption) (*pb.AvailabilityResponse, error) {
	select {
	case response := <-c.responses:
		return response, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestEmbeddedWebAssets(t *testing.T) {
	handler := New(nil, nil, func(context.Context) error { return nil })
	for _, tc := range []struct{ path, contentType, contains string }{
		{"/", "text/html", "SeatFlow"},
		{"/app.js", "text/javascript", "BookingSession"},
		{"/booking-session.mjs", "text/javascript", "seatflow-session-v1"},
		{"/seat-recommendation.mjs", "text/javascript", "recommendSeat"},
		{"/ticket-calendar.mjs", "text/javascript", "BEGIN:VCALENDAR"},
		{"/ticket-share.mjs", "text/javascript", "createTicketShare"},
		{"/styles.css", "text/css", "focus-visible"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
			if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), tc.contentType) || !strings.Contains(w.Body.String(), tc.contains) {
				t.Fatalf("status=%d type=%s", w.Code, w.Header().Get("Content-Type"))
			}
		})
	}
}

func TestSecurityHeaders(t *testing.T) {
	handler := New(nil, nil, func(context.Context) error { return nil })
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	for name, want := range map[string]string{
		"Content-Security-Policy":      "default-src 'self'; base-uri 'none'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; img-src 'self' data:; object-src 'none'; script-src 'self'; style-src 'self'",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Permissions-Policy":           "camera=(), geolocation=(), microphone=(), payment=()",
		"Referrer-Policy":              "no-referrer",
		"X-Content-Type-Options":       "nosniff",
		"X-Frame-Options":              "DENY",
	} {
		if got := w.Header().Get(name); got != want {
			t.Errorf("%s=%q want=%q", name, got, want)
		}
	}
}

func TestDecodeRejectsAmbiguousOrOversizedBodies(t *testing.T) {
	for _, body := range []string{`{"seat":1,"extra":2}`, `{"seat":1} {"seat":2}`, `{"seat":"one"}`, `{"seat":`, strings.Repeat(" ", 4097) + `{"seat":1}`} {
		var target struct {
			Seat int `json:"seat"`
		}
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		w := httptest.NewRecorder()
		if decode(w, r, &target) || w.Code != 400 {
			t.Fatalf("accepted invalid body (length %d)", len(body))
		}
	}
}

func TestInternalErrorsDoNotLeakDetails(t *testing.T) {
	w := httptest.NewRecorder()
	fail(w, status.Error(codes.Internal, "password=secret SQL table bookings"))
	if w.Code != 500 || strings.Contains(w.Body.String(), "secret") {
		t.Fatal(w.Body.String())
	}
}

func TestMetricsUseRoutePatternsAndExcludeScrapes(t *testing.T) {
	handler := New(nil, nil, func(context.Context) error { return nil })
	for _, path := range []string{"/healthz", "/not-found"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := w.Body.String()
	for _, want := range []string{
		`seatflow_http_requests_total{method="GET",route="/healthz",status="200"} 1`,
		`seatflow_http_requests_total{method="GET",route="unmatched",status="404"} 1`,
		`seatflow_http_request_duration_seconds_count{method="GET",route="/healthz"} 1`,
		`seatflow_sse_connections_active 0`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `route="/metrics"`) {
		t.Fatalf("metrics endpoint observed itself:\n%s", body)
	}
	if got := w.Header().Get("Content-Type"); got != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("content type=%q", got)
	}
}

func TestRequestIDIsValidatedAndLogged(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	handler := New(nil, nil, func(context.Context) error { return nil })
	validID := "C73A33C3-8DB7-4AC4-80D1-10A04705DA7E"
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.Header.Set("X-Request-ID", validID)
	handler.ServeHTTP(w, r)
	if got := w.Header().Get("X-Request-ID"); got != strings.ToLower(validID) {
		t.Fatalf("request ID=%q", got)
	}

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["msg"] != "http request" || entry["request_id"] != strings.ToLower(validID) ||
		entry["method"] != "GET" || entry["route"] != "/healthz" || entry["status"] != float64(200) {
		t.Fatalf("unexpected access log: %v", entry)
	}

	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.Header.Set("X-Request-ID", "not-a-safe-request-id\nforged")
	handler.ServeHTTP(w, r)
	if _, err := uuid.Parse(w.Header().Get("X-Request-ID")); err != nil {
		t.Fatalf("generated request ID is invalid: %q", w.Header().Get("X-Request-ID"))
	}
}

func TestSeatStreamPublishesAvailabilityChanges(t *testing.T) {
	client := &availabilityClient{responses: make(chan *pb.AvailabilityResponse, 2)}
	client.responses <- &pb.AvailabilityResponse{SeatIds: []int64{1, 2}, AvailableSeatIds: []int64{1, 2}}
	server := httptest.NewServer(New(nil, client, func(context.Context) error { return nil }))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/events/1/seats/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream; charset=utf-8" {
		t.Fatalf("status=%d type=%q", response.StatusCode, response.Header.Get("Content-Type"))
	}

	lines := make(chan string, 2)
	go func() {
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "data: ") {
				lines <- scanner.Text()
			}
		}
	}()
	waitFor := func(want []string) {
		t.Helper()
		select {
		case line := <-lines:
			var event struct {
				AvailableSeatIDs []string `json:"available_seat_ids"`
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(event.AvailableSeatIDs, want) {
				t.Fatalf("available seats=%v want=%v", event.AvailableSeatIDs, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for seat event")
		}
	}
	waitFor([]string{"1", "2"})
	client.responses <- &pb.AvailabilityResponse{SeatIds: []int64{1, 2}, AvailableSeatIds: []int64{2}}
	waitFor([]string{"2"})
}
