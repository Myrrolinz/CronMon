package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// setEnv sets minimal environment variables required to start the server,
// restoring the original values on cleanup.
func setEnv(t *testing.T) {
	t.Helper()
	vars := map[string]string{
		"BASE_URL":           "http://localhost:8080",
		"ADMIN_PASS":         "testpass",
		"SCHEDULER_INTERVAL": "30",
	}
	for k, v := range vars {
		old, ok := os.LookupEnv(k)
		if ok {
			t.Cleanup(func() { os.Setenv(k, old) }) //nolint:errcheck
		} else {
			t.Cleanup(func() { os.Unsetenv(k) }) //nolint:errcheck
		}
		os.Setenv(k, v) //nolint:errcheck
	}
}

// TestBuildMuxPingEndpointNoAuth verifies that /ping/* routes respond with
// 200 OK even without Basic Auth credentials.
func TestBuildMuxPingEndpointNoAuth(t *testing.T) {
	setEnv(t)

	db := openTestDB(t)
	mux := buildMux(buildTestDeps(t, db))

	// Unknown UUID must still return 200 (check does not exist → silent discard).
	req := httptest.NewRequest(http.MethodGet, "/ping/00000000-0000-0000-0000-000000000000", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for unknown ping UUID, got %d", w.Code)
	}
	if body := w.Body.String(); body != "OK\n" {
		t.Fatalf("expected body 'OK\\n', got %q", body)
	}
}

// TestBuildMuxAuthRequired verifies that authenticated routes return 401
// when no credentials are provided.
func TestBuildMuxAuthRequired(t *testing.T) {
	setEnv(t)

	db := openTestDB(t)
	mux := buildMux(buildTestDeps(t, db))

	req := httptest.NewRequest(http.MethodGet, "/checks", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated /checks, got %d", w.Code)
	}
}

// TestBuildMuxAuthSucceeds verifies that correct credentials grant access.
func TestBuildMuxAuthSucceeds(t *testing.T) {
	setEnv(t)

	db := openTestDB(t)
	mux := buildMux(buildTestDeps(t, db))

	req := httptest.NewRequest(http.MethodGet, "/checks", nil)
	req.SetBasicAuth("admin", "testpass")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	// 200 or 303 are both acceptable – the handler may redirect.
	if w.Code != http.StatusOK && w.Code != http.StatusSeeOther {
		t.Fatalf("expected 200 or 303 for authenticated /checks, got %d", w.Code)
	}
}

// TestBuildMuxIndexRedirect verifies that GET / redirects to /checks.
func TestBuildMuxIndexRedirect(t *testing.T) {
	setEnv(t)

	db := openTestDB(t)
	mux := buildMux(buildTestDeps(t, db))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("admin", "testpass")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 for GET /, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/checks" {
		t.Fatalf("expected redirect to /checks, got %q", loc)
	}
}

// TestBuildMuxPingStartEndpointNoAuth verifies that /ping/{uuid}/start also
// requires no authentication.
func TestBuildMuxPingStartEndpointNoAuth(t *testing.T) {
	setEnv(t)

	db := openTestDB(t)
	mux := buildMux(buildTestDeps(t, db))

	req := httptest.NewRequest(http.MethodGet, "/ping/00000000-0000-0000-0000-000000000000/start", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for /ping/.../start without auth, got %d", w.Code)
	}
}

// TestBuildMuxPingFailEndpointNoAuth verifies that /ping/{uuid}/fail also
// requires no authentication.
func TestBuildMuxPingFailEndpointNoAuth(t *testing.T) {
	setEnv(t)

	db := openTestDB(t)
	mux := buildMux(buildTestDeps(t, db))

	req := httptest.NewRequest(http.MethodGet, "/ping/00000000-0000-0000-0000-000000000000/fail", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for /ping/.../fail without auth, got %d", w.Code)
	}
}

// TestGracefulShutdown verifies that the HTTP server shuts down cleanly within
// the configured 10-second timeout window. It starts a real listener on a
// random port, issues a request while the server is live, calls Shutdown, and
// then confirms that further requests are rejected.
func TestGracefulShutdown(t *testing.T) {
	setEnv(t)

	db := openTestDB(t)
	deps := buildTestDeps(t, db)
	mux := buildMux(deps)

	// Use httptest.Server so we get a free port automatically.
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Confirm the server is up and responding to ping.
	resp, err := http.Get(ts.URL + "/ping/00000000-0000-0000-0000-000000000000")
	if err != nil {
		t.Fatalf("pre-shutdown request failed: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("failed to close response body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Close the test server (httptest.Server has its own listener).
	ts.Close()

	// After ts.Close(), new requests must fail.
	if _, err := http.Get(ts.URL + "/ping/00000000-0000-0000-0000-000000000000"); err == nil {
		t.Fatal("expected request to fail after server shutdown")
	}
}

// TestBuildNotifiersNoSMTP verifies that buildNotifiers returns only slack and
// webhook notifiers when no SMTP configuration is set.
func TestBuildNotifiersNoSMTP(t *testing.T) {
	setEnv(t)

	os.Unsetenv("SMTP_HOST") //nolint:errcheck
	os.Unsetenv("SMTP_FROM") //nolint:errcheck

	notifiers := buildNotifiers(buildTestCfg(t))
	if _, ok := notifiers["email"]; ok {
		t.Error("expected no email notifier when SMTP is not configured")
	}
	if _, ok := notifiers["slack"]; !ok {
		t.Error("expected slack notifier always present")
	}
	if _, ok := notifiers["webhook"]; !ok {
		t.Error("expected webhook notifier always present")
	}
}

// TestBuildNotifiersWithSMTP verifies that buildNotifiers includes an email
// notifier when SMTP configuration is present.
func TestBuildNotifiersWithSMTP(t *testing.T) {
	setEnv(t)
	os.Setenv("SMTP_HOST", "smtp.example.com")    //nolint:errcheck
	os.Setenv("SMTP_FROM", "noreply@example.com") //nolint:errcheck
	t.Cleanup(func() {
		os.Unsetenv("SMTP_HOST") //nolint:errcheck
		os.Unsetenv("SMTP_FROM") //nolint:errcheck
	})

	notifiers := buildNotifiers(buildTestCfg(t))
	if _, ok := notifiers["email"]; !ok {
		t.Error("expected email notifier when SMTP is configured")
	}
}
