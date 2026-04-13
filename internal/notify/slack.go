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
// Slack JSON types
// ---------------------------------------------------------------------------

// slackPayload is the JSON body sent to a Slack incoming-webhook URL.
type slackPayload struct {
	Text        string            `json:"text"`
	Attachments []slackAttachment `json:"attachments,omitempty"`
}

type slackAttachment struct {
	Color  string       `json:"color"`
	Fields []slackField `json:"fields"`
}

type slackField struct {
	Title string `json:"title"`
	Value string `json:"value"`
	Short bool   `json:"short"`
}

// ---------------------------------------------------------------------------
// SlackNotifier
// ---------------------------------------------------------------------------

// SlackNotifier sends alert notifications to a Slack incoming-webhook URL.
// It implements Notifier and is safe for concurrent use.
type SlackNotifier struct {
	client *http.Client
}

// NewSlackNotifier creates a production SlackNotifier backed by an SSRF-safe
// HTTP client that uses the system DNS resolver.
func NewSlackNotifier() *SlackNotifier {
	return &SlackNotifier{
		client: newSSRFSafeClient(defaultResolve, 15*time.Second),
	}
}

// WithHTTPClient replaces the internal HTTP client.
// This is intended for testing only; production code should use NewSlackNotifier.
func (n *SlackNotifier) WithHTTPClient(c *http.Client) *SlackNotifier {
	n.client = c
	return n
}

// WithResolver replaces the DNS resolver used for SSRF validation.
// A new SSRF-safe client is constructed around the supplied resolver.
// This is intended for testing only — use it to inject a resolver that
// returns controlled addresses (e.g. private IPs) so that SSRF rejection
// can be tested without making real DNS queries.
func (n *SlackNotifier) WithResolver(fn func(string) ([]string, error)) *SlackNotifier {
	n.client = newSSRFSafeClient(fn, 15*time.Second)
	return n
}

// Type implements Notifier.
func (n *SlackNotifier) Type() string { return "slack" }

// Send implements Notifier.  It parses the webhook URL from
// event.Channel.Config (JSON: {"url": "https://hooks.slack.com/..."}),
// builds a Slack message payload, and POSTs it to the webhook URL.
//
// Send returns an error for any non-200 HTTP response, in accordance with
// Slack's API contract (200 with body "ok" on success).
func (n *SlackNotifier) Send(ctx context.Context, event model.AlertEvent) error {
	// ── 1. Parse webhook URL ──────────────────────────────────────────────
	var chCfg struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(event.Channel.Config, &chCfg); err != nil {
		return fmt.Errorf("slackNotifier.Send: parse channel config: %w", err)
	}
	if chCfg.URL == "" {
		return fmt.Errorf("slackNotifier.Send: channel config missing \"url\"")
	}

	// ── 2. Build payload ─────────────────────────────────────────────────
	payload := buildSlackPayload(event)
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("slackNotifier.Send: marshal payload: %w", err)
	}

	// ── 3. POST to Slack ──────────────────────────────────────────────────
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chCfg.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("slackNotifier.Send: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("slackNotifier.Send: do request: %w", err)
	}
	defer resp.Body.Close()               //nolint:errcheck
	_, _ = io.Copy(io.Discard, resp.Body) // drain so the connection can be reused

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slackNotifier.Send: unexpected status %d from Slack webhook", resp.StatusCode)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Payload builder
// ---------------------------------------------------------------------------

// buildSlackPayload constructs the Slack JSON payload for an AlertEvent.
func buildSlackPayload(event model.AlertEvent) slackPayload {
	var color, icon, statusText string
	switch event.AlertType {
	case model.AlertDown:
		color = "#cc0000"
		icon = "⚠"
		statusText = "DOWN"
	case model.AlertFail:
		color = "#ff8c00"
		icon = "✗"
		statusText = "FAILED"
	default: // AlertUp / recovery
		color = "#007700"
		icon = "✓"
		statusText = "RECOVERED"
	}

	text := fmt.Sprintf(`%s CronMon: "%s" is %s`, icon, event.Check.Name, statusText)

	fields := []slackField{
		{Title: "Check", Value: event.Check.Name, Short: true},
		{Title: "Status", Value: statusText, Short: true},
		{Title: "Schedule", Value: event.Check.Schedule, Short: true},
	}
	if event.Check.NextExpectedAt != nil {
		fields = append(fields, slackField{
			Title: "Next Expected",
			Value: event.Check.NextExpectedAt.UTC().Format(time.RFC3339),
			Short: true,
		})
	}

	return slackPayload{
		Text: text,
		Attachments: []slackAttachment{
			{Color: color, Fields: fields},
		},
	}
}
