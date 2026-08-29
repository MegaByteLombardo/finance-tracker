# Milestones

The living roadmap. This file is updated as milestones open and close.

**Status values**

| Status | Meaning |
| --- | --- |
| `PLANNED` | On the roadmap. Scope not yet defined or approved. |
| `IN PROGRESS` | Scope approved, implementation under way. |
| `CLOSED` | Delivered, verified against its acceptance criteria, and documented. |

---

## M0 — Foundation · `CLOSED`

**Closed:** 2026-08-29

The technical foundation, with **no financial domain whatsoever**.

### Delivered

- **Bootstrap** — Go 1.27 module, one direct dependency (`modernc.org/sqlite`
  v1.57.0), Go-appropriate `.gitignore` replacing the inherited Python template.
- **Configuration** — `APP_ENV`, `HTTP_ADDR`, `DB_PATH`, `LOG_LEVEL` from the
  environment, with safe defaults, validation, and every problem reported at
  once. Errors, never panics. No configuration library.
- **SQLite** — opened through `database/sql` with `modernc.org/sqlite`, parent
  directory created on demand, connection verified at startup.
- **WAL and PRAGMAs** — `journal_mode = WAL` applied once and its returned value
  checked; `foreign_keys = ON`, `busy_timeout = 5000` and `synchronous = FULL`
  carried in the DSN so they apply to every pooled connection. Pool pinned to a
  single connection with unlimited lifetimes.
- **`/healthz`** — `200` when the process is alive and SQLite answers, `503`
  when it does not, with the failure logged.
- **Graceful shutdown** — `SIGINT` and `SIGTERM` drain in-flight requests under
  a bounded timeout, then close the database last.
- **Docker** — multi-stage build, `CGO_ENABLED=0` static binary, distroless
  runtime image of ~5.6 MB containing no source and no Go toolchain.
- **Persistence** — SQLite lives on a named volume, never in the container's
  writable layer. `docker compose down` keeps it; `docker compose down -v`
  destroys it.
- **Non-root runtime** — the process runs as uid 65532 and writes to the volume,
  verified against a real container rather than assumed.
- **Tests** — configuration, database and HTTP suites, plus a full
  startup/shutdown cycle. Database tests run against real SQLite in temporary
  directories; the driver is not mocked.
- **Documentation** — root `README.md`, `CLAUDE.md`, and this `docs/` set.

### Verification

All **27 acceptance criteria** defined for M0 were verified, covering build and
quality gates, configuration behaviour, the effective SQLite settings, HTTP
behaviour, Docker packaging and persistence, documentation, and the absence of
any financial domain.

Quality gates passing at close: `gofmt -l .` silent, `go vet ./...` clean,
`go test ./... -count=1` green, `go test -race ./...` green in the Linux build
container, `docker compose build` successful, and `GET /healthz` returning `200`
both locally and in the container.

Two risks identified during planning were closed empirically rather than by
argument:

- The `file:///…` DSN form works on both Windows and Linux, so no
  connection-hook fallback was needed.
- The named volume inherits the image directory's ownership, so the non-root
  process can write to `/data`. Confirmed by running the real container.

---

## Roadmap

Everything below is **provisional**. These are the currently expected shape of
the work, not commitments. Scope, ordering and boundaries may all change as
earlier milestones teach us something.

**The exact scope and acceptance criteria of each milestone are defined and
approved immediately before it is implemented — never in advance.** Nothing
below has acceptance criteria yet, by design.

| Milestone | Theme | Status |
| --- | --- | --- |
| M1 | Accounts & Currencies | `PLANNED` |
| M2 | Transactions & Categories | `PLANNED` |
| M3 | Dashboard | `PLANNED` |
| M4 | Multi-Currency & FX | `PLANNED` |
| M5 | Transfers | `PLANNED` |
| M6 | Historical Net Worth | `PLANNED` |
| M7 | Credit & Debt | `PLANNED` |
| M8 | Instalments & Calendar | `PLANNED` |
| M9 | Analytics | `PLANNED` |
| M10 | Hardening | `PLANNED` |

### Known carry-over into M1

One decision was explicitly deferred from M0 and belongs to M1:

**Schema migrations.** M0 ships no migration machinery and no migration
dependency. M1 introduces migrations together with the first real domain table,
at which point the tooling, the file versioning scheme, when migrations run, how
partial failures are handled, and how `status` and `down` are exposed are all
decided against a real migration rather than a hypothetical one. See
[technical-decisions.md](technical-decisions.md), TD-012.

Two smaller items are also parked, neither blocking:

- **Container `HEALTHCHECK`.** Not added at M0: the distroless image has no
  shell and no `curl`. The clean approach is a self-check flag on the binary,
  worth adding when something is orchestrating restarts.
- **Request-logging middleware.** Deferred until there are endpoints worth
  tracing.
