# CronMon — Go-Based Cron Job Monitor
> A lightweight, self-hosted cron job monitor. The spiritual Go successor to minicron.

---

## 1. Project Background

### Problem
Developers running cron jobs on servers have no reliable way to know when a job silently fails or stops running. Discovering that backups haven't run for a week — after something goes wrong — is a common and painful experience.

### Why This Project
- **minicron** (the closest prior art) has 2.3k GitHub stars but was archived in 2021 and written in Ruby. Its users are orphaned.
- **healthchecks.io** is mature and excellent but requires a full Python/Django stack to self-host. Most people end up using their SaaS tier with its limitations.
- **SaaS alternatives** (Cronitor, Cronhub, Dead Man's Snitch) cost money and send your job metadata to third parties.

### Our Value Proposition
A single Go binary. No Python. No Node. No database server. Drop it on a $5 VPS, run it, done.

### Target Audience
Developers and sysadmins who self-host, run their own VPS, and want something simple that just works. Realistic goal: **300 real users**. Not competing with healthchecks.io — inheriting minicron's orphaned audience.

---

## 2. Core Concept

### Approach: "Push" (Dead Man's Switch)

The cron job itself is responsible for pinging the monitor after it completes. If no ping arrives within the expected window, the monitor alerts you.

This means:
- No agent to install on your servers
- Works with any language, any cron implementation
- Integration is literally one `curl` command appended to an existing cron job

```bash
# Before
0 2 * * * /scripts/backup.sh

# After
0 2 * * * /scripts/backup.sh && curl -s https://cronmon.myserver.com/ping/a3f9c2d1
```

### The Check — Core Domain Object

Everything in the system revolves around a single concept: a **Check**.

```
Name:           "Database backup"
Schedule:       "0 2 * * *"    (standard cron syntax)
Grace period:   10 minutes     (wait this long after expected time before alerting)
Status:         Up | Down | New | Paused
Last ping:      2025-02-25 02:01:43
Next expected:  2025-02-26 02:00:00
Ping URL:       https://yourhost/ping/a3f9c2d1-...
```

**The grace period is critical.** Jobs don't always finish exactly on time. Don't alert the moment a ping is late — wait a reasonable buffer first.

### The Three-Ping Model

Each check supports three ping endpoints:

| Endpoint | Meaning |
|----------|---------|
| `/ping/{uuid}` | Job completed successfully |
| `/ping/{uuid}/start` | Job has started (optional, enables duration tracking) |
| `/ping/{uuid}/fail` | Job explicitly failed |

This three-state model lets users signal intent, not just presence — a deliberate improvement over minicron.

---

## 3. Feature Scope

### Month 1 — Must Have (Core, Functional)

- **Check management** — Create, edit, delete, pause checks via web UI
- **Ping endpoints** — Receive pings at `/ping/{uuid}`, `/ping/{uuid}/start`, `/ping/{uuid}/fail`
- **Background scheduler** — Continuously evaluates all checks, transitions Up → Down when window is missed
- **Email alerting** — Alert on state change (Up → Down), and recovery notification (Down → Up)
- **Web dashboard** — Single-page view: all checks, status, last ping, next expected time. Embedded into binary via Go's `embed` package.
- **SQLite storage** — Zero external dependencies. Single file on disk.

**End of Month 1 milestone**: A working, genuinely deployable tool.

---

### Month 2 — Should Have (Polished, Useful)

- **Ping history** — Store last N pings per check. Show a visual history (30 green/red squares) on check detail page.
- **Multiple notification channels**:
  - Slack webhook (POST to a Slack incoming webhook URL)
  - Generic webhook (POST JSON payload to any URL — enables every other integration without building each one)
- **Basic authentication** — Username/password to protect the dashboard. Required before anyone can deploy this publicly.
- **Environment variable config** — All settings via env vars (`PORT`, `DB_PATH`, `BASE_URL`, `SMTP_HOST`, etc). Twelve-factor app compatible.
- **Check tags** — Tag checks and filter dashboard by tag. Useful once users have >10 checks.
- **Recovery notifications** — Explicitly: when a Down check receives a ping, send "recovered" alert. Often forgotten, always appreciated.

**End of Month 2 milestone**: Something worth posting on r/selfhosted.

---

### Month 3 — Nice to Have (Ecosystem, Polish)

- **REST API** — Create/read/update/delete checks programmatically. The dashboard itself should consume this API.
- **Prometheus metrics** — `/metrics` endpoint in Prometheus exposition format. Small feature, outsized appeal to the DevOps audience.
- **Docker image** — Published to Docker Hub. One-line deploy.
- **Documentation site** — Clear README, getting started guide, configuration reference.

**End of Month 3 milestone**: Something worth posting on Hacker News.

---

### Explicitly Out of Scope

- Multi-user / teams — scope explosion, leave it out of v1
- SMS / PagerDuty — nice eventually, not for v1
- Running the cron jobs themselves — this is a monitor, not a scheduler
- SSH agents — keep it push-only

---

## 4. Architecture

### High-Level Components

```
┌─────────────────────────────────────────────────────┐
│                   Single Go Binary                   │
│                                                      │
│  ┌─────────────┐   ┌──────────────┐  ┌───────────┐  │
│  │  HTTP Server│   │  Scheduler   │  │ Notifier  │  │
│  │             │   │  (goroutine) │  │           │  │
│  │ /ping/{id}  │   │              │  │  Email    │  │
│  │ /dashboard  │   │ Checks state │  │  Slack    │  │
│  │ /api/...    │   │ transitions  │  │  Webhook  │  │
│  └──────┬──────┘   └──────┬───────┘  └─────┬─────┘  │
│         │                 │                │         │
│         └─────────────────┴────────────────┘         │
│                           │                          │
│                    ┌──────▼──────┐                   │
│                    │   SQLite    │                    │
│                    │  (one file) │                    │
│                    └─────────────┘                   │
└─────────────────────────────────────────────────────┘
```

### Key Design Decisions

**Single binary** — The web dashboard HTML/CSS/JS is embedded into the binary using Go's `embed` package. Zero files to manage at runtime beyond the database.

**SQLite** — No PostgreSQL, no MySQL, no Redis. One file. Backing up your entire monitoring setup is `cp data.db data.db.bak`. For the audience size we're targeting (individuals, small teams), SQLite handles the load trivially.

**In-memory state cache** — Check statuses are cached in memory (protected by `sync.RWMutex`) and written through to SQLite. The scheduler reads from memory; the HTTP handlers read from memory. SQLite is the source of truth on startup and for history.

**Background scheduler** — A single goroutine running on a tick (every 30 seconds) that evaluates all checks and fires notifications through a channel to a separate notification worker goroutine. Scheduler never blocks on network I/O.

---

## 5. Data Model

```sql
-- Core check definition
CREATE TABLE checks (
    id          TEXT PRIMARY KEY,  -- UUID
    name        TEXT NOT NULL,
    slug        TEXT UNIQUE,       -- human-readable URL fragment
    schedule    TEXT NOT NULL,     -- cron expression e.g. "0 2 * * *"
    grace       INTEGER NOT NULL,  -- grace period in minutes
    status      TEXT NOT NULL DEFAULT 'new',  -- new|up|down|paused
    last_ping   DATETIME,
    next_ping   DATETIME,          -- computed from schedule
    created_at  DATETIME NOT NULL,
    tags        TEXT               -- comma-separated
);

-- Every ping received
CREATE TABLE pings (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    check_id    TEXT NOT NULL REFERENCES checks(id),
    type        TEXT NOT NULL,     -- success|start|fail
    created_at  DATETIME NOT NULL,
    source_ip   TEXT
);

-- Notification channel configuration
CREATE TABLE channels (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    type        TEXT NOT NULL,     -- email|slack|webhook
    name        TEXT NOT NULL,
    config      TEXT NOT NULL,     -- JSON: {"address": "..."} or {"url": "..."}
    created_at  DATETIME NOT NULL
);

-- Many-to-many: which checks notify which channels
CREATE TABLE check_channels (
    check_id    TEXT NOT NULL REFERENCES checks(id),
    channel_id  INTEGER NOT NULL REFERENCES channels(id),
    PRIMARY KEY (check_id, channel_id)
);

-- Alert history
CREATE TABLE notifications (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    check_id    TEXT NOT NULL REFERENCES checks(id),
    channel_id  INTEGER NOT NULL REFERENCES channels(id),
    type        TEXT NOT NULL,     -- down|up
    sent_at     DATETIME NOT NULL,
    error       TEXT               -- null if delivered successfully
);
```

## 6. Deployment Story

Users should be able to deploy in under 5 minutes via any of these methods:

```bash
# Option 1: Direct binary (Linux/macOS/Windows)
./cronmon --port 8080 --db ./cronmon.db

# Option 2: Docker
docker run -p 8080:8080 -v $(pwd)/data:/data myrrolinz/cronmon

# Option 3: Docker Compose
curl -O https://raw.githubusercontent.com/myrrolinz/cronmon/main/docker-compose.yml
docker-compose up -d
```

### Environment Variable Reference (planned)

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | HTTP listen port |
| `DB_PATH` | `./cronmon.db` | SQLite database file path |
| `BASE_URL` | *(required)* | Public URL, used in ping URLs and emails |
| `SMTP_HOST` | — | SMTP server hostname |
| `SMTP_PORT` | `587` | SMTP port |
| `SMTP_USER` | — | SMTP username |
| `SMTP_PASS` | — | SMTP password |
| `SMTP_FROM` | — | From address for alert emails |
| `ADMIN_USER` | `admin` | Dashboard login username |
| `ADMIN_PASS` | *(required)* | Dashboard login password |

---

## 7. Build Roadmap

### Month 1 — Foundation
- [ ] Project scaffolding, module setup, directory structure
- [ ] SQLite schema, database layer (`database/sql`)
- [ ] Check CRUD — data layer + HTTP API
- [ ] Ping endpoint — receive and store pings
- [ ] Background scheduler goroutine — detect missed checks
- [ ] Email alerting on state change
- [ ] Minimal web dashboard (embedded HTML, no JS framework)
- [ ] Basic auth middleware
- [ ] Docker image

### Month 2 — Polish
- [ ] Ping history — storage + visual history on dashboard
- [ ] Slack webhook notifications
- [ ] Generic webhook notifications
- [ ] `Notifier` interface refactor (clean up Month 1 email code)
- [ ] Check tags + dashboard filtering
- [ ] Env var configuration with validation and startup error messages
- [ ] Recovery (Down → Up) notifications
- [ ] Graceful shutdown (handle SIGTERM correctly)

### Month 3 — Ecosystem
- [ ] REST API (checks, channels, ping history, stats)
- [ ] Dashboard consumes REST API (decouple frontend from backend)
- [ ] Prometheus `/metrics` endpoint
- [ ] README overhaul — installation, configuration, screenshots
- [ ] GitHub Actions CI — build, test, release binaries for Linux/macOS/Windows/ARM
- [ ] Post on r/selfhosted, minicron issues, Hacker News

---

## 8. Positioning & Differentiation

| | minicron | healthchecks.io (self-hosted) | **CronMon** |
|--|---------|------------------------------|-------------|
| Language | Ruby | Python / Django | **Go** |
| Self-host complexity | Requires Ruby runtime | Python + PostgreSQL/MySQL | **Single binary** |
| Maintained | ❌ Archived 2021 | ✅ Active | ✅ Active |
| Three-state pings | ❌ | ✅ | ✅ |
| Embedded dashboard | ❌ | ❌ | ✅ |
| External DB required | Optional | Required | ❌ (SQLite embedded) |

**Positioning statement**: "If you liked minicron but it's dead, or you want healthchecks.io without the Python stack — this is for you."

---

## 9. Open Questions (To Revisit)

- What should the slug/UUID strategy be for ping URLs? Pure UUID (opaque) or human-readable slug?
- Should checks support multiple schedules? (e.g. run twice a day) — probably no for v1
- What's the right default grace period? (10 minutes seems reasonable)
- Should we support HTTPS natively or document nginx/Caddy as a reverse proxy?
- Ping URL format: path-based (`/ping/{uuid}`) or subdomain-based? Path-based is simpler.

## 10. Stacks

| Layer              | Choice                        | Why                                                    |
| ------------------ | ----------------------------- | ------------------------------------------------------ |
| HTTP               | `net/http` (stdlib)           | Learn fundamentals, 10 routes doesn't need a framework |
| Routing            | Go 1.22 built-in patterns     | Path params now native in stdlib                       |
| Database interface | `database/sql` (stdlib)       | Standard, teaches SQL properly                         |
| SQLite driver      | `modernc.org/sqlite`          | Pure Go, enables cross-compilation                     |
| Cron parsing       | `github.com/robfig/cron/v3`   | De facto standard, don't reinvent this                 |
| Email              | `net/smtp` (stdlib)           | 20 lines, no dependency needed                         |
| Templates          | `html/template` (stdlib)      | Server-side rendering, embeds into binary              |
| Static assets      | `embed` (stdlib)              | Binary includes all UI files                           |
| CSS                | Pico.css (local file)         | Clean UI with zero build step                          |
| Config             | `os` + `joho/godotenv`        | Twelve-factor, minimal                                 |
| Logging            | `log/slog` (stdlib)           | Added in Go 1.21, structured, built-in                 |
| Testing            | `testing` (stdlib) + `go-cmp` | Standard + clean deep equality                         |

**Total external dependencies: 3** (`modernc.org/sqlite`, `robfig/cron`, `joho/godotenv`). Everything else is standard library.

---

*Last updated: 2026-02-26*
*Status: Pre-development — design phase*
