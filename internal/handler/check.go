package handler

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/myrrolinz/cronmon/internal/cache"
	"github.com/myrrolinz/cronmon/internal/model"
	"github.com/myrrolinz/cronmon/internal/schedule"
)

// CheckHandler handles CRUD form submissions for Check records.
//
//	POST /checks              → create check
//	POST /checks/{id}         → update check
//	POST /checks/{id}/delete  → delete check
//	POST /checks/{id}/pause   → toggle pause
type CheckHandler struct {
	cache *cache.StateCache
}

// NewCheckHandler creates a CheckHandler backed by the given cache.
func NewCheckHandler(sc *cache.StateCache) *CheckHandler {
	return &CheckHandler{cache: sc}
}

// HandleCreate handles POST /checks.
// Validates the submitted form, creates a new Check with a fresh UUIDv4, and
// redirects to the check detail page.
func (h *CheckHandler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	name, scheduleExpr, grace, tags, notifyOnFail, ok := parseCheckForm(w, r)
	if !ok {
		return
	}

	now := time.Now().UTC()
	next, err := schedule.NextExpectedAt(scheduleExpr, grace, now)
	if err != nil {
		slog.Error("check: failed to compute next_expected_at", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	id := uuid.New().String()
	check := &model.Check{
		ID:             id,
		Name:           name,
		Slug:           nil, // reserved for future human-readable URLs; unused in v1
		Schedule:       scheduleExpr,
		Grace:          grace,
		Status:         model.StatusNew,
		LastPingAt:     nil,
		NextExpectedAt: &next,
		CreatedAt:      now,
		UpdatedAt:      now,
		Tags:           tags,
		NotifyOnFail:   notifyOnFail,
	}

	if err := h.cache.Create(r.Context(), check); err != nil {
		slog.Error("check: failed to create", "err", err) //nolint:gosec
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/checks/"+id, http.StatusSeeOther)
}

// HandleUpdate handles POST /checks/{id}.
// Updates the mutable fields of an existing check and recomputes
// next_expected_at relative to the last ping time (or now if never pinged).
func (h *CheckHandler) HandleUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	check := h.cache.Get(id)
	if check == nil {
		http.NotFound(w, r)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	name, scheduleExpr, grace, tags, notifyOnFail, ok := parseCheckForm(w, r)
	if !ok {
		return
	}

	// Recompute the deadline relative to the last known ping time; fall back
	// to now if the check has never received a ping.
	now := time.Now().UTC()
	ref := now
	if check.LastPingAt != nil {
		ref = *check.LastPingAt
	}
	next, err := schedule.NextExpectedAt(scheduleExpr, grace, ref)
	if err != nil {
		slog.Error("check: failed to compute next_expected_at", //nolint:gosec
			"check_id", logSafe(id), "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	check.Name = name
	check.Schedule = scheduleExpr
	check.Grace = grace
	check.Tags = tags
	check.NotifyOnFail = notifyOnFail
	check.NextExpectedAt = &next
	check.UpdatedAt = now

	if err := h.cache.Set(r.Context(), check); err != nil {
		slog.Error("check: failed to update", "check_id", logSafe(id), "err", err) //nolint:gosec
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/checks/"+id, http.StatusSeeOther)
}

// HandleDelete handles POST /checks/{id}/delete.
// Removes the check from both the cache and the database. If the check does
// not exist the delete is still treated as a success (idempotent).
func (h *CheckHandler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if err := h.cache.Delete(r.Context(), id); err != nil {
		slog.Error("check: failed to delete", "check_id", logSafe(id), "err", err) //nolint:gosec
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/checks", http.StatusSeeOther)
}

// HandlePause handles POST /checks/{id}/pause.
// Toggles the check status between "paused" and its effective pre-pause state:
//   - Unpausing a check that has received at least one ping → "up"
//   - Unpausing a check that has never received a ping      → "new"
func (h *CheckHandler) HandlePause(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	check := h.cache.Get(id)
	if check == nil {
		http.NotFound(w, r)
		return
	}

	now := time.Now().UTC()

	if check.Status == model.StatusPaused {
		// Restore to the logical pre-pause state.
		if check.LastPingAt != nil {
			check.Status = model.StatusUp
		} else {
			check.Status = model.StatusNew
		}
	} else {
		check.Status = model.StatusPaused
	}
	check.UpdatedAt = now

	if err := h.cache.Set(r.Context(), check); err != nil {
		slog.Error("check: failed to pause/unpause", "check_id", logSafe(id), "err", err) //nolint:gosec
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/checks/"+id, http.StatusSeeOther)
}

// parseCheckForm extracts and validates the shared form fields used by both
// create and update. It writes an appropriate error response and returns
// ok=false when validation fails so the caller can return immediately.
func parseCheckForm(w http.ResponseWriter, r *http.Request) (name, scheduleExpr string, grace int, tags string, notifyOnFail bool, ok bool) {
	name = r.FormValue("name")
	scheduleExpr = r.FormValue("schedule")
	graceStr := r.FormValue("grace")
	tags = r.FormValue("tags")
	notifyOnFail = r.FormValue("notify_on_fail") == "on"

	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	if err := schedule.Validate(scheduleExpr); err != nil {
		http.Error(w, fmt.Sprintf("invalid schedule: %s", err), http.StatusBadRequest)
		return
	}

	var err error
	grace, err = strconv.Atoi(graceStr)
	if err != nil || grace < 1 {
		http.Error(w, "grace must be an integer >= 1", http.StatusBadRequest)
		return
	}

	ok = true
	return
}
