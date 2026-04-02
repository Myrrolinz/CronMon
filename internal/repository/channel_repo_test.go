package repository_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/myrrolinz/cronmon/internal/model"
	"github.com/myrrolinz/cronmon/internal/repository"
)

func TestChannelRepository_Create(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	ch := makeChannel(t, "email", "Primary email")
	if err := repos.channelRepo.Create(ctx, &ch); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if ch.ID == 0 {
		t.Error("expected ID to be set after Create, got 0")
	}
}

func TestChannelRepository_GetByID(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	ch := makeChannel(t, "slack", "Slack alerts")
	if err := repos.channelRepo.Create(ctx, &ch); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repos.channelRepo.GetByID(ctx, ch.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ID != ch.ID {
		t.Errorf("ID = %d, want %d", got.ID, ch.ID)
	}
	if got.Type != "slack" {
		t.Errorf("Type = %q, want %q", got.Type, "slack")
	}
	if got.Name != "Slack alerts" {
		t.Errorf("Name = %q", got.Name)
	}
	if string(got.Config) != string(ch.Config) {
		t.Errorf("Config = %s, want %s", got.Config, ch.Config)
	}
	if !got.CreatedAt.Equal(ch.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, ch.CreatedAt)
	}
}

func TestChannelRepository_GetByID_NotFound(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	_, err := repos.channelRepo.GetByID(ctx, 9999)
	if !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("error = %v, want to wrap ErrNotFound", err)
	}
}

func TestChannelRepository_ListAll(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	// Empty table.
	got, err := repos.channelRepo.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll on empty table: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}

	ch1 := makeChannel(t, "email", "Email channel")
	ch2 := makeChannel(t, "webhook", "Webhook channel")
	// ch2 is created 1s after ch1 to guarantee different created_at after truncation to second precision.
	ch2.CreatedAt = ch1.CreatedAt.Add(time.Second)

	if err := repos.channelRepo.Create(ctx, &ch1); err != nil {
		t.Fatalf("Create ch1: %v", err)
	}
	if err := repos.channelRepo.Create(ctx, &ch2); err != nil {
		t.Fatalf("Create ch2: %v", err)
	}

	all, err := repos.channelRepo.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("len = %d, want 2", len(all))
	}
}

func TestChannelRepository_Delete(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	ch := makeChannel(t, "email", "To delete")
	if err := repos.channelRepo.Create(ctx, &ch); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := repos.channelRepo.Delete(ctx, ch.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := repos.channelRepo.GetByID(ctx, ch.ID)
	if !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("expected ErrNotFound after Delete, got %v", err)
	}
}

func TestChannelRepository_Delete_NotFound(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	err := repos.channelRepo.Delete(ctx, 9999)
	if !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("error = %v, want to wrap ErrNotFound", err)
	}
}

func TestChannelRepository_AttachToCheck(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	c := makeCheck("attach-check", "Attach check")
	if err := repos.checkRepo.Create(ctx, &c); err != nil {
		t.Fatalf("Create check: %v", err)
	}
	ch := makeChannel(t, "email", "Attach channel")
	if err := repos.channelRepo.Create(ctx, &ch); err != nil {
		t.Fatalf("Create channel: %v", err)
	}

	if err := repos.channelRepo.AttachToCheck(ctx, "attach-check", ch.ID); err != nil {
		t.Fatalf("AttachToCheck: %v", err)
	}

	linked, err := repos.channelRepo.ListByCheckID(ctx, "attach-check")
	if err != nil {
		t.Fatalf("ListByCheckID: %v", err)
	}
	if len(linked) != 1 {
		t.Fatalf("len = %d, want 1", len(linked))
	}
	if linked[0].ID != ch.ID {
		t.Errorf("linked channel ID = %d, want %d", linked[0].ID, ch.ID)
	}
}

func TestChannelRepository_AttachToCheck_Idempotent(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	c := makeCheck("idem-check", "Idempotent check")
	if err := repos.checkRepo.Create(ctx, &c); err != nil {
		t.Fatalf("Create check: %v", err)
	}
	ch := makeChannel(t, "email", "Idempotent channel")
	if err := repos.channelRepo.Create(ctx, &ch); err != nil {
		t.Fatalf("Create channel: %v", err)
	}

	if err := repos.channelRepo.AttachToCheck(ctx, "idem-check", ch.ID); err != nil {
		t.Fatalf("first AttachToCheck: %v", err)
	}
	// Second attach should be a no-op (INSERT OR IGNORE), not an error.
	if err := repos.channelRepo.AttachToCheck(ctx, "idem-check", ch.ID); err != nil {
		t.Errorf("second AttachToCheck should be idempotent, got: %v", err)
	}

	linked, err := repos.channelRepo.ListByCheckID(ctx, "idem-check")
	if err != nil {
		t.Fatalf("ListByCheckID: %v", err)
	}
	if len(linked) != 1 {
		t.Errorf("len = %d, want 1 (idempotent attach should not duplicate)", len(linked))
	}
}

func TestChannelRepository_AttachToCheck_FKViolation(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	// Unknown check — should fail FK constraint.
	err := repos.channelRepo.AttachToCheck(ctx, "ghost-check", 999)
	if err == nil {
		t.Error("expected FK violation for unknown check_id, got nil")
	}
}

func TestChannelRepository_DetachFromCheck(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	c := makeCheck("detach-check", "Detach check")
	if err := repos.checkRepo.Create(ctx, &c); err != nil {
		t.Fatalf("Create check: %v", err)
	}
	ch := makeChannel(t, "email", "Detach channel")
	if err := repos.channelRepo.Create(ctx, &ch); err != nil {
		t.Fatalf("Create channel: %v", err)
	}

	if err := repos.channelRepo.AttachToCheck(ctx, "detach-check", ch.ID); err != nil {
		t.Fatalf("AttachToCheck: %v", err)
	}
	if err := repos.channelRepo.DetachFromCheck(ctx, "detach-check", ch.ID); err != nil {
		t.Fatalf("DetachFromCheck: %v", err)
	}

	linked, err := repos.channelRepo.ListByCheckID(ctx, "detach-check")
	if err != nil {
		t.Fatalf("ListByCheckID: %v", err)
	}
	if len(linked) != 0 {
		t.Errorf("len = %d, want 0 after detach", len(linked))
	}
}

func TestChannelRepository_DetachFromCheck_NonExistent_NoError(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	// Detaching a non-existent link should not return an error.
	if err := repos.channelRepo.DetachFromCheck(ctx, "ghost", 999); err != nil {
		t.Errorf("DetachFromCheck on non-existent link: %v", err)
	}
}

func TestChannelRepository_ListByCheckID(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	c := makeCheck("multi-check", "Multi channel check")
	if err := repos.checkRepo.Create(ctx, &c); err != nil {
		t.Fatalf("Create check: %v", err)
	}

	ch1 := makeChannel(t, "email", "Email 1")
	ch2 := makeChannel(t, "slack", "Slack 1")
	if err := repos.channelRepo.Create(ctx, &ch1); err != nil {
		t.Fatalf("Create ch1: %v", err)
	}
	if err := repos.channelRepo.Create(ctx, &ch2); err != nil {
		t.Fatalf("Create ch2: %v", err)
	}

	if err := repos.channelRepo.AttachToCheck(ctx, "multi-check", ch1.ID); err != nil {
		t.Fatalf("AttachToCheck ch1: %v", err)
	}
	if err := repos.channelRepo.AttachToCheck(ctx, "multi-check", ch2.ID); err != nil {
		t.Fatalf("AttachToCheck ch2: %v", err)
	}

	linked, err := repos.channelRepo.ListByCheckID(ctx, "multi-check")
	if err != nil {
		t.Fatalf("ListByCheckID: %v", err)
	}
	if len(linked) != 2 {
		t.Fatalf("len = %d, want 2", len(linked))
	}
}

func TestChannelRepository_ListByCheckID_Empty(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	c := makeCheck("no-ch-check", "No channels check")
	if err := repos.checkRepo.Create(ctx, &c); err != nil {
		t.Fatalf("Create check: %v", err)
	}

	linked, err := repos.channelRepo.ListByCheckID(ctx, "no-ch-check")
	if err != nil {
		t.Fatalf("ListByCheckID: %v", err)
	}
	if len(linked) != 0 {
		t.Errorf("len = %d, want 0", len(linked))
	}
}

func TestChannelRepository_Delete_CascadesToCheckChannels(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	c := makeCheck("cc-check", "Check channel cascade check")
	if err := repos.checkRepo.Create(ctx, &c); err != nil {
		t.Fatalf("Create check: %v", err)
	}
	ch := makeChannel(t, "email", "Cascade channel")
	if err := repos.channelRepo.Create(ctx, &ch); err != nil {
		t.Fatalf("Create channel: %v", err)
	}
	if err := repos.channelRepo.AttachToCheck(ctx, "cc-check", ch.ID); err != nil {
		t.Fatalf("AttachToCheck: %v", err)
	}

	// Deleting the channel should cascade-delete the check_channels row.
	if err := repos.channelRepo.Delete(ctx, ch.ID); err != nil {
		t.Fatalf("Delete channel: %v", err)
	}

	linked, err := repos.channelRepo.ListByCheckID(ctx, "cc-check")
	if err != nil {
		t.Fatalf("ListByCheckID after channel delete: %v", err)
	}
	if len(linked) != 0 {
		t.Errorf("expected 0 check_channels after channel cascade delete, got %d", len(linked))
	}
}

func TestChannelRepository_AllChannelTypes(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	types := []string{"email", "slack", "webhook"}
	for _, ct := range types {
		ch := makeChannel(t, ct, ct+" channel")
		if err := repos.channelRepo.Create(ctx, &ch); err != nil {
			t.Errorf("Create channel type %q: %v", ct, err)
		}
	}

	all, err := repos.channelRepo.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != len(types) {
		t.Errorf("len = %d, want %d", len(all), len(types))
	}
}

func TestChannelRepository_Create_InvalidTypeFails(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	ch := model.Channel{
		Type:      "sms", // not in CHECK constraint
		Name:      "SMS channel",
		Config:    mustMarshal(t, map[string]string{"number": "+1234"}),
		CreatedAt: makeCheck("x", "x").CreatedAt,
	}
	if err := repos.channelRepo.Create(ctx, &ch); err == nil {
		t.Error("expected CHECK constraint violation for invalid type, got nil")
	}
}
