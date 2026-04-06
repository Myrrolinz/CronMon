package scheduler

// Integration tests for Scheduler with real SQLite-backed repositories and
// StateCache.
//
// These exist to validate assumptions that the all-mock unit tests cannot
// confirm:
//
//  1. evaluateAll persists up→down to the real SQLite checks table — not
//     just to the in-memory cache.
//
//  2. Startup reconciliation: a check that is already overdue at the moment
//     Start() is called (i.e. it went down while the process was offline) is
//     transitioned and an AlertEvent is enqueued before the first regular tick.

import (
	"context"
	"database/sql"
	"encoding/json"
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

type testRepos struct {
	checkRepo   repository.CheckRepository
	channelRepo repository.ChannelRepository
	pingRepo    repository.PingRepository
}

func newRepos(sqlDB *sql.DB) testRepos {
	return testRepos{
		checkRepo:   repository.NewCheckRepository(sqlDB),
		channelRepo: repository.NewChannelRepository(sqlDB),
		pingRepo:    repository.NewPingRepository(sqlDB),
	}
}

// seedCheck inserts a Check and returns it.
func seedCheck(t *testing.T, repo repository.CheckRepository, c *model.Check) {
	t.Helper()
	if err := repo.Create(context.Background(), c); err != nil {
		t.Fatalf("seed check %q: %v", c.ID, err)
	}
}

// seedChannel inserts an email Channel attached to checkID, returns the channel ID.
func seedChannel(t *testing.T, repos testRepos, checkID string) int64 {
	t.Helper()
	cfg, _ := json.Marshal(map[string]string{"address": "ops@example.com"})
	ch := model.Channel{
		Type:      "email",
		Name:      "Ops email",
		Config:    cfg,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := repos.channelRepo.Create(context.Background(), &ch); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	if err := repos.channelRepo.AttachToCheck(context.Background(), checkID, ch.ID); err != nil {
		t.Fatalf("attach channel: %v", err)
	}
	return ch.ID
}

func newOverdueCheck(id string) *model.Check {
	now := time.Now().UTC().Truncate(time.Second)
	overdue := now.Add(-5 * time.Minute)
	return &model.Check{
		ID:             id,
		Name:           "Overdue check " + id,
		Schedule:       "* * * * *",
		Grace:          1,
		Status:         model.StatusUp,
		NextExpectedAt: &overdue,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

// buildScheduler sets up a real StateCache, seeds repos, then builds a
// Scheduler with a very long regular interval so only the explicit
// evaluateAll calls (or Start's reconciliation pass) run during the test.
// Returns the scheduler, the alert channel, and the check repo for DB assertions.
func buildScheduler(
	t *testing.T,
	repos testRepos,
	checks ...*model.Check,
) (*Scheduler, chan model.AlertEvent, repository.CheckRepository) {
	t.Helper()
	ctx := context.Background()

	for _, c := range checks {
		seedCheck(t, repos.checkRepo, c)
	}

	sc := cache.New(repos.checkRepo)
	if err := sc.Hydrate(ctx); err != nil {
		t.Fatalf("Hydrate: %v", err)
	}

	alertCh := make(chan model.AlertEvent, 32)
	sched := New(sc, repos.channelRepo, repos.pingRepo, alertCh, 24*time.Hour)
	return sched, alertCh, repos.checkRepo
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestSchedulerIntegration_EvaluateAllPersistsDownToSQLite verifies that an
// up→down transition actually updates the checks table in SQLite, not just
// the in-memory cache.  This is the key gap the all-mock unit tests cannot
// cover.
func TestSchedulerIntegration_EvaluateAllPersistsDownToSQLite(t *testing.T) {
	sqlDB := openDB(t)
	repos := newRepos(sqlDB)
	check := newOverdueCheck("eval-persist-1")

	sched, alertCh, checkRepo := buildScheduler(t, repos, check)
	seedChannel(t, repos, check.ID)

	// Manually trigger a single evaluation pass (don't Start the goroutine).
	sched.evaluateAll(context.Background(), time.Now())

	// 1. An AlertEvent must have been enqueued.
	if len(alertCh) != 1 {
		t.Fatalf("alertCh len = %d, want 1", len(alertCh))
	}
	event := <-alertCh
	if event.AlertType != model.AlertDown {
		t.Errorf("AlertType = %q, want %q", event.AlertType, model.AlertDown)
	}

	// 2. The real SQLite row must show status = "down".
	fromDB, err := checkRepo.GetByID(context.Background(), check.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if fromDB.Status != model.StatusDown {
		t.Errorf("DB status = %q, want %q — evaluateAll did not write through to SQLite", fromDB.Status, model.StatusDown)
	}
}

// TestSchedulerIntegration_StartReconciles verifies that when Start() is
// called with an already-overdue check in the database (simulating a process
// restart), the startup reconciliation pass transitions the check before the
// first regular tick fires.
func TestSchedulerIntegration_StartReconciles(t *testing.T) {
	sqlDB := openDB(t)
	repos := newRepos(sqlDB)
	check := newOverdueCheck("reconcile-1")

	sched, alertCh, checkRepo := buildScheduler(t, repos, check)
	seedChannel(t, repos, check.ID)

	sched.Start()
	t.Cleanup(sched.Stop)

	// The reconciliation pass runs synchronously before the first ticker, so
	// by the time Start returns the goroutine has already processed it.
	// Give it a short window to allow the goroutine to run.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(alertCh) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if len(alertCh) == 0 {
		t.Fatal("no AlertEvent enqueued after Start() reconciliation pass")
	}

	// SQLite must also reflect the transition.
	fromDB, err := checkRepo.GetByID(context.Background(), check.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if fromDB.Status != model.StatusDown {
		t.Errorf("DB status = %q, want %q after reconciliation", fromDB.Status, model.StatusDown)
	}
}

// TestSchedulerIntegration_AlreadyDownNotTransitionedAgain verifies that a
// check already in the "down" state does not produce a duplicate AlertEvent
// on the next evaluation pass.
func TestSchedulerIntegration_AlreadyDownNotTransitionedAgain(t *testing.T) {
	sqlDB := openDB(t)
	repos := newRepos(sqlDB)

	overdue := time.Now().UTC().Add(-5 * time.Minute)
	check := &model.Check{
		ID:             "already-down-1",
		Name:           "Already down check",
		Schedule:       "* * * * *",
		Grace:          1,
		Status:         model.StatusDown, // already down at seeding time
		NextExpectedAt: &overdue,
		CreatedAt:      time.Now().UTC().Truncate(time.Second),
		UpdatedAt:      time.Now().UTC().Truncate(time.Second),
	}

	sched, alertCh, _ := buildScheduler(t, repos, check)
	seedChannel(t, repos, check.ID)

	sched.evaluateAll(context.Background(), time.Now())
	sched.evaluateAll(context.Background(), time.Now())

	if len(alertCh) != 0 {
		t.Errorf("alertCh len = %d, want 0 — already-down check must not re-alert", len(alertCh))
	}
}
