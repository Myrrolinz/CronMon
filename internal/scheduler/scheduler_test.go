// Tests for the scheduler package.
// Filed as an internal test (package scheduler) so tests can access the
// unexported cleanupInterval field to avoid real 1-hour waits.
package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/myrrolinz/cronmon/internal/cache"
	"github.com/myrrolinz/cronmon/internal/model"
	"github.com/myrrolinz/cronmon/internal/repository"
)

// ---------------------------------------------------------------------------
// mock CheckRepository (backs StateCache in tests)
// ---------------------------------------------------------------------------

type mockCheckRepo struct {
	mu     sync.Mutex
	checks map[string]*model.Check

	updateErr error
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
	delete(m.checks, id)
	return nil
}

// ---------------------------------------------------------------------------
// mock ChannelRepository
// ---------------------------------------------------------------------------

type mockChannelRepo struct {
	mu         sync.Mutex
	byCheckID  map[string][]*model.Channel
	listErr    error
	listCalled int
}

func newMockChannelRepo() *mockChannelRepo {
	return &mockChannelRepo{byCheckID: make(map[string][]*model.Channel)}
}

func (m *mockChannelRepo) setChannels(checkID string, channels ...*model.Channel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byCheckID[checkID] = channels
}

func (m *mockChannelRepo) ListByCheckID(_ context.Context, checkID string) ([]*model.Channel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listCalled++
	if m.listErr != nil {
		return nil, m.listErr
	}
	chs := m.byCheckID[checkID]
	out := make([]*model.Channel, len(chs))
	copy(out, chs)
	return out, nil
}

// Unused interface stubs.
func (m *mockChannelRepo) Create(_ context.Context, _ *model.Channel) error { return nil }
func (m *mockChannelRepo) GetByID(_ context.Context, _ int64) (*model.Channel, error) {
	return nil, nil
}
func (m *mockChannelRepo) ListAll(_ context.Context) ([]*model.Channel, error) { return nil, nil }
func (m *mockChannelRepo) Delete(_ context.Context, _ int64) error             { return nil }
func (m *mockChannelRepo) AttachToCheck(_ context.Context, _ string, _ int64) error {
	return nil
}
func (m *mockChannelRepo) DetachFromCheck(_ context.Context, _ string, _ int64) error {
	return nil
}

// ---------------------------------------------------------------------------
// mock PingRepository
// ---------------------------------------------------------------------------

type mockPingRepo struct {
	mu                 sync.Mutex
	deleteOldestCalled int
	lastKeepN          int
	deleteErr          error
}

func (m *mockPingRepo) DeleteOldest(_ context.Context, _ string, keepN int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.deleteOldestCalled++
	m.lastKeepN = keepN
	return nil
}

func (m *mockPingRepo) Create(_ context.Context, _ *model.Ping) error { return nil }
func (m *mockPingRepo) ListByCheckID(_ context.Context, _ string, _ int) ([]*model.Ping, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// makeCache builds a StateCache seeded with the provided checks and returns
// both the cache and the underlying mock check repo for post-call assertions.
func makeCache(t *testing.T, checks ...*model.Check) (*cache.StateCache, *mockCheckRepo) {
	t.Helper()
	cr := newMockCheckRepo()
	for _, c := range checks {
		cr.seed(*c)
	}
	sc := cache.New(cr)
	if err := sc.Hydrate(context.Background()); err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	return sc, cr
}

// newTestScheduler builds a Scheduler with a very long regular interval and a
// short cleanupInterval so tests can trigger the cleanup tick quickly.
func newTestScheduler(
	sc *cache.StateCache,
	chRepo *mockChannelRepo,
	pingRepo *mockPingRepo,
	alertCh chan model.AlertEvent,
) *Scheduler {
	s := New(sc, chRepo, pingRepo, alertCh, 24*time.Hour)
	s.cleanupInterval = 10 * time.Millisecond
	return s
}

// pastTime returns a fixed time in the past relative to now.
func pastTime(d time.Duration) time.Time {
	return time.Now().Add(-d)
}

// ---------------------------------------------------------------------------
// evaluateAll tests
// ---------------------------------------------------------------------------

func TestEvaluateAll_UpToDown(t *testing.T) {
	overdue := pastTime(5 * time.Minute)
	c := &model.Check{
		ID:             "check-1",
		Status:         model.StatusUp,
		Schedule:       "* * * * *",
		Grace:          1,
		NextExpectedAt: &overdue,
	}

	sc, checkRepo := makeCache(t, c)
	chRepo := newMockChannelRepo()
	ch := &model.Channel{ID: 1, Type: "email", Name: "Ops"}
	chRepo.setChannels("check-1", ch)

	alertCh := make(chan model.AlertEvent, 10)
	s := newTestScheduler(sc, chRepo, &mockPingRepo{}, alertCh)
	s.evaluateAll(context.Background(), time.Now())

	// Check status must be "down" in the mock repo (write-through).
	got, err := checkRepo.GetByID(context.Background(), "check-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != model.StatusDown {
		t.Errorf("status = %q, want %q", got.Status, model.StatusDown)
	}

	// One AlertEvent must have been sent.
	if len(alertCh) != 1 {
		t.Fatalf("alertCh len = %d, want 1", len(alertCh))
	}
	event := <-alertCh
	if event.AlertType != model.AlertDown {
		t.Errorf("alert type = %q, want %q", event.AlertType, model.AlertDown)
	}
	if event.Check.ID != "check-1" {
		t.Errorf("event check ID = %q, want %q", event.Check.ID, "check-1")
	}
	if event.Channel.ID != 1 {
		t.Errorf("event channel ID = %d, want 1", event.Channel.ID)
	}
}

func TestEvaluateAll_SkipsPaused(t *testing.T) {
	overdue := pastTime(5 * time.Minute)
	c := &model.Check{
		ID:             "check-paused",
		Status:         model.StatusPaused,
		NextExpectedAt: &overdue,
	}

	sc, _ := makeCache(t, c)
	chRepo := newMockChannelRepo()
	alertCh := make(chan model.AlertEvent, 10)

	s := newTestScheduler(sc, chRepo, &mockPingRepo{}, alertCh)
	s.evaluateAll(context.Background(), time.Now())

	if len(alertCh) != 0 {
		t.Errorf("alertCh len = %d, want 0 (paused checks must not trigger alerts)", len(alertCh))
	}
	if chRepo.listCalled != 0 {
		t.Errorf("ListByCheckID called %d times, want 0", chRepo.listCalled)
	}
}

func TestEvaluateAll_SkipsNew(t *testing.T) {
	overdue := pastTime(5 * time.Minute)
	c := &model.Check{
		ID:             "check-new",
		Status:         model.StatusNew,
		NextExpectedAt: &overdue,
	}

	sc, _ := makeCache(t, c)
	alertCh := make(chan model.AlertEvent, 10)

	s := newTestScheduler(sc, newMockChannelRepo(), &mockPingRepo{}, alertCh)
	s.evaluateAll(context.Background(), time.Now())

	if len(alertCh) != 0 {
		t.Errorf("alertCh len = %d, want 0 (new checks must not trigger alerts)", len(alertCh))
	}
}

func TestEvaluateAll_SkipsAlreadyDown(t *testing.T) {
	overdue := pastTime(5 * time.Minute)
	c := &model.Check{
		ID:             "check-down",
		Status:         model.StatusDown,
		NextExpectedAt: &overdue,
	}

	sc, _ := makeCache(t, c)
	alertCh := make(chan model.AlertEvent, 10)

	s := newTestScheduler(sc, newMockChannelRepo(), &mockPingRepo{}, alertCh)
	s.evaluateAll(context.Background(), time.Now())

	if len(alertCh) != 0 {
		t.Errorf("alertCh len = %d, want 0 (already-down checks must not re-trigger)", len(alertCh))
	}
}

func TestEvaluateAll_SkipsUpNotOverdue(t *testing.T) {
	future := time.Now().Add(10 * time.Minute)
	c := &model.Check{
		ID:             "check-fine",
		Status:         model.StatusUp,
		Schedule:       "* * * * *",
		Grace:          1,
		NextExpectedAt: &future,
	}

	sc, checkRepo := makeCache(t, c)
	alertCh := make(chan model.AlertEvent, 10)

	s := newTestScheduler(sc, newMockChannelRepo(), &mockPingRepo{}, alertCh)
	s.evaluateAll(context.Background(), time.Now())

	got, _ := checkRepo.GetByID(context.Background(), "check-fine")
	if got.Status != model.StatusUp {
		t.Errorf("status = %q, want %q", got.Status, model.StatusUp)
	}
	if len(alertCh) != 0 {
		t.Errorf("alertCh len = %d, want 0", len(alertCh))
	}
}

func TestEvaluateAll_SkipsNilNextExpectedAt(t *testing.T) {
	c := &model.Check{
		ID:             "check-nil-deadline",
		Status:         model.StatusUp,
		NextExpectedAt: nil,
	}

	sc, _ := makeCache(t, c)
	alertCh := make(chan model.AlertEvent, 10)

	s := newTestScheduler(sc, newMockChannelRepo(), &mockPingRepo{}, alertCh)
	s.evaluateAll(context.Background(), time.Now())

	if len(alertCh) != 0 {
		t.Errorf("alertCh len = %d, want 0 (nil NextExpectedAt must be skipped)", len(alertCh))
	}
}

func TestEvaluateAll_NonBlockingDropWhenFull(t *testing.T) {
	overdue := pastTime(5 * time.Minute)
	c := &model.Check{
		ID:             "check-full",
		Status:         model.StatusUp,
		Schedule:       "* * * * *",
		Grace:          1,
		NextExpectedAt: &overdue,
	}

	sc, _ := makeCache(t, c)
	chRepo := newMockChannelRepo()
	chRepo.setChannels("check-full", &model.Channel{ID: 1, Type: "email"})

	// Zero-capacity channel: any blocking send would hang.
	alertCh := make(chan model.AlertEvent)
	s := newTestScheduler(sc, chRepo, &mockPingRepo{}, alertCh)

	done := make(chan struct{})
	go func() {
		s.evaluateAll(context.Background(), time.Now())
		close(done)
	}()

	select {
	case <-done:
		// Good: evaluateAll returned without blocking.
	case <-time.After(time.Second):
		t.Error("evaluateAll blocked when alertCh was full")
	}
}

func TestEvaluateAll_MultipleChannels(t *testing.T) {
	overdue := pastTime(5 * time.Minute)
	c := &model.Check{
		ID:             "check-multi",
		Status:         model.StatusUp,
		Schedule:       "* * * * *",
		Grace:          1,
		NextExpectedAt: &overdue,
	}

	sc, _ := makeCache(t, c)
	chRepo := newMockChannelRepo()
	chRepo.setChannels("check-multi",
		&model.Channel{ID: 1, Type: "email"},
		&model.Channel{ID: 2, Type: "slack"},
	)
	alertCh := make(chan model.AlertEvent, 10)

	s := newTestScheduler(sc, chRepo, &mockPingRepo{}, alertCh)
	s.evaluateAll(context.Background(), time.Now())

	if len(alertCh) != 2 {
		t.Errorf("alertCh len = %d, want 2 (one per channel)", len(alertCh))
	}
}

func TestEvaluateAll_NoChannelsSubscribed(t *testing.T) {
	overdue := pastTime(5 * time.Minute)
	c := &model.Check{
		ID:             "check-no-channels",
		Status:         model.StatusUp,
		Schedule:       "* * * * *",
		Grace:          1,
		NextExpectedAt: &overdue,
	}

	sc, checkRepo := makeCache(t, c)
	// No channels attached.
	alertCh := make(chan model.AlertEvent, 10)

	s := newTestScheduler(sc, newMockChannelRepo(), &mockPingRepo{}, alertCh)
	s.evaluateAll(context.Background(), time.Now())

	// Check still transitions to "down" even with no channels.
	got, _ := checkRepo.GetByID(context.Background(), "check-no-channels")
	if got.Status != model.StatusDown {
		t.Errorf("status = %q, want %q", got.Status, model.StatusDown)
	}
	if len(alertCh) != 0 {
		t.Errorf("alertCh len = %d, want 0 (no channels subscribed)", len(alertCh))
	}
}

// ---------------------------------------------------------------------------
// Startup reconciliation test
// ---------------------------------------------------------------------------

func TestStartupReconciliation(t *testing.T) {
	// A check that went down while offline should be detected immediately on Start().
	overdue := pastTime(5 * time.Minute)
	c := &model.Check{
		ID:             "check-offline",
		Status:         model.StatusUp,
		Schedule:       "* * * * *",
		Grace:          1,
		NextExpectedAt: &overdue,
	}

	sc, checkRepo := makeCache(t, c)
	chRepo := newMockChannelRepo()
	chRepo.setChannels("check-offline", &model.Channel{ID: 1, Type: "email"})
	alertCh := make(chan model.AlertEvent, 10)

	// Long regular interval so only the startup reconciliation fires.
	s := New(sc, chRepo, &mockPingRepo{}, alertCh, 24*time.Hour)
	s.cleanupInterval = time.Hour // no fast cleanup needed here

	s.Start()
	defer s.Stop()

	// Give the goroutine enough time to complete the startup pass.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		got, _ := checkRepo.GetByID(context.Background(), "check-offline")
		if got != nil && got.Status == model.StatusDown {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	got, err := checkRepo.GetByID(context.Background(), "check-offline")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != model.StatusDown {
		t.Errorf("status after startup reconciliation = %q, want %q", got.Status, model.StatusDown)
	}
}

// ---------------------------------------------------------------------------
// Cleanup test
// ---------------------------------------------------------------------------

func TestCleanupOldPings_CalledOnTick(t *testing.T) {
	c := &model.Check{
		ID:       "check-cleanup",
		Status:   model.StatusUp,
		Schedule: "* * * * *",
		Grace:    1,
	}

	sc, _ := makeCache(t, c)
	pingRepo := &mockPingRepo{}
	alertCh := make(chan model.AlertEvent, 10)

	s := New(sc, newMockChannelRepo(), pingRepo, alertCh, 24*time.Hour)
	s.cleanupInterval = 10 * time.Millisecond

	s.Start()
	defer s.Stop()

	// Poll until at least one cleanup pass has run.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		pingRepo.mu.Lock()
		called := pingRepo.deleteOldestCalled
		pingRepo.mu.Unlock()
		if called >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	pingRepo.mu.Lock()
	called := pingRepo.deleteOldestCalled
	keepN := pingRepo.lastKeepN
	pingRepo.mu.Unlock()

	if called == 0 {
		t.Error("DeleteOldest was never called; cleanup did not run on hourly tick")
	}
	if keepN != 1000 {
		t.Errorf("keepN = %d, want 1000", keepN)
	}
}

// ---------------------------------------------------------------------------
// Stop / lifecycle tests
// ---------------------------------------------------------------------------

func TestStop_DoesNotDeadlock(t *testing.T) {
	sc, _ := makeCache(t)
	s := New(sc, newMockChannelRepo(), &mockPingRepo{},
		make(chan model.AlertEvent, 1), 24*time.Hour)
	s.cleanupInterval = time.Hour

	s.Start()

	done := make(chan struct{})
	go func() {
		s.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("Stop() deadlocked")
	}
}

func TestStop_Idempotent(t *testing.T) {
	sc, _ := makeCache(t)
	s := New(sc, newMockChannelRepo(), &mockPingRepo{},
		make(chan model.AlertEvent, 1), 24*time.Hour)
	s.cleanupInterval = time.Hour
	s.Start()
	s.Stop()

	// Second Stop must not panic.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("second Stop() panicked: %v", r)
		}
	}()
	s.Stop()
}

func TestNew_PanicsOnZeroInterval(t *testing.T) {
	sc, _ := makeCache(t)
	defer func() {
		if r := recover(); r == nil {
			t.Error("New() with zero interval should panic")
		}
	}()
	New(sc, newMockChannelRepo(), &mockPingRepo{}, make(chan model.AlertEvent, 1), 0)
}

func TestNew_PanicsOnNegativeInterval(t *testing.T) {
	sc, _ := makeCache(t)
	defer func() {
		if r := recover(); r == nil {
			t.Error("New() with negative interval should panic")
		}
	}()
	New(sc, newMockChannelRepo(), &mockPingRepo{}, make(chan model.AlertEvent, 1), -time.Second)
}

// TestConcurrentPingsAndEvaluate verifies the scheduler's write lock does not
// race with concurrent cache reads via the -race detector.
func TestConcurrentPingsAndEvaluate(t *testing.T) {
	overdue := pastTime(5 * time.Minute)
	c := &model.Check{
		ID:             "check-race",
		Status:         model.StatusUp,
		Schedule:       "* * * * *",
		Grace:          1,
		NextExpectedAt: &overdue,
	}

	sc, _ := makeCache(t, c)
	chRepo := newMockChannelRepo()
	chRepo.setChannels("check-race", &model.Channel{ID: 1, Type: "email"})
	alertCh := make(chan model.AlertEvent, 100)

	s := newTestScheduler(sc, chRepo, &mockPingRepo{}, alertCh)
	s.Start()
	defer s.Stop()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Concurrent reads via Snapshot must not race with evaluateAll.
			_ = sc.Snapshot()
		}()
	}
	wg.Wait()
}
