package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"
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

// TestGracefulShutdown verifies that http.Server.Shutdown drains in-flight
// requests and causes subsequent connections to be refused.
// It uses httptest.NewUnstartedServer so that ts.Config.Shutdown exercises
// the real Shutdown code path (not just Close).
func TestGracefulShutdown(t *testing.T) {
	setEnv(t)

	db := openTestDB(t)
	deps := buildTestDeps(t, db)
	mux := buildMux(deps)

	ts := httptest.NewUnstartedServer(mux)
	ts.Start()
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
		t.Fatalf("expected 200 before shutdown, got %d", resp.StatusCode)
	}

	// Call Shutdown on the underlying http.Server – the same instance that
	// httptest is using – to exercise the graceful drain path.
	shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ts.Config.Shutdown(shutCtx); err != nil {
		t.Fatalf("Shutdown returned unexpected error: %v", err)
	}

	// After Shutdown, new requests must fail.
	if _, err := http.Get(ts.URL + "/ping/00000000-0000-0000-0000-000000000000"); err == nil {
		t.Fatal("expected request to fail after server shutdown")
	}
}

// TestRunServesAndShutsDown is an integration test that exercises the full
// run() startup path: DB open, cache hydration, worker + scheduler lifecycle,
// HTTP serving, and graceful shutdown via context cancellation.
func TestRunServesAndShutsDown(t *testing.T) {
	setEnv(t)

	cfg := buildTestCfg(t)
	cfg.DBPath = ":memory:"

	// Find a free TCP port.  There is a brief TOCTOU window between Close and
	// Serve, but on loopback this is acceptable for a test.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	cfg.Port = strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	ln.Close() //nolint:errcheck

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() { runDone <- run(ctx, cfg) }()

	// Wait until the server is accepting connections (up to 1 s).
	addr := "127.0.0.1:" + cfg.Port
	started := false
	for i := 0; i < 50; i++ {
		conn, dialErr := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if dialErr == nil {
			conn.Close() //nolint:errcheck
			started = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !started {
		cancel()
		t.Fatal("server did not start within 1 s")
	}

	// Verify the ping endpoint responds end-to-end through the full stack.
	resp, err := http.Get("http://" + addr + "/ping/00000000-0000-0000-0000-000000000000")
	if err != nil {
		t.Fatalf("ping request failed: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Cancel the context to trigger graceful shutdown and confirm run() exits cleanly.
	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("run() returned unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run() did not complete within shutdown timeout")
	}
}

// TestRunHandlesStartupFailure verifies that run() exits with a non-nil error
// (and does not hang) when the HTTP server cannot bind its port.
func TestRunHandlesStartupFailure(t *testing.T) {
	setEnv(t)

	cfg := buildTestCfg(t)
	cfg.DBPath = ":memory:"

	// Hold the listener so run() cannot bind the same port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close() //nolint:errcheck
	cfg.Port = strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() { runDone <- run(ctx, cfg) }()

	select {
	case err := <-runDone:
		if err == nil {
			t.Fatal("expected run() to return an error when port is already in use")
		}
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("run() did not exit within timeout on startup failure")
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
