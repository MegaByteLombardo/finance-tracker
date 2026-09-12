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

## M2 — Core Ledger MVP · `CLOSED`

**Closed:** 2026-09-05

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

### Delivered

- Migrations `00004`–`00007` (schema version 7); the `money`, `category`,
  `ledger` and `application` packages; the account subtype.
- The presentation layer in `internal/web` and `web/`, built in parallel against
  a frozen contract, plus `internal/web/adapter` translating between the two
  vocabularies.

### Verification

Quality gates at close: `gofmt -l .` silent, `go vet ./...` clean,
`go test ./... -count=1` green across 13 packages over three consecutive runs,
`go test -race ./...` green in the Linux container, `git diff --check` clean,
`docker compose build` successful.

**Verified against the running application, not only by unit tests.** A fresh
database, then categories, an account with an opening balance, an income and an
expense recorded through the real HTTP forms. Balances came out of
`SUM(account_movements.amount_minor)` exactly — 1000.00 + 1500.50 − 25.99 =
2474.51 — and a cash account with no opening balance read 0.00. A bank account
without a subtype, an amount with three decimals, and an expense filed under an
income category were each rejected with 422.

M2 was closed without a numbered acceptance list, unlike M0 and M1. The scope was
specified as capability rather than as criteria, and the gap is recorded here
rather than papered over; M3 returns to the M0/M1 standard.

### Carry-over into M3

Three findings from the parallel build became M3 requirements rather than notes:
the web layer stripped thousands separators blindly before calling the ledger,
`app.js` hard-coded two decimal places, and a `strconv.ParseInt` error was
discarded. All three are addressed in M3's contract.

---

## M3 — Data Safety, Editing & Payment Methods · `CLOSED`

**Closed:** 2026-09-06

This milestone was not planned. It exists because the M2 MVP was used, and using
it exposed two things: **there was only ever one copy of the data**, and there was
no way to correct a mistake once it was recorded.

The first cost real data. The `finance_data` volume was destroyed and recreated,
and nothing else held a copy. The investigation was unambiguous — persistence was
working correctly; there was simply no backup, because `docs/operations.md` had
declared backups out of scope in M0, when there was nothing to lose. That
reasoning stopped being true the moment M2 shipped a working ledger.

### Delivered

**Data safety.** `internal/backup`: snapshots taken with `VACUUM INTO` against
the live database, validated with `PRAGMA integrity_check` before they are given
a real name, written to a bind mount on the **host** rather than to the Docker
volume they exist to protect. Three triggers — before migrations, on graceful
shutdown, and on request — plus a pre-restore snapshot that makes a restore
itself reversible. Retention applies only to shutdown snapshots. Restore is an
offline subcommand of the same binary.

**Payment methods.** From a four-value `CHECK` constraint to a table the user
administers, with the M2 values migrated under the names the interface already
showed. The migration rebuilds `transactions` **and** `account_movements`,
dropping the old child before the old parent so that foreign keys stay on
throughout — after two other approaches were tested and rejected.

**Editing.** Transactions and opening balances can be corrected, revalidated
against exactly the same invariants as a creation, with the transaction and its
movement rewritten in one `sql.Tx`. `INCOME` and `EXPENSE` convert freely and the
movement's sign is recomputed from the new type. Account and category editing
already existed from M2 and is now exposed by the contract.

**Contract.** [m3-contract.md](m3-contract.md), frozen before implementation.

### Verified empirically

Beyond the test suite, three things were proven against real infrastructure
rather than argued from documentation:

- **A bind mount is writable by the distroless non-root user (uid 65532); a fresh
  named volume is not.** Both were tested with real containers. This is what
  decided where backups live.
- **The migration ran against the real database on this machine.** 5 accounts,
  4 categories, 10 transactions and 10 movements crossed from schema 7 to 9 with
  every balance identical, `foreign_key_check` empty and `integrity_check` `ok` —
  and the pre-migration snapshot was taken automatically first.
- **The disaster recovery cycle was rehearsed end to end**, in an isolated Compose
  project so the real data was never at risk: create data → snapshot →
  `docker compose down -v` → confirm the volume is gone and the snapshot survives
  on the host → restore → start → **identical accounts, categories, transactions,
  movements and balances**.

### Not in M3

Internal transfers, FX, exchange rates, a reporting currency, consolidated net
worth, credit, loans, instalments, recurring transactions, budgets, tags,
hierarchical categories, **any delete**, and any history of edited records.

**M3 supports editing current records. M3 does not preserve previous versions of
edited records.** The undo is a backup.

### Verification

All **76 acceptance criteria** were verified, covering quality gates, schema
migration with data preservation, backup creation and validation, retention,
restore including path safety and journal-file handling, the disaster recovery
cycle, payment methods, editing semantics and atomicity, derived balances, the
presentation requirements, and documentation.

Quality gates at close: `gofmt -l .` silent, `go vet ./...` clean,
`go test ./... -count=1` green across 15 packages over three consecutive runs,
`go test -race ./...` green in the Linux container, `git diff --check` clean,
`docker compose build` successful.

The three carry-over findings from M2 are resolved: the backend no longer strips
thousands separators — a grouped amount is now rejected rather than silently
reinterpreted, so "1,5" can no longer become "15" — the browser derives decimal
precision from the currency's minor unit instead of assuming two, and no
`strconv.ParseInt` error is discarded.

### Remaining for the presentation layer

M3's backend is complete and its contract is frozen. What is left is genuinely
new interface work, and it belongs to whoever builds the screens: a page for
managing payment methods, and forms for editing accounts, categories and
transactions. Every service and type they consume exists and is documented in
[m3-contract.md](m3-contract.md).

### A note on process

Two smoke tests during this milestone were run against the real database rather
than an isolated Compose project, and wrote test rows into it. They were removed
surgically afterwards, with a snapshot taken first, and the correct balances
restored. The disaster-recovery rehearsal was correctly isolated from the start;
the earlier checks should have been too.

---

## M4 — Internal Transfers · `IN PROGRESS`

**Backend, contract, and desktop presentation integration complete.**

Moving money between two of the user's own accounts. It is the first milestone to
use the capability the ledger was designed around in M2: **one transaction with
more than one movement**.

The design prediction M2 wrote down held. `account_movements.transaction_id` was
never `UNIQUE`, and every balance was already `SUM(amount_minor)` over whatever
rows exist, so a transfer needed no new table, no new column and no change to the
balance derivation. The only schema change was widening two `CHECK` constraints.

### Scope

- **`TRANSFER` as a fourth `transaction_type`.** One `transactions` row, **two**
  `account_movements` — negative in the source, positive in the destination,
  equal in magnitude. They sum to zero, so **the total held in a currency is
  identical before and after a transfer** (TD-041).
- **No redundant columns.** `transactions` gained nothing: no
  `source_account_id`, no `destination_account_id`, no `amount`. The two
  movements already state all three.
- **No category, no payment method.** A `TRANSFER` has both `NULL`, enforced by a
  table `CHECK`. A transfer is neither income nor an expense, and uses no channel
  with the outside world. This is **not** the payment method named "Bank
  transfer", which is how an *external* operation happened.
- **Same currency only.** Both accounts must share one. Cross-currency transfers
  need a source amount, a destination amount and a rate; all three belong to FX
  and none of them was added or prepared for.
- **`RecordTransfer` and `UpdateTransfer`**, in `internal/ledger` rather than a
  package of their own. Each writes in a single `sql.Tx`: either the transaction
  and both movements exist, or none of them do.
- **An edit keeps both movement ids** (TD-042), and revalidates exactly what a
  creation validates.
- **Each type has its own edit use case**, and each guard names what it accepts
  rather than what it rejects (TD-045). This closed a real hole: M3's
  `UpdateTransaction` excluded only `OPENING_BALANCE`, so a transfer would have
  passed through it and had one of its two movements rewritten.
- **The read path presents a transfer as one row** carrying a self-sufficient
  `TransferDetail` — both accounts, both movement ids, the positive amount — and
  **refuses to present a malformed one** (TD-044).
- **Migration `00010`**, rebuilding `transactions` and `account_movements`
  child-first with foreign keys on throughout, following TD-037.

### Decisions worth naming

- **A transfer may leave an account negative, and this is deliberate** (TD-043).
  Overdrafts are real, a negative cash balance usually signals a missing
  movement, and — decisively — `effective_date` can be backdated, so a
  balance check at write time could not enforce the rule anyway.
- **Archived accounts** may not be used for a new or edited transfer, while
  existing transfers involving them stay readable. The consequence is stated in
  the contract: a transfer whose account was archived later cannot be edited at
  all until that account is unarchived. This is M3's behaviour for income and
  expenses, unchanged.
- **Transfer integrity is not enforced by triggers.** Every rule spans more than
  one row, so `CHECK` cannot see it. It is held by one function inside one
  transaction, and re-checked on read (TD-044).

### Not in M4

Cross-currency transfers, FX, exchange rates, BCV, manual rates, a base or
reporting currency, consolidated net worth, transfer fees, split transactions,
scheduled or recurring transfers, **any delete**, reversals, and audit history of
edits.

`DeleteTransfer`, `CancelTransfer` and `ReverseTransfer` do not exist. Editing
yes; deleting no.

### Presentation layer integration
 
Delivered: the transfer creation form (`GET /transfers/new`, `POST /transfers`), the edit form (`GET /transfers/{id}/edit`, `POST /transfers/{id}`), routing guards in `transactions.go` redirecting transfer edits to `/transfers/{id}/edit`, and rendering transfers with source/destination detail and positive amounts across transaction lists and the dashboard. Mobile UI screens remain in prototype/preview status.

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
| M4 | Internal Transfers | `IN PROGRESS` |
| M5 | Dashboard | `PLANNED` |
| M6 | Multi-Currency & FX | `PLANNED` |
| M7 | Historical Net Worth | `PLANNED` |
| M8 | Credit & Debt | `PLANNED` |
| M9 | Instalments & Calendar | `PLANNED` |
| M10 | Analytics | `PLANNED` |
| M11 | Hardening | `PLANNED` |

The roadmap has now been renumbered twice, which is what "provisional" above
means in practice. Everything after M3 shifted by one when M3 became Data Safety
rather than Dashboard; transfers then moved from M6 to M4, ahead of both Dashboard
and FX, because the ledger already supported them and a dashboard showing money
that appears to vanish between two accounts would have been the wrong thing to
build first.

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
