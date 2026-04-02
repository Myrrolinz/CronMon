package repository_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/myrrolinz/cronmon/internal/model"
	"github.com/myrrolinz/cronmon/internal/repository"
)

func TestCheckRepository_Create(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	c := makeCheck("uuid-1", "Daily backup")
	if err := repos.checkRepo.Create(ctx, &c); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repos.checkRepo.GetByID(ctx, "uuid-1")
	if err != nil {
		t.Fatalf("GetByID after Create: %v", err)
	}
	if got.ID != c.ID {
		t.Errorf("ID = %q, want %q", got.ID, c.ID)
	}
	if got.Name != c.Name {
		t.Errorf("Name = %q, want %q", got.Name, c.Name)
	}
	if got.Schedule != c.Schedule {
		t.Errorf("Schedule = %q, want %q", got.Schedule, c.Schedule)
	}
	if got.Grace != c.Grace {
		t.Errorf("Grace = %d, want %d", got.Grace, c.Grace)
	}
	if got.Status != model.StatusNew {
		t.Errorf("Status = %q, want %q", got.Status, model.StatusNew)
	}
	if got.Slug != nil {
		t.Errorf("Slug = %v, want nil", got.Slug)
	}
	if got.LastPingAt != nil {
		t.Errorf("LastPingAt = %v, want nil", got.LastPingAt)
	}
	if got.NextExpectedAt != nil {
		t.Errorf("NextExpectedAt = %v, want nil", got.NextExpectedAt)
	}
	if !got.CreatedAt.Equal(c.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, c.CreatedAt)
	}
}

func TestCheckRepository_Create_WithNullableFields(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	now := truncSec(time.Now())
	slug := "daily-backup"
	c := makeCheck("uuid-slug", "Daily backup")
	c.Slug = &slug
	c.LastPingAt = &now
	c.NextExpectedAt = &now

	if err := repos.checkRepo.Create(ctx, &c); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repos.checkRepo.GetByID(ctx, "uuid-slug")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Slug == nil || *got.Slug != slug {
		t.Errorf("Slug = %v, want %q", got.Slug, slug)
	}
	if got.LastPingAt == nil || !got.LastPingAt.Equal(now) {
		t.Errorf("LastPingAt = %v, want %v", got.LastPingAt, now)
	}
	if got.NextExpectedAt == nil || !got.NextExpectedAt.Equal(now) {
		t.Errorf("NextExpectedAt = %v, want %v", got.NextExpectedAt, now)
	}
}

func TestCheckRepository_Create_DuplicateIDFails(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	c := makeCheck("dup-uuid", "Check A")
	if err := repos.checkRepo.Create(ctx, &c); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	c2 := makeCheck("dup-uuid", "Check B")
	if err := repos.checkRepo.Create(ctx, &c2); err == nil {
		t.Error("expected error on duplicate primary key, got nil")
	}
}

func TestCheckRepository_GetByID_NotFound(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	_, err := repos.checkRepo.GetByID(ctx, "does-not-exist")
	if err == nil {
		t.Fatal("expected ErrNotFound, got nil")
	}
	if !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("error = %v, want to wrap ErrNotFound", err)
	}
}

func TestCheckRepository_GetByUUID(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	c := makeCheck("ping-uuid", "Ping check")
	if err := repos.checkRepo.Create(ctx, &c); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repos.checkRepo.GetByUUID(ctx, "ping-uuid")
	if err != nil {
		t.Fatalf("GetByUUID: %v", err)
	}
	if got.ID != "ping-uuid" {
		t.Errorf("ID = %q, want %q", got.ID, "ping-uuid")
	}
}

func TestCheckRepository_GetByUUID_NotFound(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	_, err := repos.checkRepo.GetByUUID(ctx, "missing")
	if !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("error = %v, want to wrap ErrNotFound", err)
	}
}

func TestCheckRepository_ListAll(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	// Empty table should return nil, not error.
	got, err := repos.checkRepo.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll on empty table: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}

	// Insert two checks.
	c1 := makeCheck("id-1", "Check 1")
	c2 := makeCheck("id-2", "Check 2")
	// Ensure ordering: c1 created earlier.
	c2.CreatedAt = c1.CreatedAt.Add(time.Second)
	c2.UpdatedAt = c2.CreatedAt

	if err := repos.checkRepo.Create(ctx, &c1); err != nil {
		t.Fatalf("Create c1: %v", err)
	}
	if err := repos.checkRepo.Create(ctx, &c2); err != nil {
		t.Fatalf("Create c2: %v", err)
	}

	all, err := repos.checkRepo.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("len = %d, want 2", len(all))
	}
	if all[0].ID != "id-1" || all[1].ID != "id-2" {
		t.Errorf("order wrong: got [%s, %s]", all[0].ID, all[1].ID)
	}
}

func TestCheckRepository_Update(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	c := makeCheck("up-id", "Original name")
	if err := repos.checkRepo.Create(ctx, &c); err != nil {
		t.Fatalf("Create: %v", err)
	}

	now := truncSec(time.Now().Add(time.Minute))
	c.Name = "Updated name"
	c.Schedule = "*/5 * * * *"
	c.Grace = 5
	c.Status = model.StatusUp
	c.Tags = "prod,critical"
	c.UpdatedAt = now
	c.LastPingAt = &now
	c.NextExpectedAt = &now

	if err := repos.checkRepo.Update(ctx, &c); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := repos.checkRepo.GetByID(ctx, "up-id")
	if err != nil {
		t.Fatalf("GetByID after Update: %v", err)
	}
	if got.Name != "Updated name" {
		t.Errorf("Name = %q, want %q", got.Name, "Updated name")
	}
	if got.Schedule != "*/5 * * * *" {
		t.Errorf("Schedule = %q", got.Schedule)
	}
	if got.Grace != 5 {
		t.Errorf("Grace = %d, want 5", got.Grace)
	}
	if got.Status != model.StatusUp {
		t.Errorf("Status = %q, want %q", got.Status, model.StatusUp)
	}
	if got.Tags != "prod,critical" {
		t.Errorf("Tags = %q", got.Tags)
	}
	if got.LastPingAt == nil || !got.LastPingAt.Equal(now) {
		t.Errorf("LastPingAt = %v, want %v", got.LastPingAt, now)
	}
}

func TestCheckRepository_Update_NotFound(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	c := makeCheck("ghost", "Ghost check")
	err := repos.checkRepo.Update(ctx, &c)
	if !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("error = %v, want to wrap ErrNotFound", err)
	}
}

func TestCheckRepository_Delete(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	c := makeCheck("del-id", "To delete")
	if err := repos.checkRepo.Create(ctx, &c); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := repos.checkRepo.Delete(ctx, "del-id"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := repos.checkRepo.GetByID(ctx, "del-id")
	if !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("expected ErrNotFound after Delete, got %v", err)
	}
}

func TestCheckRepository_Delete_NotFound(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	err := repos.checkRepo.Delete(ctx, "ghost")
	if !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("error = %v, want to wrap ErrNotFound", err)
	}
}

func TestCheckRepository_Delete_CascadesToPings(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	c := makeCheck("casc-check", "Cascade check")
	if err := repos.checkRepo.Create(ctx, &c); err != nil {
		t.Fatalf("Create check: %v", err)
	}

	p := model.Ping{
		CheckID:   "casc-check",
		Type:      model.PingSuccess,
		CreatedAt: truncSec(time.Now()),
	}
	if err := repos.pingRepo.Create(ctx, &p); err != nil {
		t.Fatalf("Create ping: %v", err)
	}

	if err := repos.checkRepo.Delete(ctx, "casc-check"); err != nil {
		t.Fatalf("Delete check: %v", err)
	}

	// Pings should have been cascade-deleted.
	pings, err := repos.pingRepo.ListByCheckID(ctx, "casc-check", 100)
	if err != nil {
		t.Fatalf("ListByCheckID after cascade: %v", err)
	}
	if len(pings) != 0 {
		t.Errorf("expected 0 pings after cascade delete, got %d", len(pings))
	}
}

func TestCheckRepository_Delete_CascadesToCheckChannels(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	c := makeCheck("casc-check-2", "Cascade check 2")
	if err := repos.checkRepo.Create(ctx, &c); err != nil {
		t.Fatalf("Create check: %v", err)
	}

	ch := makeChannel(t, "email", "Ops email")
	if err := repos.channelRepo.Create(ctx, &ch); err != nil {
		t.Fatalf("Create channel: %v", err)
	}

	if err := repos.channelRepo.AttachToCheck(ctx, "casc-check-2", ch.ID); err != nil {
		t.Fatalf("AttachToCheck: %v", err)
	}

	if err := repos.checkRepo.Delete(ctx, "casc-check-2"); err != nil {
		t.Fatalf("Delete check: %v", err)
	}

	// check_channels row should be gone.
	linked, err := repos.channelRepo.ListByCheckID(ctx, "casc-check-2")
	if err != nil {
		t.Fatalf("ListByCheckID: %v", err)
	}
	if len(linked) != 0 {
		t.Errorf("expected 0 check_channels after cascade delete, got %d", len(linked))
	}
}
