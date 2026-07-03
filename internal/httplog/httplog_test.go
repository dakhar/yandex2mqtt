package httplog

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// capturingHandler invokes fn with the value of the "status" attribute.
type capturingHandler struct{ fn func(any) }

func (capturingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h capturingHandler) Handle(_ context.Context, r slog.Record) error {
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "status" {
			h.fn(int(a.Value.Int64()))
			return false
		}
		return true
	})
	return nil
}
func (h capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h capturingHandler) WithGroup(string) slog.Handler      { return h }

func testLogger(fn func(any)) *slog.Logger {
	return slog.New(capturingHandler{fn: fn})
}

func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		xRealIP    string
		xForwarded string
		remoteAddr string
		want       string
	}{
		{"x-real-ip wins", "203.0.113.7", "10.0.0.1", "10.0.0.9:12345", "203.0.113.7"},
		{"x-forwarded-for first entry", "", "203.0.113.9, 10.0.0.1", "10.0.0.9:12345", "203.0.113.9"},
		{"remote addr fallback", "", "", "198.51.100.4:5555", "198.51.100.4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tt.remoteAddr
			if tt.xRealIP != "" {
				r.Header.Set("X-Real-IP", tt.xRealIP)
			}
			if tt.xForwarded != "" {
				r.Header.Set("X-Forwarded-For", tt.xForwarded)
			}
			if got := ClientIP(r); got != tt.want {
				t.Fatalf("ClientIP = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMiddlewareCapturesStatus(t *testing.T) {
	var captured int
	log := testLogger(func(status any) { captured, _ = status.(int) })

	h := Middleware(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusTeapot {
		t.Fatalf("downstream status not propagated: %d", rec.Code)
	}
	if captured != http.StatusTeapot {
		t.Fatalf("logged status = %d, want %d", captured, http.StatusTeapot)
	}
}
