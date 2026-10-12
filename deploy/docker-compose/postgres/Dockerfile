# PostgreSQL/TimescaleDB with pgBackRest (WP-100, OPS-04, E-29): WAL
# archiving (archive_command) and base backups run with the same image, so
# the pgBackRest version always matches between archive-push and backup.
#
# pgBackRest is built from its release source on the image's own Alpine
# base; the runtime keeps only the shared libraries it links against.
ARG TIMESCALE_IMAGE=timescale/timescaledb:latest-pg16

FROM ${TIMESCALE_IMAGE} AS pgbackrest-build
ARG PGBACKREST_VERSION=2.56.0
RUN apk add --no-cache build-base meson ninja pkgconf curl \
        openssl-dev libpq-dev libxml2-dev lz4-dev zstd-dev bzip2-dev zlib-dev yaml-dev libssh2-dev
RUN curl -fsSL "https://github.com/pgbackrest/pgbackrest/archive/refs/tags/release/${PGBACKREST_VERSION}.tar.gz" \
        | tar xz -C /tmp \
    && meson setup /tmp/build "/tmp/pgbackrest-release-${PGBACKREST_VERSION}" --prefix=/usr/local --buildtype=release \
    && ninja -C /tmp/build \
    && install -m 0755 /tmp/build/src/pgbackrest /usr/local/bin/pgbackrest \
    && /usr/local/bin/pgbackrest version

FROM ${TIMESCALE_IMAGE}
RUN apk add --no-cache libpq openssl libxml2 lz4-libs zstd-libs libbz2 zlib libssh2
COPY --from=pgbackrest-build /usr/local/bin/pgbackrest /usr/local/bin/pgbackrest
COPY pgbackrest.conf /etc/pgbackrest/pgbackrest.conf
COPY reticora-backup.sh /usr/local/bin/reticora-backup.sh
COPY reticora-pgbackrest-env.sh /usr/local/bin/reticora-pgbackrest-env.sh
RUN chmod 0755 /usr/local/bin/reticora-backup.sh /usr/local/bin/reticora-pgbackrest-env.sh \
    && mkdir -p /var/lib/pgbackrest /var/log/pgbackrest /var/spool/pgbackrest /tmp/pgbackrest \
    && chown postgres:postgres /var/lib/pgbackrest /var/log/pgbackrest /var/spool/pgbackrest /tmp/pgbackrest \
    && chmod 0750 /var/lib/pgbackrest \
    && pgbackrest version
