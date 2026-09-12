package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPMetricsTrackLatencyAndStreams(t *testing.T) {
	metrics := newHTTPMetrics()
	metrics.observe("POST", "/api/bookings", 201, 20*time.Millisecond)
	closeStream := metrics.openStream()

	w := httptest.NewRecorder()
	metrics.serveHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	body := w.Body.String()
	for _, want := range []string{
		`seatflow_http_requests_total{method="POST",route="/api/bookings",status="201"} 1`,
		`seatflow_http_request_duration_seconds_bucket{method="POST",route="/api/bookings",le="0.01"} 0`,
		`seatflow_http_request_duration_seconds_bucket{method="POST",route="/api/bookings",le="0.025"} 1`,
		`seatflow_http_request_duration_seconds_count{method="POST",route="/api/bookings"} 1`,
		`seatflow_sse_connections_active 1`,
		`seatflow_sse_connections_total 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}

	closeStream()
	w = httptest.NewRecorder()
	metrics.serveHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if body := w.Body.String(); !strings.Contains(body, "seatflow_sse_connections_active 0") ||
		!strings.Contains(body, "seatflow_sse_connections_total 1") {
		t.Fatalf("unexpected stream metrics after close:\n%s", body)
	}
}
