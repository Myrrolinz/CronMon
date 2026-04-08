// Package handler contains HTTP request handlers for CronMon.
package handler

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/myrrolinz/cronmon/internal/cache"
	"github.com/myrrolinz/cronmon/internal/model"
	"github.com/myrrolinz/cronmon/internal/repository"
	"github.com/myrrolinz/cronmon/internal/schedule"
)

// PingHandler handles the three ping endpoints for a monitored check:
//
//	GET /ping/{uuid}          job completed successfully
//	GET /ping/{uuid}/start    job has started (extends deadline; does not change status)
//	GET /ping/{uuid}/fail     job explicitly failed (treated as a run completion)
//
// All three endpoints always return 200 OK with body "OK\n" regardless of
// whether the UUID is known. Unknown UUIDs are silently discarded so that
// scanners cannot enumerate check existence from HTTP status codes.
type PingHandler struct {
	cache        *cache.StateCache
	pingRepo     repository.PingRepository
	channelRepo  repository.ChannelRepository
	alertCh      chan<- model.AlertEvent
	trustedProxy bool
}

// NewPingHandler creates a PingHandler wired to the given dependencies.
func NewPingHandler(
	sc *cache.StateCache,
	pingRepo repository.PingRepository,
	channelRepo repository.ChannelRepository,
	alertCh chan<- model.AlertEvent,
	trustedProxy bool,
) *PingHandler {
	return &PingHandler{
		cache:        sc,
		pingRepo:     pingRepo,
		channelRepo:  channelRepo,
		alertCh:      alertCh,
		trustedProxy: trustedProxy,
	}
}

// HandleSuccess handles GET /ping/{uuid}.
// The job completed successfully; transitions new/down→up and fires a
// recovery alert if the check was previously down.
func (h *PingHandler) HandleSuccess(w http.ResponseWriter, r *http.Request) {
	h.handle(w, r, model.PingSuccess)
}

// HandleStart handles GET /ping/{uuid}/start.
// The job has started. Status is deliberately left unchanged: a down check
// stays down, a new check stays new. Only next_expected_at is extended to
// give the running job its full grace window from this moment.
func (h *PingHandler) HandleStart(w http.ResponseWriter, r *http.Request) {
	h.handle(w, r, model.PingStart)
}

// HandleFail handles GET /ping/{uuid}/fail.
// The job ran and finished (even though it failed). Treated identically to a
// success ping for state-transition purposes: new/down→up, recovery alert if
// the check was down. The ping record carries type "fail" for history.
func (h *PingHandler) HandleFail(w http.ResponseWriter, r *http.Request) {
	h.handle(w, r, model.PingFail)
}

// handle is the shared implementation for all three ping variants.
func (h *PingHandler) handle(w http.ResponseWriter, r *http.Request, pingType model.PingType) {
	uuid := r.PathValue("uuid")
	ctx := r.Context()
	now := time.Now().UTC()

	check := h.cache.Get(uuid)
	if check == nil || check.Status == model.StatusPaused {
		// Unknown UUID or paused check: return 200 silently.
		writeOK(w)
		return
	}

	ping := &model.Ping{
		CheckID:   check.ID,
		Type:      pingType,
		CreatedAt: now,
		SourceIP:  h.extractSourceIP(r),
	}
	if err := h.pingRepo.Create(ctx, ping); err != nil {
		slog.Error("ping: failed to record ping", //nolint:gosec // G706: values sanitised by logSafe
			"check_id", logSafe(check.ID), "type", logSafe(string(pingType)), "err", err)
	}

	switch pingType {
	case model.PingStart:
		h.handleStart(ctx, check, now)
	case model.PingSuccess:
		h.handleComplete(ctx, check, now, false)
	case model.PingFail:
		h.handleComplete(ctx, check, now, true)
	}

	writeOK(w)
}

// handleStart extends next_expected_at to now + grace without changing status.
func (h *PingHandler) handleStart(ctx context.Context, check *model.Check, now time.Time) {
	next := now.Add(time.Duration(check.Grace) * time.Minute)
	check.NextExpectedAt = &next
	check.UpdatedAt = now
	if err := h.cache.Set(ctx, check); err != nil {
		slog.Error("ping: failed to update check after start ping", //nolint:gosec // G706: value sanitised by logSafe
			"check_id", logSafe(check.ID), "err", err)
	}
}

// handleComplete transitions new/down→up, refreshes last_ping_at and
// next_expected_at, and enqueues alerts as appropriate:
//   - Recovery alert (AlertUp) if the check was previously down.
//   - Fail alert (AlertFail) if isFail is true and the check has NotifyOnFail=true.
func (h *PingHandler) handleComplete(ctx context.Context, check *model.Check, now time.Time, isFail bool) {
	wasDown := check.Status == model.StatusDown

	if check.Status == model.StatusNew || check.Status == model.StatusDown {
		check.Status = model.StatusUp
	}

	t := now
	check.LastPingAt = &t

	next, err := schedule.NextExpectedAt(check.Schedule, check.Grace, now)
	if err != nil {
		slog.Error("ping: failed to compute next_expected_at", //nolint:gosec // G706: values sanitised by logSafe
			"check_id", logSafe(check.ID), "schedule", logSafe(check.Schedule), "err", err)
	} else {
		check.NextExpectedAt = &next
	}

	check.UpdatedAt = now
	if err := h.cache.Set(ctx, check); err != nil {
		slog.Error("ping: failed to update check after ping", //nolint:gosec // G706: value sanitised by logSafe
			"check_id", logSafe(check.ID), "err", err)
		return
	}

	if wasDown {
		h.enqueueAlerts(ctx, check, model.AlertUp)
	}
	if isFail && check.NotifyOnFail {
		h.enqueueAlerts(ctx, check, model.AlertFail)
	}
}

// enqueueAlerts looks up all channels subscribed to check and sends one
// AlertEvent per channel to alertCh with the given alertType. The send is
// non-blocking: a full channel drops the event with a warning log.
func (h *PingHandler) enqueueAlerts(ctx context.Context, check *model.Check, alertType model.AlertType) {
	channels, err := h.channelRepo.ListByCheckID(ctx, check.ID)
	if err != nil {
		slog.Error("ping: failed to list channels for alert", //nolint:gosec // G706: values sanitised by logSafe
			"check_id", logSafe(check.ID), "alert_type", logSafe(string(alertType)), "err", err)
		return
	}
	for _, ch := range channels {
		event := model.AlertEvent{
			Check:     *check,
			Channel:   *ch,
			AlertType: alertType,
		}
		select {
		case h.alertCh <- event:
		default:
			slog.Warn("ping: alert channel full, dropping event", //nolint:gosec // G706: values sanitised by logSafe
				"check_id", logSafe(check.ID), "channel_id", ch.ID, "alert_type", logSafe(string(alertType)))
		}
	}
}

// extractSourceIP returns the client IP address from the request.
// When trustedProxy is true the first (leftmost) value in X-Forwarded-For is
// used; otherwise the host portion of r.RemoteAddr is returned.
func (h *PingHandler) extractSourceIP(r *http.Request) string {
	if h.trustedProxy {
		xff := r.Header.Get("X-Forwarded-For")
		if xff != "" {
			// X-Forwarded-For may be "client, proxy1, proxy2"; take the leftmost.
			ip, _, _ := strings.Cut(xff, ",")
			return strings.TrimSpace(ip)
		}
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// RemoteAddr without a port is unusual but handled gracefully.
		return r.RemoteAddr
	}
	return ip
}

// logSafe strips newline and carriage-return characters from s to prevent
// log-injection attacks (gosec G706).
func logSafe(s string) string {
	return strings.NewReplacer("\n", "", "\r", "").Replace(s)
}

// writeOK writes the standard 200 OK ping response.
func writeOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "OK\n") //nolint:errcheck
}
