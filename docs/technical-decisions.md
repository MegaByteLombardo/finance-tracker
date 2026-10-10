# Technical decisions

A lightweight record of architectural decisions: what was decided, why, and what
was rejected. Deliberately not a ceremonial ADR process — the point is that the
reasoning survives, not that a template is followed.

Decisions are numbered and never renumbered. A superseded decision keeps its
entry and gets a `Superseded by` line rather than being deleted.

**Status values:** `Accepted` · `Superseded` · `Revisit`

---

## TD-001 — SQLite for persistence

**Status:** Accepted · **Milestone:** M0

**Decision.** SQLite, as a single local file, is the database for this
application for as long as the data volume and access patterns allow.

**Rationale.** This is a single-user application running on one machine. SQLite
needs no server process, no network, no credentials and no operational
babysitting; the whole database is one file that can be copied. It is one of the
most thoroughly tested pieces of software in existence, and its transactional
guarantees are exactly what financial records need. At the scale of one person's
finances the ceiling is nowhere in sight.

**Alternatives considered.** PostgreSQL — a real server process, a second
container, connection management and backup tooling, all to serve one user on
one machine; the operational cost buys nothing here. A document store — the data
is relational (accounts, categories, transactions and rates all reference each
other), so a schemaless engine would push referential integrity into application
code, which is precisely where it gets forgotten.

---

## TD-002 — `modernc.org/sqlite` as the driver

**Status:** Accepted · **Milestone:** M0

**Decision.** Use `modernc.org/sqlite` (v1.57.0 at time of writing), the pure-Go
SQLite implementation, rather than a cgo binding.

**Rationale.** Being pure Go means `CGO_ENABLED=0`, which means a genuinely
static binary. That in turn makes the distroless runtime image of TD-011
possible, keeps cross-compilation trivial, and removes the need for a C
toolchain on the development machine — which matters, since the primary
development environment is Windows without gcc. It uses `database/sql`, so the
standard ecosystem still applies.

**Alternatives considered.** `mattn/go-sqlite3` — the long-standing default, but
it requires cgo, which forces a C toolchain into the build, a libc into the
runtime image, and complicates every cross-platform step. Its advantage is raw
performance, which is irrelevant at this volume. `zombiezen.com/go/sqlite` and
`crawshaw.io/sqlite` — good libraries, but they expose their own APIs rather
than `database/sql`, giving up compatibility with the wider ecosystem for
benefits this project would not use.

**Maintenance note.** `modernc.org/sqlite` is tightly coupled to a specific
`modernc.org/libc` version. Never bump `libc` independently; let
`go get modernc.org/sqlite@vX` resolve the pair.

---

## TD-003 — `net/http` from the standard library

**Status:** Accepted · **Milestone:** M0

**Decision.** Use `net/http` and `http.ServeMux` directly. No web framework, no
third-party router.

**Rationale.** Since Go 1.22 the standard `ServeMux` matches on method and path
wildcards (`GET /accounts/{id}`), which was historically the main reason to
reach for a router. What remains in a framework — middleware chaining, binding,
its own `Context` type — is not worth a dependency and a second set of idioms
for an application of this size.

**Alternatives considered.** Chi — the closest call, since its middleware
composition is genuinely pleasant; rejected because one endpoint does not justify
it and the routing gap it filled has closed. If middleware composition becomes
painful later, adopting it is a reversible afternoon's work. Gin, Fiber, Echo —
each brings its own context type, binding layer and ecosystem, pulling the
codebase away from standard Go for no capability this project needs.

---

## TD-004 — `log/slog` for logging

**Status:** Accepted · **Milestone:** M0

**Decision.** Structured logging via `log/slog`, with the handler chosen by
`APP_ENV`: readable text in development, JSON in production.

**Rationale.** It is in the standard library, it is structured, and it is fast
enough by an enormous margin at this volume. `net/http`'s own error output is
routed through it so nothing escapes as unstructured text on stderr.

**Alternatives considered.** Zap, Zerolog, Logrus — all predate `slog` and all
solve a problem the standard library now solves. Their performance advantage is
unmeasurable here.

---

## TD-005 — No ORM

**Status:** Accepted · **Milestone:** M0

**Decision.** Hand-written SQL against `database/sql`. No ORM, and no query
builder for now.

**Rationale.** SQLite has real features worth using directly — `STRICT` tables,
partial indexes, window functions, `VACUUM INTO` — and an ORM's abstraction tends
to hide exactly the ones that matter. Financial queries benefit from being read
as SQL rather than reconstructed from a fluent chain. Writing the SQL also keeps
the schema honest, since nothing generates it behind our backs.

**Alternatives considered.** GORM — heavy, magical, and its migration handling
would conflict with an explicit migration story. `sqlc` — genuinely appealing,
since it generates type-safe Go *from* hand-written SQL rather than replacing it;
not adopted now because there is no SQL yet, but it is a reasonable thing to
reconsider once the schema exists. Recorded here so the option is not forgotten.

---

## TD-006 — WAL journal mode

**Status:** Accepted · **Milestone:** M0

**Decision.** The database runs in WAL (write-ahead logging) journal mode.

**Rationale.** WAL lets readers proceed concurrently with a writer instead of
blocking on it, which matters as soon as the application serves pages while
writing. It is also generally faster for the write patterns this application will
have, and it is the mode SQLite recommends for applications rather than
command-line use.

**Alternatives considered.** The default `DELETE` rollback journal — simpler in
that it leaves no sidecar files, but readers and writers block each other, and
that becomes a real constraint the moment there is a UI. `MEMORY` and `OFF` —
trade durability for speed, which is the wrong trade for this data.

---

## TD-007 — `synchronous = FULL`

**Status:** Accepted · **Milestone:** M0

**Decision.** `synchronous = FULL`, not `NORMAL`.

**Rationale.** SQLite's documentation is explicit that in WAL mode a transaction
committed with `synchronous = NORMAL` **may roll back after a power loss or
system crash**. The database stays consistent, but an acknowledged commit can
vanish. For personal financial records that is not an acceptable failure mode:
the application would have told the user their transaction was saved, and it
would not be. `FULL` costs one additional fsync per commit, on a workload of a
few dozen writes a day. The performance difference is unmeasurable; the
durability difference is not.

**Alternatives considered.** `NORMAL` — the common recommendation for WAL, and
correct for workloads where losing the last few seconds of writes is acceptable.
It is not acceptable here. `EXTRA` — differs from `FULL` only by also syncing
the directory after unlinking a rollback journal, which applies to rollback
journal modes, not WAL; it would add cost without adding a guarantee. `OFF` — the
only setting that can actually corrupt the database.

---

## TD-008 — `MaxOpenConns(1)` and unlimited connection lifetimes

**Status:** Accepted · **Milestone:** M0

**Decision.** The pool is configured as `MaxOpenConns(1)`, `MaxIdleConns(1)`,
`ConnMaxLifetime(0)`, `ConnMaxIdleTime(0)`.

**Rationale.** A single connection serialises all database access from this
process, which makes `SQLITE_BUSY` structurally impossible from within the
application; `busy_timeout` then only has to defend against *other* processes
touching the file. The zero lifetimes are the point where pooling advice written
for network databases must not be copied: the usual five-minute `ConnMaxLifetime`
exists because of load balancers, idle timeouts and failover, none of which exist
for an in-process library. Recycling a SQLite connection only means reopening the
file and re-applying PRAGMAs — pure churn.

The cost is that reads are serialised too. For a single-user local application
that is not a real constraint.

**Alternatives considered.** A conventional multi-connection pool — would allow
concurrent reads, but introduces write contention and `SQLITE_BUSY` handling for
a benefit no current workload needs. Two pools over the same file, one writer
and one read-only — the correct answer *if* read serialisation ever becomes a
problem, and a small local change when that happens. Not built now, because
building it now would be anticipating a problem that does not exist.

**Revisit when.** Page rendering shows measurable queuing on reads.

---

## TD-009 — `_txlock` deliberately not configured

**Status:** Accepted · **Milestone:** M0

**Decision.** The DSN does not set `_txlock`. SQLite's default `DEFERRED`
transaction behaviour stands.

**Rationale.** `_txlock=immediate` makes a transaction take the write lock at
`BEGIN` rather than upgrading mid-transaction, which prevents a class of deadlock
between concurrent writers. With `MaxOpenConns(1)` there are no concurrent
writers inside this process, so it would buy nothing today. Whether a specific
operation needs `BEGIN IMMEDIATE` is a decision better made against real write
paths than guessed at in advance.

A test asserts that `_txlock` is absent from the DSN, so reintroducing it is a
deliberate act rather than an accident.

**Alternatives considered.** Setting `immediate` now as a "safe default" —
rejected as configuring against a hypothetical. It becomes relevant alongside
TD-008's two-pool option, and can be decided then.

**Revisit when.** The pool stops being single-connection, or real write paths
show lock-upgrade contention.

---

## TD-010 — `journal_mode` set outside the DSN and verified

**Status:** Accepted · **Milestone:** M0

**Decision.** `foreign_keys`, `busy_timeout` and `synchronous` are carried in the
DSN so the driver applies them to every connection. `journal_mode` is not: it is
applied once at startup and its **returned value is checked**.

**Rationale.** Two distinct problems.

The connection-scoped PRAGMAs belong in the DSN because `database/sql` is a
*pool*. Running `db.Exec("PRAGMA foreign_keys = ON")` once after `sql.Open`
configures one arbitrary physical connection and silently leaves every other
connection at SQLite's default of `OFF`. Nothing errors; foreign keys just stop
being enforced on some queries. The DSN is the only formulation under which the
guarantee holds for the whole pool.

`journal_mode` is different on both counts. It is a persistent property of the
database *file*, not of a connection, so re-issuing it per connection is
redundant work that can return `SQLITE_BUSY` under concurrency. And critically,
`PRAGMA journal_mode = WAL` does not fail when it cannot be applied — it returns
the mode the database actually ended up in. Assuming success would mean silently
running without WAL, so the returned value is compared against `"wal"` and
startup fails if it does not match.

The driver's validated shorthand DSN keys are used rather than the general
pass-through form, because the shorthand keys are all validated before any are
applied: a malformed DSN fails cleanly instead of half-configuring a connection.

**Alternatives considered.** A single `Exec` after `sql.Open` for everything —
the common approach, and silently wrong for a pool. A connection hook applying
PRAGMAs per connection in Go — equivalent in guarantees and available as a
fallback, but it requires global registration; unnecessary once the DSN form was
verified to work on both Windows and Linux. Putting `journal_mode` in the DSN
too — would work, but re-issues a persistent setting on every connection and,
more importantly, gives no place to check the result.

---

## TD-011 — Distroless non-root runtime image

**Status:** Accepted · **Milestone:** M0

**Decision.** Multi-stage build: `golang:1.27-trixie` to compile with
`CGO_ENABLED=0`, then `gcr.io/distroless/static-debian13:nonroot` as the runtime,
running as uid 65532.

**Rationale.** The static binary from TD-002 needs no libc, so the runtime image
needs to contain almost nothing: the result is around 5.6 MB with no shell, no
package manager and no Go toolchain. Smaller attack surface, and the non-root
user comes built in rather than being assembled with `adduser`.

One non-obvious detail makes this work: a Docker named volume is initialised from
whatever the image already has at that path, **ownership included**. Without an
empty `/data` in the image owned by uid 65532, the volume would be created
root-owned and every write from the non-root process would fail. The image
therefore ships `/data` with the right ownership. This was verified against a
real container rather than assumed.

**Alternatives considered.** Alpine — brings musl, busybox and apk that are not
used; its one advantage is a shell for debugging, and distroless offers a
`:debug-nonroot` tag for that. `scratch` — no `/etc/passwd`, so no usable
non-root user, and no tzdata, which will matter once there is a financial
calendar. Running as root — never, for a process that listens on a socket.

**Known limitation.** No `HEALTHCHECK`, because distroless has no shell or
`curl`. The clean fix is a self-check flag on the binary; deferred until
something is orchestrating restarts.

---

## TD-012 — Migrations deferred to M1

**Status:** Superseded by TD-014 · **Milestone:** M0

**Decision.** M0 ships no migration machinery, no `migrations/` directory and no
migration dependency in `go.mod`. All of it arrives in M1 together with the first
real domain table.

**Rationale.** Wiring a migration tool in M0 was not actually possible without
inventing a fake migration, for two verified reasons. `//go:embed migrations/*.sql`
is a **compile error** when nothing matches the pattern, and embedding a directory
with no embeddable files fails the same way — a `.gitkeep` does not help. And
`goose.NewProvider` returns `ErrNoMigrations` when the filesystem is empty;
passing a nil filesystem does not change this, since it is substituted with an
empty one.

So the choice was between a bootstrap migration that exists only to satisfy the
tooling, and deferring. Deferring won. A migration inventing a table nobody needs
is noise in the schema history forever, and `journal_mode = WAL` — the one
plausible candidate for a "real" bootstrap migration — is operational
configuration of SQLite, not schema evolution. It belongs in connection setup,
where it is applied and verified. Leaving dead, un-exercised migration wiring in
`main.go` would have been the worse half of both options.

**Alternatives considered.** A `00001_enable_wal.sql` bootstrap migration —
rejected: WAL is not schema. A dummy table such as `test_table` — rejected
outright. Building the migration runner in M0 and testing it against an
in-test filesystem, with the real directory arriving in M1 — technically clean,
but leaves code the startup path never exercises.

**Carried into M1.** The tool (`github.com/pressly/goose/v3` is the current
candidate — v3.27.3, supports `DialectSQLite3` and `embed.FS`), the file
versioning scheme, when migrations run during startup, how partial failures are
handled, and how `status` and `down` are exposed. None of these are settled here;
they are decided in M1 against a real migration. What *is* settled: we will not
write our own migration engine.

---

## TD-013 — Money is never a float

**Status:** Accepted · **Milestone:** M0

**Decision.** Monetary amounts and exchange rates are never represented as
`float32`, `float64` or SQLite `REAL`. Amounts are integer minor units
(`INTEGER` / `int64`) stored alongside their currency. Exchange rates use an
exact representation — integer numerator and denominator, or scaled integers with
a recorded scale.

**Rationale.** Binary floating point cannot represent ordinary decimal amounts
exactly. The error per operation is tiny and it compounds silently across
summation and conversion, which is what a finance application does constantly. A
balance that is off by a cent is a balance that cannot be reconciled against a
statement, and the failure gives no signal — it just quietly disagrees.

Storing minor units as integers makes the arithmetic exact by construction. An
amount is never separated from its currency, because an amount without one is
meaningless.

**Alternatives considered.** `float64` with rounding at display time — the
standard mistake; rounding the output does not undo error accumulated in the
input. A decimal library — a legitimate option and possibly worth it later for
ergonomics, but it does not change the storage rule and would be a dependency
added before there is anything to weigh it against. SQLite `REAL` — same problem
as `float64`, one layer down, and `STRICT` tables make the wrong choice harder to
make by accident.

**Note.** No `Money` type exists yet. The invariant is recorded now so that the
first code to handle an amount is written correctly rather than retrofitted. See
[data-model.md](data-model.md).

---

## TD-014 — goose for migrations, via the Provider API

**Status:** Accepted · **Milestone:** M1 · **Supersedes:** TD-012

**Decision.** Use `github.com/pressly/goose/v3` (v3.27.3) as a library, through
the Provider API. SQL migrations, embedded with `embed.FS`, applied automatically
at startup before anything listens. Rollbacks are never run automatically, and no
CLI or migration flags are added.

**Rationale.** Migrations are a solved problem and writing our own engine would
be a poor use of effort for a worse result. goose supports SQLite, takes an
`fs.FS` so migrations compile into the binary, and lets a deployment stay a single
executable — no goose CLI in the runtime image, no loose `.sql` files to keep in
sync with it.

The Provider API rather than the global `SetBaseFS`/`SetDialect`/`Up` functions:
the global form keeps package-level state and reports errors poorly, while the
Provider touches no globals and returns structured results, including
`*PartialError` with what was applied and what failed.

Two things had to be verified rather than assumed:

- **`Provider.Close()` closes the `*sql.DB` it was given** —
  `func (p *Provider) Close() error { return p.db.Close() }`. That database
  belongs to `cmd/web` for the life of the process, so `Close` is never called;
  the provider is a transient object with nothing else to release. A reflexive
  `defer provider.Close()` would leave the HTTP server without a database the
  moment migrations finished, which is why the omission is commented in the code.
- **A single-connection pool does not deadlock.** goose has a guard that errors
  when `MaxOpenConnections == 1`, but it applies only to *Go* migrations running
  without a transaction. SQL migrations execute on the already-held `*sql.Conn`
  and never request a second connection, so TD-008's `MaxOpenConns(1)` stands
  unchanged.

**Measured, not assumed.** goose's `go.mod` requires the ClickHouse, MySQL, pgx,
MSSQL, Vertica, YDB and libsql drivers *directly*, which was a real concern. None
of them reach the binary: `go list -deps` is clean for both
`./internal/migrations` and `./cmd/web`, and the runtime image grew from 5.58 MB
to 5.80 MB — 222 KB for goose plus the embedded SQL. `go.sum` did grow
considerably; that is the accepted cost.

**Alternatives considered.** Writing a migration runner — rejected outright;
it is a known problem with known edge cases and no reason to re-solve it.
`golang-migrate` — comparable, no advantage here, and a heavier module graph.
`dbmate` — CLI-oriented, which conflicts with embedding migrations in the binary.

**Deferred deliberately.** No `status` or `down` interface was added. The startup
logs say what happened, and `goose_db_version` is one query away in the sqlite3
CLI. Adding flags now would be infrastructure with no consumer.

---

## TD-015 — Migrations live in `internal/migrations`

**Status:** Accepted · **Milestone:** M1

**Decision.** The migration package and its `.sql` files live together in
`internal/migrations/`, versioned sequentially (`00001_`, `00002_`, `00003_`).

**Rationale.** goose globs `*.sql` at the **root** of the `fs.FS` it is handed
(verified in `provider_collect.go`), so the Go file carrying `//go:embed *.sql`
must sit beside the SQL — otherwise an `fs.Sub` is needed to strip a directory
prefix. That settles co-location; the remaining question was where.

`internal/` because it is a Go package of this application, and all application
code lives there; because `internal/` is compiler-enforced, and a migration
runner is not public API; and because the repository root stays a short list of
top-level concerns.

Sequential versions because there is one author and no concurrent branches. The
timestamp scheme exists to resolve collisions between developers, which cannot
happen here, and sequential numbers read in order at a glance.

**Alternatives considered.** A top-level `migrations/` directory — more
discoverable, and the convention someone coming from the goose CLI would expect.
That argument lost weight because the CLI is not used at all, and because
[data-model.md](data-model.md) and [architecture.md](architecture.md) point at the
directory explicitly. If the CLI is ever wanted it is one flag:
`goose -dir ./internal/migrations`.

---

## TD-016 — Timestamps stored as RFC 3339 TEXT

**Status:** Accepted · **Milestone:** M1

**Decision.** Every timestamp is `TEXT`: RFC 3339, UTC, with exactly three
fractional digits — `2026-08-29T22:37:55.123Z`. Go writes it with the layout
`2006-01-02T15:04:05.000Z`; SQL defaults use
`strftime('%Y-%m-%dT%H:%M:%fZ','now')`; a `CHECK … GLOB` enforces the shape.
This is the project-wide convention, not just M1's.

**Rationale.** Two reasons decide it. The database is a single file its owner
will open with the sqlite3 CLI to look at their own finances, and
`2026-08-29T22:37:55.123Z` reads where `1787012275` does not. And business dates
arriving in M2 are calendar dates, so ISO-8601 keeps system timestamps and
business dates on one convention, where `date()`, `strftime()`, comparisons and
`BETWEEN` behave identically over both; integers would mean two conventions.

Ordering is not a trade-off here: fixed-width RFC 3339 in UTC sorts
lexicographically exactly as it sorts chronologically.

**The trap this avoids.** `time.RFC3339Nano` strips trailing zeros from the
fractional part, so `…:05.1Z` sorts *after* `…:05.09Z` as text. The layout is
pinned to `.000`, which always emits three digits, and the `CHECK` enforces that
in the database so the ordering invariant survives a hand-written INSERT.

Go supplies the value explicitly; the SQL `DEFAULT` is a safety net for manual
inserts. `database.Now()` truncates to milliseconds so a value held in memory
compares equal to the one read back. Reads scan into `string` and parse
explicitly — the driver's `_texttotime` conversion is left off, because implicit
conversion would be hidden behaviour over a convention we want visible, and it
would change the DSN that M0 fixed and tested.

**Alternatives considered.** Unix `INTEGER` — compact, trivially sortable,
inherently UTC, and genuinely defensible; rejected on readability and on the
split it would create with calendar dates. SQLite `DATETIME` — not a real type,
and not permitted in a STRICT table at all.

---

## TD-017 — Currency keyed on its ISO 4217 code

**Status:** Accepted · **Milestone:** M1

**Decision.** `currencies.code TEXT PRIMARY KEY`, holding the ISO 4217 code. No
surrogate integer id.

**Rationale.** The code is stable, globally unique, fixed-width and already the
value every other table references. A surrogate key would add a join to read
something the foreign key column already contains, plus a `UNIQUE(code)` that
would be the real key anyway.

In a STRICT table, "columns that are part of the PRIMARY KEY are implicitly NOT
NULL", so SQLite's long-standing quirk of allowing NULLs in non-INTEGER primary
keys does not apply. `NOT NULL` is still written out, because a reader should not
have to know that.

**Alternatives considered.** Surrogate integer plus `UNIQUE(code)` — the usual
reflex; it buys the ability to rename a code, which is precisely what we want to
prevent. `WITHOUT ROWID` — technically reasonable for a small table with a text
key, but worth nothing measurable at three rows and adds a constraint to remember.

---

## TD-018 — `minor_unit` bounded to 0–4 and immutable

**Status:** Accepted · **Milestone:** M1

**Decision.** `CHECK (minor_unit BETWEEN 0 AND 4)`, and no operation may change
it after creation.

**Rationale.** The range is not arbitrary: it is the real span of ISO 4217
exponents — 0 (JPY, KRW, CLP), 2 (most), 3 (BHD, KWD, JOD), 4 (CLF, UYW).
Nothing in the standard exceeds 4, so the bound admits every real currency and
nothing meaningless.

Immutability matters more than the range. Changing a currency's minor unit
reinterprets every amount already stored in it — 1.00 becomes 0.100 — silently
and irreversibly. It is the worst class of failure for financial data. The
guarantee is structural: `UpdateMetadataInput` has no such field, and the
`UPDATE` statement does not name the column.

**Alternatives considered.** A wider bound for crypto (8 for BTC, 18 for ETH) —
excluded deliberately. Eighteen decimals would overflow `int64` for trivial
amounts, so supporting crypto is a numeric representation decision, not a wider
`CHECK`; leaving it out means it can only ever be adopted on purpose. No bound at
all — would accept values with no meaning. A `BEFORE UPDATE` trigger to enforce
immutability — see TD-022.

---

## TD-019 — Account types classify the container, not the purpose

**Status:** Accepted · **Milestone:** M1

**Decision.** `account_type` is one of `BANK`, `CASH`, `WALLET`, `OTHER`. No
`CHECKING`, no `SAVINGS`, and no `purpose`, `is_savings` or `account_subtype`
column.

**Rationale.** `account_type` answers one question: *what kind of container is
this money in?* "Savings" answers a different one — *what is this money for?* —
and the two are orthogonal, because one can save in a bank account, in cash or in
a wallet.

Putting both on one axis forces a false choice: a cash reserve set aside as
savings would have to be declared `CASH` **or** `SAVINGS`, discarding one of two
facts that are simultaneously true. An enum that makes a user drop true
information is malformed, and no number of extra values repairs it. `BANK`
absorbs current and savings accounts without loss; the institution is recorded
separately.

The purpose axis is not designed now because nothing consumes it. When a real
use case appears it arrives as its own decision, better informed by seeing how
accounts are actually used.

**Alternatives considered.** Keeping `CHECKING`/`SAVINGS` — the initial proposal;
rejected for the reason above. A second `purpose` column now — an anticipated
abstraction with no reader. A lookup table instead of a `CHECK` — four fixed
values the code knows as constants; a table would add a join to read something
already legible.

---

## TD-020 — One account, one currency

**Status:** Accepted · **Milestone:** M1

**Decision.** Every account is denominated in exactly one currency, and that
currency never changes. A bank offering USD and VES under one contract is
modelled as two accounts.

**Rationale.** The modelling cost is cosmetic — two rows where the bank shows one
tab, and a UI can group them by institution. What it buys is structural:

- **The ledger becomes single-currency by construction.** Every movement inherits
  its account's currency; no per-movement currency column, and no validation that
  the two agree.
- **Balances are a plain `SUM`.** With multi-currency accounts, "the balance of
  this account" would not be a number.
- **Cross-currency movement becomes explicit.** Moving USD to VES is a transfer
  between two accounts with a rate attached — which is what it economically is —
  rather than an invisible change of composition inside one account.
- **Reconciliation works.** The statement a bank issues per denomination maps to
  exactly one account.

Immutability follows from the same logic: once movements exist, re-denominating
an account would reinterpret its entire history. The remedy for a mistake is to
archive and recreate, not to rewrite the past. It is enforced by there being no
use case, no input field and no `UPDATE` statement that touches it, and by
`ON UPDATE RESTRICT` on the foreign key.

**Alternatives considered.** Multi-currency accounts mirroring how banks present
them — closer to the source documents, at the cost of every consequence above.

---

## TD-021 — Concrete repositories, plus one executor interface

**Status:** Accepted · **Milestone:** M1

**Decision.** Repositories are concrete structs. There are no repository
interfaces. There is exactly one new interface, `database.DBTX`, carrying
`ExecContext`, `QueryContext` and `QueryRowContext`.

**Rationale.** SQLite is the only backend and tests run against real SQLite, so a
repository interface would have one implementation and no test double — pure
indirection. Go interfaces are consumer-defined, so if a second implementation
ever appears, extracting one then is a non-breaking refactor.

`DBTX` is a different kind of thing and earns its place on a problem that exists
today. Both `*sql.DB` and `*sql.Tx` satisfy it, which is what lets a repository be
built over either. `account.Service.Create` must check that a currency is active
and insert the account atomically; without `DBTX` the repository would be nailed
to `*sql.DB` and could not join the transaction, leaving the invariant to luck.
It is also the standard shape for this in Go — what `sqlc` generates — and is a
standard-library interface in everything but name.

**Alternatives considered.** Repository interfaces with generated mocks —
architecture for its own sake; real SQLite tests are both simpler and stronger. A
single `INSERT … SELECT … WHERE EXISTS (…)` making the check and insert atomic in
one statement, avoiding both the transaction and the interface — it works, but it
needs a second query on the error path to distinguish "missing" from "inactive",
and the SQL becomes cleverer than it is readable. A generic base repository —
explicitly unwanted.

---

## TD-022 — Immutability enforced in code, not by triggers

**Status:** Accepted · **Milestone:** M1

**Decision.** `currencies.code`, `currencies.minor_unit` and
`accounts.currency_code` are immutable by construction: no use case accepts them,
no input struct carries them, and no `UPDATE` statement names them. No database
trigger enforces this.

**Rationale.** `CHECK` cannot compare `OLD` with `NEW`, so a `BEFORE UPDATE`
trigger is the only mechanism SQLite offers. It was rejected on two grounds: it
defends only against someone writing raw SQL, who is the same person able to drop
the trigger; and it introduces hidden control flow into the database, which this
project avoids deliberately.

**The accepted risk is real and worth stating plainly.** A manual
`UPDATE currencies SET minor_unit = …` would silently reinterpret every amount
stored in that currency. This is recorded in [data-model.md](data-model.md) under
invariants not enforced by the database, so it is a known rule rather than an
assumption.

Related: "an account's currency must be active at creation" is deliberately *not*
a database constraint either, but for a different reason — a currency may
legitimately be archived after accounts already use it, so the rule applies only
at creation time. It is enforced in the service, inside the same transaction as
the insert.

**Alternatives considered.** Triggers — as above. A composite foreign key against
a generated column to make "active currency" referential — cleverer than clear,
and it would wrongly forbid archiving a currency that accounts already use.

---

## TD-023 — SQLite constraint errors are not classified by code

**Status:** Accepted · **Milestone:** M1

**Decision.** Error classification never inspects message text. Where a SQLite
error must be recognised, `errors.As` is used against `*sqlite.Error`. But
`ErrConflict` is **not** derived from the driver's error at all: it is determined
by an existence check inside the same transaction as the insert.

**Rationale.** `(*sqlite.Error).Code()` returns the **primary** SQLite result
code, not the extended one. A duplicate primary key, a foreign key violation and
a failed `CHECK` therefore all arrive as `SQLITE_CONSTRAINT` (19) and cannot be
told apart. Any scheme that maps "constraint error" to "conflict" would
mis-report a failed `CHECK` as a duplicate, and distinguishing them would mean
parsing the message — exactly what we refuse to do.

Determining the conflict instead is both correct and simpler: the service reads
before it writes, in one transaction, so the answer is unambiguous. The primary
key remains as the database's backstop, and if it ever fires it means something
bypassed the check — a bug to surface, not a conflict to report as one.

**Note for later.** This is easy to reintroduce by accident: the next person to
map a SQLite error to a domain error will look at `Code()` and reasonably assume
it is enough. It is not.

---

## TD-024 — Account subtype is a dependent column, not more account types

**Status:** Accepted · **Milestone:** M2

**Decision.** `accounts` gains `account_subtype TEXT NULL`, constrained to
`CHECKING` / `SAVINGS` and required exactly when `account_type = 'BANK'`.
`account_type` keeps its four values.

**Rationale.** TD-019 settled that `account_type` classifies the *container*.
Current-versus-savings is a different axis, and one that only exists inside a
bank: adding `CHECKING` and `SAVINGS` to `account_type` would have made "a
savings account at Mercantil" and "cash" siblings in one enumeration, and left
`CASH`, `WALLET` and `OTHER` with no way to say which they are. A second,
dependent column says exactly what is true and nothing more.

The dependency is enforced in Go *and* by a table `CHECK`, in both directions: a
bank account without a subtype and a cash account with one are both refused, on
insert and on update. Neither is silently corrected — dropping a subtype sent for
cash would hide a caller's bug, and defaulting a missing one for a bank account
would invent a fact about someone's money.

**Applied by rebuilding the table.** SQLite cannot add a constraint to an
existing table, so migration `00004` uses the documented 12-step rebuild rather
than `ALTER TABLE ADD COLUMN`. Two reasons: copying the rows through the new
definition validates the data that is already there, which `ADD COLUMN` would
not do; and `ALTER TABLE DROP COLUMN` refuses to drop a column named in a
`CHECK`, so the `Down` would have needed a rebuild regardless.

The rebuild is ordered **before** `account_movements` exists. Nothing references
`accounts` at that point, so dropping and renaming it is safe with foreign keys
on — no children to orphan, no other table's `REFERENCES` clause to rewrite. Had
this migration come later it would have needed `PRAGMA defer_foreign_keys`,
because `PRAGMA foreign_keys` is a no-op inside the transaction goose runs each
migration in. The ordering of the four M2 migrations is deliberate for this
reason.

**Alternatives considered.** More `account_type` values — rejected above, and
already rejected once in TD-019. A `banks` table — `institution` is free text and
there is no query a normalised table would serve. `ALTER TABLE ADD COLUMN` with a
`CHECK` — works, but leaves existing rows unvalidated and the column
un-droppable.

---

## TD-025 — A `CHECK` constraint must never be able to evaluate to NULL

**Status:** Accepted · **Milestone:** M2

**Decision.** Every `CHECK` in this schema is written so that its expression is
always `TRUE` or `FALSE`, never `NULL`. Where a nullable column is involved, the
condition uses `IS NULL` / `IS NOT NULL`, or guards the comparison with one.

**Rationale.** SQLite rejects a row only when a `CHECK` evaluates to **false**.
`NULL` passes. This is easy to write by accident and impossible to see by
reading:

```sql
-- WRONG. A BANK row with a NULL subtype is ACCEPTED.
CHECK ((account_type =  'BANK' AND account_subtype IN ('CHECKING','SAVINGS'))
    OR (account_type <> 'BANK' AND account_subtype IS NULL))
```

With `account_type = 'BANK'` and `account_subtype = NULL`, the first branch is
`TRUE AND NULL` which is `NULL`, the second is `FALSE`, and `NULL OR FALSE` is
`NULL` — so the row goes in. The constraint reads as though it forbids exactly
the case it permits.

The fix is an explicit presence test before the membership test:

```sql
CHECK ((account_type =  'BANK' AND account_subtype IS NOT NULL
                                AND account_subtype IN ('CHECKING','SAVINGS'))
    OR (account_type <> 'BANK' AND account_subtype IS NULL))
```

The same trap appears with `=` in the date check: `date(x) = x` is `NULL` when
`date(x)` is `NULL`, so `date(x) IS x` is used instead — `IS` never returns
`NULL`.

**This was found by testing, not by review.** The first version of the subtype
constraint was written, looked correct, and accepted a bank account with no
subtype when run against real SQLite. Every multi-column `CHECK` in M2 is
therefore covered by a test that asserts every combination, including all the
NULL ones.

---

## TD-026 — The ledger separates the event from its effect on an account

**Status:** Accepted · **Milestone:** M2

**Decision.** Two tables. `transactions` holds the financial event — type,
category, payment method, description, effective date — and carries no amount and
no account. `account_movements` holds the effect on one account: a signed
`amount_minor`. In M2 every transaction has exactly one movement, and
`transaction_id` is deliberately **not** unique.

**Rationale.** A transfer between two of the user's own accounts is one event
with two effects. It is out of scope for M2, but a flat
`transactions(account_id, amount)` table would have to be migrated away from when
it arrives — and migrating a ledger, in place, with history in it, is exactly the
thing worth spending a second table to avoid now.

The absence of `UNIQUE(transaction_id)` is the load-bearing part. It would hold
today and be true for the wrong reason. A test asserts that no unique index
exists on that table, so it cannot be added by someone tidying up.

**Alternatives considered.** One flat table, migrated later — the migration is
the cost being avoided. Full double-entry bookkeeping with a chart of accounts —
correct for an accounting system, and far more than one person tracking their own
money needs; the movement table is the part of it that pays for itself here.

---

## TD-027 — Money crosses the boundary as a decimal string

**Status:** Accepted · **Milestone:** M2

**Decision.** Monetary input arrives as a decimal string (`"1000.50"`) and is
converted to `int64` minor units by `internal/money`, using the currency's
`minor_unit` as the scale. Output carries both the `int64` and a pre-formatted
string. No float is involved at any point, and no decimal library is added.

**Rationale.** TD-013 forbids float for storage; this is how that survives the
edges. A form posts text, so *something* has to parse it, and the two obvious
shortcuts are both wrong: `strconv.ParseFloat` reintroduces exactly the
representation error TD-013 exists to prevent, and multiplying a float by 100
rounds unpredictably. Walking the digits and building an integer is a page of
explicit code with no dependency and no rounding at all.

**More decimals than the currency has is an error, not a rounding.** `"12.345"`
in USD is rejected. Silently storing `12.34` or `12.35` would discard money the
user typed, and which one it discarded would depend on a rule they never agreed
to.

**The sign belongs to the operation, not the input.** `RecordExpense` takes
`"25.99"` and stores `-2599`; a leading `-` there is refused. This is what makes
"an expense that increases a balance" unrepresentable rather than merely invalid.

Formatting is the same function in reverse, and `Parse(Format(v, u), u) == v` is
a tested property across every scale and both int64 extremes. It emits no
thousands separators and no currency symbol: both are presentation decisions that
depend on locale, and the currency travels next to every amount so the caller has
what it needs.

**Why its own package.** `money` is numeric representation; `currency` is a
catalogue of rows. Keeping them apart leaves `currency` a pure reference-data
package, and gives the exact-rate arithmetic of the FX milestone somewhere
obvious to land. It is two functions and a sentinel error — there is no `Money`
struct, because an amount is never separated from its currency in this codebase
and a wrapper would carry no invariant the surrounding code does not already
hold.

**Alternatives considered.** `shopspring/decimal` — a dependency for arbitrary
precision this application does not need; integer minor units are both exact and
smaller. Accepting `int64` minor units directly from the handler — that pushes
parsing into the web layer, which is precisely where it must not live.

---

## TD-028 — Calendar dates are stored as TEXT, distinct from timestamps

**Status:** Accepted · **Milestone:** M2

**Decision.** `transactions.effective_date` is `TEXT` in `YYYY-MM-DD`. It is a
different convention from `created_at` / `updated_at` (TD-016), and
`internal/database` owns both: `DateLayout`, `ParseDate`, `FormatDate`, `Today`.

**Rationale.** "The purchase happened on 2026-09-03" is true in every timezone. A
timestamp is not: it names an instant that different zones render as different
days, so storing a business date as one would make the date of a transaction
depend on where it is read. They are different facts and they get different
columns — a purchase made on the 3rd and entered on the 5th has an effective date
of the 3rd and a `created_at` of the 5th, and listings order by the former.

Validity is enforced twice, and the SQL half needs both clauses:

```sql
CHECK (effective_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'
       AND date(effective_date) IS effective_date)
```

The `GLOB` fixes the shape and rejects `2026-9-5`. `date(x) IS x` rejects dates
that are well-formed but do not exist: `date('2026-02-31')` normalises to
`'2026-03-03'`, which is not equal to the input. `IS` rather than `=` for the
reason in TD-025. In Go, `time.Parse` with the same layout enforces both rules.

Whether SQLite would accept a date function inside a `CHECK` at all was verified
against the real driver before the migration was written, rather than argued from
the documentation.

Future dates are allowed. Scheduled and recurring transactions are a later
milestone, but nothing about a date in the future makes it invalid to record.

---

## TD-029 — Opening balances are ledger facts, and a zero one records nothing

**Status:** Accepted · **Milestone:** M2

**Decision.** A starting balance is an `OPENING_BALANCE` transaction with one
movement, not a column on `accounts`. An amount of zero records **no transaction
and no movement**, and `RecordOpeningBalance` returns `(nil, nil)`.
`account_movements.amount_minor` is therefore `CHECK (amount_minor <> 0)`.
Negative opening balances are allowed.

**Rationale.** `accounts.opening_balance` would be a second source of truth that
can disagree with the ledger it claims to summarise, and when it does, neither
figure can be trusted. As a transaction it is dated, listed, and summed by the
same rule as everything else: one balance, one derivation.

It is deliberately **not** income. Recording a starting balance as income would
place it in every "what did I earn" figure forever, which is why it is its own
transaction type, and why it has no category — the schema refuses one.

**Zero records nothing** because the sum over no rows is already zero. A
zero-amount row asserts nothing, and would appear in every listing as an event
that did not happen. Accepting zero rather than rejecting it matters: leaving the
balance field empty is how a user says "this account starts at zero", and that
must not be an error.

A consequence worth stating: because nothing is recorded, a zero opening balance
does not require an effective date either — there is no transaction for the date
to belong to. This is the order validation runs in, and it is tested. The account
is still checked, so a zero amount is not a way to skip everything else.

**A negative opening balance** is an account that starts overdrawn, which is a
real thing to record. No arbitrary constraint forbids it.

---

## TD-030 — `internal/application` holds cross-domain workflows

**Status:** Accepted · **Milestone:** M2

**Decision.** A third package holds workflows that span two domains and must be
atomic. It contains exactly one today: `OpenAccount`, which creates an account
and records its opening balance inside one `sql.Tx`.

**Rationale.** It is forced, not chosen. `ledger` already imports `account`, so
the workflow cannot live in `account` without a cycle, and it does not belong in
`ledger` because creating accounts is not the ledger's job.

The workflow is real today. Without it a handler writes:

```go
acct, err := accounts.Create(ctx, in)   // committed
if err == nil {
    ledgerSvc.RecordOpeningBalance(...) // may fail, independently
}
```

which leaves an account the user believes has a balance, and no error they ever
see. The compensating delete that "fixes" it can fail too.

Mechanically, each domain exposes a transaction-scoped form of the use case —
`account.CreateTx`, `ledger.RecordOpeningBalanceTx` — taking a `database.DBTX`
instead of opening its own transaction. The service methods become thin wrappers
that begin, delegate and commit. This is the mechanism TD-021 introduced `DBTX`
for, applied one layer up.

**It is not a general application layer**, and pass-through wrappers do not
belong in it. A workflow earns a place here by coordinating two domains inside
one transaction; everything else calls its domain service directly.

**Errors pass through untranslated.** `OpenAccount` declares no sentinels of its
own: its errors match `account.*` and `ledger.*` with `errors.Is`. The
encapsulation rule this bends — a caller should not have to import a neighbouring
domain to interpret a result — exists to hide *internal* consultations. Here the
caller named both domains in its input and receives values from both in its
result, so this is the composition it asked for rather than a leak.

---

## TD-031 — Category names are unique per type, case-insensitively

**Status:** Accepted · **Milestone:** M2

**Decision.** `CREATE UNIQUE INDEX idx_categories_type_name ON
categories(category_type, name COLLATE NOCASE)`. Conflicts are determined by a
query inside the same transaction as the insert, never inferred from the driver's
error (TD-023). Categories are not seeded.

**Rationale.** Two `EXPENSE` categories called "Comida" are a data-entry mistake
rather than a distinction: nothing downstream could tell the user which is which,
and their totals would be split for no reason. The type is part of the key
because the same name across the two is legitimate — "Intereses" is both earned
and paid.

**The known limitation, stated rather than hidden:** SQLite's `NOCASE` folds
ASCII only, so "Café" and "CAFÉ" are treated as different names. Fixing it
properly would mean a normalised shadow column or an ICU build, neither of which
is worth it for a table with tens of rows in it.

**No seed.** A default set of twenty categories would be rows guessed on
someone's behalf, most of which they would archive. Nor is there a special
category for opening balances: an opening balance is uncategorised by
construction, and the schema refuses a category on one.

**Immutable type.** `category_type` cannot change, for the same reason
`currencies.minor_unit` and `accounts.currency_code` cannot: flipping a category
from `INCOME` to `EXPENSE` would reinterpret every transaction already filed
under it. As with those, it is enforced by the absence of any code path rather
than by a trigger (TD-022).

---

## TD-032 — Backups live on the host, never in the volume they protect

**Status:** Accepted · **Milestone:** M3

**Decision.** The operational database stays in the `finance_data` named volume.
Snapshots go to a **bind mount** from the host, `./backups:/backups`, declared in
long syntax with `create_host_path: false`.

**Rationale.** M2 shipped a working ledger whose only copy lived in that volume,
and the volume was destroyed. The investigation was unambiguous: persistence
worked correctly — the volume was recreated at the same second as the container,
two seconds after the image was built — and there was simply nothing else to fall
back on.

A second *volume* would not have helped. `docker compose down -v` destroys every
volume in the project, and `docker volume prune` removes any volume whose
container is gone. Backups have to leave Docker's lifecycle entirely to survive
the command that makes them necessary.

**Verified, not assumed.** Two things were tested against real containers before
this was written:

- A bind mount on Windows is presented as `drwxrwxrwx`, so the non-root runtime
  user (uid 65532) can write to it.
- A **fresh named volume is `drwxr-xr-x` and root-owned**, and uid 65532 cannot
  write to it. `/data` works only because the image ships that directory owned by
  65532 and a volume inherits the image's ownership. A backups volume would have
  needed the same trick — and would still have been destroyed by `down -v`.

**The caveat, stated rather than discovered.** On a Linux host a bind mount keeps
the host's ownership, so `./backups` may need `chown 65532` there. The behaviour
that makes this effortless on Windows is Docker Desktop's, not Docker's.

`create_host_path: false` is deliberate: if `./backups` is missing, the container
fails to start instead of Docker silently creating it as root and leaving every
write to fail. `backups/.gitkeep` is tracked so the directory exists in any
checkout.

**Alternatives considered.** A second named volume — destroyed by the same
command. Writing snapshots inside `/data` — destroyed with the database. An
external object store — a network dependency for a local-first application.

---

## TD-033 — Snapshots are taken with VACUUM INTO and validated before they count

**Status:** Accepted · **Milestone:** M3

**Decision.** `VACUUM INTO` against the live database. The result is written under
a `.partial` name, validated with `PRAGMA integrity_check` and a schema check,
and only then renamed to its final name. A snapshot that fails validation is
deleted.

**Rationale.** `VACUUM INTO` produces a consistent, compacted copy of a database
that is being used, without stopping the service and without depending on the
`-wal` being complete. `docs/operations.md` had named it as the intended approach
since M0; M3 implements it.

Copying `finance.db`, `finance.db-wal` and `finance.db-shm` together is the
common advice and it is wrong here: `-shm` holds no persistent state, and a set
copied by hand mid-write can be internally inconsistent.

**`integrity_check`, not `quick_check`.** `quick_check` skips verifying that
index contents agree with table contents — exactly the silent corruption a backup
exists to rule out. At this scale the difference is milliseconds, and the file
may be opened in anger years from now.

**A backup that does not validate is removed rather than kept.** A file that
looks recoverable and is not is worse than no file: it is discovered at the
moment it is needed, which is the worst possible moment.

**Version-aware validation.** A snapshot is checked against the table set that
belonged to *its* schema version, not today's. A pre-migration backup taken at
version 7 has no `payment_methods` table and is perfectly good — restoring one
and letting migrations bring it forward is the entire point of keeping it.

**Naming.** `finance-20260905T183045Z-manual.db`. No colons, because the primary
development host is Windows. Collisions within a second are resolved by an
incremental suffix rather than sub-second precision or a UUID — it stays readable
and only appears when it is needed.

One bug found by testing is worth recording: pruning **deletes files, which frees
their names**, and the first implementation chose names by checking the directory
alone. A snapshot taken in the same second as a pruned one reused its name, so a
new file sorted as old — which would then have corrupted which snapshot pruning
believed was oldest. Sequence numbers are now monotonic within the process as
well as checked against the directory.

---

## TD-034 — Retention applies only to shutdown backups

**Status:** Accepted · **Milestone:** M3

**Decision.** Only snapshots taken on shutdown are pruned, keeping the most
recent `BACKUP_RETENTION` (default 10). Manual, pre-migration and pre-restore
snapshots are **never** removed automatically. `BACKUP_RETENTION=0` disables
pruning entirely.

**Rationale.** The three kinds are not comparable.

Shutdown snapshots are produced by the process lifecycle rather than by anyone's
intent; a development day generates dozens of near-identical ones, and the value
of the tenth-oldest is close to zero.

Pre-migration snapshots are rare — one per schema change, seven in this project's
entire life — and they are the only thing standing between a bad migration and
lost data. Pruning them by count would delete the M3 one after ten restarts.

Manual and pre-restore snapshots exist because someone decided they should. A
retention policy that quietly removes those is a policy nobody can trust, and an
untrusted backup system is not a backup system.

This is why the reason is part of the **filename**: the policy is only
expressible because the file says what it is.

**Alternatives considered.** No retention — the directory grows without bound
during development. A single count across all kinds — deletes the irreplaceable
to make room for the disposable. Age-based expiry — the same problem, with a
clock attached.

---

## TD-035 — Restore is an offline subcommand of the same binary

**Status:** Accepted · **Milestone:** M3

**Decision.** `finance-tracker restore --file <name>`, run with the server
stopped. No second binary, and no button in the web interface.

```
docker compose stop
docker compose run --rm app restore --file finance-20260905T183045Z-manual.db
docker compose up -d
```

**Rationale.** Restoring replaces the database file. Doing that while handlers
hold it open would leave them reading a file that no longer exists. Making it an
offline operation is not a limitation; it is the only version that is safe.

The same binary because it already has everything the job needs — the
configuration, the database conventions, the backup package — and the container's
ENTRYPOINT is the binary, so `docker compose run --rm app restore …` works with
no image change. A second binary would have to be built, shipped and kept in step
for nothing.

**Ordering, and why it is what it is.**

```
validate the snapshot        -- before anything is touched
snapshot what is there now   -- so the restore itself is reversible
copy the backup alongside    -- the live file is still untouched
move the live file aside     -- with its -wal and -shm, as a group
move the copy into place
discard what was moved aside
```

The journal files travel **with** the database, as a group. A `-wal` belongs to
the file it was written for: leaving a stale one beside a freshly restored
database is the one combination that can corrupt the result, and deleting it
while keeping the old database would strip that database of its most recent
transactions. Moving all three keeps the rollback exact.

Nothing is ever renamed onto an existing path — Windows fails that and POSIX
silently overwrites, and depending on either is depending on the platform.

**Restore does not run migrations.** It replaces and stops. The next ordinary
start does `Open → Up → serve`, which is what lets a schema 7 snapshot be brought
forward to 9 by the same code path that migrates anything else.

**Path safety by construction.** `--file` accepts a **bare filename** and nothing
else: absolute paths, volume names, and both separators are refused on every
platform, before the name is joined to the backup directory. A prefix comparison
against the resolved directory would have to be right about symlinks, case
folding, UNC paths and `..`; refusing to accept a path at all is a smaller thing
to be right about. Note that a backslash is an ordinary filename character on
Linux, so `C:\evil\x.db` is a single valid filename there — which is why both
separators are rejected regardless of where the container runs.

---

## TD-036 — Payment methods became an entity

**Status:** Accepted · **Milestone:** M3 · **Supersedes part of** TD-026's M2 shape

**Decision.** `transactions.payment_method TEXT` with a four-value `CHECK`
becomes `payment_method_id INTEGER` referencing a new `payment_methods` table
that the user administers. The four M2 values are migrated as rows, under the
names the interface already displayed.

**Rationale.** M2's fixed set was right while it was small and stable. Real use
produced "Pago móvil", "Zelle" and "Débito", and adding one meant writing a
migration and redeploying — which a personal finance application should not need
in order to record how someone paid for lunch.

**Seeded with the display names, not the codes.** `BANK_TRANSFER` becomes "Bank
transfer" because that is the string the interface already showed for it, so
upgrading changes nothing anyone can see. Nothing else is seeded: the user
creates their own, for the same reason categories have no seed.

**Uniqueness is exact, not `NOCASE`** — and this diverges from `categories`,
knowingly. SQLite's `NOCASE` folds ASCII only, so it would treat "Zelle" and
"zelle" as the same while treating "Café" and "CAFÉ" as different. Claiming
case-insensitive uniqueness while delivering it for half the alphabet is worse
than not claiming it, and importing ICU to do it properly is a dependency this
application will not carry for one index. Changing `categories` to match would
mean another destructive migration for no functional gain, so M2's choice stands
there and both are documented in data-model.md.

**Renaming changes history, deliberately.** A payment method is a label for a
channel, not a fact captured at the moment of the transaction, so correcting
"Point of sale" to "Punto de venta" should follow every past transaction that
used it. This is the same choice `category.Name` already makes. What must not be
done is reusing a row for a different meaning — renaming "Zelle" to "PayPal"
rather than creating a second method — because that silently rewrites what past
transactions claim. No schema can prevent it; it is recorded as a rule.

**No version history.** See TD-039.

---

## TD-037 — A parent table is rebuilt by rebuilding its child first

**Status:** Accepted · **Milestone:** M3

**Decision.** Migration `00009` rebuilds `transactions` **and**
`account_movements`, drops the old child before the old parent, and runs as an
ordinary transactional goose migration with `foreign_keys` on throughout.

**Rationale.** SQLite cannot alter a `CHECK` constraint, so replacing
`transactions.payment_method` means rebuilding the table. But `transactions` is a
parent: `account_movements` references it, and dropping a parent while children
point at it fails.

Three approaches were tested against real SQLite:

1. **Rebuild `transactions` alone.** `DROP TABLE` fails with
   `FOREIGN KEY constraint failed`.
2. **Add `PRAGMA defer_foreign_keys = ON`.** Also fails — at `COMMIT`. Dropping
   the parent increments the deferred-violation counter once per child row, and
   renaming a new table into its place does **not** decrement it. The data ends
   up consistent, `PRAGMA foreign_key_check` reports nothing, and the transaction
   is refused anyway.
3. **`PRAGMA foreign_keys = OFF` around the rebuild.** Works. But the pragma is
   connection-scoped and a no-op inside a transaction, so it forces the migration
   out of goose's transaction and leaves the connection in a state the rest of
   the schema depends on being restored.

What is done instead needs none of that. Rebuild the child as well, and **drop
the old child first**: with `account_movements` gone, nothing references the old
`transactions`, so dropping it is ordinary. The new child is created pointing at
`transactions_new`, and renaming the new parent into place makes SQLite rewrite
the child's `REFERENCES` clause to follow it — verified in the resulting schema.

Foreign keys stay on for the entire operation, and the migration stays a normal
transactional one. It was rehearsed against a schema 7 database carrying accounts,
categories, transactions and movements using all four payment methods before it
was written, and again against the real database on this machine.

**Note for later.** The instinct when a rebuild hits a foreign key is to switch
foreign keys off. It is worth remembering that reordering the drops removes the
need.

---

## TD-038 — The M3 migrations are forward-only

**Status:** Accepted · **Milestone:** M3

**Decision.** `00008` and `00009` have no working `Down`. Their Down sections
contain a statement that fails loudly and names the alternative. The supported
way back is restoring the pre-migration snapshot.

**Rationale.** After M3 a transaction can reference a payment method that M2's
four-value enumeration cannot spell. A `Down` would have to discard those rows or
collapse them into `OTHER`, and both destroy information about where money
actually went. This project does not invent lossy mappings.

The rollback path is better than a `Down` anyway, and it exists because of this
milestone: startup takes a snapshot automatically before applying migrations to a
database that already has a schema.

```
pre-migration snapshot (schema 7) → apply M3 → if needed: restore that snapshot
```

**Why the Down fails rather than doing nothing.** An empty Down would let goose
mark the migration as rolled back while the schema stayed exactly as it was —
a lie recorded in `goose_db_version`. A statement that errors is honest.

`rollback_test.go`, which exercises the reversible migrations, skips these two by
name, and a separate test asserts they refuse to run.

---

## TD-039 — Editing overwrites; there is no history of edited records

**Status:** Accepted · **Milestone:** M3

**Decision.** M3 supports editing accounts, categories, transactions and opening
balances. It does **not** preserve previous versions. There is no audit log, no
history table and no event sourcing.

**Rationale.** The need is real and small: a transaction entered against the wrong
account, an expense entered as income, a typo in an amount. Correcting those is
what the user asked for. Recording *that they were corrected* is a different
feature with a different cost — a second write path, a second schema, and a
retention question of its own — and nobody has asked for it.

For a single-user personal application the undo that matters is the one that
already exists: the whole database, from a backup. M3 makes that reliable in the
same milestone that makes editing possible, which is not a coincidence.

**Stated plainly in the documentation** so it is a known property rather than a
discovered one:

> M3 supports editing current records. M3 does not preserve previous versions of
> edited records.

Revisitable if a real need appears. It would arrive as its own decision.

**What editing does NOT relax.** An edit runs exactly the same validation a
creation does — the account exists and is active, the category matches the type,
the payment method is active, the amount is positive and within the currency's
precision. Creation and editing share one code path for this, because an edit
that could produce a state a creation would refuse would be a second door into
every invariant this ledger maintains. The transaction and its movement are
rewritten in one `sql.Tx`, so a balance is never briefly wrong.

---

## TD-040 — Moving a transaction across currencies requires explicit confirmation

**Status:** Accepted · **Milestone:** M3

**Decision.** Editing a transaction onto an account denominated in a different
currency is allowed only when the caller sets `AllowCurrencyChange`. Otherwise
the service returns `ErrCurrencyChangeNotConfirmed` and writes nothing.

**Rationale.** The amount is a decimal string, parsed at the destination
account's `minor_unit`. Moving "100.00" from a USD account to a VES account does
not convert it — it reinterprets it as a different sum of money. M3 has no
exchange rates and must not invent one.

Three options were considered:

- **Refuse the move entirely.** Safe, and wrong: picking the wrong account is a
  normal mistake, and forcing archive-and-recreate to fix it is worse than the
  mistake.
- **Allow it silently.** The amount changes meaning without anyone saying so,
  which is the exact class of error the money rules exist to prevent.
- **Require the caller to say so.** One boolean, defaulting to false, that the UI
  can only set after showing which currency the amount will be read in.

The flag is deliberately not something a handler sets unconditionally, and the
contract says so. `ErrCurrencyChangeNotConfirmed` is the only error in the ledger
that means "ask the user and try again" rather than "the input was wrong".

Moving between accounts of the **same** currency needs no confirmation: nothing
is reinterpreted.

The currency is still derived from the account. No `currency_code` column was
added to `transactions` or `account_movements`, and none will be — a second
column could only ever disagree with the account.
---

## TD-041 — A transfer is one transaction with two movements, and no new columns

**Status:** Accepted · **Milestone:** M4

**Decision.** A transfer between two of the user's own accounts is a single
`transactions` row with `transaction_type = 'TRANSFER'` and **two**
`account_movements`: a negative one in the source account and a positive one of
equal magnitude in the destination. No column was added to any table.

**Rationale.** This is the case the ledger's two-table design was built for
(TD-026), and the design paid off exactly as predicted:
`account_movements.transaction_id` was never `UNIQUE`, balances were already
`SUM(amount_minor)` over whatever rows exist, and so a transfer required no
structural change at all.

The obvious alternative — `transactions.source_account_id`,
`destination_account_id` and `amount` — was rejected because those three facts
are already stated by the two movements. A second copy is a second thing that can
be wrong, and when the two disagree neither can be trusted. It would also have
made the balance derivation type-dependent: every balance query would have to
know that a transfer's money lives somewhere other than where every other
transaction's does.

Because the two movements are equal and opposite, `SUM(amount_minor)` over a
transfer is zero, so **the total held in a currency is identical before and
after**. That is not a rule enforced somewhere; it is a consequence of the
representation, which is why it is the representation.

A transfer carries **no category** — filing it under one would put movement
between one's own accounts into every later question about where money came from
or went — and **no payment method**, because no channel with the outside world
was used. Both are `NULL` and the database refuses a row where they are not.

**`TRANSFER` is not the payment method named "Bank transfer".** The method
records how an operation with the outside world happened; the type records that
money moved between two accounts we own. The distinction was written into the
schema comments in M2, before either of them existed, and it survives unchanged.

---

## TD-042 — Editing a transfer keeps its movement ids

**Status:** Accepted · **Milestone:** M4

**Decision.** `UpdateTransfer` rewrites the two existing `account_movements`
rows in place. It does not delete and recreate them. The movement that is
currently negative stays negative and receives the new source account; the
positive one stays positive and receives the new destination. Swapping the two
accounts swaps what the accounts are, not which row is which.

**Rationale.**

- `Repository.UpdateMovement` already existed and already did exactly this for
  single-movement transactions. Reusing it keeps one write path rather than two.
- Recreating would mean **deleting** a financial row, and nothing in this
  application deletes one. "Archive or correct" is the rule everywhere else, and
  a delete introduced here for convenience would be the first exception.
- `account_movements` has no `updated_at` because a movement records the same
  event it always did (migration `00007`). Keeping the id and `created_at` is the
  same statement; recreating them would contradict it.
- `TransactionView.MovementID` and `TransferDetail`'s two movement ids reach the
  presentation layer. Recreating would make them churn on every edit for no gain.

The alternative — delete both and insert two fresh rows — is simpler to write and
was rejected for the reasons above. It is also not obviously safer: nothing
references `account_movements`, so neither approach risks a dangling foreign key.

---

## TD-043 — A transfer may leave an account negative; there is no insufficient-funds rule

**Status:** Accepted · **Milestone:** M4

**Decision.** No balance check is performed before a transfer. An account may end
up overdrawn. There is no overdraft setting, no threshold, and no warning in the
backend.

**Rationale.** Three reasons, the third of which is decisive.

- **Overdrafts are real.** Bank statements show them. A ledger that cannot
  represent what the statement says is a ledger that has to be reconciled by
  hand.
- **A negative cash balance is information.** It almost always means a movement
  has not been entered yet. Refusing the transfer hides the signal and leaves the
  user with two wrong balances instead of one obviously wrong one.
- **The rule would be unenforceable anyway.** `effective_date` can be backdated.
  A transfer entered today and dated three weeks ago can make every balance in
  between negative, and no check performed at the moment of writing prevents that
  without a temporal balance model M4 does not have. A rule that only sometimes
  applies is worse than no rule: it teaches the user to trust a guarantee that
  does not exist.

Prohibiting a negative balance is a **policy**, not an accounting truth. It would
also have been inconsistent: M3 already permits a negative opening balance and
permits editing an expense to any size.

The presentation layer may style a negative balance distinctively. It must not
refuse one.

---

## TD-044 — Transfer integrity is enforced in Go and re-checked on read, not by triggers

**Status:** Accepted · **Milestone:** M4

**Decision.** The rules that make a transfer a transfer — exactly two movements,
one negative and one positive, equal in magnitude, in two different accounts of
the same currency — are enforced in `internal/ledger` inside one `sql.Tx`, and
**re-verified by the read path** before a transfer is presented. They are not
`CHECK` constraints and not triggers.

**Rationale.** Every one of those rules spans more than one row, and a `CHECK`
sees one row. Expressing them in SQLite would take triggers on every write to
`account_movements`, each having to reason about the transaction's other rows
mid-statement. That is a lot of machinery to guard a shape that one function and
one database transaction already guarantee, and the machinery itself becomes
something that can be wrong.

What the schema *can* see from a single row is enforced there: a `TRANSFER` has
no category and no payment method (a table `CHECK`), and no movement is ever zero
(a column `CHECK`).

**The read path is the second half of this decision, and the part that is not
obvious.** `scanView` refuses to build a `TransferDetail` from movements that do
not satisfy the rules, returning `ErrInconsistentTransfer`. No input can trigger
it — writes establish the shape atomically and nothing deletes a movement — so
reaching it means the file was modified by something else. The wrong response is
to render half a transfer: a row reading "250.00 left Mercantil" with no
destination, or with a destination that received a different amount, is a ledger
lying quietly, and quiet is the failure mode this project spends most of its
effort avoiding.

For the same reason the listing anchors a transfer on its **lowest-numbered**
movement rather than on its negative one. Anchoring by sign reads better, but a
malformed transfer with no negative movement would then match nothing and vanish
from the listing entirely. One row per transaction, always, and then complain.

A partial unique index — "at most one negative movement per transaction" — was
considered and rejected: it would hold today and would freeze "two movements, one
per sign" into the schema, which a later split transaction would have to migrate
away from. See `account_movements` in [data-model.md](data-model.md).

---

## TD-045 — Each transaction type has its own edit use case, and the guard names what is allowed

**Status:** Accepted · **Milestone:** M4

**Decision.** Three edit use cases, not one:

```
UpdateTransaction     → INCOME, EXPENSE
UpdateOpeningBalance  → OPENING_BALANCE
UpdateTransfer        → TRANSFER
```

Each refuses a transaction of any other type with `ErrTypeChangeNotAllowed`, and
each guard is written as **"the current type must be one of the ones I handle"**
rather than as a list of types to exclude.

**Rationale.** The positive form is the whole decision, and it was arrived at by
finding the bug the negative form had already planted.

M3's `UpdateTransaction` read:

```go
if current.Type == OpeningBalance { return ErrTypeChangeNotAllowed }
```

which was correct while three types existed. The moment `TRANSFER` joined the
enumeration it became wrong — a transfer is not an opening balance, so it would
have walked straight through. `UpdateTransaction` rewrites **one** movement. A
transfer has two. The result would have been a debit with no matching credit:
money destroyed, in the one place in this application whose entire purpose is
that this cannot happen. Nothing would have failed loudly; a balance would just
have been wrong.

Rewritten as

```go
if current.Type != Income && current.Type != Expense { return ErrTypeChangeNotAllowed }
```

the same guard also refuses the fifth type, whenever it arrives, without anyone
having to remember to come back.

The same reasoning applies at the HTTP boundary. The income-and-expense form
previously coerced any unrecognised `type` value to `EXPENSE`, so a submission
saying `TRANSFER` would have been recorded as an expense against one account. It
now returns 422 and records nothing. `web.TransactionTypesForIncomeExpenseForm`
exists so that a form's options and the handler's validation cannot drift apart.

This continues M2's reasoning for three separate `Record*` methods: one method
per use case makes an invalid combination something with no signature to arrive
through, rather than something that is merely rejected.

---

## TD-046 — An exchange rate is a canonical decimal string, and its arithmetic is `math/big`

**Status:** Accepted · **Milestone:** M5

**Decision.** `exchange_rates.rate` is `TEXT` holding a canonical decimal —
`"842.20670000"` — and every operation on it is performed on `math/big.Int` and
`math/big.Rat` through a new `money.Decimal` type. No decimal dependency was
added.

**Rationale.** Three options were weighed: TEXT, an integer mantissa plus a scale
column, and a third-party decimal package.

The first thing that settles it is that **the storage choice does not decide
exactness.** Valuation needs inversion (`1/842.2067`) and division (a cross
rate), and neither result is a finite decimal in general. Any decimal
representation — TEXT, mantissa/scale, or a library — has to round to express
one. So the arithmetic has to be rational regardless, and `big.Rat` divides
exactly by construction. What remains to choose is only fidelity and legibility.

On those, TEXT wins outright. It stores exactly what the source published,
trailing zeros included, so the precision of the statement is preserved and the
stored value can be compared with the published one as a string. It is one column
rather than two that can fall out of step, it has one spelling per value rather
than several, and the owner of this database opens it with the sqlite3 CLI —
`842.20670000` reads where `84220670000` paired with `8` does not. That is the
same argument that made timestamps TEXT in TD-016.

The usual objection to TEXT is that SQLite cannot compare it numerically.
**Nothing in this application orders or range-filters by rate**: the lookup is by
pair and date, both exact. The one numeric property the database does need — that
a rate is greater than zero — is expressed honestly without a cast: stripping the
decimal point and then the leading zeros leaves an empty string exactly when the
value is zero.

`github.com/shopspring/decimal` was rejected on a specific fact rather than on
principle: its `Div` uses `DivisionPrecision = 16` and **rounds by default**,
which is precisely the trap this project exists to avoid.
`github.com/govalues/decimal` is pre-1.0 and capped at 19 digits. `math/big` is
the standard library and its division is exact.

**Consequence.** `internal/money` gained `Decimal`, `ParseDecimal`, `ConvertMinor`
and exact half-even rounding. This is where TD-027 said the FX arithmetic would
land, and it is why `money` was its own package rather than part of `currency`.

The `Decimal` type holds its unscaled integer behind a **pointer** that nothing
hands out, and every accessor allocates. A `big.Int` embedded by value would make
every copy of a `Decimal` share one backing slice, so a single in-place `math/big`
operation — which is how that API is normally used — would reach through every
copy ever made of it.

---

## TD-047 — Valuation rounds half to even, once, per currency

**Status:** Accepted · **Milestone:** M5

**Decision.** A conversion is computed exactly as a rational and rounded **once**,
**half to even**, into the destination currency's minor units. The rounding
happens **per currency component**, never per account, and the consolidated total
is the plain integer sum of the rounded components.

**Rationale, in two parts.**

*Why half to even.* This is a valuation, not a payment. Nothing changes hands,
there is no party to favour, and the same balances are revalued many times a day.
Half away from zero biases every one of those revaluations upward; half to even
does not. It is also symmetric about zero, which matters here because a transfer
may legitimately leave an account overdrawn (TD-043) and a debt must not round by
a different rule from the credit that cancels it.

*Why per currency.* The alternative — convert each account, then sum —
accumulates up to half a unit of error per account. The visible consequence
decides it: with per-currency rounding, **the components a screen shows add up
exactly to the total that screen shows**. With per-account rounding they do not,
and a page whose lines do not sum to its own total is the first thing a person
notices and the last thing they trust.

---

## TD-048 — Application settings are a typed singleton row, not a key/value table

**Status:** Accepted · **Milestone:** M5

**Decision.** `app_settings` has one row, forced by `CHECK (id = 1)`, with
`base_currency_code` as a typed column carrying a real foreign key to
`currencies(code)`.

**Rationale.** M0 put configuration in the environment, and that was right while
every setting described the *deployment*. The base currency of valuation is a
different kind of fact: it is a choice the user makes from inside the application,
about their own money.

The deciding argument is the foreign key. **Only a typed column can have one.** A
generic `settings(key, value)` table holding the string `'USD'` references
nothing, so archiving or renaming a currency could leave the setting pointing at
something that no longer exists and nothing in the database would object. A
generic table also invites unrelated settings to accumulate in a place with no
schema.

`CHECK (id = 1)` rather than a convention: with two rows, every reader would have
to pick one, and "the settings" would stop being a meaningful phrase. The row is
created by the migration rather than lazily, so every read is a plain `SELECT`
with no "or insert a default" branch, and its absence is an integrity failure
rather than an ordinary state something has to handle.

This is **not** the beginning of a preferences system. One user, one process, one
database file. A second setting arrives as a second column when a second setting
exists.

---

## TD-049 — Source precedence applies within one effective date, never across dates

**Status:** Accepted · **Milestone:** M5

**Decision.** When several sources have spoken about a currency pair, the lookup
is:

```
1. the latest effective_date <= the valuation date, from any source
2. among the observations OF THAT DATE, the highest-precedence source
```

with `MANUAL` above `BCV_OFFICIAL`.

**Rationale.** The obvious implementation — order by precedence, then by date — is
silently, catastrophically wrong. A manual rate typed once in January would
outrank every publication for the rest of the year, and the user's net worth would
stop moving. Nothing would fail, nothing would be logged, and the figure would
stay plausible: exactly the shape of bug this project takes most seriously.

Scoped to one date, a manual observation wins precisely where it claims to apply
and nowhere else, which is what "override" actually means. A test asserts the
failure mode directly: a manual rate dated Friday must **not** be preferred over a
published rate dated the following Monday.

**One `MANUAL` concept, not two.** "Manual override" and "manual fallback" were
considered as separate sources and rejected: with precedence bounded by date, both
collapse into the same behaviour — a typed rate applies on its own day, and where
no publication exists it is simply the only observation. A second enum value would
double the precedence matrix and buy nothing.

**Withdrawing an override is archiving, not deleting.** `is_active = 0` makes the
published rate underneath apply again while keeping the record that somebody
disagreed with it — the same retirement mechanism every other table here uses, and
consistent with this project having no delete anywhere.

---

## TD-050 — The BCV's broken TLS chain is handled with a provider-scoped additional trust anchor

**Status:** Accepted · **Milestone:** M5

**Decision.** The HTTP client used for `www.bcv.org.ve` — and for **no other
host** — verifies against the system root pool **plus** one embedded Sectigo CA
certificate. `InsecureSkipVerify` is not used anywhere in this repository, and a
test walks the source tree to keep it that way.

**The problem, measured rather than assumed.** `www.bcv.org.ve` serves a
certificate chain that does not verify. Its leaf is issued by *Sectigo Public
Server Authentication CA DV R36*, but the intermediate it actually sends is
*Sectigo RSA Domain Validation Secure Server CA* — a different CA, which cannot
have signed it.

Browsers and curl hide this by fetching the correct intermediate from the leaf's
Authority Information Access extension. **Go does not do AIA fetching on Linux.**
Probed on 2026-09-11 (21:59 UTC−04:00) against the real host:

```
Windows, Go 1.27                     → 200 OK
gcr.io/distroless/static, Go 1.27    → x509: certificate signed by unknown authority
system roots + embedded R36          → 200 OK
```

The first two lines are the dangerous part: this works perfectly on the
development machine, because Go uses the Windows platform verifier there and
Windows does chase AIA, and fails only in the container that actually runs.

**What the fix is, stated honestly.** This is **not** "supplying a missing
intermediate". Go offers no way to inject an extra intermediate into the standard
verifier, so the certificate is added to the **root** pool, which makes it an
**additional trust anchor** for that client: anything that CA signs would be
accepted by this transport. That is a real widening of trust. It is bounded on
three sides:

- the anchor is a genuine, publicly trusted Sectigo CA — not a self-signed
  certificate anyone could mint;
- the pool **starts from the system roots**, so nothing normally trusted is
  removed;
- it is applied **only when the configured URL names `www.bcv.org.ve`**. The base
  URL is configurable through `FX_BCV_URL`, and an extra trust anchor must never
  follow a configured URL to wherever it happens to point. Any other URL — a test
  server, a mirror, a typo — gets ordinary verification.

Everything else stays strict: hostname verification on, TLS 1.2 floor, same-host
redirects only, at most three of them.

The embedded certificate's subject, issuer, SHA-256 fingerprint and validity are
recorded in code and asserted by a test, so that a replacement — a bad merge, a
careless refresh, a copy-paste from the wrong tab — fails the build rather than
silently widening trust to whoever issued the substitute.

**When it stops working.** If Sectigo rotates that CA, or the BCV fixes its chain
and moves to another one, the fetch fails loudly and manual entry keeps working.
That is the intended failure mode: a broken fetch is visible, silently weakened
verification is not.

---

## TD-051 — The business calendar is a fixed UTC−04:00 zone, and `Today` is not UTC

**Status:** Accepted · **Milestone:** M5

**Decision.** `database.EconomicLocation` is `time.FixedZone("-04:00", -4*3600)`,
and `database.Today` returns the calendar date it is *in Venezuela*, not
`time.Now().UTC()` truncated to a day.

**Rationale for the second half first, because it was a live bug.** At 21:59 in
Caracas on 2026-09-11, UTC is already 2026-09-12. A UTC "today" is therefore wrong
for four hours out of every twenty-four, and in this application that means asking
for exchange rates effective on a day the user has not reached, and pre-filling a
financial form with tomorrow's date. `database.Today` had existed since M2 with
exactly this defect; nothing had caught it because nothing called it yet.

**Rationale for the fixed zone.** `time.LoadLocation("America/Caracas")` is the
obvious spelling and would fail at runtime: `gcr.io/distroless/static` ships no
tzdata at all, so it would work in development and break in the container — the
worst possible distribution of failure.

`import _ "time/tzdata"` fixes that by embedding the database, at roughly 450 KB
on a 5.8 MB image. It was rejected because **it buys no freshness**: an embedded
tzdata is frozen at build time exactly like a constant is, so a change to
Venezuela's offset would require a rebuild either way. Given that, the constant is
smaller and cannot fail at runtime.

Venezuela observes no daylight saving, so there is no *rule* to get wrong within a
year — only a permanent offset, which has changed twice in twenty years (2007 and
2016). If it changes again, `EconomicLocation` is the one place it changes.

---

## TD-052 — Valuation is its own package, and the net worth is never stored

**Status:** Accepted · **Milestone:** M5

**Decision.** `internal/valuation` holds the consolidated net worth. It reads from
`ledger`, `fx` and `settings`, writes nothing, and stores nothing.

**Why its own package.** It combines three domains that do not know about each
other. It is not in `internal/application`, because that package exists for
**writes that must span two domains atomically** (TD-030) and this writes nothing
— the atomicity argument that justifies `application` simply does not apply. It is
not inside `fx` either, because that would make the package that owns exchange
rates depend on the ledger, and `fx` should be about rates.

**Why nothing is stored.** There is no `current_net_worth` column, no
`converted_balance`, no cache table and no in-memory cache. The net worth is a
*function* of the ledger, the stored rates and the base currency; a stored copy
would be a fourth thing that could disagree with the three that produce it, and
the first time it disagreed nobody would know which was right. This is the same
reasoning that has kept every balance derived since M2, applied one level up.

A test looks for tables whose names suggest a cached valuation and fails if it
finds one, so the idea has to be argued for rather than merely added.

**Why a missing rate is not an error.** `GetNetWorthAt` returns `Complete = false`
plus the list of unresolvable pairs, rather than failing. An error would deny the
user the part of their net worth that *is* known; a zero would silently shrink the
total. Reporting the gap explicitly is the only option that is both useful and
true, and it gives the interface something to act on — the missing pair, the
amount at stake, and a link to the form that fixes it.

---

## TD-053 — An observation is never used before its effective date

**Status:** Accepted · **Milestone:** M5

**Decision.** The rate lookup is `latest effective_date <= valuation_date`, with
**no exception and no fallback**. An observation dated after the valuation date
takes no part in any resolution — direct, inverted or crossed. If it is the only
one for a pair, that currency is reported **missing**, not approximated.

**Why this needs its own decision.** The BCV publishes **forward**. On Friday
2026-09-11 its page carried a Fecha Valor of Tuesday 2026-09-15 — verified by
fetching it. So a fresh installation whose first fetch happens on a Friday can
hold a perfectly good rate and still be unable to value today, and the tempting
fix is to fall back to the nearest available observation and label it.

That fix was designed, and then rejected. Reaching forward is **look-ahead bias**:
it makes a figure about a given day depend on information that did not exist on
that day. It would be harmless-looking on today's dashboard and quietly wrong
everywhere the same function is used to value the past, which is precisely what
the historical milestone will do with it. A label on the screen does not undo a
number that was computed from the future.

The gap is the honest answer, and it is actionable: the interface names the
missing pair and offers the form that fixes it. The forward-dated observation is
still stored and still shown, marked as upcoming, and becomes usable on its own
date and not one day earlier.

---

## TD-054 — No third-party rate mirror in M5

**Status:** Accepted · **Milestone:** M5

**Decision.** `source` admits `MANUAL` and `BCV_OFFICIAL` and nothing else. No
`BCV_MIRROR` value exists, not even unused.

**Rationale.** A mirror was genuinely wanted: this application can be switched off
for days, the BCV's page shows only the current publication, and the historical
milestone will need past rates. Two candidates were evaluated on 2026-09-11.

`pydolarve.org`, the most-cited historical mirror, **no longer resolves in DNS**.

`ve.dolarapi.com` answers, and its answer disqualifies it:

```json
{"moneda":"USD","promedio":832.4883,"fechaActualizacion":"2026-09-11T00:00:00-04:00"}
```

Four decimal places where the BCV publishes eight, delivered as a JSON *number* —
so it has already been through a float — under a date field that is not the Fecha
Valor, and holding a value that disagrees with what the BCV's own page was showing
at that moment.

Storing that as a source called `BCV_MIRROR` would put rows in this database that
say "BCV" without being the BCV, at reduced precision, dated by a field that means
something else. Manual backfill is exact and already exists. A value is added to
the enumeration when a source worth trusting is actually implemented, not in
anticipation of one.


---

## TD-055 — A currency exchange is its own transaction type, and its rate is derived

**Status:** Accepted · **Milestone:** M5.5

**Decision.** Money actually exchanged between two of the user's own accounts in
two different currencies is `transaction_type = 'FX_EXCHANGE'`: **one transaction
with two movements**, one negative in the source account and one positive in the
destination, in two different currencies and of **unrelated magnitude**.

The rate it happened at is **derived from the two amounts on every read** and
stored nowhere. `transactions` gained no `exchange_rate_id`, no `fx_rate` and no
column of any kind.

**Rationale, in three parts.**

*Why not a widened `TRANSFER`.* A transfer's two movements are equal and
opposite, so the total held in its currency is identical before and after — that
invariant is the entire point of the type, and it is what `splitTransfer`
enforces on every read. An exchange breaks it deliberately: bolívares go down and
dollars go up, and the two magnitudes are quantities of different things. Adding
a second currency to `TRANSFER` would have meant weakening the one rule that made
it trustworthy, for every transfer ever recorded, in order to accommodate an
operation that is not a transfer.

*Why not `BUY_USD` / `SELL_USD`.* They would be two names for one event,
distinguished by a fact the movements already state: which account was the
destination. "Compra de USD" is a label a screen infers; encoding it as a type
would let the label and the movements disagree.

*Why the rate is not stored.* The two amounts state it exactly — 84,220.67 ÷ 100
is 842.2067, with nothing left over. A stored copy would be a third source of
truth able to disagree with the two that produced it, and the disagreement would
be invisible: an edit that corrected an amount and not the rate would leave a row
that looks fine and means nothing. Deriving it costs one `big.Rat` division per
read, on a screen that shows fifty rows.

**Alternatives considered.** A rate-driven input mode — the user types a rate and
one amount, the application computes the other — was considered and left out of
scope. It sounds convenient and is a trap: the computed amount would disagree
with the bank by a céntimo whenever the bank rounded differently, and the ledger
would then be wrong about what actually moved. The user knows both amounts;
they are on the statement.

A fee or commission field was also rejected. If the bank charged one separately
it is an ordinary `EXPENSE` under a category such as "Bank fees". If it was
deducted from the exchange, the two real amounts already include it and the
derived rate already reflects what it cost.

---

## TD-056 — Reference FX and execution FX are never reconciled

**Status:** Accepted · **Milestone:** M5.5

**Decision.** The rate an `FX_EXCHANGE` happened at takes **no part** in
`internal/valuation`. Recording or editing one reads no row of `exchange_rates`
and writes none. The difference between what the user paid and what the BCV
published is **not** converted into an expense, an income, a fee, a gain or a
loss; no such row exists and no code creates one.

The visible consequence is intended: **after an exchange at a rate other than the
reference rate, the valued net worth changes.** Buying dollars above the official
rate reduces it; buying below it raises it.

**Rationale.** M5 fixed the distinction and this is the first milestone that can
break it. A reference rate answers "what is this worth today?"; an execution rate
answers "what did I get for it on the 16th?". They are different questions about
different moments, and the two obvious ways to connect them are both wrong:

- *Using the execution rate to value balances.* One trade would then set the
  price of everything held in that currency until the next one — the same failure
  TD-049 rejected for manual rates across dates, with a worse input.
- *Booking the difference as a gain or a loss.* That is cost-basis accounting. It
  needs lot tracking, a policy for which lot was sold, and a decision about
  unrealized movement on every revaluation. None of it exists, and inventing a
  single-number version of it would produce a figure that looks like accounting
  and is not.

The honest answer is that the net worth moved because the money did. A user who
paid above the official rate really is worth less at the official rate than they
were an hour earlier, and the application says so rather than papering over it
with an invented offsetting entry.

**Alternatives considered.** Storing the reference rate of the day alongside the
exchange, "for comparison". Rejected: it is `exchange_rate_id` wearing a hat, it
would go stale the moment a `MANUAL` rate for that day was added, and the
comparison it enables is one nothing in this application has asked for.

---

> **Numbering note.** TD-057 to TD-062 are taken by the production-deployment
> work on the `cloud-hosting` branch, which is `POSTPONED` and not yet merged.
> M6 continues from TD-063 so that the two sets can be merged without
> renumbering either.

## TD-063 — A balance is always "as of" a date, and "current" means today

**Status:** Accepted · **Milestone:** M6

**Decision.** Every balance this application shows is the sum of the movements
whose transaction has `effective_date <= D` for some date D. The **current**
balance is the one as of `database.TodayAt(now)` — today in Venezuela (TD-051):

```
current balance = SUM(amount_minor) WHERE effective_date <= today
```

not `SUM(amount_minor)` over every movement. The rule lives in the ledger's own
balance queries (`GetAccountBalance`, `ListAccountBalances`,
`ListCurrencyTotals`), not in a dashboard filter, so the accounts screens, the
dashboard and the valuation cannot disagree about what an account holds today.

A future-dated transaction still exists, is listed, and can be edited. It is
simply not part of the balance yet. Each balance carries
`FutureMovementCount`, so a screen can say "2 movimientos futuros" without
computing anything; there is no projected balance.

**Rationale.** Future dates have been allowed since M2 (TD-028). Until M6 the
"current" balance silently included them, and nothing noticed because nothing
compared a balance with a date. A historical net worth does exactly that: the
series point for today would have excluded a movement dated next week while the
account screen included it, and the two figures for "today" would have differed
with no explanation. One rule, applied by the code that owns balances, removes
the possibility.

**Alternatives considered.** Filtering only in the dashboard (rejected: it is
the inconsistency itself, moved somewhere else). A projected-balance system
(out of scope; the count is the honest minimum).

---

## TD-064 — Archived accounts are valued; archiving is visibility, not an event

**Status:** Accepted · **Milestone:** M6 · **Supersedes:** M5's "active
accounts only" valuation rule

**Decision.** The net worth — current and historical — includes **every**
account, active or archived. `accounts.is_active` decides whether an account is
offered for new movements and listed by default; it is not a financial fact and
does not change any figure. There is no `archived_at` and no migration.

An archived account whose balance is not zero is reported
(`AccountComponent.IsActive = false` with a non-zero balance,
`NetWorth.ArchivedWithBalance`) so the interface can warn about it. Archiving is
never refused for that reason.

**Rationale.** Excluding archived accounts from history breaks the one invariant
a transfer has. Account A holds 100, transfers it to B, and is archived at zero.
If A disappears from history, the 100 it held before the transfer disappears too
and reappears in B on the day of the transfer: a transfer creates net worth.
Excluding archived accounts only from "today" while including them in history
would make the last point of the chart disagree with the headline figure. The
ledger is the source of truth; an account's visibility is not.

An `archived_at` column would not fix it either: archiving is reversible, so one
date cannot describe its history, and the money did not stop existing when the
account was hidden.

---

## TD-065 — History is rebuilt from the ledger on every read, in bulk

**Status:** Accepted · **Milestone:** M6

**Decision.** `NetWorth(date)` and `NetWorthSeries(from, to)` are derived on
every request from `account_movements`, `transactions` and `exchange_rates`.
There are no snapshot tables, no cache and no background job (TD-052 extended to
history). One request loads everything it needs with a constant number of
queries:

| Query | What it reads |
| --- | --- |
| settings | the valuation base |
| currencies | names, symbols, minor units |
| accounts | every account, active or archived |
| daily deltas | `SUM(amount_minor)` and a movement count per account per `effective_date` |
| rates | every active observation with `effective_date <= to` |

and then walks the days in memory, adding each day's deltas to running
per-currency balances. The cost is O(days × currencies) with no I/O inside the
loop. Five years of daily points is about 1,800 iterations.

The single-date valuation is the same engine asked for one day, so the headline
figure and the last point of the chart cannot differ.

**Rationale.** At the volume of a personal ledger — tens of thousands of
movements over a decade at most — the bulk read is a few milliseconds, and a
stored copy would be a second truth that every edit (backdated, reassigned,
recategorised) would have to invalidate correctly. Snapshots can be added later
if a measurement shows they are needed; nothing here makes that harder.

**Indexes.** None added. The deltas query is a full read of the ledger — the
future-dated rows are needed for the "movimientos futuros" count — for which a
scan is the optimal plan. The flows query ranges on
`idx_transactions_effective_date`. The rates query reads a table of a few
thousand rows. No read is done inside a database transaction: the pool holds a
single connection, and a read transaction that then called a service using the
pool would wait on itself forever.

---

## TD-066 — One resolver for one date and for a thousand

**Status:** Accepted · **Milestone:** M6

**Decision.** The rate-resolution rules of M5 — identity, direct, inverted,
one-pivot cross; `effective_date <= on`, never `>`; precedence `MANUAL >
BCV_OFFICIAL` within one date only; the freshest stalest leg for a cross — now
live in one pure function over an in-memory set of observations, `fx.Book`.
`fx.Service.Resolve` loads the observations and calls it; the historical series
builds one `Book` and calls it once per currency per day.

The semantics are **unchanged**. There is no maximum age for a rate: the latest
observation with `effective_date <= D` applies, however old. Every resolution
reports the effective date of the observations it used, so a screen can show
how old a rate is.

**Rationale.** The SQL-per-lookup resolver needed two to four queries per
(currency, date); a 365-day series would have issued thousands. Keeping the SQL
path for single dates and adding an in-memory path for series would have been
two implementations of the rule this project most needs to be the same
everywhere (TD-049, TD-053).

---

## TD-067 — A zero balance needs no rate, and a missing rate is a gap

**Status:** Accepted · **Milestone:** M6

**Decision.** A currency whose balance on a date is exactly zero is valued at
exactly zero, whether or not a rate exists for it. A non-zero balance with no
applicable rate makes that date **incomplete** (`Complete = false`, `Missing`
naming the currency and the amount at stake), as in M5. Never zero, never a
future rate, never the first rate found looking forward, never an invented one.

On the chart an incomplete day is a **gap**. A change (absolute or percentage)
whose endpoints include an incomplete day is reported as unavailable, with the
reason, rather than computed from a partial total.

**Rationale.** Zero times any rate is zero, exactly; demanding a rate for it
would have made every day before a foreign-currency account's first movement
incomplete, for no information. A missing rate for money that exists is the
opposite case: the honest answer is that the figure is unknown.

No historical rates exist before the first BCV fetch. M6 does not backfill them;
the user can enter past rates manually through the existing rates screen, and the
gaps close as they do.

---

## TD-068 — Cash flow is income and expense only, each at its own day's rate

**Status:** Accepted · **Milestone:** M6

**Decision.** The cash-flow figures count `INCOME` and `EXPENSE` transactions and
nothing else; the query names what it accepts (TD-045). `TRANSFER`,
`FX_EXCHANGE` and `OPENING_BALANCE` are neither income nor expense and appear in
no flow figure.

Each flow is converted into the valuation base at the reference rate that applied
on **its own `effective_date`** (the same `<=` rule). Within a category the
converted amounts are summed as exact rationals and rounded **once**, half to
even; a total is the integer sum of the rounded category lines, so the lines a
screen shows add up to its total. A flow with no applicable rate makes the figure
incomplete and is reported in its own currency.

`Net cash flow = income − expenses`. `Savings rate = (income − expenses) /
income`, in basis points, half to even. It is **undefined** — shown as "—", never
NaN, never infinity — when income is zero or the figures are incomplete.

**Rationale.** A flow's value is what it was worth when it happened. Converting a
March salary at December's rate would make an old month's figures move every
time the bolívar does. The period-end alternative was rejected for that reason.

---

## TD-069 — Periods are fixed day counts, compared with the adjacent equal period

**Status:** Accepted · **Milestone:** M6

**Decision.** `7D`, `30D`, `90D` and `1Y` are 7, 30, 90 and 365 calendar days
ending today inclusive. The previous period is the same number of days ending the
day before the current one starts: no overlap, no gap, equal length. `ALL` starts
at the earliest movement and has no previous period.

The net-worth change over a period is `NW(to) − NW(from − 1 day)` — from the
close of the day before the period to the close of its last day. The percentage
is that change over `|NW(from − 1)|`, undefined when that is zero.

Percentages everywhere are integers in basis points, rounded half to even by
`internal/money`; they are formatted for display and never computed again.

All of it is Go, driven by an injected clock, and tested with fixed dates.

---

## TD-070 — Breakdown lines are allocated so they add up, and the chart is SVG

**Status:** Accepted · **Milestone:** M6

**Decision.** The per-account equivalents in the base currency are allocated
from their currency's rounded figure by **largest remainder**, so accounts add up
to their currency and currencies add up to the total, to the minor unit. Shares
of the total are allocated the same way to 10,000 basis points.

The history chart is an inline `<svg>` generated by the server. Its coordinates
are computed in Go from the integer series with integer arithmetic. No charting
library, no JavaScript computing anything, and no float.

**Rationale.** Rounding each account independently would produce a breakdown
whose lines disagree with its total by a cent, which on a finance screen reads as
a bug. A charting library for one line, with no interaction beyond a period
switch, would be the largest dependency in the project for the smallest job.
