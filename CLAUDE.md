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
  pool. Nothing else builds a DSN or sets a PRAGMA.
- `internal/httpserver` owns HTTP. It never builds SQL.
- Domain packages (`internal/account`, `internal/transaction`, …) arrive with
  the milestone that needs them. Do not create a package before it has code.

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
- No ORM. Hand-written SQL against `database/sql`. Revisit only as an explicit,
  recorded decision.

## Dependencies

- **Research a dependency before adding it**: current version, maintenance
  status, what it pulls in. Prefer the standard library.
- Never bump `modernc.org/libc` on its own — it is tightly coupled to
  `modernc.org/sqlite`. Let `go get modernc.org/sqlite@vX` resolve the pair.
- No Viper, no ORM, no DI framework, no Redis, no PostgreSQL, no Kubernetes.

## Testing

- Infrastructure tests use **real SQLite** in `t.TempDir()`. Do not mock the
  driver — a mock only proves the mock behaves as written.
- Do not assert on the existence of `-wal` / `-shm` files. Their lifecycle is
  SQLite's business and testing for them buys flakes.
- Quality gates: `gofmt -l .` (silent), `go vet ./...`, `go test ./... -count=1`.
  `-race` needs a C toolchain, so run it in the Linux container on Windows.

## Current scope

**M0 (done): foundation only.** Config, SQLite, `/healthz`, graceful shutdown,
Docker, tests, docs. **No financial domain whatsoever** — no accounts,
currencies, categories, transactions, rates, credits or instalments.

**Schema migrations arrive in M1**, together with the first real domain
migration. M0 deliberately ships no `migrations/` directory and no migration
dependency: `go:embed` cannot embed an empty directory, and a bootstrap
migration invented only to satisfy the tooling would be noise.

Do not implement a future milestone's domain because it seems convenient.
