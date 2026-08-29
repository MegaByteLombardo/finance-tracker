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
| `HTTP_PORT` | `8080` | Host port published by Compose. Read by `compose.yaml` only, not by the application. |

Under Docker Compose, `HTTP_ADDR` is fixed to `:8080` inside the container and
`DB_PATH` to `/data/finance.db`. Use `HTTP_PORT` to change the port on the host.

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
version, the **effective** SQLite settings, and the address actually bound.

```text
msg="sqlite ready" sqlite.journal_mode=wal sqlite.foreign_keys=true
                   sqlite.busy_timeout_ms=5000 sqlite.synchronous=FULL
```

Those values are read back from SQLite rather than echoed from configuration, so
the line reflects what is actually in force.

There is no per-request logging. The health endpoint logs only when it fails.

## Tests

```sh
gofmt -l .            # must print nothing
go vet ./...
go test ./... -count=1
```

Database tests run against real SQLite in temporary directories rather than a
mock. They verify the PRAGMAs actually in effect, that foreign keys are genuinely
enforced, that those settings hold on **every** connection in the pool, and that
data survives a close and reopen.

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

**Not implemented. Automatic backups are explicitly out of scope for M0.**

The strategy below is the agreed approach for when they are added, and is the
correct manual procedure in the meantime.

**With the application stopped cleanly** — copying the database file is
sufficient. A clean shutdown checkpoints and closes the database properly.

**With the application running** — copying the file by hand is *not* safe. Use
one of:

- SQLite's **Online Backup API**, which produces a consistent snapshot of a live
  database.
- `VACUUM INTO '/path/to/backup.db'`, which writes a consistent, compacted copy
  without stopping the service.

**Do not** copy `finance.db`, `finance.db-wal` and `finance.db-shm` together as a
backup strategy. It is a common suggestion and it is wrong here: `-shm` is shared
memory with no persistent state and is not part of a backup, and a hand-copied
set taken mid-write can be inconsistent. Use one of the two mechanisms above,
which exist precisely to avoid this.

## Not in scope

Automatic backups, CI/CD, cloud deployment, monitoring and alerting, and hot
reload are all outside M0. See [milestones.md](milestones.md).
