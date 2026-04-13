package notify_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/myrrolinz/cronmon/internal/model"
	"github.com/myrrolinz/cronmon/internal/notify"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// makeSlackEvent builds an AlertEvent whose Channel.Config points to the
// supplied URL (as if the user registered a Slack channel with that webhook).
func makeSlackEvent(alertType model.AlertType, webhookURL string) model.AlertEvent {
	t := time.Date(2025, 2, 26, 2, 10, 0, 0, time.UTC)
	cfgJSON, _ := json.Marshal(map[string]string{"url": webhookURL})
	return model.AlertEvent{
		AlertType: alertType,
		Check: model.Check{
			ID:             "a3f9c2d1-0000-0000-0000-000000000001",
			Name:           "Database backup",
			Schedule:       "0 2 * * *",
			Status:         model.StatusDown,
			NextExpectedAt: &t,
		},
		Channel: model.Channel{
			Type:   "slack",
			Config: cfgJSON,
		},
	}
}

// captureSlackServer starts a test HTTP server that captures the last request
// body.  It responds 200 "ok" to all POSTs.  The server is shut down via
// t.Cleanup.
type captureServer struct {
	ts       *httptest.Server
	lastBody []byte
	lastReq  *http.Request
}

func newCaptureServer(t *testing.T, statusCode int, responseBody string) *captureServer {
	t.Helper()
	cs := &captureServer{}
	cs.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		cs.lastBody = body
		cs.lastReq = r
		w.WriteHeader(statusCode)
		_, _ = fmt.Fprint(w, responseBody)
	}))
	t.Cleanup(cs.ts.Close)
	return cs
}

// plainClient returns an *http.Client that connects to the test server without
// SSRF validation (the test server is on loopback, which SSRF would reject).
func plainClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

// ---------------------------------------------------------------------------
// SlackNotifier tests
// ---------------------------------------------------------------------------

func TestSlackNotifier_Type(t *testing.T) {
	t.Parallel()
	n := notify.NewSlackNotifier()
	if got := n.Type(); got != "slack" {
		t.Errorf("Type() = %q; want %q", got, "slack")
	}
}

// TestSlackNotifier_Send_DOWN verifies that a DOWN event results in a POST to
// the webhook URL with a payload that contains the expected text and color.
func TestSlackNotifier_Send_DOWN(t *testing.T) {
	cs := newCaptureServer(t, http.StatusOK, "ok")
	n := notify.NewSlackNotifier().WithHTTPClient(plainClient(5 * time.Second))

	err := n.Send(context.Background(), makeSlackEvent(model.AlertDown, cs.ts.URL))
	if err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}

	var p slackPayloadShape
	if err := json.Unmarshal(cs.lastBody, &p); err != nil {
		t.Fatalf("parse captured body: %v", err)
	}

	if !strings.Contains(p.Text, "DOWN") {
		t.Errorf("text %q should contain DOWN", p.Text)
	}
	if !strings.Contains(p.Text, "⚠") {
		t.Errorf("text %q should contain ⚠", p.Text)
	}
	if len(p.Attachments) == 0 {
		t.Fatal("expected at least one attachment")
	}
	if p.Attachments[0].Color != "#cc0000" {
		t.Errorf("attachment color = %q; want %q", p.Attachments[0].Color, "#cc0000")
	}
}

// TestSlackNotifier_Send_UP verifies that a UP (recovery) event produces the
// correct text and green attachment color.
func TestSlackNotifier_Send_UP(t *testing.T) {
	cs := newCaptureServer(t, http.StatusOK, "ok")
	n := notify.NewSlackNotifier().WithHTTPClient(plainClient(5 * time.Second))

	err := n.Send(context.Background(), makeSlackEvent(model.AlertUp, cs.ts.URL))
	if err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}

	var p slackPayloadShape
	if err := json.Unmarshal(cs.lastBody, &p); err != nil {
		t.Fatalf("parse captured body: %v", err)
	}

	if !strings.Contains(p.Text, "RECOVERED") {
		t.Errorf("text %q should contain RECOVERED", p.Text)
	}
	if !strings.Contains(p.Text, "✓") {
		t.Errorf("text %q should contain ✓", p.Text)
	}
	if len(p.Attachments) == 0 {
		t.Fatal("expected at least one attachment")
	}
	if p.Attachments[0].Color != "#007700" {
		t.Errorf("attachment color = %q; want %q", p.Attachments[0].Color, "#007700")
	}
}

// TestSlackNotifier_Send_FAIL verifies that a FAIL event produces the correct
// text and amber attachment color.
func TestSlackNotifier_Send_FAIL(t *testing.T) {
	cs := newCaptureServer(t, http.StatusOK, "ok")
	n := notify.NewSlackNotifier().WithHTTPClient(plainClient(5 * time.Second))

	err := n.Send(context.Background(), makeSlackEvent(model.AlertFail, cs.ts.URL))
	if err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}

	var p slackPayloadShape
	if err := json.Unmarshal(cs.lastBody, &p); err != nil {
		t.Fatalf("parse captured body: %v", err)
	}

	if !strings.Contains(p.Text, "FAILED") {
		t.Errorf("text %q should contain FAILED", p.Text)
	}
	if !strings.Contains(p.Text, "✗") {
		t.Errorf("text %q should contain ✗", p.Text)
	}
	if len(p.Attachments) == 0 {
		t.Fatal("expected at least one attachment")
	}
	if p.Attachments[0].Color != "#ff8c00" {
		t.Errorf("attachment color = %q; want %q", p.Attachments[0].Color, "#ff8c00")
	}
}

// TestSlackNotifier_PayloadFields verifies that the attachment contains
// the expected fields derived from the check.
func TestSlackNotifier_PayloadFields(t *testing.T) {
	cs := newCaptureServer(t, http.StatusOK, "ok")
	n := notify.NewSlackNotifier().WithHTTPClient(plainClient(5 * time.Second))

	event := makeSlackEvent(model.AlertDown, cs.ts.URL)
	if err := n.Send(context.Background(), event); err != nil {
		t.Fatalf("Send: %v", err)
	}

	var p slackPayloadShape
	if err := json.Unmarshal(cs.lastBody, &p); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if len(p.Attachments) == 0 || len(p.Attachments[0].Fields) == 0 {
		t.Fatal("expected non-empty fields in attachment")
	}

	fieldTitles := make(map[string]string)
	for _, f := range p.Attachments[0].Fields {
		fieldTitles[f.Title] = f.Value
	}

	if fieldTitles["Check"] != "Database backup" {
		t.Errorf("Check field = %q; want %q", fieldTitles["Check"], "Database backup")
	}
	if fieldTitles["Schedule"] != "0 2 * * *" {
		t.Errorf("Schedule field = %q; want %q", fieldTitles["Schedule"], "0 2 * * *")
	}
	if _, ok := fieldTitles["Next Expected"]; !ok {
		t.Error("Next Expected field not present")
	}
}

// TestSlackNotifier_ContentTypeHeader verifies that the request carries the
// application/json content-type header required by Slack webhooks.
func TestSlackNotifier_ContentTypeHeader(t *testing.T) {
	cs := newCaptureServer(t, http.StatusOK, "ok")
	n := notify.NewSlackNotifier().WithHTTPClient(plainClient(5 * time.Second))

	if err := n.Send(context.Background(), makeSlackEvent(model.AlertDown, cs.ts.URL)); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if ct := cs.lastReq.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q; want %q", ct, "application/json")
	}
}

// TestSlackNotifier_Non200Response verifies that a non-200 HTTP response is
// returned as an error, matching Slack's documented behaviour.
func TestSlackNotifier_Non200Response(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		statusCode int
	}{
		{"500 Internal Server Error", http.StatusInternalServerError},
		{"400 Bad Request", http.StatusBadRequest},
		{"404 Not Found", http.StatusNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := newCaptureServer(t, tc.statusCode, "error")
			n := notify.NewSlackNotifier().WithHTTPClient(plainClient(5 * time.Second))

			err := n.Send(context.Background(), makeSlackEvent(model.AlertDown, cs.ts.URL))
			if err == nil {
				t.Errorf("expected error for status %d, got nil", tc.statusCode)
			}
		})
	}
}

// TestSlackNotifier_MissingURL verifies that Send returns an error when the
// channel config does not contain a URL.
func TestSlackNotifier_MissingURL(t *testing.T) {
	t.Parallel()
	n := notify.NewSlackNotifier()

	event := model.AlertEvent{
		AlertType: model.AlertDown,
		Check:     model.Check{Name: "backup"},
		Channel: model.Channel{
			Type:   "slack",
			Config: []byte(`{}`),
		},
	}

	if err := n.Send(context.Background(), event); err == nil {
		t.Error("expected error for missing url, got nil")
	}
}

// TestSlackNotifier_InvalidConfig verifies that Send returns an error for
// malformed channel config JSON.
func TestSlackNotifier_InvalidConfig(t *testing.T) {
	t.Parallel()
	n := notify.NewSlackNotifier()

	event := model.AlertEvent{
		AlertType: model.AlertDown,
		Check:     model.Check{Name: "backup"},
		Channel: model.Channel{
			Type:   "slack",
			Config: []byte(`not-json`),
		},
	}

	if err := n.Send(context.Background(), event); err == nil {
		t.Error("expected error for invalid JSON config, got nil")
	}
}

// TestSlackNotifier_ContextTimeout verifies that Send respects a context
// deadline and returns before the server responds.
func TestSlackNotifier_ContextTimeout(t *testing.T) {
	// Server that hangs until either the client disconnects or 10 s elapse.
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-r.Context().Done():
		}
	}))
	// CloseClientConnections unblocks any active handler goroutines so that
	// Close() can return promptly.
	t.Cleanup(func() {
		hang.CloseClientConnections()
		hang.Close()
	})

	n := notify.NewSlackNotifier().WithHTTPClient(plainClient(5 * time.Second))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := n.Send(ctx, makeSlackEvent(model.AlertDown, hang.URL))
	elapsed := time.Since(start)

	if err == nil {
		t.Error("expected timeout error, got nil")
	}
	if elapsed > 2*time.Second {
		t.Errorf("Send took %v; expected it to respect the 50ms context deadline", elapsed)
	}
}

// TestSlackNotifier_SSRF_RejectsPrivateIP verifies that Send refuses to
// connect to a webhook URL that resolves to a private IP address.  The test
// injects a mock resolver that always returns a known private address so that
// SSRF rejection can be asserted without real DNS queries.
func TestSlackNotifier_SSRF_RejectsPrivateIP(t *testing.T) {
	t.Parallel()

	privateAddresses := []struct {
		name string
		ip   string
	}{
		{"RFC1918 class-A (10.x)", "10.0.0.1"},
		{"RFC1918 class-B (172.16.x)", "172.16.0.1"},
		{"RFC1918 class-C (192.168.x)", "192.168.1.100"},
		{"loopback (127.x)", "127.0.0.1"},
		{"loopback (::1)", "::1"},
		{"link-local", "169.254.169.254"}, // AWS metadata service
	}

	for _, tc := range privateAddresses {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			privateResolver := func(host string) ([]string, error) {
				return []string{tc.ip}, nil
			}

			n := notify.NewSlackNotifier().WithResolver(privateResolver)

			cfgJSON, _ := json.Marshal(map[string]string{"url": "http://evil.internal/webhook"})
			event := model.AlertEvent{
				AlertType: model.AlertDown,
				Check:     model.Check{Name: "test"},
				Channel:   model.Channel{Type: "slack", Config: cfgJSON},
			}

			err := n.Send(context.Background(), event)
			if err == nil {
				t.Errorf("expected SSRF error for private IP %s, got nil", tc.ip)
			}
			if !strings.Contains(err.Error(), "private") && !strings.Contains(err.Error(), "reserved") {
				t.Errorf("error %q should mention private/reserved IP", err.Error())
			}
		})
	}
}

// TestSlackNotifier_SSRF_AllowsPublicIP verifies that Send proceeds normally
// when the resolver returns a public IP (the actual connection goes to the
// test server via WithHTTPClient which bypasses SSRF at dial time).
func TestSlackNotifier_SSRF_AllowsPublicIP(t *testing.T) {
	cs := newCaptureServer(t, http.StatusOK, "ok")

	// Resolver returns a public IP to satisfy the SSRF pre-check.
	// WithHTTPClient then takes over for the actual TCP connection.
	publicResolver := func(host string) ([]string, error) {
		return []string{"1.2.3.4"}, nil
	}

	// We need both: SSRF pre-check uses our public resolver, actual TCP
	// connection uses the plain client to reach the test server.
	// Build a notifier with the public resolver for SSRF, then also override
	// the client so the TCP dial goes to the test server.
	n := notify.NewSlackNotifier().
		WithResolver(publicResolver).
		WithHTTPClient(plainClient(5 * time.Second))

	err := n.Send(context.Background(), makeSlackEvent(model.AlertDown, cs.ts.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// isPrivateIP unit tests (via the SSRF-safe client rejection behaviour)
// ---------------------------------------------------------------------------

// TestSSRF_IsPrivateIP_TableDriven exercises all private IP categories through
// the SlackNotifier.WithResolver hook.  This validates the isPrivateIP helper
// indirectly without depending on its unexported status.
func TestSSRF_IsPrivateIP_TableDriven(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		ip      string
		private bool
	}{
		{"10.0.0.0 (RFC 1918 boundary)", "10.0.0.0", true},
		{"10.255.255.255 (RFC 1918 top)", "10.255.255.255", true},
		{"172.16.0.0 (RFC 1918 boundary)", "172.16.0.0", true},
		{"172.31.255.255 (RFC 1918 top)", "172.31.255.255", true},
		{"172.32.0.0 (just outside RFC 1918)", "172.32.0.0", false},
		{"192.168.0.0 (RFC 1918 boundary)", "192.168.0.0", true},
		{"192.168.255.255 (RFC 1918 top)", "192.168.255.255", true},
		{"127.0.0.1 (loopback)", "127.0.0.1", true},
		{"127.255.255.255 (loopback top)", "127.255.255.255", true},
		{"169.254.1.1 (link-local)", "169.254.1.1", true},
		{"::1 (IPv6 loopback)", "::1", true},
		{"fe80::1 (IPv6 link-local)", "fe80::1", true},
		{"fd00::1 (IPv6 unique local)", "fd00::1", true},
		{"::ffff:192.168.1.1 (IPv4-mapped private)", "::ffff:192.168.1.1", true},
		{"::ffff:10.0.0.1 (IPv4-mapped private)", "::ffff:10.0.0.1", true},
		{"1.1.1.1 (public)", "1.1.1.1", false},
		{"8.8.8.8 (public)", "8.8.8.8", false},
		{"2001:db8::1 (public IPv6)", "2001:db8::1", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resolver := func(host string) ([]string, error) {
				return []string{tc.ip}, nil
			}

			n := notify.NewSlackNotifier().WithResolver(resolver)
			cfgJSON, _ := json.Marshal(map[string]string{"url": "http://target.example.com/hook"})
			event := model.AlertEvent{
				AlertType: model.AlertDown,
				Check:     model.Check{Name: "test"},
				Channel:   model.Channel{Type: "slack", Config: cfgJSON},
			}

			err := n.Send(context.Background(), event)

			if tc.private {
				if err == nil {
					t.Errorf("IP %s: expected SSRF rejection, got nil error", tc.ip)
				}
			} else {
				// For public IPs the SSRF pre-check passes, but the dial will
				// fail (no real server at 1.2.3.4).  Any error here is fine
				// as long as the error is NOT an SSRF rejection.
				if err != nil {
					if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "reserved") {
						t.Errorf("IP %s: unexpected SSRF rejection: %v", tc.ip, err)
					}
					// Network dial error is expected for non-existent public IPs.
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// slackPayloadShape mirrors the structure we expect to receive; used only
// within this test file for JSON-unmarshalling / assertion.
// ---------------------------------------------------------------------------

type slackPayloadShape struct {
	Text        string `json:"text"`
	Attachments []struct {
		Color  string `json:"color"`
		Fields []struct {
			Title string `json:"title"`
			Value string `json:"value"`
			Short bool   `json:"short"`
		} `json:"fields"`
	} `json:"attachments"`
}
