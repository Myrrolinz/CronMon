# CronMon — Agent & Tooling Configuration

This directory contains the agents, prompts, skills, and rules selected for the CronMon project.

---

## Directory Layout

```
.github/
├── agents/               # Agent definitions (.agent.md)
│   └── prompts/          # Slash-command prompts (.prompt.md)
├── skills/               # Skill knowledge packs (SKILL.md per folder)
└── instructions/         # Coding rules / instructions (.instructions.md)
```

---

## Agents (`.github/agents/`)

| File | Purpose |
|------|---------|
| `architect.agent.md` | System design, component layout, ADRs (HTTP server, scheduler, notifier) |
| `planner.agent.md` | Break down Month 1–3 roadmap items into actionable steps |
| `go-reviewer.agent.md` | Idiomatic Go review — goroutines, error handling, interfaces |
| `go-build-resolver.agent.md` | Fix `go build`, `go vet`, and linter failures |
| `tdd-guide.agent.md` | Enforce test-first methodology, target ≥80% coverage |
| `code-reviewer.agent.md` | General code quality, maintainability, readability |
| `security-reviewer.agent.md` | OWASP Top 10 checks on ping endpoints, basic auth, webhook handlers |
| `database-reviewer.agent.md` | SQLite schema, migrations, query safety (SQL injection) |
| `doc-updater.agent.md` | Keep README, codemaps, and inline docs in sync |

---

## Prompts (`.github/agents/prompts/`)

| File | When to Use |
|------|------------|
| `plan.prompt.md` | Plan a new feature before coding |
| `tdd.prompt.md` | Start a TDD cycle (red → green → refactor) |
| `go-build.prompt.md` | Resolve a failing `go build` or `go vet` |
| `go-test.prompt.md` | Run and review test results |
| `go-review.prompt.md` | Trigger a Go-specific code review |
| `code-review.prompt.md` | Full code review pass |
| `test-coverage.prompt.md` | Check and improve test coverage |
| `refactor-clean.prompt.md` | Clean up code after feature is working |
| `verify.prompt.md` | Final verification pass before merging |

---

## Skills (`.github/skills/`)

| Skill | Why Included |
|-------|-------------|
| `backend-patterns` | Repository pattern, service layer, middleware — core to this project's architecture |
| `coding-standards` | Universal Go/TypeScript standards — consistency across the codebase |
| `api-design` | REST API design for the Month 3 `/api/...` endpoints |
| `security-review` | Auth middleware, ping endpoint validation, webhook security |
| `tdd-workflow` | Test-driven workflow for all new features |
| `verification-loop` | Pre-merge verification checklist |
| `search-first` | Research existing libraries before writing custom code |

---

## Instructions (`.github/instructions/`)

### Go-Specific
| File | Scope |
|------|-------|
| `golang-coding-style.instructions.md` | Formatting, naming, idiomatic Go |
| `golang-patterns.instructions.md` | Interfaces, embedding, concurrency patterns |
| `golang-security.instructions.md` | Input validation, SQL injection, secrets |
| `golang-testing.instructions.md` | Table-driven tests, `go-cmp`, test helpers |
| `golang-hooks.instructions.md` | Pre-commit hooks for `go vet`, `golangci-lint` |

### Common / Cross-Cutting
| File | Scope |
|------|-------|
| `common-agents.instructions.md` | How to orchestrate agents and sub-agents |
| `common-coding-style.instructions.md` | Style rules applying to all languages/files |
| `common-development-workflow.instructions.md` | Feature branches, PR process, CI gates |
| `common-git-workflow.instructions.md` | Commit message format, branching strategy |
| `common-testing.instructions.md` | Test requirements, naming, coverage thresholds |
| `common-security.instructions.md` | Security baselines for all code |
| `common-patterns.instructions.md` | Repository, service, middleware patterns |
| `common-performance.instructions.md` | Profiling, query optimization, caching |

---

## What Was Excluded & Why

| Resource | Reason Excluded |
|----------|----------------|
| `frontend-patterns` | React/Next.js skill — dashboard is server-rendered Go templates |
| `e2e-testing` | Playwright-based — no JS browser automation in v1 |
| `eval-harness` / `strategic-compact` | Meta-skills for agent management, not development |
| `frontend-slides` / `article-writing` / `content-engine` | Content creation, not applicable |
| `investor-materials` / `investor-outreach` / `market-research` | Business skills, not applicable |
| `python-*` instructions & agents | Wrong language stack |
| `kotlin-reviewer.agent.md` | Wrong language stack |
| `common-hooks.instructions.md` | Uses Node/husky hooks — Go project uses `golangci-lint` pre-commit |
