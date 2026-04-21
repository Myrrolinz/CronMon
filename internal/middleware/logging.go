package middleware

import (
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// RequestLogging returns middleware that logs each completed request with the
// provided logger. Fields: method, path, status, duration_ms, remote_addr.
//
// Paths for ping routes are treated as sensitive (the UUID is a credential);
// the logged path is always /ping/[REDACTED] instead of the real path.
//
// Callers should pass slog.Default() for production use. Accepting an explicit
// logger avoids mutating global state in tests, making parallel test execution
// safe.
func RequestLogging(logger *slog.Logger, next http.Handler) http.Handler {
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
		method := sanitizeLogString(r.Method)
		path = sanitizeLogString(path)
		remoteAddr := sanitizeLogString(r.RemoteAddr)

		//nolint:gosec // request-derived fields are sanitized to strip control chars.
		logger.Info("request",
			"method", method,
			"path", path,
			"status", status,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote_addr", remoteAddr,
		)
	})
}

// sanitizeLogString strips control characters to prevent log injection.
func sanitizeLogString(value string) string {
	if value == "" {
		return value
	}

	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		if (r >= 0x00 && r <= 0x1f) || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
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
