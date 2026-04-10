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

// cloneCheck returns a deep copy of c, allocating new values for all pointer
// fields (Slug, LastPingAt, NextExpectedAt) so that neither the caller nor the
// internal cache can mutate the other's data through shared pointers.
func cloneCheck(c *model.Check) *model.Check {
	cp := *c
	if c.Slug != nil {
		s := *c.Slug
		cp.Slug = &s
	}
	if c.LastPingAt != nil {
		t := *c.LastPingAt
		cp.LastPingAt = &t
	}
	if c.NextExpectedAt != nil {
		t := *c.NextExpectedAt
		cp.NextExpectedAt = &t
	}
	return &cp
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
		return fmt.Errorf("stateCache.Hydrate: %w", err)
	}

	sc.mu.Lock()
	defer sc.mu.Unlock()

	sc.checks = make(map[string]*model.Check, len(checks))
	for _, c := range checks {
		sc.checks[c.ID] = cloneCheck(c)
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
	return cloneCheck(c)
}

// setLocked persists c to the database and updates the cache map.
// The caller MUST hold sc.mu.Lock(). Errors are returned unwrapped so the
// caller can annotate with the appropriate context.
func (sc *StateCache) setLocked(ctx context.Context, c *model.Check) error {
	if err := sc.repo.Update(ctx, c); err != nil {
		return err
	}
	sc.checks[c.ID] = cloneCheck(c)
	return nil
}

// Create inserts a new check into the database and adds it to the in-memory
// cache. The mutex is held across both the DB insert and the map addition so
// that readers never observe a partial state. If the database insert fails the
// cache is not modified and the error is returned to the caller.
func (sc *StateCache) Create(ctx context.Context, c *model.Check) error {
	cp := cloneCheck(c)

	sc.mu.Lock()
	defer sc.mu.Unlock()

	if err := sc.repo.Create(ctx, cp); err != nil {
		return fmt.Errorf("stateCache.Create: %w", err)
	}
	sc.checks[cp.ID] = cloneCheck(cp)
	return nil
}

// Set persists the check to the database and then updates the in-memory cache.
// The mutex is held across both the DB write and the map update so that readers
// never observe a state where the DB has been updated but the cache has not.
// If the database write fails the cache is not modified and the error is
// returned to the caller.
func (sc *StateCache) Set(ctx context.Context, c *model.Check) error {
	cp := cloneCheck(c)

	sc.mu.Lock()
	defer sc.mu.Unlock()

	if err := sc.setLocked(ctx, cp); err != nil {
		return fmt.Errorf("stateCache.Set: %w", err)
	}
	return nil
}

// Delete removes the check from both the database and the in-memory cache.
// The mutex is held across both the DB delete and the map removal so that
// readers never observe a state where the record is gone from the DB but still
// visible in the cache. If the database delete fails the cache is not modified
// and the error is returned to the caller.
func (sc *StateCache) Delete(ctx context.Context, uuid string) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	if err := sc.repo.Delete(ctx, uuid); err != nil {
		return fmt.Errorf("stateCache.Delete: %w", err)
	}

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
		result = append(result, cloneCheck(c))
	}
	return result
}

// WithWriteLock holds the exclusive write lock for the entire duration of fn.
//
// fn receives:
//   - checks: deep copies of all checks for read-only inspection.
//   - update: a closure that writes an updated check to both the database and
//     the cache while the same lock is held. If the database write fails,
//     update returns an error and the cache entry is left unchanged.
//
// This method is intended exclusively for the scheduler's evaluateAll() pass
// to eliminate TOCTOU races during state transitions.
//
// WARNING: fn must not call any StateCache method (Get, Set, Delete, Snapshot,
// WithWriteLock). The write lock is already held; re-entrant calls will
// deadlock.
func (sc *StateCache) WithWriteLock(ctx context.Context, fn func(checks []*model.Check, update func(*model.Check) error)) {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	// Build deep copies for the callback so it cannot mutate internal state
	// without going through the update closure.
	copies := make([]*model.Check, 0, len(sc.checks))
	for _, c := range sc.checks {
		copies = append(copies, cloneCheck(c))
	}

	update := func(c *model.Check) error {
		if err := sc.setLocked(ctx, c); err != nil {
			return fmt.Errorf("stateCache.WithWriteLock update: %w", err)
		}
		return nil
	}

	fn(copies, update)
}
