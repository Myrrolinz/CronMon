package notify_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/myrrolinz/cronmon/internal/model"
	"github.com/myrrolinz/cronmon/internal/notify"
)

// ---------------------------------------------------------------------------
// Mock SMTP server
// ---------------------------------------------------------------------------

// capturedEmail holds the data captured by the mock SMTP server for one
// successfully delivered message (i.e. one DATA...dot sequence).
type capturedEmail struct {
	from     string
	to       []string
	body     string   // raw MIME message written by the client
	commands []string // all non-data SMTP commands received in this connection
}

// mockSMTPServer is a minimal, synchronous-safe fake SMTP server.
// It captures sent messages and exposes them via receive().
type mockSMTPServer struct {
	listener net.Listener
	addr     string
	// msgs is a buffered channel; each successfully delivered message is
	// pushed here so that receive() can wait with a timeout.
	msgs chan capturedEmail
}

// newMockSMTPServer starts a mock SMTP server on a random loopback port.
// The server is shut down when t.Cleanup runs.
func newMockSMTPServer(t *testing.T) *mockSMTPServer {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("mockSMTPServer: listen: %v", err)
	}

	s := &mockSMTPServer{
		listener: l,
		addr:     l.Addr().String(),
		msgs:     make(chan capturedEmail, 16),
	}

	t.Cleanup(func() { _ = s.listener.Close() })

	go s.run()
	return s
}

// receive waits up to 3 s for the next delivered message, or fatals.
func (s *mockSMTPServer) receive(t *testing.T) capturedEmail {
	t.Helper()
	select {
	case msg := <-s.msgs:
		return msg
	case <-time.After(3 * time.Second):
		t.Fatal("mockSMTPServer: timed out waiting for email")
		return capturedEmail{}
	}
}

func (s *mockSMTPServer) run() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return // listener closed
		}
		go s.handleConn(conn)
	}
}

// handleConn speaks enough SMTP to receive one message per connection.
// It responds 502 to STARTTLS so that TLS=true tests can verify the attempt.
func (s *mockSMTPServer) handleConn(conn net.Conn) {
	defer conn.Close() //nolint:errcheck

	r := bufio.NewReader(conn)

	writeln := func(format string, args ...any) {
		_, _ = fmt.Fprintf(conn, format+"\r\n", args...)
	}

	// Greeting.
	writeln("220 mock.smtp.test ESMTP CronMon-test")

	var em capturedEmail
	var inData bool
	var dataLines strings.Builder

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")

		// ── DATA body section ──────────────────────────────────────────────
		if inData {
			if line == "." {
				em.body = dataLines.String()
				// Send message to channel BEFORE the 250 ACK so that by the
				// time the client's w.Close() returns the message is already
				// available in s.msgs.
				s.msgs <- em
				writeln("250 OK: queued")
				inData = false
				dataLines.Reset()
			} else {
				// RFC 5321 §4.5.2 dot-unstuffing.
				if len(line) > 0 && line[0] == '.' {
					line = line[1:]
				}
				dataLines.WriteString(line + "\n")
			}
			continue
		}

		// ── Command section ────────────────────────────────────────────────
		em.commands = append(em.commands, line)
		upper := strings.ToUpper(line)

		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			writeln("250-mock.smtp.test")
			writeln("250 AUTH PLAIN LOGIN")
		case strings.HasPrefix(upper, "AUTH"):
			writeln("235 2.7.0 Authentication successful")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			em.from = extractAngle(line[len("MAIL FROM:"):])
			writeln("250 OK")
		case strings.HasPrefix(upper, "RCPT TO:"):
			em.to = append(em.to, extractAngle(line[len("RCPT TO:"):]))
			writeln("250 OK")
		case upper == "DATA":
			writeln("354 End data with <CR><LF>.<CR><LF>")
			inData = true
		case upper == "QUIT":
			writeln("221 Bye")
			return
		case strings.HasPrefix(upper, "STARTTLS"):
			// Reject STARTTLS so TLS=true tests can assert error propagation.
			writeln("502 5.5.2 Command not implemented")
		default:
			writeln("500 5.5.1 Unrecognized command")
		}
	}
}

// extractAngle extracts the address from an SMTP envelope token like
// `<user@example.com>`, trimming angle brackets and whitespace.
func extractAngle(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimLeft(s, "<")
	s = strings.TrimRight(s, ">")
	return s
}

// extractHeader parses a single RFC 5322 header value from the raw MIME
// message.  It returns the empty string when the header is absent.
func extractHeader(body, header string) string {
	prefix := header + ": "
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Test fixtures
// ---------------------------------------------------------------------------

// makeEvent builds a minimal AlertEvent for the given alert type.
func makeEvent(at model.AlertType) model.AlertEvent {
	t := time.Date(2025, 2, 26, 2, 10, 0, 0, time.UTC)
	cfgJSON, _ := json.Marshal(map[string]string{"address": "alert@example.com"})
	return model.AlertEvent{
		AlertType: at,
		Check: model.Check{
			ID:             "a3f9c2d1-0000-0000-0000-000000000001",
			Name:           "Database backup",
			Schedule:       "0 2 * * *",
			NextExpectedAt: &t,
		},
		Channel: model.Channel{
			Type:   "email",
			Config: cfgJSON,
		},
	}
}

// makeNotifier creates an EmailNotifier pointed at addr with the given TLS flag.
func makeNotifier(addr string, useTLS bool) *notify.EmailNotifier {
	host, port, _ := net.SplitHostPort(addr)
	return notify.NewEmailNotifier(notify.EmailConfig{
		Host:    host,
		Port:    port,
		From:    "cronmon@example.com",
		TLS:     useTLS,
		BaseURL: "https://cronmon.example.com",
	})
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestEmailNotifier_Type(t *testing.T) {
	t.Parallel()
	n := notify.NewEmailNotifier(notify.EmailConfig{})
	if got := n.Type(); got != "email" {
		t.Errorf("Type() = %q; want %q", got, "email")
	}
}

// TestEmailNotifier_Send_DOWN verifies that a DOWN alert is delivered and the
// captured body contains the expected status and emoji.
func TestEmailNotifier_Send_DOWN(t *testing.T) {
	srv := newMockSMTPServer(t)
	n := makeNotifier(srv.addr, false)

	if err := n.Send(context.Background(), makeEvent(model.AlertDown)); err != nil {
		t.Fatalf("Send: %v", err)
	}

	msg := srv.receive(t)

	if !strings.Contains(msg.body, "DOWN") {
		t.Error("body should contain 'DOWN'")
	}
	// Plain-text body must include the ⚠ warning sign (in the subject or body).
	if !strings.Contains(msg.body, "⚠") {
		t.Error("body should contain '⚠'")
	}
}

// TestEmailNotifier_Send_UP verifies that an UP (recovery) alert is delivered
// and the captured body contains the expected status and emoji.
func TestEmailNotifier_Send_UP(t *testing.T) {
	srv := newMockSMTPServer(t)
	n := makeNotifier(srv.addr, false)

	if err := n.Send(context.Background(), makeEvent(model.AlertUp)); err != nil {
		t.Fatalf("Send: %v", err)
	}

	msg := srv.receive(t)

	if !strings.Contains(msg.body, "RECOVERED") {
		t.Error("body should contain 'RECOVERED'")
	}
	if !strings.Contains(msg.body, "✓") {
		t.Error("body should contain '✓'")
	}
}

// TestEmailNotifier_SubjectFormat verifies the exact subject line for both
// alert types after decoding Q-encoded UTF-8.
func TestEmailNotifier_SubjectFormat(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		alertType model.AlertType
		checkName string
		wantSubj  string
	}{
		{
			name:      "DOWN alert",
			alertType: model.AlertDown,
			checkName: "Database backup",
			wantSubj:  `[CronMon] ⚠ "Database backup" is DOWN`,
		},
		{
			name:      "UP alert",
			alertType: model.AlertUp,
			checkName: "Database backup",
			wantSubj:  `[CronMon] ✓ "Database backup" RECOVERED`,
		},
		{
			name:      "DOWN alert with special chars",
			alertType: model.AlertDown,
			checkName: "nightly/backup",
			wantSubj:  `[CronMon] ⚠ "nightly/backup" is DOWN`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := newMockSMTPServer(t)
			ev := makeEvent(tc.alertType)
			ev.Check.Name = tc.checkName
			n := makeNotifier(srv.addr, false)

			if err := n.Send(context.Background(), ev); err != nil {
				t.Fatalf("Send: %v", err)
			}

			msg := srv.receive(t)
			rawSubj := extractHeader(msg.body, "Subject")
			if rawSubj == "" {
				t.Fatal("Subject header not found in captured message")
			}

			dec := new(mime.WordDecoder)
			got, err := dec.DecodeHeader(rawSubj)
			if err != nil {
				t.Fatalf("decode subject header %q: %v", rawSubj, err)
			}

			if got != tc.wantSubj {
				t.Errorf("subject = %q\n         want %q", got, tc.wantSubj)
			}
		})
	}
}

// TestEmailNotifier_BodyContent verifies that all required fields are present
// in the plain-text part of the email body.
func TestEmailNotifier_BodyContent(t *testing.T) {
	srv := newMockSMTPServer(t)
	ev := makeEvent(model.AlertDown)
	n := makeNotifier(srv.addr, false)

	if err := n.Send(context.Background(), ev); err != nil {
		t.Fatalf("Send: %v", err)
	}

	msg := srv.receive(t)

	requiredSubstrings := []string{
		ev.Check.Name,     // check name
		ev.Check.Schedule, // schedule
		ev.Check.NextExpectedAt.UTC().Format(time.RFC3339), // next_expected_at
		"https://cronmon.example.com/ping/" + ev.Check.ID,  // ping URL
	}

	for _, want := range requiredSubstrings {
		if !strings.Contains(msg.body, want) {
			t.Errorf("body does not contain %q", want)
		}
	}
}

// TestEmailNotifier_TLS_Disabled verifies that no STARTTLS command is issued
// when TLS is disabled, and the message is delivered successfully.
func TestEmailNotifier_TLS_Disabled(t *testing.T) {
	srv := newMockSMTPServer(t)
	n := makeNotifier(srv.addr, false)

	if err := n.Send(context.Background(), makeEvent(model.AlertDown)); err != nil {
		t.Fatalf("Send with TLS=false: %v", err)
	}

	msg := srv.receive(t)

	for _, cmd := range msg.commands {
		if strings.HasPrefix(strings.ToUpper(cmd), "STARTTLS") {
			t.Error("STARTTLS must NOT be sent when TLS is disabled")
		}
	}
}

// TestEmailNotifier_TLS_Enabled verifies that the EmailNotifier attempts
// STARTTLS when TLS is enabled.  The mock server rejects it (502), so Send
// must return a non-nil error wrapping the failure.
func TestEmailNotifier_TLS_Enabled(t *testing.T) {
	srv := newMockSMTPServer(t)
	n := makeNotifier(srv.addr, true) // TLS=true

	err := n.Send(context.Background(), makeEvent(model.AlertDown))
	if err == nil {
		t.Fatal("expected error when server rejects STARTTLS, got nil")
	}
	if !strings.Contains(err.Error(), "StartTLS") {
		t.Errorf("error should mention StartTLS; got: %v", err)
	}
}

// TestEmailNotifier_InvalidChannelConfig verifies that Send returns a
// meaningful error when the channel config JSON is malformed.
func TestEmailNotifier_InvalidChannelConfig(t *testing.T) {
	t.Parallel()

	n := notify.NewEmailNotifier(notify.EmailConfig{
		Host: "localhost", Port: "25", From: "from@example.com",
	})

	ev := makeEvent(model.AlertDown)
	ev.Channel.Config = []byte(`not valid json`)

	err := n.Send(context.Background(), ev)
	if err == nil {
		t.Fatal("expected error for invalid channel config JSON, got nil")
	}
}

// TestEmailNotifier_MissingAddress verifies that Send returns an error when
// the channel config omits the required "address" key.
func TestEmailNotifier_MissingAddress(t *testing.T) {
	t.Parallel()

	n := notify.NewEmailNotifier(notify.EmailConfig{
		Host: "localhost", Port: "25", From: "from@example.com",
	})

	ev := makeEvent(model.AlertDown)
	ev.Channel.Config = []byte(`{}`) // address is absent

	err := n.Send(context.Background(), ev)
	if err == nil {
		t.Fatal("expected error for missing address, got nil")
	}
}

// TestEmailNotifier_DialFailure verifies that Send propagates a dial error.
// Uses WithDialer to inject a deterministic failure rather than relying on a
// specific port being closed (which is environment-dependent).
func TestEmailNotifier_DialFailure(t *testing.T) {
	t.Parallel()

	wantErr := fmt.Errorf("injected dial failure")
	n := notify.NewEmailNotifier(notify.EmailConfig{
		Host:    "127.0.0.1",
		Port:    "25",
		From:    "from@example.com",
		BaseURL: "https://cronmon.example.com",
	}).WithDialer(func(_ context.Context, _, _ string) (net.Conn, error) {
		return nil, wantErr
	})

	err := n.Send(context.Background(), makeEvent(model.AlertDown))
	if err == nil {
		t.Fatal("expected dial error, got nil")
	}
	if !strings.Contains(err.Error(), wantErr.Error()) {
		t.Fatalf("expected error to contain %q; got: %v", wantErr.Error(), err)
	}
}

// TestEmailNotifier_WithAuth verifies that authentication credentials are
// forwarded to the SMTP server when User is set.
func TestEmailNotifier_WithAuth(t *testing.T) {
	srv := newMockSMTPServer(t)
	host, port, _ := net.SplitHostPort(srv.addr)
	n := notify.NewEmailNotifier(notify.EmailConfig{
		Host:    host,
		Port:    port,
		From:    "cronmon@example.com",
		User:    "smtpuser",
		Pass:    "smtppass",
		TLS:     false,
		BaseURL: "https://cronmon.example.com",
	})

	if err := n.Send(context.Background(), makeEvent(model.AlertDown)); err != nil {
		t.Fatalf("Send with auth: %v", err)
	}

	msg := srv.receive(t)

	// AUTH command should appear in the command list.
	var authSeen bool
	for _, cmd := range msg.commands {
		if strings.HasPrefix(strings.ToUpper(cmd), "AUTH") {
			authSeen = true
			break
		}
	}
	if !authSeen {
		t.Error("AUTH command should be sent when User is set")
	}
}

// TestEmailNotifier_AuthWithoutTLS_NonLoopback verifies that PlainAuth is
// refused when TLS is disabled and the configured host is not loopback,
// to prevent accidental credential exposure in production.
func TestEmailNotifier_AuthWithoutTLS_NonLoopback(t *testing.T) {
	srv := newMockSMTPServer(t)

	n := notify.NewEmailNotifier(notify.EmailConfig{
		Host:    "smtp.example.com", // non-loopback hostname
		Port:    "587",
		From:    "cronmon@example.com",
		User:    "smtpuser",
		Pass:    "smtppass",
		TLS:     false,
		BaseURL: "https://cronmon.example.com",
	}).WithDialer(func(ctx context.Context, network, _ string) (net.Conn, error) {
		// Redirect to the local mock server so EHLO succeeds.
		return (&net.Dialer{}).DialContext(ctx, network, srv.addr)
	})

	err := n.Send(context.Background(), makeEvent(model.AlertDown))
	if err == nil {
		t.Fatal("expected error for PlainAuth over unencrypted non-loopback, got nil")
	}
	if !strings.Contains(err.Error(), "unencrypted") {
		t.Errorf("error should mention \"unencrypted\"; got: %v", err)
	}
}

// TestEmailNotifier_HeaderInjection verifies that CR or LF in the From address
// or recipient address is rejected before any bytes are sent to the server.
func TestEmailNotifier_HeaderInjection(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		from    string
		cfgJSON []byte
		wantErr string
	}{
		{
			name: "CR in From",
			from: "cronmon@example.com\rX-Injected: bad",
			cfgJSON: func() []byte {
				b, _ := json.Marshal(map[string]string{"address": "alert@example.com"})
				return b
			}(),
			wantErr: `"From"`,
		},
		{
			name: "LF in recipient",
			from: "cronmon@example.com",
			cfgJSON: func() []byte {
				b, _ := json.Marshal(map[string]string{"address": "alert@example.com\nX-Injected: bad"})
				return b
			}(),
			wantErr: `"To"`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// No server needed: sanitization fires before any network I/O.
			n := notify.NewEmailNotifier(notify.EmailConfig{
				Host:    "localhost",
				Port:    "25",
				From:    tc.from,
				BaseURL: "https://cronmon.example.com",
			})
			ev := makeEvent(model.AlertDown)
			ev.Channel.Config = tc.cfgJSON

			err := n.Send(context.Background(), ev)
			if err == nil {
				t.Fatal("expected header injection error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error should contain %s; got: %v", tc.wantErr, err)
			}
		})
	}
}
