// Package httplog provides request-logging middleware that records the real
// client IP (honoring the reverse proxy's X-Forwarded-For / X-Real-IP), plus
// method, path, status, duration and the Yandex X-Request-Id.
package httplog

import (
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// Middleware returns request-logging middleware.
func Middleware(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			// Only the path is logged (never RawQuery/body) so OAuth codes and
			// tokens never end up in logs.
			log.Info("http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"ip", ClientIP(r),
				"dur_ms", time.Since(start).Milliseconds(),
				"req_id", r.Header.Get("X-Request-Id"),
				"ua", r.UserAgent(),
			)
		})
	}
}

// ClientIP returns the originating client IP, trusting the reverse proxy
// headers first (the app runs behind nginx-proxy).
func ClientIP(r *http.Request) string {
	if xrip := strings.TrimSpace(r.Header.Get("X-Real-IP")); xrip != "" {
		return xrip
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// The left-most entry is the original client.
		if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
			return first
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// statusRecorder captures the response status code for logging.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.wroteHeader = true
	return r.ResponseWriter.Write(b)
}
