package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/myrrolinz/cronmon/internal/model"
)

// ---------------------------------------------------------------------------
// Webhook JSON payload
// ---------------------------------------------------------------------------

// webhookPayload is the JSON body POSTed to a generic webhook endpoint.
// The schema is intentionally flat and stable so that consumers can rely on it
// across CronMon versions.
type webhookPayload struct {
	AlertType string       `json:"alert_type"` // "down" | "up" | "fail"
	Check     webhookCheck `json:"check"`
	SentAt    time.Time    `json:"sent_at"` // UTC, RFC3339
}

// webhookCheck is the subset of model.Check fields included in the payload.
// Only stable, non-sensitive fields are exported.
type webhookCheck struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Schedule       string     `json:"schedule"`
	Status         string     `json:"status"`
	NextExpectedAt *time.Time `json:"next_expected_at,omitempty"`
}

// ---------------------------------------------------------------------------
// WebhookNotifier
// ---------------------------------------------------------------------------

// WebhookNotifier sends alert notifications via POST to an arbitrary HTTP/HTTPS
// endpoint.  It implements Notifier and is safe for concurrent use.
//
// SSRF mitigation: all connections are made through an SSRF-safe HTTP client
// that resolves the target hostname, rejects private/reserved IP addresses, and
// connects directly to the validated IP to prevent TOCTOU DNS-rebinding attacks.
type WebhookNotifier struct {
	client *http.Client
}

// NewWebhookNotifier creates a production WebhookNotifier backed by an
// SSRF-safe HTTP client that uses the system DNS resolver.
func NewWebhookNotifier() *WebhookNotifier {
	return &WebhookNotifier{
		client: newSSRFSafeClient(defaultResolve, 15*time.Second),
	}
}

// WithHTTPClient replaces the internal HTTP client.
// This is intended for testing only; production code should use NewWebhookNotifier.
func (n *WebhookNotifier) WithHTTPClient(c *http.Client) *WebhookNotifier {
	n.client = c
	return n
}

// WithResolver replaces the DNS resolver used for SSRF validation.
// A new SSRF-safe client is constructed around the supplied resolver.
// This is intended for testing only — use it to inject a resolver that
// returns controlled addresses (e.g. private IPs) so that SSRF rejection
// can be tested without making real DNS queries.
func (n *WebhookNotifier) WithResolver(fn func(string) ([]string, error)) *WebhookNotifier {
	n.client = newSSRFSafeClient(fn, 15*time.Second)
	return n
}

// Type implements Notifier.
func (n *WebhookNotifier) Type() string { return "webhook" }

// Send implements Notifier.  It parses the endpoint URL from
// event.Channel.Config (JSON: {"url": "https://..."}), builds a JSON payload,
// and POSTs it to the endpoint.
//
// Any 2xx HTTP response is treated as success.  Non-2xx responses are returned
// as errors.
func (n *WebhookNotifier) Send(ctx context.Context, event model.AlertEvent) error {
	// ── 1. Parse endpoint URL ─────────────────────────────────────────────
	var chCfg struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(event.Channel.Config, &chCfg); err != nil {
		return fmt.Errorf("webhookNotifier.Send: parse channel config: %w", err)
	}
	if chCfg.URL == "" {
		return fmt.Errorf("webhookNotifier.Send: channel config missing \"url\"")
	}

	// ── 2. Build payload ──────────────────────────────────────────────────
	payload := webhookPayload{
		AlertType: string(event.AlertType),
		Check: webhookCheck{
			ID:             event.Check.ID,
			Name:           event.Check.Name,
			Schedule:       event.Check.Schedule,
			Status:         string(event.Check.Status),
			NextExpectedAt: event.Check.NextExpectedAt,
		},
		SentAt: time.Now().UTC(),
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("webhookNotifier.Send: marshal payload: %w", err)
	}

	// ── 3. POST to endpoint ───────────────────────────────────────────────
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chCfg.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhookNotifier.Send: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhookNotifier.Send: do request: %w", err)
	}
	defer resp.Body.Close()               //nolint:errcheck
	_, _ = io.Copy(io.Discard, resp.Body) // drain so the connection can be reused

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhookNotifier.Send: non-2xx status %d from endpoint", resp.StatusCode)
	}

	return nil
}
