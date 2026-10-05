# Operations

How to run, configure, observe and back up Finance Tracker.

Most of this document is about running it locally, which is still how it is
developed. The [Production](#production) section near the end covers the
deployment target added in M6 — one Oracle Cloud instance, reachable only inside
a Tailscale network, with the database on a block volume and Litestream
replicating it off-site. There is still no CI/CD, deliberately.

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
| `FX_BCV_ENABLED` | `true` | Whether to fetch exchange rates from the BCV at all. `false` leaves manual entry and everything else working. |
| `FX_BCV_URL` | `https://www.bcv.org.ve/` | Where the rates are read from. See the note below before pointing this elsewhere. |
| `FX_BCV_TIMEOUT` | `15s` | Bounds one fetch, end to end. |
| `FX_REFRESH_EVERY` | `4h` | How often the refresher **wakes up**, not how often it fetches: at most one successful fetch per Venezuelan calendar day. |

`FX_BCV_URL` is configurable for tests and rehearsals, and one thing about it is
deliberate: the additional TLS trust anchor the real host needs is applied **only**
when this names `www.bcv.org.ve`. Any other address gets ordinary verification.
See [TD-050](technical-decisions.md).

Under Docker Compose, `HTTP_ADDR` is fixed to `:8080` inside the container,
`DB_PATH` to `/data/finance.db` and `BACKUP_DIR` to `/backups`. Use `HTTP_PORT`
to change the port on the host, and `BACKUP_HOST_DIR` to move where snapshots
land.

An invalid value is reported **by name** at startup and the process exits
non-zero. All problems are reported together, so a `.env` with three mistakes
takes one startup attempt to diagnose rather than three.

**The application gained no configuration in M6.** The production-only variables
— `APP_IMAGE`, `LITESTREAM_IMAGE`, `HTTP_BIND`, `DATA_HOST_DIR`,
`LITESTREAM_HOST_DIR`, `LITESTREAM_ENV_FILE`, `COMPOSE_FILE` — are read by
`compose.prod.yaml` alone and never reach the process. The Cloudflare R2
credentials are read by Litestream, from a file the application container does
not load. See [Production](#production) and `.env.example`.

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

The same check is available as a subcommand, which is what the container's
`HEALTHCHECK` runs:

```sh
finance-tracker healthcheck            # exit 0 healthy, exit 1 not
finance-tracker healthcheck --timeout 10s
```

It reads `HTTP_ADDR` — the same variable the server reads — so moving the port
cannot leave the check probing the wrong one, and it requires **both** a `200`
and a body saying `ok`: the status code alone would accept anything answering on
that port. A wildcard address (`:8080`, `0.0.0.0:8080`, `[::]:8080`) is probed on
the loopback, which is where the server is listening in all three cases.

Under Docker, the result shows up without running anything:

```sh
docker compose ps                                           # STATUS says (healthy)
docker inspect --format '{{json .State.Health}}' <container>
```

## Verifying a database file

```sh
finance-tracker verify --file ./backups/finance-20260920T225746Z-shutdown.db
```

```text
database:       ./backups/finance-20260920T225746Z-shutdown.db
size:           131072 bytes
integrity:      ok
schema version: 13
rows:
  account_movements  86
  accounts           4
  …
```

It exits non-zero if the file is not SQLite, fails `PRAGMA integrity_check` — the
full one, not `quick_check` — carries no migration history, or is missing a table
that belonged to the schema version it records.

Three things to know about it:

- **It opens read-only.** Looking at a backup cannot change it, which matters
  because a backup that changed because someone inspected it is no longer the
  thing that was taken.
- **`--file` is a path**, unlike `restore --file`, which takes a bare name inside
  the backup directory. Verify is pointed at files produced elsewhere — a
  Litestream restore under `/tmp`, say.
- **The row counts are the point.** "Something was restored" and "my data was
  restored" are different claims, and an integrity check only supports the first.

Under Docker, with the snapshot directory already mounted:

```sh
docker compose run --rm app verify --file /backups/finance-<stamp>-shutdown.db
```

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

## Exchange rates and the BCV

From M5 the application values everything the user holds in a single base
currency. The rates it uses are **reference rates** — what a source said a
currency was worth on a day — and never the rate a particular transaction
happened at. No transaction carries a rate.

### The external source is never a dependency

**Finance Tracker starts and works with no network at all.** Accounts, the ledger,
balances, manually entered rates and the net worth computed from rates already
stored are all available the moment the listener opens.

The refresher starts *after* the HTTP server is listening and never blocks
startup. A DNS failure, a TLS failure, a timeout, a 500 from the BCV or a change
to its page all produce a log line and nothing else. Migrations are a
precondition for serving; an external rate source is not.

The same applies in the interface: pressing **Actualizar tasas BCV** when the site
is down re-renders the page with a message and HTTP 200. Every stored rate stays
visible and manual entry stays available.

### When rates are fetched

At most **one successful fetch per Venezuelan calendar day**. The refresher wakes
every `FX_REFRESH_EVERY` (four hours by default) and does nothing unless the day's
rates have not been collected yet, so the interval is really the retry cadence for
a day when the source is down — six attempts rather than the twenty-four an hourly
tick would make.

Staleness is measured on `fetched_at`, not on `effective_date`, and deliberately:

- the BCV holds one Fecha Valor across a weekend, so an `effective_date` two days
  old is not evidence of anything being wrong;
- the BCV also publishes **forward** — its page on Friday 2026-09-11 carried a
  Fecha Valor of Tuesday 2026-09-15.

A rule based on `effective_date` would therefore be wrong in both directions at
once.

### Reading the logs

```text
exchange rates refreshed    provider=BCV_OFFICIAL effective_date=2026-09-15 rates=2 skipped=3 duration=412ms
exchange rate refresh failed provider=BCV_OFFICIAL duration=15.0s error="the rate provider could not be reached: ..."
```

`skipped` counts observations for currencies this application does not have. They
are dropped, never created: scraping an external document must not be able to
bring new entities into the database.

**The body of the response is never logged.** It is 150 KB of somebody else's
HTML, and nothing in it is worth keeping.

The one line worth an alert is at `ERROR` level:

```text
the exchange rate provider returned something unexpected  provider=BCV_OFFICIAL error="..."
```

It means the site answered and this application could not understand it, which
almost always means the page changed shape and the parser needs updating.

### When the BCV changes its page

The parser anchors on the element ids of the rate blocks and **verifies that each
block states the currency it is expected to state**, so a rearranged page fails
rather than being read as a dollar rate. That is on purpose: a missing rate is
visible and recoverable, a wrong one is neither.

To diagnose one:

1. Fetch the page by hand and look at the rate section:
   `curl -s https://www.bcv.org.ve/ | grep -o 'id="dolar".\{0,400\}'`
2. Compare it with the fixtures in `internal/fx/testdata/`, which are the real
   markup captured on 2026-09-11.
3. Update `bcvBlocks` and the parsing in `internal/fx/provider_bcv.go`, and add a
   fixture for whatever the new shape is.

**Nothing is urgent while this is broken.** Manual entry covers it, and the stored
rates keep working.

### If the fetch starts failing with a certificate error

`www.bcv.org.ve` serves a broken certificate chain: the intermediate it sends is
not its leaf's issuer. Go does not chase AIA on Linux, so the correct issuer is
embedded in the binary and added as an additional trust anchor **for that one
hostname**. See [TD-050](technical-decisions.md) for the full reasoning and its
boundaries.

If Sectigo rotates that CA, or the BCV moves to a different one, the fetch fails
with `x509: certificate signed by unknown authority`. The fix is to fetch the new
issuer from the leaf's AIA extension and replace
`internal/fx/bcv_issuer.pem`, updating the recorded fingerprint in
`internal/fx/bcv_tls.go` — a test asserts the two agree, so a mismatched
replacement fails the build rather than silently widening trust.

```
openssl s_client -connect www.bcv.org.ve:443 -servername www.bcv.org.ve </dev/null 2>/dev/null \
  | openssl x509 -noout -text | grep -A2 "Authority Information Access"
```

**Do not reach for `InsecureSkipVerify`.** It appears nowhere in this repository
and a test walks the source tree to keep it that way.

### Turning the source off

```
FX_BCV_ENABLED=false
```

Everything else keeps working; the refresh button disappears from the interface
and manual entry is the only way rates arrive.

### Where a rate comes from, and which one wins

Two sources can state different values for the same day, and both are kept.
`MANUAL` beats `BCV_OFFICIAL` **within one effective date and never across
dates** — a manual rate typed on the 3rd does not shadow a publication dated the
10th. Withdrawing a manual override is archiving it, not deleting it; the
published rate underneath applies again.

To see what the application is actually using:

```sql
SELECT base_currency_code, quote_currency_code, effective_date, rate, source, is_active
  FROM exchange_rates
 ORDER BY effective_date DESC, base_currency_code;
```

### The business calendar is Venezuela's

The valuation date, the date a form defaults to, and the day the refresher
considers "today" are all the calendar date in **UTC−04:00**, not UTC. At 21:00 in
Caracas, UTC is already tomorrow. See [TD-051](technical-decisions.md).

## Production

**Status: M6.1.** What exists today is in the repository — `compose.prod.yaml`,
`deploy/litestream.yml`, the hardened image, the two new subcommands. **Nothing
has been provisioned yet**: no instance, no block volume, no tailnet node, no R2
bucket. The bootstrap, upgrade and recovery procedures are written in M6.5 as
`deployment.md` and `disaster-recovery.md`. This section covers the parts that
are decided now and would otherwise be easy to get wrong later.

### The production stack is a separate file

```sh
# on the server, where .env carries COMPOSE_FILE=compose.prod.yaml
docker compose up -d
docker compose ps
docker compose logs -f app
```

`compose.prod.yaml` is standalone rather than an override of `compose.yaml`.
Compose merges a `ports:` list by appending rather than replacing, so running the
two together would publish `0.0.0.0:8080` *alongside* `127.0.0.1:8080` — on a
host with a public IP address. Do not combine them with `-f`.

The check that this is right takes one command, and is worth running after every
change to either file:

```sh
docker compose config | grep -c '0\.0\.0\.0'   # must print 0
ss -lntp | grep 8080                           # must say 127.0.0.1:8080
```

### Cost, and the one component that can produce a bill

The operational target is **USD 0/month**, and every component is sized to stay
inside a free allowance. That is a target, not a guarantee: **Cloudflare R2 is
usage-based and will charge for anything beyond its free tier.** There is no hard
spending cap on R2 — an alert is an alert, not a ceiling.

The free allowance is 10 GB-month of storage, 1,000,000 Class A operations and
10,000,000 Class B. Storage and Class B are not close to being a concern: the
database is a few megabytes and reads only happen during a restore. Class A is
the one to watch, and it is why `sync-interval` is 60s rather than Litestream's
default of 1s — 43,200 cycles a month at worst against 2.59M (TD-060).

**Check these every week for the first month**, then monthly:

| Where | What |
| --- | --- |
| Cloudflare dashboard → R2 → Billable usage | That there is a free-tier bar and it is nowhere near full |
| Cloudflare dashboard → R2 → Metrics | Class A operations for the month |
| Cloudflare dashboard → R2 → Metrics | Class B operations for the month |
| Cloudflare dashboard → R2 → the bucket | Stored bytes and object count — should plateau once retention starts expiring |
| OCI console → Billing → Cost Analysis | That the total is 0.00 and no service has appeared on it |
| OCI console → Limits, Quotas and Usage | Ampere A1 OCPU-hours and GB-hours against 1,500 / 9,000 |

If Cloudflare offers a billing alert on the account, set one at the lowest
threshold it allows and record that it has been set. It will not stop a charge;
it will tell you one is happening, which is the difference between noticing in a
day and noticing in a quarter.

Two charges that are easy to cause by accident, neither of which this deployment
does:

- **Enabling an OCI volume backup policy.** Always Free includes five volume
  backups in total; a daily policy passes that in under a week. Litestream and
  the local snapshots are the backup strategy, and the block volume's own backups
  are deliberately not used.
- **Reserving a public IP and leaving it unattached.** The ephemeral address
  attached to the instance is free; a reserved one sitting unused is not.

### Observability

No Datadog, no Grafana, no uptime service. For one user on one machine:

```sh
docker compose ps                        # both services up, app (healthy)
docker compose logs -f --tail=100 app    # structured JSON in production
docker compose logs -f --tail=100 litestream
curl -fsS http://127.0.0.1:8080/healthz  # from the host, over the loopback
df -h /srv/finance-tracker               # the block volume, not the boot volume
tailscale status                         # the node is online
tailscale serve status                   # the proxy is configured and persistent
```

Health is already on `docker compose ps`, because the image carries a
`HEALTHCHECK` (TD-062). A container that fails it three times in a row shows as
`unhealthy` there without anything polling from outside.

## Not in scope

CI/CD and hot reload remain outside the project's scope. **Cloud deployment is
no longer outside it** — that is M6, above — but automating the deployment is:
proving the infrastructure and building a pipeline for it are two problems, and
doing them together means debugging both at once.

Monitoring and alerting stay out of scope in the platform sense. `docker compose
ps`, the container health check, `/healthz`, `df` and the logs are what one user
on one machine needs; a metrics stack would be more to operate than the thing it
watches.

Scheduled backups are not implemented either — snapshots are taken before
migrations, on shutdown and on request, which covers the failure this project
actually suffered, and Litestream now covers the off-site case continuously; a
cron-style scheduler arrives if a need for one does.

The exchange-rate refresher is **not** a general scheduler and should not become
one: it is one goroutine with one ticker running one job once a day. A second
periodic job would be the moment to reconsider, not before. See
[milestones.md](milestones.md).
