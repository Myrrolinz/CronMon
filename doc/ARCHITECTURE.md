# CronMon — Architecture Document

> **Status:** Pre-development  
> **Version:** 1.1  
> **Last Updated:** 2026-03-30  
> **Authors:** Engineering

---

## Table of Contents

1. [System Overview](#1-system-overview)
2. [Goals and Constraints](#2-goals-and-constraints)
3. [Component Architecture](#3-component-architecture)
4. [Data Architecture](#4-data-architecture)
5. [API Design](#5-api-design)
6. [Concurrency Model](#6-concurrency-model)
7. [State Machine](#7-check-state-machine)
8. [Notification Pipeline](#8-notification-pipeline)
9. [Security Architecture](#9-security-architecture)
10. [Configuration](#10-configuration)
11. [Deployment Architecture](#11-deployment-architecture)
12. [Build & Release Pipeline](#12-build--release-pipeline)
13. [Architecture Decision Records](#13-architecture-decision-records)
14. [Open Issues](#14-open-issues)

---

## 1. System Overview

CronMon is a self-hosted cron job monitoring service distributed as a single, statically-linked Go binary. It implements a **dead man's switch** (push-based) model: the cron job pings the monitor after completion; if no ping arrives within the expected window plus a configurable grace period, an alert is fired.

### Design Philosophy

- **Zero external runtime dependencies** — one binary, one SQLite file, done.
- **Minimal operator surface** — no daemon manager, no config files required (env vars only), no DB migration tools.
- **Correct by default** — the grace period is mandatory, never zero. Silent failures are the enemy.
- **Honest scope** — this is a monitor, not a scheduler. It never runs jobs.

### Context Diagram

```
┌──────────────────────────────────────────────────────────────┐
│                         Operator VPS                         │
│                                                              │
│   cron job: backup.sh && curl /ping/{uuid}                   │
│                        │                                     │
│                        ▼                                     │
│          ┌─────────────────────────┐                        │
│          │        CronMon          │  ← single process      │
│          │   (single Go binary)    │                        │
│          └──────────┬──────────────┘                        │
│                     │ reads/writes                           │
│                     ▼                                        │
│              cronmon.db (SQLite)                             │
└──────────────────────────────────────────────────────────────┘
         │                          │
         ▼                          ▼
   Browser (dashboard)    SMTP / Slack / Webhook
   (operator only)        (outbound alerts)
```

---

## 2. Goals and Constraints

### Functional Goals

| ID | Goal |
|----|------|
| G1 | Receive and record pings from cron jobs (success, start, fail) |
| G2 | Detect missed pings and alert within one scheduler tick of the deadline |
| G3 | Send alerts via email, Slack webhook, and generic webhook |
| G4 | Provide a web dashboard for check management and status overview |
| G5 | Expose a REST API for programmatic check management (Month 3) |
| G6 | Expose a Prometheus `/metrics` endpoint (Month 3) |

### Non-Functional Goals

| ID | Goal | Target |
|----|------|--------|
| N1 | Ping endpoint latency | p99 < 10 ms (no network I/O in hot path) |
| N2 | Scheduler accuracy | Alert within 30 s of deadline (one tick) |
| N3 | Startup time | < 1 s including DB migration |
| N4 | Memory footprint | < 50 MB RSS under normal operation |
| N5 | SQLite write throughput | Sufficient for 1,000 checks × 1 ping/min = 1,000 writes/min |
| N6 | Graceful shutdown | Drain in-flight requests; flush notification queue before exit |

### Hard Constraints

- **No external process dependencies at runtime** — no Redis, no Postgres, no message broker.
- **Single process** — no clustering, no horizontal scaling in v1.
- **Pure Go binary** — must cross-compile for `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`, `windows/amd64` without CGO.
- **SQLite driver must be pure Go** (`modernc.org/sqlite`) to satisfy the above.

---

## 3. Component Architecture

### Internal Component Diagram

```
┌─────────────────────────────────────────────────────────────────┐
│                        CronMon Process                          │
│                                                                 │
│  ┌────────────────────────────────────────────────────────┐     │
│  │                     HTTP Server                         │     │
│  │  (net/http, Go 1.22 pattern routing)                   │     │
│  │                                                         │     │
│  │  GET  /                         → Dashboard handler    │     │
│  │  GET  /checks                   → Check list handler   │     │
│  │  POST /checks                   → Create check         │     │
│  │  GET  /checks/{id}              → Check detail         │     │
│  │  PUT  /checks/{id}              → Update check         │     │
│  │  DELETE /checks/{id}            → Delete check         │     │
│  │  POST /checks/{id}/pause        → Pause check          │     │
│  │  GET  /ping/{uuid}              → Ping success         │     │
│  │  GET  /ping/{uuid}/start        → Ping start           │     │
│  │  GET  /ping/{uuid}/fail         → Ping fail            │     │
│  │  GET  /api/v1/...               → REST API (Month 3)   │     │
│  │  GET  /metrics                  → Prometheus (Month 3) │     │
│  └───────────────┬────────────────────────────────────────┘     │
│                  │                                               │
│         ┌────────▼────────┐                                     │
│         │   State Cache   │  sync.RWMutex-protected map         │
│         │  (in-memory)    │  map[uuid]*Check                    │
│         └────────┬────────┘                                     │
│                  │ read-through / write-through                  │
│         ┌────────▼────────┐   ┌──────────────────────┐          │
│         │    Repository   │   │      Scheduler       │          │
│         │  (database/sql) │   │   (goroutine, 30s)   │          │
│         │                 │   │                      │          │
│         │  CheckRepo      │   │  evaluateAll()       │          │
│         │  PingRepo       │◄──│  → ch <- AlertEvent  │          │
│         │  ChannelRepo    │   └──────────────────────┘          │
│         │  NotifRepo      │                                      │
│         └────────┬────────┘   ┌──────────────────────┐          │
│                  │            │   Notifier Worker    │          │
│                  │            │   (goroutine)        │          │
│         ┌────────▼────────┐   │                      │          │
│         │     SQLite      │◄──│  email / slack /     │          │
│         │  (cronmon.db)   │   │  webhook dispatch    │          │
│         └─────────────────┘   └──────────────────────┘          │
└─────────────────────────────────────────────────────────────────┘
```

### Package Layout

```
cronmon/
├── main.go                    # Entry point: wire everything, start server
├── internal/
│   ├── config/
│   │   └── config.go          # Env var loading, validation, Config struct
│   ├── db/
│   │   ├── db.go              # Open + migrate SQLite, return *sql.DB
│   │   └── migrations/
│   │       └── 001_initial.sql
│   ├── model/
│   │   └── model.go           # Check, Ping, Channel, Notification structs
│   ├── repository/
│   │   ├── check_repo.go      # CheckRepository interface + SQLite impl
│   │   ├── ping_repo.go       # PingRepository interface + SQLite impl
│   │   ├── channel_repo.go    # ChannelRepository interface + SQLite impl
│   │   └── notification_repo.go
│   ├── cache/
│   │   └── check_cache.go     # In-memory RWMutex cache, wraps CheckRepository
│   ├── scheduler/
│   │   └── scheduler.go       # Ticker loop, state transitions, alert dispatch
│   ├── notify/
│   │   ├── notifier.go        # Notifier interface
│   │   ├── email.go           # SMTP implementation
│   │   ├── slack.go           # Slack webhook implementation
│   │   └── webhook.go         # Generic webhook implementation
│   ├── handler/
│   │   ├── ping.go            # /ping/{uuid}[/start|/fail] handlers
│   │   ├── check.go           # /checks CRUD handlers
│   │   ├── dashboard.go       # / and /checks/{id} template handlers
│   │   └── api.go             # /api/v1/... handlers (Month 3)
│   ├── middleware/
│   │   ├── auth.go            # Basic auth middleware
│   │   └── logging.go         # Request logging (slog)
│   └── metrics/
│       └── metrics.go         # Prometheus counters/gauges (Month 3)
├── web/
│   ├── templates/
│   │   ├── layout.html
│   │   ├── dashboard.html
│   │   └── check_detail.html
│   └── static/
│       └── pico.min.css
└── doc/
    └── ARCHITECTURE.md
```

> `internal/` enforces Go's visibility rule: nothing outside this module can import these packages.

---

## 4. Data Architecture

### Schema

```sql
-- Schema version: 1
-- All DATETIME columns store UTC in RFC3339 format ("2006-01-02T15:04:05Z").

CREATE TABLE checks (
    id              TEXT        PRIMARY KEY,          -- UUIDv4
    name            TEXT        NOT NULL,
    slug            TEXT        UNIQUE,               -- reserved for future human-readable URLs
    schedule        TEXT        NOT NULL,             -- cron expression, validated on write
    grace           INTEGER     NOT NULL DEFAULT 10,  -- minutes; minimum 1
    status          TEXT        NOT NULL DEFAULT 'new'
                                CHECK(status IN ('new','up','down','paused')),
    last_ping_at    DATETIME,                         -- last successful/fail/start ping
    next_expected_at DATETIME,                        -- deadline: schedule + grace, updated each ping
    created_at      DATETIME    NOT NULL,
    updated_at      DATETIME    NOT NULL,
    tags            TEXT        NOT NULL DEFAULT '',   -- comma-separated, empty string not NULL
    notify_on_fail  INTEGER     NOT NULL DEFAULT 0    -- boolean (0/1); opt-in: fire an AlertFail on each /fail ping
);

CREATE TABLE pings (
    id          INTEGER     PRIMARY KEY AUTOINCREMENT,
    check_id    TEXT        NOT NULL REFERENCES checks(id) ON DELETE CASCADE,
    type        TEXT        NOT NULL CHECK(type IN ('success','start','fail')),
    created_at  DATETIME    NOT NULL,
    source_ip   TEXT        NOT NULL DEFAULT ''       -- may be empty if behind proxy without forwarding
);

CREATE TABLE channels (
    id          INTEGER     PRIMARY KEY AUTOINCREMENT,
    type        TEXT        NOT NULL CHECK(type IN ('email','slack','webhook')),
    name        TEXT        NOT NULL,
    config      TEXT        NOT NULL,                 -- JSON blob, validated on write by channel type
    created_at  DATETIME    NOT NULL
);

CREATE TABLE check_channels (
    check_id    TEXT        NOT NULL REFERENCES checks(id) ON DELETE CASCADE,
    channel_id  INTEGER     NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    PRIMARY KEY (check_id, channel_id)
);

CREATE TABLE notifications (
    id          INTEGER     PRIMARY KEY AUTOINCREMENT,
    check_id    TEXT        NOT NULL REFERENCES checks(id) ON DELETE CASCADE,
    channel_id  INTEGER              REFERENCES channels(id) ON DELETE SET NULL,  -- NULL = channel deleted; record preserved for audit
    type        TEXT        NOT NULL CHECK(type IN ('down','up')),
    sent_at     DATETIME    NOT NULL,
    error       TEXT                                  -- NULL means delivered successfully
);

-- Indexes
CREATE INDEX idx_checks_status          ON checks(status);
CREATE INDEX idx_pings_check_created    ON pings(check_id, created_at DESC);
CREATE INDEX idx_notifications_check    ON notifications(check_id, sent_at DESC);
```

### Data Design Notes

**`next_expected_at` (not `next_ping`)** — this column represents the absolute deadline by which the next ping must arrive. The scheduler compares `now > next_expected_at` to decide whether to alert. It is computed and updated as follows:

- **On check creation:** `next_expected_at = cron_next_after(schedule, now) + grace_minutes`. Using the next scheduled run _after_ the current time prevents a false "down" alert immediately after the check is created.
- **On `/ping/{uuid}` (success) or `/ping/{uuid}/fail`:** `next_expected_at = cron_next_after(schedule, now) + grace_minutes` — reset from the schedule as normal.
- **On `/ping/{uuid}/start`:** `next_expected_at = now + grace_minutes`. This extends the deadline to give the running job its full grace window from the moment it started, preventing false "down" alerts for jobs whose runtime approaches or exceeds the originally scheduled window.

**`channels.config` validation** — channel config is validated against a per-type schema at write time (HTTP handler layer), never at read time. Invalid configs are rejected with a 400 before reaching the database.

| Channel type | Required config keys |
|---|---|
| `email` | `{"address": "user@example.com"}` |
| `slack` | `{"url": "https://hooks.slack.com/..."}` |
| `webhook` | `{"url": "https://..."}` |

**Ping retention** — pings are pruned by a background cleanup task that runs on an hourly ticker inside the scheduler goroutine. The per-check retention limit is 1,000 rows (hardcoded in v1; configurable in a future release). The `DELETE` runs entirely outside the hot write path and has no impact on ping endpoint latency. Implementation: delete all rows for each check that fall outside the most recent 1,000 ordered by `created_at DESC`.

**Cascade deletes** — `pings` and `check_channels` rows are deleted when their parent `check` is deleted (`ON DELETE CASCADE`). `notifications` rows are **not** cascade-deleted; instead, `channel_id` is set to NULL (`ON DELETE SET NULL`) when a channel is deleted. This preserves the full alert history even after a channel is removed. `PRAGMA foreign_keys = ON` must be set on every connection.

---

## 5. API Design

### Ping Endpoints (public, no auth)

These endpoints are intentionally unauthenticated: the UUID is the secret.

```
GET /ping/{uuid}          → 200 OK    (job completed successfully)
GET /ping/{uuid}/start    → 200 OK    (job has started)
GET /ping/{uuid}/fail     → 200 OK    (job explicitly failed)
```

**Response:** `200 OK` with body `"OK\n"` (text/plain). Always 200 on receipt; never 404 (to avoid leaking check existence to scanners). Unknown UUIDs return 200 and are silently discarded.

**Idempotency:** Duplicate pings (e.g., from a curl retry) are each stored as separate ping records. The state machine uses the most recent ping. This is intentional — the history log is append-only.

**Ping with `start`:** Receiving a `/start` ping sets `next_expected_at = now + grace_minutes`. This gives the actively running job its full grace window from the moment it started, preventing false "down" alerts for long-running jobs. The grace period therefore serves dual purpose: post-schedule tolerance *and* maximum allowed runtime when a start ping is used. A subsequent `/ping/{uuid}` (success) or `/ping/{uuid}/fail` then resets `next_expected_at` from the schedule as normal.

> **Note for operators:** if a job's maximum runtime can exceed the configured grace period, increase `grace` on that check accordingly. A `max_duration_minutes` field separate from `grace` is deferred to a future release.

### Dashboard Endpoints (protected by basic auth)

```
GET  /                        → redirect to /checks
GET  /checks                  → dashboard: list all checks
POST /checks                  → create check (form submission)
GET  /checks/{id}             → check detail + ping history
POST /checks/{id}             → update check (HTML forms use POST)
POST /checks/{id}/delete      → delete check
POST /checks/{id}/pause       → toggle pause
GET  /channels                → list notification channels
POST /channels                → create channel
POST /channels/{id}/delete    → delete channel
```

> HTML forms do not support PUT/DELETE natively. The dashboard uses POST-method overrides with hidden `_method` fields, interpreted by a middleware.

### REST API (Month 3, protected by basic auth)

```
GET    /api/v1/checks              → list checks (supports ?tag=, ?status=)
POST   /api/v1/checks              → create check
GET    /api/v1/checks/{id}         → get check
PUT    /api/v1/checks/{id}         → update check
DELETE /api/v1/checks/{id}         → delete check
GET    /api/v1/checks/{id}/pings   → ping history (paginated)
GET    /api/v1/channels            → list channels
POST   /api/v1/channels            → create channel
DELETE /api/v1/channels/{id}       → delete channel
GET    /api/v1/stats               → aggregate counts by status
```

**Pagination:** cursor-based using `?before={id}&limit={n}` (default limit 50, max 200).

**Error format:**
```json
{
  "error": "check not found",
  "code": "NOT_FOUND"
}
```

---

## 6. Concurrency Model

### Goroutine Architecture

```
main goroutine
│
├── http.ListenAndServe()           ← serves all HTTP traffic
│   └── per-request goroutines      ← spun by net/http (pooled)
│       └── reads StateCache (RLock)
│           └── writes StateCache + DB (Lock)
│
├── scheduler goroutine             ← single, owned by Scheduler
│   ├── time.Ticker (30s)
│   │   └── evaluateAll()           ← two-phase; Lock() for transitions only
│   │       ├── Phase 1 (Lock held): up→down transitions persisted atomically
│   │       └── Phase 2 (Lock released): channel lookups + ch <- AlertEvent
│   └── time.Ticker (1h)
│       └── cleanupOldPings()       ← DELETE pings beyond 1,000-row limit per check
│
└── notifier goroutine              ← single, owned by NotifierWorker
    └── for event := range ch
        └── dispatch(event)         ← SMTP/HTTP calls here, not in scheduler
```

### State Cache

```go
type StateCache struct {
    mu     sync.RWMutex
    checks map[string]*Check  // keyed by UUID
}
```

- HTTP ping handlers: `Lock()` → update check + write to SQLite → `Unlock()`
- HTTP read handlers: `RLock()` → read → `RUnlock()`
- Scheduler: `Lock()` → scan all checks and apply any transitions → `Unlock()`

`evaluateAll()` uses a deliberate two-phase design to balance correctness and lock contention:

- **Phase 1 (write lock held):** iterate all checks, skip those that are not `"up"` or not yet overdue, and atomically persist each `up → down` transition via the `WithWriteLock` update closure. Collect each transitioned check into a local slice. The write lock is then released. This phase contains only in-memory comparisons and SQLite `UPDATE` calls — no network I/O.
- **Phase 2 (write lock released):** for each transitioned check, call `channelRepo.ListByCheckID` and enqueue one `AlertEvent` per channel via a non-blocking send. Moving the repository query and channel send outside the lock prevents DB latency from blocking concurrent ping handlers that also need the write lock.

This eliminates the TOCTOU window that would exist between a read scan and a separate write transition, while ensuring the write lock is held only for the DB persists, not for subsequent network-bound work.

**Write-through, not write-behind:** every state mutation writes to SQLite in the same transaction before releasing the lock. The cache is always consistent with the database. On startup, the cache is hydrated from SQLite.

### Notification Channel

```go
alertCh := make(chan AlertEvent, 64)  // buffered
```

Buffer size of 64 prevents the scheduler from blocking if the notifier is temporarily slow (e.g., SMTP timeout). If the buffer fills, new alert events are dropped with a log warning — this is acceptable because the scheduler will re-evaluate on the next tick and re-queue if the check is still down.

### Graceful Shutdown

```go
ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
defer stop()

// 1. Stop accepting new HTTP connections (10s drain)
srv.Shutdown(shutdownCtx)
// 2. Stop scheduler ticker
scheduler.Stop()
// 3. Close alert channel, wait for notifier to drain
close(alertCh)
notifierDone.Wait()
// 4. Close DB
db.Close()
```

---

## 7. Check State Machine

```
                    ┌──────────┐
                    │   new    │  ← initial state on creation
                    └────┬─────┘
                         │ success or fail ping received
                         ▼
               ┌─────────────────┐
         ┌────►│       up        │◄────────────────────┐
         │     └────────┬────────┘                     │
         │              │ deadline exceeded             │ success or fail
         │              │ (now > next_expected_at)      │ ping received
         │              ▼                              │
         │     ┌─────────────────┐                     │
         │     │      down       │─────────────────────┘
         │     └─────────────────┘        "recovery"
         │
         └─── manual unpause
              ┌──────────────┐
              │    paused    │  ← scheduler ignores paused checks
              └──────────────┘
```

> **Note on `/start` ping:** A `/start` ping does **not** change the check's `status`. It only extends `next_expected_at = now + grace_minutes` to give the running job its full grace window. A `down` check receiving `/start` stays `down`; only a subsequent success or fail ping transitions it to `up` and fires a recovery alert. The semantic is: `/start` means "I've started, not yet done." `/ping` (success) or `/fail` means "I completed a run."

### Transition Rules

| From | To | Trigger | Alert? |
|------|----|---------|--------|
| `new` | `up` | Success or fail ping received | No |
| `up` | `down` | `now > next_expected_at` (scheduler tick) | Yes — **down** alert |
| `down` | `up` | Success or fail ping received | Yes — **recovery** alert |
| `up` | `up` | `/fail` ping received, `notify_on_fail = true` | Yes — **fail** alert (status unchanged) |
| `new` or `down` | *(no change)* | `/start` ping received | No — status unchanged; only extends `next_expected_at` |
| `up` | `paused` | Manual pause (dashboard/API) | No |
| `down` | `paused` | Manual pause | No |
| `paused` | `up` | Manual unpause; ping received while paused | No |
| `new` | `paused` | Manual pause | No |

### Startup Reconciliation

On startup, after hydrating the cache from SQLite, the scheduler runs a **reconciliation pass** before beginning the tick loop:

1. For every check with `status = 'up'` where `now > next_expected_at`: transition to `down` and fire alerts.
2. For every check with `status = 'down'`: no action (already alerted before shutdown).

This prevents the silent failure window that would occur if CronMon was restarted after being down for hours.

### Grace Period

- Minimum: 1 minute (enforced at validation layer)
- Default: 10 minutes
- The `next_expected_at` value already incorporates the grace period:
  ```
  next_expected_at = cron_next_after(schedule, last_ping_at) + grace_minutes
  ```

---

## 8. Notification Pipeline

### Notifier Interface

```go
type AlertEvent struct {
    Check     Check
    Channel   Channel
    AlertType string  // "down" | "up" | "fail"
}

type Notifier interface {
    Send(ctx context.Context, event AlertEvent) error
    Type() string  // "email" | "slack" | "webhook"
}
```

### Dispatch Flow

```
Scheduler detects check transition
    → constructs AlertEvent for each subscribed channel
    → sends to buffered alertCh (non-blocking)

NotifierWorker (goroutine)
    → receives from alertCh
    → looks up Notifier by channel.Type
    → calls Notifier.Send() with 15s timeout context
    → writes result to notifications table (success or error)
    → logs outcome via slog
```

### Email (SMTP)

- Uses `net/smtp` stdlib — no third-party dependency.
- Sends over TLS (STARTTLS on port 587 by default).
- Subject: `[CronMon] ⚠ Check "Database backup" is DOWN` / `✓ RECOVERED`.
- Retry: none in v1 (the notification record captures the error; operator can see it in the dashboard).

### Slack Webhook

- HTTP POST to Slack incoming webhook URL.
- Payload:
  ```json
  {
    "text": "⚠ *[CronMon]* Check _Database backup_ is *DOWN*",
    "attachments": [{"text": "Expected at 2026-03-11T02:10:00Z. Ping URL: https://..."}]
  }
  ```

### Generic Webhook

- HTTP POST to configured URL.
- Content-Type: `application/json`
- Payload:
  ```json
  {
    "event":    "down",
    "check_id": "a3f9c2d1-...",
    "name":     "Database backup",
    "status":   "down",
    "expected_at": "2026-03-11T02:10:00Z",
    "ping_url": "https://cronmon.myserver.com/ping/a3f9c2d1-..."
  }
  ```
- Timeout: 15 seconds.

---

## 9. Security Architecture

### Threat Model

| Threat | Mitigated By |
|--------|-------------|
| Unauthorized dashboard access | HTTP Basic Auth on all non-ping routes |
| Ping endpoint enumeration | UUIDs are 128-bit; 200 response for unknown UUIDs (no oracle) |
| SQL injection | Parameterized queries everywhere; ORM-free but `database/sql` placeholders |
| Channel config tampering | Config validated server-side on write; JSON schema per channel type |
| SSRF via webhook URL | Webhook URL resolved and the resulting IP checked against RFC 1918 / loopback ranges **at call time** (not just at save time) to prevent DNS rebinding attacks |
| Credential exposure in logs | `SMTP_PASS`, `ADMIN_PASS` never logged; config struct masks sensitive fields in `String()` |
| `X-Forwarded-For` spoofing | By default, `source_ip` is taken from `RemoteAddr`. `TRUSTED_PROXY=true` opt-in to read `X-Forwarded-For` — disabled unless explicitly configured. |
| Alert suppression via ping flood | A leaked UUID allows an attacker to send repeated success pings, continuously resetting `next_expected_at` and masking real failures. Mitigated by UUID rotation (operator responsibility, documented). No in-band rate limiting in v1 — acceptable for single-operator self-hosted deployment. |
| Reverse proxy caching of ping endpoints | `GET /ping/*` must reach the application on every call. Reverse proxy configurations must disable caching for the `/ping/` path prefix (see Deployment section for nginx/Caddy examples). |

### Basic Auth Middleware

```go
func BasicAuth(username, password string) func(http.Handler) http.Handler {
    // uses subtle.ConstantTimeCompare to prevent timing attacks
}
```

- Applied to all routes except `/ping/{uuid}[/*]`.
- Session tokens are intentionally out of scope for v1 (stateless basic auth per request).
- `ADMIN_PASS` must be set at startup; missing value is a fatal startup error.

### HTTPS

CronMon does not terminate TLS itself. The recommended deployment places it behind nginx or Caddy which handles TLS. This is clearly documented. The `BASE_URL` config enforces `https://` prefix when `REQUIRE_HTTPS=true` (default: false, but documented as strongly recommended).

---

## 10. Configuration

All configuration is via environment variables. A `.env` file is supported via `joho/godotenv` (loaded if `.env` exists; silently skipped if not). Command-line flags mirror env vars for ergonomics.

### Full Reference

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `PORT` | No | `8080` | HTTP listen port |
| `DB_PATH` | No | `./cronmon.db` | SQLite database file path |
| `BASE_URL` | Yes | — | Public URL (e.g. `https://cronmon.example.com`). Used in ping URLs and notification links. Must not have trailing slash. |
| `SCHEDULER_INTERVAL` | No | `30` | Scheduler tick interval in seconds. Minimum 10. |
| `ADMIN_USER` | No | `admin` | Dashboard login username |
| `ADMIN_PASS` | Yes | — | Dashboard login password. Startup fails if unset. |
| `SMTP_HOST` | No | — | SMTP server hostname. Required to send email alerts. |
| `SMTP_PORT` | No | `587` | SMTP server port |
| `SMTP_USER` | No | — | SMTP username |
| `SMTP_PASS` | No | — | SMTP password |
| `SMTP_FROM` | No | — | From address for alert emails |
| `SMTP_TLS` | No | `true` | Use STARTTLS. Set `false` for local/test SMTP. |
| `TRUSTED_PROXY` | No | `false` | Trust `X-Forwarded-For` header for `source_ip` recording |
| `REQUIRE_HTTPS` | No | `false` | Reject BASE_URL that does not start with `https://` |
| `LOG_LEVEL` | No | `info` | `debug`, `info`, `warn`, `error` |

### Startup Validation

At startup, before any goroutines are launched:

1. `BASE_URL` must be set and parseable as a URL.
2. `ADMIN_PASS` must be set and non-empty.
3. `SCHEDULER_INTERVAL` must be ≥ 10.
4. If any `SMTP_*` var is set, all required SMTP vars (`HOST`, `FROM`) must also be set.
5. SQLite file must be openable (creates if not exists).

Fatal errors emit a structured log line and exit with code 1.

---

## 11. Deployment Architecture

### Deployment Options

**Option 1 — Direct binary (recommended for simplicity)**
```bash
export BASE_URL=https://cronmon.example.com
export ADMIN_PASS=changeme
./cronmon
```

**Option 2 — Docker**
```bash
docker run -d \
  -p 8080:8080 \
  -v /data/cronmon:/data \
  -e BASE_URL=https://cronmon.example.com \
  -e ADMIN_PASS=changeme \
  -e DB_PATH=/data/cronmon.db \
  yourname/cronmon:latest
```

**Option 3 — Docker Compose (with Caddy for TLS)**
```yaml
services:
  cronmon:
    image: myrrolinz/cronmon:latest
    restart: unless-stopped
    volumes:
      - ./data:/data
    environment:
      BASE_URL: https://cronmon.example.com
      ADMIN_PASS: ${ADMIN_PASS}
      DB_PATH: /data/cronmon.db
    expose:
      - "8080"

  caddy:
    image: caddy:2-alpine
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile
      - caddy_data:/data
```

### Reverse Proxy (nginx example)

```nginx
server {
    listen 443 ssl;
    server_name cronmon.example.com;

    # Ping endpoints MUST NOT be cached — every request must reach the application.
    location /ping/ {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header Host $host;
        proxy_no_cache 1;
        proxy_cache_bypass 1;
    }

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header Host $host;
    }
}
```

Set `TRUSTED_PROXY=true` if using `X-Forwarded-For` for accurate source IP recording.

### Backup

```bash
# SQLite backup — zero downtime (SQLite WAL mode supports online backup)
sqlite3 cronmon.db ".backup cronmon.$(date +%Y%m%d).db"

# Or simple copy (safe if cronmon is stopped)
cp cronmon.db cronmon.$(date +%Y%m%d).db
```

CronMon opens SQLite in WAL mode (`PRAGMA journal_mode=WAL`) to enable online backups without stopping the process.

---

## 12. Build & Release Pipeline

### Go Module

```
module github.com/myrrolinz/cronmon

go 1.22
```

### Build Targets

```makefile
LDFLAGS := -ldflags "-s -w -X main.version=$(VERSION)"

build-linux-amd64:
    GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o dist/cronmon-linux-amd64 .

build-linux-arm64:
    GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o dist/cronmon-linux-arm64 .

build-darwin-amd64:
    GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o dist/cronmon-darwin-amd64 .

build-darwin-arm64:
    GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o dist/cronmon-darwin-arm64 .

build-windows-amd64:
    GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o dist/cronmon-windows-amd64.exe .
```

`-s -w` strips debug symbols, reducing binary size. `modernc.org/sqlite` is pure Go, so all targets compile without CGO.

### CI (GitHub Actions)

```
on: [push, pull_request]

jobs:
  test:
    - go vet ./...
    - golangci-lint run
    - go test -race -coverprofile=coverage.out ./...
    - go tool cover -func=coverage.out  (must be ≥ 80%)

  build:
    needs: test
    - cross-compile all 5 targets
    - upload artifacts

  release:
    on: push to v* tags
    - build all targets
    - create GitHub release with binary attachments
    - push Docker image to Docker Hub
```

### Docker Image

```dockerfile
FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /app/cronmon /cronmon
COPY --from=builder /app/web /web
EXPOSE 8080
ENTRYPOINT ["/cronmon"]
```

`FROM scratch` with a statically-linked binary gives a minimal attack surface and sub-10 MB image.

> Note: embedded assets with `go:embed` are compiled into the binary, so the `COPY /app/web` step is only needed if static files are not embedded. They should be embedded in production builds.

---

## 13. Architecture Decision Records

### ADR-001: Pure Go SQLite Driver

**Context:** SQLite requires a C binding, which breaks cross-compilation (`CGO_ENABLED=0`).

**Decision:** Use `modernc.org/sqlite` — a pure Go port of SQLite generated by transpiling the SQLite C source.

**Consequences:**
- ✅ Full cross-compilation to all 5 targets without a C toolchain
- ✅ `go build` works anywhere Go works
- ⚠ ~5–10% slower than the CGO driver (`mattn/go-sqlite3`) for CPU-bound queries — acceptable for our write volume
- ⚠ Slightly larger binary size

**Alternatives rejected:** `mattn/go-sqlite3` (requires CGO), PostgreSQL (violates no-external-service constraint), BoltDB (not SQL, loses relational model).

---

### ADR-002: In-Memory State Cache

**Context:** The scheduler and HTTP handlers both need to read check state at high frequency. SQLite reads on every handler invocation would be slow and create unnecessary lock contention.

**Decision:** Maintain an in-memory `map[uuid]*Check` protected by `sync.RWMutex`. All mutations write through to SQLite in the same critical section.

**Consequences:**
- ✅ Sub-microsecond reads for ping handlers and scheduler evaluation
- ✅ No caching inconsistency — write-through guarantees SQLite is always current
- ⚠ Memory grows linearly with check count — acceptable (1 million checks ≈ ~200 MB; realistic deployment is hundreds of checks)
- ⚠ On process crash between cache update and SQLite write: impossible — they are atomic under the same Lock()

---

### ADR-003: Scheduler Does No Network I/O

**Context:** Scheduler goroutine evaluates all checks every 30 seconds. If it blocks on SMTP or HTTP calls for a slow or down notification endpoint, it could miss subsequent evaluation cycles.

**Decision:** Scheduler sends `AlertEvent` to a buffered channel (`cap=64`). A separate notifier goroutine performs all outbound network operations.

**Consequences:**
- ✅ Scheduler evaluation latency is bounded (memory + mutex only)
- ✅ Slow notification endpoints cannot delay alert detection
- ⚠ If buffer fills (64 pending alerts), new alerts are dropped with a log warning. In practice, this requires 64 simultaneous channel failures, which is far beyond realistic deployment.

---

### ADR-004: Ping Endpoints Are Unauthenticated

**Context:** Ping endpoints must be callable from cron via a single `curl` command without managing credentials.

**Decision:** The UUID in `/ping/{uuid}` is the credential. Endpoints return 200 for unknown UUIDs to prevent existence enumeration.

**Consequences:**
- ✅ Zero-friction integration — one curl command, no headers, no auth setup
- ✅ No oracle for scanners — 200 for any UUID
- ⚠ UUID must be kept secret. A flood of success pings to a valid UUID continuously resets `next_expected_at`, keeping the check in "up" state and suppressing real failure alerts. Mitigation: UUID rotation (operator responsibility, documented in the dashboard).

---

### ADR-005: Server-Side Rendering for Dashboard (Month 1–2)

**Context:** Dashboard could be SPA (React/Vue) or server-rendered Go templates.

**Decision:** Go `html/template` for Month 1–2. Month 3 adds REST API; the dashboard may optionally be decoupled to consume it.

**Consequences:**
- ✅ No build step, no npm, embedded directly in binary
- ✅ Consistent with "single binary" philosophy
- ✅ Faster to build the core product
- ⚠ Interactive UI (real-time updates, filtering) is harder without JS. Acceptable for a monitoring dashboard that refreshes on page load.
- ⚠ Month 3 "dashboard consumes REST API" is a partial rewrite. Decision: keep SSR for dashboard permanently; REST API is for programmatic access only. This avoids the rewrite.

**Revised:** The REST API in Month 3 is for programmatic/external consumer use. The dashboard remains server-rendered. This is a material change from the original design doc.

---

## 14. Open Issues

These items require decisions before or during development. Open issues become ADRs when resolved.

| ID | Status | Issue | Resolution |
|----|--------|-------|------------|
| OI-1 | ✅ RESOLVED | **Basic auth is Month 2, but Month 1 claims to be "deployable."** A deployable tool without auth is a security hole. | Basic auth is implemented in Month 1 alongside the check CRUD handlers. ~50 lines of middleware with `subtle.ConstantTimeCompare`. |
| OI-2 | ✅ RESOLVED | **Minimum grace period.** A 1-minute grace with 30-second scheduler tick is technically viable but confusing to users. | Minimum grace enforced at 1 minute at the validation layer. Documented that the scheduler runs every 30 seconds. |
| OI-3 | ✅ RESOLVED | **How does `source_ip` handle requests behind nginx/Caddy?** `RemoteAddr` will be `127.0.0.1`. | `TRUSTED_PROXY=true` config flag enables reading `X-Forwarded-For`. Disabled by default to prevent header spoofing. |
| OI-4 | ✅ RESOLVED | **`/start` ping causes false "down" alerts for long-running jobs.** The original design left `next_expected_at` unchanged on `/start`, so a job running past `next_expected_at` would trigger a spurious alert. | `/start` now sets `next_expected_at = now + grace_minutes`, extending the deadline from the moment the job starts. The grace period doubles as max-allowed-runtime when start pings are used. A separate `max_duration_minutes` field is deferred to a future release. |
| OI-5 | ✅ RESOLVED | **Slug strategy.** Pure UUID (opaque) or human-readable slug for ping URLs? | UUIDs for ping URLs (security through obscurity per ADR-004). Slugs are reserved in the schema but unused in v1. |
| OI-6 | Open | **Notification delivery retry.** No retry in v1 means a transient SMTP failure produces a missed alert. | Accept for v1. The `notifications.error` column records failures visible in the dashboard. Retry / dead-letter queue is Month 3 backlog. |
| OI-7 | ✅ RESOLVED | **Dashboard REST API separation (Month 3).** Original design says dashboard "consumes REST API." This is a significant rewrite. | Per ADR-005: REST API is external-facing only. Dashboard stays SSR permanently. Eliminates the rewrite risk. |
