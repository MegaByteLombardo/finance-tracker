# Milestones

The living roadmap. This file is updated as milestones open and close.

**Status values**

| Status | Meaning |
| --- | --- |
| `PLANNED` | On the roadmap. Scope not yet defined or approved. |
| `IN PROGRESS` | Scope approved, implementation under way. |
| `CLOSED` | Delivered, verified against its acceptance criteria, and documented. |
| `POSTPONED` | Started or scoped, then deliberately set aside. Its work is kept, not deleted. |

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

## M4 — Internal Transfers · `CLOSED`

**Closed:** 2026-09-11

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

Delivered: the transfer creation form (`GET /transfers/new`, `POST /transfers`),
the edit form (`GET /transfers/{id}/edit`, `POST /transfers/{id}`), routing guards
in `transactions.go` redirecting transfer edits to `/transfers/{id}/edit`, and
rendering transfers with source/destination detail and positive amounts across
transaction lists and the dashboard.

### Verification

Quality gates at close: `gofmt -l .` silent, `go vet ./...` clean,
`go test ./... -count=1` green, `go test -race ./...` green in the Linux
container, `git diff --check` clean, `docker compose build` successful.

### One thing M4 shipped that should not have

The mobile dashboard header carried a **hard-coded "+2.4% este mes"** and a mock
net worth of `$5,842.37`, both invented, with no computation behind either. In a
finance application a fabricated figure is worse than a blank one: it is
indistinguishable from a real one. **Both were removed in M5**, where the net
worth became a real derived figure and the monthly change was dropped rather than
faked — a genuine one needs the net worth at a past date, which is M6's work.

---

---

## M5 — Multi-Currency, Exchange Rates & Current Net Worth · `IN PROGRESS`

**Backend, contract and presentation plumbing complete. Interface polish
outstanding.**

The first milestone that answers "what is all of this worth?" in one number —
and the first one that had to be careful about what that number is allowed to
mean.

### The rule the whole milestone hangs on

```
Reference / Valuation FX        ≠        Transaction FX
```

M5 implements **only the first**. A reference rate says what a currency was worth
on a day, and is used to express balances the user already holds in one currency,
for presentation. A transaction rate is the rate a particular operation actually
happened at. **`transactions` gained no `exchange_rate_id`, no transaction carries
a rate, and nothing in M5 prepares for one.**

### Scope

- **`exchange_rates`.** Observations, not truths: one source, one pair, one
  economic day. Several can exist for one day and precedence chooses between them
  rather than one overwriting the other, so evidence is never destroyed.
- **Exact decimals, no dependency.** `rate` is a canonical decimal `TEXT`;
  arithmetic is `math/big.Rat`, which divides exactly — necessary because
  inversion and cross rates are not finite decimals. `shopspring/decimal` was
  rejected on a specific fact: its `Div` rounds by default (TD-046).
- **Round half to even, once, per currency**, so the components a screen shows add
  up exactly to the total that screen shows (TD-047).
- **`app_settings`**, one row, holding the base currency of valuation as a typed
  column with a real foreign key (TD-048). Changing it rewrites nothing.
- **Precedence within a date, never across dates.** `MANUAL` beats `BCV_OFFICIAL`
  on the day it names and nowhere else. The obvious alternative would have let a
  rate typed once in January freeze the user's net worth for a year, silently
  (TD-049).
- **The BCV provider**: plain HTTP, DOM parsing with `golang.org/x/net/html`,
  anchored on element ids and verifying that each block states the currency it is
  expected to state. No Playwright, no Chromium, no Python, no sidecar.
- **All-or-nothing per publication.** One document, one Fecha Valor, one
  transaction.
- **Automatic refresh** at most once per Venezuelan calendar day, started *after*
  the server is listening, with every failure a log line.
- **Valuation**: direct, inverted and single-pivot cross rates, all exact, each
  reporting the observations it used.
- **A missing rate is reported, not guessed**: `Complete = false` plus the pairs
  and the amounts at stake. Never zero, never a partial total presented as whole.

### Decisions worth naming

- **The BCV's TLS chain is broken** — it serves an intermediate that is not its
  leaf's issuer — and Go does not chase AIA on Linux. It works on Windows and
  fails in the container, which is the worst possible distribution of failure. It
  is handled with an additional trust anchor **scoped to that one hostname**, with
  `InsecureSkipVerify` nowhere in the repository and a test enforcing that
  (TD-050).
- **`database.Today` was wrong and nothing had noticed**, because nothing called
  it: it returned a UTC date, which for four hours out of every twenty-four names
  a day Venezuela has not reached. The business calendar is now a fixed UTC−04:00
  zone (TD-051).
- **The BCV publishes forward.** Its page on Friday 2026-09-11 carried a Fecha
  Valor of Tuesday 2026-09-15. A fallback to the nearest observation was designed
  and then **rejected**: reaching forward is look-ahead bias, and a label on the
  screen does not undo a number computed from the future. The gap is the honest
  answer (TD-053).
- **No third-party mirror.** Both candidates were tested. One no longer resolves
  in DNS; the other publishes four decimal places where the BCV publishes eight,
  as a JSON number, under a date that is not the Fecha Valor (TD-054).
- **M4's mobile header showed a hard-coded "+2.4%" monthly change with nothing
  behind it.** It is gone. A fabricated trend in a finance application is worse
  than showing nothing, and a real one needs the net worth at a past date.

### Delivered

Migrations `00011` and `00012` (schema version 12); `internal/fx`,
`internal/settings`, `internal/valuation`; `money.Decimal` and exact conversion;
the contract in [m5-contract.md](m5-contract.md); the presentation plumbing,
routes, settings and rates screens, and the real net worth on both the desktop and
the mobile dashboard.

### Not in M5

Cross-currency transfers, FX attached to a transaction, merchant or card FX,
realized and unrealized FX gains, the historical net-worth chart, a third-party
mirror, browser automation of any kind, an external scraper service, a public REST
API, budgets, forecasting, credit and debt, recurring transactions, **any delete**,
and audit history of edits.

---

## M5.5 — Currency Exchange Transactions · `CLOSED`

A short milestone with one job: let the user record the operation this
application exists to track. In Venezuela, buying and selling dollars is not an
edge case — it is a weekly event, and until now the ledger had no way to say it
happened.

```
Compra de USD                          Venta de USD

Banesco VES                            Mercantil USD
-85,000.00 VES                         -100.00 USD
        ↓                                      ↓
Mercantil USD                          Banesco VES
+100.00 USD                            +84,500.00 VES
```

### The rule the milestone hangs on

```
Reference / Valuation FX        ≠        Execution FX
```

M5 built the first and named the second "Transaction FX". **M5.5 builds something
strictly narrower than that name suggested**: an operation between two accounts
the user owns, where both amounts are facts on a statement. No transaction
carries a rate, recording one reads no reference rate and writes none, and
`exchange_rates` is not touched at all.

### Scope

- **`FX_EXCHANGE`**: one transaction, two movements, one negative in the source
  and one positive in the destination, in two **different** currencies and of
  **unrelated magnitude**. No category, no payment method, enforced by the table
  `CHECK`.
- **`TRANSFER` was not widened.** They are different financial events: a
  transfer's two movements cancel out and an exchange's do not. `ErrSameCurrency`
  is `ErrCurrencyMismatch`'s mirror image and each belongs to exactly one of them
  (TD-055).
- **No `BUY_USD` and no `SELL_USD`.** Which currency was bought is which one was
  the destination; "Compra de USD" is a label a screen infers.
- **The execution rate is derived, never stored.** `84,220.67 ÷ 100 = 842.2067`,
  computed with `math/big` on every read, at the smallest scale that states it
  exactly — and reported as approximate, with `≈`, when no finite decimal
  expansion exists.
- **Orientation**: `1 base = rate quote`, with USD as the base whenever one side
  is USD, otherwise whichever side makes the rate at least 1. So a purchase and a
  sale of the same pair can be compared at a glance.
- **`RecordExchange` / `UpdateExchange`**, following `RecordTransfer` /
  `UpdateTransfer` exactly. Both amounts, both accounts, the date and the
  description are editable; the two movements keep their ids (TD-042).
- **No `AllowCurrencyChange` on the edit**, and its absence is the point: that
  flag exists where one amount is re-read at another scale, and an exchange names
  both currencies and both amounts explicitly.
- **Migration `00013`, schema version 13.** One `CHECK` widened. No table, no
  column, no index added anywhere — the two-table ledger from M2 did the rest.
  Forward-only (TD-038), because no honest mapping back exists.
- **Valuation untouched.** Balances move, and `internal/valuation` values them
  with reference rates as before. An exchange away from the reference rate moves
  the valued net worth, and that difference becomes **no** expense, income, fee,
  gain or loss (TD-056).

### Decisions worth naming

- **The rate is derived because a stored copy could disagree with the two amounts
  that produced it.** An edit that corrected an amount and not the rate would
  leave a row that looks fine and means nothing (TD-055).
- **A rate-driven input mode was considered and left out.** Typing a rate and one
  amount sounds convenient and is a trap: the computed amount would disagree with
  the bank by a céntimo whenever the bank rounded differently, and the ledger
  would then be wrong about what actually moved.
- **No fee field.** A commission charged separately is an `EXPENSE` under "Bank
  fees". One deducted from the exchange is already inside the two real amounts,
  and the derived rate already reflects what it cost.
- **`TransferSide` and `TransferAccountError` became `MovementSide` and
  `AccountSideError`** — as **aliases**, not parallel types, so every M4 call site
  and `errors.As` with either spelling keeps working. An exchange form has two
  account fields too; the M4 names had stopped describing the idea.

### One thing M5.5 found and fixed

The mobile "Nueva operación" form posts to several endpoints with different field
names, and the template hard-coded the income-and-expense shape while a script
rewrote it on the first tab click. **Opening
`/mobile/operacion/nueva?type=TRANSFER` directly therefore produced a form
labelled "Transferencia" that would have posted to `/transactions`** — filing a
transfer as an expense against one account. It had been there since M4 and no
test looked at it.

The server now renders the shape it was asked for, the script applies it on load
as well as on click, and a test reads `mobile.js` and checks its table against
the Go one so the two cannot drift.

Two presentation-layer guards were also rewritten. `GET /transactions/{id}/edit`
and `POST /transactions/{id}` tested `if type == TRANSFER`, which is exactly the
shape TD-045 records as rotting when the enumeration grows — `FX_EXCHANGE` would
have fallen through to the income form. Both now `switch` over every type and
refuse the default.

### Delivered

Migration `00013` (schema version 13); `ledger.FXExchange`, `RecordExchange`,
`UpdateExchange`, `ExchangeDetail`, `ExecutionRate`;
`money.NewDecimalFromRat`; the contract in [m5.5-contract.md](m5.5-contract.md);
the desktop `/exchanges` screens, the mobile "Cambiar divisas" operation, and
both amounts plus the derived rate in every listing.

### Not in M5.5

An FX fee model, an exchange-rate column on `transactions` in any spelling,
cross-currency `TRANSFER`, FX gains and losses, cost basis, tax accounting, a
rate-driven input mode, automatic or scheduled exchanges, historical FX
performance, new providers, browser automation, **any delete**, and audit history
of edits.


---

## M6 — Historical Net Worth & Analytics Dashboard · `CLOSED`

One milestone where the roadmap had two. M6 (Historical Net Worth) and M7
(Dashboard & Analytics) were merged because neither is useful alone: every figure
the dashboard needs — the change over a period, the chart, the breakdown — is a
question about the net worth at a date, and a history with no screen is a
function nobody calls.

### The question it answers

> What was everything I hold worth at the close of a given day, in my base
> currency, using only the information and the rates available for that day?

And the dashboard is built from that answer and nothing else.

### Scope

- **One engine for a date and for a range.** `valuation.Service.Load` reads,
  with five queries, everything a valuation up to a date needs into a
  `Snapshot`; `NetWorthAt`, `Series` and `ConvertToBaseExact` then work in
  memory. The headline figure is the series asked for today (TD-065).
- **One rate resolver.** M5's resolution rules moved, unchanged, into a pure
  `fx.Book`. `fx.Service.Resolve` and the series both use it (TD-066).
- **Balances are "as of" a date; "current" means today** (TD-063), in the
  ledger's own queries. Future-dated movements are counted, not summed.
- **Archived accounts are valued** (TD-064). Archiving is visibility.
- **A zero balance needs no rate; a missing rate is a gap** (TD-067).
- **`internal/analytics`**: periods, change, cash flow, savings rate, spending
  and its comparison with the previous period (TD-068, TD-069).
- **The dashboard**, desktop and mobile: KPIs, the history chart with a period
  selector, the breakdown by currency and by account, cash flow and spending. The
  chart is a server-rendered SVG with integer geometry (TD-070).

### Decisions worth naming

- **The current balance used to include future-dated movements.** Nothing
  compared a balance with a date until a history did, and the series point for
  today would have disagreed with the account screen. The rule moved into the
  ledger rather than into a dashboard filter, so there is one definition of
  "what this account holds today".
- **Excluding archived accounts from history would let a transfer create net
  worth.** Account A holds 100, transfers it to B and is archived empty; drop A,
  and the 100 appears in B from nowhere on the day of the transfer. M5's
  active-only rule was superseded rather than extended to history.
- **A zero balance needs no rate.** Without that, every day before a
  foreign-currency account's first movement would have been "incomplete" for no
  information at all.
- **Flows are valued at their own day's rate**, not the period-end one: a
  March salary converted at December's rate would make March's figures move
  every time the bolívar does.
- **Percentages are basis points.** `Ratio{BasisPoints, Defined}`; a ratio over
  nothing is undefined and shown as "—". There is no float in which a NaN could
  hide.
- **No index, no migration, no snapshot table.** Each was considered and
  measured against the reads the milestone actually makes (data-model.md).
- **No charting library.** One line, one area, gaps, hover titles: about a
  hundred lines of Go.

### Found and fixed on the way

- **The desktop forms defaulted to the UTC date.** `TodayDate()` — the default
  effective date of the account, transaction and transfer forms — still read the
  clock in UTC, so for four hours every evening it offered tomorrow. Harmless
  until M6, when a movement dated tomorrow stopped counting in today's balance.
  It now uses the Venezuelan calendar, and two tests that built "today" in UTC
  were corrected the same way.
- **The dashboard labelled an exchange as an expense.** Its recent-activity table
  read `if TRANSFER … else Expense`, the TD-045 shape, and `FX_EXCHANGE` fell
  through. Every type is now named, and a test checks it.

- **The mobile movements summary summed rows in its handler — and the wrong
  ones.** It added up the income and expenses of the last hundred listed
  transactions, future-dated ones included, under a heading that named the
  current month. It now shows `analytics.MonthToDateCashFlow`: the first of the
  month to today, `INCOME` and `EXPENSE` only, each at its own day's rate, in the
  valuation base, with the same incomplete-figure warning as the dashboard. No
  amount is summed anywhere in `internal/web` any more.
- **Archiving an account had no button.** The domain supported it and M6 warns
  about archived accounts with a balance, but no screen could do it. Both now
  can: `POST /accounts/{id}/archive` and `/restore`, which name the state they
  set rather than toggling, so a double submission cannot undo itself. The edit
  page states the balance and that it keeps counting; the mobile accounts screen
  lists archived accounts in their own section with a restore action.

### Delivered

`internal/analytics`; `valuation.Snapshot`, `GetNetWorthSeries`, the breakdown by
account; `fx.Book`, `fx.Service.BookThrough`, `Repository.ListActiveThrough`
(replacing `LookupOn` and `ListApplicableFor`); `ledger.ListDailyAccountDeltas`,
`ledger.ListFlows`, as-of balances and `FutureMovementCount`;
`money.ConvertMinorExact`, `RoundHalfEven`, `RatioBasisPoints`, `Allocate`; the
web `AnalyticsService`, its adapter and fake, `chart.go`, the rebuilt desktop
dashboard and the mobile history and cash-flow cards; the contract in
[m6-contract.md](m6-contract.md); TD-063 to TD-070. Schema version unchanged: 13.

### Verification

- `gofmt -l` silent, `go vet ./...` clean, `go test ./... -count=1` green, and
  `go test -race ./...` green in the Linux container.
- The no-float test now covers `internal/analytics` too.
- The milestone's twelve cases each have a test against real SQLite: single
  currency, several currencies, historical FX, no future leakage, a missing rate,
  income, expense, transfer, currency exchange, an account with no activity, an
  empty portfolio, zero income, period comparison and both boundaries.
- Desktop and mobile were checked in a browser against a seeded ledger with a
  month of missing rates, a future-dated expense, a transfer and an exchange.

### Not in M6

Budgets, spending limits and alerts; anomaly detection; notifications; credit,
debt and instalments; forecasting and recommendations; a projected balance; an
automatic importer of historical BCV rates; a maximum age for rates; snapshot
tables, caches or background jobs; a JSON API; production deployment; **any
delete**; and audit history of edits.

---

## M6.5 — Weekly & Monthly Recap · `IN PROGRESS`

The dashboard answers "how am I doing over the last N days". The recap answers
the question a person actually asks at the end of a week or a month: how did it
go against the one before, what went up, what went down, and where do I stand
now. It is built on M6's engine and changes none of its rules. The contract is
[m6.5-contract.md](m6.5-contract.md).

### Scope

- **Calendar periods** (TD-072): Monday-to-Sunday weeks and calendar months,
  compared with the previous calendar period; a period in progress is compared
  like for like. The default is the last closed period, with arrows back to the
  first movement and forward to the period in progress.
- **Net worth then and now**: the close of the day before the period against the
  close of its last day, and the same movement over the previous period.
- **Cash flow against the previous period**: income, expenses, net, savings rate
  (in percentage points) and the daily average expense.
- **Every category compared**, with a direction — up, down, same, new, gone — and
  none folded into "other".
- **The five largest expenses**, ranked in the base currency (TD-074).
- **The change of each account**, in its own currency and in the base.
- **Four indicators** with fixed rules and a summary (TD-073).
- `/recap` on desktop (in the main navigation and linked from the dashboard) and
  `/mobile/recap` (linked from Inicio).

### Decisions worth naming

- **The recap lives in `internal/analytics`.** It is the same cross-domain read
  over a period as the dashboard and reuses its cash-flow arithmetic and
  `compare`; a separate package would have had to export them for nothing.
- **A month in progress is not compared with a whole month.** October 1–12 is
  compared with September 1–12. The alternative always shows spending falling.
- **"Better" and "worse" are stated rules, not advice.** Each indicator names
  its rule on the screen; nothing is weighted or recommended.
- **The difference between the net worth's change and the net cash flow is left
  visible, not explained.** It is the effect of valuation at moving rates.
  Splitting it out was considered and left out: it is not a gain, a loss or a
  fee (M5.5), and naming it invites reading it as one.

### Delivered

`analytics.Recap`, the calendar periods (`calendar.go`), `compareCategories`,
`allocateShares` (extracted from the spending card); `ledger.ListExpenses`; the
web `Recap` types, adapter, fake and handlers; `recap/index.html` and
`mobile/recap.html`; the contract in [m6.5-contract.md](m6.5-contract.md);
TD-072 to TD-074. Schema version unchanged: 13.

### Verification

- `go vet ./...` clean and `go test ./... -count=1` green. `gofmt` clean on every
  changed file.
- Against real SQLite: calendar boundaries (month and year ends, leap years, the
  31st), the like-for-like cut, anchor clamping, the first period with nothing
  before it, each category direction, the largest expenses across two
  currencies, an archived account, every indicator, and a missing rate that
  makes each dependent figure unavailable rather than zero.
- The recap was rendered against a seeded ledger of four months in two
  currencies, desktop and mobile, for a closed month and a week in progress.

### Not in M6.5

Budgets, goals, alerts, notifications and recommendations; a combined score; a
decomposition of the net worth's change into cash flow and exchange-rate effect;
custom ranges and quarterly or yearly recaps; a stored or emailed recap; caches,
snapshot tables, background jobs and a JSON API.

---

## M7 — Savings Fund & Goals · `IN PROGRESS`

Replaces the M7 the roadmap called Credit & Debt, which becomes M8. The ledger
says where the money is; M7 adds what the user means to do with it: a policy
that suggests a share of every eligible income, money set aside against real USD
accounts, and goals it is assigned to. Four submilestones, each approved and
closed on its own. The contract is [m7-contract.md](m7-contract.md).

| Submilestone | Status |
| --- | --- |
| M7.1 — Savings policy & suggestions | `CLOSED` (2026-10-18) |
| M7.2 — Savings fund | `PLANNED` |
| M7.3 — Savings goals | `PLANNED` |
| M7.4 — Ledger integration & dashboard | `PLANNED` |

### The rule the milestone hangs on

**A layer over the ledger, never a second ledger.** Income recorded, saving
suggested, money reserved and money assigned to a goal are four different facts
in four different places. Nothing in M7 adds a row or a column to `transactions`
or `account_movements`, and nothing in it is read by the valuation: reserving
money cannot change the net worth.

### M7.1 delivered

- **Eligibility** (TD-076): `INCOME` only, by type; every income category
  eligible unless excluded; per-income overrides with a reason. Never by the
  description.
- **A versioned policy** (TD-077): basis points, chosen by the income's
  effective date; only the first version may start in the past; a rate that
  governs suggestions never changes.
- **One suggestion per eligible income**, written in the same transaction as the
  income (TD-080), in USD at the reference rate of the income's date, rounded
  once, half to even. The valuation and the observations used are frozen
  (TD-078); a corrected rate is reported and revalued only on request.
- **Revisions** (TD-079): a correction closes the live revision and opens the
  next; at most one live revision per income, enforced by a partial unique
  index. A missing rate leaves a suggestion pending, never zero; a future-dated
  income in another currency waits for its date (TD-081).
- **A previewed, idempotent sync** for a retroactive start, a category rule and
  anything changed outside the application.
- **`BEGIN IMMEDIATE`** for every read-write transaction (TD-075).
- **Screens**, desktop and mobile: overview, policy and eligibility, sync
  preview, one income's history.

Schema version 13 → 15 (00014, 00015), purely additive and reversible. The
ledger gained `RecordIncomeTx`, `UpdateTransactionTx`, `GetIncome` and
`ListIncomesSince`, with no change of behaviour.

### M7.1 verification

- `go vet ./...` clean, `go test ./... -count=1` green, `gofmt` clean on every
  changed file, and `go test -race -count=1 -p 4 ./...` green in the Linux
  container. At the default parallelism, under the race detector,
  `cmd/web`'s `TestShutdownTakesABackup` can exceed its 30-second wait for the
  server to start while twenty packages compete for the CPU; it passes alone
  in about a second, on this branch and on `main`.
- The no-float test covers `internal/savings` and `internal/application`.
- Against real SQLite: $800 at 20% suggests $160; $500 + $300 suggest $160; a VES
  income at the rate of its date, never a later or forward-dated one; a cross
  rate through the pivot; a missing rate, then valued in place; a future-dated
  income; an exchange and a transfer suggest nothing; single half-even rounding;
  20% → 25% leaves earlier suggestions alone; every kind of correction; a
  dismissed suggestion; a corrected rate and its revaluation; a retroactive start
  backfilled only on confirmation, idempotently; eligibility previewed and
  overrides applied; atomicity (a failed suggestion leaves no income); a
  suggestion counted in its income's period; concurrent incomes and concurrent
  syncs, in one pool and in two pools over one file.
- An upgrade from a schema 13 database with M3–M5 data changes no row, creates
  no suggestion, and rolls back cleanly.
- Desktop and mobile were checked in a browser against a seeded database.

### Not in M7.1

Reservations, confirmation, the fund, goals, distribution, the expense and
transfer integration, reconciliation of reserves and the dashboard block.

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
| M5 | Multi-Currency, Exchange Rates & Current Net Worth | `IN PROGRESS` |
| M5.5 | Currency Exchange Transactions | `CLOSED` |
| M6 | Historical Net Worth & Analytics Dashboard | `CLOSED` |
| M6.5 | Weekly & Monthly Recap | `IN PROGRESS` |
| — | Production Deployment, Private Access & Continuous Backup | `POSTPONED` |
| M7 | Savings Fund & Goals | `IN PROGRESS` (M7.1 closed) |
| M8 | Credit & Debt | `PLANNED` |
| M9 | Instalments & Calendar | `PLANNED` |
| M10 | Hardening | `PLANNED` |

**M7 is now Savings Fund & Goals**, and Credit & Debt, Instalments and Hardening
each moved down one. Planning savings needs only what the ledger already holds;
credit needs a model of its own.

**M6 merges what were M6 (Historical Net Worth) and M7 (Dashboard &
Analytics).** They were never separable: every figure the dashboard needs — the
change over a period, the chart, the breakdown — is a question about the net
worth at a date, so building the history without its only consumer, or the
dashboard without its only source, would each have produced half a feature.

**Production deployment is `POSTPONED`, not dropped.** It was inserted as M6 on
the `cloud-hosting` branch, where its work (Compose for production, Litestream,
Tailscale, the OCI runbook, TD-057 to TD-062) is kept intact and unmerged. It
takes a milestone number again when it is resumed. The application keeps running
in its current local environment until then.

The roadmap has now been renumbered three times, which is what "provisional"
above means in practice. Everything after M3 shifted by one when M3 became Data
Safety rather than Dashboard; transfers then moved from M6 to M4, ahead of both
Dashboard and FX, because the ledger already supported them and a dashboard
showing money that appears to vanish between two accounts would have been the
wrong thing to build first. FX then moved ahead of Dashboard for the same kind of
reason: a dashboard whose headline figure could not be computed would have been a
dashboard with a hole in it — which is exactly what M4 shipped, with a hard-coded
"+2.4%" standing in for a number nothing could produce yet.

### Known carry-over

Parked items, none blocking:

- **Container `HEALTHCHECK`.** The distroless image has no shell and no `curl`,
  so the usual form does not apply. The clean approach is a self-check flag on
  the binary, worth adding when something is orchestrating restarts.
- **Request-logging middleware.** Deferred until there are endpoints worth
  tracing.
- **Migration `status` / `down` interface.** No CLI or flags were added: the
  startup logs report what happened, and `goose_db_version` is one query away in
  the sqlite3 CLI. Revisit if a rollback is ever needed in anger.
- **Purpose dimension for accounts.** `account_type` classifies the container
  only. M7 answers "which money is savings" without touching accounts: a
  reservation (M7.2) marks part of an account's balance, so one account can hold
  both. If a per-account purpose is ever still needed, it is its own decision.
- **Historical backfill of exchange rates.** M5 stores what it is told and what it
  fetches from today onward; there is no history before the first run. M6 decided
  that manual entry is enough for now: days without a rate are gaps on the chart,
  never guesses (TD-067). An importer of the BCV's historical files would be its
  own milestone.
- **Cosmetic dates in the mobile shell.** The greeting and the date headings still
  read the clock in UTC. They are display text, not financial dates — the form
  defaults and the valuation date were fixed in M5 — but they should follow the
  same calendar.
