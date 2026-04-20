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

func TestRequestLogging_RedactsPingPath(t *testing.T) {
	var buf bytes.Buffer
	restore := setTestLogger(t, &buf)

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := middleware.RequestLogging(inner)

	req := httptest.NewRequest(http.MethodGet, "/ping/00000000-0000-0000-0000-000000000000/start", nil)
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)
	restore()

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
	restore := setTestLogger(t, &buf)

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	mw := middleware.RequestLogging(inner)

	req := httptest.NewRequest(http.MethodGet, "/checks", nil)
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)
	restore()

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
	restore := setTestLogger(t, &buf)

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := middleware.RequestLogging(inner)

	req := httptest.NewRequest(http.MethodGet, "/checks", nil)
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)
	restore()

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

// setTestLogger installs a JSON slog handler writing to buf and returns a
// function that restores the previous default logger.
func setTestLogger(t *testing.T, buf *bytes.Buffer) (restore func()) {
	t.Helper()
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	return func() { slog.SetDefault(prev) }
}
