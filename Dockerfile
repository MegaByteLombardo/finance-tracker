# syntax=docker/dockerfile:1

# --- build -------------------------------------------------------------------
#
# Both base images are pinned by digest, not by tag. A tag moves; "the image we
# shipped in October" has to mean one thing when a rollback needs it to. The
# digests are multi-architecture indexes, so the same line works on amd64 and
# arm64.
#
# To update either one:
#
#   docker buildx imagetools inspect golang:1.27-trixie
#   docker buildx imagetools inspect gcr.io/distroless/static-debian13:nonroot
#
# and copy the top-level Digest. Do not copy a per-platform manifest digest --
# that would pin the image to one architecture and break the other.
#
# --platform=$BUILDPLATFORM keeps the toolchain running natively and
# cross-compiles instead of emulating it. That is free here: the binary is pure
# Go with cgo disabled, so building linux/arm64 from a Windows or amd64 machine
# costs nothing and takes seconds rather than minutes under QEMU.
FROM --platform=$BUILDPLATFORM golang:1.27-trixie@sha256:3b77fc618ec235a1ab412de7737f120dd507c57e8d87de4cbb7994fb94275ed5 AS build

# Supplied by BuildKit from the --platform being built for.
ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

# Dependencies first, so the download layer survives source-only changes.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# CGO_ENABLED=0 is not a workaround here. modernc.org/sqlite is a pure-Go SQLite
# implementation, so disabling cgo costs nothing and yields a genuinely static
# binary that needs no libc at runtime -- which is what makes the distroless
# static base below possible, and what makes cross-compiling above free.
ENV CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/finance-tracker ./cmd/web

# A mount point has to exist in the image, and its ownership is what a Docker
# named volume inherits when one is created over it. Shipping /data and /backups
# empty and owned by the runtime user is what allows the non-root process to
# write to them; without it a fresh volume would be root-owned and every write
# would fail.
#
# In production both are bind mounts from the OCI Block Volume instead, and the
# host directories carry the same ownership -- see docs/deployment.md.
RUN mkdir -p /out/data /out/backups

# --- runtime -----------------------------------------------------------------
# distroless/static: no shell, no package manager, non-root by default. The
# static binary needs nothing else.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3

# 65532 is the uid of the "nonroot" user in the distroless images. It is spelled
# numerically because COPY --chown resolves names against the target image.
COPY --from=build /out/finance-tracker /usr/local/bin/finance-tracker
COPY --from=build --chown=65532:65532 /out/data /data
# /backups is normally a bind mount from the host, which shadows this. It is
# shipped owned by the runtime user anyway so that mounting a named volume there
# instead also works: a volume inherits the image directory's ownership, and one
# created over a path the image does not have would be root-owned and unwritable.
COPY --from=build --chown=65532:65532 /out/backups /backups

# The templates and static assets are NOT copied. They are compiled into the
# binary by web/embed.go (`//go:embed templates static`), and web.DefaultConfig
# serves them from there. An earlier version of this file copied /src/web into
# the image as well; nothing ever read it, and leaving it in suggested the
# binary loads its assets from disk, which it does not.

USER 65532:65532
WORKDIR /

ENV HTTP_ADDR=":8080" \
    DB_PATH="/data/finance.db" \
    BACKUP_DIR="/backups"

EXPOSE 8080

# The health check is a subcommand of this same binary.
#
# distroless has no shell and no curl, so `CMD curl -f .../healthz` does not
# apply. What the image does have is this executable, and it reads HTTP_ADDR --
# the same variable the server reads -- so moving the port cannot leave the
# check probing the wrong one.
#
# start-period covers migrations, which run before anything listens: a container
# is not unhealthy while it is still applying them.
HEALTHCHECK --interval=30s --timeout=10s --start-period=30s --retries=3 \
    CMD ["/usr/local/bin/finance-tracker", "healthcheck"]

ENTRYPOINT ["/usr/local/bin/finance-tracker"]
