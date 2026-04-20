package middleware

import (
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// RequestLogging returns middleware that logs each completed request with slog.
// Fields: method, path, status, duration_ms, remote_addr.
//
// Paths for ping routes are treated as sensitive (the UUID is a credential);
// the logged path is always /ping/[REDACTED] instead of the real path.
func RequestLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &responseRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)

		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		path := r.URL.Path
		if path == "/ping" || strings.HasPrefix(path, "/ping/") {
			path = "/ping/[REDACTED]"
		}
		slog.Info("request",
			"method", r.Method,
			"path", path,
			"status", status,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote_addr", r.RemoteAddr,
		)
	})
}

type responseRecorder struct {
	http.ResponseWriter
	status int
}

func (rec *responseRecorder) WriteHeader(code int) {
	if rec.status == 0 {
		rec.status = code
	}
	rec.ResponseWriter.WriteHeader(code)
}
