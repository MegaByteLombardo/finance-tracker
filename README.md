# Finance Tracker

A local, single-user web application for managing personal finances. Go +
SQLite, server-rendered, run on your own machine with Docker Compose. No cloud,
no accounts, no telemetry.

**This repository is at milestone M4.** It records income, expenses and transfers
between your own accounts, across accounts in several currencies, derives every
balance from the ledger, lets you correct what you entered, and takes backups you
can restore from. See [Scope](#scope) below.

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
| `BACKUP_DIR` | `./backups`         | Where snapshots are written. `/backups` under Compose.       |
| `BACKUP_RETENTION` | `10`          | Shutdown snapshots to keep. `0` keeps every one.             |
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
11. Move money between two of your own accounts — see below
12. Review balances on dashboard

### Recording a transfer between your own accounts

A **transfer** moves money you already have from one of your accounts to another.
Use it when you withdraw cash from your bank, top up a wallet, or move money
between two of your own accounts for any other reason.

1. Open the transfer form
2. Choose the account the money leaves (**From**)
3. Choose the account it arrives in (**To**) — it must be in the **same
   currency** and cannot be the same account
4. Enter the amount as a positive number, e.g. `250.00`
5. Enter the date it happened
6. Optionally describe it, e.g. "Pasar dinero a efectivo"

The From balance goes down, the To balance goes up, and **the total you hold in
that currency does not change** — because none did. Nothing was earned and
nothing was spent.

Both accounts must be active. A transfer between accounts in *different*
currencies is refused: converting between them needs an exchange rate, which
belongs to a later milestone. You can edit a transfer afterwards; you cannot
delete it.

A transfer may leave an account with a negative balance, and this is allowed on
purpose. Overdrafts happen, and a cash account that goes negative usually means
something has not been entered yet — hiding that would not make it untrue.

### Important financial model notes

* **Balances are derived from transactions:** Accounts have no stored balance column. Every balance is calculated dynamically from ledger movements (`SUM(movements)`).
* **Currencies are still not converted:** Accounts in different currencies (USD, EUR, VES) are displayed independently. Balances in different currencies are never summed together.
* **An Internal Transfer is not the "Bank transfer" payment method.** These are two different things and the application treats them as such:
  * a **Transfer** moves money between **two accounts you own**. It is neither income nor an expense, has no category and no payment method, and does not change your total wealth in that currency.
  * the payment method named **"Bank transfer"** describes the **channel** an operation with the **outside world** used — how your salary arrived, or how you paid a bill.

  Paying rent by bank transfer is an *expense* whose payment method is "Bank transfer". Moving $250 from your bank to your cash is a *transfer* with no payment method at all.
* **A transfer is one event, shown once.** Internally it is one transaction with two movements — one negative, one positive — which is why the totals always balance.


## Where the database lives

**Locally:** `./data/finance.db` (configurable via `DB_PATH`).

**Under Docker:** `/data/finance.db`, on a named volume called `finance_data`.
The database is never in the container's writable layer, so:

| Command                 | Effect on your data                                  |
| ----------------------- | ---------------------------------------------------- |
| `docker compose down`   | Containers removed, **volume kept**. Data survives.   |
| `docker compose up`     | The same volume is reattached. Data is still there.   |
| `docker compose up -d --build` | Rebuilds the image, **keeps the volume**. This is how to pick up code changes. |
| `docker compose down -v`| **Destroys the volume and the database.** Recoverable only from a backup. |

`docker volume prune` and `docker system prune --volumes` will also remove the
volume once the container is gone. Both are easy to run without meaning to.

SQLite runs in WAL mode, so alongside `finance.db` you may see `finance.db-wal`
and `finance.db-shm`. They are SQLite's own working files, not something to
manage by hand.

## Backups

Snapshots go to **`./backups/` on your machine**, not into the Docker volume.
That is the whole point: `docker compose down -v` destroys volumes, so a backup
kept in one would die with the database it exists to protect.

They are taken automatically:

- **before migrations**, whenever a schema that already holds data is about to
  change;
- **on shutdown**, after requests drain and before the database closes — including
  the shutdown that `docker compose down` performs before destroying anything;
- **on request**, from the application.

Only shutdown snapshots are ever deleted, keeping the most recent
`BACKUP_RETENTION` (default 10). Manual and pre-migration snapshots are kept
indefinitely.

Each one is a complete SQLite database taken with `VACUUM INTO` and verified with
`PRAGMA integrity_check` before it counts. You can open one directly:

```sh
sqlite3 backups/finance-20260905T183045Z-manual.db "SELECT count(*) FROM transactions"
```

### Restoring

Restore is deliberately an **offline** operation — replacing the database while
the server is running would pull the file out from under it:

```sh
docker compose stop
docker compose run --rm app restore --file finance-20260905T183045Z-manual.db
docker compose up -d
```

It validates the snapshot first, takes a snapshot of what it is replacing so the
restore itself can be undone, and leaves everything untouched if anything fails.
A snapshot from an older schema is fine: the next normal start migrates it
forward.

`docs/operations.md` has the full procedure, including how to rehearse a recovery
on a throwaway project.

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
cmd/web/                composition root; `serve` (default) and `restore`
internal/config/        environment loading and validation
internal/database/      SQLite: DSN, PRAGMAs, pool, time and date conventions
internal/migrations/    schema, as embedded SQL migrations
internal/backup/        snapshots and restore
internal/money/         exact decimal string ↔ integer minor units
internal/currency/      currencies
internal/account/       accounts
internal/category/      income and expense categories
internal/paymentmethod/ payment channels
internal/ledger/        transactions and account movements; balances
internal/application/   cross-domain workflows (open account + opening balance)
internal/httpserver/    HTTP server, timeouts, /healthz
internal/web/           handlers, templates, view models
internal/web/adapter/   translation between the web layer and the domain
web/                    templates and static assets
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

**M2 — the ledger.** Categories, transactions and account movements, with every
balance derived from `SUM(account_movements.amount_minor)` rather than stored.
Amounts are exact integer minor units end to end — no float anywhere in parsing,
storage, summation or formatting. Opening balances are ledger facts, not columns.
Plus the server-rendered interface: dashboard, accounts, categories and
transactions.

**M3 — data safety, editing and payment methods.** Backups taken automatically
before migrations and on shutdown, written to the host rather than the Docker
volume, with an offline restore. Payment methods became rows you administer
instead of four fixed values. Transactions and opening balances can be corrected,
revalidated against exactly the same rules as a creation.

**M4 — internal transfers.** Moving money between two of your own accounts, as a
single event with two movements: one out of the source, one into the destination,
equal and opposite. The total held in that currency is identical before and
after, because a transfer neither creates nor destroys money. Both accounts must
be active and share a currency. Transfers can be corrected, never deleted.

All of it is covered by tests running against real SQLite, and the disaster
recovery path has been rehearsed against real Docker.

### Not built yet

Everything below is deliberately absent, and none of it is described anywhere in
this README as though it existed:

**No deleting.** Accounts, categories, payment methods and transactions are
archived or corrected, never removed. Cancelling a transaction is an accounting
decision that has not been made yet.

**No history of edits.** M3 supports editing current records; it does not keep
previous versions of them. The undo is a backup.

**No currency conversion.** Accounts in USD, EUR and VES are shown side by side
and never summed together. Exchange rates, a reporting currency and consolidated
net worth belong to a later milestone.

**No transfers between accounts in different currencies.** A transfer requires
both accounts to share a currency. Converting between them needs an exchange
rate, a source amount and a destination amount, all of which belong to the FX
milestone. Nothing has been prepared for it in advance.

Also absent: authentication and users, historical net worth, credit cards, loans,
instalments, a financial calendar, recurring transactions, budgets, tags,
hierarchical categories, charts and analytics, scheduled backups, Excel import,
export, cloud deployment and CI/CD.
