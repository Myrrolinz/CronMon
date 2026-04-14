package handler

import (
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/myrrolinz/cronmon/internal/cache"
	"github.com/myrrolinz/cronmon/internal/model"
	"github.com/myrrolinz/cronmon/internal/repository"
	"github.com/myrrolinz/cronmon/web"
)

// ---------------------------------------------------------------------------
// View models
// ---------------------------------------------------------------------------

// CheckRow is the per-row view model used by the dashboard check list.
type CheckRow struct {
	ID             string
	Name           string
	Status         model.Status
	Schedule       string
	LastPingAt     *time.Time
	NextExpectedAt *time.Time
	TagList        []string // pre-split from Check.Tags
	PingURL        string
}

// PingSquare represents one coloured cell in the 30-ping history strip.
type PingSquare struct {
	Class string // "success" | "fail" | "start" | "empty"
	Title string // tooltip text (timestamp or "no data")
}

// DashboardData is the template data for GET /checks.
type DashboardData struct {
	Tag     string     // current filter tag (empty = show all)
	AllTags []string   // deduplicated tags across all checks (for filter bar)
	Checks  []CheckRow // sorted and filtered check rows
}

// CheckDetailData is the template data for GET /checks/{id}.
type CheckDetailData struct {
	Check              *model.Check
	PingURL            string
	PingSquares        []PingSquare
	AllChannels        []*model.Channel // all channels in the system (for attach form)
	AttachedChannelIDs map[int64]bool   // set of IDs currently attached to this check
	Notifications      []*model.Notification
}

// ChannelsData is the template data for GET /channels.
type ChannelsData struct {
	Channels []*model.Channel
}

// ---------------------------------------------------------------------------
// DashboardHandler
// ---------------------------------------------------------------------------

// DashboardHandler serves the three primary read-only dashboard views:
//
//	GET /          → redirect to /checks
//	GET /checks    → check list (dashboard)
//	GET /checks/{id} → check detail
//	GET /channels  → channel list
type DashboardHandler struct {
	cache       *cache.StateCache
	pingRepo    repository.PingRepository
	channelRepo repository.ChannelRepository
	notifRepo   repository.NotificationRepository
	baseURL     string // e.g. "https://cronmon.example.com" — no trailing slash

	tmplDashboard *template.Template
	tmplDetail    *template.Template
	tmplChannels  *template.Template
}

// NewDashboardHandler parses all templates from the embedded web.FS and
// returns a ready-to-use DashboardHandler. An error is returned if any
// template fails to parse; callers should treat this as a fatal startup error.
func NewDashboardHandler(
	sc *cache.StateCache,
	pingRepo repository.PingRepository,
	channelRepo repository.ChannelRepository,
	notifRepo repository.NotificationRepository,
	baseURL string,
) (*DashboardHandler, error) {
	funcs := templateFuncs()

	dashTmpl, err := template.New("layout").Funcs(funcs).ParseFS(
		web.FS, "templates/layout.html", "templates/dashboard.html",
	)
	if err != nil {
		return nil, fmt.Errorf("dashboard handler: parse dashboard templates: %w", err)
	}

	detailTmpl, err := template.New("layout").Funcs(funcs).ParseFS(
		web.FS, "templates/layout.html", "templates/check_detail.html",
	)
	if err != nil {
		return nil, fmt.Errorf("dashboard handler: parse detail templates: %w", err)
	}

	channelsTmpl, err := template.New("layout").Funcs(funcs).ParseFS(
		web.FS, "templates/layout.html", "templates/channels.html",
	)
	if err != nil {
		return nil, fmt.Errorf("dashboard handler: parse channels templates: %w", err)
	}

	return &DashboardHandler{
		cache:         sc,
		pingRepo:      pingRepo,
		channelRepo:   channelRepo,
		notifRepo:     notifRepo,
		baseURL:       strings.TrimRight(baseURL, "/"),
		tmplDashboard: dashTmpl,
		tmplDetail:    detailTmpl,
		tmplChannels:  channelsTmpl,
	}, nil
}

// ---------------------------------------------------------------------------
// Route handlers
// ---------------------------------------------------------------------------

// HandleIndex handles GET /: redirects to /checks.
func (h *DashboardHandler) HandleIndex(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/checks", http.StatusSeeOther)
}

// HandleCheckList handles GET /checks.
// Reads all checks from the in-memory cache, sorts them by status priority
// (down → up → new → paused), and optionally filters by the ?tag= query
// parameter.
func (h *DashboardHandler) HandleCheckList(w http.ResponseWriter, r *http.Request) {
	tagFilter := r.URL.Query().Get("tag")

	checks := h.cache.Snapshot()

	// Collect all tags across every check for the filter bar.
	tagSet := make(map[string]struct{})
	for _, c := range checks {
		for _, t := range splitTags(c.Tags) {
			tagSet[t] = struct{}{}
		}
	}
	allTags := make([]string, 0, len(tagSet))
	for t := range tagSet {
		allTags = append(allTags, t)
	}
	sort.Strings(allTags)

	// Apply tag filter.
	var filtered []*model.Check
	for _, c := range checks {
		if tagFilter == "" || containsTag(c.Tags, tagFilter) {
			filtered = append(filtered, c)
		}
	}

	// Sort: down < up < new < paused.
	sort.Slice(filtered, func(i, j int) bool {
		return statusOrder(filtered[i].Status) < statusOrder(filtered[j].Status)
	})

	rows := make([]CheckRow, 0, len(filtered))
	for _, c := range filtered {
		rows = append(rows, CheckRow{
			ID:             c.ID,
			Name:           c.Name,
			Status:         c.Status,
			Schedule:       c.Schedule,
			LastPingAt:     c.LastPingAt,
			NextExpectedAt: c.NextExpectedAt,
			TagList:        splitTags(c.Tags),
			PingURL:        h.pingURL(c.ID),
		})
	}

	data := DashboardData{
		Tag:     tagFilter,
		AllTags: allTags,
		Checks:  rows,
	}

	if err := h.tmplDashboard.ExecuteTemplate(w, "layout", data); err != nil {
		slog.Error("dashboard: render checks list", "err", err)
	}
}

// HandleCheckDetail handles GET /checks/{id}.
// Loads the check from the cache; renders the detail page with ping history,
// attached channels, and the last 10 notifications.
func (h *DashboardHandler) HandleCheckDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	check := h.cache.Get(id)
	if check == nil {
		http.NotFound(w, r)
		return
	}

	ctx := r.Context()

	// Load last 30 pings for the history strip.
	pings, err := h.pingRepo.ListByCheckID(ctx, id, 30)
	if err != nil {
		slog.Error("dashboard: list pings", "check_id", logSafe(id), "err", err) //nolint:gosec
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Load all channels and the subset attached to this check.
	allChannels, err := h.channelRepo.ListAll(ctx)
	if err != nil {
		slog.Error("dashboard: list all channels", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	attached, err := h.channelRepo.ListByCheckID(ctx, id)
	if err != nil {
		slog.Error("dashboard: list attached channels", "check_id", logSafe(id), "err", err) //nolint:gosec
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	attachedIDs := make(map[int64]bool, len(attached))
	for _, ch := range attached {
		attachedIDs[ch.ID] = true
	}

	// Load last 10 notifications.
	notifications, err := h.notifRepo.ListByCheckID(ctx, id, 10)
	if err != nil {
		slog.Error("dashboard: list notifications", "check_id", logSafe(id), "err", err) //nolint:gosec
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := CheckDetailData{
		Check:              check,
		PingURL:            h.pingURL(check.ID),
		PingSquares:        buildPingSquares(pings),
		AllChannels:        allChannels,
		AttachedChannelIDs: attachedIDs,
		Notifications:      notifications,
	}

	if err := h.tmplDetail.ExecuteTemplate(w, "layout", data); err != nil {
		slog.Error("dashboard: render check detail", "check_id", logSafe(id), "err", err) //nolint:gosec
	}
}

// HandleChannelList handles GET /channels.
// Lists all notification channels and renders the channel management page.
func (h *DashboardHandler) HandleChannelList(w http.ResponseWriter, r *http.Request) {
	channels, err := h.channelRepo.ListAll(r.Context())
	if err != nil {
		slog.Error("dashboard: list channels", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := ChannelsData{Channels: channels}
	if err := h.tmplChannels.ExecuteTemplate(w, "layout", data); err != nil {
		slog.Error("dashboard: render channels", "err", err)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// pingURL builds the public success ping URL for a check UUID.
func (h *DashboardHandler) pingURL(id string) string {
	return h.baseURL + "/ping/" + id
}

// buildPingSquares pads the most recent N pings to a fixed grid of 30 cells.
// Pings are assumed to be ordered newest-first (as returned by the repo).
// The grid is presented oldest-to-newest (left to right) so we reverse the
// slice after building it.
func buildPingSquares(pings []*model.Ping) []PingSquare {
	const gridSize = 30
	squares := make([]PingSquare, gridSize)

	// Fill from the end of the grid with actual ping data.
	for i := 0; i < len(pings) && i < gridSize; i++ {
		p := pings[i]
		sq := PingSquare{
			Class: string(p.Type), // model.PingType matches the CSS class suffix
			Title: p.CreatedAt.Format(time.RFC3339),
		}
		squares[gridSize-1-i] = sq
	}

	// Fill remaining slots (positions 0..gridSize-1-len(pings)) with empty.
	for i := 0; i < gridSize-len(pings); i++ {
		squares[i] = PingSquare{Class: "empty", Title: "no data"}
	}

	return squares
}

// statusOrder maps a check status to a sort priority (lower = shown first).
func statusOrder(s model.Status) int {
	switch s {
	case model.StatusDown:
		return 0
	case model.StatusUp:
		return 1
	case model.StatusNew:
		return 2
	case model.StatusPaused:
		return 3
	default:
		return 4
	}
}

// splitTags splits the comma-separated tags string into a trimmed, non-empty
// slice. An empty or whitespace-only string returns nil.
func splitTags(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// containsTag reports whether the comma-separated tags string contains the
// specified tag (case-sensitive, exact match after trimming).
func containsTag(tags, tag string) bool {
	for _, t := range splitTags(tags) {
		if t == tag {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Template functions
// ---------------------------------------------------------------------------

// templateFuncs returns the function map injected into every template set.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		// timeAgo renders a *time.Time as a relative human-readable string.
		// A nil pointer is rendered as "—".
		"timeAgo": func(t *time.Time) string {
			if t == nil {
				return "—"
			}
			d := time.Since(*t)
			switch {
			case d < 0:
				return "in " + formatDuration(-d)
			case d < time.Minute:
				return "just now"
			default:
				return formatDuration(d) + " ago"
			}
		},

		// formatTime renders a time.Time as a human-readable UTC timestamp.
		"formatTime": func(t time.Time) string {
			return t.UTC().Format("2006-01-02 15:04 UTC")
		},

		// isAttached reports whether a channel ID is in the attached set.
		"isAttached": func(ch *model.Channel, ids map[int64]bool) bool {
			return ids[ch.ID]
		},

		// derefStr dereferences a *string; returns "" if nil.
		"derefStr": func(s *string) string {
			if s == nil {
				return ""
			}
			return *s
		},
	}
}

// formatDuration formats a duration as a human-readable string with the
// largest appropriate unit (days, hours, minutes, seconds).
func formatDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", days)
	case d >= time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", h)
	case d >= time.Minute:
		m := int(d.Minutes())
		if m == 1 {
			return "1 min"
		}
		return fmt.Sprintf("%d mins", m)
	default:
		s := int(d.Seconds())
		if s == 1 {
			return "1 sec"
		}
		return fmt.Sprintf("%d secs", s)
	}
}
