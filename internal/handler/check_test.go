package handler_test

// Unit tests for CheckHandler using a mock CheckRepository so no database
// is needed. Tests cover CRUD operations, validation, redirect behaviour,
// and pause toggling.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/myrrolinz/cronmon/internal/cache"
	"github.com/myrrolinz/cronmon/internal/handler"
	"github.com/myrrolinz/cronmon/internal/model"
)

// ---------------------------------------------------------------------------
// helpers re-used from ping_test.go (both files are in package handler_test)
// ---------------------------------------------------------------------------

// makeCheckCache seeds a StateCache with the provided checks.
func makeCheckCache(t *testing.T, checks ...model.Check) (*cache.StateCache, *mockCheckRepo) {
	t.Helper()
	return makeCache(t, checks...)
}

// postForm sends a POST request with the given url.Values and returns the recorder.
func postForm(t *testing.T, h http.Handler, path string, vals url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(vals.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// newCheck returns a minimal valid Check for use in tests.
func newCheck(id string, status model.Status) model.Check {
	return model.Check{
		ID:        id,
		Name:      "Test Job",
		Schedule:  "0 2 * * *",
		Grace:     10,
		Status:    status,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
}

// ---------------------------------------------------------------------------
// HandleCreate tests
// ---------------------------------------------------------------------------

func TestCheckHandler_Create_Redirect(t *testing.T) {
	sc, _ := makeCheckCache(t)
	h := handler.NewCheckHandler(sc)

	w := postForm(t, http.HandlerFunc(h.HandleCreate), "/checks", url.Values{
		"name":     {"Database backup"},
		"schedule": {"0 2 * * *"},
		"grace":    {"10"},
	})

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected 303 got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/checks/") {
		t.Errorf("expected redirect to /checks/{id}, got %q", loc)
	}
}

func TestCheckHandler_Create_InvalidSchedule(t *testing.T) {
	sc, _ := makeCheckCache(t)
	h := handler.NewCheckHandler(sc)

	w := postForm(t, http.HandlerFunc(h.HandleCreate), "/checks", url.Values{
		"name":     {"Job"},
		"schedule": {"not-a-cron"},
		"grace":    {"5"},
	})

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 got %d", w.Code)
	}
}

func TestCheckHandler_Create_EmptyName(t *testing.T) {
	sc, _ := makeCheckCache(t)
	h := handler.NewCheckHandler(sc)

	w := postForm(t, http.HandlerFunc(h.HandleCreate), "/checks", url.Values{
		"name":     {""},
		"schedule": {"0 2 * * *"},
		"grace":    {"10"},
	})

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 got %d", w.Code)
	}
}

func TestCheckHandler_Create_GraceTooLow(t *testing.T) {
	sc, _ := makeCheckCache(t)
	h := handler.NewCheckHandler(sc)

	w := postForm(t, http.HandlerFunc(h.HandleCreate), "/checks", url.Values{
		"name":     {"Job"},
		"schedule": {"0 2 * * *"},
		"grace":    {"0"},
	})

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 got %d", w.Code)
	}
}

func TestCheckHandler_Create_GraceNotAnInteger(t *testing.T) {
	sc, _ := makeCheckCache(t)
	h := handler.NewCheckHandler(sc)

	w := postForm(t, http.HandlerFunc(h.HandleCreate), "/checks", url.Values{
		"name":     {"Job"},
		"schedule": {"0 2 * * *"},
		"grace":    {"abc"},
	})

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 got %d", w.Code)
	}
}

func TestCheckHandler_Create_StoresInCache(t *testing.T) {
	sc, repo := makeCheckCache(t)
	h := handler.NewCheckHandler(sc)

	postForm(t, http.HandlerFunc(h.HandleCreate), "/checks", url.Values{
		"name":           {"Backup"},
		"schedule":       {"*/5 * * * *"},
		"grace":          {"3"},
		"tags":           {"prod,db"},
		"notify_on_fail": {"on"},
	})

	// Verify the newly created check was written to the mock repo.
	all, err := repo.ListAll(context.Background())
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 check, got %d", len(all))
	}
	c := all[0]
	if c.Name != "Backup" {
		t.Errorf("Name: got %q want %q", c.Name, "Backup")
	}
	if c.Schedule != "*/5 * * * *" {
		t.Errorf("Schedule: got %q", c.Schedule)
	}
	if c.Grace != 3 {
		t.Errorf("Grace: got %d", c.Grace)
	}
	if c.Tags != "prod,db" {
		t.Errorf("Tags: got %q", c.Tags)
	}
	if !c.NotifyOnFail {
		t.Error("NotifyOnFail: expected true")
	}
	if c.Status != model.StatusNew {
		t.Errorf("Status: got %q want %q", c.Status, model.StatusNew)
	}
}

// ---------------------------------------------------------------------------
// HandleUpdate tests
// ---------------------------------------------------------------------------

func TestCheckHandler_Update_Redirect(t *testing.T) {
	const id = "aaaaaaaa-0000-0000-0000-000000000001"
	sc, _ := makeCheckCache(t, newCheck(id, model.StatusUp))
	h := handler.NewCheckHandler(sc)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /checks/{id}", h.HandleUpdate)

	w := postForm(t, mux, "/checks/"+id, url.Values{
		"name":     {"Renamed"},
		"schedule": {"0 3 * * *"},
		"grace":    {"5"},
	})

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected 303 got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/checks/"+id {
		t.Errorf("expected redirect to /checks/%s, got %q", id, loc)
	}
}

func TestCheckHandler_Update_NotFound(t *testing.T) {
	sc, _ := makeCheckCache(t)
	h := handler.NewCheckHandler(sc)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /checks/{id}", h.HandleUpdate)

	w := postForm(t, mux, "/checks/nonexistent", url.Values{
		"name":     {"Job"},
		"schedule": {"0 2 * * *"},
		"grace":    {"5"},
	})

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 got %d", w.Code)
	}
}

func TestCheckHandler_Update_InvalidSchedule(t *testing.T) {
	const id = "aaaaaaaa-0000-0000-0000-000000000002"
	sc, _ := makeCheckCache(t, newCheck(id, model.StatusUp))
	h := handler.NewCheckHandler(sc)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /checks/{id}", h.HandleUpdate)

	w := postForm(t, mux, "/checks/"+id, url.Values{
		"name":     {"Job"},
		"schedule": {"not-valid"},
		"grace":    {"5"},
	})

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 got %d", w.Code)
	}
}

func TestCheckHandler_Update_UpdatesFields(t *testing.T) {
	const id = "aaaaaaaa-0000-0000-0000-000000000003"
	sc, repo := makeCheckCache(t, newCheck(id, model.StatusUp))
	h := handler.NewCheckHandler(sc)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /checks/{id}", h.HandleUpdate)

	postForm(t, mux, "/checks/"+id, url.Values{
		"name":           {"Updated"},
		"schedule":       {"*/10 * * * *"},
		"grace":          {"7"},
		"tags":           {"staging"},
		"notify_on_fail": {"on"},
	})

	c := repo.get(id)
	if c == nil {
		t.Fatal("check not found in repo")
	}
	if c.Name != "Updated" {
		t.Errorf("Name: got %q want %q", c.Name, "Updated")
	}
	if c.Schedule != "*/10 * * * *" {
		t.Errorf("Schedule: got %q", c.Schedule)
	}
	if c.Grace != 7 {
		t.Errorf("Grace: got %d want 7", c.Grace)
	}
	if c.Tags != "staging" {
		t.Errorf("Tags: got %q", c.Tags)
	}
	if !c.NotifyOnFail {
		t.Error("NotifyOnFail: expected true")
	}
}

// ---------------------------------------------------------------------------
// HandleDelete tests
// ---------------------------------------------------------------------------

func TestCheckHandler_Delete_Redirect(t *testing.T) {
	const id = "aaaaaaaa-0000-0000-0000-000000000004"
	sc, _ := makeCheckCache(t, newCheck(id, model.StatusUp))
	h := handler.NewCheckHandler(sc)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /checks/{id}/delete", h.HandleDelete)

	w := postForm(t, mux, "/checks/"+id+"/delete", url.Values{})

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected 303 got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/checks" {
		t.Errorf("expected redirect to /checks, got %q", loc)
	}
}

func TestCheckHandler_Delete_RemovesFromCache(t *testing.T) {
	const id = "aaaaaaaa-0000-0000-0000-000000000005"
	sc, repo := makeCheckCache(t, newCheck(id, model.StatusUp))
	h := handler.NewCheckHandler(sc)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /checks/{id}/delete", h.HandleDelete)

	postForm(t, mux, "/checks/"+id+"/delete", url.Values{})

	if c := repo.get(id); c != nil {
		t.Error("check still present in repo after delete")
	}
	if c := sc.Get(id); c != nil {
		t.Error("check still present in cache after delete")
	}
}

// ---------------------------------------------------------------------------
// HandlePause tests
// ---------------------------------------------------------------------------

func TestCheckHandler_Pause_UpTosPaused(t *testing.T) {
	const id = "aaaaaaaa-0000-0000-0000-000000000006"
	now := time.Now().UTC()
	sc, repo := makeCheckCache(t, model.Check{
		ID:         id,
		Name:       "Job",
		Schedule:   "0 2 * * *",
		Grace:      10,
		Status:     model.StatusUp,
		LastPingAt: &now,
		CreatedAt:  now,
		UpdatedAt:  now,
	})
	h := handler.NewCheckHandler(sc)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /checks/{id}/pause", h.HandlePause)

	w := postForm(t, mux, "/checks/"+id+"/pause", url.Values{})

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected 303 got %d", w.Code)
	}

	c := repo.get(id)
	if c.Status != model.StatusPaused {
		t.Errorf("Status: got %q want %q", c.Status, model.StatusPaused)
	}
}

func TestCheckHandler_Pause_PausedToUp(t *testing.T) {
	const id = "aaaaaaaa-0000-0000-0000-000000000007"
	now := time.Now().UTC()
	prePause := model.StatusUp
	sc, repo := makeCheckCache(t, model.Check{
		ID:             id,
		Name:           "Job",
		Schedule:       "0 2 * * *",
		Grace:          10,
		Status:         model.StatusPaused,
		PrePauseStatus: &prePause, // was "up" before being paused
		CreatedAt:      now,
		UpdatedAt:      now,
	})
	h := handler.NewCheckHandler(sc)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /checks/{id}/pause", h.HandlePause)

	postForm(t, mux, "/checks/"+id+"/pause", url.Values{})

	c := repo.get(id)
	if c.Status != model.StatusUp {
		t.Errorf("Status: got %q want %q", c.Status, model.StatusUp)
	}
	if c.PrePauseStatus != nil {
		t.Errorf("PrePauseStatus: expected nil after unpause, got %q", *c.PrePauseStatus)
	}
}

func TestCheckHandler_Pause_PausedToNew_WhenNeverPinged(t *testing.T) {
	const id = "aaaaaaaa-0000-0000-0000-000000000008"
	now := time.Now().UTC()
	sc, repo := makeCheckCache(t, model.Check{
		ID:         id,
		Name:       "Job",
		Schedule:   "0 2 * * *",
		Grace:      10,
		Status:     model.StatusPaused,
		LastPingAt: nil, // never received a ping
		CreatedAt:  now,
		UpdatedAt:  now,
	})
	h := handler.NewCheckHandler(sc)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /checks/{id}/pause", h.HandlePause)

	postForm(t, mux, "/checks/"+id+"/pause", url.Values{})

	c := repo.get(id)
	if c.Status != model.StatusNew {
		t.Errorf("Status: got %q want %q", c.Status, model.StatusNew)
	}
}

func TestCheckHandler_Pause_DownToPaused(t *testing.T) {
	const id = "aaaaaaaa-0000-0000-0000-000000000009"
	now := time.Now().UTC()
	sc, repo := makeCheckCache(t, model.Check{
		ID:         id,
		Name:       "Job",
		Schedule:   "0 2 * * *",
		Grace:      10,
		Status:     model.StatusDown,
		LastPingAt: &now,
		CreatedAt:  now,
		UpdatedAt:  now,
	})
	h := handler.NewCheckHandler(sc)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /checks/{id}/pause", h.HandlePause)

	postForm(t, mux, "/checks/"+id+"/pause", url.Values{})

	c := repo.get(id)
	if c.Status != model.StatusPaused {
		t.Errorf("Status: got %q want %q", c.Status, model.StatusPaused)
	}
	if c.PrePauseStatus == nil || *c.PrePauseStatus != model.StatusDown {
		t.Errorf("PrePauseStatus: expected %q, got %v", model.StatusDown, c.PrePauseStatus)
	}
}

// TestCheckHandler_Pause_DownPausedUnpauseRestoresDown verifies that a check
// that was "down" before being paused returns to "down" on unpause, not "up".
// This guards against the bug where unpausing would incorrectly show a down
// check as up, hiding an ongoing outage.
func TestCheckHandler_Pause_DownPausedUnpauseRestoresDown(t *testing.T) {
	const id = "aaaaaaaa-0000-0000-0000-00000000000a"
	now := time.Now().UTC()
	prePause := model.StatusDown
	sc, repo := makeCheckCache(t, model.Check{
		ID:             id,
		Name:           "Job",
		Schedule:       "0 2 * * *",
		Grace:          10,
		Status:         model.StatusPaused,
		PrePauseStatus: &prePause,
		LastPingAt:     &now,
		CreatedAt:      now,
		UpdatedAt:      now,
	})
	h := handler.NewCheckHandler(sc)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /checks/{id}/pause", h.HandlePause)

	postForm(t, mux, "/checks/"+id+"/pause", url.Values{})

	c := repo.get(id)
	if c.Status != model.StatusDown {
		t.Errorf("Status: got %q want %q (pre-pause state must be preserved)", c.Status, model.StatusDown)
	}
	if c.PrePauseStatus != nil {
		t.Errorf("PrePauseStatus: expected nil after unpause, got %q", *c.PrePauseStatus)
	}
}

func TestCheckHandler_Pause_NotFound(t *testing.T) {
	sc, _ := makeCheckCache(t)
	h := handler.NewCheckHandler(sc)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /checks/{id}/pause", h.HandlePause)

	w := postForm(t, mux, "/checks/nonexistent/pause", url.Values{})

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 got %d", w.Code)
	}
}
