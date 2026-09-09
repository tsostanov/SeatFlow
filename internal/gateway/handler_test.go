package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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
