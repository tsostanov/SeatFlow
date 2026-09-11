//go:build integration

package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	pb "github.com/tsostanov/SeatFlow/gen/booking/v1"
	"github.com/tsostanov/SeatFlow/internal/booking"
	"github.com/tsostanov/SeatFlow/internal/gateway"
	"github.com/tsostanov/SeatFlow/internal/inventory"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type harness struct {
	url       string
	db        *pgxpool.Pool
	inventory *inventory.Service
	client    *http.Client
}

func setup(t *testing.T) *harness {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "booking_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.ConnConfig.RuntimeParams["application_name"] = schema
	config.MaxConns = 20
	db, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	inv, err := inventory.New(ctx, db, true)
	if err != nil {
		t.Fatal(err)
	}
	ic := grpcClient(t, func(s *grpc.Server) { pb.RegisterInventoryServiceServer(s, inv) })
	bc := grpcClient(t, func(s *grpc.Server) {
		pb.RegisterBookingServiceServer(s, booking.New(pb.NewInventoryServiceClient(ic), 10*time.Minute))
	})
	server := httptest.NewServer(gateway.New(pb.NewBookingServiceClient(bc), pb.NewInventoryServiceClient(ic), func(context.Context) error { return db.Ping(ctx) }))
	t.Cleanup(server.Close)
	return &harness{url: server.URL, db: db, inventory: inv, client: &http.Client{Timeout: 10 * time.Second}}
}

func grpcClient(t *testing.T, register func(*grpc.Server)) *grpc.ClientConn {
	t.Helper()
	l := bufconn.Listen(1024 * 1024)
	s := grpc.NewServer()
	register(s)
	go s.Serve(l)
	t.Cleanup(s.Stop)
	conn, err := grpc.NewClient("passthrough:///integration", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return l.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func (h *harness) request(method, path, body, key string) (int, map[string]any, error) {
	r, err := http.NewRequest(method, h.url+path, bytes.NewBufferString(body))
	if err != nil {
		return 0, nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	resp, err := h.client.Do(r)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	var result map[string]any
	err = json.NewDecoder(resp.Body).Decode(&result)
	return resp.StatusCode, result, err
}

func (h *harness) expect(t *testing.T, method, path, body, key string, want int) map[string]any {
	t.Helper()
	code, data, err := h.request(method, path, body, key)
	if err != nil || code != want {
		t.Fatalf("%s %s: status=%d want=%d body=%v error=%v", method, path, code, want, data, err)
	}
	return data
}

func (h *harness) reserve(t *testing.T, seat int) map[string]any {
	t.Helper()
	return h.expect(t, "POST", "/api/bookings", fmt.Sprintf(`{"event_id":1,"seat_id":%d}`, seat), uuid.NewString(), 200)
}

func path(b map[string]any) string { return "/api/bookings/" + b["id"].(string) }

func (h *harness) expectHistory(t *testing.T, b map[string]any, want ...string) {
	t.Helper()
	data := h.expect(t, "GET", path(b)+"/history", "", "", 200)
	events, ok := data["events"].([]any)
	if !ok {
		t.Fatalf("invalid history response: %v", data)
	}
	statuses := make([]string, 0, len(events))
	var previous time.Time
	for _, value := range events {
		event, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("invalid history event: %v", value)
		}
		statuses = append(statuses, event["status"].(string))
		occurred, err := time.Parse(time.RFC3339Nano, event["occurred_at"].(string))
		if err != nil || (!previous.IsZero() && occurred.Before(previous)) {
			t.Fatalf("invalid history time %v after %v: %v", occurred, previous, err)
		}
		previous = occurred
	}
	if !slices.Equal(statuses, want) {
		t.Fatalf("history=%v want=%v", statuses, want)
	}
}

func TestPlatform(t *testing.T) {
	h := setup(t)
	t.Run("availability includes actual seat IDs even when occupied", func(t *testing.T) {
		h := setup(t)
		ctx := context.Background()
		if _, err := h.db.Exec(ctx, `INSERT INTO events(id,title,venue,starts_at,price_minor,currency)
 VALUES (4,'Small venue','Test hall',now()+interval '1 day',10000,'RUB'),
 (5,'Empty venue','Test hall',now()+interval '1 day',10000,'RUB');
 INSERT INTO seats(event_id,id) VALUES (4,10),(4,40),(4,99)`); err != nil {
			t.Fatal(err)
		}
		assertSeats := func(available string) {
			t.Helper()
			data := h.expect(t, "GET", "/api/events/4/seats", "", "", 200)
			all, _ := json.Marshal(data["seat_ids"])
			free, _ := json.Marshal(data["available_seat_ids"])
			if string(all) != `["10","40","99"]` || string(free) != available {
				t.Fatalf("all=%s available=%s", all, free)
			}
		}
		assertSeats(`["10","40","99"]`)
		b := h.expect(t, "POST", "/api/bookings", `{"event_id":4,"seat_id":40}`, uuid.NewString(), 200)
		assertSeats(`["10","99"]`)
		h.expect(t, "DELETE", path(b), "", "", 200)
		assertSeats(`["10","40","99"]`)
		empty := h.expect(t, "GET", "/api/events/5/seats", "", "", 200)
		if len(empty["seat_ids"].([]any)) != 0 || len(empty["available_seat_ids"].([]any)) != 0 {
			t.Fatal(empty)
		}
	})
	t.Run("catalog and validation", func(t *testing.T) {
		events := h.expect(t, "GET", "/api/events", "", "", 200)
		if len(events["events"].([]any)) != 3 {
			t.Fatal(events)
		}
		h.expect(t, "GET", "/api/events/999/seats", "", "", 404)
		h.expect(t, "GET", "/api/events/0/seats", "", "", 400)
		h.expect(t, "GET", "/api/bookings/not-a-uuid", "", "", 400)
		h.expect(t, "GET", "/api/bookings/not-a-uuid/history", "", "", 400)
		h.expect(t, "GET", "/api/bookings/"+uuid.NewString()+"/history", "", "", 404)
		h.expect(t, "GET", "/api/bookings/"+uuid.NewString(), "", "", 404)
		h.expect(t, "POST", "/api/bookings", `{"event_id":1,"seat_id":1}`, "", 400)
		h.expect(t, "POST", "/api/bookings", `{"event_id":1,"seat_id":1,"unknown":true}`, uuid.NewString(), 400)
		h.expect(t, "POST", "/api/bookings", `{"event_id":1,"seat_id":1} {}`, uuid.NewString(), 400)
		h.expect(t, "POST", "/api/bookings", `{"event_id":1,"seat_id":999}`, uuid.NewString(), 404)
	})
	t.Run("40 competing requests have exactly one winner", func(t *testing.T) {
		var wg sync.WaitGroup
		results := make(chan int, 40)
		for i := 0; i < 40; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				code, _, err := h.request("POST", "/api/bookings", `{"event_id":1,"seat_id":1}`, uuid.NewString())
				if err != nil {
					t.Error(err)
				}
				results <- code
			}()
		}
		wg.Wait()
		close(results)
		winners := 0
		for code := range results {
			if code == 200 {
				winners++
			} else if code != 409 {
				t.Errorf("unexpected status: %d", code)
			}
		}
		if winners != 1 {
			t.Fatalf("got %d winners", winners)
		}
	})
	t.Run("concurrent retry and request key conflict", func(t *testing.T) {
		key := uuid.NewString()
		var wg sync.WaitGroup
		ids := make(chan string, 20)
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				code, b, err := h.request("POST", "/api/bookings", `{"event_id":1,"seat_id":2}`, key)
				if err != nil || code != 200 {
					t.Errorf("%d %v %v", code, b, err)
					return
				}
				ids <- b["id"].(string)
			}()
		}
		wg.Wait()
		close(ids)
		unique := map[string]bool{}
		for id := range ids {
			unique[id] = true
		}
		if len(unique) != 1 {
			t.Fatalf("retry created multiple bookings: %v", unique)
		}
		h.expect(t, "POST", "/api/bookings", `{"event_id":1,"seat_id":3}`, key, 409)
		for id := range unique {
			h.expect(t, "DELETE", "/api/bookings/"+id, "", "", 200)
			retry := h.expect(t, "POST", "/api/bookings", `{"event_id":1,"seat_id":2}`, key, 200)
			if retry["status"] != "CANCELLED" {
				t.Fatal(retry)
			}
		}
	})
	t.Run("checkout and cancellation are idempotent", func(t *testing.T) {
		b := h.reserve(t, 3)
		h.expect(t, "POST", path(b)+"/checkout", `{"payment_result":"fail"}`, "", 409)
		still := h.expect(t, "GET", path(b), "", "", 200)
		if still["status"] != "RESERVED" {
			t.Fatal(still)
		}
		for i := 0; i < 2; i++ {
			sold := h.expect(t, "POST", path(b)+"/checkout", `{"payment_result":"success"}`, "", 200)
			if sold["status"] != "SOLD" {
				t.Fatal(sold)
			}
		}
		h.expect(t, "DELETE", path(b), "", "", 409)
		h.expect(t, "POST", "/api/bookings", `{"event_id":1,"seat_id":3}`, uuid.NewString(), 409)
		c := h.reserve(t, 4)
		for i := 0; i < 2; i++ {
			cancelled := h.expect(t, "DELETE", path(c), "", "", 200)
			if cancelled["status"] != "CANCELLED" {
				t.Fatal(cancelled)
			}
		}
		next := h.reserve(t, 4)
		h.expect(t, "DELETE", path(c), "", "", 200)
		active := h.expect(t, "GET", path(next), "", "", 200)
		if active["status"] != "RESERVED" {
			t.Fatal(active)
		}
		h.expect(t, "POST", path(c)+"/checkout", `{"payment_result":"success"}`, "", 409)
	})
	t.Run("booking history records each state exactly once", func(t *testing.T) {
		b := h.reserve(t, 9)
		h.expectHistory(t, b, "RESERVED")
		h.expect(t, "POST", path(b)+"/checkout", `{"payment_result":"fail"}`, "", 409)
		h.expectHistory(t, b, "RESERVED")
		for i := 0; i < 2; i++ {
			h.expect(t, "POST", path(b)+"/checkout", `{"payment_result":"success"}`, "", 200)
		}
		h.expectHistory(t, b, "RESERVED", "SOLD")

		cancelled := h.reserve(t, 10)
		for i := 0; i < 2; i++ {
			h.expect(t, "DELETE", path(cancelled), "", "", 200)
		}
		h.expectHistory(t, cancelled, "RESERVED", "CANCELLED")
	})
	t.Run("expiry works without worker and survives service recreation", func(t *testing.T) {
		b := h.reserve(t, 5)
		if _, err := h.db.Exec(context.Background(), "UPDATE bookings SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", b["id"]); err != nil {
			t.Fatal(err)
		}
		fresh, err := inventory.New(context.Background(), h.db, false)
		if err != nil {
			t.Fatal(err)
		}
		expired, err := fresh.GetBooking(context.Background(), &pb.BookingRequest{BookingId: b["id"].(string)})
		if err != nil || expired.Status != "EXPIRED" {
			t.Fatalf("%v %v", expired, err)
		}
		h.expectHistory(t, b, "RESERVED", "EXPIRED")
		h.expect(t, "POST", path(b)+"/checkout", `{"payment_result":"success"}`, "", 409)
		seats := h.expect(t, "GET", "/api/events/1/seats", "", "", 200)
		found := false
		for _, seat := range seats["available_seat_ids"].([]any) {
			if seat == "5" {
				found = true
			}
		}
		if !found {
			t.Fatal("expired seat not available")
		}
		next := h.reserve(t, 5)
		h.expect(t, "DELETE", path(b), "", "", 200)
		active := h.expect(t, "GET", path(next), "", "", 200)
		if active["status"] != "RESERVED" {
			t.Fatal(active)
		}
	})
	t.Run("worker persists expiration", func(t *testing.T) {
		b := h.reserve(t, 6)
		if _, err := h.db.Exec(context.Background(), "UPDATE bookings SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", b["id"]); err != nil {
			t.Fatal(err)
		}
		n, err := h.inventory.Expire(context.Background())
		if err != nil || n < 1 {
			t.Fatalf("%d %v", n, err)
		}
		var state string
		if err := h.db.QueryRow(context.Background(), "SELECT status FROM bookings WHERE id=$1", b["id"]).Scan(&state); err != nil || state != "EXPIRED" {
			t.Fatalf("%s %v", state, err)
		}
		h.expectHistory(t, b, "RESERVED", "EXPIRED")
	})
	t.Run("checkout racing cancel has one terminal outcome", func(t *testing.T) {
		b := h.reserve(t, 7)
		var wg sync.WaitGroup
		results := make(chan int, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			code, _, err := h.request("POST", path(b)+"/checkout", `{"payment_result":"success"}`, "")
			if err != nil {
				t.Error(err)
			}
			results <- code
		}()
		go func() {
			defer wg.Done()
			code, _, err := h.request("DELETE", path(b), "", "")
			if err != nil {
				t.Error(err)
			}
			results <- code
		}()
		wg.Wait()
		close(results)
		success, conflict := 0, 0
		for code := range results {
			if code == 200 {
				success++
			}
			if code == 409 {
				conflict++
			}
		}
		if success != 1 || conflict != 1 {
			t.Fatalf("success=%d conflict=%d", success, conflict)
		}
	})
	t.Run("checkout rechecks expiry after waiting for seat lock", func(t *testing.T) {
		b := h.reserve(t, 8)
		ctx := context.Background()
		tx, err := h.db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, "SELECT id FROM seats WHERE event_id=1 AND id=8 FOR UPDATE"); err != nil {
			t.Fatal(err)
		}
		if _, err := h.db.Exec(ctx, "UPDATE bookings SET expires_at=clock_timestamp()+interval '1 second' WHERE id=$1", b["id"]); err != nil {
			t.Fatal(err)
		}
		result := make(chan int, 1)
		go func() {
			code, _, err := h.request("POST", path(b)+"/checkout", `{"payment_result":"success"}`, "")
			if err != nil {
				t.Error(err)
			}
			result <- code
		}()
		// Wait until PostgreSQL reports the confirm transaction waiting on our seat lock.
		deadline := time.Now().Add(3 * time.Second)
		for {
			var waiting bool
			if err := h.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
 WHERE datname=current_database() AND application_name=current_setting('application_name')
 AND wait_event_type='Lock' AND query LIKE 'SELECT id FROM seats WHERE event_id=$1%')`).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("checkout did not reach the seat lock")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if _, err := h.db.Exec(ctx, "UPDATE bookings SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", b["id"]); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if code := <-result; code != 409 {
			t.Fatalf("checkout after expiry returned %d", code)
		}
		state := h.expect(t, "GET", path(b), "", "", 200)
		if state["status"] != "EXPIRED" {
			t.Fatal(state)
		}
	})
}
