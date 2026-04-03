package cache_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/myrrolinz/cronmon/internal/cache"
	"github.com/myrrolinz/cronmon/internal/model"
	"github.com/myrrolinz/cronmon/internal/repository"
)

// ---------------------------------------------------------------------------
// Mock CheckRepository
// ---------------------------------------------------------------------------

type mockCheckRepo struct {
	mu sync.Mutex

	checks map[string]*model.Check

	// call counters
	listAllCalled int
	updateCalled  int
	deleteCalled  int

	// injectable errors
	listAllErr error
	updateErr  error
	deleteErr  error
}

func newMockCheckRepo() *mockCheckRepo {
	return &mockCheckRepo{checks: make(map[string]*model.Check)}
}

func (m *mockCheckRepo) seed(c model.Check) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := c
	m.checks[c.ID] = &cp
}

func (m *mockCheckRepo) Create(_ context.Context, c *model.Check) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *c
	m.checks[c.ID] = &cp
	return nil
}

func (m *mockCheckRepo) GetByID(_ context.Context, id string) (*model.Check, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.checks[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *c
	return &cp, nil
}

func (m *mockCheckRepo) GetByUUID(ctx context.Context, uuid string) (*model.Check, error) {
	return m.GetByID(ctx, uuid)
}

func (m *mockCheckRepo) ListAll(_ context.Context) ([]*model.Check, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listAllCalled++
	if m.listAllErr != nil {
		return nil, m.listAllErr
	}
	result := make([]*model.Check, 0, len(m.checks))
	for _, c := range m.checks {
		cp := *c
		result = append(result, &cp)
	}
	return result, nil
}

func (m *mockCheckRepo) Update(_ context.Context, c *model.Check) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateCalled++
	if m.updateErr != nil {
		return m.updateErr
	}
	cp := *c
	m.checks[c.ID] = &cp
	return nil
}

func (m *mockCheckRepo) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleteCalled++
	if m.deleteErr != nil {
		return m.deleteErr
	}
	delete(m.checks, id)
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func makeCheck(id, name string) model.Check {
	now := time.Now().UTC().Truncate(time.Second)
	return model.Check{
		ID:        id,
		Name:      name,
		Schedule:  "0 2 * * *",
		Grace:     10,
		Status:    model.StatusNew,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// ---------------------------------------------------------------------------
// Hydrate
// ---------------------------------------------------------------------------

func TestStateCache_Hydrate(t *testing.T) {
	repo := newMockCheckRepo()
	c1 := makeCheck("uuid-1", "Backup")
	c2 := makeCheck("uuid-2", "Deploy")
	repo.seed(c1)
	repo.seed(c2)

	sc := cache.New(repo)
	if err := sc.Hydrate(context.Background()); err != nil {
		t.Fatalf("Hydrate: %v", err)
	}

	if repo.listAllCalled != 1 {
		t.Errorf("ListAll called %d times, want 1", repo.listAllCalled)
	}

	got1 := sc.Get("uuid-1")
	if got1 == nil {
		t.Fatal("Get(uuid-1) returned nil after Hydrate")
	}
	if got1.Name != "Backup" {
		t.Errorf("Name = %q, want %q", got1.Name, "Backup")
	}

	got2 := sc.Get("uuid-2")
	if got2 == nil {
		t.Fatal("Get(uuid-2) returned nil after Hydrate")
	}
	if got2.Name != "Deploy" {
		t.Errorf("Name = %q, want %q", got2.Name, "Deploy")
	}
}

func TestStateCache_Hydrate_DBError(t *testing.T) {
	repo := newMockCheckRepo()
	repo.listAllErr = errors.New("db offline")

	sc := cache.New(repo)
	err := sc.Hydrate(context.Background())
	if err == nil {
		t.Fatal("expected error from Hydrate when DB fails, got nil")
	}
}

func TestStateCache_Hydrate_ReplacesExistingCache(t *testing.T) {
	repo := newMockCheckRepo()
	c := makeCheck("uuid-1", "Backup")
	repo.seed(c)

	sc := cache.New(repo)
	if err := sc.Hydrate(context.Background()); err != nil {
		t.Fatalf("first Hydrate: %v", err)
	}

	// Remove from repo and add a different check; re-hydrate should use new state.
	repo.mu.Lock()
	delete(repo.checks, "uuid-1")
	c2 := makeCheck("uuid-99", "New job")
	cp2 := c2
	repo.checks["uuid-99"] = &cp2
	repo.mu.Unlock()

	if err := sc.Hydrate(context.Background()); err != nil {
		t.Fatalf("second Hydrate: %v", err)
	}

	if sc.Get("uuid-1") != nil {
		t.Error("uuid-1 should have been removed from cache after re-hydrate")
	}
	if sc.Get("uuid-99") == nil {
		t.Error("uuid-99 should be present after re-hydrate")
	}
}

// makeCheckWithPtrs returns a Check with all pointer fields populated, so that
// shallow-copy regressions (shared *string / *time.Time) will be detected by
// the copy-safety tests.
func makeCheckWithPtrs(id, name string) model.Check {
	c := makeCheck(id, name)
	slug := "slug-" + id
	c.Slug = &slug
	now := time.Now().UTC().Truncate(time.Second)
	later := now.Add(10 * time.Minute)
	c.LastPingAt = &now
	c.NextExpectedAt = &later
	return c
}

// ---------------------------------------------------------------------------
// Get
// ---------------------------------------------------------------------------

func TestStateCache_Get_NotFound(t *testing.T) {
	sc := cache.New(newMockCheckRepo())
	if got := sc.Get("nonexistent"); got != nil {
		t.Errorf("expected nil for unknown UUID, got %+v", got)
	}
}

func TestStateCache_Get_ReturnsCopy(t *testing.T) {
	repo := newMockCheckRepo()
	c := makeCheckWithPtrs("uuid-1", "Backup")
	repo.seed(c)

	sc := cache.New(repo)
	if err := sc.Hydrate(context.Background()); err != nil {
		t.Fatalf("Hydrate: %v", err)
	}

	got := sc.Get("uuid-1")
	if got == nil {
		t.Fatal("Get returned nil")
	}
	if got.Slug == nil || got.LastPingAt == nil || got.NextExpectedAt == nil {
		t.Fatal("Get returned nil pointer field")
	}

	originalSlug := *got.Slug
	originalLastPingAt := *got.LastPingAt
	originalNextExpectedAt := *got.NextExpectedAt

	// Mutate all fields of the returned copy — the cache must not reflect any change.
	got.Name = "mutated"
	*got.Slug = "mutated-slug"
	*got.LastPingAt = got.LastPingAt.Add(time.Hour)
	*got.NextExpectedAt = got.NextExpectedAt.Add(time.Hour)

	got2 := sc.Get("uuid-1")
	if got2 == nil {
		t.Fatal("second Get returned nil")
	}
	if got2.Name == "mutated" {
		t.Error("mutating Name of the returned value affected the internal cache state")
	}
	if got2.Slug == nil || *got2.Slug != originalSlug {
		t.Errorf("Slug = %v, want %q", got2.Slug, originalSlug)
	}
	if got2.LastPingAt == nil || !got2.LastPingAt.Equal(originalLastPingAt) {
		t.Errorf("LastPingAt = %v, want %v", got2.LastPingAt, originalLastPingAt)
	}
	if got2.NextExpectedAt == nil || !got2.NextExpectedAt.Equal(originalNextExpectedAt) {
		t.Errorf("NextExpectedAt = %v, want %v", got2.NextExpectedAt, originalNextExpectedAt)
	}
}

// ---------------------------------------------------------------------------
// Set
// ---------------------------------------------------------------------------

func TestStateCache_Set_WritesThrough(t *testing.T) {
	repo := newMockCheckRepo()
	c := makeCheck("uuid-1", "Backup")
	repo.seed(c)

	sc := cache.New(repo)
	if err := sc.Hydrate(context.Background()); err != nil {
		t.Fatalf("Hydrate: %v", err)
	}

	updated := makeCheck("uuid-1", "Backup Updated")
	updated.Status = model.StatusUp
	if err := sc.Set(context.Background(), &updated); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Cache must reflect the change.
	got := sc.Get("uuid-1")
	if got == nil {
		t.Fatal("Get returned nil after Set")
	}
	if got.Status != model.StatusUp {
		t.Errorf("Status = %q, want %q", got.Status, model.StatusUp)
	}

	// DB (repo) must also reflect the change.
	if repo.updateCalled != 1 {
		t.Errorf("repo.Update called %d times, want 1", repo.updateCalled)
	}
	dbCheck, err := repo.GetByID(context.Background(), "uuid-1")
	if err != nil {
		t.Fatalf("repo.GetByID: %v", err)
	}
	if dbCheck.Status != model.StatusUp {
		t.Errorf("DB Status = %q, want %q", dbCheck.Status, model.StatusUp)
	}
}

func TestStateCache_Set_DBError(t *testing.T) {
	repo := newMockCheckRepo()
	repo.updateErr = errors.New("write failed")

	sc := cache.New(repo)
	c := makeCheck("uuid-1", "Backup")
	err := sc.Set(context.Background(), &c)
	if err == nil {
		t.Fatal("Set should return error when DB write fails")
	}
}

func TestStateCache_Set_ReturnsCopyAfterSet(t *testing.T) {
	repo := newMockCheckRepo()
	c := makeCheck("uuid-1", "Backup")
	repo.seed(c)

	sc := cache.New(repo)
	if err := sc.Hydrate(context.Background()); err != nil {
		t.Fatalf("Hydrate: %v", err)
	}

	toSet := makeCheckWithPtrs("uuid-1", "Original")
	if err := sc.Set(context.Background(), &toSet); err != nil {
		t.Fatalf("Set: %v", err)
	}

	originalSlug := *toSet.Slug

	// Mutate the value we passed to Set (including pointer fields) — cache must not be affected.
	toSet.Name = "mutated-after-set"
	*toSet.Slug = "mutated-slug-after-set"

	got := sc.Get("uuid-1")
	if got.Name == "mutated-after-set" {
		t.Error("mutating Name of the value passed to Set affected the internal cache state")
	}
	if got.Slug == nil || *got.Slug != originalSlug {
		t.Errorf("Slug = %v, want %q", got.Slug, originalSlug)
	}
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestStateCache_Delete_RemovesFromCacheAndDB(t *testing.T) {
	repo := newMockCheckRepo()
	c := makeCheck("uuid-1", "Backup")
	repo.seed(c)

	sc := cache.New(repo)
	if err := sc.Hydrate(context.Background()); err != nil {
		t.Fatalf("Hydrate: %v", err)
	}

	if err := sc.Delete(context.Background(), "uuid-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if sc.Get("uuid-1") != nil {
		t.Error("check still in cache after Delete")
	}
	if repo.deleteCalled != 1 {
		t.Errorf("repo.Delete called %d times, want 1", repo.deleteCalled)
	}
	_, err := repo.GetByID(context.Background(), "uuid-1")
	if !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("expected ErrNotFound from repo after Delete, got %v", err)
	}
}

func TestStateCache_Delete_DBError(t *testing.T) {
	repo := newMockCheckRepo()
	repo.deleteErr = errors.New("delete failed")

	sc := cache.New(repo)
	c := makeCheck("uuid-1", "Backup")
	repo.seed(c)
	if err := sc.Hydrate(context.Background()); err != nil {
		t.Fatalf("Hydrate: %v", err)
	}

	err := sc.Delete(context.Background(), "uuid-1")
	if err == nil {
		t.Fatal("Delete should return error when DB delete fails")
	}

	// Cache must NOT be modified if DB write failed.
	if sc.Get("uuid-1") == nil {
		t.Error("check was removed from cache despite DB error")
	}
}

// ---------------------------------------------------------------------------
// Snapshot
// ---------------------------------------------------------------------------

func TestStateCache_Snapshot_ReturnsAllCopies(t *testing.T) {
	repo := newMockCheckRepo()
	repo.seed(makeCheck("uuid-1", "A"))
	repo.seed(makeCheck("uuid-2", "B"))
	repo.seed(makeCheck("uuid-3", "C"))

	sc := cache.New(repo)
	if err := sc.Hydrate(context.Background()); err != nil {
		t.Fatalf("Hydrate: %v", err)
	}

	snap := sc.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("Snapshot len = %d, want 3", len(snap))
	}
}

func TestStateCache_Snapshot_ReturnsCopies(t *testing.T) {
	repo := newMockCheckRepo()
	repo.seed(makeCheckWithPtrs("uuid-1", "Backup"))

	sc := cache.New(repo)
	if err := sc.Hydrate(context.Background()); err != nil {
		t.Fatalf("Hydrate: %v", err)
	}

	snap := sc.Snapshot()
	if len(snap) == 0 {
		t.Fatal("Snapshot is empty")
	}
	if snap[0].Slug == nil || snap[0].LastPingAt == nil || snap[0].NextExpectedAt == nil {
		t.Fatal("Snapshot element has nil pointer field")
	}

	originalSlug := *snap[0].Slug
	originalLastPingAt := *snap[0].LastPingAt

	// Mutate the snapshot element (scalar and pointer) — the cache must not reflect it.
	snap[0].Name = "mutated"
	*snap[0].Slug = "mutated-slug"
	*snap[0].LastPingAt = snap[0].LastPingAt.Add(time.Hour)

	got := sc.Get("uuid-1")
	if got.Name == "mutated" {
		t.Error("mutating Name of a Snapshot element affected the internal cache state")
	}
	if got.Slug == nil || *got.Slug != originalSlug {
		t.Errorf("Slug = %v, want %q", got.Slug, originalSlug)
	}
	if got.LastPingAt == nil || !got.LastPingAt.Equal(originalLastPingAt) {
		t.Errorf("LastPingAt = %v, want %v", got.LastPingAt, originalLastPingAt)
	}
}

func TestStateCache_Snapshot_Empty(t *testing.T) {
	sc := cache.New(newMockCheckRepo())
	if err := sc.Hydrate(context.Background()); err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	snap := sc.Snapshot()
	if snap == nil {
		t.Error("Snapshot should return empty slice, not nil")
	}
	if len(snap) != 0 {
		t.Errorf("Snapshot len = %d, want 0", len(snap))
	}
}

// ---------------------------------------------------------------------------
// WithWriteLock
// ---------------------------------------------------------------------------

func TestStateCache_WithWriteLock_UpdatesPersistsToDBAndCache(t *testing.T) {
	repo := newMockCheckRepo()
	c := makeCheck("uuid-1", "Backup")
	c.Status = model.StatusUp
	repo.seed(c)

	sc := cache.New(repo)
	if err := sc.Hydrate(context.Background()); err != nil {
		t.Fatalf("Hydrate: %v", err)
	}

	sc.WithWriteLock(context.Background(), func(checks []*model.Check, update func(*model.Check) error) {
		if len(checks) != 1 {
			t.Errorf("callback received %d checks, want 1", len(checks))
			return
		}
		checks[0].Status = model.StatusDown
		if err := update(checks[0]); err != nil {
			t.Errorf("update returned error: %v", err)
		}
	})

	// Verify cache was updated.
	got := sc.Get("uuid-1")
	if got == nil {
		t.Fatal("Get returned nil after WithWriteLock update")
	}
	if got.Status != model.StatusDown {
		t.Errorf("cache Status = %q, want %q", got.Status, model.StatusDown)
	}

	// Verify DB was updated.
	if repo.updateCalled < 1 {
		t.Error("repo.Update was not called during WithWriteLock")
	}
	dbCheck, err := repo.GetByID(context.Background(), "uuid-1")
	if err != nil {
		t.Fatalf("repo.GetByID: %v", err)
	}
	if dbCheck.Status != model.StatusDown {
		t.Errorf("DB Status = %q, want %q", dbCheck.Status, model.StatusDown)
	}
}

func TestStateCache_WithWriteLock_PassesCopiesNotPointers(t *testing.T) {
	repo := newMockCheckRepo()
	c := makeCheck("uuid-1", "Backup")
	repo.seed(c)

	sc := cache.New(repo)
	if err := sc.Hydrate(context.Background()); err != nil {
		t.Fatalf("Hydrate: %v", err)
	}

	sc.WithWriteLock(context.Background(), func(checks []*model.Check, _ func(*model.Check) error) {
		// Mutate the copy WITHOUT calling update — the cache must not reflect this.
		checks[0].Name = "mutated-without-update"
	})

	got := sc.Get("uuid-1")
	if got.Name == "mutated-without-update" {
		t.Error("mutating a check copy inside WithWriteLock modified the cache without calling update")
	}
}

func TestStateCache_WithWriteLock_UpdateError_CacheNotModified(t *testing.T) {
	repo := newMockCheckRepo()
	c := makeCheck("uuid-1", "Backup")
	c.Status = model.StatusUp
	repo.seed(c)

	sc := cache.New(repo)
	if err := sc.Hydrate(context.Background()); err != nil {
		t.Fatalf("Hydrate: %v", err)
	}

	repo.updateErr = errors.New("disk full")

	sc.WithWriteLock(context.Background(), func(checks []*model.Check, update func(*model.Check) error) {
		checks[0].Status = model.StatusDown
		err := update(checks[0])
		if err == nil {
			t.Error("expected update to return error when repo fails")
		}
	})

	// Cache must be unchanged because the DB write failed.
	got := sc.Get("uuid-1")
	if got.Status != model.StatusUp {
		t.Errorf("cache Status = %q, want %q (should be unchanged after failed update)", got.Status, model.StatusUp)
	}
}

// ---------------------------------------------------------------------------
// Concurrent read/write (run with -race)
// ---------------------------------------------------------------------------

func TestStateCache_ConcurrentReadWrite(t *testing.T) {
	repo := newMockCheckRepo()
	for i := range 20 {
		id := "uuid-" + itoa(i)
		repo.seed(makeCheck(id, "job-"+itoa(i)))
	}

	sc := cache.New(repo)
	if err := sc.Hydrate(context.Background()); err != nil {
		t.Fatalf("Hydrate: %v", err)
	}

	var wg sync.WaitGroup
	ctx := context.Background()

	// Concurrent Gets.
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "uuid-" + itoa(i%20)
			_ = sc.Get(id)
		}(i)
	}

	// Concurrent Snapshots.
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = sc.Snapshot()
		}()
	}

	// Concurrent Sets.
	for i := range 10 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "uuid-" + itoa(i%20)
			c := makeCheck(id, "updated-"+itoa(i))
			c.Status = model.StatusUp
			_ = sc.Set(ctx, &c)
		}(i)
	}

	// Concurrent WithWriteLock calls.
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sc.WithWriteLock(ctx, func(checks []*model.Check, update func(*model.Check) error) {
				for _, c := range checks {
					if c.Status == model.StatusNew {
						c.Status = model.StatusUp
						_ = update(c)
					}
				}
			})
		}()
	}

	wg.Wait()
}

// itoa converts an int to a decimal string without importing strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 10)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}
