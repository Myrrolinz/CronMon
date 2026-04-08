package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"text/template"
	"time"

	"github.com/myrrolinz/cronmon/internal/model"
)

// ---------------------------------------------------------------------------
// EmailConfig
// ---------------------------------------------------------------------------

// EmailConfig holds the SMTP configuration consumed by EmailNotifier.
type EmailConfig struct {
	Host    string // SMTP server hostname
	Port    string // SMTP server port (default "587")
	User    string // SMTP username; empty = no authentication
	Pass    string // SMTP password
	From    string // RFC 5321 envelope sender / From header
	TLS     bool   // true = upgrade with STARTTLS; false = plain (local/test only)
	BaseURL string // public root URL, e.g. "https://cronmon.example.com"
}

// ---------------------------------------------------------------------------
// EmailNotifier
// ---------------------------------------------------------------------------

// EmailNotifier sends alert emails via SMTP.
// It constructs a multipart/alternative message (plain-text + HTML) and
// delivers it to the address stored in the event's channel config.
type EmailNotifier struct {
	cfg EmailConfig
	// dialFunc is the function used to open the raw TCP connection.
	// When nil the standard net.Dialer is used.  Override in tests.
	dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)
}

// NewEmailNotifier creates a new EmailNotifier.
func NewEmailNotifier(cfg EmailConfig) *EmailNotifier {
	return &EmailNotifier{cfg: cfg}
}

// WithDialer overrides the dial function used to open the SMTP connection.
// This is intended for testing only; production callers should not set it.
func (n *EmailNotifier) WithDialer(
	fn func(ctx context.Context, network, addr string) (net.Conn, error),
) *EmailNotifier {
	n.dialFunc = fn
	return n
}

// Type implements Notifier.
func (n *EmailNotifier) Type() string { return "email" }

// Send implements Notifier.  It parses the recipient address from
// event.Channel.Config (JSON: {"address": "user@example.com"}), builds a
// multipart/alternative email, and delivers it via SMTP.
func (n *EmailNotifier) Send(ctx context.Context, event model.AlertEvent) error {
	// ── 1. Parse recipient address ────────────────────────────────────────
	var chCfg struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal(event.Channel.Config, &chCfg); err != nil {
		return fmt.Errorf("emailNotifier.Send: parse channel config: %w", err)
	}
	if chCfg.Address == "" {
		return fmt.Errorf("emailNotifier.Send: channel config missing \"address\"")
	}

	// ── 2. Build message ──────────────────────────────────────────────────
	subject := buildSubject(event)

	plainBody, htmlBody, err := buildBodies(event, n.cfg.BaseURL)
	if err != nil {
		return fmt.Errorf("emailNotifier.Send: build body: %w", err)
	}

	msg, err := buildMIMEMessage(n.cfg.From, chCfg.Address, subject, plainBody, htmlBody)
	if err != nil {
		return fmt.Errorf("emailNotifier.Send: build MIME message: %w", err)
	}

	// ── 3. Deliver ────────────────────────────────────────────────────────
	return n.sendSMTP(ctx, chCfg.Address, msg)
}

// ---------------------------------------------------------------------------
// Subject and body builders
// ---------------------------------------------------------------------------

// buildSubject returns the email subject line for the given alert event.
// The format mandated by the spec:
//
//	DOWN:      [CronMon] ⚠ "CheckName" is DOWN
//	FAIL:      [CronMon] ✗ "CheckName" FAILED
//	RECOVERED: [CronMon] ✓ "CheckName" RECOVERED
func buildSubject(event model.AlertEvent) string {
	switch event.AlertType {
	case model.AlertDown:
		return fmt.Sprintf(`[CronMon] ⚠ "%s" is DOWN`, event.Check.Name)
	case model.AlertFail:
		return fmt.Sprintf(`[CronMon] ✗ "%s" FAILED`, event.Check.Name)
	default: // AlertUp
		return fmt.Sprintf(`[CronMon] ✓ "%s" RECOVERED`, event.Check.Name)
	}
}

// emailData is the template context for both plain-text and HTML bodies.
type emailData struct {
	CheckName    string
	Schedule     string
	Status       string // "⚠ DOWN" | "✓ RECOVERED"
	NextExpected string // RFC3339 in UTC, or "unknown"
	PingURL      string
	HeadingColor string // "#cc0000" for down, "#007700" for recovered
}

var plainTemplate = template.Must(template.New("plain").Parse(
	`CronMon Alert
=============

Check:         {{ .CheckName }}
Schedule:      {{ .Schedule }}
Status:        {{ .Status }}
Next Expected: {{ .NextExpected }}
Ping URL:      {{ .PingURL }}

---
This is an automated alert from CronMon.
`))

var htmlTemplate = template.Must(template.New("html").Parse(
	`<!DOCTYPE html>
<html>
<head><meta charset="utf-8"></head>
<body style="font-family:sans-serif;max-width:600px;margin:0 auto;padding:20px">
  <h2 style="color:{{ .HeadingColor }}">CronMon Alert — {{ .Status }}</h2>
  <table style="border-collapse:collapse;width:100%;margin-bottom:16px">
    <tr>
      <td style="padding:8px;border:1px solid #ddd;font-weight:bold;width:35%">Check</td>
      <td style="padding:8px;border:1px solid #ddd">{{ .CheckName }}</td>
    </tr>
    <tr>
      <td style="padding:8px;border:1px solid #ddd;font-weight:bold">Schedule</td>
      <td style="padding:8px;border:1px solid #ddd">{{ .Schedule }}</td>
    </tr>
    <tr>
      <td style="padding:8px;border:1px solid #ddd;font-weight:bold">Status</td>
      <td style="padding:8px;border:1px solid #ddd">{{ .Status }}</td>
    </tr>
    <tr>
      <td style="padding:8px;border:1px solid #ddd;font-weight:bold">Next Expected</td>
      <td style="padding:8px;border:1px solid #ddd">{{ .NextExpected }}</td>
    </tr>
  </table>
  <p>Ping URL: <a href="{{ .PingURL }}">{{ .PingURL }}</a></p>
  <p style="color:#999;font-size:12px">This is an automated alert from CronMon.</p>
</body>
</html>
`))

// buildBodies renders the plain-text and HTML email bodies using event data.
func buildBodies(event model.AlertEvent, baseURL string) (plain, html string, err error) {
	nextExpected := "unknown"
	if event.Check.NextExpectedAt != nil {
		nextExpected = event.Check.NextExpectedAt.UTC().Format(time.RFC3339)
	}

	status := "⚠ DOWN"
	headingColor := "#cc0000"
	switch event.AlertType {
	case model.AlertUp:
		status = "✓ RECOVERED"
		headingColor = "#007700"
	case model.AlertFail:
		status = "✗ FAILED"
		headingColor = "#cc6600"
	}

	pingURL := strings.TrimRight(baseURL, "/") + "/ping/" + event.Check.ID

	data := emailData{
		CheckName:    event.Check.Name,
		Schedule:     event.Check.Schedule,
		Status:       status,
		NextExpected: nextExpected,
		PingURL:      pingURL,
		HeadingColor: headingColor,
	}

	var plainBuf bytes.Buffer
	if err = plainTemplate.Execute(&plainBuf, data); err != nil {
		return "", "", fmt.Errorf("plain template: %w", err)
	}

	var htmlBuf bytes.Buffer
	if err = htmlTemplate.Execute(&htmlBuf, data); err != nil {
		return "", "", fmt.Errorf("html template: %w", err)
	}

	return plainBuf.String(), htmlBuf.String(), nil
}

// sanitizeHeader returns an error if value contains CR or LF, which would
// allow header injection into the RFC 5322 message.
func sanitizeHeader(name, value string) error {
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("emailNotifier: header %q contains illegal CR or LF", name)
	}
	return nil
}

// isLoopback reports whether host is a loopback address ("localhost",
// 127.x.x.x, or ::1).
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// buildMIMEMessage constructs a multipart/alternative MIME email message.
// The returned byte slice is ready to be written to a smtp.Client.Data writer.
func buildMIMEMessage(from, to, subject, plainBody, htmlBody string) ([]byte, error) {
	// Reject header injection: CR or LF in From/To would split RFC 5322 headers.
	if err := sanitizeHeader("From", from); err != nil {
		return nil, err
	}
	if err := sanitizeHeader("To", to); err != nil {
		return nil, err
	}

	// Use a time-based boundary; uniqueness within a process is sufficient.
	boundary := fmt.Sprintf("cronmon%d", time.Now().UnixNano())

	var buf bytes.Buffer

	fmt.Fprintf(&buf, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&buf, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&buf, "From: %s\r\n", from)
	fmt.Fprintf(&buf, "To: %s\r\n", to)
	// Encode subject to support the UTF-8 emoji characters.
	fmt.Fprintf(&buf, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&buf, "Content-Type: multipart/alternative; boundary=%q\r\n", boundary)
	fmt.Fprintf(&buf, "\r\n")

	// ── Plain-text part ───────────────────────────────────────────────────
	fmt.Fprintf(&buf, "--%s\r\n", boundary)
	fmt.Fprintf(&buf, "Content-Type: text/plain; charset=utf-8\r\n")
	fmt.Fprintf(&buf, "Content-Transfer-Encoding: 8bit\r\n")
	fmt.Fprintf(&buf, "\r\n")
	buf.WriteString(plainBody)
	fmt.Fprintf(&buf, "\r\n")

	// ── HTML part ─────────────────────────────────────────────────────────
	fmt.Fprintf(&buf, "--%s\r\n", boundary)
	fmt.Fprintf(&buf, "Content-Type: text/html; charset=utf-8\r\n")
	fmt.Fprintf(&buf, "Content-Transfer-Encoding: 8bit\r\n")
	fmt.Fprintf(&buf, "\r\n")
	buf.WriteString(htmlBody)
	fmt.Fprintf(&buf, "\r\n")

	fmt.Fprintf(&buf, "--%s--\r\n", boundary)

	return buf.Bytes(), nil
}

// ---------------------------------------------------------------------------
// SMTP delivery
// ---------------------------------------------------------------------------

// sendSMTP opens a connection to the configured SMTP server, optionally
// upgrades it with STARTTLS, authenticates if credentials are set, and sends
// msg to the supplied recipient address.
func (n *EmailNotifier) sendSMTP(ctx context.Context, to string, msg []byte) error {
	addr := net.JoinHostPort(n.cfg.Host, n.cfg.Port)

	dial := n.dialFunc
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}

	conn, err := dial(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("emailNotifier: dial %s: %w", addr, err)
	}

	// Wire ctx deadline to the raw connection so all subsequent SMTP I/O
	// (EHLO, STARTTLS, AUTH, DATA …) is bounded by the caller's context.
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			_ = conn.Close()
			return fmt.Errorf("emailNotifier: set deadline: %w", err)
		}
	}

	client, err := smtp.NewClient(conn, n.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("emailNotifier: smtp.NewClient: %w", err)
	}
	defer client.Close() //nolint:errcheck

	// ── Optional STARTTLS ─────────────────────────────────────────────────
	if n.cfg.TLS {
		tlsCfg := &tls.Config{ServerName: n.cfg.Host, MinVersion: tls.VersionTLS12}
		if err := client.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("emailNotifier: StartTLS: %w", err)
		}
	}

	// ── Optional AUTH ─────────────────────────────────────────────────────
	if n.cfg.User != "" {
		// Refuse to transmit credentials over a plaintext connection to any
		// non-loopback host to prevent accidental exposure in production.
		if !n.cfg.TLS && !isLoopback(n.cfg.Host) {
			return fmt.Errorf("emailNotifier: refusing to send credentials over unencrypted connection to %q; set SMTP_TLS=true", n.cfg.Host)
		}
		auth := smtp.PlainAuth("", n.cfg.User, n.cfg.Pass, n.cfg.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("emailNotifier: Auth: %w", err)
		}
	}

	// ── Envelope + body ───────────────────────────────────────────────────
	if err := client.Mail(n.cfg.From); err != nil {
		return fmt.Errorf("emailNotifier: MAIL FROM: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("emailNotifier: RCPT TO: %w", err)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("emailNotifier: DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("emailNotifier: write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("emailNotifier: close data writer: %w", err)
	}

	return nil
}
