# Finance Tracker

A local, single-user web application for managing personal finances. Go +
SQLite, server-rendered, run on your own machine with Docker Compose. No cloud,
no accounts, no telemetry.

**This repository is at milestone M0.** M0 is the technical foundation only — it
starts, opens SQLite correctly, answers a health check, and shuts down cleanly.
It contains no financial features yet. See [Scope](#scope) below.

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
cmd/web/            composition root: config → database → server → shutdown
internal/config/    environment loading and validation
internal/database/  SQLite: DSN, PRAGMAs, connection pool
internal/httpserver/ HTTP server, timeouts, routes
```

The service and repository layers arrive with the first domain milestone. No
package exists before it has code in it.

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

### In M0

- Environment-based configuration with validation
- SQLite opened with WAL, `foreign_keys`, `busy_timeout` and `synchronous`
  verified at startup
- `GET /healthz` — reports 200 when the process is alive and SQLite answers,
  503 when it does not
- Graceful shutdown on `SIGINT` / `SIGTERM`
- Multi-stage Docker build, non-root distroless runtime, persistent volume
- Tests against real SQLite

### Not in M0

Schema migrations arrive in **M1**, with the first real domain migration.

Everything else is a later milestone and is deliberately absent: UI, htmx,
templates, CSS, authentication, users, accounts, cash, currencies, income,
expenses, categories, transfers, net worth, exchange rates, credits, cards,
loans, instalments, financial calendar, charts, dashboards, automatic backups,
Excel import, export, cloud deployment and CI/CD.

Nothing in this README describes a feature that does not exist yet.
