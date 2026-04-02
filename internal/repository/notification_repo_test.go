package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/myrrolinz/cronmon/internal/model"
)

func TestNotificationRepository_Create(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	c := makeCheck("notif-check", "Notif check")
	if err := repos.checkRepo.Create(ctx, &c); err != nil {
		t.Fatalf("Create check: %v", err)
	}
	ch := makeChannel(t, "email", "Alert email")
	if err := repos.channelRepo.Create(ctx, &ch); err != nil {
		t.Fatalf("Create channel: %v", err)
	}

	n := model.Notification{
		CheckID:   "notif-check",
		ChannelID: &ch.ID,
		Type:      model.AlertDown,
		SentAt:    truncSec(time.Now()),
	}
	if err := repos.notifRepo.Create(ctx, &n); err != nil {
		t.Fatalf("Create notification: %v", err)
	}
	if n.ID == 0 {
		t.Error("expected ID to be set after Create, got 0")
	}
}

func TestNotificationRepository_Create_WithError(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	c := makeCheck("err-check", "Error check")
	if err := repos.checkRepo.Create(ctx, &c); err != nil {
		t.Fatalf("Create check: %v", err)
	}

	errMsg := "connection refused"
	n := model.Notification{
		CheckID: "err-check",
		Type:    model.AlertDown,
		SentAt:  truncSec(time.Now()),
		Error:   &errMsg,
	}
	if err := repos.notifRepo.Create(ctx, &n); err != nil {
		t.Fatalf("Create notification with error: %v", err)
	}

	notifs, err := repos.notifRepo.ListByCheckID(ctx, "err-check", 10)
	if err != nil {
		t.Fatalf("ListByCheckID: %v", err)
	}
	if len(notifs) != 1 {
		t.Fatalf("len = %d, want 1", len(notifs))
	}
	if notifs[0].Error == nil || *notifs[0].Error != errMsg {
		t.Errorf("Error = %v, want %q", notifs[0].Error, errMsg)
	}
}

func TestNotificationRepository_Create_NilChannelID(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	c := makeCheck("null-ch-check", "Null channel check")
	if err := repos.checkRepo.Create(ctx, &c); err != nil {
		t.Fatalf("Create check: %v", err)
	}

	// channel_id = NULL is allowed (channel may have been deleted).
	n := model.Notification{
		CheckID:   "null-ch-check",
		ChannelID: nil,
		Type:      model.AlertUp,
		SentAt:    truncSec(time.Now()),
	}
	if err := repos.notifRepo.Create(ctx, &n); err != nil {
		t.Fatalf("Create notification with nil ChannelID: %v", err)
	}

	notifs, err := repos.notifRepo.ListByCheckID(ctx, "null-ch-check", 10)
	if err != nil {
		t.Fatalf("ListByCheckID: %v", err)
	}
	if len(notifs) != 1 {
		t.Fatalf("len = %d, want 1", len(notifs))
	}
	if notifs[0].ChannelID != nil {
		t.Errorf("ChannelID = %v, want nil", notifs[0].ChannelID)
	}
}

func TestNotificationRepository_ListByCheckID(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	c := makeCheck("list-notif-check", "List notif check")
	if err := repos.checkRepo.Create(ctx, &c); err != nil {
		t.Fatalf("Create check: %v", err)
	}

	base := truncSec(time.Now())
	for i := 0; i < 5; i++ {
		n := model.Notification{
			CheckID: "list-notif-check",
			Type:    model.AlertDown,
			SentAt:  base.Add(time.Duration(i) * time.Second),
		}
		if err := repos.notifRepo.Create(ctx, &n); err != nil {
			t.Fatalf("Create notification %d: %v", i, err)
		}
	}

	// Limit 3 — returned in DESC sent_at order.
	notifs, err := repos.notifRepo.ListByCheckID(ctx, "list-notif-check", 3)
	if err != nil {
		t.Fatalf("ListByCheckID: %v", err)
	}
	if len(notifs) != 3 {
		t.Fatalf("len = %d, want 3", len(notifs))
	}
	// Verify descending order.
	if !notifs[0].SentAt.After(notifs[1].SentAt) {
		t.Errorf("notifications not in DESC order: [0]=%v [1]=%v", notifs[0].SentAt, notifs[1].SentAt)
	}
}

func TestNotificationRepository_ListByCheckID_Empty(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	c := makeCheck("empty-notif-check", "Empty notif check")
	if err := repos.checkRepo.Create(ctx, &c); err != nil {
		t.Fatalf("Create check: %v", err)
	}

	notifs, err := repos.notifRepo.ListByCheckID(ctx, "empty-notif-check", 10)
	if err != nil {
		t.Fatalf("ListByCheckID: %v", err)
	}
	if len(notifs) != 0 {
		t.Errorf("len = %d, want 0", len(notifs))
	}
}

func TestNotificationRepository_ChannelDelete_SetsChannelIDNull(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	c := makeCheck("set-null-check", "Set null check")
	if err := repos.checkRepo.Create(ctx, &c); err != nil {
		t.Fatalf("Create check: %v", err)
	}
	ch := makeChannel(t, "email", "Ephemeral channel")
	if err := repos.channelRepo.Create(ctx, &ch); err != nil {
		t.Fatalf("Create channel: %v", err)
	}

	n := model.Notification{
		CheckID:   "set-null-check",
		ChannelID: &ch.ID,
		Type:      model.AlertDown,
		SentAt:    truncSec(time.Now()),
	}
	if err := repos.notifRepo.Create(ctx, &n); err != nil {
		t.Fatalf("Create notification: %v", err)
	}

	// Delete the channel — notification should be preserved but channel_id set to NULL.
	if err := repos.channelRepo.Delete(ctx, ch.ID); err != nil {
		t.Fatalf("Delete channel: %v", err)
	}

	notifs, err := repos.notifRepo.ListByCheckID(ctx, "set-null-check", 10)
	if err != nil {
		t.Fatalf("ListByCheckID after channel delete: %v", err)
	}
	if len(notifs) != 1 {
		t.Fatalf("expected notification to be preserved, len = %d", len(notifs))
	}
	if notifs[0].ChannelID != nil {
		t.Errorf("ChannelID = %v, want nil (ON DELETE SET NULL)", notifs[0].ChannelID)
	}
}

func TestNotificationRepository_ListByCheckID_PreservesFields(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	c := makeCheck("field-notif-check", "Field notif check")
	if err := repos.checkRepo.Create(ctx, &c); err != nil {
		t.Fatalf("Create check: %v", err)
	}
	ch := makeChannel(t, "slack", "Slack channel")
	if err := repos.channelRepo.Create(ctx, &ch); err != nil {
		t.Fatalf("Create channel: %v", err)
	}

	now := truncSec(time.Now())
	n := model.Notification{
		CheckID:   "field-notif-check",
		ChannelID: &ch.ID,
		Type:      model.AlertUp,
		SentAt:    now,
	}
	if err := repos.notifRepo.Create(ctx, &n); err != nil {
		t.Fatalf("Create: %v", err)
	}

	notifs, err := repos.notifRepo.ListByCheckID(ctx, "field-notif-check", 10)
	if err != nil {
		t.Fatalf("ListByCheckID: %v", err)
	}
	if len(notifs) != 1 {
		t.Fatalf("len = %d, want 1", len(notifs))
	}
	got := notifs[0]
	if got.CheckID != "field-notif-check" {
		t.Errorf("CheckID = %q", got.CheckID)
	}
	if got.ChannelID == nil || *got.ChannelID != ch.ID {
		t.Errorf("ChannelID = %v, want %d", got.ChannelID, ch.ID)
	}
	if got.Type != model.AlertUp {
		t.Errorf("Type = %q, want %q", got.Type, model.AlertUp)
	}
	if !got.SentAt.Equal(now) {
		t.Errorf("SentAt = %v, want %v", got.SentAt, now)
	}
	if got.Error != nil {
		t.Errorf("Error = %v, want nil", got.Error)
	}
}
