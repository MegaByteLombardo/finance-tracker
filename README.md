# Finance Tracker

A local, single-user web application for managing personal finances. Go +
SQLite, server-rendered, run on your own machine with Docker Compose. No cloud,
no accounts, no telemetry.

**This repository is at milestone M1.** It starts, migrates its schema, and can
model currencies and accounts. There is no user interface yet — the domain is
exercised through its Go API and its tests. See [Scope](#scope) below.

## Requirements

- **Go 1.27+** — to run or test locally
- **Docker + Docker Compose v2** — to run in a container

Nothing else. There is no C toolchain requirement: the SQLite driver
(`modernc.org/sqlite`) is pure Go.

## Running locally with Go

```sh
go run ./cmd/web
curl http://localhost:8080/healthz     # {"status":"ok"}
```

Every setting has a working default, so no environment configuration is needed.
The database is created at `./data/finance.db` on first start, along with its
directory. `data/` is git-ignored.

Press `Ctrl+C` to stop; the server drains in-flight requests before closing the
database.

## Running with Docker Compose

```sh
docker compose up -d --build
curl http://localhost:8080/healthz     # {"status":"ok"}
docker compose logs -f app
docker compose down
```

The container runs as a non-root user (uid 65532) on a distroless image with no
shell and no package manager.

## Configuration

Configuration comes from environment variables. Copy `.env.example` to `.env` to
change anything — Docker Compose reads `.env` on its own, so no dotenv library is
involved. `.env` is git-ignored.

| Variable    | Default              | Meaning                                                     |
| ----------- | -------------------- | ----------------------------------------------------------- |
| `APP_ENV`   | `development`        | `development` or `production`. Selects text vs. JSON logging. |
| `HTTP_ADDR` | `:8080`              | Listen address as `host:port`. Empty host means all interfaces. |
| `DB_PATH`   | `./data/finance.db`  | SQLite file. Its parent directory is created on startup.     |
| `LOG_LEVEL` | `info`               | `debug`, `info`, `warn` or `error`.                          |
| `HTTP_PORT` | `8080`               | Host port published by Compose. Read by `compose.yaml` only. |

An invalid value is reported by name at startup and the process exits non-zero.
All problems are reported at once, not one per restart.

## Using Finance Tracker

A complete walkthrough to get started with a fresh instance:

1. Start application
2. Open http://localhost:8080
3. Create categories (e.g. Salary as INCOME, Groceries as EXPENSE)
4. Create account
5. Select BANK/CASH/WALLET/OTHER
6. For BANK select CHECKING/SAVINGS
7. Select currency (USD, EUR, VES)
8. Enter optional initial balance
9. Record income
10. Record expense
11. Review balances on dashboard

### Important financial model notes

* **Balances are derived from transactions:** Accounts have no stored balance column. Every balance is calculated dynamically from ledger movements (`SUM(movements)`).
* **Currencies are not converted in M2:** Accounts in different currencies (USD, EUR, VES) are displayed independently. Balances in different currencies are never summed together.
* **A bank transfer payment method is not an internal-account transfer:** In M2, the "Bank transfer" option describes the payment channel for external income or expenses; transfers between your own accounts belong to a later milestone.


## Where the database lives

**Locally:** `./data/finance.db` (configurable via `DB_PATH`).

**Under Docker:** `/data/finance.db`, on a named volume called `finance_data`.
The database is never in the container's writable layer, so:

| Command                 | Effect on your data                                  |
| ----------------------- | ---------------------------------------------------- |
| `docker compose down`   | Containers removed, **volume kept**. Data survives.   |
| `docker compose up`     | The same volume is reattached. Data is still there.   |
| `docker compose down -v`| **Destroys the volume and the database. Irreversible.** |

SQLite runs in WAL mode, so alongside `finance.db` you may see `finance.db-wal`
and `finance.db-shm`. They are SQLite's own working files, not something to
manage by hand.

### Backups

Not implemented — backups are out of scope for M0. When they are added:

- With the application **stopped cleanly**, copying the database file is enough.
- With the application **running**, copying the file by hand is not safe. The
  correct mechanism is SQLite's Online Backup API or
  `VACUUM INTO '/path/to/backup.db'`, both of which produce a consistent copy
  without stopping the service.

## Tests

```sh
gofmt -l .            # must print nothing
go vet ./...
go test ./... -count=1
```

Database tests run against real SQLite in a temporary directory rather than a
mock — they verify the PRAGMAs that are actually in effect, that foreign keys
are genuinely enforced, and that those settings hold on every connection in the
pool.

The race detector needs a C toolchain, which Windows does not have by default.
Run it in the build container instead:

```sh
docker run --rm -v "${PWD}:/src" -w /src golang:1.27-trixie go test -race ./...
```

## Documentation

Project documentation lives in [`docs/`](docs/README.md) — functional context,
architecture, the roadmap, persistence conventions, operations, and the record of
technical decisions with their rationale.

## Architecture

A modular monolith in a single binary:

```
HTTP  →  Application / Services  →  Repository  →  SQLite
```

```
cmd/web/             composition root: config → database → migrations → server
internal/config/     environment loading and validation
internal/database/   SQLite: DSN, PRAGMAs, connection pool, time convention
internal/migrations/ schema, as embedded SQL migrations
internal/currency/   currencies: model, repository, service
internal/account/    accounts: model, repository, service
internal/httpserver/ HTTP server, timeouts, routes
```

Domain packages hold their own model, repository and service — organised by
feature, not by layer. No package exists before it has code in it.

The domain services have no HTTP surface yet; that arrives with the htmx
milestone.

Some decisions worth knowing about, all explained in the code:

- **PRAGMAs live in the DSN.** `database/sql` is a pool, so setting them once
  after `sql.Open` would configure a single arbitrary connection and silently
  leave the others at SQLite's defaults.
- **`synchronous = FULL`.** In WAL mode, `NORMAL` is consistent but not durable —
  SQLite documents that a committed transaction can roll back after a power
  loss. That is not an acceptable trade for financial records.
- **The pool holds one connection.** SQLite is in-process; a single connection
  makes `SQLITE_BUSY` structurally impossible here, and connection recycling
  advice written for network databases does not apply.
- **Money will never be a float.** Amounts will be integer minor units. See
  `CLAUDE.md`.

## Scope

### Built so far

**M0 — foundation.** Environment-based configuration with validation; SQLite
opened with WAL, `foreign_keys`, `busy_timeout` and `synchronous` verified at
startup; `GET /healthz`; graceful shutdown on `SIGINT` / `SIGTERM`; multi-stage
Docker build with a non-root distroless runtime and a persistent volume.

**M1 — currencies and accounts.** Schema migrations embedded in the binary and
applied at startup; a `currencies` table seeded with USD, EUR and VES; an
`accounts` table where every account is denominated in exactly one immutable
currency; and Go packages for both, each with validation, a repository and a
service. Accounts and currencies are archived, never deleted.

All of it is covered by tests running against real SQLite.

### Not built yet

There is **no user interface**. Currencies and accounts exist in the database and
in the Go API, but nothing renders them; the domain is exercised through its
tests. HTML, htmx, templates and CSS are a later milestone.

There are also **no balances and no amounts anywhere**. `accounts` deliberately
has no balance column: balances will be derived from the movement ledger that
arrives with transactions, so a stored balance would be a denormalised value that
can silently disagree with its own history.

Everything else remains a later milestone and is deliberately absent:
authentication, users, income, expenses, categories, transfers, net worth,
exchange rates and FX conversion, credits, cards, loans, instalments, financial
calendar, charts, dashboards, automatic backups, Excel import, export, cloud
deployment and CI/CD.

Nothing in this README describes a feature that does not exist yet.
