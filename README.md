# CronMon

> A lightweight, self-hosted cron job monitor. The spiritual Go successor to [minicron](https://github.com/jamesrwhite/minicron).

**Status:** Pre-development — design phase

---

## What is CronMon?

CronMon monitors your cron jobs using a **dead man's switch** (push-based) model. Your cron job pings CronMon after it completes — if no ping arrives within the expected window plus a configurable grace period, you get alerted.

Integration is a single `curl` command appended to any existing cron job:

```bash
# Before
0 2 * * * /scripts/backup.sh

# After
0 2 * * * /scripts/backup.sh && curl -s https://cronmon.example.com/ping/your-uuid
```

## Why CronMon?

| | minicron | healthchecks.io (self-hosted) | **CronMon** |
|--|---------|------------------------------|-------------|
| Language | Ruby | Python / Django | **Go** |
| Self-host complexity | Requires Ruby runtime | Python + PostgreSQL | **Single binary** |
| Maintained | ❌ Archived 2021 | ✅ Active | ✅ Active |
| Three-state pings | ❌ | ✅ | ✅ |
| External DB required | Optional | Required | ❌ (SQLite embedded) |

## Key Features (Planned)

- **Single binary** — drop it on a VPS and run it. No Docker required (but Docker supported).
- **SQLite storage** — no database server. One file on disk.
- **Three-state pings** — `/ping/{uuid}`, `/ping/{uuid}/start`, `/ping/{uuid}/fail`
- **Email, Slack & webhook alerts** — notified on failure and recovery
- **Web dashboard** — embedded in the binary, no separate frontend to deploy
- **Environment variable config** — twelve-factor compatible

## Quick Start (Planned)

```bash
# Option 1: Direct binary
export BASE_URL=https://cronmon.example.com
export ADMIN_PASS=changeme
./cronmon

# Option 2: Docker
docker run -d \
  -p 8080:8080 \
  -v $(pwd)/data:/data \
  -e BASE_URL=https://cronmon.example.com \
  -e ADMIN_PASS=changeme \
  myrrolinz/cronmon
```

## Tech Stack

| Layer | Choice |
|---|---|
| Language | Go 1.22+ |
| HTTP | `net/http` stdlib |
| Database | SQLite via `modernc.org/sqlite` (pure Go) |
| Cron parsing | `github.com/robfig/cron/v3` |
| Templates | `html/template` stdlib |
| Config | env vars + `joho/godotenv` |
| Logging | `log/slog` stdlib |

**Total external dependencies: 3.** Everything else is standard library.

## Roadmap

### Month 1 — Core
Check management, ping endpoints, background scheduler, email alerts, web dashboard, basic auth, Docker image.

### Month 2 — Polish
Ping history, Slack & webhook notifications, check tags, env var config, recovery notifications, graceful shutdown.

### Month 3 — Ecosystem
REST API, Prometheus `/metrics`, GitHub Actions releases, documentation site.

## Documentation

- [Design Document](doc/CRONMON_DESIGN.md)
- [Architecture](doc/ARCHITECTURE.md)

---

*Pre-development. Nothing is deployable yet.*
