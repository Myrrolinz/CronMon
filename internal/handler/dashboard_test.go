package handler_test

// Unit tests for DashboardHandler.
//
// Templates are parsed from the real web.FS (embedded at compile time), so
// these tests validate that templates don't panic on nil fields, correct
// content is rendered, and tag filtering works as expected.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/myrrolinz/cronmon/internal/handler"
	"github.com/myrrolinz/cronmon/internal/model"
)

// ---------------------------------------------------------------------------
// seededPingRepo – a PingRepository that returns a configured slice of pings
// ---------------------------------------------------------------------------

type seededPingRepo struct {
	mu    sync.Mutex
	pings []*model.Ping
}

func newSeededPingRepo(pings ...*model.Ping) *seededPingRepo {
	r := &seededPingRepo{}
	r.pings = append(r.pings, pings...)
	return r
}

func (r *seededPingRepo) Create(_ context.Context, _ *model.Ping) error { return nil }

func (r *seededPingRepo) ListByCheckID(_ context.Context, _ string, limit int) ([]*model.Ping, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if limit > 0 && len(r.pings) > limit {
		return r.pings[:limit], nil
	}
	cp := make([]*model.Ping, len(r.pings))
	copy(cp, r.pings)
	return cp, nil
}

func (r *seededPingRepo) DeleteOldest(_ context.Context, _ string, _ int) error { return nil }

// ---------------------------------------------------------------------------
// fakeNotifRepo – a NotificationRepository that returns a configured slice
// ---------------------------------------------------------------------------

type fakeNotifRepo struct {
	mu            sync.Mutex
	notifications []*model.Notification
}

func newFakeNotifRepo(ns ...*model.Notification) *fakeNotifRepo {
	r := &fakeNotifRepo{}
	r.notifications = append(r.notifications, ns...)
	return r
}

func (r *fakeNotifRepo) Create(_ context.Context, _ *model.Notification) error { return nil }

func (r *fakeNotifRepo) ListByCheckID(_ context.Context, _ string, limit int) ([]*model.Notification, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if limit > 0 && len(r.notifications) > limit {
		return r.notifications[:limit], nil
	}
	cp := make([]*model.Notification, len(r.notifications))
	copy(cp, r.notifications)
	return cp, nil
}

// pings is used below only for length comparison — define a real field.
func (r *fakeNotifRepo) pings() int { return len(r.notifications) }

// ---------------------------------------------------------------------------
// helper: build a DashboardHandler with all-empty repos for rendering tests
// ---------------------------------------------------------------------------

func newDashboardHandler(
	t *testing.T,
	checks []model.Check,
	pings []*model.Ping,
	allChannels []*model.Channel,
	attachedChannels []*model.Channel,
	notifications []*model.Notification,
) *handler.DashboardHandler {
	t.Helper()

	sc, _ := makeCheckCache(t, checks...)

	pr := newSeededPingRepo(pings...)

	cr := newFakeChannelRepo()
	for _, ch := range allChannels {
		cr.seed(*ch)
	}
	if len(attachedChannels) > 0 && len(checks) > 0 {
		for _, ch := range attachedChannels {
			_ = cr.AttachToCheck(context.Background(), checks[0].ID, ch.ID)
		}
	}

	nr := newFakeNotifRepo(notifications...)

	h, err := handler.NewDashboardHandler(sc, pr, cr, nr, "https://cronmon.example.com")
	if err != nil {
		t.Fatalf("NewDashboardHandler: %v", err)
	}
	return h
}

// ---------------------------------------------------------------------------
// TestDashboardHandler_HandleIndex
// ---------------------------------------------------------------------------

func TestDashboardHandler_HandleIndex(t *testing.T) {
	h := newDashboardHandler(t, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	http.HandlerFunc(h.HandleIndex).ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d (SeeOther)", rec.Code, http.StatusSeeOther)
	}
	if loc := rec.Header().Get("Location"); loc != "/checks" {
		t.Errorf("Location = %q, want %q", loc, "/checks")
	}
}

// ---------------------------------------------------------------------------
// TestDashboardHandler_HandleCheckList
// ---------------------------------------------------------------------------

func TestDashboardHandler_HandleCheckList_Empty(t *testing.T) {
	h := newDashboardHandler(t, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/checks", nil)
	rec := httptest.NewRecorder()
	http.HandlerFunc(h.HandleCheckList).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "CronMon") {
		t.Errorf("response missing site title: %q", body[:min(200, len(body))])
	}
}

func TestDashboardHandler_HandleCheckList_NilPtrFields(t *testing.T) {
	// Check with nil LastPingAt and nil NextExpectedAt must not cause a panic.
	check := model.Check{
		ID:             "abc-123",
		Name:           "My Job",
		Schedule:       "0 * * * *",
		Grace:          5,
		Status:         model.StatusNew,
		LastPingAt:     nil, // explicitly nil
		NextExpectedAt: nil, // explicitly nil
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	h := newDashboardHandler(t, []model.Check{check}, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/checks", nil)
	rec := httptest.NewRecorder()

	// Should not panic.
	http.HandlerFunc(h.HandleCheckList).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "My Job") {
		t.Errorf("response missing check name; body snippet: %q", body[:min(400, len(body))])
	}
}

func TestDashboardHandler_HandleCheckList_ShowsChecks(t *testing.T) {
	now := time.Now().UTC()
	next := now.Add(10 * time.Minute)
	check := model.Check{
		ID:             "uuid-1",
		Name:           "Database Backup",
		Schedule:       "0 2 * * *",
		Grace:          10,
		Status:         model.StatusUp,
		LastPingAt:     &now,
		NextExpectedAt: &next,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	h := newDashboardHandler(t, []model.Check{check}, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/checks", nil)
	rec := httptest.NewRecorder()
	http.HandlerFunc(h.HandleCheckList).ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "Database Backup") {
		t.Errorf("check name not in body; snippet: %q", body[:min(400, len(body))])
	}
	if !strings.Contains(body, "uuid-1") {
		t.Errorf("check ID not in body; snippet: %q", body[:min(400, len(body))])
	}
	// Ping URL must be present.
	if !strings.Contains(body, "https://cronmon.example.com/ping/uuid-1") {
		t.Errorf("ping URL not in body; snippet: %q", body[:min(600, len(body))])
	}
}

func TestDashboardHandler_HandleCheckList_TagFilter(t *testing.T) {
	now := time.Now().UTC()
	checkA := model.Check{
		ID: "a1", Name: "Backup", Schedule: "0 2 * * *", Grace: 10,
		Status: model.StatusUp, Tags: "backup,production",
		CreatedAt: now, UpdatedAt: now,
	}
	checkB := model.Check{
		ID: "b2", Name: "Reports", Schedule: "0 6 * * *", Grace: 10,
		Status: model.StatusUp, Tags: "reports",
		CreatedAt: now, UpdatedAt: now,
	}
	checkC := model.Check{
		ID: "c3", Name: "No tags", Schedule: "*/5 * * * *", Grace: 5,
		Status:    model.StatusNew,
		CreatedAt: now, UpdatedAt: now,
	}

	h := newDashboardHandler(t, []model.Check{checkA, checkB, checkC}, nil, nil, nil, nil)

	t.Run("filter by backup tag shows only checkA", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/checks?tag=backup", nil)
		rec := httptest.NewRecorder()
		http.HandlerFunc(h.HandleCheckList).ServeHTTP(rec, req)

		body := rec.Body.String()
		if !strings.Contains(body, "Backup") {
			t.Error("expected 'Backup' check in filtered response")
		}
		if strings.Contains(body, "Reports") {
			t.Error("expected 'Reports' check to be filtered out")
		}
		if strings.Contains(body, "No tags") {
			t.Error("expected 'No tags' check to be filtered out")
		}
	})

	t.Run("empty tag shows all checks", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/checks", nil)
		rec := httptest.NewRecorder()
		http.HandlerFunc(h.HandleCheckList).ServeHTTP(rec, req)

		body := rec.Body.String()
		if !strings.Contains(body, "Backup") {
			t.Error("expected 'Backup' in unfiltered response")
		}
		if !strings.Contains(body, "Reports") {
			t.Error("expected 'Reports' in unfiltered response")
		}
		if !strings.Contains(body, "No tags") {
			t.Error("expected 'No tags' in unfiltered response")
		}
	})

	t.Run("tag filter bar contains all tags", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/checks", nil)
		rec := httptest.NewRecorder()
		http.HandlerFunc(h.HandleCheckList).ServeHTTP(rec, req)

		body := rec.Body.String()
		for _, tag := range []string{"backup", "production", "reports"} {
			if !strings.Contains(body, "tag="+tag) {
				t.Errorf("tag %q not in filter bar; snippet: %q", tag, body[:min(800, len(body))])
			}
		}
	})
}

func TestDashboardHandler_HandleCheckList_SortOrder(t *testing.T) {
	now := time.Now().UTC()
	down := model.Check{ID: "d1", Name: "Down Job", Schedule: "* * * * *", Grace: 1, Status: model.StatusDown, CreatedAt: now, UpdatedAt: now}
	up := model.Check{ID: "u1", Name: "Up Job", Schedule: "* * * * *", Grace: 1, Status: model.StatusUp, CreatedAt: now, UpdatedAt: now}
	newc := model.Check{ID: "n1", Name: "New Job", Schedule: "* * * * *", Grace: 1, Status: model.StatusNew, CreatedAt: now, UpdatedAt: now}
	paused := model.Check{ID: "p1", Name: "Paused Job", Schedule: "* * * * *", Grace: 1, Status: model.StatusPaused, CreatedAt: now, UpdatedAt: now}

	h := newDashboardHandler(t, []model.Check{newc, paused, up, down}, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/checks", nil)
	rec := httptest.NewRecorder()
	http.HandlerFunc(h.HandleCheckList).ServeHTTP(rec, req)

	body := rec.Body.String()
	// Verify down appears before up in the rendered HTML.
	downIdx := strings.Index(body, "Down Job")
	upIdx := strings.Index(body, "Up Job")
	newIdx := strings.Index(body, "New Job")
	pausedIdx := strings.Index(body, "Paused Job")

	if downIdx == -1 || upIdx == -1 || newIdx == -1 || pausedIdx == -1 {
		t.Fatal("one or more check names missing from body")
	}
	if downIdx > upIdx {
		t.Error("down check should appear before up check")
	}
	if upIdx > newIdx {
		t.Error("up check should appear before new check")
	}
	if newIdx > pausedIdx {
		t.Error("new check should appear before paused check")
	}
}

// ---------------------------------------------------------------------------
// TestDashboardHandler_HandleCheckDetail
// ---------------------------------------------------------------------------

func TestDashboardHandler_HandleCheckDetail_NotFound(t *testing.T) {
	h := newDashboardHandler(t, nil, nil, nil, nil, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /checks/{id}", h.HandleCheckDetail)

	req := httptest.NewRequest(http.MethodGet, "/checks/nonexistent", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestDashboardHandler_HandleCheckDetail_Renders(t *testing.T) {
	now := time.Now().UTC()
	check := model.Check{
		ID:        "detail-id",
		Name:      "Detail Check",
		Schedule:  "0 3 * * *",
		Grace:     15,
		Status:    model.StatusUp,
		CreatedAt: now,
		UpdatedAt: now,
	}

	pings := []*model.Ping{
		{ID: 1, CheckID: "detail-id", Type: model.PingSuccess, CreatedAt: now},
		{ID: 2, CheckID: "detail-id", Type: model.PingFail, CreatedAt: now.Add(-time.Hour)},
	}

	h := newDashboardHandler(t, []model.Check{check}, pings, nil, nil, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /checks/{id}", h.HandleCheckDetail)

	req := httptest.NewRequest(http.MethodGet, "/checks/detail-id", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Detail Check") {
		t.Error("check name not in response")
	}
	if !strings.Contains(body, "https://cronmon.example.com/ping/detail-id") {
		t.Error("ping URL not in response")
	}
	// Ping squares should be rendered.
	if !strings.Contains(body, "ping-square") {
		t.Error("ping squares not rendered")
	}
}

func TestDashboardHandler_HandleCheckDetail_NilPingHistory(t *testing.T) {
	// Zero pings — grid should be all 30 empty squares, no panic.
	now := time.Now().UTC()
	check := model.Check{
		ID:        "no-pings",
		Name:      "No Ping History",
		Schedule:  "*/10 * * * *",
		Grace:     5,
		Status:    model.StatusNew,
		CreatedAt: now,
		UpdatedAt: now,
	}
	h := newDashboardHandler(t, []model.Check{check}, nil, nil, nil, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /checks/{id}", h.HandleCheckDetail)

	req := httptest.NewRequest(http.MethodGet, "/checks/no-pings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	// 30 empty squares.
	count := strings.Count(body, "ping-square ping-empty")
	if count != 30 {
		t.Errorf("expected 30 empty ping squares, got %d", count)
	}
}

func TestDashboardHandler_HandleCheckDetail_WithChannels(t *testing.T) {
	now := time.Now().UTC()
	check := model.Check{
		ID: "ch-check", Name: "Channel Check", Schedule: "0 * * * *", Grace: 5,
		Status: model.StatusUp, CreatedAt: now, UpdatedAt: now,
	}
	ch := &model.Channel{ID: 1, Type: "email", Name: "Alert Email", Config: []byte(`{"address":"a@b.com"}`), CreatedAt: now}

	h := newDashboardHandler(t, []model.Check{check}, nil, []*model.Channel{ch}, nil, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /checks/{id}", h.HandleCheckDetail)

	req := httptest.NewRequest(http.MethodGet, "/checks/ch-check", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Alert Email") {
		t.Error("channel name not in response")
	}
}

func TestDashboardHandler_HandleCheckDetail_WithNotifications(t *testing.T) {
	now := time.Now().UTC()
	check := model.Check{
		ID: "notif-check", Name: "Notif Check", Schedule: "0 * * * *", Grace: 5,
		Status: model.StatusUp, CreatedAt: now, UpdatedAt: now,
	}
	errMsg := "connection refused"
	notifs := []*model.Notification{
		{ID: 1, CheckID: "notif-check", Type: model.AlertDown, SentAt: now, Error: &errMsg},
		{ID: 2, CheckID: "notif-check", Type: model.AlertUp, SentAt: now.Add(time.Minute), Error: nil},
	}
	h := newDashboardHandler(t, []model.Check{check}, nil, nil, nil, notifs)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /checks/{id}", h.HandleCheckDetail)

	req := httptest.NewRequest(http.MethodGet, "/checks/notif-check", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "connection refused") {
		t.Error("error message not in response")
	}
}

// ---------------------------------------------------------------------------
// TestDashboardHandler_HandleChannelList
// ---------------------------------------------------------------------------

func TestDashboardHandler_HandleChannelList_Empty(t *testing.T) {
	h := newDashboardHandler(t, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/channels", nil)
	rec := httptest.NewRecorder()
	http.HandlerFunc(h.HandleChannelList).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Channels") {
		t.Errorf("page title missing; snippet: %q", body[:min(300, len(body))])
	}
}

func TestDashboardHandler_HandleChannelList_WithChannels(t *testing.T) {
	now := time.Now().UTC()
	ch := &model.Channel{ID: 1, Type: "slack", Name: "Dev Slack", Config: []byte(`{"url":"https://hooks.slack.com/x"}`), CreatedAt: now}

	h := newDashboardHandler(t, nil, nil, []*model.Channel{ch}, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/channels", nil)
	rec := httptest.NewRecorder()
	http.HandlerFunc(h.HandleChannelList).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Dev Slack") {
		t.Errorf("channel name not in response; snippet: %q", body[:min(400, len(body))])
	}
	if !strings.Contains(body, "slack") {
		t.Error("channel type badge not in response")
	}
}

// ---------------------------------------------------------------------------
// TestNewDashboardHandler_TemplateParseError (invalid FS)
// ---------------------------------------------------------------------------

func TestNewDashboardHandler_Construction(t *testing.T) {
	// Verify that NewDashboardHandler succeeds with valid dependencies (real
	// web.FS is embedded at compile time). If templates fail to parse, the
	// test will fail at construction — this catches template syntax errors.
	sc, _ := makeCheckCache(t)
	pr := newSeededPingRepo()
	cr := newFakeChannelRepo()
	nr := newFakeNotifRepo()

	_, err := handler.NewDashboardHandler(sc, pr, cr, nr, "https://example.com")
	if err != nil {
		t.Fatalf("NewDashboardHandler returned unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// min helper (Go 1.21 has built-in min, but keep explicit for older compat)
// ---------------------------------------------------------------------------

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
