# Implementation Plan: CronMon

> **Status:** Ready for development  
> **Version:** 1.0  
> **Last Updated:** 2026-03-30  
> **Based on:** ARCHITECTURE.md v1.1, CRONMON_DESIGN.md

---

## Overview

CronMon is implemented in three independently deliverable phases, each producing a mergeable milestone. Every phase follows the TDD workflow: write tests first (RED), implement (GREEN), refactor (IMPROVE), verify ≥ 80% coverage.

**External dependencies (total: 4):**
- `modernc.org/sqlite` — pure Go SQLite
- `github.com/robfig/cron/v3` — cron expression parsing
- `github.com/joho/godotenv` — `.env` file loading
- `github.com/google/uuid` — UUIDv4 generation

---

## Phase 1 — Foundation (Month 1)

**Deliverable:** A working, deployable cron job monitor with check management, ping reception, missed-ping detection, email alerting, a web dashboard, and basic auth.

### Step 1: Project Scaffolding
**Files:** `go.mod`, `go.sum`, `Makefile`, `.gitignore`, `.golangci.yml`

- Action: Run `go mod init github.com/myrrolinz/cronmon`; add the 4 external dependencies with `go get`; create `Makefile` with targets: `build`, `test`, `lint`, `run`, `clean`, and all 5 cross-compile targets
- `Makefile` build flags: `-ldflags "-s -w -X main.version=$(VERSION)"`
- `.golangci.yml`: enable `errcheck`, `govet`, `staticcheck`, `gosec`, `goimports`
- Why: Every subsequent step depends on a working module and lint pipeline
- Dependencies: None
- Risk: Low

---

### Step 2: Configuration Package
**File:** `internal/config/config.go`

- Action: Define `Config` struct; load from env vars with `os.LookupEnv`; load `.env` via `joho/godotenv` (silently skip if absent); implement `Validate()` that enforces startup rules:
  1. `BASE_URL` set and parseable
  2. `ADMIN_PASS` set and non-empty
  3. `SCHEDULER_INTERVAL` ≥ 10
  4. If any `SMTP_*` var is set, `SMTP_HOST` and `SMTP_FROM` must also be set
- Note: grace minimum (≥ 1 minute) is a per-check constraint enforced in the check CRUD handler (Step 12), not a global config value
- `String()` method must redact `AdminPass` and `SMTPPass` (replace with `***`)
- Tests: table-driven tests for every validation rule; test that `String()` never leaks passwords
- Dependencies: None
- Risk: Low

**Full env var list:** `PORT`, `DB_PATH`, `BASE_URL`, `SCHEDULER_INTERVAL`, `ADMIN_USER`, `ADMIN_PASS`, `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASS`, `SMTP_FROM`, `SMTP_TLS`, `TRUSTED_PROXY`, `REQUIRE_HTTPS`, `LOG_LEVEL`

---

### Step 3: Database Layer
**Files:** `internal/db/db.go`, `internal/db/migrations/001_initial.sql`

- Action: `Open(path string) (*sql.DB, error)` — opens SQLite, sets `PRAGMA journal_mode=WAL`, `PRAGMA foreign_keys=ON`, `PRAGMA busy_timeout=5000`; runs embedded migration SQL via `go:embed`
- Migration uses a simple version table (`schema_migrations`) and runs all `.sql` files in order; idempotent on re-run
- `001_initial.sql`: full schema from ARCHITECTURE.md §4 including all CHECK constraints, indexes, and `ON DELETE SET NULL` for `notifications.channel_id`
- Tests: open in-memory DB (`file::memory:?cache=shared`); verify all tables exist; verify FK pragma is on; verify migration is idempotent
- Dependencies: Step 1
- Risk: Low

---

### Step 4: Domain Models
**File:** `internal/model/model.go`

- Action: Define all structs: `Check`, `Ping`, `Channel`, `CheckChannel`, `Notification`, `AlertEvent`
- `Check` fields: `ID`, `Name`, `Slug`, `Schedule`, `Grace`, `Status`, `LastPingAt *time.Time`, `NextExpectedAt *time.Time`, `CreatedAt`, `UpdatedAt`, `Tags`
- `Status` type as named string with constants: `StatusNew`, `StatusUp`, `StatusDown`, `StatusPaused`
- `PingType` type with constants: `PingSuccess`, `PingStart`, `PingFail`
- `AlertType` type with constants: `AlertDown`, `AlertUp`
- No behaviour — pure data structs
- Tests: verify zero values, JSON marshaling round-trip
- Dependencies: None
- Risk: Low

---

### Step 5: Repository Layer
**Files:** `internal/repository/check_repo.go`, `ping_repo.go`, `channel_repo.go`, `notification_repo.go`

- Action: Define interfaces + SQLite implementations for each repository

**`CheckRepository` interface:**
```go
Create(ctx, *Check) error
GetByID(ctx, id string) (*Check, error)
GetByUUID(ctx, uuid string) (*Check, error)
ListAll(ctx) ([]*Check, error)
Update(ctx, *Check) error
Delete(ctx, id string) error
```

**`PingRepository` interface:**
```go
Create(ctx, *Ping) error
ListByCheckID(ctx, checkID string, limit int) ([]*Ping, error)
DeleteOldest(ctx, checkID string, keepN int) error
```

**`ChannelRepository` interface:**
```go
Create(ctx, *Channel) error
GetByID(ctx, id int64) (*Channel, error)
ListAll(ctx) ([]*Channel, error)
Delete(ctx, id int64) error
ListByCheckID(ctx, checkID string) ([]*Channel, error)
AttachToCheck(ctx, checkID string, channelID int64) error
DetachFromCheck(ctx, checkID string, channelID int64) error
```

**`NotificationRepository` interface:**
```go
Create(ctx, *Notification) error
ListByCheckID(ctx, checkID string, limit int) ([]*Notification, error)
```

- All queries use parameterized placeholders — no string concatenation
- All methods wrap errors: `fmt.Errorf("checkRepo.Create: %w", err)`
- Tests: integration tests against in-memory SQLite; test CRUD, cascade behavior, FK violations
- Dependencies: Steps 3, 4
- Risk: Medium (FK and cascade edge cases)

---

### Step 6: In-Memory State Cache
**File:** `internal/cache/check_cache.go`

- Action: Implement `StateCache` wrapping `CheckRepository`

```go
type StateCache struct {
    mu     sync.RWMutex
    checks map[string]*model.Check  // keyed by UUID (check.ID)
    repo   repository.CheckRepository
}
```

- `Hydrate(ctx)` — loads all checks from DB into map on startup
- `Get(uuid)` — `RLock`, return copy of check
- `Set(check)` — `Lock`, write to map + write-through to DB
- `Delete(uuid)` — `Lock`, remove from map + delete from DB
- `Snapshot()` — `RLock`, return slice of value-copies of all checks (read-only; used by `cleanupOldPings`)
- `WithWriteLock(fn func(checks []*model.Check, update func(*model.Check) error))` — holds `mu.Lock()` for the entire duration of `fn`; passes value-copies for reading, and an `update` closure that writes the updated check to both DB and cache under the same lock. Used exclusively by the scheduler's `evaluateAll()` to eliminate TOCTOU.
- Return value copies, never pointers to internal map entries, to prevent external mutation
- Tests: concurrent read/write test with `-race`; hydration test; write-through consistency test; `WithWriteLock` test verifies DB and cache are both updated atomically
- Dependencies: Steps 4, 5
- Risk: Medium (concurrency)

---

### Step 7: Cron Schedule Helper
**File:** `internal/schedule/schedule.go`

- Action: Thin wrapper around `robfig/cron/v3`

```go
// Validate returns an error if expr is not a valid 5-field cron expression
Validate(expr string) error

// NextAfter returns the next scheduled time strictly after t
NextAfter(expr string, t time.Time) (time.Time, error)
```

- `NextExpectedAt(expr string, grace int, ref time.Time) (time.Time, error)` — convenience: `NextAfter(expr, ref) + grace minutes`
- Tests: table-driven with known cron expressions and reference times; invalid expression returns error; DST boundary cases
- Dependencies: Step 1 (go.mod)
- Risk: Low

---

### Step 8: Notifier Interface + Email Implementation
**Files:** `internal/notify/notifier.go`, `internal/notify/email.go`

- Action: Define `Notifier` interface:

```go
type Notifier interface {
    Send(ctx context.Context, event model.AlertEvent) error
    Type() string
}
```

- `EmailNotifier` — uses `net/smtp` stdlib; STARTTLS on port 587; constructs plain-text + HTML multipart email
- Subject format: `[CronMon] ⚠ "{{.Check.Name}}" is DOWN` / `✓ "{{.Check.Name}}" RECOVERED`
- Body includes: check name, schedule, `next_expected_at`, ping URL
- `SMTP_TLS=false` disables STARTTLS for local/test SMTP only
- Tests: test with a mock SMTP server (capture output); test subject line formatting; test both alert types; test TLS config branch
- Dependencies: Steps 4, 2 (config)
- Risk: Medium (SMTP edge cases: TLS handshake, auth failure)

---

### Step 9: Scheduler
**File:** `internal/scheduler/scheduler.go`

- Action: Implement `Scheduler` struct

```go
type Scheduler struct {
    cache           *cache.StateCache
    channelRepo     repository.ChannelRepository
    pingRepo        repository.PingRepository
    alertCh         chan<- model.AlertEvent
    interval        time.Duration
    cleanupInterval time.Duration
    stopCh          chan struct{}
    cancel          context.CancelFunc
    wg              sync.WaitGroup
    stopOnce        sync.Once
}
```

- `notifRepo` is intentionally absent: the scheduler only enqueues `AlertEvent`s; the `NotifierWorker` (Step 10) owns all writes to the `notifications` table
- `New()` — panics if `interval <= 0` (mirrors `time.NewTicker` contract; catches misconfiguration at startup rather than with a cryptic runtime panic)
- `Start()` — creates a `context.WithCancel`; stores the cancel func; launches goroutine; runs `evaluateAll` immediately as a startup reconciliation pass; then `time.Ticker` at configured interval
- `Stop()` — guarded by `sync.Once`: calls the context cancel func (interrupts any in-flight DB calls), then closes `stopCh`; blocks on `wg.Wait()` until the goroutine exits. Safe to call multiple times.
- `evaluateAll(now time.Time)` — **two-phase** to minimise write-lock duration:
  - **Phase 1 (under write lock):** calls `cache.WithWriteLock(...)`. For each check: skip if `status != "up"`, skip if `NextExpectedAt` is nil or not yet passed. Otherwise call the `update` closure to atomically persist `status = "down"` to both DB and cache; collect each transitioned check into a local slice.
  - **Phase 2 (lock released):** for each transitioned check, call `channelRepo.ListByCheckID` and enqueue one `AlertEvent` per channel. Moving the DB query and channel send outside the lock prevents SMTP/DB latency from stalling concurrent ping handlers.
  - Non-blocking send: `select { case alertCh <- e: default: slog.Warn("alert channel full, dropping") }`
  - Scheduler skips `"paused"`, `"new"`, and already-`"down"` checks
- `cleanupOldPings(ctx)` — runs on 1h ticker; calls `cache.Snapshot()` (RLock) to get check IDs, then calls `pingRepo.DeleteOldest(ctx, checkID, 1000)` for every check — runs entirely outside the write lock
- Startup reconciliation: before first regular tick, run `evaluateAll` once to catch checks that went down while the process was offline
- Tests: unit tests with mock cache and repos; test `up→down` transition; test paused/new/already-down skipped; test non-blocking drop when channel full; test cleanup called on tick; test `Stop()` idempotent (no panic on double call); test `New()` panics on zero/negative interval; race detector
- Dependencies: Steps 6, 7, 8
- Risk: High (concurrency; correct lock strategy; startup reconciliation ordering)

---

### Step 10: Notifier Worker
**File:** `internal/notify/worker.go`

- Action: `NotifierWorker` goroutine

```go
type Worker struct {
    alertCh    <-chan model.AlertEvent
    notifiers  map[string]Notifier  // keyed by channel type
    notifRepo  repository.NotificationRepository
    done       chan struct{}
}
```

- `Start()` — range over `alertCh`; dispatch to notifier by `event.Channel.Type`; write result to `notifications` table; log outcome via `slog`
- Each `Send` call wrapped in a 15s `context.WithTimeout`
- `Stop()` — drains channel after it is closed by graceful shutdown; writes remaining notifications; signals `done`
- Tests: mock notifier; verify notification record written on success and on error; verify 15s timeout fires
- Dependencies: Steps 8, 5 (notification repo)
- Risk: Low

---

### Step 11: Ping Handlers
**File:** `internal/handler/ping.go`

- Action: Three HTTP handlers registered on `GET /ping/{uuid}`, `/ping/{uuid}/start`, `/ping/{uuid}/fail`

For **success** ping:
1. Parse UUID from `r.PathValue("uuid")` (Go 1.22)
2. Look up check in cache — if not found, return `200 OK "OK\n"` (no leak)
3. If found and `status != "paused"`: set `status = "up"` if was new/down (fire recovery if down), compute `next_expected_at = NextExpectedAt(schedule, grace, now)`, record ping
4. If recovery: enqueue `AlertEvent{AlertType: "up"}` to alertCh

For **start** ping:
1. Same lookup
2. **Do not change `status`** — `/start` means "I've started, I'm not done yet." A `down` check receiving `/start` stays `down`; a `new` check stays `new`. The job has not completed a run.
3. Set `next_expected_at = now + grace_minutes` (extends deadline from the moment the job started)
4. Record ping with `type = "start"`; no recovery alert

For **fail** ping:
1. Same state-transition logic as success: set `status = "up"` if was `new` or `down`, compute `next_expected_at = NextExpectedAt(schedule, grace, now)`, record ping with `type = "fail"`
2. If recovery (was `down`): enqueue `AlertEvent{AlertType: "up"}` — a `/fail` ping means the job ran and finished (even though it failed), which IS a recovery from missed execution

- Source IP: `r.RemoteAddr` by default; if `TRUSTED_PROXY=true`, read `X-Forwarded-For` first header value
- Response: always `200 OK` with body `"OK\n"` text/plain
- Tests: table-driven covering: unknown UUID (200 no panic), new→up transition, up stays up, down→up recovery via success ping, down→up recovery via fail ping, paused check ignored, start ping updates `next_expected_at` but does NOT change status, start ping on down check stays down, fail ping recorded
- Dependencies: Steps 6, 7, 9
- Risk: Medium (state transitions, concurrent pings)

---

### Step 12: Check CRUD Handlers + Basic Auth
**Files:** `internal/handler/check.go`, `internal/middleware/auth.go`

**Basic Auth middleware** (`auth.go`):
- `BasicAuth(username, password string) func(http.Handler) http.Handler`
- Uses `subtle.ConstantTimeCompare` on both username and password — compare both regardless of username match to avoid timing oracle
- Returns `401 WWW-Authenticate: Basic realm="CronMon"` on failure
- Applied to all routes except `/ping/*`

**Check handlers** (`check.go`):
- `POST /checks` — parse form; validate (name required, schedule valid via `schedule.Validate`, grace ≥ 1 minute); generate UUIDv4; set `Slug = ""` (reserved, unused in v1); compute initial `next_expected_at = NextExpectedAt(schedule, grace, now)`; store via cache; redirect to `/checks/{id}`
- `POST /checks/{id}` — update name, schedule, grace (must be ≥ 1), tags; recompute `next_expected_at` based on last ping or now; update via cache
- `POST /checks/{id}/delete` — delete via cache; redirect to `/checks`  
- `POST /checks/{id}/pause` — toggle `status` between `"paused"` and previous state (store `pre_pause_status` or default to `"new"`); update via cache

**CSRF note:** All state-mutating form endpoints are POST-only and protected by basic auth — no additional CSRF token needed for v1 (basic auth per-request provides equivalent protection).

**`_method` override middleware:** reads `_method` hidden field on POST requests and rewrites `r.Method` — apply only to auth-protected routes, never to `/ping/*`

- Tests: all CRUD operations; auth middleware timing-safe test; invalid schedule rejected with 400; pause toggle; redirect behavior
- Dependencies: Steps 6, 7
- Risk: Low

---

### Step 13: Channel CRUD Handlers
**File:** `internal/handler/channel.go`

- Action: Handlers for channel management

- `GET /channels` — list all channels; render template
- `POST /channels` — parse form; validate `type` is one of `email|slack|webhook`; validate `config` JSON against per-type schema (email requires `address`; slack/webhook require `url`; for webhook/slack URLs, also perform SSRF pre-validation of the URL format — resolve IP at call time during `Send`, not here); store via channel repo
- `POST /channels/{id}/delete` — delete channel; redirect to `/channels`
- `POST /checks/{id}/channels` — attach/detach channel to check (form checkbox pattern)

**Channel config validation:**
- `email`: validate `address` is a syntactically valid email (stdlib `mail.ParseAddress`)
- `slack`: validate `url` has `https://hooks.slack.com/` prefix
- `webhook`: validate URL is parseable and scheme is `https` or `http`; reject private IPs at URL-validation time as a fast-fail; the definitive IP check happens at send time (see Step 14)

- Tests: valid/invalid config for each type; SSRF pre-check rejects `http://192.168.1.1`; attach/detach works
- Dependencies: Step 5 (channel repo)
- Risk: Low

---

### Step 14: Slack + Webhook Notifiers
**Files:** `internal/notify/slack.go`, `internal/notify/webhook.go`

**SSRF mitigation (both):**
- After `net.LookupHost(host)`, check all resolved IPs against RFC 1918 (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`), loopback (`127.0.0.0/8`, `::1`), link-local (`169.254.0.0/16`), and private IPv6 ranges
- Reject with error if any resolved IP is in a private range
- Use a custom `http.Client` with `DialContext` that re-checks the resolved IP at dial time (prevents DNS TOCTOU rebinding)

**`SlackNotifier.Send`:**
- POST JSON `{"text": "...", "attachments": [...]}` to `channel.Config["url"]`
- Check HTTP response status 200; Slack returns `"ok"` in body on success

**`WebhookNotifier.Send`:**
- POST JSON payload (per ARCHITECTURE.md §8) with 15s timeout
- Accept 2xx responses as success; log non-2xx as error

- Tests: mock HTTP server; SSRF rejection test (private IP resolver mock); Slack payload shape; webhook payload shape; timeout fires correctly
- Dependencies: Step 8 (Notifier interface)
- Risk: High (SSRF DNS rebinding is subtle to implement correctly)

---

### Step 15: Dashboard + Templates
**Files:** `internal/handler/dashboard.go`, `web/templates/layout.html`, `web/templates/dashboard.html`, `web/templates/check_detail.html`, `web/templates/channels.html`, `web/static/pico.min.css`

- Action: Server-side rendered templates embedded via `//go:embed`

**`GET /checks`** (dashboard):
- List all checks sorted by status (down first, then up, then new, then paused)
- Per check: name, status badge, schedule, last ping time (relative: "2 min ago"), next expected time, ping URL (copyable)
- Filter by tag (query param `?tag=`)

**`GET /checks/{id}`** (detail):
- Check metadata + edit form
- Last 30 pings shown as colored squares (green=success, red=fail, yellow=start, grey=no data)
- Ping URL with copy button (JS `navigator.clipboard`)
- Attached channels section
- Last 10 notification records

**`GET /channels`**:
- List all channels with type badge
- Create channel form (type selector triggers appropriate config fields)

**Layout:** Pico.css (no build step); minimal JavaScript only for clipboard copy; fully functional without JS (clipboard degrades gracefully)

- HTML templates use `html/template` — auto-escapes all values; no `template.HTML` casts except for pre-validated static content
- Tests: render each template with fixture data; verify no template panics on nil fields; test tag filter
- Dependencies: Steps 11, 12, 13
- Risk: Low

---

### Step 16: Main Entry Point + Wiring
**File:** `main.go`

- Action: Wire all components together

```go
func main() {
    cfg := config.Load()
    if err := cfg.Validate(); err != nil { log.Fatal(err) }

    sqlDB, err := db.Open(cfg.DBPath)  // renamed to avoid shadowing the db package
    if err != nil { log.Fatal(err) }
    defer sqlDB.Close()

    // Repos
    checkRepo  := repository.NewCheckRepo(sqlDB)
    pingRepo   := repository.NewPingRepo(sqlDB)
    chanRepo   := repository.NewChannelRepo(sqlDB)
    notifRepo  := repository.NewNotifRepo(sqlDB)

    // Cache
    stateCache := cache.New(checkRepo)
    if err := stateCache.Hydrate(context.Background()); err != nil { log.Fatal(err) }

    // Notifiers
    alertCh   := make(chan model.AlertEvent, 64)
    notifiers := buildNotifiers(cfg)
    worker    := notify.NewWorker(alertCh, notifiers, notifRepo)
    worker.Start()

    // Scheduler (no notifRepo — worker owns the notifications table)
    sched := scheduler.New(stateCache, chanRepo, pingRepo, alertCh, cfg)
    sched.Start()

    // HTTP
    mux := buildMux(cfg, cache, pingRepo, chanRepo, notifRepo)
    srv := &http.Server{Addr: ":"+cfg.Port, Handler: mux}

    // Graceful shutdown
    ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
    defer stop()
    go srv.ListenAndServe()
    <-ctx.Done()

    shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    srv.Shutdown(shutCtx)
    sched.Stop()
    close(alertCh)
    worker.Wait()
}
```

- `buildMux`: register all routes including `GET /` → redirect to `/checks`; apply `BasicAuth` middleware to all except `/ping/*`; apply `_method` override middleware to auth routes only; apply request logging middleware to all routes
- Tests: smoke test that `main` starts and handles `/ping/unknown-uuid` with 200; test graceful shutdown sequence
- Dependencies: All prior steps
- Risk: Medium (wiring errors, shutdown ordering)

---

### Step 17: Request Logging Middleware
**File:** `internal/middleware/logging.go`

- Action: Structured request logging via `log/slog`
- Log fields: `method`, `path`, `status`, `duration_ms`, `remote_addr`
- **Do not log** the full path for `/ping/` routes (would log UUIDs — treat UUID as a secret); log only `/ping/[REDACTED]`
- Tests: verify UUID path is redacted in log output
- Dependencies: None
- Risk: Low

---

### Phase 1 Milestone Checklist

- [ ] `go build ./...` succeeds for all 5 targets
- [ ] `go test -race -count=1 ./...` passes with ≥ 80% coverage
- [ ] `golangci-lint run` passes with zero issues
- [ ] Manual smoke test: create check, receive ping, check goes up, miss a ping, check goes down, receive email alert, receive recovery email
- [ ] Dashboard accessible at `http://localhost:8080/checks` behind basic auth
- [ ] Binary size < 20 MB (stripped)
- [ ] Startup time < 1 s on a $5 VPS

---

## Phase 2 — Polish (Month 2)

**Deliverable:** A production-worthy, community-postable tool with ping history visualization, Slack/webhook notifications, check tags, and robust configuration.

### Step 18: Ping History UI
**Files:** `internal/handler/dashboard.go` (update), `web/templates/check_detail.html` (update)

- Action: `GET /checks/{id}` now loads last 30 pings from `pingRepo.ListByCheckID(ctx, id, 30)`; renders as a row of 30 colored squares using CSS (no JS required); squares are clickable to show timestamp tooltip
- Color: green = `success`, red = `fail`, amber = `start`, grey = absent slot (if fewer than 30 pings)
- Tests: render with 0, 1, 30 pings; verify correct colors
- Dependencies: Phase 1 complete
- Risk: Low

---

### Step 19: Check Tags + Dashboard Filtering
**Files:** `internal/handler/dashboard.go` (update), `web/templates/dashboard.html` (update)

- Action: `GET /checks?tag=backup` filters cache snapshot by tag; tag filter buttons shown in dashboard header if any checks have tags; tags stored as comma-separated string, displayed as individual badges
- `internal/model/model.go`: add `Tags() []string` helper that splits `Check.Tags` on comma
- Tests: filter returns only matching checks; empty tag param returns all; checks with multiple tags appear in each relevant filter
- Dependencies: Phase 1 complete
- Risk: Low

---

### Step 20: Slack + Webhook Notifiers Integrated
_Step 14 implements the notifiers. This step integrates them into the worker and registers them._

**File:** `main.go` (update `buildNotifiers`)

- Action: `buildNotifiers(cfg)` now returns `[]notify.Notifier{emailNotifier, slackNotifier, webhookNotifier}`; worker routes by `channel.Type`; Slack and webhook channels created via the channel UI are immediately active
- Tests: end-to-end test: create Slack channel, attach to check, trigger down alert, verify mock webhook server received correct payload
- Dependencies: Steps 14, 16
- Risk: Low

---

### Step 21: Graceful Shutdown (Hardening)
**File:** `main.go` (update), `internal/scheduler/scheduler.go` (update)

- Action: Ensure cleanup ticker also stops on `scheduler.Stop()`; verify notifier worker drains all remaining buffered events before `worker.Wait()` returns; add a `10s` timeout to the notifier drain (log warning if exceeded)
- Tests: integration test: enqueue 10 alert events, call shutdown, verify all 10 are written to notifications table before process exits
- Dependencies: Steps 9, 10, 16
- Risk: Medium

---

### Step 22: Ping Retention Cleanup
_Step 9 implements `cleanupOldPings` in the scheduler. This step adds the hourly ticker and verifies it works end-to-end._

**File:** `internal/scheduler/scheduler.go` (verify + test)

- Action: Confirm the 1h ticker is wired and calls `pingRepo.DeleteOldest(ctx, checkID, 1000)` for every check in the cache snapshot; verify it runs outside the write lock (reads snapshot copy first, then deletes by check ID)
- `DeleteOldest` SQL: `DELETE FROM pings WHERE id NOT IN (SELECT id FROM pings WHERE check_id = ? ORDER BY created_at DESC LIMIT ?)`
- Tests: insert 1,200 pings for one check; trigger `cleanupOldPings`; verify only 1,000 remain; verify the 1,000 kept are the most recent
- Dependencies: Step 9
- Risk: Low

---

### Phase 2 Milestone Checklist

- [ ] `go test -race ./...` ≥ 80% coverage
- [ ] Manual smoke test: check detail shows 30-square history; tag filter works; Slack webhook receives alert; graceful shutdown under load drains all notifications
- [ ] Run for 2+ hours under simulated load (1,000 pings per check); verify ping table stays bounded at 1,000 rows per check

---

## Phase 3 — Ecosystem (Month 3)

**Deliverable:** REST API, Prometheus metrics, Docker Hub image, full documentation.

### Step 23: REST API
**File:** `internal/handler/api.go`

- Action: Implement all REST API endpoints under `/api/v1/` from ARCHITECTURE.md §5

All endpoints:
- Return `Content-Type: application/json`
- Protected by same `BasicAuth` middleware
- Error responses: `{"error": "...", "code": "NOT_FOUND"}` with appropriate HTTP status codes
- Input validated server-side; 400 on invalid JSON or missing required fields
- `GET /api/v1/checks` supports `?tag=` and `?status=` query params
- `GET /api/v1/checks/{id}/pings` cursor-paginated: `?before={id}&limit={n}` (default 50, max 200)
- `DELETE /api/v1/channels/{id}` returns 204 No Content
- `GET /api/v1/stats` — returns aggregate counts: `{"up": n, "down": n, "new": n, "paused": n, "total": n}`; reads from cache snapshot (no DB query needed); no pagination

**Status codes:**
- 200 GET/PUT success
- 201 POST created (with `Location` header)
- 204 DELETE success
- 400 invalid input
- 401 unauthorized (basic auth)
- 404 not found
- 500 internal error (log but don't expose details)

- Tests: table-driven tests for each endpoint; test pagination cursor; test all error cases; test auth required
- Dependencies: Phase 2 complete
- Risk: Medium

---

### Step 24: Prometheus Metrics
**File:** `internal/metrics/metrics.go`

- Action: Register counters/gauges using `expvar` package (no Prometheus library dependency — keeps to 4 external deps)

**Alternative:** Add `prometheus/client_golang` as a 5th dependency. Given the target audience (DevOps users who will expect standard Prometheus client format), this is worth it. Decision: add as 5th dependency.

Metrics to expose at `GET /metrics`:
- `cronmon_checks_total{status="up|down|new|paused"}` gauge — count of checks per status
- `cronmon_pings_total{type="success|start|fail"}` counter — lifetime ping count
- `cronmon_alerts_sent_total{type="down|up",channel_type="email|slack|webhook"}` counter
- `cronmon_alert_channel_full_total` counter — dropped alerts

- `GET /metrics` endpoint: public (no auth) — consistent with Prometheus scrape convention; operators expose or protect this via nginx as needed; document this decision
- Tests: verify metric names and labels; verify counter increments on events
- Dependencies: Phase 2 complete
- Risk: Low

---

### Step 25: Docker Image
**Files:** `Dockerfile`, `docker-compose.yml`, `.dockerignore`

- Action: Multi-stage build:

```dockerfile
FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags "-s -w" -o cronmon .

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /app/cronmon /cronmon
EXPOSE 8080
ENTRYPOINT ["/cronmon"]
```

- Assets embedded via `go:embed` so `FROM scratch` works — no web directory needed
- `.dockerignore`: exclude `.git`, `doc/`, `dist/`
- `docker-compose.yml`: includes cronmon + caddy services with TLS (from ARCHITECTURE.md §11)
- Tests: build image; run `docker run ... /ping/test-uuid` returns 200; image size < 20 MB
- Dependencies: Phase 2 complete
- Risk: Low

---

### Step 26: GitHub Actions CI/CD
**File:** `.github/workflows/ci.yml`

- Action: Three jobs

**`test` job** (on push + PR):
```yaml
- go vet ./...
- golangci-lint run
- go test -race -coverprofile=coverage.out ./...
- go tool cover -func=coverage.out  # fail if < 80%
```

**`build` job** (needs: test):
```yaml
- cross-compile all 5 targets (linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64)
- upload as artifacts
```

**`release` job** (on push to `v*` tags):
```yaml
- build all targets
- create GitHub release with binary attachments + checksums
- docker buildx build + push to Docker Hub (multi-arch: linux/amd64, linux/arm64)
```

- Dependencies: Phase 2 complete
- Risk: Low

---

### Step 27: Documentation
**Files:** `README.md`, `doc/QUICKSTART.md`

- Action: `README.md` — badges (build, coverage, license); one-paragraph description; feature list; quickstart (3 deployment methods from ARCHITECTURE.md §11); configuration reference (env var table); integration example (`curl` command to append to a cron job)
- `doc/QUICKSTART.md` — getting started guide: install, first check creation, verifying a ping, adding a Slack channel
- Document the proxy cache requirement for `/ping/` paths
- Document UUID rotation procedure for rekeying a compromised ping URL
- Dependencies: Phase 3 complete
- Risk: Low

---

### Phase 3 Milestone Checklist

- [ ] REST API tested with `curl` against live instance
- [ ] Prometheus metrics scraped by local Prometheus instance
- [ ] Docker image published to Docker Hub (multi-arch)
- [ ] GitHub Actions CI green on all branches
- [ ] README matches current feature set
- [ ] Post-release checklist: post to r/selfhosted; comment on archived minicron issues

---

## Testing Strategy

### Unit Tests
Every package gets a `_test.go` file. Use table-driven tests. All tests pass with `-race`.

| Package | What to test |
|---------|-------------|
| `config` | All validation rules; credential redaction |
| `schedule` | Valid/invalid cron expressions; `NextAfter` accuracy; DST boundaries |
| `repository` | CRUD for all repos; cascade behavior; FK constraints; `DeleteOldest` |
| `cache` | Concurrent read/write; hydration; write-through consistency |
| `scheduler` | State transitions; startup reconciliation; paused skipped; channel-full drop |
| `notify` | Email formatting; SSRF rejection; mock SMTP and webhook; timeout fires |
| `handler/ping` | All state transitions; unknown UUID returns 200; source IP logic |
| `handler/check` | CRUD; invalid schedule rejected; auth required |
| `handler/api` | All endpoints; pagination; error codes |
| `middleware` | BasicAuth timing-safe; UUID path redaction |

### Integration Tests
`internal/integration_test.go` (build tag `//go:build integration`):
- Full flow: create check → receive ping → check goes up → miss ping → check goes down → receive notification
- Startup reconciliation: populate DB with `up` + overdue check, restart scheduler, verify `down` transition fires
- Graceful shutdown drain: fill alert channel, send SIGTERM, verify all events written

### Coverage Target
≥ 80% per package, ≥ 80% overall. Enforced in CI.

---

## Risks & Mitigations

| Risk | Severity | Mitigation |
|------|----------|-----------|
| SSRF DNS rebinding in webhook notifier | High | Re-check resolved IP at dial time via custom `DialContext`; unit test with mock resolver |
| TOCTOU in scheduler eliminated | — | Resolved: write lock held for entire `evaluateAll()` pass |
| SQLite write contention under high load | Medium | WAL mode; `busy_timeout=5000ms`; benchmark 1,000 pings/min in CI |
| Ping flood suppresses alerts | Medium | Documented; UUID rotation is the v1 mitigation. Rate limiting is v2. |
| `next_expected_at` off-by-one at check creation | Medium | Use `cron_next_after(schedule, now)` — strict "after now" prevents immediate false positive |
| Template XSS | Low | `html/template` auto-escapes by default; never use `template.HTML` cast for user data |
| Credential leakage in logs | Low | `Config.String()` redacts passwords; UUID paths logged as `/ping/[REDACTED]` |

---

## Implementation Order Summary

```
Phase 1 (core, ~4 weeks):
  1 → 2 → 3 → 4            (scaffolding, config, DB, models — independent)
  5 → 6                    (repos → cache)
  7                        (schedule helper)
  8 → 9 → 10               (notifier → scheduler → worker)
  11 → 12 → 13             (ping handlers, check CRUD+auth, channel CRUD)
  14                       (Slack/webhook notifiers)
  15 → 16 → 17             (templates, main wiring, logging)

Phase 2 (polish, ~3 weeks):
  18 → 19                  (ping history UI, tags)
  20 → 21 → 22             (notifier integration, shutdown hardening, ping cleanup)

Phase 3 (ecosystem, ~3 weeks):
  23 → 24                  (REST API, Prometheus)
  25 → 26 → 27             (Docker, CI/CD, docs)
```

Steps within each numbered group can be parallelized if more than one developer is working.
