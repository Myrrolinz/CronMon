package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/myrrolinz/cronmon/internal/cache"
	"github.com/myrrolinz/cronmon/internal/model"
	"github.com/myrrolinz/cronmon/internal/netutil"
	"github.com/myrrolinz/cronmon/internal/repository"
)

// ChannelHandler handles CRUD form submissions for Channel records and the
// many-to-many association between checks and channels.
//
//	GET  /channels               → list all channels
//	POST /channels               → create channel
//	POST /channels/{id}/delete   → delete channel
//	POST /checks/{id}/channels   → attach/detach channels to a check
type ChannelHandler struct {
	channelRepo repository.ChannelRepository
	checkCache  *cache.StateCache
}

// NewChannelHandler creates a ChannelHandler backed by the given channel
// repository and check cache.
func NewChannelHandler(channelRepo repository.ChannelRepository, checkCache *cache.StateCache) *ChannelHandler {
	return &ChannelHandler{
		channelRepo: channelRepo,
		checkCache:  checkCache,
	}
}

// HandleList handles GET /channels.
// Fetches all channels from the repository. Template rendering is wired in
// Step 15 (dashboard / templates).
func (h *ChannelHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	channels, err := h.channelRepo.ListAll(r.Context())
	if err != nil {
		slog.Error("channel: failed to list", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	// Template rendering is added in Step 15.
	_ = channels
	w.WriteHeader(http.StatusOK)
}

// HandleCreate handles POST /channels.
// Validates the channel type and config JSON, then stores the new channel and
// redirects to /channels.
func (h *ChannelHandler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	channelType := r.FormValue("type")
	name := r.FormValue("name")
	configStr := r.FormValue("config")

	// If the JS-assembled config field is empty, build it from the individual
	// visible form fields so the form works without JavaScript.
	if configStr == "" {
		configStr = buildConfigFromFields(channelType, r)
	}

	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	if err := validateChannelConfig(channelType, configStr); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ch := &model.Channel{
		Type:      channelType,
		Name:      name,
		Config:    json.RawMessage(configStr),
		CreatedAt: time.Now().UTC(),
	}

	if err := h.channelRepo.Create(r.Context(), ch); err != nil {
		slog.Error("channel: failed to create", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/channels", http.StatusSeeOther)
}

// HandleDelete handles POST /channels/{id}/delete.
// Deletes the channel and redirects to /channels. If the channel does not
// exist the redirect still occurs (idempotent).
func (h *ChannelHandler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid channel id", http.StatusBadRequest)
		return
	}

	if err := h.channelRepo.Delete(r.Context(), id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			http.Redirect(w, r, "/channels", http.StatusSeeOther)
			return
		}
		slog.Error("channel: failed to delete", "channel_id", id, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/channels", http.StatusSeeOther)
}

// HandleAttachDetach handles POST /checks/{id}/channels.
// Reads the desired set of channel IDs from the submitted form checkboxes,
// detaches any channels no longer selected, and attaches any newly selected
// channels. Attaching is idempotent (INSERT OR IGNORE in the repo layer).
func (h *ChannelHandler) HandleAttachDetach(w http.ResponseWriter, r *http.Request) {
	checkID := r.PathValue("id")
	if h.checkCache.Get(checkID) == nil {
		http.NotFound(w, r)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Build the desired set of channel IDs from submitted checkboxes.
	desired := make(map[int64]bool)
	for _, s := range r.Form["channel_ids"] {
		if id, err := strconv.ParseInt(s, 10, 64); err == nil {
			desired[id] = true
		}
	}

	// Validate desired IDs against known channels to prevent FK failures on
	// stale or fabricated IDs submitted in the form. Unknown IDs are silently
	// dropped; the FK constraint in SQLite remains the final safety net.
	for id := range desired {
		if _, err := h.channelRepo.GetByID(r.Context(), id); err != nil {
			delete(desired, id)
		}
	}

	// Fetch currently attached channels to compute the diff.
	current, err := h.channelRepo.ListByCheckID(r.Context(), checkID)
	if err != nil {
		slog.Error("channel: failed to list channels for check", //nolint:gosec // G706: value sanitised by logSafe
			"check_id", logSafe(checkID), "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Detach channels that are no longer in the desired set.
	for _, ch := range current {
		if !desired[ch.ID] {
			if err := h.channelRepo.DetachFromCheck(r.Context(), checkID, ch.ID); err != nil {
				slog.Error("channel: failed to detach from check", //nolint:gosec // G706: value sanitised by logSafe
					"check_id", logSafe(checkID), "channel_id", ch.ID, "err", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
		}
	}

	// Attach all desired channels (idempotent via INSERT OR IGNORE in the repo).
	for chID := range desired {
		if err := h.channelRepo.AttachToCheck(r.Context(), checkID, chID); err != nil {
			slog.Error("channel: failed to attach to check", //nolint:gosec // G706: value sanitised by logSafe
				"check_id", logSafe(checkID), "channel_id", chID, "err", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
	}

	http.Redirect(w, r, "/checks/"+checkID, http.StatusSeeOther)
}

// ---------------------------------------------------------------------------
// Channel config validation
// ---------------------------------------------------------------------------

// buildConfigFromFields constructs the config JSON string from the individual
// named form fields (e.g. _email_address, _slack_url, _webhook_url) that are
// submitted by the non-JS version of the channel creation form.
func buildConfigFromFields(channelType string, r *http.Request) string {
	switch channelType {
	case "email":
		addr := strings.TrimSpace(r.FormValue("_email_address"))
		if addr == "" {
			return ""
		}
		b, _ := json.Marshal(struct {
			Address string `json:"address"`
		}{Address: addr})
		return string(b)
	case "slack":
		u := strings.TrimSpace(r.FormValue("_slack_url"))
		if u == "" {
			return ""
		}
		b, _ := json.Marshal(struct {
			URL string `json:"url"`
		}{URL: u})
		return string(b)
	case "webhook":
		u := strings.TrimSpace(r.FormValue("_webhook_url"))
		if u == "" {
			return ""
		}
		b, _ := json.Marshal(struct {
			URL string `json:"url"`
		}{URL: u})
		return string(b)
	default:
		return ""
	}
}

// validateChannelConfig dispatches to the per-type validation function.
// It returns a descriptive error for unknown types or malformed configs.
func validateChannelConfig(channelType, configJSON string) error {
	switch channelType {
	case "email":
		return validateEmailConfig(configJSON)
	case "slack":
		return validateSlackConfig(configJSON)
	case "webhook":
		return validateWebhookConfig(configJSON)
	default:
		return fmt.Errorf("type must be one of: email, slack, webhook")
	}
}

// validateEmailConfig checks that the config JSON contains a syntactically
// valid email address using the stdlib mail.ParseAddress function.
func validateEmailConfig(configJSON string) error {
	var cfg struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return fmt.Errorf("config: invalid JSON: %w", err)
	}
	if cfg.Address == "" {
		return fmt.Errorf("email config: address is required")
	}
	if _, err := mail.ParseAddress(cfg.Address); err != nil {
		return fmt.Errorf("email config: invalid address: %w", err)
	}
	return nil
}

// validateSlackConfig checks that the config JSON contains a URL beginning
// with the canonical Slack incoming-webhook prefix.
func validateSlackConfig(configJSON string) error {
	var cfg struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return fmt.Errorf("config: invalid JSON: %w", err)
	}
	if cfg.URL == "" {
		return fmt.Errorf("slack config: url is required")
	}
	if !strings.HasPrefix(cfg.URL, "https://hooks.slack.com/") {
		return fmt.Errorf("slack config: url must start with https://hooks.slack.com/")
	}
	return nil
}

// validateWebhookConfig checks that the config JSON contains a parseable
// http or https URL. As a fast-fail SSRF pre-check, it also rejects any URL
// whose host is a literal private, loopback, or link-local IP address.
// The definitive IP check (including DNS resolution) is performed at send time.
func validateWebhookConfig(configJSON string) error {
	var cfg struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return fmt.Errorf("config: invalid JSON: %w", err)
	}
	if cfg.URL == "" {
		return fmt.Errorf("webhook config: url is required")
	}
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return fmt.Errorf("webhook config: url is not valid")
	}
	hostname := u.Hostname()
	if u.Host == "" || hostname == "" {
		return fmt.Errorf("webhook config: url is not valid")
	}
	if port := u.Port(); port != "" {
		if _, err := strconv.ParseUint(port, 10, 16); err != nil {
			return fmt.Errorf("webhook config: url is not valid")
		}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("webhook config: url scheme must be http or https")
	}
	// Fast-fail: reject URLs with literal private/loopback IP addresses.
	// Hostname-based targets are validated at send time via DNS resolution.
	if ip := net.ParseIP(hostname); ip != nil && netutil.IsPrivateIP(ip) {
		return fmt.Errorf("webhook config: url must not target a private IP address")
	}
	return nil
}
