package notify_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/myrrolinz/cronmon/internal/model"
	"github.com/myrrolinz/cronmon/internal/notify"
)

// ---------------------------------------------------------------------------
// Helpers (webhook-specific)
// ---------------------------------------------------------------------------

// makeWebhookEvent builds an AlertEvent whose Channel.Config points to the
// supplied URL (as if the user registered a webhook channel with that URL).
func makeWebhookEvent(alertType model.AlertType, endpointURL string) model.AlertEvent {
	ts := time.Date(2025, 2, 26, 2, 10, 0, 0, time.UTC)
	cfgJSON, _ := json.Marshal(map[string]string{"url": endpointURL})
	return model.AlertEvent{
		AlertType: alertType,
		Check: model.Check{
			ID:             "b4e8d3f2-0000-0000-0000-000000000002",
			Name:           "Nightly backup",
			Schedule:       "0 3 * * *",
			Status:         model.StatusDown,
			NextExpectedAt: &ts,
		},
		Channel: model.Channel{
			Type:   "webhook",
			Config: cfgJSON,
		},
	}
}

// ---------------------------------------------------------------------------
// WebhookNotifier tests
// ---------------------------------------------------------------------------

func TestWebhookNotifier_Type(t *testing.T) {
	t.Parallel()
	n := notify.NewWebhookNotifier()
	if got := n.Type(); got != "webhook" {
		t.Errorf("Type() = %q; want %q", got, "webhook")
	}
}

// TestWebhookNotifier_Send_DOWN verifies that a DOWN event results in a POST
// containing the expected alert_type and check fields.
func TestWebhookNotifier_Send_DOWN(t *testing.T) {
	cs := newCaptureServer(t, http.StatusOK, "")
	n := notify.NewWebhookNotifier().WithHTTPClient(plainClient(5 * time.Second))

	err := n.Send(context.Background(), makeWebhookEvent(model.AlertDown, cs.ts.URL))
	if err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}

	var p webhookPayloadShape
	if err := json.Unmarshal(cs.lastBody, &p); err != nil {
		t.Fatalf("parse captured body: %v", err)
	}

	if p.AlertType != "down" {
		t.Errorf("alert_type = %q; want %q", p.AlertType, "down")
	}
	if p.Check.Name != "Nightly backup" {
		t.Errorf("check.name = %q; want %q", p.Check.Name, "Nightly backup")
	}
	if p.Check.ID != "b4e8d3f2-0000-0000-0000-000000000002" {
		t.Errorf("check.id = %q; want UUID", p.Check.ID)
	}
	if p.Check.Schedule != "0 3 * * *" {
		t.Errorf("check.schedule = %q; want %q", p.Check.Schedule, "0 3 * * *")
	}
	if p.Check.Status != "down" {
		t.Errorf("check.status = %q; want %q", p.Check.Status, "down")
	}
	if p.Check.NextExpectedAt == nil {
		t.Error("check.next_expected_at is nil; want a non-nil time")
	}
	if p.SentAt.IsZero() {
		t.Error("sent_at is zero; want a non-zero time")
	}
}

// TestWebhookNotifier_Send_AlertTypes verifies payload alert_type for each
// alert variant.
func TestWebhookNotifier_Send_AlertTypes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		alertType     model.AlertType
		wantAlertType string
	}{
		{model.AlertDown, "down"},
		{model.AlertUp, "up"},
		{model.AlertFail, "fail"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(string(tc.alertType), func(t *testing.T) {
			cs := newCaptureServer(t, http.StatusOK, "")
			n := notify.NewWebhookNotifier().WithHTTPClient(plainClient(5 * time.Second))

			if err := n.Send(context.Background(), makeWebhookEvent(tc.alertType, cs.ts.URL)); err != nil {
				t.Fatalf("Send: %v", err)
			}

			var p webhookPayloadShape
			if err := json.Unmarshal(cs.lastBody, &p); err != nil {
				t.Fatalf("parse body: %v", err)
			}

			if p.AlertType != tc.wantAlertType {
				t.Errorf("alert_type = %q; want %q", p.AlertType, tc.wantAlertType)
			}
		})
	}
}

// TestWebhookNotifier_ContentTypeHeader verifies that the request carries the
// application/json content-type header.
func TestWebhookNotifier_ContentTypeHeader(t *testing.T) {
	cs := newCaptureServer(t, http.StatusOK, "")
	n := notify.NewWebhookNotifier().WithHTTPClient(plainClient(5 * time.Second))

	if err := n.Send(context.Background(), makeWebhookEvent(model.AlertDown, cs.ts.URL)); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if ct := cs.lastReq.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q; want %q", ct, "application/json")
	}
}

// TestWebhookNotifier_2xxResponsesAccepted verifies that all 2xx status codes
// are treated as success.
func TestWebhookNotifier_2xxResponsesAccepted(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		statusCode int
	}{
		{"200 OK", http.StatusOK},
		{"201 Created", http.StatusCreated},
		{"204 No Content", http.StatusNoContent},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cs := newCaptureServer(t, tc.statusCode, "")
			n := notify.NewWebhookNotifier().WithHTTPClient(plainClient(5 * time.Second))

			if err := n.Send(context.Background(), makeWebhookEvent(model.AlertDown, cs.ts.URL)); err != nil {
				t.Errorf("status %d: unexpected error: %v", tc.statusCode, err)
			}
		})
	}
}

// TestWebhookNotifier_Non2xxResponseReturnsError verifies that non-2xx HTTP
// responses are returned as errors.
func TestWebhookNotifier_Non2xxResponseReturnsError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		statusCode int
	}{
		{"400 Bad Request", http.StatusBadRequest},
		{"401 Unauthorized", http.StatusUnauthorized},
		{"500 Internal Server Error", http.StatusInternalServerError},
		{"503 Service Unavailable", http.StatusServiceUnavailable},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cs := newCaptureServer(t, tc.statusCode, "error")
			n := notify.NewWebhookNotifier().WithHTTPClient(plainClient(5 * time.Second))

			err := n.Send(context.Background(), makeWebhookEvent(model.AlertDown, cs.ts.URL))
			if err == nil {
				t.Errorf("status %d: expected error, got nil", tc.statusCode)
			}
		})
	}
}

// TestWebhookNotifier_MissingURL verifies that Send returns an error when the
// channel config does not contain a URL.
func TestWebhookNotifier_MissingURL(t *testing.T) {
	t.Parallel()
	n := notify.NewWebhookNotifier()

	event := model.AlertEvent{
		AlertType: model.AlertDown,
		Check:     model.Check{Name: "backup"},
		Channel: model.Channel{
			Type:   "webhook",
			Config: []byte(`{}`),
		},
	}

	if err := n.Send(context.Background(), event); err == nil {
		t.Error("expected error for missing url, got nil")
	}
}

// TestWebhookNotifier_InvalidConfig verifies that Send returns an error for
// malformed channel config JSON.
func TestWebhookNotifier_InvalidConfig(t *testing.T) {
	t.Parallel()
	n := notify.NewWebhookNotifier()

	event := model.AlertEvent{
		AlertType: model.AlertDown,
		Check:     model.Check{Name: "backup"},
		Channel: model.Channel{
			Type:   "webhook",
			Config: []byte(`not-json`),
		},
	}

	if err := n.Send(context.Background(), event); err == nil {
		t.Error("expected error for invalid JSON config, got nil")
	}
}

// TestWebhookNotifier_ContextTimeout verifies that Send respects a context
// deadline and returns before the server responds.
func TestWebhookNotifier_ContextTimeout(t *testing.T) {
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

	n := notify.NewWebhookNotifier().WithHTTPClient(plainClient(5 * time.Second))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := n.Send(ctx, makeWebhookEvent(model.AlertDown, hang.URL))
	elapsed := time.Since(start)

	if err == nil {
		t.Error("expected timeout error, got nil")
	}
	if elapsed > 2*time.Second {
		t.Errorf("Send took %v; expected it to respect the 50ms context deadline", elapsed)
	}
}

// TestWebhookNotifier_SSRF_RejectsPrivateIP verifies that Send refuses to
// connect to a webhook URL that resolves to a private IP address.
func TestWebhookNotifier_SSRF_RejectsPrivateIP(t *testing.T) {
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
		{"link-local (169.254.x)", "169.254.169.254"},
	}

	for _, tc := range privateAddresses {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			privateResolver := func(host string) ([]string, error) {
				return []string{tc.ip}, nil
			}

			n := notify.NewWebhookNotifier().WithResolver(privateResolver)

			cfgJSON, _ := json.Marshal(map[string]string{"url": "http://evil.internal/webhook"})
			event := model.AlertEvent{
				AlertType: model.AlertDown,
				Check:     model.Check{Name: "test"},
				Channel:   model.Channel{Type: "webhook", Config: cfgJSON},
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

// TestWebhookNotifier_SentAt_IsUTC verifies that the sent_at field in the
// payload is a valid, recent UTC timestamp.
func TestWebhookNotifier_SentAt_IsUTC(t *testing.T) {
	cs := newCaptureServer(t, http.StatusOK, "")
	n := notify.NewWebhookNotifier().WithHTTPClient(plainClient(5 * time.Second))

	before := time.Now().UTC()
	if err := n.Send(context.Background(), makeWebhookEvent(model.AlertDown, cs.ts.URL)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	after := time.Now().UTC()

	var p webhookPayloadShape
	if err := json.Unmarshal(cs.lastBody, &p); err != nil {
		t.Fatalf("parse body: %v", err)
	}

	if p.SentAt.Before(before) || p.SentAt.After(after) {
		t.Errorf("sent_at %v is outside the expected window [%v, %v]", p.SentAt, before, after)
	}
}

// ---------------------------------------------------------------------------
// webhookPayloadShape mirrors the structure we expect to receive; used only
// within this test file for JSON-unmarshalling / assertion.
// ---------------------------------------------------------------------------

type webhookPayloadShape struct {
	AlertType string    `json:"alert_type"`
	SentAt    time.Time `json:"sent_at"`
	Check     struct {
		ID             string     `json:"id"`
		Name           string     `json:"name"`
		Schedule       string     `json:"schedule"`
		Status         string     `json:"status"`
		NextExpectedAt *time.Time `json:"next_expected_at"`
	} `json:"check"`
}
