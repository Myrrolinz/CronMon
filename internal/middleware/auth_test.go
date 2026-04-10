package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/myrrolinz/cronmon/internal/middleware"
)

// sentinel is the handler wrapped by BasicAuth in all tests.
var sentinel = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
})

func TestBasicAuth_ValidCredentials(t *testing.T) {
	h := middleware.BasicAuth("admin", "s3cret")(sentinel)

	req := httptest.NewRequest(http.MethodGet, "/checks", nil)
	req.SetBasicAuth("admin", "s3cret")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 got %d", w.Code)
	}
}

func TestBasicAuth_WrongPassword(t *testing.T) {
	h := middleware.BasicAuth("admin", "s3cret")(sentinel)

	req := httptest.NewRequest(http.MethodGet, "/checks", nil)
	req.SetBasicAuth("admin", "wrong")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 got %d", w.Code)
	}
	if www := w.Header().Get("WWW-Authenticate"); www == "" {
		t.Error("expected WWW-Authenticate header to be set")
	}
}

func TestBasicAuth_WrongUsername(t *testing.T) {
	h := middleware.BasicAuth("admin", "s3cret")(sentinel)

	req := httptest.NewRequest(http.MethodGet, "/checks", nil)
	req.SetBasicAuth("other", "s3cret")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 got %d", w.Code)
	}
}

func TestBasicAuth_NoCredentials(t *testing.T) {
	h := middleware.BasicAuth("admin", "s3cret")(sentinel)

	req := httptest.NewRequest(http.MethodGet, "/checks", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 got %d", w.Code)
	}
}

// TestBasicAuth_TimingSafe verifies that the handler takes roughly the same
// time regardless of whether the username matches.  We measure 1000 trials
// for each path and assert that the averages do not differ by more than 10×.
// This is a smoke test — it catches a naive early-exit "if username != …
// return 401" rather than a rigorous timing proof.
func TestBasicAuth_TimingSafe(t *testing.T) {
	const trials = 1000
	h := middleware.BasicAuth("admin", "s3cret")(sentinel)

	measure := func(user, pass string) time.Duration {
		var total time.Duration
		for i := 0; i < trials; i++ {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.SetBasicAuth(user, pass)
			w := httptest.NewRecorder()
			start := time.Now()
			h.ServeHTTP(w, req)
			total += time.Since(start)
		}
		return total / trials
	}

	correctUser := measure("admin", "wrong")  // correct username, wrong password
	wrongUser := measure("attacker", "wrong") // wrong username, wrong password

	// Allow up to 10× difference — the point is it must not be orders of
	// magnitude faster for the wrong-username path.
	if correctUser > 0 && wrongUser > 0 {
		ratio := float64(correctUser) / float64(wrongUser)
		if ratio > 10 || ratio < 0.1 {
			t.Errorf("suspicious timing ratio %v (correctUser=%v wrongUser=%v) — possible early exit", ratio, correctUser, wrongUser)
		}
	}
}
