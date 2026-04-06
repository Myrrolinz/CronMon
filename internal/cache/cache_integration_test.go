package cache_test

// Integration tests for StateCache with a real SQLite-backed CheckRepository.
//
// These exist to validate assumptions that the mock-backed unit tests cannot
// confirm:
//
//  1. Hydrate correctly round-trips all Check fields from SQLite (column
//     mapping, time parsing, pointer fields).
//  2. Set write-through actually persists the row — confirmed by querying
//     the DB directly via a second repository instance that bypasses the cache.
//  3. WithWriteLock update closure is atomic: the status change is visible in
//     SQLite immediately after the closure returns, not just in the in-memory
//     map.
//  4. Delete removes the record from both the map and the DB.

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/myrrolinz/cronmon/internal/cache"
	"github.com/myrrolinz/cronmon/internal/db"
	"github.com/myrrolinz/cronmon/internal/model"
	"github.com/myrrolinz/cronmon/internal/repository"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() }) //nolint:errcheck
	return database
}

// newCacheAndRepo opens a real SQLite DB, inserts c via the repo, then
// returns a hydrated StateCache and the repository backed by the same DB.
// Tests can use the returned repository to verify persisted writes directly,
// because repository reads go to SQLite rather than through the cache.
func newCacheAndRepo(t *testing.T, checks ...*model.Check) (*cache.StateCache, repository.CheckRepository) {
	t.Helper()
	sqlDB := openDB(t)
	repo := repository.NewCheckRepository(sqlDB)
	ctx := context.Background()
	for _, c := range checks {
		cp := *c
		if err := repo.Create(ctx, &cp); err != nil {
			t.Fatalf("seed check %q: %v", c.ID, err)
		}
	}
	sc := cache.New(repo)
	if err := sc.Hydrate(ctx); err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	return sc, repo
}

func makeTestCheck(id string) *model.Check {
	now := time.Now().UTC().Truncate(time.Second)
	next := now.Add(10 * time.Minute)
	return &model.Check{
		ID:             id,
		Name:           "Test check " + id,
		Schedule:       "0 2 * * *",
		Grace:          10,
		Status:         model.StatusNew,
		NextExpectedAt: &next,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestCacheIntegration_HydrateRoundTrips verifies that Hydrate reads back all
// persisted Check fields correctly — including pointer fields like
// NextExpectedAt.
func TestCacheIntegration_HydrateRoundTrips(t *testing.T) {
	c := makeTestCheck("hydrate-1")
	sc, _ := newCacheAndRepo(t, c)

	got := sc.Get("hydrate-1")
	if got == nil {
		t.Fatal("Get returned nil after Hydrate")
	}
	if got.ID != c.ID {
		t.Errorf("ID: got %q, want %q", got.ID, c.ID)
	}
	if got.Name != c.Name {
		t.Errorf("Name: got %q, want %q", got.Name, c.Name)
	}
	if got.Status != c.Status {
		t.Errorf("Status: got %q, want %q", got.Status, c.Status)
	}
	if got.Schedule != c.Schedule {
		t.Errorf("Schedule: got %q, want %q", got.Schedule, c.Schedule)
	}
	if got.Grace != c.Grace {
		t.Errorf("Grace: got %d, want %d", got.Grace, c.Grace)
	}
	if got.NextExpectedAt == nil {
		t.Fatal("NextExpectedAt: got nil, want non-nil")
	}
	if !got.NextExpectedAt.Equal(*c.NextExpectedAt) {
		t.Errorf("NextExpectedAt: got %v, want %v", got.NextExpectedAt, c.NextExpectedAt)
	}
}

// TestCacheIntegration_SetWritesThrough verifies that Set persists the updated
// Check to SQLite. The verification reads back via the repo (bypassing the
// cache map) to confirm the SQL UPDATE fired with the correct values.
func TestCacheIntegration_SetWritesThrough(t *testing.T) {
	c := makeTestCheck("set-1")
	sc, repo := newCacheAndRepo(t, c)

	updated := sc.Get("set-1")
	updated.Status = model.StatusUp
	updated.Name = "Updated name"
	if err := sc.Set(context.Background(), updated); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Read back directly from the DB, bypassing the cache.
	fromDB, err := repo.GetByID(context.Background(), "set-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if fromDB.Status != model.StatusUp {
		t.Errorf("DB status: got %q, want %q", fromDB.Status, model.StatusUp)
	}
	if fromDB.Name != "Updated name" {
		t.Errorf("DB name: got %q, want %q", fromDB.Name, "Updated name")
	}
}

// TestCacheIntegration_WithWriteLockPersistsAtomically verifies the key
// scheduler invariant: after WithWriteLock's update closure returns, the new
// status is visible in SQLite — not just in the in-memory map.
func TestCacheIntegration_WithWriteLockPersistsAtomically(t *testing.T) {
	overdue := time.Now().UTC().Add(-1 * time.Minute)
	c := makeTestCheck("wl-1")
	c.Status = model.StatusUp
	c.NextExpectedAt = &overdue

	sc, repo := newCacheAndRepo(t, c)

	sc.WithWriteLock(context.Background(), func(checks []*model.Check, update func(*model.Check) error) {
		for _, ch := range checks {
			if ch.ID == "wl-1" {
				ch.Status = model.StatusDown
				if err := update(ch); err != nil {
					t.Errorf("update: %v", err)
				}
			}
		}
	})

	// Cache should reflect the change.
	cached := sc.Get("wl-1")
	if cached == nil {
		t.Fatal("Get returned nil after WithWriteLock")
	}
	if cached.Status != model.StatusDown {
		t.Errorf("cache status: got %q, want %q", cached.Status, model.StatusDown)
	}

	// SQLite must also reflect it.
	fromDB, err := repo.GetByID(context.Background(), "wl-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if fromDB.Status != model.StatusDown {
		t.Errorf("DB status: got %q, want %q", fromDB.Status, model.StatusDown)
	}
}

// TestCacheIntegration_DeleteRemovesFromMapAndDB verifies that Delete removes
// the check from both the in-memory map and the SQLite table.
func TestCacheIntegration_DeleteRemovesFromMapAndDB(t *testing.T) {
	c := makeTestCheck("del-1")
	sc, repo := newCacheAndRepo(t, c)

	if err := sc.Delete(context.Background(), "del-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Cache must no longer return the check.
	if sc.Get("del-1") != nil {
		t.Error("Get returned non-nil after Delete; cache not updated")
	}

	// SQLite must also have the row removed.
	_, err := repo.GetByID(context.Background(), "del-1")
	if err == nil {
		t.Error("GetByID returned nil error after Delete; row still in DB")
	}
}

// TestCacheIntegration_HydrateClearsStaleEntries verifies that calling Hydrate
// a second time reflects the current DB state and discards entries that are no
// longer in the DB.
func TestCacheIntegration_HydrateClearsStaleEntries(t *testing.T) {
	c1 := makeTestCheck("stale-1")
	c2 := makeTestCheck("stale-2")
	sc, repo := newCacheAndRepo(t, c1, c2)

	// Delete c2 directly from DB (simulating an external change).
	if err := repo.Delete(context.Background(), "stale-2"); err != nil {
		t.Fatalf("repo.Delete: %v", err)
	}

	// Re-hydrate: cache should now only see c1.
	if err := sc.Hydrate(context.Background()); err != nil {
		t.Fatalf("Hydrate: %v", err)
	}

	if sc.Get("stale-1") == nil {
		t.Error("stale-1 should still be in cache after re-Hydrate")
	}
	if sc.Get("stale-2") != nil {
		t.Error("stale-2 should not be in cache after re-Hydrate (was deleted from DB)")
	}
}
