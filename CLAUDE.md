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
- Domain packages (`internal/currency`, `internal/account`, `internal/fx`,
  `internal/savings`, …) hold their own model, repository and service —
  organised by feature, not by layer. Do not create a package before it has
  code.
- `internal/application` is for **writes** spanning two domains atomically:
  opening an account with its balance, and (M7) recording or correcting an
  income together with its savings suggestion.
  `internal/valuation` is a cross-domain **read**, and is its own package for
  that reason (TD-052). `internal/analytics` (M6) is the dashboard's
  cross-domain read over a period, built on `valuation`.

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
aggregation. A test parses the source of every package on the money path and
fails the build if one appears.

Amounts are stored as **integer minor units** (`INTEGER` / `int64`) — cents,
céntimos — alongside their currency.

**Exchange rates are `money.Decimal`**: a canonical decimal string in the
database, `math/big.Rat` in memory. Inversion and cross rates are exact fractions
and are never materialised as decimals. Conversion rounds **once**, **half to
even**, per currency (TD-046, TD-047).

A `Money` type does not exist. `money.Decimal` is not one: it is a number, and an
amount never travels without its currency anyway.

## SQLite

- WAL journal mode, `foreign_keys = ON`, `busy_timeout = 5000`,
  `synchronous = FULL`, `_txlock = immediate`. Durability beats write throughput
  for financial records. Every read-write transaction takes the write lock at
  `BEGIN` (TD-075): a transaction that reads, decides and writes cannot be
  overtaken by another process in between.
- **Connection-scoped PRAGMAs go in the DSN, never in a one-off `db.Exec` after
  `sql.Open`.** `database/sql` is a pool: a single `Exec` configures one
  arbitrary connection and silently leaves the rest at SQLite's defaults.
- `journal_mode` is a persistent property of the file, so it is applied once in
  `database.Open` and its returned value is *checked* — the PRAGMA reports the
  resulting mode instead of failing.
- The pool is pinned to a single connection. That is deliberate; read the
  comments in `internal/database/sqlite.go` before changing it. Its corollary:
  **inside an open transaction, never call a service that uses the `*sql.DB`** —
  it waits for the connection the transaction holds, forever. Read through
  `Repository(exec)` (and `fx.NewRepository(exec)` + `fx.NewBook`) instead.
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
- `golang.org/x/net` is a direct dependency from M5, for `html` only. Only
  `html` and `html/atom` reach the binary; re-check with
  `go list -deps ./cmd/web` if that changes.
- **No decimal library.** `math/big` divides exactly; `shopspring/decimal`'s
  `Div` rounds by default (TD-046).
- goose's `go.mod` requires several database drivers directly. They do not reach
  our binary; if a dependency change is made, re-check with
  `go list -deps ./cmd/web`.
- No Viper, no ORM, no DI framework, no Redis, no PostgreSQL, no Kubernetes,
  no headless browser, no decimal library.

## Testing

- Infrastructure tests use **real SQLite** in `t.TempDir()`. Do not mock the
  driver — a mock only proves the mock behaves as written.
- Do not assert on the existence of `-wal` / `-shm` files. Their lifecycle is
  SQLite's business and testing for them buys flakes.
- Quality gates: `gofmt -l .` (silent), `go vet ./...`, `go test ./... -count=1`.
  `-race` needs a C toolchain, so run it in the Linux container on Windows.

## Current scope

**M0–M4 (closed).** Foundation; currencies and accounts; the ledger, categories,
money, opening balances and the server-rendered interface; data safety, editing
and payment methods; internal same-currency transfers.

**M4, for reference:**

- A **`TRANSFER`** is ONE transaction with **TWO** movements: negative in the
  source account, positive in the destination, equal in magnitude. They sum to
  zero. **Nothing was added to `transactions`.** `category_id` and
  `payment_method_id` are NULL, enforced by a table CHECK. Both accounts must
  share a currency and both must be active.
- **Each type has its own edit use case**, and every guard names what it ACCEPTS
  rather than what it excludes (TD-045).
- **`TRANSFER` ≠ the payment method "Bank transfer".**

**M5 (in progress): multi-currency, exchange rates and current net worth.**

- **`Reference/Valuation FX` ≠ `Transaction FX`.** M5 implements only the first:
  what a source said a currency was worth on a day, used to express balances the
  user already holds in one currency. **`transactions` gained no
  `exchange_rate_id`, no transaction carries a rate**, and nothing prepares for
  one.
- **Orientation, everywhere:** `1 base_currency_code = rate quote_currency_code`.
  Never `from`/`to`/`value`. The "base of a pair" is not the "valuation base";
  they are never called the same thing.
- **`exchange_rates` holds OBSERVATIONS**, unique on
  `(base, quote, effective_date, source)`. Several sources may speak about one
  day and all of them are kept. `rate` is canonical decimal `TEXT` (TD-046);
  positivity is checked in Go and by a `CHECK` that never casts to `REAL`.
- **`effective_date` ≠ `fetched_at`.** The BCV holds one Fecha Valor across a
  weekend AND publishes forward — its page on 2026-09-11 carried a Fecha Valor of
  2026-09-15. Neither date can stand in for the other.
- **Lookup is `latest effective_date <= valuation_date`. No exception.** Never
  `=` (weekends would need invented rows) and never `>` (that is look-ahead bias,
  TD-053). A forward-dated observation is stored, may be shown as upcoming, and
  takes no part in any resolution before its date.
- **Precedence is `MANUAL > BCV_OFFICIAL` WITHIN one effective date, never across
  dates** (TD-049). Across dates it would let one typed rate freeze the net worth
  for a year, silently.
- **Inversion and cross rates are computed, never stored.** One pivot maximum.
  Identity `X→X` is exactly 1 with no row — `USD/USD` must never exist.
- **A missing rate is reported, never guessed**: `Complete = false` plus the
  pairs and the amounts at stake. Never zero, never a partial total presented as
  whole, and never an error that hides what IS known.
- **The net worth is derived on every read and never stored.** No
  `current_net_worth`, no `converted_balance`, no cache (TD-052).
- **`app_settings` is one row** with `CHECK (id = 1)` and a typed FK to
  `currencies(code)` (TD-048). Changing the base currency rewrites nothing.
- **The BCV provider is plain HTTP + DOM parsing** (`golang.org/x/net/html`),
  anchored on element ids and verifying each block's currency label. **No
  Playwright, no Chromium, no Python, no sidecar.** All-or-nothing per
  publication; a currency this application does not have is skipped, never
  created.
- **`InsecureSkipVerify` appears nowhere**, and a test enforces it. The BCV's
  chain is broken and is handled by an additional trust anchor scoped to
  `www.bcv.org.ve` alone — not to whatever `FX_BCV_URL` names (TD-050).
- **The provider is never a dependency.** It starts after the listener, never
  blocks startup, and every failure is a log line or a message on a page — never
  a 500 and never a reason the application will not open.
- **The business calendar is Venezuela's**, `database.EconomicLocation`
  (UTC−04:00). `database.Today` is not UTC (TD-051).

**M5.5 (closed): currency exchange transactions.**

- A **`FX_EXCHANGE`** is ONE transaction with **TWO** movements in **DIFFERENT**
  currencies: negative in the source, positive in the destination. Their
  magnitudes are UNRELATED — never compare them. `category_id` and
  `payment_method_id` are NULL, enforced by a table CHECK. **Nothing was added
  to `transactions`.**
- **`FX_EXCHANGE` ≠ `TRANSFER`.** A transfer's movements cancel out and share a
  currency; an exchange's do neither. `TRANSFER` was NOT widened, and must not be.
  `ErrSameCurrency` is `ErrCurrencyMismatch`'s mirror image.
- **No `BUY_USD`, no `SELL_USD`.** Which currency was bought is which one was the
  destination. "Compra de USD" is a label a screen infers, never a stored type.
- **`Reference FX` ≠ `Execution FX`.** The execution rate is DERIVED from the two
  amounts on every read, with `math/big`, and stored NOWHERE. Recording or
  editing an exchange reads no row of `exchange_rates` and writes none (TD-055,
  TD-056).
- **Orientation:** `1 base = rate quote`. Base is USD if exactly one side is USD;
  otherwise whichever side makes the rate ≥ 1; a tie goes to the destination.
  The rate carries the smallest scale that states it exactly, and reports
  `Exact = false` when there is no such scale.
- **Valuation is untouched.** An exchange away from the reference rate moves the
  valued net worth. That difference is NEVER an expense, income, fee, gain or
  loss.
- **`UpdateExchange` has no `AllowCurrencyChange`**: both currencies and both
  amounts are explicit, so nothing is reinterpreted at another scale.
- **`MovementSide` / `AccountSideError`** are M4's `TransferSide` /
  `TransferAccountError` renamed as **aliases**, not parallel types.

**M6 (closed): historical net worth & analytics dashboard.** Read
[`docs/m6-contract.md`](docs/m6-contract.md).

- **A balance is always "as of" a date, and "current" means today** in Venezuela
  (TD-063): `SUM(movements WHERE effective_date <= today)`, never every
  movement. The rule lives in the ledger's balance queries, not in a screen. A
  future-dated movement is listed and counted (`FutureMovementCount`), never
  summed early.
- **Archived accounts are valued, current and historical** (TD-064). Archiving is
  visibility, not a financial event; an archived account with a balance is
  warned about, never excluded. This superseded M5's active-only rule.
- **One engine.** `valuation.Service.Load(through)` reads, with a constant number
  of queries, settings, currencies, every account, the ledger's daily deltas and
  the rates through that date into a `Snapshot`; `NetWorthAt`, `Series` and
  `ConvertToBaseExact` work in memory. The headline figure is the series asked
  for today. **No query per day, per account or per currency** (TD-065).
- **One rate resolver**: `fx.Book`, used by `fx.Service.Resolve` and by the
  series. M5's lookup policy is unchanged and lives only there (TD-066).
- **A zero balance needs no rate; a non-zero one without a rate is a gap**
  (TD-067): incomplete point, `Missing`, a hole in the chart, a change shown as
  unavailable. Never zero, never a later rate.
- **Cash flow is `INCOME` and `EXPENSE` only**, each converted at the rate of its
  own `effective_date`, summed exactly per category and rounded once (TD-068).
  `TRANSFER`, `FX_EXCHANGE` and `OPENING_BALANCE` are never income or expense.
- **Periods are fixed day counts** (7/30/90/365, ALL) ending today; the previous
  period is adjacent and equally long; the change is `NW(to) − NW(from − 1)`
  (TD-069). The clock is injected.
- **Percentages are `int64` basis points**, half to even, with `Defined = false`
  for a ratio over nothing. Never NaN, never Inf.
- **Breakdown lines are allocated by largest remainder** so they add up; **the
  chart is a server-rendered SVG with integer geometry** (TD-070). Templates
  only prefix a sign or a `%` to strings the backend formatted.

**M6.5 (in progress): weekly & monthly recap.** Read
[`docs/m6.5-contract.md`](docs/m6.5-contract.md).

- **Calendar periods** (TD-072): Monday–Sunday weeks, calendar months, against
  the previous calendar period. A closed period is compared with the WHOLE
  previous one; a period in progress is cut at today and compared LIKE FOR LIKE
  with the same elapsed days. Default: the last closed period. TD-069 still
  governs the dashboard's rolling presets.
- **It lives in `internal/analytics`** and reuses M6's cash flow and `compare`.
  Seven queries whatever the period.
- **Every category is compared**, none folded into "other", with a `Direction`
  the backend decides; a template never compares two amounts.
- **The largest expenses are ranked in the base currency** at each one's own
  day's rate; an unvalued one is counted, never estimated (TD-074).
- **Indicators are fixed-rule comparisons, not advice** (TD-073). No score, no
  weights, no recommendation text.

**M7 (in progress): Savings Fund & Goals.** Read
[`docs/m7-contract.md`](docs/m7-contract.md). M7.1 (policy & suggestions) is
delivered; M7.2–M7.4 are each approved before they start.

- **Four things are never confused:** income recorded (the ledger), saving
  suggested (M7.1), money reserved (M7.2), money assigned to a goal (M7.3). M7
  adds **no row and no column** to `transactions` or `account_movements`, and
  nothing in it is read by `valuation`: reserving cannot move the net worth.
- **All M7 figures are USD** (`savings.FundCurrency`), which is not the
  valuation base.
- **Only `INCOME` is saved from**, by type; categories are eligible unless
  excluded; overrides carry a reason; never by description (TD-076).
- **The policy version is chosen by the income's effective date.** Only the
  first version may start in the past (TD-077).
- **A suggestion freezes its USD valuation and copies the rate legs it used**
  (TD-078) — a deliberate exception to "derived on every read", because it is a
  record of what was suggested. The rate is still the reference rate of the
  income's date via `fx.Book`; a corrected rate is reported, never applied
  silently. A missing rate is `PENDING_VALUATION`, never zero.
- **One live revision per income** (partial unique index); a correction
  supersedes, never edits. Confirmation states are derived, never stored
  (TD-079). Reservations confirmed against a revision are never moved or
  deleted automatically.
- **An income and its suggestion commit in one transaction** through
  `internal/application`; the web adapter routes every income write there.
  Anything wider is a previewed, idempotent sync (TD-080).
- The policy's "tasa de ahorro objetivo" is **not** the recap's "tasa de ahorro".

**Design overhaul (in progress): skeuomorphic instrument** (TD-071).

- One visual system for desktop and `/mobile`: brushed chassis panels, keys for
  every action, recessed **LCD readouts for headline figures**, ledger paper for
  lists. Two finishes behind the toggle: Aluminio (light, default) and Grafito
  (dark). Every colour is a token in `app.css`; `mobile.css` only aliases them.
  A new component rule takes its colours from those tokens.
- **A figure never wraps and never overflows its container.** Put it in
  `.lcd-value` (or another `nowrap` element) with `data-fit`; `fit.js` shrinks
  it if needed. `fit.js` measures text only and must never read or compute a
  number. Every amount a template shows goes through `formatAmount`.
- **Fonts are embedded** (`web/static/fonts`, OFL). No Google Fonts, no CDN, no
  request to another host from any page.
- The desktop dashboard is a grid: summary; evolution beside accounts;
  currencies beside the wide cash-flow card; recent movements at full width.
  A new block goes into that grid, not appended below it.

**Not built, and not to be added opportunistically:**

- **No delete of anything.** Archive or correct.
- **No history of edited records.** The undo is a backup.
- **No FX attached to an ordinary transaction**, no merchant or card rate, no
  realized or unrealized FX gains, no cost basis. An `FX_EXCHANGE` derives its
  own rate; no transaction carries a rate column.
- **No FX fee model** and **no rate-driven input** (a rate plus one amount, with
  the other computed). Both amounts are always entered.
- **No cross-currency transfers**, no transfer fees, no split transactions, no
  scheduled or recurring transfers, no scheduled or automatic exchanges.
- **No budgets, spending limits, alerts, anomaly detection, notifications,
  forecasting or recommendations.** M6's dashboard reports; it does not judge.
  M7's savings suggestion is a mechanical application of a rate the user set,
  not advice, and it never moves money.
- **No projected balance**, no maximum age for a rate, no automatic importer of
  historical BCV rates, no snapshot table, no cache, no background job, no JSON
  API and no charting library.
- **No third-party rate mirror** (TD-054).
- No credit, loans, instalments (M8, M9), calendar, recurring transactions,
  budgets, tags, hierarchical categories, authentication or public API.

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
- **A guard that lists what to EXCLUDE rots when the enumeration grows.** M3's
  edit guard read `if current.Type == OpeningBalance`; adding `TRANSFER` silently
  made it wrong. Name what is allowed (TD-045). It happened a second time in M5.5,
  in the PRESENTATION layer: two handlers read `if type == TRANSFER`, and
  `FX_EXCHANGE` would have fallen straight through to the income form. The rule
  applies to every layer, not only the domain.
- **A form whose shape is patched in by a script is wrong until the script
  runs.** The mobile operation form hard-coded the income endpoint and field
  names, and a click handler rewrote them; a deep link to `?type=TRANSFER`
  therefore posted a transfer to `/transactions`. Render the shape server-side and
  let the script keep it in step.
- **Precedence that is not scoped to a date freezes a derived figure silently.**
  Ordering by source before date would let one manual rate outrank every later
  publication forever. Nothing fails; the number just stops moving (TD-049).
- **A date is not "today in UTC".** `database.Today` returned a UTC date for four
  milestones; for four hours out of every twenty-four that names a day Venezuela
  has not reached. Nothing caught it because nothing called it (TD-051).
- **Go does not chase AIA, and Windows does.** A TLS chain that verifies on the
  development machine can fail in the container. Test the container (TD-050).
- **A balance with no date silently included the future** for five milestones
  (TD-063). Nothing compared a balance with a date until a history did. And the
  forms' default date was still UTC, so once balances were cut at "today" an
  evening entry would have been tomorrow's. A date-sensitive rule needs every
  "today" in the application to be the same one: `database.TodayAt`.
- **A read model that anchors on a semantic property can hide broken data.**
  Picking a transfer's row by "the negative movement" drops a malformed transfer
  with no negative movement out of the listing entirely. Anchor structurally,
  then complain (TD-044).
