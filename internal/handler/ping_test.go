package handler_test

// Unit tests for PingHandler.
//
// The cache is backed by a mockCheckRepo so no database is required.
// A mockPingRepo and mockChannelRepo record calls for assertion.
// Tests exercise the HTTP response, state transitions, and alert dispatch.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/myrrolinz/cronmon/internal/cache"
	"github.com/myrrolinz/cronmon/internal/handler"
	"github.com/myrrolinz/cronmon/internal/model"
	"github.com/myrrolinz/cronmon/internal/repository"
)

// ---------------------------------------------------------------------------
// mock CheckRepository (backs StateCache)
// ---------------------------------------------------------------------------

type mockCheckRepo struct {
	mu     sync.Mutex
	checks map[string]*model.Check
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

func (m *mockCheckRepo) get(id string) *model.Check {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.checks[id]
	if c == nil {
		return nil
	}
	cp := *c
	return &cp
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
// mock PingRepository
// ---------------------------------------------------------------------------

type mockPingRepo struct {
	mu    sync.Mutex
	pings []*model.Ping
}

func (m *mockPingRepo) Create(_ context.Context, p *model.Ping) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *p
	m.pings = append(m.pings, &cp)
	return nil
}

func (m *mockPingRepo) ListByCheckID(_ context.Context, _ string, _ int) ([]*model.Ping, error) {
	return nil, nil
}

func (m *mockPingRepo) DeleteOldest(_ context.Context, _ string, _ int) error {
	return nil
}

func (m *mockPingRepo) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pings)
}

func (m *mockPingRepo) last() *model.Ping {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.pings) == 0 {
		return nil
	}
	cp := *m.pings[len(m.pings)-1]
	return &cp
}

// ---------------------------------------------------------------------------
// mock ChannelRepository
// ---------------------------------------------------------------------------

type mockChannelRepo struct {
	mu        sync.Mutex
	byCheckID map[string][]*model.Channel
}

func newMockChannelRepo() *mockChannelRepo {
	return &mockChannelRepo{byCheckID: make(map[string][]*model.Channel)}
}

func (m *mockChannelRepo) setChannels(checkID string, channels ...*model.Channel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byCheckID[checkID] = channels
}

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
func (m *mockChannelRepo) ListByCheckID(_ context.Context, checkID string) ([]*model.Channel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	chs := m.byCheckID[checkID]
	out := make([]*model.Channel, len(chs))
	copy(out, chs)
	return out, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// makeCache seeds a StateCache with the given checks using a mock repo.
// Returns the cache and the underlying mock repo for post-call assertions.
func makeCache(t *testing.T, checks ...model.Check) (*cache.StateCache, *mockCheckRepo) {
	t.Helper()
	repo := newMockCheckRepo()
	for _, c := range checks {
		repo.seed(c)
	}
	sc := cache.New(repo)
	if err := sc.Hydrate(context.Background()); err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	return sc, repo
}

// makeCheck returns a minimal check with the given ID and status.
func makeCheck(id string, status model.Status) model.Check {
	now := time.Now().UTC().Truncate(time.Second)
	next := now.Add(10 * time.Minute)
	return model.Check{
		ID:             id,
		Name:           "Test " + id,
		Schedule:       "0 2 * * *",
		Grace:          10,
		Status:         status,
		NextExpectedAt: &next,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

// newPingHandler creates a PingHandler wired to the test doubles.
func newPingHandler(
	sc *cache.StateCache,
	pr *mockPingRepo,
	cr *mockChannelRepo,
	alertCh chan model.AlertEvent,
	trustedProxy bool,
) *handler.PingHandler {
	return handler.NewPingHandler(sc, pr, cr, alertCh, trustedProxy)
}

// get builds a GET request routed to /ping/{uuid} path.
// The uuid is injected via SetPathValue to simulate Go 1.22 routing.
func get(uuid string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/ping/"+uuid, nil)
	r.SetPathValue("uuid", uuid)
	return r
}

// ---------------------------------------------------------------------------
// HTTP response tests
// ---------------------------------------------------------------------------

func TestPingHandler_AlwaysReturns200(t *testing.T) {
	sc, _ := makeCache(t)
	pr := &mockPingRepo{}
	cr := newMockChannelRepo()
	alertCh := make(chan model.AlertEvent, 10)
	h := newPingHandler(sc, pr, cr, alertCh, false)

	cases := []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{"success", h.HandleSuccess},
		{"start", h.HandleStart},
		{"fail", h.HandleFail},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tc.handler(w, get("unknown-uuid"))

			if w.Code != http.StatusOK {
				t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
			}
			if body := w.Body.String(); body != "OK\n" {
				t.Errorf("body = %q, want %q", body, "OK\n")
			}
			if ct := w.Header().Get("Content-Type"); ct != "text/plain" {
				t.Errorf("Content-Type = %q, want %q", ct, "text/plain")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Success ping state-transition tests
// ---------------------------------------------------------------------------

func TestPingHandler_Success_NewToUp(t *testing.T) {
	c := makeCheck("check-1", model.StatusNew)
	sc, repo := makeCache(t, c)
	pr := &mockPingRepo{}
	cr := newMockChannelRepo()
	alertCh := make(chan model.AlertEvent, 10)
	h := newPingHandler(sc, pr, cr, alertCh, false)

	w := httptest.NewRecorder()
	h.HandleSuccess(w, get("check-1"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	got := repo.get("check-1")
	if got.Status != model.StatusUp {
		t.Errorf("status = %q, want %q", got.Status, model.StatusUp)
	}
	if got.LastPingAt == nil {
		t.Error("LastPingAt should be set after success ping")
	}
	if got.NextExpectedAt == nil {
		t.Error("NextExpectedAt should be updated after success ping")
	}
	// No recovery alert for new→up
	if len(alertCh) != 0 {
		t.Errorf("alertCh len = %d, want 0 (no recovery for new→up)", len(alertCh))
	}
	// Ping record created
	if pr.count() != 1 {
		t.Errorf("ping count = %d, want 1", pr.count())
	}
	if p := pr.last(); p.Type != model.PingSuccess {
		t.Errorf("ping type = %q, want %q", p.Type, model.PingSuccess)
	}
}

func TestPingHandler_Success_UpStaysUp(t *testing.T) {
	c := makeCheck("check-1", model.StatusUp)
	originalNext := *c.NextExpectedAt
	sc, repo := makeCache(t, c)
	pr := &mockPingRepo{}
	cr := newMockChannelRepo()
	alertCh := make(chan model.AlertEvent, 10)
	h := newPingHandler(sc, pr, cr, alertCh, false)

	time.Sleep(2 * time.Millisecond) // ensure now > originalNext for assertion
	w := httptest.NewRecorder()
	h.HandleSuccess(w, get("check-1"))

	got := repo.get("check-1")
	if got.Status != model.StatusUp {
		t.Errorf("status = %q, want %q (up should stay up)", got.Status, model.StatusUp)
	}
	// next_expected_at should be refreshed (after original value)
	if got.NextExpectedAt == nil || !got.NextExpectedAt.After(originalNext) {
		t.Errorf("NextExpectedAt should be refreshed after success ping")
	}
	if len(alertCh) != 0 {
		t.Errorf("alertCh len = %d, want 0 (no alert for up→up)", len(alertCh))
	}
}

func TestPingHandler_Success_DownToUpRecovery(t *testing.T) {
	c := makeCheck("check-1", model.StatusDown)
	sc, repo := makeCache(t, c)
	pr := &mockPingRepo{}
	cr := newMockChannelRepo()
	ch := &model.Channel{ID: 1, Type: "email", Name: "Ops"}
	cr.setChannels("check-1", ch)
	alertCh := make(chan model.AlertEvent, 10)
	h := newPingHandler(sc, pr, cr, alertCh, false)

	w := httptest.NewRecorder()
	h.HandleSuccess(w, get("check-1"))

	got := repo.get("check-1")
	if got.Status != model.StatusUp {
		t.Errorf("status = %q, want %q", got.Status, model.StatusUp)
	}
	if len(alertCh) != 1 {
		t.Fatalf("alertCh len = %d, want 1", len(alertCh))
	}
	event := <-alertCh
	if event.AlertType != model.AlertUp {
		t.Errorf("alert type = %q, want %q", event.AlertType, model.AlertUp)
	}
	if event.Check.ID != "check-1" {
		t.Errorf("alert check ID = %q, want %q", event.Check.ID, "check-1")
	}
	if event.Channel.ID != 1 {
		t.Errorf("alert channel ID = %d, want 1", event.Channel.ID)
	}
}

// ---------------------------------------------------------------------------
// Fail ping tests
// ---------------------------------------------------------------------------

func TestPingHandler_Fail_DownToUpRecovery(t *testing.T) {
	c := makeCheck("check-1", model.StatusDown)
	sc, repo := makeCache(t, c)
	pr := &mockPingRepo{}
	cr := newMockChannelRepo()
	ch := &model.Channel{ID: 2, Type: "slack", Name: "Slack"}
	cr.setChannels("check-1", ch)
	alertCh := make(chan model.AlertEvent, 10)
	h := newPingHandler(sc, pr, cr, alertCh, false)

	w := httptest.NewRecorder()
	h.HandleFail(w, get("check-1"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	got := repo.get("check-1")
	if got.Status != model.StatusUp {
		t.Errorf("status = %q, want %q", got.Status, model.StatusUp)
	}
	// Fail ping is a recovery if the check was down.
	if len(alertCh) != 1 {
		t.Fatalf("alertCh len = %d, want 1", len(alertCh))
	}
	if event := (<-alertCh); event.AlertType != model.AlertUp {
		t.Errorf("alert type = %q, want %q", event.AlertType, model.AlertUp)
	}
	// Ping type must be recorded as "fail", not "success".
	if p := pr.last(); p.Type != model.PingFail {
		t.Errorf("ping type = %q, want %q", p.Type, model.PingFail)
	}
}

func TestPingHandler_Fail_NewToUp(t *testing.T) {
	c := makeCheck("check-1", model.StatusNew)
	sc, repo := makeCache(t, c)
	pr := &mockPingRepo{}
	cr := newMockChannelRepo()
	alertCh := make(chan model.AlertEvent, 10)
	h := newPingHandler(sc, pr, cr, alertCh, false)

	w := httptest.NewRecorder()
	h.HandleFail(w, get("check-1"))

	got := repo.get("check-1")
	if got.Status != model.StatusUp {
		t.Errorf("status = %q, want %q", got.Status, model.StatusUp)
	}
	// new→up via fail is not a recovery (no previous alert was sent).
	if len(alertCh) != 0 {
		t.Errorf("alertCh len = %d, want 0 (no recovery for new→up)", len(alertCh))
	}
}

// ---------------------------------------------------------------------------
// Paused check tests
// ---------------------------------------------------------------------------

func TestPingHandler_PausedCheckIgnored(t *testing.T) {
	c := makeCheck("check-1", model.StatusPaused)
	sc, repo := makeCache(t, c)
	pr := &mockPingRepo{}
	cr := newMockChannelRepo()
	alertCh := make(chan model.AlertEvent, 10)
	h := newPingHandler(sc, pr, cr, alertCh, false)

	for _, name := range []string{"success", "start", "fail"} {
		t.Run(name, func(t *testing.T) {
			var fn func(http.ResponseWriter, *http.Request)
			switch name {
			case "success":
				fn = h.HandleSuccess
			case "start":
				fn = h.HandleStart
			default:
				fn = h.HandleFail
			}
			w := httptest.NewRecorder()
			fn(w, get("check-1"))

			if w.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", w.Code)
			}
			// Status must not change (still paused).
			got := repo.get("check-1")
			if got.Status != model.StatusPaused {
				t.Errorf("status = %q, want %q (paused should be ignored)", got.Status, model.StatusPaused)
			}
			// No pings recorded and no alerts sent.
			if pr.count() != 0 {
				t.Errorf("ping count = %d, want 0 for paused check", pr.count())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Start ping tests
// ---------------------------------------------------------------------------

func TestPingHandler_Start_UpdatesNextExpectedAtButNotStatus(t *testing.T) {
	c := makeCheck("check-1", model.StatusUp)
	originalNext := *c.NextExpectedAt
	sc, repo := makeCache(t, c)
	pr := &mockPingRepo{}
	cr := newMockChannelRepo()
	alertCh := make(chan model.AlertEvent, 10)
	h := newPingHandler(sc, pr, cr, alertCh, false)

	w := httptest.NewRecorder()
	h.HandleStart(w, get("check-1"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	got := repo.get("check-1")
	if got.Status != model.StatusUp {
		t.Errorf("status = %q, want %q (start should not change status)", got.Status, model.StatusUp)
	}
	// next_expected_at should be extended (now + grace, so after original).
	if got.NextExpectedAt == nil || !got.NextExpectedAt.After(originalNext) {
		t.Errorf("NextExpectedAt should be extended by start ping")
	}
	if got.LastPingAt != nil {
		t.Error("LastPingAt should NOT be set by a start ping")
	}
	if len(alertCh) != 0 {
		t.Errorf("alertCh len = %d, want 0 (no alert for start ping)", len(alertCh))
	}
	// Ping record created with correct type.
	if pr.count() != 1 {
		t.Fatalf("ping count = %d, want 1", pr.count())
	}
	if p := pr.last(); p.Type != model.PingStart {
		t.Errorf("ping type = %q, want %q", p.Type, model.PingStart)
	}
}

func TestPingHandler_Start_OnDownCheckKeepsDown(t *testing.T) {
	c := makeCheck("check-1", model.StatusDown)
	sc, repo := makeCache(t, c)
	pr := &mockPingRepo{}
	cr := newMockChannelRepo()
	alertCh := make(chan model.AlertEvent, 10)
	h := newPingHandler(sc, pr, cr, alertCh, false)

	w := httptest.NewRecorder()
	h.HandleStart(w, get("check-1"))

	got := repo.get("check-1")
	if got.Status != model.StatusDown {
		t.Errorf("status = %q, want %q (start ping must not recover a down check)", got.Status, model.StatusDown)
	}
	if len(alertCh) != 0 {
		t.Errorf("alertCh len = %d, want 0", len(alertCh))
	}
}

func TestPingHandler_Start_OnNewCheckKeepsNew(t *testing.T) {
	c := makeCheck("check-1", model.StatusNew)
	sc, repo := makeCache(t, c)
	pr := &mockPingRepo{}
	cr := newMockChannelRepo()
	alertCh := make(chan model.AlertEvent, 10)
	h := newPingHandler(sc, pr, cr, alertCh, false)

	w := httptest.NewRecorder()
	h.HandleStart(w, get("check-1"))

	got := repo.get("check-1")
	if got.Status != model.StatusNew {
		t.Errorf("status = %q, want %q (start ping must not change new status)", got.Status, model.StatusNew)
	}
}

// ---------------------------------------------------------------------------
// Unknown UUID test
// ---------------------------------------------------------------------------

func TestPingHandler_UnknownUUID_Returns200NoPanic(t *testing.T) {
	sc, _ := makeCache(t) // empty cache
	pr := &mockPingRepo{}
	cr := newMockChannelRepo()
	alertCh := make(chan model.AlertEvent, 10)
	h := newPingHandler(sc, pr, cr, alertCh, false)

	cases := []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{"success", h.HandleSuccess},
		{"start", h.HandleStart},
		{"fail", h.HandleFail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			// Must not panic.
			tc.handler(w, get("00000000-0000-0000-0000-000000000000"))
			if w.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", w.Code)
			}
			if pr.count() != 0 {
				t.Errorf("ping count = %d, want 0 for unknown UUID", pr.count())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Source IP extraction tests
// ---------------------------------------------------------------------------

func TestPingHandler_SourceIP_RemoteAddr(t *testing.T) {
	c := makeCheck("check-1", model.StatusUp)
	sc, _ := makeCache(t, c)
	pr := &mockPingRepo{}
	cr := newMockChannelRepo()
	alertCh := make(chan model.AlertEvent, 10)
	h := newPingHandler(sc, pr, cr, alertCh, false)

	r := httptest.NewRequest(http.MethodGet, "/ping/check-1", nil)
	r.SetPathValue("uuid", "check-1")
	r.RemoteAddr = "203.0.113.10:54321"

	w := httptest.NewRecorder()
	h.HandleSuccess(w, r)

	if p := pr.last(); p.SourceIP != "203.0.113.10" {
		t.Errorf("SourceIP = %q, want %q", p.SourceIP, "203.0.113.10")
	}
}

func TestPingHandler_SourceIP_TrustedProxy_XFF(t *testing.T) {
	c := makeCheck("check-1", model.StatusUp)
	sc, _ := makeCache(t, c)
	pr := &mockPingRepo{}
	cr := newMockChannelRepo()
	alertCh := make(chan model.AlertEvent, 10)
	h := newPingHandler(sc, pr, cr, alertCh, true) // trustedProxy=true

	r := httptest.NewRequest(http.MethodGet, "/ping/check-1", nil)
	r.SetPathValue("uuid", "check-1")
	r.Header.Set("X-Forwarded-For", "198.51.100.5, 10.0.0.1")
	r.RemoteAddr = "10.0.0.1:12345"

	w := httptest.NewRecorder()
	h.HandleSuccess(w, r)

	if p := pr.last(); p.SourceIP != "198.51.100.5" {
		t.Errorf("SourceIP = %q, want leftmost XFF value %q", p.SourceIP, "198.51.100.5")
	}
}

func TestPingHandler_SourceIP_TrustedProxy_FallsBackToRemoteAddr(t *testing.T) {
	c := makeCheck("check-1", model.StatusUp)
	sc, _ := makeCache(t, c)
	pr := &mockPingRepo{}
	cr := newMockChannelRepo()
	alertCh := make(chan model.AlertEvent, 10)
	h := newPingHandler(sc, pr, cr, alertCh, true) // trustedProxy=true, no XFF

	r := httptest.NewRequest(http.MethodGet, "/ping/check-1", nil)
	r.SetPathValue("uuid", "check-1")
	r.RemoteAddr = "203.0.113.99:9876"
	// No X-Forwarded-For header.

	w := httptest.NewRecorder()
	h.HandleSuccess(w, r)

	if p := pr.last(); p.SourceIP != "203.0.113.99" {
		t.Errorf("SourceIP = %q, want %q", p.SourceIP, "203.0.113.99")
	}
}

// ---------------------------------------------------------------------------
// NotifyOnFail opt-in tests
// ---------------------------------------------------------------------------

// TestPingHandler_Fail_NotifyOnFail_UpStaysUp_AlertFired verifies that when
// NotifyOnFail=true a /fail ping on an up check fires an AlertFail event
// without changing the status.
func TestPingHandler_Fail_NotifyOnFail_UpStaysUp_AlertFired(t *testing.T) {
	c := makeCheck("check-1", model.StatusUp)
	c.NotifyOnFail = true
	sc, repo := makeCache(t, c)
	pr := &mockPingRepo{}
	cr := newMockChannelRepo()
	ch := &model.Channel{ID: 1, Type: "email", Name: "Ops"}
	cr.setChannels("check-1", ch)
	alertCh := make(chan model.AlertEvent, 10)
	h := newPingHandler(sc, pr, cr, alertCh, false)

	w := httptest.NewRecorder()
	h.HandleFail(w, get("check-1"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	// Status must remain up.
	got := repo.get("check-1")
	if got.Status != model.StatusUp {
		t.Errorf("status = %q, want %q (fail must not change up status)", got.Status, model.StatusUp)
	}
	// Exactly one AlertFail event must be fired.
	if len(alertCh) != 1 {
		t.Fatalf("alertCh len = %d, want 1", len(alertCh))
	}
	event := <-alertCh
	if event.AlertType != model.AlertFail {
		t.Errorf("alert type = %q, want %q", event.AlertType, model.AlertFail)
	}
	if event.Check.ID != "check-1" {
		t.Errorf("alert check ID = %q, want %q", event.Check.ID, "check-1")
	}
}

// TestPingHandler_Fail_NotifyOnFail_False_NoAlert verifies that when
// NotifyOnFail=false a /fail ping on an up check fires no alert.
func TestPingHandler_Fail_NotifyOnFail_False_NoAlert(t *testing.T) {
	c := makeCheck("check-1", model.StatusUp)
	c.NotifyOnFail = false
	sc, _ := makeCache(t, c)
	pr := &mockPingRepo{}
	cr := newMockChannelRepo()
	ch := &model.Channel{ID: 1, Type: "email", Name: "Ops"}
	cr.setChannels("check-1", ch)
	alertCh := make(chan model.AlertEvent, 10)
	h := newPingHandler(sc, pr, cr, alertCh, false)

	w := httptest.NewRecorder()
	h.HandleFail(w, get("check-1"))

	if len(alertCh) != 0 {
		t.Errorf("alertCh len = %d, want 0 (NotifyOnFail=false must suppress alert)", len(alertCh))
	}
}

// TestPingHandler_Fail_NotifyOnFail_DownToUp_BothAlertsEnqueued verifies that
// when NotifyOnFail=true and the check was down, both a recovery (AlertUp) and
// a fail (AlertFail) event are enqueued — one per semantic.
func TestPingHandler_Fail_NotifyOnFail_DownToUp_BothAlertsEnqueued(t *testing.T) {
	c := makeCheck("check-1", model.StatusDown)
	c.NotifyOnFail = true
	sc, _ := makeCache(t, c)
	pr := &mockPingRepo{}
	cr := newMockChannelRepo()
	ch := &model.Channel{ID: 1, Type: "email", Name: "Ops"}
	cr.setChannels("check-1", ch)
	alertCh := make(chan model.AlertEvent, 10)
	h := newPingHandler(sc, pr, cr, alertCh, false)

	w := httptest.NewRecorder()
	h.HandleFail(w, get("check-1"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	// Two events: recovery + fail.
	if len(alertCh) != 2 {
		t.Fatalf("alertCh len = %d, want 2 (recovery + fail)", len(alertCh))
	}
	types := map[model.AlertType]bool{}
	for i := 0; i < 2; i++ {
		e := <-alertCh
		types[e.AlertType] = true
	}
	if !types[model.AlertUp] {
		t.Error("expected an AlertUp (recovery) event")
	}
	if !types[model.AlertFail] {
		t.Error("expected an AlertFail event")
	}
}

// TestPingHandler_Success_NotifyOnFail_NoExtraAlert verifies that a success
// ping never fires an AlertFail even when NotifyOnFail=true.
func TestPingHandler_Success_NotifyOnFail_NoExtraAlert(t *testing.T) {
	c := makeCheck("check-1", model.StatusUp)
	c.NotifyOnFail = true
	sc, _ := makeCache(t, c)
	pr := &mockPingRepo{}
	cr := newMockChannelRepo()
	ch := &model.Channel{ID: 1, Type: "email", Name: "Ops"}
	cr.setChannels("check-1", ch)
	alertCh := make(chan model.AlertEvent, 10)
	h := newPingHandler(sc, pr, cr, alertCh, false)

	w := httptest.NewRecorder()
	h.HandleSuccess(w, get("check-1"))

	if len(alertCh) != 0 {
		t.Errorf("alertCh len = %d, want 0 (success never fires AlertFail)", len(alertCh))
	}
}
