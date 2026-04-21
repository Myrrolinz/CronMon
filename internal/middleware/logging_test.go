package middleware_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/myrrolinz/cronmon/internal/middleware"
)

// newTestLogger creates an isolated JSON slog.Logger that writes to buf.
// Because the logger is injected directly into the middleware, the global
// slog default is never mutated and tests are safe to run in parallel.
func newTestLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

func TestRequestLogging_RedactsPingPath(t *testing.T) {
	var buf bytes.Buffer
	mw := middleware.RequestLogging(newTestLogger(&buf), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/ping/00000000-0000-0000-0000-000000000000/start", nil)
	mw.ServeHTTP(httptest.NewRecorder(), req)

	if bytes.Contains(buf.Bytes(), []byte("00000000-0000-0000-0000-000000000000")) {
		t.Fatal("UUID must not appear in request log")
	}

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("log line is not valid JSON: %v\n%s", err, buf.String())
	}
	if got, _ := rec["path"].(string); got != "/ping/[REDACTED]" {
		t.Fatalf("path field: got %q want /ping/[REDACTED]", got)
	}
}

func TestRequestLogging_LogsNonPingPath(t *testing.T) {
	var buf bytes.Buffer
	mw := middleware.RequestLogging(newTestLogger(&buf), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))

	req := httptest.NewRequest(http.MethodGet, "/checks", nil)
	mw.ServeHTTP(httptest.NewRecorder(), req)

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("log line is not valid JSON: %v", err)
	}
	if got, _ := rec["path"].(string); got != "/checks" {
		t.Fatalf("path field: got %q want /checks", got)
	}
	if status, ok := rec["status"].(float64); !ok || int(status) != http.StatusUnauthorized {
		t.Fatalf("status field: got %v want %d", rec["status"], http.StatusUnauthorized)
	}
}

func TestRequestLogging_Fields(t *testing.T) {
	var buf bytes.Buffer
	mw := middleware.RequestLogging(newTestLogger(&buf), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/checks", nil)
	mw.ServeHTTP(httptest.NewRecorder(), req)

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("log line is not valid JSON: %v", err)
	}
	for _, key := range []string{"method", "path", "status", "duration_ms", "remote_addr"} {
		if _, ok := rec[key]; !ok {
			t.Errorf("missing log field %q in %s", key, buf.String())
		}
	}
}
