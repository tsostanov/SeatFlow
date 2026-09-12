package gateway

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var requestDurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

type requestMetricKey struct {
	method string
	route  string
	status int
}

type routeMetricKey struct {
	method string
	route  string
}

type requestHistogram struct {
	buckets []uint64
	count   uint64
	sum     float64
}

type httpMetrics struct {
	mu                sync.Mutex
	requests          map[requestMetricKey]uint64
	durations         map[routeMetricKey]*requestHistogram
	activeStreams     int64
	streamConnections uint64
}

func newHTTPMetrics() *httpMetrics {
	return &httpMetrics{
		requests:  make(map[requestMetricKey]uint64),
		durations: make(map[routeMetricKey]*requestHistogram),
	}
}

func (m *httpMetrics) observe(method, route string, status int, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.requests[requestMetricKey{method: method, route: route, status: status}]++
	key := routeMetricKey{method: method, route: route}
	histogram := m.durations[key]
	if histogram == nil {
		histogram = &requestHistogram{buckets: make([]uint64, len(requestDurationBuckets))}
		m.durations[key] = histogram
	}
	seconds := duration.Seconds()
	for i, upperBound := range requestDurationBuckets {
		if seconds <= upperBound {
			histogram.buckets[i]++
		}
	}
	histogram.count++
	histogram.sum += seconds
}

func (m *httpMetrics) openStream() func() {
	m.mu.Lock()
	m.activeStreams++
	m.streamConnections++
	m.mu.Unlock()

	return func() {
		m.mu.Lock()
		m.activeStreams--
		m.mu.Unlock()
	}
}

func (m *httpMetrics) serveHTTP(w http.ResponseWriter, _ *http.Request) {
	m.mu.Lock()

	var output strings.Builder
	output.WriteString("# HELP seatflow_http_requests_total Total HTTP requests handled by the gateway.\n")
	output.WriteString("# TYPE seatflow_http_requests_total counter\n")
	requestKeys := make([]requestMetricKey, 0, len(m.requests))
	for key := range m.requests {
		requestKeys = append(requestKeys, key)
	}
	sort.Slice(requestKeys, func(i, j int) bool {
		left, right := requestKeys[i], requestKeys[j]
		if left.method != right.method {
			return left.method < right.method
		}
		if left.route != right.route {
			return left.route < right.route
		}
		return left.status < right.status
	})
	for _, key := range requestKeys {
		fmt.Fprintf(&output, "seatflow_http_requests_total{method=\"%s\",route=\"%s\",status=\"%s\"} %d\n",
			escapeMetricLabel(key.method), escapeMetricLabel(key.route), strconv.Itoa(key.status), m.requests[key])
	}

	output.WriteString("# HELP seatflow_http_request_duration_seconds HTTP request duration in seconds.\n")
	output.WriteString("# TYPE seatflow_http_request_duration_seconds histogram\n")
	routeKeys := make([]routeMetricKey, 0, len(m.durations))
	for key := range m.durations {
		routeKeys = append(routeKeys, key)
	}
	sort.Slice(routeKeys, func(i, j int) bool {
		if routeKeys[i].method != routeKeys[j].method {
			return routeKeys[i].method < routeKeys[j].method
		}
		return routeKeys[i].route < routeKeys[j].route
	})
	for _, key := range routeKeys {
		histogram := m.durations[key]
		method, route := escapeMetricLabel(key.method), escapeMetricLabel(key.route)
		for i, upperBound := range requestDurationBuckets {
			fmt.Fprintf(&output, "seatflow_http_request_duration_seconds_bucket{method=\"%s\",route=\"%s\",le=\"%s\"} %d\n",
				method, route, strconv.FormatFloat(upperBound, 'g', -1, 64), histogram.buckets[i])
		}
		fmt.Fprintf(&output, "seatflow_http_request_duration_seconds_bucket{method=\"%s\",route=\"%s\",le=\"+Inf\"} %d\n",
			method, route, histogram.count)
		fmt.Fprintf(&output, "seatflow_http_request_duration_seconds_sum{method=\"%s\",route=\"%s\"} %s\n",
			method, route, strconv.FormatFloat(histogram.sum, 'g', -1, 64))
		fmt.Fprintf(&output, "seatflow_http_request_duration_seconds_count{method=\"%s\",route=\"%s\"} %d\n",
			method, route, histogram.count)
	}

	output.WriteString("# HELP seatflow_sse_connections_active Current seat availability SSE connections.\n")
	output.WriteString("# TYPE seatflow_sse_connections_active gauge\n")
	fmt.Fprintf(&output, "seatflow_sse_connections_active %d\n", m.activeStreams)
	output.WriteString("# HELP seatflow_sse_connections_total Total seat availability SSE connections accepted.\n")
	output.WriteString("# TYPE seatflow_sse_connections_total counter\n")
	fmt.Fprintf(&output, "seatflow_sse_connections_total %d\n", m.streamConnections)
	m.mu.Unlock()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(output.String()))
}

func escapeMetricLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return strings.ReplaceAll(value, `"`, `\"`)
}

type statusResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (w *statusResponseWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *statusResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *statusResponseWriter) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}
