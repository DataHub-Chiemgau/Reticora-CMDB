# PostgreSQL/TimescaleDB with pgBackRest (WP-100, OPS-04, E-29): WAL
# archiving (archive_command) and base backups run with the same image, so
# the pgBackRest version always matches between archive-push and backup.
#
# pgBackRest is built from its release source on the image's own Alpine
# base; the runtime keeps only the shared libraries it links against.
#
# Reproducible and integrity-checked (WP-389, TEC-15, E-96): the base is
# pinned by version and by the digest of its multi-arch index, the same
# reference as the CI database and every other TimescaleDB image of the
# repository (tests/ci-config.test.sh); the pgBackRest source tarball is
# checked against its sha256 before it is unpacked. A new version needs both
# values anew.
ARG TIMESCALE_IMAGE=timescale/timescaledb:2.31.0-pg16@sha256:322ae364eca3321cf2a48f1ef31213b87843413b3cf8feeebf54e999a26bb731
# Go toolchain of the repository (backend/Dockerfile), pinned by digest.
ARG GO_IMAGE=golang:1.26.10-alpine@sha256:9ce231b28998b32e116927c61aa5c406183061d4f3f2d61b292c88e7f6bdf7ca

# The Go helpers of the base image carry fixable findings of their Go
# toolchain (E-96); both are rebuilt here at the module version the base
# image ships. go install verifies every module against sum.golang.org.
# - timescaledb-tune tunes postgresql.conf at the first init
#   (/docker-entrypoint-initdb.d/001_timescaledb_tune.sh).
# - gosu: the entrypoint drops from root to postgres with it. gosu has no
#   semver tags; the pseudo-version is the commit of its tag 1.19.
FROM ${GO_IMAGE} AS go-tools
ARG TIMESCALEDB_TUNE_VERSION=v0.19.0
ARG GOSU_VERSION=v0.0.0-20250923190938-6456aaa0f3c8
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org
RUN go install -trimpath -ldflags='-d -w' "github.com/timescale/timescaledb-tune/cmd/timescaledb-tune@${TIMESCALEDB_TUNE_VERSION}" \
    && go install -trimpath -ldflags='-d -w' "github.com/tianon/gosu@${GOSU_VERSION}" \
    && /go/bin/timescaledb-tune --version \
    && /go/bin/gosu nobody true

FROM ${TIMESCALE_IMAGE} AS pgbackrest-build
ARG PGBACKREST_VERSION=2.56.0
ARG PGBACKREST_SHA256=e4ed91be1c518817fd2909c99edc0d3661126fde86abf77a0d470da09741447f
RUN apk add --no-cache build-base meson ninja pkgconf curl \
        openssl-dev libpq-dev libxml2-dev lz4-dev zstd-dev bzip2-dev zlib-dev yaml-dev libssh2-dev
RUN curl -fsSL -o /tmp/pgbackrest.tar.gz \
        "https://github.com/pgbackrest/pgbackrest/archive/refs/tags/release/${PGBACKREST_VERSION}.tar.gz" \
    && [ "${#PGBACKREST_SHA256}" -eq 64 ] \
    && echo "${PGBACKREST_SHA256}  /tmp/pgbackrest.tar.gz" | sha256sum -c - \
    && tar xzf /tmp/pgbackrest.tar.gz -C /tmp \
    && meson setup /tmp/build "/tmp/pgbackrest-release-${PGBACKREST_VERSION}" --prefix=/usr/local --buildtype=release \
    && ninja -C /tmp/build \
    && install -m 0755 /tmp/build/src/pgbackrest /usr/local/bin/pgbackrest \
    && /usr/local/bin/pgbackrest version

FROM ${TIMESCALE_IMAGE}
# libexpat of the base image has a fixable High finding (E-96). curl: the
# signed GET ?versioning of `reticora-backup.sh verify-repo` (NFR-05).
RUN apk add --no-cache libpq openssl libxml2 lz4-libs zstd-libs libbz2 zlib libssh2 curl \
    && apk upgrade --no-cache libexpat \
    && curl --version
# Rebuilt Go helpers (stage go-tools). timescaledb-parallel-copy is used by
# neither the entrypoint, the init scripts nor Reticora and is removed.
COPY --from=go-tools /go/bin/timescaledb-tune /usr/local/bin/timescaledb-tune
COPY --from=go-tools /go/bin/gosu /usr/local/bin/gosu
RUN rm /usr/local/bin/timescaledb-parallel-copy \
    && timescaledb-tune --version \
    && gosu --version \
    && [ "$(gosu postgres id -un)" = postgres ]
COPY --from=pgbackrest-build /usr/local/bin/pgbackrest /usr/local/bin/pgbackrest
COPY pgbackrest.conf /etc/pgbackrest/pgbackrest.conf
COPY reticora-backup.sh /usr/local/bin/reticora-backup.sh
COPY reticora-pgbackrest-env.sh /usr/local/bin/reticora-pgbackrest-env.sh
RUN chmod 0755 /usr/local/bin/reticora-backup.sh /usr/local/bin/reticora-pgbackrest-env.sh \
    && mkdir -p /var/lib/pgbackrest /var/log/pgbackrest /var/spool/pgbackrest /tmp/pgbackrest \
    && chown postgres:postgres /var/lib/pgbackrest /var/log/pgbackrest /var/spool/pgbackrest /tmp/pgbackrest \
    && chmod 0750 /var/lib/pgbackrest \
    && pgbackrest version
