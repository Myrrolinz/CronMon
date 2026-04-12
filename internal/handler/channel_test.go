package handler_test

// Unit tests for ChannelHandler.
//
// These tests use a fakeChannelRepo (distinct from the minimal mockChannelRepo
// used by PingHandler tests) that tracks full state so we can assert on
// created/deleted channels and attach/detach operations.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/myrrolinz/cronmon/internal/handler"
	"github.com/myrrolinz/cronmon/internal/model"
	"github.com/myrrolinz/cronmon/internal/repository"
)

// ---------------------------------------------------------------------------
// fakeChannelRepo — stateful in-memory ChannelRepository for channel tests
// ---------------------------------------------------------------------------

type fakeChannelRepo struct {
	mu       sync.Mutex
	channels map[int64]*model.Channel
	nextID   int64
	attached map[string]map[int64]bool // checkID → set of channelIDs
}

func newFakeChannelRepo() *fakeChannelRepo {
	return &fakeChannelRepo{
		channels: make(map[int64]*model.Channel),
		nextID:   1,
		attached: make(map[string]map[int64]bool),
	}
}

func (f *fakeChannelRepo) Create(_ context.Context, ch *model.Channel) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *ch
	cp.ID = f.nextID
	f.nextID++
	f.channels[cp.ID] = &cp
	ch.ID = cp.ID
	return nil
}

func (f *fakeChannelRepo) GetByID(_ context.Context, id int64) (*model.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch, ok := f.channels[id]
	if !ok {
		return nil, fmt.Errorf("fakeChannelRepo.GetByID %d: %w", id, repository.ErrNotFound)
	}
	cp := *ch
	return &cp, nil
}

func (f *fakeChannelRepo) ListAll(_ context.Context) ([]*model.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*model.Channel, 0, len(f.channels))
	for _, ch := range f.channels {
		cp := *ch
		out = append(out, &cp)
	}
	return out, nil
}

func (f *fakeChannelRepo) Delete(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.channels[id]; !ok {
		return fmt.Errorf("fakeChannelRepo.Delete %d: %w", id, repository.ErrNotFound)
	}
	delete(f.channels, id)
	return nil
}

func (f *fakeChannelRepo) ListByCheckID(_ context.Context, checkID string) ([]*model.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := f.attached[checkID]
	out := make([]*model.Channel, 0, len(ids))
	for id := range ids {
		if ch, ok := f.channels[id]; ok {
			cp := *ch
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (f *fakeChannelRepo) AttachToCheck(_ context.Context, checkID string, channelID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.channels[channelID]; !ok {
		return fmt.Errorf("fakeChannelRepo.AttachToCheck %d: %w", channelID, repository.ErrNotFound)
	}
	if f.attached[checkID] == nil {
		f.attached[checkID] = make(map[int64]bool)
	}
	f.attached[checkID][channelID] = true
	return nil
}

func (f *fakeChannelRepo) DetachFromCheck(_ context.Context, checkID string, channelID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.attached[checkID] != nil {
		delete(f.attached[checkID], channelID)
	}
	return nil
}

// count returns the number of channels in the repo.
func (f *fakeChannelRepo) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.channels)
}

// isAttached reports whether channelID is currently attached to checkID.
func (f *fakeChannelRepo) isAttached(checkID string, channelID int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attached[checkID][channelID]
}

// seed inserts a pre-existing channel (used for delete / attach tests).
func (f *fakeChannelRepo) seed(ch model.Channel) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := ch
	if cp.ID == 0 {
		cp.ID = f.nextID
		f.nextID++
	}
	if f.nextID <= cp.ID {
		f.nextID = cp.ID + 1
	}
	f.channels[cp.ID] = &cp
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// makeChannelHandlerWithChecks creates a ChannelHandler with access to a
// fakeChannelRepo and a StateCache pre-seeded with the supplied checks.
func makeChannelHandlerWithChecks(t *testing.T, checks ...model.Check) (*handler.ChannelHandler, *fakeChannelRepo) {
	t.Helper()
	repo := newFakeChannelRepo()
	sc, _ := makeCheckCache(t, checks...)
	return handler.NewChannelHandler(repo, sc), repo
}

// newChannel returns a minimal Channel used to seed the fake repo.
func newChannel(id int64, channelType, name string) model.Channel {
	return model.Channel{
		ID:        id,
		Type:      channelType,
		Name:      name,
		Config:    []byte(`{"address":"test@example.com"}`),
		CreatedAt: time.Now().UTC(),
	}
}

// ---------------------------------------------------------------------------
// HandleCreate tests
// ---------------------------------------------------------------------------

func TestChannelHandler_HandleCreate(t *testing.T) {
	tests := []struct {
		name       string
		form       url.Values
		wantStatus int
		wantCount  int
	}{
		// ── email ────────────────────────────────────────────────────────────
		{
			name:       "valid email config",
			form:       url.Values{"type": {"email"}, "name": {"Alerts"}, "config": {`{"address":"user@example.com"}`}},
			wantStatus: http.StatusSeeOther,
			wantCount:  1,
		},
		{
			name:       "email with display name",
			form:       url.Values{"type": {"email"}, "name": {"Alerts"}, "config": {`{"address":"User <user@example.com>"}`}},
			wantStatus: http.StatusSeeOther,
			wantCount:  1,
		},
		{
			name:       "invalid email address",
			form:       url.Values{"type": {"email"}, "name": {"Alerts"}, "config": {`{"address":"not-an-email"}`}},
			wantStatus: http.StatusBadRequest,
			wantCount:  0,
		},
		{
			name:       "email missing address field",
			form:       url.Values{"type": {"email"}, "name": {"Alerts"}, "config": {`{}`}},
			wantStatus: http.StatusBadRequest,
			wantCount:  0,
		},
		// ── slack ────────────────────────────────────────────────────────────
		{
			name:       "valid slack config",
			form:       url.Values{"type": {"slack"}, "name": {"Slack"}, "config": {`{"url":"https://hooks.slack.com/services/abc/def"}`}},
			wantStatus: http.StatusSeeOther,
			wantCount:  1,
		},
		{
			name:       "slack url wrong prefix",
			form:       url.Values{"type": {"slack"}, "name": {"Slack"}, "config": {`{"url":"https://example.com/hook"}`}},
			wantStatus: http.StatusBadRequest,
			wantCount:  0,
		},
		{
			name:       "slack url http scheme rejected",
			form:       url.Values{"type": {"slack"}, "name": {"Slack"}, "config": {`{"url":"http://hooks.slack.com/services/abc"}`}},
			wantStatus: http.StatusBadRequest,
			wantCount:  0,
		},
		{
			name:       "slack missing url field",
			form:       url.Values{"type": {"slack"}, "name": {"Slack"}, "config": {`{}`}},
			wantStatus: http.StatusBadRequest,
			wantCount:  0,
		},
		// ── webhook ──────────────────────────────────────────────────────────
		{
			name:       "valid webhook https config",
			form:       url.Values{"type": {"webhook"}, "name": {"Hook"}, "config": {`{"url":"https://example.com/webhook"}`}},
			wantStatus: http.StatusSeeOther,
			wantCount:  1,
		},
		{
			name:       "valid webhook http config",
			form:       url.Values{"type": {"webhook"}, "name": {"Hook"}, "config": {`{"url":"http://example.com/webhook"}`}},
			wantStatus: http.StatusSeeOther,
			wantCount:  1,
		},
		{
			name:       "webhook private IP (RFC 1918) rejected",
			form:       url.Values{"type": {"webhook"}, "name": {"Hook"}, "config": {`{"url":"http://192.168.1.1/hook"}`}},
			wantStatus: http.StatusBadRequest,
			wantCount:  0,
		},
		{
			name:       "webhook 10.x.x.x range rejected",
			form:       url.Values{"type": {"webhook"}, "name": {"Hook"}, "config": {`{"url":"https://10.0.0.1/hook"}`}},
			wantStatus: http.StatusBadRequest,
			wantCount:  0,
		},
		{
			name:       "webhook loopback IP rejected",
			form:       url.Values{"type": {"webhook"}, "name": {"Hook"}, "config": {`{"url":"http://127.0.0.1/hook"}`}},
			wantStatus: http.StatusBadRequest,
			wantCount:  0,
		},
		{
			name:       "webhook 172.16.x.x range rejected",
			form:       url.Values{"type": {"webhook"}, "name": {"Hook"}, "config": {`{"url":"http://172.16.0.1/hook"}`}},
			wantStatus: http.StatusBadRequest,
			wantCount:  0,
		},
		{
			name:       "webhook ftp scheme rejected",
			form:       url.Values{"type": {"webhook"}, "name": {"Hook"}, "config": {`{"url":"ftp://example.com/hook"}`}},
			wantStatus: http.StatusBadRequest,
			wantCount:  0,
		},
		{
			name:       "webhook missing url field",
			form:       url.Values{"type": {"webhook"}, "name": {"Hook"}, "config": {`{}`}},
			wantStatus: http.StatusBadRequest,
			wantCount:  0,
		},
		// ── general ──────────────────────────────────────────────────────────
		{
			name:       "unknown channel type",
			form:       url.Values{"type": {"sms"}, "name": {"SMS"}, "config": {`{}`}},
			wantStatus: http.StatusBadRequest,
			wantCount:  0,
		},
		{
			name:       "missing name field",
			form:       url.Values{"type": {"email"}, "name": {""}, "config": {`{"address":"user@example.com"}`}},
			wantStatus: http.StatusBadRequest,
			wantCount:  0,
		},
		{
			name:       "invalid JSON config",
			form:       url.Values{"type": {"email"}, "name": {"Alerts"}, "config": {`not-json`}},
			wantStatus: http.StatusBadRequest,
			wantCount:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, repo := makeChannelHandlerWithChecks(t)
			mux := http.NewServeMux()
			mux.HandleFunc("POST /channels", h.HandleCreate)

			rec := postForm(t, mux, "/channels", tt.form)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d; body: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus == http.StatusSeeOther {
				if got := rec.Header().Get("Location"); got != "/channels" {
					t.Errorf("location = %q, want /channels", got)
				}
			}
			if got := repo.count(); got != tt.wantCount {
				t.Errorf("channel count = %d, want %d", got, tt.wantCount)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// HandleDelete tests
// ---------------------------------------------------------------------------

func TestChannelHandler_HandleDelete(t *testing.T) {
	t.Run("existing channel deleted and redirected", func(t *testing.T) {
		h, repo := makeChannelHandlerWithChecks(t)
		repo.seed(newChannel(1, "email", "Alerts"))

		mux := http.NewServeMux()
		mux.HandleFunc("POST /channels/{id}/delete", h.HandleDelete)

		rec := postForm(t, mux, "/channels/1/delete", url.Values{})

		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusSeeOther)
		}
		if got := rec.Header().Get("Location"); got != "/channels" {
			t.Errorf("location = %q, want /channels", got)
		}
		if repo.count() != 0 {
			t.Error("channel should have been deleted")
		}
	})

	t.Run("non-existent channel redirects (idempotent)", func(t *testing.T) {
		h, _ := makeChannelHandlerWithChecks(t)

		mux := http.NewServeMux()
		mux.HandleFunc("POST /channels/{id}/delete", h.HandleDelete)

		rec := postForm(t, mux, "/channels/999/delete", url.Values{})

		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusSeeOther)
		}
		if got := rec.Header().Get("Location"); got != "/channels" {
			t.Errorf("location = %q, want /channels", got)
		}
	})

	t.Run("non-numeric id returns 400", func(t *testing.T) {
		h, _ := makeChannelHandlerWithChecks(t)

		mux := http.NewServeMux()
		mux.HandleFunc("POST /channels/{id}/delete", h.HandleDelete)

		rec := postForm(t, mux, "/channels/abc/delete", url.Values{})

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})
}

// ---------------------------------------------------------------------------
// HandleAttachDetach tests
// ---------------------------------------------------------------------------

func TestChannelHandler_HandleAttachDetach(t *testing.T) {
	const checkID = "aaaaaaaa-0000-0000-0000-000000000001"

	seedCheck := newCheck(checkID, model.StatusUp)

	t.Run("attach channel to check", func(t *testing.T) {
		h, repo := makeChannelHandlerWithChecks(t, seedCheck)
		repo.seed(newChannel(1, "email", "Alerts"))

		mux := http.NewServeMux()
		mux.HandleFunc("POST /checks/{id}/channels", h.HandleAttachDetach)

		rec := postForm(t, mux, "/checks/"+checkID+"/channels", url.Values{
			"channel_ids": {"1"},
		})

		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusSeeOther, rec.Body.String())
		}
		if got := rec.Header().Get("Location"); got != "/checks/"+checkID {
			t.Errorf("location = %q, want /checks/%s", got, checkID)
		}
		if !repo.isAttached(checkID, 1) {
			t.Error("channel 1 should be attached to check")
		}
	})

	t.Run("detach channel from check", func(t *testing.T) {
		h, repo := makeChannelHandlerWithChecks(t, seedCheck)
		repo.seed(newChannel(1, "email", "Alerts"))
		// Pre-attach the channel.
		if err := repo.AttachToCheck(context.Background(), checkID, 1); err != nil {
			t.Fatalf("setup: AttachToCheck: %v", err)
		}

		mux := http.NewServeMux()
		mux.HandleFunc("POST /checks/{id}/channels", h.HandleAttachDetach)

		// Submit form with no channel_ids → detaches channel 1.
		rec := postForm(t, mux, "/checks/"+checkID+"/channels", url.Values{})

		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusSeeOther)
		}
		if repo.isAttached(checkID, 1) {
			t.Error("channel 1 should have been detached from check")
		}
	})

	t.Run("attach one detach another", func(t *testing.T) {
		h, repo := makeChannelHandlerWithChecks(t, seedCheck)
		repo.seed(newChannel(1, "email", "Email"))
		repo.seed(newChannel(2, "slack", "Slack"))
		// Channel 1 is currently attached; we want to swap to channel 2.
		if err := repo.AttachToCheck(context.Background(), checkID, 1); err != nil {
			t.Fatalf("setup: AttachToCheck: %v", err)
		}

		mux := http.NewServeMux()
		mux.HandleFunc("POST /checks/{id}/channels", h.HandleAttachDetach)

		rec := postForm(t, mux, "/checks/"+checkID+"/channels", url.Values{
			"channel_ids": {"2"},
		})

		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusSeeOther)
		}
		if repo.isAttached(checkID, 1) {
			t.Error("channel 1 should have been detached")
		}
		if !repo.isAttached(checkID, 2) {
			t.Error("channel 2 should have been attached")
		}
	})

	t.Run("unknown check ID returns 404", func(t *testing.T) {
		h, _ := makeChannelHandlerWithChecks(t) // no checks seeded

		mux := http.NewServeMux()
		mux.HandleFunc("POST /checks/{id}/channels", h.HandleAttachDetach)

		rec := postForm(t, mux, "/checks/nonexistent-id/channels", url.Values{
			"channel_ids": {"1"},
		})

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})

	t.Run("stale or fabricated numeric channel ID is silently ignored", func(t *testing.T) {
		h, repo := makeChannelHandlerWithChecks(t, seedCheck)
		// No channel seeded — ID 999 doesn't exist.

		mux := http.NewServeMux()
		mux.HandleFunc("POST /checks/{id}/channels", h.HandleAttachDetach)

		rec := postForm(t, mux, "/checks/"+checkID+"/channels", url.Values{
			"channel_ids": {"999"},
		})

		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusSeeOther, rec.Body.String())
		}
		attached, err := repo.ListByCheckID(context.Background(), checkID)
		if err != nil {
			t.Fatalf("ListByCheckID: %v", err)
		}
		if len(attached) != 0 {
			t.Errorf("attached count = %d, want 0 (stale ID should be silently dropped)", len(attached))
		}
	})

	t.Run("invalid channel_id values are silently ignored", func(t *testing.T) {
		h, repo := makeChannelHandlerWithChecks(t, seedCheck)

		mux := http.NewServeMux()
		mux.HandleFunc("POST /checks/{id}/channels", h.HandleAttachDetach)

		rec := postForm(t, mux, "/checks/"+checkID+"/channels", url.Values{
			"channel_ids": {"not-a-number"},
		})

		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusSeeOther)
		}
		// Invalid IDs are skipped; nothing gets attached.
		ctx := context.Background()
		attached, err := repo.ListByCheckID(ctx, checkID)
		if err != nil {
			t.Fatalf("ListByCheckID: %v", err)
		}
		if len(attached) != 0 {
			t.Errorf("attached count = %d, want 0", len(attached))
		}
	})
}

// ---------------------------------------------------------------------------
// HandleList tests
// ---------------------------------------------------------------------------

func TestChannelHandler_HandleList(t *testing.T) {
	t.Run("returns 200", func(t *testing.T) {
		h, repo := makeChannelHandlerWithChecks(t)
		repo.seed(newChannel(1, "email", "Alerts"))

		mux := http.NewServeMux()
		mux.HandleFunc("GET /channels", h.HandleList)

		req := httptest.NewRequest(http.MethodGet, "/channels", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
		}
	})
}
