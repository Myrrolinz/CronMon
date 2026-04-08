package handler_test

// Integration tests for PingHandler with a real StateCache, real SQLite
// database, and real repositories.
//
// These validate assumptions that all-mock unit tests cannot confirm:
//
//  1. new→up transition: after a success ping, checks.status = 'up' in SQLite
//     and a pings row exists.
//
//  2. down→up recovery: an AlertEvent is enqueued on alertCh and the ping row
//     is written to SQLite.
//
//  3. start ping on up check: next_expected_at is extended in SQLite but
//     status remains 'up'.
//
// These catch bugs in cache write-through and ping insertion that HTTP-level
// unit tests (which use a mock repo) cannot detect.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/myrrolinz/cronmon/internal/cache"
	"github.com/myrrolinz/cronmon/internal/db"
	"github.com/myrrolinz/cronmon/internal/handler"
	"github.com/myrrolinz/cronmon/internal/model"
	"github.com/myrrolinz/cronmon/internal/repository"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func openIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() }) //nolint:errcheck
	return database
}

type integrationEnv struct {
	sqlDB       *sql.DB
	checkRepo   repository.CheckRepository
	pingRepo    repository.PingRepository
	channelRepo repository.ChannelRepository
	cache       *cache.StateCache
	alertCh     chan model.AlertEvent
	handler     *handler.PingHandler
}

func newIntegrationEnv(t *testing.T) *integrationEnv {
	t.Helper()
	sqlDB := openIntegrationDB(t)
	checkRepo := repository.NewCheckRepository(sqlDB)
	pingRepo := repository.NewPingRepository(sqlDB)
	channelRepo := repository.NewChannelRepository(sqlDB)

	sc := cache.New(checkRepo)
	if err := sc.Hydrate(context.Background()); err != nil {
		t.Fatalf("Hydrate: %v", err)
	}

	alertCh := make(chan model.AlertEvent, 64)
	h := handler.NewPingHandler(sc, pingRepo, channelRepo, alertCh, false)

	return &integrationEnv{
		sqlDB:       sqlDB,
		checkRepo:   checkRepo,
		pingRepo:    pingRepo,
		channelRepo: channelRepo,
		cache:       sc,
		alertCh:     alertCh,
		handler:     h,
	}
}

// seedCheck inserts c into the DB and re-hydrates the cache so the check is
// available via cache.Get.
func (e *integrationEnv) seedCheck(t *testing.T, c *model.Check) {
	t.Helper()
	if err := e.checkRepo.Create(context.Background(), c); err != nil {
		t.Fatalf("seedCheck %q: %v", c.ID, err)
	}
	if err := e.cache.Hydrate(context.Background()); err != nil {
		t.Fatalf("Hydrate after seed: %v", err)
	}
}

// seedChannel inserts an email channel and attaches it to checkID.
func (e *integrationEnv) seedChannel(t *testing.T, checkID string) *model.Channel {
	t.Helper()
	cfg, _ := json.Marshal(map[string]string{"address": "ops@example.com"})
	ch := &model.Channel{
		Type:      "email",
		Name:      "Ops email",
		Config:    cfg,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := e.channelRepo.Create(context.Background(), ch); err != nil {
		t.Fatalf("seedChannel create: %v", err)
	}
	if err := e.channelRepo.AttachToCheck(context.Background(), checkID, ch.ID); err != nil {
		t.Fatalf("seedChannel attach: %v", err)
	}
	return ch
}

// queryStatus reads the status column directly from SQLite, bypassing the cache.
func (e *integrationEnv) queryStatus(t *testing.T, id string) model.Status {
	t.Helper()
	var s string
	err := e.sqlDB.QueryRowContext(context.Background(),
		"SELECT status FROM checks WHERE id = ?", id).Scan(&s)
	if err != nil {
		t.Fatalf("queryStatus %q: %v", id, err)
	}
	return model.Status(s)
}

// queryNextExpectedAt reads next_expected_at from SQLite directly.
func (e *integrationEnv) queryNextExpectedAt(t *testing.T, id string) *time.Time {
	t.Helper()
	var ns sql.NullString
	err := e.sqlDB.QueryRowContext(context.Background(),
		"SELECT next_expected_at FROM checks WHERE id = ?", id).Scan(&ns)
	if err != nil {
		t.Fatalf("queryNextExpectedAt %q: %v", id, err)
	}
	if !ns.Valid {
		return nil
	}
	ti, err := time.Parse(time.RFC3339, ns.String)
	if err != nil {
		t.Fatalf("parse next_expected_at: %v", err)
	}
	return &ti
}

// countPings returns the number of ping rows for a given check.
func (e *integrationEnv) countPings(t *testing.T, checkID string) int {
	t.Helper()
	var n int
	err := e.sqlDB.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM pings WHERE check_id = ?", checkID).Scan(&n)
	if err != nil {
		t.Fatalf("countPings %q: %v", checkID, err)
	}
	return n
}

// makeBaseCheck returns a minimal check in the given status.
func makeBaseCheck(id string, status model.Status) *model.Check {
	now := time.Now().UTC().Truncate(time.Second)
	next := now.Add(10 * time.Minute)
	return &model.Check{
		ID:             id,
		Name:           "Integration " + id,
		Schedule:       "0 2 * * *",
		Grace:          10,
		Status:         status,
		NextExpectedAt: &next,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

// getReq builds a GET request with the UUID injected as a path value.
func getReq(uuid string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/ping/"+uuid, nil)
	r.SetPathValue("uuid", uuid)
	return r
}

// ---------------------------------------------------------------------------
// Integration tests
// ---------------------------------------------------------------------------

// TestPingIntegration_NewToUp verifies that a success ping on a new check
// transitions the row in SQLite to 'up' and creates a pings row.
func TestPingIntegration_NewToUp(t *testing.T) {
	env := newIntegrationEnv(t)
	c := makeBaseCheck("integ-1", model.StatusNew)
	env.seedCheck(t, c)

	w := httptest.NewRecorder()
	env.handler.HandleSuccess(w, getReq("integ-1"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	// SQLite must show 'up'.
	if got := env.queryStatus(t, "integ-1"); got != model.StatusUp {
		t.Errorf("SQLite status = %q, want %q", got, model.StatusUp)
	}

	// A pings row must exist.
	if n := env.countPings(t, "integ-1"); n != 1 {
		t.Errorf("pings count = %d, want 1", n)
	}

	// No recovery alert (new→up is not a recovery).
	if len(env.alertCh) != 0 {
		t.Errorf("alertCh len = %d, want 0", len(env.alertCh))
	}
}

// TestPingIntegration_DownToUpRecovery verifies that a success ping on a down
// check enqueues an AlertEvent and writes the ping row to SQLite.
func TestPingIntegration_DownToUpRecovery(t *testing.T) {
	env := newIntegrationEnv(t)
	c := makeBaseCheck("integ-2", model.StatusDown)
	env.seedCheck(t, c)
	env.seedChannel(t, "integ-2")

	w := httptest.NewRecorder()
	env.handler.HandleSuccess(w, getReq("integ-2"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	// SQLite must show 'up'.
	if got := env.queryStatus(t, "integ-2"); got != model.StatusUp {
		t.Errorf("SQLite status = %q, want %q", got, model.StatusUp)
	}

	// A pings row must exist.
	if n := env.countPings(t, "integ-2"); n != 1 {
		t.Errorf("pings count = %d, want 1", n)
	}

	// A recovery AlertEvent must be on the channel.
	if len(env.alertCh) != 1 {
		t.Fatalf("alertCh len = %d, want 1", len(env.alertCh))
	}
	event := <-env.alertCh
	if event.AlertType != model.AlertUp {
		t.Errorf("alert type = %q, want %q", event.AlertType, model.AlertUp)
	}
	if event.Check.ID != "integ-2" {
		t.Errorf("alert check ID = %q, want %q", event.Check.ID, "integ-2")
	}
}

// TestPingIntegration_StartPing_ExtendsDeadlineKeepsStatus verifies that a
// start ping on an up check extends next_expected_at in SQLite without
// changing the status column.
func TestPingIntegration_StartPing_ExtendsDeadlineKeepsStatus(t *testing.T) {
	env := newIntegrationEnv(t)
	c := makeBaseCheck("integ-3", model.StatusUp)
	// Seed with a next_expected_at in the past so the start-ping's new value
	// (now + grace) is clearly after it, regardless of sub-second timing.
	pastNext := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Second)
	c.NextExpectedAt = &pastNext
	originalNext := pastNext
	env.seedCheck(t, c)

	w := httptest.NewRecorder()
	env.handler.HandleStart(w, getReq("integ-3"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	// Status must remain 'up' in SQLite.
	if got := env.queryStatus(t, "integ-3"); got != model.StatusUp {
		t.Errorf("SQLite status = %q, want %q (start must not change status)", got, model.StatusUp)
	}

	// next_expected_at must be extended beyond the original value.
	newNext := env.queryNextExpectedAt(t, "integ-3")
	if newNext == nil {
		t.Fatal("next_expected_at is NULL after start ping")
	}
	if !newNext.After(originalNext) {
		t.Errorf("next_expected_at %v is not after original %v", newNext, originalNext)
	}

	// A pings row must exist with type 'start'.
	if n := env.countPings(t, "integ-3"); n != 1 {
		t.Errorf("pings count = %d, want 1", n)
	}
}

// TestPingIntegration_FailPing_DownToUp verifies that a fail ping on a down
// check triggers recovery (same behaviour as success ping).
func TestPingIntegration_FailPing_DownToUp(t *testing.T) {
	env := newIntegrationEnv(t)
	c := makeBaseCheck("integ-4", model.StatusDown)
	env.seedCheck(t, c)
	env.seedChannel(t, "integ-4")

	w := httptest.NewRecorder()
	env.handler.HandleFail(w, getReq("integ-4"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := env.queryStatus(t, "integ-4"); got != model.StatusUp {
		t.Errorf("SQLite status = %q, want %q", got, model.StatusUp)
	}
	if len(env.alertCh) != 1 {
		t.Fatalf("alertCh len = %d, want 1", len(env.alertCh))
	}
	if event := (<-env.alertCh); event.AlertType != model.AlertUp {
		t.Errorf("alert type = %q, want %q", event.AlertType, model.AlertUp)
	}
}
