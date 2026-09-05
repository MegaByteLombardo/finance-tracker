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

## M1 — Accounts & Currencies · `CLOSED`

**Closed:** 2026-08-29

The first real domain: currencies and accounts, with schema migrations and the
`Domain → Service → Repository → SQLite` chain in place. No HTTP surface, no
balances, no exchange rates.

### Delivered

- **Migrations** — `github.com/pressly/goose/v3` v3.27.3 via the Provider API,
  SQL migrations embedded with `embed.FS`, applied automatically at startup
  before anything listens. Three migrations: create currencies, seed them,
  create accounts. `internal/migrations` is the only package that imports goose.
- **`currencies`** — STRICT table keyed on the ISO 4217 code, with constraints on
  the code shape, name, symbol and minor unit. Seeded with USD, EUR and VES.
- **`accounts`** — STRICT table with a mandatory, immutable currency, four
  container types (`BANK`, `CASH`, `WALLET`, `OTHER`), and `RESTRICT` on both
  sides of the foreign key. **No monetary column of any kind.**
- **Domain packages** — `internal/currency` and `internal/account`, each with a
  model, validation, a concrete repository and a service exposing exactly five
  use cases. No repository interfaces.
- **Transactional creation** — `account.Service.Create` reads the currency and
  inserts the account through repositories built over one `*sql.Tx`, so "no
  account is created with a retired currency" is an invariant rather than a race.
- **Encapsulated errors** — `account` translates currency failures into
  `ErrCurrencyNotFound` / `ErrCurrencyInactive`, so a future accounts handler
  never imports `currency` to interpret them.
- **Timestamp convention** — TEXT, RFC 3339, UTC, fixed-width, enforced by CHECK
  constraints. Applies project-wide from here.
- **Tests** — migration application, idempotence and failure; every schema
  constraint; both domains end to end. Real SQLite throughout.

### Verification

All 46 acceptance criteria passed. Quality gates at close: `gofmt -l .` silent,
`go vet ./...` clean, `go test ./... -count=1` green over six consecutive runs,
`go test -race ./...` green in the Linux container, `docker compose build`
successful, and `/healthz` returning 200 in the container.

Two risks identified during planning were closed by measurement:

- **goose's dependency graph.** Its `go.mod` requires ClickHouse, MySQL, pgx,
  MSSQL, Vertica, YDB and libsql drivers directly, so the concern was real. None
  of them reach our binary: `go list -deps` is clean for both
  `./internal/migrations` and `./cmd/web`, and the runtime image grew from
  5.58 MB to 5.80 MB — 222 KB, which is goose plus the embedded SQL. The image
  still contains no `.sql` files, no `.go` files and no goose CLI.
- **`goose.WithSlog` log volume.** Not noisy: goose emits nothing extra at info
  level, so it stays, and its warnings will surface structured rather than being
  lost.

One test flake was found and fixed rather than tolerated: the shutdown test slept
250 ms before cancelling, which under full-suite load raced with migrations. It
now waits for the "http server listening" line on the log stream, so it is
condition-based rather than a guess.

### Carry-over resolved

The migration decision deferred from M0 (TD-012) is settled by TD-014.

---

## M2 — Core Ledger MVP · `IN PROGRESS`

**Opened:** 2026-09-05

The first amounts in the system. M2 turns the domain from a catalogue of accounts
into a working ledger: money enters, money leaves, and every balance is derived
from those movements rather than stored.

Backend and presentation are built **in parallel by two agents**, which is why
this milestone has something no earlier one needed: a frozen interface between
them, [m2-contract.md](m2-contract.md). It is written before the implementation
and changed only deliberately and visibly.

### Scope

- **Account subtype.** `BANK` accounts are `CHECKING` or `SAVINGS`; every other
  type has no subtype. Enforced in Go *and* in SQLite — not folded into
  `account_type`, which continues to classify only the container (TD-019).
- **Categories.** `internal/category`, with `INCOME` and `EXPENSE` types, unique
  names per type, archive/restore, and no seed data. Not hierarchical.
- **The ledger.** `internal/ledger`, splitting the financial **event**
  (`transactions`) from its **impact on an account** (`account_movements`). One
  movement per transaction in M2; the schema allows several, because that is
  where transfers will live.
- **Money.** `internal/money` — exact conversion between decimal strings and
  `int64` minor units, at the currency's scale, with no float anywhere in
  parsing, arithmetic or formatting.
- **Opening balances as ledger facts.** A starting balance is an
  `OPENING_BALANCE` transaction, not a column. There is still no balance stored
  anywhere.
- **Derived balances.** Per account and per currency, always `SUM(movements)` at
  read time. No conversion between currencies.
- **`internal/application`.** One cross-domain workflow — open an account and
  record its opening balance in a single transaction — which cannot live in
  either domain without an import cycle.

### Not in M2

Transfers between the user's own accounts, FX and exchange rates, a reporting
currency, consolidated or historical net worth, credit, instalments, recurring
transactions, budgets, tags, hierarchical categories, and editing or deleting a
transaction.

### Status

**Backend, presentation and their integration are all in place.**

- Migrations `00004`–`00007` (schema version 7); the `money`, `category`,
  `ledger` and `application` packages; the account subtype.
- The presentation layer in `internal/web` and `web/`, built in parallel against
  the contract.
- `internal/web/adapter`, which translates between the two vocabularies and
  builds the presentation layer's services over the real database. `cmd/web` no
  longer runs on fakes; the fakes remain as the presentation layer's test double.

**Quality gates at this point:** `gofmt -l .` silent, `go vet ./...` clean,
`go test ./... -count=1` green across 13 packages, `go test -race ./...` green in
the Linux container, `git diff --check` clean, `docker compose build`
successful.

**Verified against the running application**, not only by unit tests: a fresh
database, then categories, an account with an opening balance, an income and an
expense recorded through the real HTTP forms. Balances came out of
`SUM(account_movements.amount_minor)` exactly — 1000.00 + 1500.50 − 25.99 =
2474.51 — and a cash account with no opening balance read 0.00. A bank account
without a subtype, an amount with three decimals, and an expense filed under an
income category were each rejected with 422.

**What remains before this milestone can be declared closed:** agreeing the
acceptance criteria and verifying them one by one, as M0 and M1 were, and a
final pass over the `Using Finance Tracker` walkthrough in the README now that
there is a UI to check it against.

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
| M2 | Core Ledger MVP | `IN PROGRESS` — see above |
| M3 | Dashboard | `PLANNED` |
| M4 | Multi-Currency & FX | `PLANNED` |
| M5 | Transfers | `PLANNED` |
| M6 | Historical Net Worth | `PLANNED` |
| M7 | Credit & Debt | `PLANNED` |
| M8 | Instalments & Calendar | `PLANNED` |
| M9 | Analytics | `PLANNED` |
| M10 | Hardening | `PLANNED` |

### Known carry-over

Parked items, none blocking:

- **Container `HEALTHCHECK`.** The distroless image has no shell and no `curl`,
  so the usual form does not apply. The clean approach is a self-check flag on
  the binary, worth adding when something is orchestrating restarts.
- **Request-logging middleware.** Deferred until there are endpoints worth
  tracing. The domain services built in M1 have no HTTP surface yet, so there is
  still nothing to trace.
- **Migration `status` / `down` interface.** No CLI or flags were added: the
  startup logs report what happened, and `goose_db_version` is one query away in
  the sqlite3 CLI. Revisit if a rollback is ever needed in anger.
- **Purpose dimension for accounts.** `account_type` classifies the container
  only. If distinguishing savings from day-to-day money ever has a real
  consumer, it arrives as its own decision rather than as more enum values.
