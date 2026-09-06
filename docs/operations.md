# Operations

How to run, configure, observe and back up Finance Tracker locally. Deployment
is local only; there is no cloud target and no CI/CD.

## Running with Go

```sh
go run ./cmd/web
```

Every setting has a working default, so no configuration is required. The
database is created at `./data/finance.db` on first start, together with its
directory. `data/` is git-ignored.

Stop with `Ctrl+C`.

## Running with Docker Compose

```sh
docker compose up -d --build
docker compose logs -f app
docker compose down
```

The container runs as a non-root user (uid 65532) on a distroless image with no
shell and no package manager. Because there is no shell, `docker compose exec`
cannot be used to poke around inside it; inspect from the host instead:

```sh
docker inspect $(docker compose ps -q app) --format '{{.Config.User}}'
```

## Configuration

Configuration comes from environment variables. Copy `.env.example` to `.env` to
change anything — Docker Compose reads `.env` directly, so no dotenv library is
involved. `.env` is git-ignored and must never be committed.

| Variable | Default | Meaning |
| --- | --- | --- |
| `APP_ENV` | `development` | `development` or `production`. Selects text vs. JSON log output. |
| `HTTP_ADDR` | `:8080` | Listen address as `host:port`. An empty host means all interfaces. |
| `DB_PATH` | `./data/finance.db` | SQLite file. Its parent directory is created at startup. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `BACKUP_DIR` | `./backups` | Where snapshots are written. Under Compose this is `/backups`, a bind mount to the host. |
| `BACKUP_RETENTION` | `10` | How many **shutdown** snapshots to keep. `0` keeps every one. Other kinds are never pruned. |
| `HTTP_PORT` | `8080` | Host port published by Compose. Read by `compose.yaml` only, not by the application. |
| `BACKUP_HOST_DIR` | `./backups` | Host side of the backups bind mount. Read by `compose.yaml` only. |

Under Docker Compose, `HTTP_ADDR` is fixed to `:8080` inside the container,
`DB_PATH` to `/data/finance.db` and `BACKUP_DIR` to `/backups`. Use `HTTP_PORT`
to change the port on the host, and `BACKUP_HOST_DIR` to move where snapshots
land.

An invalid value is reported **by name** at startup and the process exits
non-zero. All problems are reported together, so a `.env` with three mistakes
takes one startup attempt to diagnose rather than three.

## Health check

```sh
curl -i http://localhost:8080/healthz
```

| Response | Meaning |
| --- | --- |
| `200` `{"status":"ok"}` | The process is alive and SQLite answered. |
| `503` `{"status":"unavailable"}` | The process is alive but the database probe failed. The underlying error is logged. |

The database probe has its own timeout, so the endpoint does not hang when it
queues behind another query on the single pooled connection.

## Where the database lives

**Locally:** `./data/finance.db`, or wherever `DB_PATH` points.

**Under Docker:** `/data/finance.db`, on a named volume called `finance_data`.
The database is never in the container's writable layer.

| Command | Effect on your data |
| --- | --- |
| `docker compose down` | Containers and network removed, **volume kept**. Data survives, and the next `up` reattaches the same volume. |
| `docker compose up` | The existing volume is reused. Data is still there. |
| `docker compose down -v` | **Destroys the volume and the database. Irreversible.** |

To confirm the volume was reused rather than recreated, compare its identity
across a restart:

```sh
docker volume inspect finance-tracker_finance_data --format '{{.Name}} {{.CreatedAt}}'
```

An unchanged `CreatedAt` means the same volume.

SQLite runs in WAL mode, so alongside `finance.db` you may see `finance.db-wal`
and `finance.db-shm`. They are SQLite's own working files. Do not manage them by
hand.

## Graceful shutdown

`SIGINT` (`Ctrl+C`) and `SIGTERM` (what `docker compose stop` sends) both trigger
an ordered shutdown: stop accepting connections, drain in-flight requests under
a bounded timeout, wait for the serve loop to return, then close the database
last. The process exits `0`.

A second `Ctrl+C` during shutdown falls back to default signal handling and kills
the process, so a wedged shutdown is still interruptible.

Expected log sequence:

```text
shutdown signal received
http server stopped
database closed
```

## Logging

Structured logging via `log/slog`, written to stdout.

- `APP_ENV=development` → human-readable text
- `APP_ENV=production` → JSON
- `LOG_LEVEL=debug` additionally includes source locations

Logged at startup: the environment, the database path, the log level, the Go
version, the **effective** SQLite settings, what migrations did, and the address
actually bound.

```text
msg="starting finance-tracker" env=development db_path=/data/finance.db …
msg="database opened" sqlite.journal_mode=wal sqlite.foreign_keys=true
                      sqlite.busy_timeout_ms=5000 sqlite.synchronous=FULL
msg="migrations applied" count=3 version=3        # or: "database schema already current"
msg="http server listening" addr=[::]:8080
```

The SQLite values are read back from the database rather than echoed from
configuration, so the line reflects what is actually in force.

Those four lines are the whole startup output at info level. There is no
per-request logging, and the health endpoint logs only when it fails.

## Migrations

Migrations are embedded in the binary and applied automatically at startup,
before the process listens. Nothing has to be run by hand, and there is no goose
CLI in the image.

Applying them on every start is safe: goose records applied versions, so a
current database is a no-op and says so.

**A migration failure ends the process** — the server never starts, rather than
serving requests against a half-migrated schema. The error names the migration
that failed and how many were applied before it. SQLite has transactional DDL, so
the failed migration itself rolled back and the schema sits at the last
consistent version; fix it and restart.

**Rollbacks are never run automatically**, and the binary offers no way to
trigger one. To inspect the current state:

```sh
sqlite3 ./data/finance.db "SELECT version_id, is_applied, tstamp
                           FROM goose_db_version ORDER BY version_id;"
```

## Inspecting the data

The database is one file, so the sqlite3 CLI is the quickest way in:

```sh
sqlite3 ./data/finance.db ".schema currencies"
sqlite3 ./data/finance.db "SELECT * FROM currencies;"
sqlite3 ./data/finance.db "SELECT id, name, account_type, currency_code FROM accounts;"
```

Timestamps are stored as readable RFC 3339 UTC text, which is much of why that
format was chosen.

**Three columns are read-only in practice:** `currencies.code`,
`currencies.minor_unit` and `accounts.currency_code`. No application code changes
them, and no database constraint stops you from doing so by hand — but changing a
minor unit would silently reinterpret every amount stored in that currency. See
[data-model.md](data-model.md).

## Tests

```sh
gofmt -l .            # must print nothing
go vet ./...
go test ./... -count=1
```

Tests run against real SQLite in temporary directories rather than a mock. They
verify the PRAGMAs actually in effect, that foreign keys are genuinely enforced,
that those settings hold on **every** connection in the pool, that data survives
a close and reopen, that every schema constraint rejects what it should, and that
migrations are idempotent across restarts.

Tests deliberately do not assert on the presence of `-wal` or `-shm` files:
their lifecycle depends on checkpointing and clean shutdown, and asserting on
them would only produce flakes.

### Race detector

The race detector needs a C toolchain, which Windows does not have by default.
Run it in the build container instead:

```sh
docker run --rm -v "${PWD}:/src" -w /src golang:1.27-trixie go test -race ./...
```

On Windows PowerShell, pass the path in native form:

```powershell
docker run --rm -v "${PWD}:/src" -w /src golang:1.27-trixie go test -race ./...
```

## Backups

**Implemented in M3.** They were out of scope for M0, which was reasonable while
the database was empty, and stopped being reasonable the moment M2 shipped a
working ledger. The volume holding that ledger was then destroyed, with no second
copy. This section is what came of that.

### Where they live, and why not in the volume

```
operational database   finance_data:/data/finance.db    named volume
backups                ./backups:/backups               bind mount to the host
```

**Backups must not live in a Docker volume.** `docker compose down -v` destroys
every volume in the project, and `docker volume prune` removes any volume whose
container no longer exists. A backup kept in one dies with the database it exists
to protect. Putting them on the host takes them out of Docker's lifecycle
entirely.

`backups/.gitkeep` is tracked so the directory exists in any checkout. The
compose file mounts it with `create_host_path: false`, so a missing directory
stops the container rather than being silently created as root — which the
non-root process could not then write to.

`BACKUP_HOST_DIR` moves the host side elsewhere if you need it to.

**On a Linux host**, a bind mount keeps the host's ownership, so `./backups` may
need `chown 65532` before the container can write to it. On Windows, Docker
Desktop presents bind mounts as world-writable and nothing is needed. This was
verified with a real container, not assumed.

### When snapshots are taken

| Trigger | When | Pruned? |
| --- | --- | --- |
| **Pre-migration** | Startup, when migrations are pending on a database that already has a schema | Never |
| **Shutdown** | After HTTP drains, before the database closes | Yes — last 10 |
| **Manual** | On request, from the application | Never |
| **Pre-restore** | Automatically, before a restore replaces anything | Never |

No pre-migration snapshot is taken of a brand-new empty database: there is
nothing in it worth keeping. If one *is* needed and cannot be taken, **startup
fails** — being unable to snapshot a database you are about to alter is precisely
the moment not to proceed.

The shutdown snapshot is the one that would have prevented the original data
loss: `docker compose down` sends `SIGTERM` and waits before destroying anything.
`stop_grace_period` is 30s so that draining and snapshotting both fit; past it
Docker sends `SIGKILL` and the snapshot is abandoned, leaving a `.partial` file
that is never mistaken for a backup.

If a shutdown snapshot fails, the failure is logged at `ERROR` and the process
exits non-zero — but the database still closes cleanly. An unclean close would
turn a missing snapshot into a damaged database.

### Retention

`BACKUP_RETENTION` (default 10) applies **only to shutdown snapshots**. Manual,
pre-migration and pre-restore snapshots are never removed automatically: each was
taken because someone or something decided it should be. `BACKUP_RETENTION=0`
disables pruning entirely.

### Filenames

```
finance-20260905T183045Z-manual.db
finance-20260905T183045Z-shutdown.db
finance-20260905T183045Z-premigration.db
finance-20260905T183045Z-prerestore.db
```

UTC, no colons — the primary development host is Windows. Two snapshots in the
same second get an incremental suffix (`…-manual-2.db`). Nothing is ever
overwritten.

### How a snapshot is taken

`VACUUM INTO`, against the live database. It produces a consistent, compacted
copy without stopping the service, and the result stands alone — no `-wal`, no
`-shm`.

**Do not** copy `finance.db`, `finance.db-wal` and `finance.db-shm` together as a
backup strategy. It is a common suggestion and it is wrong here: `-shm` holds no
persistent state, and a hand-copied set taken mid-write can be inconsistent.

Every snapshot is validated with `PRAGMA integrity_check` and a schema check
before it is given a real name. One that fails is deleted rather than kept —
a file that looks recoverable and is not is worse than no file, because it is
discovered at the worst possible moment.

## Restoring

Restore is **offline**. Replacing the database while the server is serving would
leave handlers reading a file that no longer exists, so there is no button for it
in the web interface.

```sh
docker compose stop
docker compose run --rm app restore --file finance-20260905T183045Z-manual.db
docker compose up -d
```

`--file` takes a **filename**, not a path — anything with a directory component,
an absolute path or a drive letter is refused.

What it does, in order:

1. Validates the snapshot. A restore that cannot go ahead leaves everything as it
   was.
2. Takes a `prerestore` snapshot of the database it is about to replace, so the
   restore itself can be undone.
3. Stages the copy beside the target, moves the live database and its journal
   files aside as a group, moves the copy into place, and discards what it moved.
   A failure at any point puts the original back.
4. **Does not migrate.** The next ordinary start does that, which is what lets a
   snapshot from an older schema be brought forward.

Restoring onto a machine where the database does not exist at all — the shape of
a real recovery — works the same way.

### Rehearsing recovery

Worth doing once, on a throwaway project, so the procedure is familiar before it
is needed:

```sh
export BACKUP_HOST_DIR=/tmp/dr-backups HTTP_PORT=18110
mkdir -p "$BACKUP_HOST_DIR"

docker compose -p financedr up -d          # create some data through the UI
docker compose -p financedr stop           # leaves a shutdown snapshot on the host
docker compose -p financedr down -v        # destroys the volume
docker compose -p financedr run --rm app restore --file <snapshot>
docker compose -p financedr up -d          # verify everything is back

docker compose -p financedr down -v        # clean up
```

Using a separate project name keeps the real volume and the real backups out of
it.

### Rolling back a migration

There is none for M3's migrations: they are forward-only, because a payment
method created after the upgrade cannot be represented by the schema before it,
and a `Down` would have to discard that. The supported path is the snapshot taken
automatically before they ran:

```sh
docker compose stop
docker compose run --rm app restore --file finance-<stamp>-premigration.db
docker compose up -d
```

## Not in scope

CI/CD, cloud deployment, monitoring and alerting, and hot reload remain outside
the project's scope. Scheduled backups are not implemented either — snapshots are
taken before migrations, on shutdown and on request, which covers the failure
this project actually suffered; a cron-style scheduler arrives if a need for one
does. See [milestones.md](milestones.md).
