// Package cache provides an in-memory, RWMutex-protected cache of Check
// records that writes through to a CheckRepository.
//
// The cache eliminates database round-trips on the hot ping path and inside
// the scheduler's evaluation loop. SQLite remains the authoritative source of
// truth; the cache is re-populated from it via Hydrate on every process start.
package cache

import (
	"context"
	"fmt"
	"sync"

	"github.com/myrrolinz/cronmon/internal/model"
	"github.com/myrrolinz/cronmon/internal/repository"
)

// StateCache is a concurrency-safe, write-through in-memory cache of all
// Check records. Callers always receive value copies; internal map entries are
// never exposed as pointers to prevent external mutation.
type StateCache struct {
	mu     sync.RWMutex
	checks map[string]*model.Check // keyed by UUID (check.ID)
	repo   repository.CheckRepository
}

// New creates a StateCache backed by the given CheckRepository.
// Call Hydrate before first use.
func New(repo repository.CheckRepository) *StateCache {
	return &StateCache{
		checks: make(map[string]*model.Check),
		repo:   repo,
	}
}

// Hydrate loads all checks from the database into the in-memory map.
// It replaces any previous cache state. Call once at startup before the HTTP
// server and scheduler begin.
func (sc *StateCache) Hydrate(ctx context.Context) error {
	checks, err := sc.repo.ListAll(ctx)
	if err != nil {
		return fmt.Errorf("StateCache.Hydrate: %w", err)
	}

	sc.mu.Lock()
	defer sc.mu.Unlock()

	sc.checks = make(map[string]*model.Check, len(checks))
	for _, c := range checks {
		cp := *c
		sc.checks[c.ID] = &cp
	}
	return nil
}

// Get returns a value copy of the check with the given UUID, or nil if not
// found. The returned value is safe for the caller to mutate.
func (sc *StateCache) Get(uuid string) *model.Check {
	sc.mu.RLock()
	defer sc.mu.RUnlock()

	c, ok := sc.checks[uuid]
	if !ok {
		return nil
	}
	cp := *c
	return &cp
}

// Set persists the check to the database and then updates the in-memory cache.
// If the database write fails the cache is not modified and the error is
// returned to the caller.
func (sc *StateCache) Set(ctx context.Context, c *model.Check) error {
	cp := *c

	if err := sc.repo.Update(ctx, &cp); err != nil {
		return fmt.Errorf("StateCache.Set: %w", err)
	}

	sc.mu.Lock()
	defer sc.mu.Unlock()

	stored := cp
	sc.checks[cp.ID] = &stored
	return nil
}

// Delete removes the check from both the database and the in-memory cache.
// If the database delete fails the cache is not modified and the error is
// returned to the caller.
func (sc *StateCache) Delete(ctx context.Context, uuid string) error {
	if err := sc.repo.Delete(ctx, uuid); err != nil {
		return fmt.Errorf("StateCache.Delete: %w", err)
	}

	sc.mu.Lock()
	defer sc.mu.Unlock()

	delete(sc.checks, uuid)
	return nil
}

// Snapshot returns value-copies of every check currently in the cache as a
// slice. The returned slice and its elements are safe to read without holding
// any lock. An empty cache returns a non-nil empty slice.
func (sc *StateCache) Snapshot() []*model.Check {
	sc.mu.RLock()
	defer sc.mu.RUnlock()

	result := make([]*model.Check, 0, len(sc.checks))
	for _, c := range sc.checks {
		cp := *c
		result = append(result, &cp)
	}
	return result
}

// WithWriteLock holds the exclusive write lock for the entire duration of fn.
//
// fn receives:
//   - checks: value-copies of all checks for read-only inspection.
//   - update: a closure that writes an updated check to both the database and
//     the cache while the same lock is held. If the database write fails,
//     update returns an error and the cache entry is left unchanged.
//
// This method is intended exclusively for the scheduler's evaluateAll() pass
// to eliminate TOCTOU races during state transitions.
func (sc *StateCache) WithWriteLock(ctx context.Context, fn func(checks []*model.Check, update func(*model.Check) error)) {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	// Build read-only copies for the callback.
	copies := make([]*model.Check, 0, len(sc.checks))
	for _, c := range sc.checks {
		cp := *c
		copies = append(copies, &cp)
	}

	update := func(c *model.Check) error {
		if err := sc.repo.Update(ctx, c); err != nil {
			return fmt.Errorf("StateCache.WithWriteLock update: %w", err)
		}
		stored := *c
		sc.checks[c.ID] = &stored
		return nil
	}

	fn(copies, update)
}
