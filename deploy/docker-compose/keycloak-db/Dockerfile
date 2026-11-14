# PostgreSQL of Keycloak (service keycloak-db, E-96, E-246, A-85): the
# official postgres image with its gosu rebuilt by the Go toolchain of the
# repository. The vendor image ships gosu built with an outdated Go (1.24.6
# in postgres:16.16-alpine: CVE-2025-68121 critical and about 25 high
# findings of the Go standard library) and does not rebuild it; its
# docker-entrypoint.sh calls gosu by name (su-exec is a link to it), so the
# binary is replaced in place. Everything else — PostgreSQL major 16,
# PGDATA /var/lib/postgresql/data, user postgres with UID/GID 70 — stays the
# vendor's, so an existing data volume of postgres:16.x-alpine keeps running
# unchanged (tests/keycloak-db-image.test.sh).
#
# Reproducible and integrity-checked (E-96): both bases are pinned by
# version and by the digest of their multi-arch index; go install verifies
# the module against sum.golang.org. A new version needs both values anew.
ARG POSTGRES_IMAGE=postgres:16.16-alpine@sha256:0adbf25a9e96d38823bb813f3ac0ed41fadb665c88f1df7fe6dbfb060cbdc106
# Go toolchain of the repository (backend/Dockerfile), pinned by digest.
ARG GO_IMAGE=golang:1.26.11-alpine@sha256:ae52b5bc6ed045a9ed26ab03dc19327a802a9cc9aa8b27ed57f5c6b60579b0bb

# gosu has no semver tags; the pseudo-version is the commit of its tag 1.20.
FROM ${GO_IMAGE} AS go-tools
ARG GOSU_VERSION=v0.0.0-20261025094355-29b939aa5bd4
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org
RUN go install -trimpath -ldflags='-d -w' "github.com/tianon/gosu@${GOSU_VERSION}" \
    && /go/bin/gosu nobody true

FROM ${POSTGRES_IMAGE}
COPY --from=go-tools /go/bin/gosu /usr/local/bin/gosu
# The entrypoint drops from root to postgres (UID 70) with the rebuilt gosu,
# also through the su-exec link of the base image.
RUN gosu --version \
    && [ "$(gosu postgres id -un)" = postgres ] \
    && [ "$(id -u postgres):$(id -g postgres)" = 70:70 ] \
    && [ "$(su-exec postgres id -u)" = 70 ] \
    && [ "$PGDATA" = /var/lib/postgresql/data ]
