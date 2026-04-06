package notify_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/myrrolinz/cronmon/internal/model"
	"github.com/myrrolinz/cronmon/internal/notify"
)

// ---------------------------------------------------------------------------
// Mock notifier
// ---------------------------------------------------------------------------

type mockNotifier struct {
	mu          sync.Mutex
	channelType string
	calls       []model.AlertEvent
	err         error
	delay       time.Duration // simulate slow send
}

func (m *mockNotifier) Send(ctx context.Context, event model.AlertEvent) error {
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, event)
	return m.err
}

func (m *mockNotifier) Type() string { return m.channelType }

func (m *mockNotifier) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// ---------------------------------------------------------------------------
// Mock notification repository
// ---------------------------------------------------------------------------

type mockNotifRepo struct {
	mu            sync.Mutex
	notifications []*model.Notification
	createErr     error
}

func (m *mockNotifRepo) Create(_ context.Context, n *model.Notification) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *n
	m.notifications = append(m.notifications, &cp)
	return nil
}

func (m *mockNotifRepo) ListByCheckID(_ context.Context, checkID string, limit int) ([]*model.Notification, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*model.Notification
	for _, n := range m.notifications {
		if n.CheckID == checkID {
			result = append(result, n)
		}
	}
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (m *mockNotifRepo) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.notifications)
}

func (m *mockNotifRepo) last() *model.Notification {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.notifications) == 0 {
		return nil
	}
	return m.notifications[len(m.notifications)-1]
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func makeWorkerEvent(channelType string) model.AlertEvent {
	channelID := int64(1)
	return model.AlertEvent{
		Check: model.Check{
			ID:     "check-uuid-1",
			Name:   "Daily backup",
			Status: model.StatusDown,
		},
		Channel: model.Channel{
			ID:   channelID,
			Type: channelType,
			Name: "Test channel",
		},
		AlertType: model.AlertDown,
	}
}

func startWorker(
	t *testing.T,
	notifiers map[string]notify.Notifier,
	repo *mockNotifRepo,
	opts ...notify.WorkerOption,
) (chan model.AlertEvent, *notify.Worker) {
	t.Helper()
	alertCh := make(chan model.AlertEvent, 16)
	w := notify.NewWorker(alertCh, notifiers, repo, opts...)
	w.Start()
	return alertCh, w
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestWorker_SuccessRecordsNilError verifies that a successfully dispatched
// alert writes a notification record with a nil error field.
func TestWorker_SuccessRecordsNilError(t *testing.T) {
	n := &mockNotifier{channelType: "email"}
	repo := &mockNotifRepo{}
	ch, w := startWorker(t, map[string]notify.Notifier{"email": n}, repo)

	ch <- makeWorkerEvent("email")
	close(ch)
	w.Wait()

	if n.callCount() != 1 {
		t.Fatalf("expected 1 Send call, got %d", n.callCount())
	}
	rec := repo.last()
	if rec == nil {
		t.Fatal("expected notification record")
	}
	if rec.Error != nil {
		t.Errorf("expected nil error in notification, got %q", *rec.Error)
	}
	if rec.CheckID != "check-uuid-1" {
		t.Errorf("unexpected CheckID: %q", rec.CheckID)
	}
	if rec.Type != model.AlertDown {
		t.Errorf("unexpected alert type: %q", rec.Type)
	}
}

// TestWorker_SendErrorRecordsErrorMessage verifies that when Send returns an
// error the error message is stored in the notification record.
func TestWorker_SendErrorRecordsErrorMessage(t *testing.T) {
	n := &mockNotifier{channelType: "email", err: errors.New("smtp: connection refused")}
	repo := &mockNotifRepo{}
	ch, w := startWorker(t, map[string]notify.Notifier{"email": n}, repo)

	ch <- makeWorkerEvent("email")
	close(ch)
	w.Wait()

	rec := repo.last()
	if rec == nil {
		t.Fatal("expected notification record")
	}
	if rec.Error == nil {
		t.Fatal("expected non-nil error in notification record")
	}
	if *rec.Error != "smtp: connection refused" {
		t.Errorf("unexpected error string: %q", *rec.Error)
	}
}

// TestWorker_UnknownChannelTypeRecordsError verifies that events for channel
// types with no registered notifier still produce a notification record that
// captures the "no notifier" error rather than silently dropping the event.
func TestWorker_UnknownChannelTypeRecordsError(t *testing.T) {
	repo := &mockNotifRepo{}
	ch, w := startWorker(t, map[string]notify.Notifier{}, repo) // no notifiers

	ch <- makeWorkerEvent("slack")
	close(ch)
	w.Wait()

	rec := repo.last()
	if rec == nil {
		t.Fatal("expected notification record for unknown channel type")
	}
	if rec.Error == nil {
		t.Fatal("expected error recorded for unknown channel type")
	}
}

// TestWorker_DrainMultipleEvents verifies that the worker processes all
// queued events before Wait() returns.
func TestWorker_DrainMultipleEvents(t *testing.T) {
	const n = 5
	notifier := &mockNotifier{channelType: "email"}
	repo := &mockNotifRepo{}
	ch, w := startWorker(t, map[string]notify.Notifier{"email": notifier}, repo)

	for range n {
		ch <- makeWorkerEvent("email")
	}
	close(ch)
	w.Wait()

	if notifier.callCount() != n {
		t.Errorf("expected %d Send calls, got %d", n, notifier.callCount())
	}
	if repo.count() != n {
		t.Errorf("expected %d notification records, got %d", n, repo.count())
	}
}

// TestWorker_TimeoutCancelsSlowNotifier verifies that the per-send timeout
// fires when a notifier takes longer than the configured deadline.  The
// notification record must be written and must contain the context error.
func TestWorker_TimeoutCancelsSlowNotifier(t *testing.T) {
	// delay is longer than the injected send timeout
	notifier := &mockNotifier{channelType: "email", delay: 200 * time.Millisecond}
	repo := &mockNotifRepo{}
	ch, w := startWorker(
		t,
		map[string]notify.Notifier{"email": notifier},
		repo,
		notify.WithSendTimeout(50*time.Millisecond),
	)

	ch <- makeWorkerEvent("email")
	close(ch)
	w.Wait()

	rec := repo.last()
	if rec == nil {
		t.Fatal("expected notification record after timeout")
	}
	if rec.Error == nil {
		t.Fatal("expected error recorded when send timed out")
	}
	if *rec.Error != context.DeadlineExceeded.Error() {
		t.Errorf("expected DeadlineExceeded error, got %q", *rec.Error)
	}
}

// TestWorker_ChannelIDStoredInNotification verifies the channel ID is
// correctly propagated into the notification record.
func TestWorker_ChannelIDStoredInNotification(t *testing.T) {
	notifier := &mockNotifier{channelType: "email"}
	repo := &mockNotifRepo{}
	ch, w := startWorker(t, map[string]notify.Notifier{"email": notifier}, repo)

	event := makeWorkerEvent("email")
	event.Channel.ID = 99
	ch <- event
	close(ch)
	w.Wait()

	rec := repo.last()
	if rec == nil {
		t.Fatal("expected notification record")
	}
	if rec.ChannelID == nil || *rec.ChannelID != 99 {
		t.Errorf("expected ChannelID 99, got %v", rec.ChannelID)
	}
}

// TestWorker_RecoveryAlertType verifies AlertUp events are recorded correctly.
func TestWorker_RecoveryAlertType(t *testing.T) {
	notifier := &mockNotifier{channelType: "email"}
	repo := &mockNotifRepo{}
	ch, w := startWorker(t, map[string]notify.Notifier{"email": notifier}, repo)

	event := makeWorkerEvent("email")
	event.AlertType = model.AlertUp
	ch <- event
	close(ch)
	w.Wait()

	rec := repo.last()
	if rec == nil {
		t.Fatal("expected notification record")
	}
	if rec.Type != model.AlertUp {
		t.Errorf("expected AlertUp, got %q", rec.Type)
	}
}

// TestWorker_RepoErrorDoesNotPanic verifies that a notification repository
// failure does not crash the worker or prevent subsequent events from being
// processed.
func TestWorker_RepoErrorDoesNotPanic(t *testing.T) {
	notifier := &mockNotifier{channelType: "email"}
	repo := &mockNotifRepo{createErr: errors.New("db: disk full")}
	ch, w := startWorker(t, map[string]notify.Notifier{"email": notifier}, repo)

	ch <- makeWorkerEvent("email")
	ch <- makeWorkerEvent("email")
	close(ch)
	w.Wait()

	// No panic, and Send was called for both events
	if notifier.callCount() != 2 {
		t.Errorf("expected 2 Send calls, got %d", notifier.callCount())
	}
}
