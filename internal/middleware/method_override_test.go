package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/myrrolinz/cronmon/internal/middleware"
)

func TestMethodOverride_RewritesMethod(t *testing.T) {
	var captured string
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		captured = r.Method
	})

	h := middleware.MethodOverride(inner)

	body := strings.NewReader("_method=DELETE")
	req := httptest.NewRequest(http.MethodPost, "/checks/1/delete", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if captured != http.MethodDelete {
		t.Errorf("expected method %q got %q", http.MethodDelete, captured)
	}
}

func TestMethodOverride_OnlyRewritesPost(t *testing.T) {
	var captured string
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		captured = r.Method
	})

	h := middleware.MethodOverride(inner)

	req := httptest.NewRequest(http.MethodGet, "/checks", nil)
	req.Form = map[string][]string{"_method": {"DELETE"}}
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if captured != http.MethodGet {
		t.Errorf("expected method GET, got %q", captured)
	}
}

func TestMethodOverride_NoFieldPassesThrough(t *testing.T) {
	var captured string
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		captured = r.Method
	})

	h := middleware.MethodOverride(inner)

	req := httptest.NewRequest(http.MethodPost, "/checks", strings.NewReader("name=foo"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if captured != http.MethodPost {
		t.Errorf("expected method POST, got %q", captured)
	}
}

func TestMethodOverride_NonWhitelistedMethodIgnored(t *testing.T) {
	var captured string
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		captured = r.Method
	})

	h := middleware.MethodOverride(inner)

	body := strings.NewReader("_method=TRACE")
	req := httptest.NewRequest(http.MethodPost, "/checks", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if captured != http.MethodPost {
		t.Errorf("expected method POST (non-whitelisted _method ignored), got %q", captured)
	}
}
