// Package scheduler implements the background evaluation loop that detects
// missed pings and enqueues alert events for the NotifierWorker to dispatch.
//
// The Scheduler runs two tickers inside a single goroutine:
//   - eval ticker (configurable interval, 30 s in production): calls evaluateAll
//     to transition overdue "up" checks to "down" and enqueue AlertEvents.
//   - cleanup ticker (1 h): calls cleanupOldPings to prune the pings table.
//
// A startup reconciliation pass of evaluateAll is executed before the first
// regular tick so that checks which went down while the process was offline
// are detected without waiting a full interval.
package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/myrrolinz/cronmon/internal/cache"
	"github.com/myrrolinz/cronmon/internal/model"
	"github.com/myrrolinz/cronmon/internal/repository"
)

// defaultCleanupInterval is the period between old-ping cleanup passes.
// It is an unexported variable so the test file (same package) can override
// it to avoid real 1-hour waits.
var defaultCleanupInterval = time.Hour

// Scheduler periodically evaluates all checks and enqueues alert events when
// a "up" check misses its ping deadline.  It also prunes old ping rows on a
// slower hourly tick.
type Scheduler struct {
	cache           *cache.StateCache
	channelRepo     repository.ChannelRepository
	pingRepo        repository.PingRepository
	alertCh         chan<- model.AlertEvent
	interval        time.Duration
	cleanupInterval time.Duration
	stopCh          chan struct{}
	wg              sync.WaitGroup
}

// New creates a Scheduler backed by the given cache and repositories.
// interval is the period between evaluateAll passes; use 30 s in production.
// Call Start to begin the background goroutine.
func New(
	sc *cache.StateCache,
	channelRepo repository.ChannelRepository,
	pingRepo repository.PingRepository,
	alertCh chan<- model.AlertEvent,
	interval time.Duration,
) *Scheduler {
	return &Scheduler{
		cache:           sc,
		channelRepo:     channelRepo,
		pingRepo:        pingRepo,
		alertCh:         alertCh,
		interval:        interval,
		cleanupInterval: defaultCleanupInterval,
		stopCh:          make(chan struct{}),
	}
}

// Start runs the scheduler in a background goroutine.  It immediately
// performs a startup reconciliation pass before waiting for the first
// regular tick, so any check that went down while the process was offline is
// detected without delay.  Stop must be called to release the goroutine.
func (s *Scheduler) Start() {
	s.wg.Add(1)
	go s.run()
}

// Stop signals the background goroutine to exit and blocks until it has
// finished cleanly.
func (s *Scheduler) Stop() {
	close(s.stopCh)
	s.wg.Wait()
}

// run is the scheduler's main goroutine body.
func (s *Scheduler) run() {
	defer s.wg.Done()

	ctx := context.Background()

	// Startup reconciliation: catch checks that went down while the process
	// was offline, before the first regular tick fires.
	s.evaluateAll(ctx, time.Now())

	evalTicker := time.NewTicker(s.interval)
	defer evalTicker.Stop()

	cleanupTicker := time.NewTicker(s.cleanupInterval)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case now := <-evalTicker.C:
			s.evaluateAll(ctx, now)
		case <-cleanupTicker.C:
			s.cleanupOldPings(ctx)
		}
	}
}

// evaluateAll inspects every check and transitions "up" checks to "down" when
// their deadline has passed.  One AlertEvent is enqueued per channel
// subscribed to the affected check.
//
// The entire pass runs under the cache's exclusive write lock (via
// WithWriteLock) to eliminate TOCTOU races with concurrent ping handlers.
// The update closure passed by WithWriteLock persists each state change to
// both the database and the cache atomically under that same lock.
func (s *Scheduler) evaluateAll(ctx context.Context, now time.Time) {
	s.cache.WithWriteLock(ctx, func(checks []*model.Check, update func(*model.Check) error) {
		for _, c := range checks {
			// Only "up" checks can transition to "down".
			// "paused", "new", and already-"down" checks are all skipped.
			if c.Status != model.StatusUp {
				continue
			}

			// A nil deadline means next_expected_at has not been set yet;
			// the check cannot be overdue.
			if c.NextExpectedAt == nil {
				continue
			}

			if !now.After(*c.NextExpectedAt) {
				continue
			}

			// Deadline missed: persist the down transition atomically.
			c.Status = model.StatusDown
			c.UpdatedAt = now
			if err := update(c); err != nil {
				slog.Error("scheduler: failed to update check to down",
					"check_id", c.ID, "error", err)
				continue
			}

			// Enqueue one AlertEvent per channel subscribed to this check.
			channels, err := s.channelRepo.ListByCheckID(ctx, c.ID)
			if err != nil {
				slog.Error("scheduler: failed to list channels for check",
					"check_id", c.ID, "error", err)
				continue
			}

			for _, ch := range channels {
				event := model.AlertEvent{
					Check:     *c,
					Channel:   *ch,
					AlertType: model.AlertDown,
				}
				// Non-blocking send: if the notifier worker is behind, drop
				// the event and log a warning rather than stalling the
				// scheduler's write lock.
				select {
				case s.alertCh <- event:
				default:
					slog.Warn("scheduler: alert channel full, dropping event",
						"check_id", c.ID, "channel_id", ch.ID)
				}
			}
		}
	})
}

// cleanupOldPings prunes ping rows for every check, keeping only the most
// recent 1,000.  It uses Snapshot (RLock) to obtain the check list so it
// does not contend with the write lock held during evaluateAll.
func (s *Scheduler) cleanupOldPings(ctx context.Context) {
	const keepN = 1000
	checks := s.cache.Snapshot()
	for _, c := range checks {
		if err := s.pingRepo.DeleteOldest(ctx, c.ID, keepN); err != nil {
			slog.Error("scheduler: ping cleanup failed",
				"check_id", c.ID, "error", err)
		}
	}
}
