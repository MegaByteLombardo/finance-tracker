# Architecture

This document describes the architecture **as actually implemented at M5**.
Layers that do not exist yet are marked as such and are not described as if they
did.

## Shape

A **modular monolith**: one process, one binary, one database file. Internal
boundaries are enforced by package structure, not by network calls.

There are no microservices, no message broker, no cache tier and no separate
database server. At the scale of one person's finances, those would add
operational surface without adding capability.

## Stack

| Concern | Choice |
| --- | --- |
| Language | Go 1.27 |
| HTTP | `net/http` (standard library) |
| Database access | `database/sql` (standard library) |
| SQLite driver | `modernc.org/sqlite` — pure Go, no cgo |
| Logging | `log/slog` (standard library) |
| Storage | SQLite, a single local file |
| Migrations | `github.com/pressly/goose/v3`, SQL embedded in the binary |
| HTML parsing | `golang.org/x/net/html`, for the exchange-rate provider only |
| Exact decimals | `math/big` (standard library) — no decimal dependency |
| Packaging | Docker, multi-stage build |
| Runtime image | `gcr.io/distroless/static-debian13:nonroot`, uid 65532 |

There are **three** direct dependencies: `modernc.org/sqlite`,
`github.com/pressly/goose/v3` and, from M5, `golang.org/x/net` v0.59.0.

The third one earned its place narrowly. M5 reads the Banco Central de
Venezuela's published rates out of an HTML page, and the two alternatives were
matching that page with regular expressions — which cannot tell an attribute from
text, breaks on an added space, and fails by silently matching the wrong thing —
or writing an HTML5 parser. Only `html` and `html/atom` reach the binary
(`go list -deps ./cmd/web`); nothing else in `x/net` does.

**No decimal library was added.** Exchange rates are exact through `math/big`;
see [TD-046](technical-decisions.md).

Still no web framework, no router, no ORM, no configuration library, no
dependency-injection container, and no headless browser.

## Dependency direction

```text
cmd/web                         serve (default) | restore
   ↓
HTTP                    (internal/httpserver, internal/web)
   ↓
cross-domain reads      (internal/analytics -> internal/valuation)
cross-domain workflows  (internal/application)
   ↓
domain services         (internal/currency, internal/account,
                         internal/category, internal/paymentmethod,
                         internal/ledger, internal/fx, internal/settings)
   ↓
repositories            (same packages)
   ↓
database                (internal/database, internal/migrations)
   ↓
SQLite

internal/money is a leaf: it depends on nothing and everything above may use it.
internal/backup sits beside the database layer -- it snapshots and restores the
file rather than reading the domain.
internal/fx reaches OUT as well as down: it is the only package that makes a
network request, and it is the only one whose failure is routine.
```

Dependencies point downward only. HTTP does not build SQL; the database layer
knows nothing about HTTP. Domain packages may depend on each other where the
domain itself does — `account` uses `currency`, because an account is denominated
in one, and `ledger` uses `account`, `category` and `currency`, because a
movement lands in an account, is classified, and is denominated — but never in a
cycle.

**`internal/application` exists because of that last constraint.** Opening an
account and recording its opening balance is one user action spanning two
domains, and it must be atomic. It cannot live in `account`, which would have to
import `ledger`, nor in `ledger`, whose job is not creating accounts. It holds
exactly one workflow and is not a general application layer; see
[TD-030](technical-decisions.md).

**The presentation layer is built against a written contract, not against the
packages directly.** M2 was delivered by two agents working in parallel, so the
boundary between the domain services and `internal/web` is specified in
[m2-contract.md](m2-contract.md) and frozen for the milestone: the service
methods, their inputs and outputs, the enumerations, the money and date
semantics, and the sentinel errors the handlers match on.
[m3-contract.md](m3-contract.md) extends it and records what M3 breaks.

Those documents are the interface. If the two sides disagree, the contract is what
they are both wrong or right against.

`internal/web/adapter` is what makes that possible in practice. `internal/web`
defines its own vocabulary rather than importing the domain packages, so the
adapter translates in both directions and maps each domain sentinel to the web
one. It is the only place that knows both languages.

## Packages

### `cmd/web`

The **composition root**, and the only place that wires packages together.

It dispatches on the subcommand -- no arguments means `serve`, which is what the
container does -- then loads configuration, builds the logger, opens the database,
takes a pre-migration snapshot if one is warranted, runs migrations, creates the
listener, starts the server, waits for a signal, and shuts everything down in
order, snapshotting once more on the way out.

The one other subcommand is `restore`, which replaces the database from a
snapshot and exits. It lives in this binary rather than a second one because it
needs exactly what this binary already has, and the container's ENTRYPOINT is the
binary, so `docker compose run --rm app restore …` works unchanged. See TD-035.

Every failure is a returned error — there is no `panic` on an ordinary error path
and no `os.Exit` buried in a helper.

The startup and shutdown logic lives in a `run` function that takes its
environment and output stream as parameters, which is what makes a full
startup/shutdown cycle testable.

### `internal/config`

Loads and validates configuration from the environment: `APP_ENV`, `HTTP_ADDR`,
`DB_PATH`, `LOG_LEVEL`, `BACKUP_DIR` and `BACKUP_RETENTION`, each with a working
default.

Validation collects every problem and reports them together, so a misconfigured
deployment reveals all of its mistakes in one startup attempt instead of one per
restart. Invalid configuration is an error, never a panic.

The environment is read through an injected function rather than `os.Getenv`
directly, so tests describe an environment without mutating the one they run in.

### `internal/database`

Owns **every** decision about how SQLite is opened: the DSN, the PRAGMAs, the
connection pool, and the verification that the requested settings actually took
effect. Nothing else in the codebase builds a DSN or issues a PRAGMA.

It also exposes a way to read the *effective* settings back, which serves both
the startup log line and the tests.

Two further things live here because they are persistence conventions rather
than anything domain-specific:

- **`DBTX`** — the three-method subset of `*sql.DB` that `*sql.Tx` also
  satisfies. Repositories are built over it, which is what lets a service run a
  read and a dependent write inside one transaction. It is not a repository
  abstraction and does not exist to be mocked; see TD-021.
- **The timestamp convention** — `TimeLayout`, `FormatTime`, `ParseTime` and
  `Now`. One definition of how time is stored, used by every domain package.
- **The calendar-date convention** — `DateLayout`, `FormatDate`, `ParseDate` and
  `Today`. A business date is a different fact from an instant, and gets a
  different representation; see TD-028.

### `internal/migrations`

Owns the schema and its evolution. The SQL files sit beside the Go file and are
compiled into the binary with `embed.FS`, so a deployment is a single executable:
no goose CLI in the runtime image and no loose `.sql` files to keep in sync.

goose discovers migrations by globbing `*.sql` at the **root** of the filesystem
it is given, which is why the package and its SQL share a directory and no
`fs.Sub` is needed.

**This is the only package that imports goose.** Everything else sees `Up`, plus
two questions the rest of the system needs answered without knowing how
migrations work: `PendingOnExistingSchema`, which decides whether startup takes a
snapshot first, and `ExpectedTables`, which lets a backup be validated against the
schema version it was actually taken at.

### `internal/backup`

Snapshots of the operational database, and putting one back.

It is infrastructure rather than a domain: it has no financial model and no money
rules, and it knows about SQLite files rather than about accounts. Snapshots are
taken with `VACUUM INTO` against the live database, validated before they are
given a real name, and written to a directory **outside** the Docker volume --
because `docker compose down -v` destroys volumes, and that is how this project
lost data once. See TD-032 to TD-035.

`Restore` is a package-level function rather than a method on the service: it
operates on files with the database closed, which is the opposite of what a
service holding an open pool would do.

### `internal/paymentmethod`

The channels money moves through, as rows the user administers. It mirrors
`internal/category` exactly -- model, repository, service, archive rather than
delete -- because it is the same shape of problem. M2 held these four values in a
`CHECK` constraint; TD-036 covers why they became data.

### `internal/money`

Exact conversion between the decimal strings a person types and the `int64`
minor units the database stores, at a currency's scale. Two functions and a
sentinel error.

It is a **leaf**: it imports nothing from this project, which is what lets every
layer above use it without creating a cycle. It is deliberately not part of
`currency` — that package is a catalogue of rows, this is numeric
representation, and the exact rate arithmetic of the FX milestone will land here
rather than in the reference data. See TD-027.

There is no `Money` struct. An amount never travels without its currency in this
codebase, so a wrapper would carry no invariant the surrounding code does not
already hold.

### `internal/currency`, `internal/account`, `internal/category` and `internal/ledger`

One package per domain, each holding its model, its validation, its repository
and its service. Organised by feature rather than by layer: there is no
`domain/entities`, `application/usecases`, `infrastructure/repository` tree.

- `<domain>.go` — the model, its constants, its sentinel errors, and
  validation/normalisation of input. No SQL, no `context`.
- `repository.go` — translation between the model and SQL, one hand-written
  statement per method. No business rules; it converts `sql.ErrNoRows` into the
  domain's `ErrNotFound`.
- `service.go` — the use cases. The only surface a future handler will call.

Repositories are concrete structs, not interfaces. There is one implementation,
tests run against real SQLite, and an interface with a single implementation and
no test double is indirection without a purpose.

Each package declares its own sentinel errors rather than sharing a common
package — the standard library does the same with `sql.ErrNoRows` and
`os.ErrNotExist`, and it avoids a junk-drawer package. Where one domain consults
another, it **translates**: `account` returns `ErrCurrencyNotFound` and
`ErrCurrencyInactive` rather than letting `currency`'s errors escape, so a
handler for accounts never needs to import `currency`. `ledger` does the same for
`account`, `category` and `money`, which is why it declares
`ErrAccountInactive`, `ErrCategoryInactive`, `ErrCategoryTypeMismatch` and
`ErrInvalidAmount` of its own.

**`internal/ledger` is where the two-table ledger lives**: `transactions` for the
financial event, `account_movements` for its effect on an account. Its service
exposes one method per use case — `RecordIncome`, `RecordExpense`,
`RecordOpeningBalance`, `RecordTransfer`, `RecordExchange` — rather than a
general `CreateTransaction(type, …)`, so that invalid combinations have no
signature to arrive through. Editing follows the same rule, one use case per
type: `UpdateTransaction` for income and expenses, `UpdateOpeningBalance`,
`UpdateTransfer`, `UpdateExchange`. Each refuses every type it does not handle
(TD-045). It is also the only package that reads or writes an amount.

**Transfers live here too, not in a package of their own.** A transfer
(`transfer.go`) writes the same two tables through the same repository, validates
with the same helpers, and speaks the same errors as every other ledger use case;
a separate package would have to import all of that back, or `ledger` would have
to import it, and either way the boundary would be a line drawn through one idea.
It is not in `internal/application` either: that is for workflows spanning two
domains that cannot import each other, and a transfer touches `account` and
`currency`, which `ledger` already imports for every other use case.

A transfer is the first transaction with **two** movements — one negative, one
positive, equal in magnitude — which is what the two-table split existed for
since M2 (TD-041). Its rules span several rows, so they are held by one function
inside one `sql.Tx` and re-checked on read rather than by triggers (TD-044).

**Currency exchanges live here too** *(M5.5)*, in `exchange.go`, beside transfers
and for the same reasons. An `FX_EXCHANGE` is money the user actually exchanged
between two of their own accounts in two **different** currencies: one
transaction, two movements, and — unlike a transfer — two amounts whose
magnitudes have no relation to each other. `TRANSFER` was not widened to accept
it; they are different financial events, and one of them leaves the total held in
its currency unchanged while the other does not (TD-055).

The rate an exchange happened at is **derived from its two amounts on every
read**, exactly, with `math/big`, and stored nowhere. That derivation is in this
package rather than in a template because it is arithmetic on money, and because
choosing which of the two currencies the rate is quoted in requires comparing two
exact fractions. It has nothing to do with `internal/fx`: this package does not
import it, does not read `exchange_rates`, and does not write to it (TD-056).

**Transaction-scoped entry points.** Where a use case must be composable into a
larger transaction, the package exposes a second form taking a `database.DBTX`:
`account.CreateTx`, `ledger.RecordOpeningBalanceTx`. There is no
`RecordTransferTx`, because nothing needs to compose a transfer into a larger
workflow; it will be added if something ever does. The service method becomes a
thin wrapper that begins, delegates and commits. This is what
`internal/application` composes, and it is the `DBTX` mechanism of TD-021 applied
one layer up.

### `internal/fx` *(M5)*

Reference exchange rates: what a source said one currency was worth in another,
on a given day, and how to turn those observations into an exact conversion
factor.

Everything here is **reference / valuation FX**, used to express balances the
user already holds in a single currency. It is never the rate a particular
operation happened at; `transactions` carries no rate and gained no
`exchange_rate_id`. The two are different facts and the package documentation says
so at the top, because conflating them would make a stored number ambiguous
forever.

M5.5 made that boundary load-bearing rather than theoretical. `internal/ledger`
now records real currency exchanges and derives the rate each one happened at —
and the two packages still do not touch. `ledger` does not import `fx`, `fx` does
not know `FX_EXCHANGE` exists, and `valuation` values balances with reference
rates regardless of what the user traded at. The visible consequence is intended:
an exchange away from the reference rate moves the valued net worth, and nothing
converts that difference into a gain, a loss or a fee (TD-056).

It is the **only package in this application that makes a network request**, and
the only one whose failure is routine. That shapes it:

- `RateProvider` is the one interface in the package, and it exists because there
  are genuinely two implementations — the BCV's website and the fixture-backed one
  the tests drive. It is not a plugin framework.
- **Manual entry is a use case, not a provider.** A provider fetches: it has a
  network, a document and a publication date of its own. A person typing a rate
  has none of those, and its source is not theirs to choose — the backend sets
  `MANUAL`, so a form submission cannot claim to be the Banco Central.
- The provider takes an `*http.Client` and a base URL, which is the seam the tests
  use with `httptest.Server`. No HTTP interface was invented: the standard library
  already has one.
- `provider_bcv.go` parses the DOM with `golang.org/x/net/html`, anchoring on
  element ids and **verifying that each block states the currency it is expected
  to state**. A rearranged page fails loudly rather than being read as a dollar
  rate. A missing rate is recoverable and visible; a wrong one is neither.
- `bcv_tls.go` holds the one uncomfortable thing in this milestone: the BCV serves
  a broken certificate chain, and reaching it needs an additional trust anchor
  scoped to that one hostname. It is documented in full at the top of the file and
  in [TD-050](technical-decisions.md). `InsecureSkipVerify` appears nowhere in the
  repository and a test keeps it that way.
- `refresh.go` is one goroutine and one ticker. Every failure it meets is a log
  line, because there is nobody to return an error to.

**Resolution is one pure function over an in-memory `Book`** *(M6)*. The lookup
policy — the latest `effective_date <= D`, never later; `MANUAL > BCV_OFFICIAL`
within one date only; identity, direct, inverted, one pivot — lives in
`book.go` and nowhere else. `Service.Resolve` loads the observations in effect on
a date (`ListActiveThrough`, one query) and asks a `Book`; the historical series
builds one `Book` for its whole range and asks it once per currency per day. A
single date and five years of dates are therefore resolved by the same code
([TD-066](technical-decisions.md)). There is still no maximum age for a rate.

### `internal/settings` *(M5)*

The application's own configuration, as opposed to the deployment's, which is
`internal/config`'s. One model, one repository, one service, one row.

The base currency of valuation lives here rather than in an environment variable
because it must be a foreign key to a row in `currencies`
([TD-048](technical-decisions.md)). It translates `currency`'s errors into its
own, the way every domain package here does.

### `internal/valuation` *(M5)*

The consolidated net worth: everything the user holds, expressed in one currency.

It reads from `ledger`, `fx` and `settings`, which do not know about each other,
and it **writes nothing**. That is why it is not in `internal/application` —
that package exists for writes that must span two domains atomically (TD-030), and
the atomicity argument simply does not apply to a read. It is not inside `fx`
either, which would make the package that owns exchange rates depend on the
ledger.

**Nothing it produces is stored.** No column, no table, no cache. The net worth is
a function of the movements, the rates and the base currency, recomputed on every
read; see [TD-052](technical-decisions.md).

A currency it cannot value is **reported**, not raised: the result carries
`Complete = false` and the list of missing pairs, so the interface can show what
is known and name what is not.

**M6 made it historical.** `Service.Load(through)` reads, with a constant number
of queries, everything a valuation up to a date needs — settings, currencies,
every account (archived ones included), the ledger's daily deltas per account,
and the rates in effect through that date — into a `Snapshot`. Every figure after
that is computed in memory:

- `NetWorthAt(D)` — the net worth at the close of D, with its breakdown by
  currency and by account, allocated by largest remainder so the lines add up
  ([TD-070](technical-decisions.md));
- `Series(from, to)` — one point per day, walking the calendar once and adding
  each day's deltas to running per-currency balances ([TD-065](technical-decisions.md));
- `ConvertToBaseExact(amount, currency, D)` — an amount at the rate of its own
  day, unrounded, for the cash flow.

`GetCurrentNetWorth` is `NetWorthAt(today)`, so the headline figure and the last
point of the chart are the same computation. A balance as of D is the sum of the
movements dated on or before D — the same rule the ledger's current balances use
([TD-063](technical-decisions.md)). Archived accounts are valued like any other
([TD-064](technical-decisions.md)). A zero balance needs no rate; a non-zero one
without a rate makes that day a gap ([TD-067](technical-decisions.md)).

### `internal/analytics` *(M6)*

The dashboard's questions about a period: how the net worth moved, what came in,
what went out, and where it went. Like `valuation` it is a cross-domain **read**
and writes nothing.

It owns the arithmetic **of a period** and nothing else: the presets (7D, 30D,
90D, 1Y, ALL), the previous period of equal length, the change from the close of
the day before the period, income, expenses, net cash flow, the savings rate,
spending by category and its comparison. Balances, history and conversion come
from one `valuation.Snapshot`; flows come from `ledger.ListFlows`, which accepts
`INCOME` and `EXPENSE` and nothing else ([TD-068](technical-decisions.md)). Each
flow is converted at the rate of its own day and rounded once per category.

Every percentage is an integer number of basis points rounded half to even by
`internal/money`, and a ratio over nothing is `Defined = false` — never NaN,
never infinity. "Today" comes from a clock injected by the composition root, so
every period is testable with fixed dates ([TD-069](technical-decisions.md)).

One dashboard request costs a constant number of queries whatever its period:
the snapshot's five plus one for the flows of the current and previous period
together. No query is issued per day, per account or per currency.

**The recap** *(M6.5)* lives here too: `Recap` reads the same snapshot for a
calendar week or month and the one before it (`calendar.go`, TD-072), compares
every category, ranks the period's largest expenses from `ledger.ListExpenses`
(TD-074) and reports four fixed-rule indicators (TD-073). Seven queries whatever
the period: the snapshot's five, the flows, and the expenses.

### `internal/application`

Cross-domain workflows, and nothing else. One today: `OpenAccount`, which creates
an account and records its opening balance in a single `sql.Tx`.

A workflow earns a place here by coordinating two domains atomically. Pass-through
wrappers do not belong in it, and neither does anything a single domain service
can already do — **and neither does a read**. M5's consolidated net worth spans
three domains and went to `internal/valuation` instead, because it writes nothing
and the atomicity that justifies this package does not apply to it. M6's history
and analytics went to `internal/valuation` and `internal/analytics` for the same
reason.

### `internal/httpserver`

Builds the HTTP server and its routes. Owns the server timeouts and routes
`net/http`'s internal error reporting into the structured logger.

Routing uses the standard `http.ServeMux`, which since Go 1.22 matches on method
and path wildcards — the capability that used to justify a third-party router.

It owns `GET /healthz` and, from M2, mounts the presentation layer's handler at
`/`. The health endpoint stays here rather than moving into `internal/web`: it
reports on the process and its database, not on anything a user looks at.

`http.Server.Addr` is deliberately left empty. The composition root creates the
listener itself, so a port conflict is reported as an error before the process
claims to be listening.

## SQLite configuration

These settings are in force on every connection:

```text
journal_mode     = WAL
foreign_keys     = ON
busy_timeout     = 5000
synchronous      = FULL

MaxOpenConns     = 1
MaxIdleConns     = 1
ConnMaxLifetime  = 0   (never expire)
ConnMaxIdleTime  = 0   (never expire)
```

### Why the connection-scoped PRAGMAs live in the DSN

`database/sql` is a **connection pool**. Running `db.Exec("PRAGMA foreign_keys = ON")`
once after `sql.Open` configures exactly one arbitrary physical connection and
leaves every other connection the pool later opens at SQLite's default of `OFF`.

That failure is silent. Foreign keys simply stop being enforced on some queries,
with no error anywhere.

`foreign_keys`, `busy_timeout` and `synchronous` are therefore encoded in the
connection string, which the driver applies on every connection it opens. That
is the only formulation under which the guarantee holds for the whole pool — and
it is verified by a test that holds several connections open at once and checks
each one independently.

The driver's validated shorthand keys are used rather than the general
pass-through form, because the shorthand keys are all validated before any of
them are applied: a malformed DSN fails cleanly instead of leaving a connection
half-configured.

`_txlock` is deliberately **not** set; SQLite's default `DEFERRED` transaction
behaviour stands until real write paths exist to justify anything else.

### Why `journal_mode` is handled separately

WAL is not a connection setting. It is a **persistent property of the database
file** — once set, it survives closing and reopening, and applies to every
connection thereafter. Re-issuing it on each pooled connection would be
redundant work that can return `SQLITE_BUSY` under concurrency.

More importantly, `PRAGMA journal_mode = WAL` **does not fail** when it cannot be
applied. It returns the mode the database actually ended up in — `"memory"` for
an in-memory database, for instance. Treating that as success would mean
silently running without WAL.

So it is applied once at startup and the **returned value is checked**. If the
database did not end up in WAL mode, startup fails with an explicit error rather
than continuing in an unknown state.

### Why the pool holds a single connection

SQLite is an in-process library, not a network server. The usual pool advice —
several connections, a five-minute `ConnMaxLifetime` — is written for databases
reached over a socket, where connections are expensive to establish and get
killed out from under you by load balancers and idle timeouts. None of that
applies here: recycling a connection buys nothing and only costs reopening the
file and re-applying the PRAGMAs. Hence lifetimes of zero.

A single open connection serialises all access from this process, which makes
`SQLITE_BUSY` structurally impossible from within the application. `busy_timeout`
remains as protection against *other* processes touching the same file.

The cost is that reads are serialised too. For a single-user local application
that is not a real constraint. If it ever becomes one, the answer is two pools
over the same file — one writer, and a read-only pool — which WAL supports
directly. That is a small, local change and is not being anticipated now.

The reasoning for each of these choices, including the alternatives rejected, is
recorded in [technical-decisions.md](technical-decisions.md).

## HTTP

One endpoint:

- `GET /healthz` — `200` with `{"status":"ok"}` when the process is alive and
  SQLite answers; `503` when the database probe fails. The database probe is
  bounded by its own timeout, because it may queue behind another query on the
  single pooled connection.

The server sets read-header, read, write and idle timeouts plus a header size
cap. `http.Server`'s zero value means "no timeout", which lets one stalled
client hold a connection indefinitely — never the right default for a server on
a real socket.

There is no request-logging middleware. A health endpoint is typically polled on
a short interval, and a line per successful probe would bury everything else.
The health handler logs only on failure. This will be revisited when there are
endpoints worth tracing.

### The dashboard *(M6)*

`GET /?period=7D|30D|90D|1Y|ALL` (desktop) and `GET /mobile?period=…` both make
**one** call to the analytics service and render what it returns. An absent or
unknown period means 30D. The period selector is a set of ordinary links that
htmx enhances with `hx-select`: it fetches the same full page and swaps only the
analytics block, so a deep link and a click are rendered by the same server code
and the page works with JavaScript off. There is no JSON endpoint; the chart is an
inline SVG whose integer geometry `internal/web/chart.go` computes from the series
(TD-070). No handler, template or script performs financial arithmetic; the
template functions `signedChange`, `signedRatio` and `ratio` only prefix a sign or
a `%` to strings the backend already formatted.

### The recap *(M6.5)*

`GET /recap?period=week|month&start=YYYY-MM-DD` (desktop) and `GET /mobile/recap`
make one call to `Analytics.Recap`. Without `start` the recap is of the last
closed period; anything malformed falls back to the default. The granularity
selector and the arrows are links enhanced with `hx-select`, exactly like the
dashboard's period selector. Directions and verdicts arrive decided; templates
only choose a class from them.

### Presentation *(design overhaul)*

The visual system is a skeuomorphic instrument with two finishes, Aluminio and
Grafito, all tokens in `web/static/css/app.css`; `mobile.css` aliases them
(TD-071). Headline figures sit in `.lcd` readouts whose value line never wraps;
`web/static/js/fit.js` shrinks a `data-fit` figure that would overflow its
container and computes nothing. Fonts (Archivo, DM Mono) are embedded under
`web/static/fonts`; no page requests anything from another host.

## Lifecycle

Startup, in order:

```text
configuration → logger → database (opened and verified) → migrations
              → listener → HTTP server → [exchange rate refresher]
```

**The exchange rate refresher starts after the server is listening, and never
before.** That ordering is a decision, not an accident of where the line sits.
Rates come from a government website that is regularly slow, occasionally
unreachable and entirely outside this application's control; fetching them during
startup — the way migrations run during startup — would turn somebody else's
downtime into this application's downtime, for a feature that only changes how a
total is *presented*.

So accounts, the ledger, balances, manual rates and every rate already stored are
available the moment the listener opens, and the refresher catches up in its own
time. **Migrations are a precondition; an external rate source is not.**

**Migrations run before anything listens.** Serving requests against a
half-migrated schema would be worse than not serving at all, so a migration
failure ends the process. The startup log makes the outcome explicit:

```text
database opened            sqlite.journal_mode=wal sqlite.foreign_keys=true …
migrations applied         count=3 version=3          (or: database schema already current)
http server listening      addr=…
```

Applying migrations on every start is safe: goose records applied versions, so a
current database is a no-op. A failure that leaves some migrations applied is
reported with which one failed and how many succeeded; SQLite has transactional
DDL, so the failed migration itself rolled back and the schema sits at the last
consistent version.

Rollbacks are never run automatically. `Up` is the only exported function in
`internal/migrations`.

Shutdown, on `SIGINT` or `SIGTERM`: stop the rate refresher and wait for it,
stop accepting connections, drain in-flight requests under a bounded timeout, wait
for the serve loop to return, snapshot, then close the database. The database
closes **last** — closing it while requests are still draining, or while a fetch
is holding the single connection, would break them.

Default signal handling is restored once shutdown begins, so a second `Ctrl+C`
can still kill a process wedged mid-shutdown.

## Transactions

Single-statement operations run directly on the pool; SQLite already wraps each
one in its own implicit transaction.

Operations that must read and then write atomically open an explicit transaction
and build their repositories over the same `*sql.Tx`. `account.Service.Create` is
the case that exists today:

```text
BeginTx
   ↓
currencies.Get(tx)      read the currency
   ↓
check IsActive          business rule, decided on the field
   ↓
accounts.Insert(tx)     write, same transaction
   ↓
Commit                  (any error on the way → explicit Rollback)
```

The foreign key guarantees the currency exists; only the transaction can
guarantee it was still active at the moment of the insert. Rollback is handled in
a `defer` so it covers every error path, and distinguishes the benign
`sql.ErrTxDone` that follows a successful commit from a real failure.
