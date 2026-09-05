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
