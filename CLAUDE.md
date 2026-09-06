# CLAUDE.md

Operational guide for working on Finance Tracker. Short on purpose — these are
the constraints that outlive any single milestone.

## Documentation is part of the contract

The documents under [`docs/`](docs/README.md) are part of the project's technical
contract. When a change alters architecture, schema, a technical decision,
operational behaviour, or a milestone's status, the corresponding document is
updated **in the same change** — not afterwards.

Read `docs/` for the full reasoning behind anything below.

## What this is

A local, single-user personal finance web application. Go + SQLite, rendered
server-side (htmx later). It runs on one machine, in Docker Compose. There is no
cloud, no multi-tenancy, and no user accounts.

## Architecture

A **modular monolith**, one binary:

```
HTTP  →  Application / Services  →  Repository  →  SQLite
```

- `cmd/web` is the composition root. It is the only place that wires
  dependencies together. No DI framework.
- `internal/database` owns every decision about opening SQLite — DSN, PRAGMAs,
  pool — plus the `DBTX` executor interface and the timestamp convention.
  Nothing else builds a DSN or sets a PRAGMA.
- `internal/migrations` owns the schema. It is the **only** package that imports
  goose.
- `internal/httpserver` owns HTTP. It never builds SQL.
- Domain packages (`internal/currency`, `internal/account`, …) hold their own
  model, repository and service — organised by feature, not by layer. Do not
  create a package before it has code.

Each domain package declares its own sentinel errors and **translates** errors
from packages it consults, so a handler never has to import a neighbouring domain
to interpret what it was returned.

Separation of concerns, yes. Ceremonial Clean Architecture, no. Do not add an
abstraction for a second implementation that does not exist.

## Go

- Idiomatic Go, standard library first.
- **Errors are always handled.** Never `_` an error away. If an error genuinely
  cannot be acted on, log it — do not drop it.
- Wrap with context: `fmt.Errorf("opening database %q: %w", path, err)`.
- No `panic` for ordinary failures. `main` is `if err := run(...); err != nil`.
- `log/slog` for logging. Not Zap, Zerolog or Logrus.
- `net/http` for serving. Not Gin, Fiber, Echo or Chi.

## Money — non-negotiable

**Never use `float32`, `float64`, or SQLite `REAL` for a monetary amount, an
exchange rate, or anything derived from them.** Binary floating point cannot
represent decimal money exactly, and the error compounds silently across
aggregation.

Amounts are stored as **integer minor units** (`INTEGER` / `int64`) — cents,
céntimos — alongside their currency. Exchange rates use an exact representation
(integer numerator/denominator, or scaled integers), never a float.

A `Money` type does not exist yet. When it is introduced, it enforces this.

## SQLite

- WAL journal mode, `foreign_keys = ON`, `busy_timeout = 5000`,
  `synchronous = FULL`. Durability beats write throughput for financial records.
- **Connection-scoped PRAGMAs go in the DSN, never in a one-off `db.Exec` after
  `sql.Open`.** `database/sql` is a pool: a single `Exec` configures one
  arbitrary connection and silently leaves the rest at SQLite's defaults.
- `journal_mode` is a persistent property of the file, so it is applied once in
  `database.Open` and its returned value is *checked* — the PRAGMA reports the
  resulting mode instead of failing.
- The pool is pinned to a single connection. That is deliberate; read the
  comments in `internal/database/sqlite.go` before changing it.
- **Every business table is `CREATE TABLE ... STRICT;`** unless there is a
  written reason not to.
- **Timestamps are `TEXT`**, RFC 3339 UTC with exactly three fractional digits.
  Use `database.FormatTime` / `database.Now`, never `time.RFC3339Nano` — it
  strips trailing zeros and breaks the ordering the fixed width guarantees.
- **Never classify a SQLite error by its code or its message.** `Error.Code()`
  returns the primary result code, so a duplicate key, a foreign key violation
  and a failed CHECK are all `SQLITE_CONSTRAINT`. Determine the condition with a
  query instead, in the same transaction.
- Migrations are SQL, embedded, sequential, applied at startup before anything
  listens. `Down` never runs automatically. Never call `provider.Close()` — it
  closes the `*sql.DB` the caller owns.
- No ORM. Hand-written SQL against `database/sql`. Revisit only as an explicit,
  recorded decision.
- Repositories are concrete structs over `database.DBTX`. **Do not add
  repository interfaces** to enable mocks; tests use real SQLite.

## Dependencies

- **Research a dependency before adding it**: current version, maintenance
  status, what it pulls in. Prefer the standard library.
- Never bump `modernc.org/libc` on its own — it is tightly coupled to
  `modernc.org/sqlite`. Let `go get modernc.org/sqlite@vX` resolve the pair.
- goose's `go.mod` requires several database drivers directly. They do not reach
  our binary; if a dependency change is made, re-check with
  `go list -deps ./cmd/web`.
- No Viper, no ORM, no DI framework, no Redis, no PostgreSQL, no Kubernetes.

## Testing

- Infrastructure tests use **real SQLite** in `t.TempDir()`. Do not mock the
  driver — a mock only proves the mock behaves as written.
- Do not assert on the existence of `-wal` / `-shm` files. Their lifecycle is
  SQLite's business and testing for them buys flakes.
- Quality gates: `gofmt -l .` (silent), `go vet ./...`, `go test ./... -count=1`.
  `-race` needs a C toolchain, so run it in the Linux container on Windows.

## Current scope

**M0–M2 (closed).** Foundation; currencies and accounts; the ledger, categories,
money, opening balances and the server-rendered interface.

**M3 (in progress): data safety, editing and payment methods.**

- **Backups.** `internal/backup` snapshots the live database with `VACUUM INTO`,
  validates each one with `PRAGMA integrity_check` before it counts, and writes
  to a bind mount on the **host** — never to the Docker volume, because
  `docker compose down -v` destroys volumes and this project lost data that way
  once. Restore is an offline subcommand of the same binary.
- **Payment methods** are rows the user administers, not a fixed enumeration.
- **Editing** exists for accounts, categories, transactions and opening balances.
  An edit runs the **same validation as a creation** — they share one code path,
  because an edit that could produce a state a creation would refuse is a second
  door into every invariant.

**Not built, and not to be added opportunistically:**

- **No delete of anything.** Archive or correct.
- **No history of edited records.** M3 overwrites. The undo is a backup.
- **No FX, exchange rates, reporting currency or consolidated net worth.**
  Currencies are shown separately and never summed.
- **No transfers between the user's own accounts.** A payment method named "Bank
  transfer" is a channel to the outside world, not an internal transfer.
- No credit, loans, instalments, calendar, recurring transactions, budgets, tags,
  hierarchical categories, authentication or public API.

Do not implement a future milestone's domain because it seems convenient.

## Things that were got wrong once

Short list, because each cost real time and each is easy to repeat.

- **A `CHECK` that can evaluate to NULL does not constrain anything.** SQLite
  rejects a row only when the expression is *false*; `NULL` passes. Guard every
  nullable column with `IS NULL` / `IS NOT NULL` (TD-025).
- **Rebuilding a table that has children:** drop the old **child** first, then the
  old parent. `PRAGMA defer_foreign_keys` does not help — it fails at COMMIT
  (TD-037).
- **A fresh named volume is root-owned** and a non-root process cannot write to
  it unless the image ships that directory pre-owned. A bind mount on Windows is
  world-writable; on Linux it keeps the host's ownership (TD-032).
- **Pruning frees filenames.** Anything that picks a name by checking the
  directory must also remember what it already issued (TD-033).
