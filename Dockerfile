# syntax=docker/dockerfile:1

# --- build -------------------------------------------------------------------
FROM golang:1.27-trixie AS build

WORKDIR /src

# Dependencies first, so the download layer survives source-only changes.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# CGO_ENABLED=0 is not a workaround here. modernc.org/sqlite is a pure-Go SQLite
# implementation, so disabling cgo costs nothing and yields a genuinely static
# binary that needs no libc at runtime -- which is what makes the distroless
# static base below possible.
ENV CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/finance-tracker ./cmd/web

# A Docker named volume is initialised from whatever the image already has at
# that path, ownership included. Shipping an empty /data owned by the runtime
# user is what allows the non-root process to write to the volume; without it
# the volume would be created root-owned and every write would fail.
RUN mkdir -p /out/data /out/backups

# --- runtime -----------------------------------------------------------------
# distroless/static: no shell, no package manager, non-root by default. The
# static binary needs nothing else.
FROM gcr.io/distroless/static-debian13:nonroot

# 65532 is the uid of the "nonroot" user in the distroless images. It is spelled
# numerically because COPY --chown resolves names against the target image.
COPY --from=build /out/finance-tracker /usr/local/bin/finance-tracker
COPY --from=build --chown=65532:65532 /out/data /data
# /backups is normally a bind mount from the host, which shadows this. It is
# shipped owned by the runtime user anyway so that mounting a named volume there
# instead also works: a volume inherits the image directory's ownership, and one
# created over a path the image does not have would be root-owned and unwritable.
COPY --from=build --chown=65532:65532 /out/backups /backups
COPY --from=build /src/web /web

USER 65532:65532
WORKDIR /

ENV HTTP_ADDR=":8080" \
    DB_PATH="/data/finance.db" \
    BACKUP_DIR="/backups"

EXPOSE 8080

# No HEALTHCHECK: distroless has no shell and no curl, so the usual form does
# not apply. `docker compose ps` plus a request to /healthz covers it for now.
ENTRYPOINT ["/usr/local/bin/finance-tracker"]
