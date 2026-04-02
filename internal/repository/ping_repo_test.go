package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/myrrolinz/cronmon/internal/model"
)

func TestPingRepository_Create(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	c := makeCheck("ping-check", "Ping test check")
	if err := repos.checkRepo.Create(ctx, &c); err != nil {
		t.Fatalf("Create check: %v", err)
	}

	p := model.Ping{
		CheckID:   "ping-check",
		Type:      model.PingSuccess,
		CreatedAt: truncSec(time.Now()),
		SourceIP:  "10.0.0.1",
	}
	if err := repos.pingRepo.Create(ctx, &p); err != nil {
		t.Fatalf("Create ping: %v", err)
	}
	if p.ID == 0 {
		t.Error("expected LastInsertId to be set (non-zero), got 0")
	}
}

func TestPingRepository_Create_AllTypes(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	c := makeCheck("type-check", "Type test check")
	if err := repos.checkRepo.Create(ctx, &c); err != nil {
		t.Fatalf("Create check: %v", err)
	}

	now := truncSec(time.Now())
	for _, pt := range []model.PingType{model.PingSuccess, model.PingStart, model.PingFail} {
		p := model.Ping{
			CheckID:   "type-check",
			Type:      pt,
			CreatedAt: now,
		}
		if err := repos.pingRepo.Create(ctx, &p); err != nil {
			t.Errorf("Create ping type %q: %v", pt, err)
		}
	}
}

func TestPingRepository_Create_FKViolation(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	p := model.Ping{
		CheckID:   "nonexistent-check",
		Type:      model.PingSuccess,
		CreatedAt: truncSec(time.Now()),
	}
	if err := repos.pingRepo.Create(ctx, &p); err == nil {
		t.Error("expected FK violation error for unknown check_id, got nil")
	}
}

func TestPingRepository_ListByCheckID(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	c := makeCheck("list-check", "List test check")
	if err := repos.checkRepo.Create(ctx, &c); err != nil {
		t.Fatalf("Create check: %v", err)
	}

	base := truncSec(time.Now())
	for i := 0; i < 5; i++ {
		p := model.Ping{
			CheckID:   "list-check",
			Type:      model.PingSuccess,
			CreatedAt: base.Add(time.Duration(i) * time.Second),
		}
		if err := repos.pingRepo.Create(ctx, &p); err != nil {
			t.Fatalf("Create ping %d: %v", i, err)
		}
	}

	// Limit 3 should return the 3 most recent (DESC order).
	pings, err := repos.pingRepo.ListByCheckID(ctx, "list-check", 3)
	if err != nil {
		t.Fatalf("ListByCheckID: %v", err)
	}
	if len(pings) != 3 {
		t.Fatalf("len = %d, want 3", len(pings))
	}
	// Verify descending order: first result should have the latest CreatedAt.
	if !pings[0].CreatedAt.After(pings[1].CreatedAt) {
		t.Errorf("pings not in DESC order: [0]=%v [1]=%v", pings[0].CreatedAt, pings[1].CreatedAt)
	}
}

func TestPingRepository_ListByCheckID_Empty(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	c := makeCheck("empty-check", "Empty check")
	if err := repos.checkRepo.Create(ctx, &c); err != nil {
		t.Fatalf("Create check: %v", err)
	}

	pings, err := repos.pingRepo.ListByCheckID(ctx, "empty-check", 100)
	if err != nil {
		t.Fatalf("ListByCheckID: %v", err)
	}
	if len(pings) != 0 {
		t.Errorf("len = %d, want 0", len(pings))
	}
}

func TestPingRepository_ListByCheckID_PreservesFields(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	c := makeCheck("field-check", "Field check")
	if err := repos.checkRepo.Create(ctx, &c); err != nil {
		t.Fatalf("Create check: %v", err)
	}

	now := truncSec(time.Now())
	p := model.Ping{
		CheckID:   "field-check",
		Type:      model.PingFail,
		CreatedAt: now,
		SourceIP:  "192.168.1.100",
	}
	if err := repos.pingRepo.Create(ctx, &p); err != nil {
		t.Fatalf("Create ping: %v", err)
	}

	pings, err := repos.pingRepo.ListByCheckID(ctx, "field-check", 10)
	if err != nil {
		t.Fatalf("ListByCheckID: %v", err)
	}
	if len(pings) != 1 {
		t.Fatalf("expected 1 ping, got %d", len(pings))
	}
	got := pings[0]
	if got.CheckID != "field-check" {
		t.Errorf("CheckID = %q", got.CheckID)
	}
	if got.Type != model.PingFail {
		t.Errorf("Type = %q, want %q", got.Type, model.PingFail)
	}
	if !got.CreatedAt.Equal(now) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, now)
	}
	if got.SourceIP != "192.168.1.100" {
		t.Errorf("SourceIP = %q", got.SourceIP)
	}
}

func TestPingRepository_DeleteOldest(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	c := makeCheck("del-check", "Delete oldest check")
	if err := repos.checkRepo.Create(ctx, &c); err != nil {
		t.Fatalf("Create check: %v", err)
	}

	base := truncSec(time.Now())
	for i := 0; i < 10; i++ {
		p := model.Ping{
			CheckID:   "del-check",
			Type:      model.PingSuccess,
			CreatedAt: base.Add(time.Duration(i) * time.Second),
		}
		if err := repos.pingRepo.Create(ctx, &p); err != nil {
			t.Fatalf("Create ping %d: %v", i, err)
		}
	}

	// Keep only the 5 most recent.
	if err := repos.pingRepo.DeleteOldest(ctx, "del-check", 5); err != nil {
		t.Fatalf("DeleteOldest: %v", err)
	}

	pings, err := repos.pingRepo.ListByCheckID(ctx, "del-check", 100)
	if err != nil {
		t.Fatalf("ListByCheckID after DeleteOldest: %v", err)
	}
	if len(pings) != 5 {
		t.Errorf("len = %d, want 5", len(pings))
	}
	// The remaining 5 should be the most recent (highest CreatedAt).
	for _, p := range pings {
		if p.CreatedAt.Before(base.Add(5 * time.Second)) {
			t.Errorf("ping with CreatedAt %v should have been deleted", p.CreatedAt)
		}
	}
}

func TestPingRepository_DeleteOldest_NoOp_WhenBelowLimit(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	c := makeCheck("noop-check", "No-op check")
	if err := repos.checkRepo.Create(ctx, &c); err != nil {
		t.Fatalf("Create check: %v", err)
	}

	for i := 0; i < 3; i++ {
		p := model.Ping{
			CheckID:   "noop-check",
			Type:      model.PingSuccess,
			CreatedAt: truncSec(time.Now()).Add(time.Duration(i) * time.Second),
		}
		if err := repos.pingRepo.Create(ctx, &p); err != nil {
			t.Fatalf("Create ping: %v", err)
		}
	}

	// keepN=10 > actual count=3; should be a no-op.
	if err := repos.pingRepo.DeleteOldest(ctx, "noop-check", 10); err != nil {
		t.Fatalf("DeleteOldest: %v", err)
	}

	pings, err := repos.pingRepo.ListByCheckID(ctx, "noop-check", 100)
	if err != nil {
		t.Fatalf("ListByCheckID: %v", err)
	}
	if len(pings) != 3 {
		t.Errorf("len = %d, want 3 (no pings should have been deleted)", len(pings))
	}
}
