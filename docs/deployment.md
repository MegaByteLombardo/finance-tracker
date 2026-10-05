# Deployment

How Finance Tracker is put into production, from an empty Oracle Cloud tenancy to
a running application holding real data.

**This document is written one phase at a time, as each phase is executed.**
Anything not yet done is marked as such rather than described as if it were. A
runbook that describes a machine nobody has built is a plan, and plans belong in
[milestones.md](milestones.md).

| Phase | Covers | Status |
| --- | --- | --- |
| M6.1 | Production readiness in the repository | Done — [milestones.md](milestones.md) |
| **M6.2** | **Instance, block volume, Docker, data cutover, first boot** | **This document** |
| M6.3 | Tailscale and private access | Not written |
| M6.4 | Cloudflare R2 and Litestream | Not written |
| M6.5 | Restore scripts and `disaster-recovery.md` | Not written |

> **Scope guard for M6.2.** No Tailscale, no `tailscale serve`, no Cloudflare R2,
> no Litestream, no CI/CD, no domain, no reverse proxy. Port 8080 is **never**
> opened to the internet. The only public ingress at any point in this phase is
> `22/tcp` from one address, and it stays open only because M6.3 has not closed
> it yet.

---

## 0. Before you start

**On the local machine** you need a working `ssh`, `scp`, and a current snapshot
of the database (§8 explains how it was produced).

**In the OCI console** you need the tenancy's home region, and to know that an
Always Free A1 instance is what you are asking for.

Record your current public address — the temporary SSH rule is scoped to it, and
nothing else:

```sh
curl -s https://checkip.amazonaws.com
```

If your ISP changes it, you will have to update the security list rule. That is
a feature: a rule that accepts the whole internet does not need updating, which
is exactly what makes it the wrong rule.

---

## 1. Account and home region

Block volumes in Always Free exist **only in the tenancy's home region**, and an
instance can only attach a volume in its own availability domain. Get this wrong
and the fix is rebuilding both.

In the console: **Profile → Tenancy**. The *Home Region* is on that page. Every
resource in this document goes there.

Then **Governance → Limits, Quotas and Usage**, service *Compute*, and check the
Ampere A1 limits. This is also where you confirm afterwards that the running
instance is inside the free allowance.

> **The Always Free Ampere allowance was halved in 2026** — 1,500 OCPU-hours and
> 9,000 GB-hours a month, enforced from 18 August — which is **2 OCPU and 12 GB
> running continuously**, not the 4 and 24 that most older guides still quote.
> This deployment asks for **1 OCPU and 6 GB**: half the allowance, so the margin
> survives another change.

---

## 2. Create the instance

**Compute → Instances → Create instance**, in the home region.

| Field | Value |
| --- | --- |
| Image | Canonical Ubuntu (current LTS), **aarch64** build |
| Shape | `VM.Standard.A1.Flex` |
| OCPUs | **1** |
| Memory | **6 GB** |
| Boot volume | **50 GB** |
| SSH key | paste `~/.ssh/id_ed25519.pub` |
| Public IPv4 | **Ephemeral**, assigned |

Two things to check before clicking Create:

- The shape panel shows an **"Always Free eligible"** label. If it does not, you
  are about to create a billable instance.
- The boot volume is 50 GB. Minimum is 47; the Always Free pool is **200 GB for
  boot and block volumes combined**, and this deployment uses 50 + 50 = 100.

Keep the **ephemeral** public IP. A reserved public IP left unattached is
billable; an ephemeral one attached to a running instance is not.

### If you get "Out of host capacity"

**Stop.** Do not let the console talk you into a different shape, and do not
upgrade the account.

A1 capacity in Always Free is genuinely scarce and the error is transient. Try
another availability domain in the same region, or try again later. A paid shape
would work immediately, which is exactly why it is the wrong thing to click while
annoyed.

---

## 3. Temporary SSH

**Networking → Virtual Cloud Networks → your VCN → Security Lists → Default**.

Add exactly one ingress rule, and nothing else:

| Field | Value |
| --- | --- |
| Source CIDR | `<your public IP>/32` |
| Protocol | TCP |
| Destination port | 22 |

**Do not add a rule for 8080, 80 or 443.** None is needed now and none will be
needed later: M6.3 reaches the application through Tailscale, and the application
port is published on the host loopback where no VCN rule can route to it.

```sh
ssh ubuntu@<public-ip>
uname -m        # must print: aarch64
```

`aarch64` is the first acceptance criterion of this milestone and the first thing
to check — everything after it assumes the architecture is what was ordered.

Ubuntu images on OCI ship host firewall rules of their own in
`/etc/iptables/rules.v4`. **Leave them alone**, and do not install `ufw`. Three
filtering layers where one governs is how a rule ends up protecting nothing while
appearing to protect something; see TD-058.

---

## 4. Docker

```sh
sudo apt-get update && sudo apt-get upgrade -y
sudo install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg \
  | sudo tee /etc/apt/keyrings/docker.asc > /dev/null
sudo chmod a+r /etc/apt/keyrings/docker.asc
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] \
https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo $VERSION_CODENAME) stable" \
  | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null
sudo apt-get update
sudo apt-get install -y docker-ce docker-ce-cli containerd.io \
                        docker-buildx-plugin docker-compose-plugin

sudo usermod -aG docker "$USER"
```

Log out and back in, then confirm:

```sh
docker version --format '{{.Server.Arch}}'   # arm64
docker compose version                       # v2.24 or newer
```

**v2.24 is a real minimum, not a preference.** `compose.prod.yaml` uses the long
form of `env_file` with `required: false`, which older versions do not
understand — and that attribute is what allows the application to start in this
phase, before any Cloudflare credentials exist. See §10.

---

## 5. Create and attach the block volume

**Storage → Block Volumes → Create block volume**, in the **same availability
domain as the instance**.

| Field | Value |
| --- | --- |
| Size | **50 GB** |
| Performance | **Balanced** (the Always Free default) |
| Backup policy | **None** |

> Always Free includes **five volume backups in total**. A daily policy passes
> that in under a week and starts billing. Backups here are the application's
> `VACUUM INTO` snapshots and, from M6.4, Litestream — not the block volume
> service.

Then **Attached instances → Attach to instance**:

| Field | Value |
| --- | --- |
| Attachment type | **Paravirtualized** |
| Access | **Read/write** |
| Shareable | **No** |
| Device path | **`/dev/oracleoci/oraclevdb`** |

Paravirtualized means nothing has to be done with `iscsiadm` on the instance, and
nothing has to be redone after a reboot. Specifying the **device path** is what
makes the next step unambiguous: `/dev/oracleoci/oraclevdb` is a symlink Oracle
maintains to *this* volume, so identifying the right disk does not depend on
reading `/dev/sdX` and hoping.

---

## 6. Identification gate — STOP

**Nothing destructive runs until this output has been read.** `mkfs` on the wrong
device destroys the boot disk, and the only thing standing between those two
outcomes is looking first.

```sh
lsblk -f -o NAME,SIZE,FSTYPE,LABEL,UUID,MOUNTPOINT
ls -l /dev/oracleoci/
readlink -f /dev/oracleoci/oraclevdb
lsblk -no FSTYPE "$(readlink -f /dev/oracleoci/oraclevdb)"
```

Confirm all five, out loud:

1. **The boot disk** — the one with a mountpoint of `/`, around 50 G, already
   formatted and carrying partitions.
2. **The new volume** — a separate device, **50 G**, **no mountpoint**.
3. **The device path** — `readlink -f /dev/oracleoci/oraclevdb` resolves to that
   same device and not to the boot disk.
4. **The size** — 50 G, matching what was created in §5.
5. **The current filesystem — must be EMPTY.** If `lsblk -no FSTYPE` prints
   anything at all, the device already holds a filesystem. **Stop and work out
   why**; it is not the volume you just created.

Only when all five agree:

```sh
sudo mkfs.ext4 -L ftdata /dev/oracleoci/oraclevdb
```

ext4 rather than XFS: it is the default everywhere, `resize2fs` can grow it
online if the volume is ever expanded, and `e2fsck` is the tool most likely to be
on a rescue system years from now. The filesystem label `ftdata` is a second,
human-readable way to recognise the volume later.

---

## 7. Mount it, by UUID

```sh
sudo blkid /dev/oracleoci/oraclevdb
```

Take the `UUID=` value, then:

```sh
sudo mkdir -p /srv/finance-tracker
echo "UUID=<uuid>  /srv/finance-tracker  ext4  defaults,noatime,_netdev,nofail  0 2" \
  | sudo tee -a /etc/fstab
sudo systemctl daemon-reload
sudo mount -a
findmnt /srv/finance-tracker
```

Every option in that line is there for a reason:

- **`UUID=` rather than the device path.** Oracle documents both forms, and
  documents that "the order in which volumes are attached is non-deterministic,
  so it can change with each reboot". The UUID belongs to the *filesystem*, so it
  survives being attached to a different instance in a different slot — which is
  exactly Case 1 of disaster recovery. The consistent device path did its job in
  §6, identifying the disk; it is not needed to mount it.
- **`nofail`** — without it, a volume that is missing or slow stops the instance
  reaching multi-user, and SSH with it. On a machine whose recovery plan begins
  with "ssh in", that is not a risk worth taking. Oracle documents it as
  required.
- **`_netdev`** — Oracle documents it for block volumes generally, not only for
  iSCSI. It is harmless on a paravirtualized attachment and matching the vendor's
  own line beats being clever.
- **`noatime`** — the one addition of our own. It stops a read updating an inode,
  which on a database file is pure write amplification.

### Reboot test — do it now, not later

```sh
sudo reboot
# wait, then:
ssh ubuntu@<public-ip>
findmnt /srv/finance-tracker
lsblk -f | grep ftdata
```

A mount that works until the next reboot is a mount that fails at the worst
possible moment. Testing it on an empty filesystem costs two minutes; testing it
by accident, later, costs the afternoon.

---

## 8. Directories and ownership

Both containers run as **uid 65532** — that is the `nonroot` user in the
distroless image, and the application has no way to become anything else.

```sh
sudo mkdir -p /srv/finance-tracker/{data,backups,litestream}
sudo chown -R 65532:65532 /srv/finance-tracker
sudo chmod 750 /srv/finance-tracker /srv/finance-tracker/{data,backups,litestream}
```

`750` means the owner and its group, and nobody else on the machine. That also
means your login user cannot `ls` the directory — and being able to do exactly
that is one of the reasons the data is a bind mount rather than a named volume.
Give yourself group access rather than loosening the mode:

```sh
getent group 65532 || sudo groupadd -g 65532 ftapp
sudo usermod -aG ftapp "$USER"
# log out and back in, then:
ls -la /srv/finance-tracker/data
```

The `litestream/` directory is created now and stays empty until M6.4. Creating
it here keeps the layout in one place.

---

## 9. Code and image

```sh
sudo install -d -o "$USER" -g "$USER" /opt/finance-tracker
git clone https://github.com/MegaByteLombardo/finance-tracker.git /opt/finance-tracker
cd /opt/finance-tracker

docker build -t "finance-tracker:$(git rev-parse --short HEAD)" .
docker images finance-tracker
```

The code lives on the **boot volume** and the data on the **block volume**,
deliberately. The boot volume is disposable — a lost instance is a `git clone`
away from being replaced. Nothing on it is irreplaceable, and nothing
irreplaceable is on it.

The image is tagged with the commit, never `latest`. Rollback is setting
`APP_IMAGE` back to the previous tag, which only means something if the tag is
specific. **Keep the last two images**; never run `docker image prune -a` here.

Then the environment — no secrets in it, and none in this phase at all:

```sh
cat > /opt/finance-tracker/.env <<EOF
COMPOSE_FILE=compose.prod.yaml
APP_ENV=production
LOG_LEVEL=info
HTTP_BIND=127.0.0.1
HTTP_PORT=8080
APP_IMAGE=finance-tracker:$(git rev-parse --short HEAD)
LITESTREAM_IMAGE=litestream/litestream:0.5.17@sha256:4b02b9859a6b6b4087d8b8944e15f7e984bd7957cba322bbeee38b0e27b9656a
DATA_HOST_DIR=/srv/finance-tracker/data
BACKUP_HOST_DIR=/srv/finance-tracker/backups
LITESTREAM_HOST_DIR=/srv/finance-tracker/litestream
EOF
chmod 640 /opt/finance-tracker/.env
```

`COMPOSE_FILE` is what makes a bare `docker compose up -d` resolve to the
production stack. **Never run `-f compose.yaml -f compose.prod.yaml`**: Compose
appends `ports:` across files rather than replacing them, so combining the two
would publish `0.0.0.0:8080` alongside the loopback — silently, on a host with a
public address.

Confirm before going further:

```sh
docker compose config | grep -c '0\.0\.0\.0'     # must print 0
docker compose config | grep -A4 'ports:'        # host_ip must be 127.0.0.1
```

---

## 10. The data cutover

Production does not start empty. The local instance holds real records, and this
is how they move — **without ever copying a live `finance.db`, and without
touching `-wal` or `-shm`**. Those three files copied by hand, mid-write, can be
mutually inconsistent; that is the whole reason `VACUUM INTO` exists and why the
application already does it.

### 10.1 Produce the snapshot (local) — DONE

Taken through the mechanism the application already has — the manual backup on
**Settings → Data**, which calls `backup.Service.CreateManual` and runs
`VACUUM INTO`. No new backup layer was invented for this.

```sh
curl -X POST http://127.0.0.1:8080/settings/data/backup   # or click the button
```

### 10.2 Verify it (local) — DONE

```sh
docker compose build app          # the image must be at the M6.1 head or newer;
                                  # `verify` does not exist in an older one
docker compose run --rm --no-deps app verify --file /backups/<snapshot>
sha256sum backups/<snapshot>
```

Recorded for this cutover:

| Property | Value |
| --- | --- |
| Snapshot | `finance-20261002T014216Z-manual.db` |
| Size | 143,360 bytes |
| sha256 | `c60db945c32eeae62edf5d04057ad86e3c0f33d780fc9ee3a7cae11409ad2962` |
| integrity | `ok` |
| schema version | `13` |
| transactions | **103** |
| account_movements | **121** |
| accounts / categories / currencies | 5 / 14 / 3 |
| exchange_rates / payment_methods / app_settings | 9 / 4 / 1 |

The same `verify` was also run against the **live** database, and every count
matched. That is the check that makes the snapshot a cutover source rather than
just a valid file: `integrity: ok` proves it is a sound database, and the
matching counts prove it is *this* database.

> On Windows, Git Bash rewrites a container path like `/backups/x.db` into
> `C:/Program Files/Git/backups/x.db`. Prefix the command with
> `MSYS_NO_PATHCONV=1`.

### 10.3 Transfer

```sh
scp backups/finance-20261002T014216Z-manual.db \
    ubuntu@<public-ip>:/tmp/finance-cutover.db
```

Over the temporary SSH channel from §3 — the only channel that exists in this
phase.

### 10.4 Verify again, on the server, with the production binary

```sh
cd /opt/finance-tracker
sha256sum /tmp/finance-cutover.db        # must equal the local sha256 exactly

docker run --rm -v /tmp:/restore:ro "$APP_IMAGE" verify --file /restore/finance-cutover.db
```

Two different questions, both of which have to be answered:

- **The checksum** proves the bytes that arrived are the bytes that left.
- **`verify`** proves the file is a sound Finance Tracker database *as read by the
  binary that is about to serve it*, on a different CPU architecture. A database
  that was fine on amd64 and is not fine on aarch64 is the kind of problem to
  find now, with the production directory still empty.

**The schema version and every row count must match §10.2.** If they do not,
stop: something was lost between the two machines and continuing would make that
permanent.

### 10.5 Install it

Only after both checks pass:

```sh
sudo install -o 65532 -g 65532 -m 640 /tmp/finance-cutover.db \
     /srv/finance-tracker/data/finance.db
ls -l /srv/finance-tracker/data/
rm /tmp/finance-cutover.db
```

`install` rather than `cp` + `chown` + `chmod`: one command, no window in which
the file exists with the wrong ownership. `640` is enough — the application
writes it as uid 65532, and nothing else has any business reading it.

There is no `-wal` and no `-shm`, and that is correct. A `VACUUM INTO` output is
a complete, self-contained database; SQLite recreates both sidecars on the first
open.

---

## 11. First boot — application only

```sh
cd /opt/finance-tracker
docker compose up -d --no-deps app
docker compose ps
docker compose logs -f app
```

`--no-deps` is what keeps Litestream out of this phase. `compose.prod.yaml`
declares `app` as depending on `litestream` — correct for production, because
dependencies start first and stop last — but there is no Cloudflare account yet,
so starting it would be starting a service with nothing to replicate to.

This works only because the `env_file` for the credentials is declared
`required: false`. **Do not create a placeholder credentials file to work around
it.** A fake secret on disk is worse than an absent one: it is indistinguishable
from a real one that has stopped working.

The startup log should show, in this order:

```text
"msg":"database opened","sqlite":{"journal_mode":"wal","foreign_keys":true,
                                   "busy_timeout_ms":5000,"synchronous":"FULL"}
"msg":"http server listening","addr":"[::]:8080"
```

**There should be no `migrations applied` line.** The snapshot is already at
schema 13, which is the latest, so there is nothing to apply — and because
nothing is pending, no pre-migration snapshot is taken either. A `migrations
applied` line here would mean the snapshot was older than the image, which is
supported but is not what was expected.

`addr` reads `[::]:8080` rather than `0.0.0.0:8080`. That is the listener inside
the container's own network namespace and says nothing about exposure. What
governs exposure is `docker compose ps`, which must show
`127.0.0.1:8080->8080/tcp`.

---

## 12. Verification — M6.2 is not done until all of this passes

```sh
# architecture
uname -m                                     # aarch64

# the volume
findmnt /srv/finance-tracker
lsblk -f -o NAME,SIZE,FSTYPE,LABEL,UUID,MOUNTPOINT
df -h /srv/finance-tracker

# the database is physically where it should be
ls -l /srv/finance-tracker/data/             # finance.db, -wal, -shm

# the application
docker compose ps                            # Up (healthy), 127.0.0.1:8080->8080/tcp
curl -fsS http://127.0.0.1:8080/healthz      # {"status":"ok"}
docker compose logs app | grep journal_mode  # wal / true / 5000 / FULL

# the data is the data
docker compose run --rm --no-deps app verify --file /data/finance.db
#   → schema 13, transactions 103, account_movements 121

# nothing is exposed
ss -lntp | grep 8080                         # 127.0.0.1:8080 only, never 0.0.0.0
nmap -Pn -p- <public-ip>                     # run from the LOCAL machine: 22 only
```

Then the two persistence tests, which are the point of the whole phase:

```sh
# containers come and go; data does not
docker compose down
ls -l /srv/finance-tracker/data/             # finance.db still there
ls -l /srv/finance-tracker/backups/          # a shutdown snapshot was taken
docker compose up -d --no-deps app
docker compose run --rm --no-deps app verify --file /data/finance.db   # counts unchanged

# the machine reboots; data does not move
sudo reboot
# wait, then:
findmnt /srv/finance-tracker                 # still mounted
docker compose ps                            # restarted by restart: unless-stopped
curl -fsS http://127.0.0.1:8080/healthz
docker compose run --rm --no-deps app verify --file /data/finance.db   # counts unchanged
```

### Cost check

**Governance → Limits, Quotas and Usage** → Compute → Ampere A1: the instance
should account for roughly 730 OCPU-hours and 4,380 GB-hours a month, against
1,500 and 9,000.

**Billing → Cost Analysis**: the total must be **0.00**, with no service listed.

Block storage in use: 50 GB boot + 50 GB block = **100 GB of the 200 GB** free
allowance.

---

## 13. Leaving it here

At the end of M6.2 the application is running on OCI with real data, and is
reachable **only** by SSH from one address. It is not reachable by a browser from
anywhere, which is correct and temporary: M6.3 adds Tailscale, and closing the
public SSH rule is the last step of that phase, not this one.

Until then, to look at it from your own machine, forward the port over the SSH
channel that already exists rather than opening anything:

```sh
ssh -L 8080:127.0.0.1:8080 ubuntu@<public-ip>
# then open http://127.0.0.1:8080 locally
```

That tunnel exists only while the SSH session does, and adds no listening socket
on the server.
