package notify_test

// Integration tests for Worker with a real SQLite database.
//
// These tests use db.Open(":memory:") so that PRAGMA foreign_keys=ON is active
// and the full migration schema is applied — the same environment used by the
// repository integration tests.  They exist to validate assumptions that the
// unit-test mocks cannot confirm:
//
//  1. Inserting a notification with a stale (deleted) channel_id actually
//     triggers an FK constraint error in SQLite.
//  2. The retry with ChannelID=nil succeeds and the row is readable.
//  3. The Worker wired to the real repo writes the expected columns.

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/myrrolinz/cronmon/internal/db"
	"github.com/myrrolinz/cronmon/internal/model"
	"github.com/myrrolinz/cronmon/internal/notify"
	"github.com/myrrolinz/cronmon/internal/repository"
)

// openTestDB opens an in-memory SQLite database with migrations applied.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() }) //nolint:errcheck
	return database
}

// seedCheck inserts a minimal check into the database.
func seedCheck(t *testing.T, repo repository.CheckRepository, id string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	c := model.Check{
		ID:        id,
		Name:      "Integration test check",
		Schedule:  "0 2 * * *",
		Grace:     10,
		Status:    model.StatusDown,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := repo.Create(context.Background(), &c); err != nil {
		t.Fatalf("seed check: %v", err)
	}
}

// seedChannel inserts an email channel and returns its auto-assigned ID.
func seedChannel(t *testing.T, repo repository.ChannelRepository) int64 {
	t.Helper()
	cfg, _ := json.Marshal(map[string]string{"address": "ops@example.com"})
	ch := model.Channel{
		Type:      "email",
		Name:      "Integration test channel",
		Config:    cfg,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := repo.Create(context.Background(), &ch); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	return ch.ID
}

// ---------------------------------------------------------------------------
// Integration: success path — notification written with correct columns
// ---------------------------------------------------------------------------

// TestWorkerIntegration_SuccessWritesToSQLite boots the Worker against a real
// SQLite database and verifies that a successful dispatch produces a correctly
// populated notification row (non-nil channel_id, nil error, correct type).
func TestWorkerIntegration_SuccessWritesToSQLite(t *testing.T) {
	sqlDB := openTestDB(t)
	checkRepo := repository.NewCheckRepository(sqlDB)
	chanRepo := repository.NewChannelRepository(sqlDB)
	notifRepo := repository.NewNotificationRepository(sqlDB)

	seedCheck(t, checkRepo, "int-check-1")
	chanID := seedChannel(t, chanRepo)

	n := &mockNotifier{channelType: "email"}
	alertCh := make(chan model.AlertEvent, 1)
	w := notify.NewWorker(alertCh, map[string]notify.Notifier{"email": n}, notifRepo)
	w.Start()

	alertCh <- model.AlertEvent{
		Check:     model.Check{ID: "int-check-1", Name: "Integration test check"},
		Channel:   model.Channel{ID: chanID, Type: "email"},
		AlertType: model.AlertDown,
	}
	close(alertCh)
	w.Wait()

	notifs, err := notifRepo.ListByCheckID(context.Background(), "int-check-1", 10)
	if err != nil {
		t.Fatalf("ListByCheckID: %v", err)
	}
	if len(notifs) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notifs))
	}

	rec := notifs[0]
	if rec.ChannelID == nil || *rec.ChannelID != chanID {
		t.Errorf("ChannelID = %v, want %d", rec.ChannelID, chanID)
	}
	if rec.Error != nil {
		t.Errorf("Error = %q, want nil", *rec.Error)
	}
	if rec.Type != model.AlertDown {
		t.Errorf("Type = %q, want %q", rec.Type, model.AlertDown)
	}
}

// ---------------------------------------------------------------------------
// Integration: FK retry — deleted channel triggers retry with nil channel_id
// ---------------------------------------------------------------------------

// TestWorkerIntegration_DeletedChannelRetries verifies the FK retry path using
// real SQLite with foreign_keys=ON.  The scenario:
//  1. Create a channel, note its ID.
//  2. Delete the channel (simulating the race between enqueue and insert).
//  3. Dispatch an event still referencing the now-deleted channel ID.
//  4. Confirm the notification row is persisted with ChannelID=nil.
func TestWorkerIntegration_DeletedChannelRetries(t *testing.T) {
	sqlDB := openTestDB(t)
	checkRepo := repository.NewCheckRepository(sqlDB)
	chanRepo := repository.NewChannelRepository(sqlDB)
	notifRepo := repository.NewNotificationRepository(sqlDB)

	seedCheck(t, checkRepo, "int-check-2")
	chanID := seedChannel(t, chanRepo)

	// Delete the channel — now the channel_id is stale.
	if err := chanRepo.Delete(context.Background(), chanID); err != nil {
		t.Fatalf("Delete channel: %v", err)
	}

	n := &mockNotifier{channelType: "email"}
	alertCh := make(chan model.AlertEvent, 1)
	w := notify.NewWorker(alertCh, map[string]notify.Notifier{"email": n}, notifRepo)
	w.Start()

	alertCh <- model.AlertEvent{
		Check:     model.Check{ID: "int-check-2", Name: "Integration test check"},
		Channel:   model.Channel{ID: chanID, Type: "email"},
		AlertType: model.AlertDown,
	}
	close(alertCh)
	w.Wait()

	notifs, err := notifRepo.ListByCheckID(context.Background(), "int-check-2", 10)
	if err != nil {
		t.Fatalf("ListByCheckID: %v", err)
	}
	if len(notifs) != 1 {
		t.Fatalf("expected 1 notification after FK retry, got %d — audit record lost", len(notifs))
	}
	if notifs[0].ChannelID != nil {
		t.Errorf("ChannelID = %v, want nil after FK retry", *notifs[0].ChannelID)
	}
}
