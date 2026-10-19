# Finance Tracker

A personal finance app that runs on your own computer. You record your income,
expenses and the money you move between accounts, and it shows your balances,
your net worth and how each week or month went.

- **Local and private.** Single user, no cloud, no accounts, no telemetry.
- **Multi-currency.** Accounts in USD, EUR and VES, valued together in one base
  currency.
- **Exact.** Money is stored as whole cents, never as floating point.
- **Simple to run.** One Go binary and one SQLite file, with Docker Compose.

## Contents

- [Features](#features)
- [Quick start](#quick-start)
- [Configuration](#configuration)
- [Using the app](#using-the-app)
- [How the numbers work](#how-the-numbers-work)
- [Your data](#your-data)
- [Backups and restore](#backups-and-restore)
- [Development](#development)
- [What it does not do](#what-it-does-not-do)
- [Documentation](#documentation)

## Features

| Area | What you get |
| --- | --- |
| Accounts | Bank, cash, wallet and other accounts, each in one currency. Archive them when you stop using them. |
| Transactions | Income and expenses with a category and a payment method. Every one can be edited later. |
| Transfers | Move money between two of your own accounts in the same currency. |
| Currency exchanges | Record buying or selling currency between your own accounts. The rate is worked out from the two amounts. |
| Exchange rates | Fetched daily from the Banco Central de Venezuela (BCV), or typed in by hand. |
| Dashboard | Net worth today, its history chart, cash flow and spending by category over 7, 30, 90 or 365 days. |
| Recap | A weekly or monthly summary against the previous period: what went up, what went down, and whether things improved. |
| Savings | Set what share of each income you want to save; every eligible income suggests an amount in USD, at the exchange rate of its own date. |
| Mobile | A phone-sized version of the app at `/mobile`. |
| Backups | Automatic snapshots of the database, plus a one-command restore. |

## Quick start

You need **Docker with Docker Compose v2**, or **Go 1.27+** to run it without
Docker. Nothing else is needed: the SQLite driver is pure Go.

### With Docker Compose (recommended)

```sh
docker compose up -d --build
```

Open <http://localhost:8080>.

| Task | Command |
| --- | --- |
| See the logs | `docker compose logs -f app` |
| Stop | `docker compose down` |
| Update after pulling new code | `docker compose up -d --build` |

### With Go

```sh
go run ./cmd/web
```

Open <http://localhost:8080>. The database is created at `./data/finance.db`
the first time. Press `Ctrl+C` to stop.

To check that the server is up: `curl http://localhost:8080/healthz` returns
`{"status":"ok"}`.

## Configuration

Everything has a working default, so no configuration is needed to start. To
change something, copy `.env.example` to `.env` and edit it. Docker Compose
reads `.env` on its own.

| Variable | Default | What it does |
| --- | --- | --- |
| `HTTP_PORT` | `8080` | The port on your machine (Docker Compose only). |
| `HTTP_ADDR` | `:8080` | The address the server listens on. |
| `DB_PATH` | `./data/finance.db` | Where the database file lives. |
| `BACKUP_DIR` | `./backups` | Where backups are written. |
| `BACKUP_RETENTION` | `10` | How many shutdown backups to keep. `0` keeps all. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `APP_ENV` | `development` | `production` switches the logs to JSON. |
| `FX_BCV_ENABLED` | `true` | Set to `false` to enter rates by hand only. |
| `FX_BCV_TIMEOUT` | `15s` | How long one fetch from the BCV may take. |
| `FX_REFRESH_EVERY` | `4h` | How often to retry the BCV. It fetches at most once a day. |

If a value is invalid, the app names it at startup and does not start.

## Using the app

### First steps

1. Open <http://localhost:8080>.
2. **Create categories**, for example *Salary* (income) and *Groceries* (expense).
3. **Create an account**: choose its type, its currency and, optionally, the
   balance it starts with.
4. **Choose your base currency** in Settings. Your net worth is shown in it.
5. **Record income and expenses** as they happen.
6. Check the **Dashboard** and the **Recap**.

### Transfers between your own accounts

Use a transfer when you withdraw cash, top up a wallet, or move money between
two of your accounts.

- Both accounts must be active and in the **same currency**.
- One balance goes down and the other goes up by the same amount. Your total
  does not change, because nothing was earned or spent.
- A transfer is not income or an expense, and it has no category.

A transfer may leave an account negative. That is allowed on purpose: it usually
means something has not been recorded yet.

### Currency exchanges

Use an exchange when you actually bought or sold currency, for example bolívares
out of one account and dollars into another.

```
Banesco VES      -85,000.00 VES
      ↓
Mercantil USD       +100.00 USD
```

1. Open **Exchange Currency** (on mobile, **Cambiar divisas**).
2. Choose the account the money leaves and enter what you handed over.
3. Choose the account it arrives in, in a **different** currency, and enter
   what you received.

You always enter **both amounts**, as they appear on your statements. The app
works out the rate from them; you never type a rate. If the bank charged a
commission, record it as a separate expense.

### Exchange rates

Rates are used to value what you hold in your base currency. They come from two
places:

- **The BCV**, fetched automatically once a day. If the site is down, the app
  keeps working and tries again later.
- **You**, in *Settings → Exchange rates*. A manual rate wins over the BCV's for
  the same day.

If a rate is missing, the app says so and links to the form to add it. It never
guesses a value.

### Dashboard

Your net worth today, how it changed, its history chart, your income and
expenses, and your spending by category. Choose 7, 30, 90 or 365 days, or all
time.

### Recap

A summary of one **week** (Monday to Sunday) or one **month**, compared with the
previous one:

- your net worth at the start and at the end;
- income, expenses, net cash flow, savings rate and daily average spending;
- the categories where you spent more, and where you spent less;
- your five largest expenses;
- how each account's balance changed;
- four indicators that say whether things improved or got worse.

It opens on the last finished period. Use the arrows to see earlier ones or the
period in progress. A period in progress is compared with the same days of the
previous one. Each card has a **?** that explains what it shows.

### Savings

Under **Ahorro**, set a savings rate — for example 20% — and the date it starts
from. From then on, every income you record suggests how much of it to save, in
USD:

- an income in bolívares is converted at the exchange rate of **its own date**;
  if there is no rate for that day yet, the suggestion waits and is never shown
  as zero;
- transfers, currency purchases and opening balances are never income, so they
  never suggest anything;
- every income category counts unless you exclude it, and a single income can be
  included or excluded with a reason;
- changing the rate never changes the suggestions of incomes dated before the
  change, and editing an income keeps the previous suggestion in its history.

A suggestion is only a target: it moves no money and does not change your net
worth. If you start the policy on a past date, the app shows the incomes it
would reach and suggests from them only when you confirm. Setting money aside
against a suggestion, and savings goals, come in later releases.

### Mobile

Open <http://localhost:8080/mobile> on your phone, on the same network. It has
the dashboard, movements, accounts, the recap, savings and a button to record a
new operation.

## How the numbers work

- **Balances are calculated, never stored.** Each balance is the sum of its
  account's movements up to today. A movement dated in the future is listed but
  not counted yet.
- **Currencies are never added together directly.** Each account shows its own
  currency. Only the net worth converts everything, using exchange rates.
- **Each amount is valued at the rate of its own date.** Old income and expenses
  use the rate of the day they happened, not today's.
- **A missing rate is reported, never guessed.** The figure is marked as
  incomplete and the app tells you which rate is missing.
- **Archived accounts still count.** Archiving hides an account. Its balance is
  still part of your net worth.
- **An exchange can move your net worth.** If you buy dollars above the official
  rate, your net worth valued at that rate goes down. That is the real effect,
  and the app does not record it as an expense.
- **A transfer is not the "Bank transfer" payment method.** A transfer moves
  money between your own accounts. "Bank transfer" is how you paid someone or
  got paid.

## Your data

| Where | Database file |
| --- | --- |
| Running with Go | `./data/finance.db` |
| Running with Docker | `/data/finance.db`, inside a Docker volume called `finance_data` |

What Docker commands do to your data:

| Command | Your data |
| --- | --- |
| `docker compose down` | Kept. |
| `docker compose up -d --build` | Kept. |
| `docker compose down -v` | **Deleted.** Only a backup can bring it back. |
| `docker volume prune` | **Can delete it** once the container is gone. |

You may see `finance.db-wal` and `finance.db-shm` next to the database. They are
SQLite's working files; leave them alone.

## Backups and restore

Backups are written to **`./backups/` on your computer**, outside Docker, so
deleting the Docker volume does not delete them.

A backup is taken automatically:

- before the database schema is upgraded;
- every time the app shuts down;
- whenever you ask for one, in *Settings → Data*.

Only shutdown backups are cleaned up, keeping the latest 10. Each backup is a
complete SQLite file, checked before it is kept.

### Restoring a backup

Stop the app, restore, and start it again:

```sh
docker compose stop
docker compose run --rm app restore --file finance-20260905T183045Z-manual.db
docker compose up -d
```

The restore checks the backup first and saves a copy of the current database, so
it can be undone. If anything fails, nothing is changed. See
[`docs/operations.md`](docs/operations.md) for the full procedure.

## Development

### Tests

```sh
gofmt -l .            # must print nothing
go vet ./...
go test ./... -count=1
```

Tests run against a real SQLite database in a temporary folder, not a mock.

The race detector needs a C compiler. On Windows, run it in a container:

```sh
docker run --rm -v "${PWD}:/src" -w /src golang:1.27-trixie go test -race ./...
```

### Project layout

```
cmd/web/                 entry point: starts the server, or restores a backup
internal/config/         settings from environment variables
internal/database/       opening SQLite, dates and times
internal/migrations/     the database schema
internal/backup/         backups and restore
internal/money/          exact amounts and decimals
internal/currency/       currencies
internal/account/        accounts
internal/category/       categories
internal/paymentmethod/  payment methods
internal/ledger/         transactions, movements and balances
internal/application/    operations that touch two areas at once
internal/fx/             exchange rates and the BCV fetcher
internal/settings/       app settings, such as the base currency
internal/valuation/      net worth, current and historical
internal/analytics/      dashboard figures and the recap
internal/savings/        savings policy and suggestions
internal/httpserver/     the HTTP server
internal/web/            pages, handlers and view models
web/                     templates, CSS, fonts and scripts
```

It is one Go program built from these packages:
HTTP → services → repositories → SQLite. Project rules for contributors are in
[`CLAUDE.md`](CLAUDE.md).

## What it does not do

These are left out on purpose:

- **No deleting.** Records are archived or corrected; a backup is the undo.
- **No edit history** of changed records.
- **No budgets, alerts, forecasts or recommendations.** The app reports; it does
  not judge. A savings suggestion applies the rate you chose; it is not advice.
- **No transfers between different currencies.** That is a currency exchange.
- **No FX gains or losses** and no cost basis.
- **No recurring transactions,** credit cards, loans, tags or subcategories.
- **No login, no public API, no import or export, no cloud.**

## Documentation

[`docs/`](docs/README.md) has the details: architecture, data model,
operations, and the reasoning behind each technical decision.
