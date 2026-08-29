# Architecture

This document describes the architecture **as actually implemented at M0**.
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
| Packaging | Docker, multi-stage build |
| Runtime image | `gcr.io/distroless/static-debian13:nonroot`, uid 65532 |

There is exactly one direct dependency: `modernc.org/sqlite`. No web framework,
no router, no ORM, no configuration library, no dependency-injection container.

## Dependency direction

```text
cmd/web
   ↓
HTTP
   ↓
future application/domain
   ↓
database
   ↓
SQLite
```

Dependencies point downward only. HTTP does not build SQL; the database layer
knows nothing about HTTP.

**The application/domain layer does not exist yet.** At M0 the HTTP layer talks
to the database layer directly, because the only endpoint is a health check that
probes the database. When the first domain arrives, it goes between them; no
existing package has to be restructured to make room for it.

## Packages

### `cmd/web`

The **composition root**, and the only place that wires packages together.

It loads configuration, builds the logger, opens the database, creates the
listener, starts the server, waits for a signal, and shuts everything down in
order. Every failure is a returned error — there is no `panic` on an ordinary
error path and no `os.Exit` buried in a helper.

The startup and shutdown logic lives in a `run` function that takes its
environment and output stream as parameters, which is what makes a full
startup/shutdown cycle testable.

### `internal/config`

Loads and validates configuration from the environment. Four variables:
`APP_ENV`, `HTTP_ADDR`, `DB_PATH`, `LOG_LEVEL`, each with a working default.

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

### `internal/httpserver`

Builds the HTTP server and its routes. Owns the server timeouts and routes
`net/http`'s internal error reporting into the structured logger.

Routing uses the standard `http.ServeMux`, which since Go 1.22 matches on method
and path wildcards — the capability that used to justify a third-party router.

At M0 it exposes one route: `GET /healthz`.

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

One endpoint at M0:

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

## Lifecycle

Startup, in order: configuration → logger → database (opened and verified) →
listener → HTTP server.

Shutdown, on `SIGINT` or `SIGTERM`: stop accepting connections, drain in-flight
requests under a bounded timeout, wait for the serve loop to return, then close
the database. The database closes **last** — closing it while requests are still
draining would break them.

Default signal handling is restored once shutdown begins, so a second `Ctrl+C`
can still kill a process wedged mid-shutdown.

## Migrations

**There is no migration machinery at M0**, and no migration dependency in
`go.mod`.

Schema migrations arrive in M1, together with the first real domain table. The
reasoning is recorded in [technical-decisions.md](technical-decisions.md),
TD-012.
