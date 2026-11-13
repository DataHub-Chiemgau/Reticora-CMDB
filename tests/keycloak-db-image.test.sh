#!/usr/bin/env bash
#
# keycloak-db-image.test.sh — the own image reticora-keycloak-db
# (deploy/docker-compose/keycloak-db, E-96, A-85) takes over the data volume
# of the vendor image it replaces. Needs Docker.
#
# For each earlier image of the service keycloak-db — postgres:16.15-alpine
# (until 2026-11-13) and postgres:16.16-alpine (until the own image), both
# pinned by version and digest — the test initializes a volume with it the
# way docker-compose.yml runs the service (POSTGRES_DB/USER/PASSWORD,
# volume on /var/lib/postgresql/data), writes a marker row, stops it and
# starts the own image on the same volume:
#   - it becomes ready (pg_isready) and runs PostgreSQL 16.16;
#   - the server process runs as postgres, UID 70, the data directory keeps
#     owner 70:70 and mode 0700 (the entrypoint changes nothing);
#   - the marker row is there; the password of the first start is accepted
#     over TCP (as Keycloak and install-cloud.sh connect), a wrong one is not;
#   - gosu of the image is built with the Go toolchain of backend/Dockerfile.
# Then a fresh volume initialized by the own image passes the same checks.
#
# Usage: ./tests/keycloak-db-image.test.sh
#   KEYCLOAK_DB_TEST_IMAGE  a prebuilt image to test (default: built here
#                           from deploy/docker-compose/keycloak-db)
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1

if ! docker info >/dev/null 2>&1; then
    echo "FAIL: Docker is required"
    exit 1
fi

# The images keycloak-db ran before (git history of docker-compose.yml).
PREVIOUS_IMAGES=(
    "postgres:16.15-alpine@sha256:aa6cb33cf4c19c765cefcf48d96176258800870a9a1e930ad126200f9d825139"
    "postgres:16.16-alpine@sha256:0adbf25a9e96d38823bb813f3ac0ed41fadb665c88f1df7fe6dbfb060cbdc106"
)
RUN_ID="kcdb-image-$$"
IMAGE="${KEYCLOAK_DB_TEST_IMAGE:-reticora-keycloak-db:$RUN_ID}"
PASSWORD="kc-$(openssl rand -hex 12)"

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; [ -z "${2:-}" ] || printf '%s\n' "$2" | head -n 10 | sed 's/^/     /'; return 0; }

CONTAINERS=()
VOLUMES=()
cleanup() {
    local c v
    for c in "${CONTAINERS[@]}"; do docker rm -f "$c" >/dev/null 2>&1; done
    for v in "${VOLUMES[@]}"; do docker volume rm -f "$v" >/dev/null 2>&1; done
    docker network rm "$RUN_ID" >/dev/null 2>&1
    [ -n "${KEYCLOAK_DB_TEST_IMAGE:-}" ] || docker rmi "$IMAGE" >/dev/null 2>&1
    return 0
}
trap cleanup EXIT

if [ -z "${KEYCLOAK_DB_TEST_IMAGE:-}" ]; then
    if ! docker build -q -t "$IMAGE" deploy/docker-compose/keycloak-db >/dev/null; then
        echo "FAIL: building deploy/docker-compose/keycloak-db failed"
        exit 1
    fi
fi
docker network create "$RUN_ID" >/dev/null || exit 1

# start_db <container> <image> <volume> — keycloak-db as docker-compose.yml
# runs it; waits until it accepts connections over TCP.
start_db() {
    local i
    CONTAINERS+=("$1")
    docker image inspect "$2" >/dev/null 2>&1 || docker pull -q "$2" >/dev/null || return 1
    docker run -d --name "$1" --network "$RUN_ID" --network-alias keycloak-db \
        -e POSTGRES_DB=keycloak -e POSTGRES_USER=keycloak -e POSTGRES_PASSWORD="$PASSWORD" \
        -v "$3:/var/lib/postgresql/data" "$2" >/dev/null || return 1
    for i in $(seq 1 60); do
        docker exec "$1" pg_isready -q -h 127.0.0.1 -U keycloak 2>/dev/null && return 0
        sleep 1
    done
    docker logs --tail 30 "$1"
    return 1
}
sql() { docker exec "$1" psql -X -qtA -U keycloak -d keycloak -c "$2"; }
tcp_login() {
    docker exec -e PGPASSWORD="$2" -e PGCONNECT_TIMEOUT=5 "$1" \
        psql -X -qtA -h keycloak-db -U keycloak -d keycloak -c 'SELECT 1' 2>/dev/null
}

# check_running <label> <container> <marker>
check_running() {
    local label="$1" c="$2" marker="$3" problems="" version owner user
    version="$(sql "$c" 'SHOW server_version')"
    case "$version" in 16.16*) ;; *) problems+="server_version $version, not 16.16; " ;; esac
    user="$(docker exec "$c" sh -c 'ps -o user= -o comm= | awk "\$2 == \"postgres\" {print \$1}" | sort -u')"
    [ "$user" = postgres ] || problems+="postgres runs as '$user'; "
    [ "$(docker exec "$c" id -u postgres)" = 70 ] || problems+="UID of postgres is not 70; "
    owner="$(docker exec "$c" stat -c '%u:%g %a' /var/lib/postgresql/data)"
    [ "$owner" = "70:70 700" ] || problems+="data directory $owner, not 70:70 700; "
    [ -z "$marker" ] || [ "$(sql "$c" 'SELECT note FROM upgrade_marker')" = "$marker" ] || problems+="marker row missing; "
    [ "$(tcp_login "$c" "$PASSWORD")" = 1 ] || problems+="password not accepted over TCP; "
    [ -z "$(tcp_login "$c" "wrong-$PASSWORD")" ] || problems+="a wrong password is accepted over TCP; "
    if [ -z "$problems" ]; then ok "$label"; else fail "$label" "$problems"; fi
}

n=0
for previous in "${PREVIOUS_IMAGES[@]}"; do
    n=$((n + 1))
    vol="$RUN_ID-$n"
    VOLUMES+=("$vol")
    marker="written by ${previous%@*}"
    if start_db "$RUN_ID-old-$n" "$previous" "$vol" \
        && sql "$RUN_ID-old-$n" "CREATE TABLE upgrade_marker (note text); INSERT INTO upgrade_marker VALUES ('$marker')" \
        && docker stop "$RUN_ID-old-$n" >/dev/null; then
        if start_db "$RUN_ID-new-$n" "$IMAGE" "$vol"; then
            check_running "upgrade: the volume of ${previous%@*} runs on the own image (16.16, UID 70, data and password kept)" \
                "$RUN_ID-new-$n" "$marker"
            docker stop "$RUN_ID-new-$n" >/dev/null
        else
            fail "upgrade: the volume of ${previous%@*} runs on the own image" "the own image did not become ready"
        fi
    else
        fail "upgrade: the volume of ${previous%@*} runs on the own image" "could not initialize the volume with ${previous%@*}"
    fi
done

VOLUMES+=("$RUN_ID-fresh")
if start_db "$RUN_ID-fresh" "$IMAGE" "$RUN_ID-fresh"; then
    check_running "fresh: the own image initializes an empty volume (16.16, UID 70, password over TCP)" "$RUN_ID-fresh" ""
else
    fail "fresh: the own image initializes an empty volume" "it did not become ready"
fi

toolchain="$(sed -n 's/^FROM golang:\([0-9.]*\)-alpine.*/go\1/p' backend/Dockerfile | head -n 1)"
gosu="$(docker run --rm --entrypoint gosu "$IMAGE" --version)"
if [ -n "$toolchain" ] && grep -q "($toolchain on " <<<"$gosu"; then
    ok "gosu of the image is built with the repository toolchain ($gosu)"
else
    fail "gosu of the image is built with the repository toolchain $toolchain" "$gosu"
fi

echo
printf '%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
