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

**Status:** Accepted · **Milestone:** M0

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
