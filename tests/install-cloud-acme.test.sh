#!/usr/bin/env bash
#
# install-cloud-acme.test.sh — Let's Encrypt issuance of install-cloud.sh
# (WP-094/WP-419, INS-03, TEC-09, F-033, F-034). Moved from tests/audit/
# (Phase-1 acceptance audit, group T5) by WP-419.
#
# issue_tls_certificate moves the bootstrap placeholder (live/<domain>,
# archive/<domain>, renewal/<domain>.conf) aside before `certbot certonly`
# and restores it when the request fails, so certbot stores the lineage in
# live/<domain>: no CertStorageError, no second lineage <domain>-0001, no
# second ACME order on a re-run. certbot runs the deploy hook
# deploy/docker-compose/certbot-deploy-hook.sh (--deploy-hook), which gives
# the key to group 101 (0640) and makes the chain readable for nginx (uid/gid
# 101) in the frontend. Before it reloads nginx and reports success,
# issue_tls_certificate checks with `nginx -t` in the frontend that nginx can
# load the pair; otherwise it aborts with rc 1 (F-034, no silent success).
#
# The certbot stand-in (fake_certbot_certonly) mirrors certbot 2.11.0, which
# the audit drove for real (scratch probe verify-B12/certbot/cli_probe.py):
#   - a failed http-01 challenge aborts before any file is written;
#   - lineage name = --cert-name or the first -d domain, made unique against
#     renewal/<name>.conf (util.unique_lineage_name: <name>-0001, ...);
#   - the (still empty) renewal/<name>.conf is created first;
#   - archive/ and live/ are created 0700 when missing;
#   - a non-empty archive/<name> or live/<name> raises CertStorageError;
#   - archive/<name>/{cert,chain,fullchain}1.pem 0644, privkey1.pem 0600,
#     live/<name>/*.pem are relative symlinks into archive/<name>/;
#   - --deploy-hook: once the lineage is stored, the hook command runs through
#     the shell with RENEWED_LINEAGE=<config dir>/live/<name> and
#     RENEWED_DOMAINS; a failing hook is reported, the exit code stays 0. The
#     hook's path in the certbot container is mapped to the file the certbot
#     service of docker-compose.yml mounts there.
# nginx in the frontend (compose_cmd stub): `nginx -t` and `nginx -s reload`
# succeed only when uid 101 (gid 101, no supplementary groups, no
# capabilities) can read live/<domain>/fullchain.pem and privkey.pem: every
# directory on the literal and on the resolved path, from the bind-mounted
# letsencrypt/ directory down, searchable and the target readable under
# owner/group/other permission bits.
#
# Runs as root (CI: sudo) like certbot and its deploy hook in the certbot
# container: only root can give the key to group 101. docker, sudo, curl and
# systemctl are refused stubs on PATH; a call to one of them fails the run.
#
# Usage: sudo ./tests/install-cloud-acme.test.sh
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1
REPO_ROOT="$(pwd)"
COMPOSE_SRC="$REPO_ROOT/deploy/docker-compose/docker-compose.yml"

if [ "$(id -u)" -ne 0 ]; then
    echo "install-cloud-acme.test.sh runs as root (sudo ./tests/install-cloud-acme.test.sh): certbot's deploy hook gives the key to group 101 as root in the certbot container." >&2
    exit 1
fi

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT
# The unprivileged-installer case reads the harness and its files as nobody.
chmod 755 "$TMP_ROOT"

HARNESS="$TMP_ROOT/harness.sh"
head -n -1 install-cloud.sh > "$HARNESS"
cp install-preflight.sh "$TMP_ROOT/"

# Anything outside the harness the installer could reach is refused and logged.
GUARD_LOG="$TMP_ROOT/guard.log"
: > "$GUARD_LOG"
mkdir -p "$TMP_ROOT/bin"
for tool in docker docker-compose sudo curl systemctl service apt-get; do
    printf '#!/bin/sh\nprintf "%%s %%s\\n" %s "$*" >> %s\nexit 1\n' "$tool" "$GUARD_LOG" > "$TMP_ROOT/bin/$tool"
done
chmod 755 "$TMP_ROOT/bin" "$TMP_ROOT"/bin/*
PATH="$TMP_ROOT/bin:$PATH"

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; }

load_installer() {
    set --
    # shellcheck disable=SC1090
    . "$HARNESS"
    NON_INTERACTIVE=1
    COMPOSE=(docker compose)
}

run_test() {
    local name="$1"; shift
    if ( set -e; load_installer; "$@" ) >"$TMP_ROOT/out" 2>&1; then
        ok "$name"
    else
        fail "$name"
        sed 's/\x1b\[[0-9;]*m//g; s/^/     /' "$TMP_ROOT/out" | tail -n 14
    fi
}

# make_ca_chain <dir> <domain> — test root CA (ca.pem) plus a leaf for <domain>
# signed by an intermediate: leaf.pem, int.pem, fullchain.pem, privkey.pem.
make_ca_chain() {
    local dir="$1" domain="$2"
    mkdir -p "$dir"
    openssl req -x509 -newkey rsa:2048 -nodes -keyout "$dir/ca.key" -out "$dir/ca.pem" -days 30 \
        -subj "/CN=Audit ACME Root" -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" 2>/dev/null
    openssl req -newkey rsa:2048 -nodes -keyout "$dir/int.key" -out "$dir/int.csr" -subj "/CN=Audit ACME Intermediate" 2>/dev/null
    printf 'basicConstraints=critical,CA:TRUE\nkeyUsage=critical,keyCertSign,cRLSign\n' > "$dir/int.ext"
    openssl x509 -req -in "$dir/int.csr" -CA "$dir/ca.pem" -CAkey "$dir/ca.key" -CAcreateserial -days 30 \
        -extfile "$dir/int.ext" -out "$dir/int.pem" 2>/dev/null
    openssl req -newkey rsa:2048 -nodes -keyout "$dir/privkey.pem" -out "$dir/leaf.csr" -subj "/CN=$domain" 2>/dev/null
    printf 'subjectAltName=DNS:%s\n' "$domain" > "$dir/leaf.ext"
    openssl x509 -req -in "$dir/leaf.csr" -CA "$dir/int.pem" -CAkey "$dir/int.key" -CAcreateserial -days 30 \
        -extfile "$dir/leaf.ext" -out "$dir/leaf.pem" 2>/dev/null
    cat "$dir/leaf.pem" "$dir/int.pem" > "$dir/fullchain.pem"
}

# certbot_mount_source <container path> — the host file the certbot service of
# docker-compose.yml mounts at <container path>.
certbot_mount_source() {
    local src
    src="$(awk -v target="$1" '
        /^  [a-z][a-z0-9-]*:$/ { svc = $1 }
        svc == "certbot:" && $1 == "-" { n = split($2, p, ":"); if (n >= 2 && p[2] == target) print p[1] }
    ' "$COMPOSE_SRC")"
    [ -n "$src" ] || return 1
    printf '%s/deploy/docker-compose/%s' "$REPO_ROOT" "${src#./}"
}

# run_deploy_hook <command> <lineage> <domains> — certbot runs the hook
# command through the shell; "sh <path>" runs the mounted script with sh, a
# bare path must be executable.
run_deploy_hook() {
    local cmd="$1" lineage="$2" domains="$3" path src
    case "$cmd" in
        "sh "*) path="${cmd#sh }" ;;
        *) path="$cmd" ;;
    esac
    src="$(certbot_mount_source "$path")" || { echo "certbot: deploy hook $path is not mounted in the certbot service" >&2; return 127; }
    [ -f "$src" ] || { echo "certbot: deploy hook $src does not exist" >&2; return 127; }
    if [ "$path" != "$cmd" ]; then
        RENEWED_LINEAGE="$lineage" RENEWED_DOMAINS="$domains" sh "$src"
    else
        [ -x "$src" ] || return 126
        RENEWED_LINEAGE="$lineage" RENEWED_DOMAINS="$domains" "$src"
    fi
}

# fake_certbot_certonly <args…> — certbot 2.11.0 `certonly --webroot` storage
# behaviour (see header). Config dir: $(letsencrypt_dir), the host side of the
# /etc/letsencrypt bind mount. Issued material: $ACME_ISSUED. Every ACME order
# appends a line to $ACME_ORDERS. Knobs: ACME_CHALLENGE_SERVED=0 (challenge
# fails), ACME_DEPLOY_HOOK=0 (the deploy hook does not run),
# ACME_FAIL_WHILE_STORING=1 (error after the empty renewal configuration and
# the directories were created), ACME_EXIT_AFTER_STORE=1 (error exit after
# the lineage and the hook).
fake_certbot_certonly() {
    local domain="" certname="" hook="" prev=""
    for a in "$@"; do
        case "$prev" in
            -d) [ -n "$domain" ] || domain="$a" ;;
            --cert-name) certname="$a" ;;
            --deploy-hook) hook="$a" ;;
        esac
        prev="$a"
    done
    [ -n "$domain" ] || { echo "certbot: no domain" >&2; return 1; }
    if [ "${ACME_CHALLENGE_SERVED:-1}" != "1" ]; then
        echo "Certbot failed to authenticate some domains (authenticator: webroot)." >&2
        return 1
    fi
    echo "order $domain" >> "$ACME_ORDERS"
    local le name renewal
    le="$(letsencrypt_dir)"
    name="${certname:-$domain}"
    mkdir -p "$le/renewal"
    renewal="$le/renewal/$name.conf"
    if [ -e "$renewal" ]; then
        local n=1
        while [ -e "$le/renewal/$name-$(printf '%04d' "$n").conf" ]; do n=$((n + 1)); done
        name="$name-$(printf '%04d' "$n")"
        renewal="$le/renewal/$name.conf"
    fi
    : > "$renewal"
    local d
    for d in "$le/archive" "$le/live"; do
        [ -d "$d" ] || { mkdir -p "$d"; chmod 700 "$d"; }
    done
    if [ -e "$le/archive/$name" ] && [ -n "$(ls -A "$le/archive/$name" 2>/dev/null)" ]; then
        echo "An unexpected error occurred: CertStorageError: archive directory exists for $name" >&2
        return 1
    fi
    if [ -e "$le/live/$name" ] && [ -n "$(ls -A "$le/live/$name" 2>/dev/null)" ]; then
        echo "An unexpected error occurred: CertStorageError: live directory exists for $name" >&2
        return 1
    fi
    mkdir -p "$le/archive/$name" "$le/live/$name"
    chmod 755 "$le/archive/$name" "$le/live/$name"
    if [ "${ACME_FAIL_WHILE_STORING:-0}" = "1" ]; then
        echo "An unexpected error occurred: OSError: [Errno 28] No space left on device" >&2
        return 1
    fi
    cp "$ACME_ISSUED/leaf.pem" "$le/archive/$name/cert1.pem"
    cp "$ACME_ISSUED/int.pem" "$le/archive/$name/chain1.pem"
    cp "$ACME_ISSUED/fullchain.pem" "$le/archive/$name/fullchain1.pem"
    chmod 644 "$le/archive/$name/cert1.pem" "$le/archive/$name/chain1.pem" "$le/archive/$name/fullchain1.pem"
    ( umask 077; cp "$ACME_ISSUED/privkey.pem" "$le/archive/$name/privkey1.pem" )
    chmod 600 "$le/archive/$name/privkey1.pem"
    local kind
    for kind in cert chain fullchain privkey; do
        ln -s "../../archive/$name/${kind}1.pem" "$le/live/$name/$kind.pem"
    done
    printf 'version = 2.11.0\narchive_dir = %s\ncert = %s\nprivkey = %s\nchain = %s\nfullchain = %s\n\n[renewalparams]\nauthenticator = webroot\n' \
        "$le/archive/$name" "$le/live/$name/cert.pem" "$le/live/$name/privkey.pem" "$le/live/$name/chain.pem" \
        "$le/live/$name/fullchain.pem" > "$renewal"
    [ -z "$hook" ] || printf 'renew_hook = %s\n' "$hook" >> "$renewal"
    echo "Successfully received certificate."
    echo "Certificate is saved at: /etc/letsencrypt/live/$name/fullchain.pem"
    if [ -n "$hook" ] && [ "${ACME_DEPLOY_HOOK:-1}" = "1" ]; then
        local hook_rc=0
        run_deploy_hook "$hook" "$le/live/$name" "$domain" || hook_rc=$?
        [ "$hook_rc" -eq 0 ] || echo "Hook 'deploy-hook' reported error code $hook_rc"
    fi
    if [ "${ACME_EXIT_AFTER_STORE:-0}" = "1" ]; then
        echo "An unexpected error occurred: the post hook failed" >&2
        return 1
    fi
}

# can_read_as <uid> <gid> <path> — 0 when a process with <uid>/<gid> (no
# supplementary groups, no capabilities) can open <path>: every directory on
# the literal and on the resolved path, from the bind-mounted letsencrypt/
# directory down, must be searchable and the target readable under
# owner/group/other permission bits.
perm_allows() {
    local p="$1" bit="$2" as_uid="$3" as_gid="$4" uid gid mode
    IFS=' ' read -r uid gid mode < <(stat -L -c '%u %g %a' "$p") || return 1
    [ -n "$mode" ] || return 1
    mode=$((8#$mode))
    if [ "$uid" = "$as_uid" ]; then
        [ $(( (mode >> 6) & bit )) -ne 0 ]
    elif [ "$gid" = "$as_gid" ]; then
        [ $(( (mode >> 3) & bit )) -ne 0 ]
    else
        [ $(( mode & bit )) -ne 0 ]
    fi
}

dirs_searchable_from_le() {
    local le="$1" target_dir="$2" as_uid="$3" as_gid="$4" rel cur part
    case "$target_dir" in "$le"|"$le"/*) ;; *) return 1 ;; esac
    perm_allows "$le" 1 "$as_uid" "$as_gid" || return 1
    rel="${target_dir#"$le"}"
    cur="$le"
    local -a parts=()
    IFS=/ read -r -a parts <<< "$rel"
    for part in "${parts[@]}"; do
        [ -n "$part" ] || continue
        cur="$cur/$part"
        perm_allows "$cur" 1 "$as_uid" "$as_gid" || return 1
    done
}

can_read_as() {
    local as_uid="$1" as_gid="$2" path="$3" le resolved
    le="$(cd "$(letsencrypt_dir)" && pwd -P)"
    [ -e "$path" ] || return 1
    resolved="$(readlink -f "$path")"
    dirs_searchable_from_le "$le" "$(cd "$(dirname "$path")" && pwd -P)" "$as_uid" "$as_gid" || return 1
    dirs_searchable_from_le "$le" "$(dirname "$resolved")" "$as_uid" "$as_gid" || return 1
    perm_allows "$resolved" 4 "$as_uid" "$as_gid"
}

can_read_as_uid101() { can_read_as 101 101 "$1"; }

# nginx_loads_served_pair — nginx (uid 101) reads the configured pair.
nginx_loads_served_pair() {
    local live; live="$(tls_live_dir)"
    can_read_as_uid101 "$live/fullchain.pem" && can_read_as_uid101 "$live/privkey.pem"
}

# setup_case <name> <domain> — isolated compose dir, issued chain, compose stub.
setup_case() {
    local name="$1"
    RETICORA_TLS_DOMAIN="$2"
    RETICORA_CERT_EMAIL="ops@example.com"
    COMPOSE_DIR="$TMP_ROOT/$name/compose"
    # letsencrypt/live/ exists from earlier installer runs (bootstrap_tls_cert
    # creates it with mkdir -p); certbot creates archive/ itself.
    mkdir -p "$COMPOSE_DIR/letsencrypt/live"
    chmod 755 "$TMP_ROOT/$name" "$COMPOSE_DIR" "$COMPOSE_DIR/letsencrypt" "$COMPOSE_DIR/letsencrypt/live"
    ACME_ISSUED="$TMP_ROOT/$name/issued"
    ACME_ORDERS="$TMP_ROOT/$name/orders"
    COMPOSE_CALLS="$TMP_ROOT/$name/compose-calls"
    : > "$ACME_ORDERS"
    : > "$COMPOSE_CALLS"
    make_ca_chain "$ACME_ISSUED" "$RETICORA_TLS_DOMAIN"
    chmod 755 "$ACME_ISSUED"
    RETICORA_TLS_CA_BUNDLE="$ACME_ISSUED/ca.pem"
    compose_cmd() {
        printf '%s\n' "$*" >> "$COMPOSE_CALLS"
        case "$*" in
            *"--entrypoint certbot certbot certonly"*)
                shift 5 # run --rm --no-deps --entrypoint certbot
                shift 2 # certbot certonly
                fake_certbot_certonly "$@"
                return $?
                ;;
            *"exec -T frontend nginx -t"*)
                nginx_loads_served_pair || { echo "nginx: [emerg] cannot load certificate key \"/etc/letsencrypt/live/$RETICORA_TLS_DOMAIN/privkey.pem\": Permission denied"; return 1; }
                echo "nginx: configuration file /etc/nginx/nginx.conf test is successful"
                ;;
            *"exec -T frontend nginx -s reload"*)
                # nginx (uid 101) re-reads the served pair on reload.
                nginx_loads_served_pair
                return $?
                ;;
        esac
        return 0
    }
}

orders() { wc -l < "$ACME_ORDERS" | tr -d ' '; }

# call_line <pattern> — line number of the first recorded compose call
# containing <pattern> (empty when none).
call_line() { grep -n -F -- "$1" "$COMPOSE_CALLS" | head -n 1 | cut -d: -f1; }

served_chain_is_issued() {
    local live; live="$(tls_live_dir)"
    tls_cert_is_trusted "$live/fullchain.pem" || { echo "live/$RETICORA_TLS_DOMAIN/fullchain.pem does not chain to the issuing CA (still the placeholder?)"; return 1; }
    validate_tls_material "$RETICORA_TLS_DOMAIN" 1 >/dev/null
}

no_suffixed_lineage() {
    local le; le="$(letsencrypt_dir)"
    if ls -d "$le/live/$RETICORA_TLS_DOMAIN"-[0-9][0-9][0-9][0-9] >/dev/null 2>&1; then
        echo "certbot created a second lineage: $(ls -d "$le/live/$RETICORA_TLS_DOMAIN"-[0-9]*)"
        return 1
    fi
}

no_empty_renewal_conf() {
    local le f; le="$(letsencrypt_dir)"
    for f in "$le"/renewal/*.conf; do
        [ -e "$f" ] || continue
        [ -s "$f" ] || { echo "empty renewal configuration left behind: ${f#"$le"/}"; return 1; }
    done
}

no_placeholder_backup() {
    [ ! -e "$(tls_placeholder_backup_dir)" ] || { echo "placeholder backup left behind: $(tls_placeholder_backup_dir)"; return 1; }
}

fingerprint() { openssl x509 -in "$1" -noout -fingerprint -sha256 2>/dev/null; }

# issue_capture — issue_tls_certificate in a subshell; output in $OUT, rc in $RC.
issue_capture() {
    if OUT="$( (issue_tls_certificate) 2>&1)"; then RC=0; else RC=$?; fi
    OUT="$(printf '%s' "$OUT" | sed 's/\x1b\[[0-9;]*m//g')"
}

# ─── F-033: fresh installation with the bootstrap placeholder ───────────────
# The installer's own sequence: bootstrap_tls_cert (placeholder), then
# issue_tls_certificate. Afterwards nginx must serve the issued chain from
# live/<domain>/ and validate_tls_material <domain> 1 must pass.
test_fresh_install_serves_issued_certificate() {
    setup_case fresh cmdb.example.com
    bootstrap_tls_cert
    tls_cert_is_selfsigned || { echo "precondition: placeholder expected"; return 1; }
    issue_tls_certificate
    served_chain_is_issued || return 1
    no_suffixed_lineage || return 1
    no_empty_renewal_conf || return 1
    no_placeholder_backup || return 1
    [ "$(orders)" = "1" ] || { echo "ACME orders: $(orders), expected 1"; return 1; }
}

# ─── F-033: installer re-run after the first issuance ───────────────────────
# Re-running the installer must be idempotent: it completes, serves the
# issued certificate, creates no <domain>-0001 lineage and places at most the
# one ACME order of the first run.
test_rerun_is_idempotent() {
    setup_case rerun cmdb.example.org
    bootstrap_tls_cert
    issue_tls_certificate
    local first_orders; first_orders="$(orders)"
    # Second installer run: same steps again.
    bootstrap_tls_cert
    if ! ( issue_tls_certificate ); then
        echo "installer re-run aborted in issue_tls_certificate"
        return 1
    fi
    served_chain_is_issued || return 1
    no_suffixed_lineage || return 1
    [ "$(orders)" = "1" ] || { echo "ACME orders after re-run: $(orders) (first run: $first_orders), expected exactly 1"; return 1; }
}

# ─── F-034: issued material readable by the frontend's nginx user (uid 101) ─
# No bootstrap certificate exists (a missing certificate also triggers ACME).
# nginx in the frontend container (uid 101) must be able to read the chain
# and key it is configured with, otherwise reload and every later frontend
# restart fail.
test_issued_material_readable_by_nginx() {
    setup_case perms cmdb.example.net
    issue_tls_certificate
    local live; live="$(tls_live_dir)"
    tls_cert_is_trusted "$live/fullchain.pem" || { echo "precondition: certbot fake did not issue into live/<domain>"; return 1; }
    can_read_as_uid101 "$live/fullchain.pem" || { echo "uid 101 cannot read live/<domain>/fullchain.pem -> $(readlink -f "$live/fullchain.pem") ($(stat -c '%a %U' "$(letsencrypt_dir)/archive"))"; return 1; }
    can_read_as_uid101 "$live/privkey.pem" || { echo "uid 101 cannot read live/<domain>/privkey.pem -> $(readlink -f "$live/privkey.pem") (archive/ $(stat -c '%a %U' "$(letsencrypt_dir)/archive"), key $(stat -L -c '%a %U' "$live/privkey.pem"))"; return 1; }
    grep -q -- "exec -T frontend nginx -s reload" "$COMPOSE_CALLS" || { echo "nginx was not reloaded"; return 1; }
}

# ─── control: challenge not served ──────────────────────────────────────────
# Issuance fails: the installer only warns, the placeholder is restored
# unchanged and no empty renewal configuration is left behind.
test_failed_challenge_keeps_placeholder() {
    setup_case nochallenge cmdb.example.info
    bootstrap_tls_cert
    local before; before="$(fingerprint "$(tls_live_dir)/fullchain.pem")"
    ACME_CHALLENGE_SERVED=0 issue_tls_certificate
    validate_tls_material "$RETICORA_TLS_DOMAIN" >/dev/null || return 1
    [ "$(fingerprint "$(tls_live_dir)/fullchain.pem")" = "$before" ] || { echo "live/<domain> does not hold the placeholder again"; return 1; }
    no_empty_renewal_conf || return 1
    no_placeholder_backup || return 1
    [ "$(orders)" = "0" ] || return 1
}

# ─── F-034: the key is not world-readable ───────────────────────────────────
# Group 101 (nginx) may read the key, nobody else but root: no world-read bit,
# and neither the server's nobody (65534) nor an ordinary host user (1000)
# can read it. Like the F-034 cases below without a placeholder, isolated
# from F-033.
test_key_not_world_readable() {
    setup_case keymode cmdb.example.biz
    issue_tls_certificate
    local key le
    key="$(readlink -f "$(tls_live_dir)/privkey.pem")"
    le="$(cd "$(letsencrypt_dir)" && pwd -P)"
    case "$key" in "$le/archive/$RETICORA_TLS_DOMAIN/privkey"*) ;; *) echo "precondition: key not in certbot's archive: $key"; return 1 ;; esac
    local mode; mode="$(stat -c '%a' "$key")"
    [ $(( 8#$mode & 7 )) -eq 0 ] || { echo "privkey.pem has mode $mode (world bits set)"; return 1; }
    [ "$(stat -c '%u:%g' "$key")" = "0:101" ] || { echo "privkey.pem belongs to $(stat -c '%u:%g' "$key"), expected 0:101"; return 1; }
    ! can_read_as 65534 65534 "$(tls_live_dir)/privkey.pem" || { echo "uid 65534 can read privkey.pem"; return 1; }
    ! can_read_as 1000 1000 "$(tls_live_dir)/privkey.pem" || { echo "uid 1000 can read privkey.pem"; return 1; }
    can_read_as_uid101 "$(tls_live_dir)/privkey.pem" || { echo "uid 101 cannot read privkey.pem"; return 1; }
}

# ─── F-034: no silent success (nginx -t before the reload) ──────────────────
# Deploy hook not run: certbot's key stays 0600 root in archive/ 0700. nginx -t
# in the frontend fails, so the installer reports an error with the
# troubleshooting reference and rc != 0, never a success message, and sends
# no reload.
test_unreadable_key_no_success_message() {
    setup_case nohook cmdb.example.dev
    ACME_DEPLOY_HOOK=0 issue_capture
    [ "$RC" -ne 0 ] || { echo "rc 0 although nginx cannot load the key"; printf '%s\n' "$OUT"; return 1; }
    ! grep -q -- "certificate issued for" <<< "$OUT" || { echo "success message printed:"; printf '%s\n' "$OUT"; return 1; }
    ! grep -q -- "HTTPS is now active" <<< "$OUT" || { echo "'HTTPS is now active' printed"; return 1; }
    { grep -q -- "nginx -t failed" <<< "$OUT" && grep -q -- "docs/troubleshooting-installation.md" <<< "$OUT"; } \
        || { echo "no error with the troubleshooting reference:"; printf '%s\n' "$OUT"; return 1; }
    [ -n "$(call_line "exec -T frontend nginx -t")" ] || { echo "nginx -t not run"; return 1; }
    [ -z "$(call_line "exec -T frontend nginx -s reload")" ] || { echo "nginx was reloaded despite the failed check"; return 1; }
    [ "$(stat -L -c '%a %u' "$(tls_live_dir)/privkey.pem")" = "600 0" ] || { echo "precondition: key not 0600 root"; return 1; }
}

# With the deploy hook: nginx -t passes, then the reload, both success
# messages and rc 0.
test_loadable_key_success_message() {
    setup_case withhook cmdb.example.app
    bootstrap_tls_cert
    issue_capture
    [ "$RC" -eq 0 ] || { echo "rc $RC"; printf '%s\n' "$OUT"; return 1; }
    { grep -q -- "Let's Encrypt certificate issued for $RETICORA_TLS_DOMAIN" <<< "$OUT" &&
        grep -q -- "HTTPS is now active at https://$RETICORA_TLS_DOMAIN" <<< "$OUT"; } \
        || { echo "success messages missing:"; printf '%s\n' "$OUT"; return 1; }
    local check reload
    check="$(call_line "exec -T frontend nginx -t")"
    reload="$(call_line "exec -T frontend nginx -s reload")"
    { [ -n "$check" ] && [ -n "$reload" ] && [ "$check" -lt "$reload" ]; } \
        || { echo "nginx -t (line ${check:-none}) must precede the reload (line ${reload:-none})"; cat "$COMPOSE_CALLS"; return 1; }
}

# ─── the server (nobody) still reads the chain ──────────────────────────────
# RETICORA_OIDC_CA_CERT_FILE is live/<domain>/fullchain.pem; the server
# container runs as nobody (65534) and exits when it cannot read it.
test_chain_readable_by_server() {
    setup_case server cmdb.example.eu
    issue_tls_certificate
    tls_cert_is_trusted "$(tls_live_dir)/fullchain.pem" || { echo "precondition: certbot fake did not issue into live/<domain>"; return 1; }
    [ "$(tls_ca_cert_path_in_container)" = "/etc/letsencrypt/live/$RETICORA_TLS_DOMAIN/fullchain.pem" ] || return 1
    can_read_as 65534 65534 "$(tls_live_dir)/fullchain.pem" || { echo "uid 65534 cannot read live/<domain>/fullchain.pem"; return 1; }
}

# ─── leftovers of the former installer ──────────────────────────────────────
# A request of the former installer left an empty renewal/<domain>.conf
# behind (certbot writes it before it stores the certificate). It moves aside
# with the placeholder, so certbot still stores the lineage under the domain.
test_leftover_renewal_conf_no_second_lineage() {
    setup_case leftover cmdb.example.at
    bootstrap_tls_cert
    mkdir -p "$(letsencrypt_dir)/renewal"
    : > "$(letsencrypt_dir)/renewal/$RETICORA_TLS_DOMAIN.conf"
    issue_tls_certificate
    served_chain_is_issued || return 1
    no_suffixed_lineage || return 1
    no_empty_renewal_conf || return 1
    no_placeholder_backup || return 1
    [ "$(orders)" = "1" ] || { echo "ACME orders: $(orders), expected 1"; return 1; }
}

# ─── certbot fails while storing ────────────────────────────────────────────
# The order is placed, then storing fails after the empty renewal
# configuration and the lineage directories exist: they are removed, the
# placeholder is back, rc 0 with the warning; the next run stores the lineage
# under the domain.
test_failure_while_storing_restores_placeholder() {
    setup_case storing cmdb.example.ch
    bootstrap_tls_cert
    local before; before="$(fingerprint "$(tls_live_dir)/fullchain.pem")"
    ACME_FAIL_WHILE_STORING=1 issue_capture
    [ "$RC" -eq 0 ] || { echo "rc $RC"; printf '%s\n' "$OUT"; return 1; }
    grep -q -- "Let's Encrypt certificate issuance failed" <<< "$OUT" || { echo "no warning"; return 1; }
    [ "$(fingerprint "$(tls_live_dir)/fullchain.pem")" = "$before" ] || { echo "live/<domain> does not hold the placeholder again"; return 1; }
    [ ! -e "$(letsencrypt_dir)/archive/$RETICORA_TLS_DOMAIN" ] || { echo "archive/<domain> left behind"; return 1; }
    [ ! -e "$(letsencrypt_dir)/renewal/$RETICORA_TLS_DOMAIN.conf" ] || { echo "renewal/<domain>.conf left behind"; return 1; }
    no_placeholder_backup || return 1
    issue_tls_certificate
    served_chain_is_issued || return 1
    no_suffixed_lineage || return 1
    [ "$(orders)" = "2" ] || { echo "ACME orders: $(orders), expected 2"; return 1; }
}

# ─── certbot error after the lineage is stored ──────────────────────────────
# The stored certificate verifies: it is kept and checked like a success (no
# restore of the placeholder, no further order on the next run).
test_error_after_store_keeps_certificate() {
    setup_case afterstore cmdb.example.de
    bootstrap_tls_cert
    ACME_EXIT_AFTER_STORE=1 issue_capture
    [ "$RC" -eq 0 ] || { echo "rc $RC"; printf '%s\n' "$OUT"; return 1; }
    grep -q -- "Let's Encrypt certificate issued for" <<< "$OUT" || { echo "no success message:"; printf '%s\n' "$OUT"; return 1; }
    served_chain_is_issued || return 1
    no_placeholder_backup || return 1
    issue_tls_certificate
    [ "$(orders)" = "1" ] || { echo "ACME orders: $(orders), expected 1"; return 1; }
}

# ─── installer without root ─────────────────────────────────────────────────
# The installer runs as an ordinary user (here nobody): it can read the
# issued chain but not the key (group 101); validate_tls_material <domain> 1
# still passes and leaves the key to nginx -t.
test_unprivileged_installer_validates_chain() {
    command -v setpriv >/dev/null || { echo "setpriv (util-linux) is required"; return 1; }
    setup_case unpriv cmdb.example.nl
    issue_tls_certificate
    local out
    # shellcheck disable=SC2016 # expanded by the shell that runs as nobody
    out="$(cd "$TMP_ROOT" && setpriv --reuid=65534 --regid=65534 --clear-groups -- \
        env PATH="$PATH" HARNESS="$HARNESS" D="$RETICORA_TLS_DOMAIN" CD="$COMPOSE_DIR" CA="$RETICORA_TLS_CA_BUNDLE" bash -c '
            set -- ; . "$HARNESS"
            COMPOSE_DIR="$CD"; RETICORA_TLS_DOMAIN="$D"; RETICORA_TLS_CA_BUNDLE="$CA"
            [ ! -r "$(tls_live_dir)/privkey.pem" ] || { echo "precondition: key readable for nobody"; exit 1; }
            tls_cert_is_selfsigned && { echo "issued chain not readable for nobody"; exit 1; }
            validate_tls_material "$D" 1' 2>&1)" || { printf '%s\n' "$out"; return 1; }
    grep -q -- "key checked by nginx, chain verifies" <<< "$out" || { printf '%s\n' "$out"; return 1; }
}

# ─── the domain names the moved paths ───────────────────────────────────────
# collect_config accepts only host names; the move and removal (with sudo -n
# when needed) refuse anything else as well, before certbot runs.
test_invalid_domain_moves_nothing() {
    setup_case baddomain cmdb.example.se
    mkdir -p "$TMP_ROOT/baddomain/compose/keep"
    RETICORA_TLS_DOMAIN="../../keep"
    issue_capture
    [ "$RC" -ne 0 ] || { echo "rc 0"; return 1; }
    grep -q -- "Invalid TLS domain" <<< "$OUT" || { printf '%s\n' "$OUT"; return 1; }
    [ -d "$TMP_ROOT/baddomain/compose/keep" ] || { echo "a directory outside letsencrypt/live was moved"; return 1; }
    { [ "$(orders)" = "0" ] && [ ! -s "$COMPOSE_CALLS" ]; } || { echo "certbot ran"; return 1; }
}

# ─── renewal ────────────────────────────────────────────────────────────────
# The certbot service renews with the same deploy hook, mounted from
# deploy/docker-compose/certbot-deploy-hook.sh (--server of the renewal loop:
# WP-420, tests/install-cloud-helpers.test.sh).
test_renewal_loop_runs_deploy_hook() {
    local cmd path hook="${CERTBOT_DEPLOY_HOOK:-}"
    [ -n "$hook" ] || { echo "install-cloud.sh passes no deploy hook (CERTBOT_DEPLOY_HOOK)"; return 1; }
    cmd="$(grep -E '^[[:space:]]+command: .*certbot renew' "$COMPOSE_SRC")"
    grep -qF -- "certbot renew --webroot -w /var/www/certbot --quiet --deploy-hook \"$hook\"" <<< "$cmd" \
        || { echo "renewal loop without the deploy hook: $cmd"; return 1; }
    path="${hook#sh }"
    [ "$(certbot_mount_source "$path")" = "$REPO_ROOT/deploy/docker-compose/certbot-deploy-hook.sh" ] \
        || { echo "certbot service does not mount certbot-deploy-hook.sh at $path"; return 1; }
}

run_test "acme: fresh install serves the issued certificate from live/<domain>" test_fresh_install_serves_issued_certificate
run_test "acme: installer re-run is idempotent (no -0001 lineage, no 2nd order)" test_rerun_is_idempotent
run_test "acme: issued key and chain are readable by nginx (uid 101)"           test_issued_material_readable_by_nginx
run_test "acme: failed challenge keeps the placeholder (control)"               test_failed_challenge_keeps_placeholder
run_test "acme: key is not world-readable (only root and gid 101)"              test_key_not_world_readable
run_test "acme: issued key unreadable -> no success message, rc != 0"           test_unreadable_key_no_success_message
run_test "acme: with the deploy hook nginx -t passes before the reload, rc 0"   test_loadable_key_success_message
run_test "acme: the server (nobody) still reads the issued chain"               test_chain_readable_by_server
run_test "acme: an empty renewal conf of a former run causes no -0001 lineage"  test_leftover_renewal_conf_no_second_lineage
run_test "acme: a request failing while storing restores the placeholder"       test_failure_while_storing_restores_placeholder
run_test "acme: an error after storing keeps the issued certificate"            test_error_after_store_keeps_certificate
run_test "acme: an installer without root validates the issued chain"          test_unprivileged_installer_validates_chain
run_test "acme: a domain with path parts is refused before anything moves"     test_invalid_domain_moves_nothing
run_test "acme: the certbot service renews with the same deploy hook"           test_renewal_loop_runs_deploy_hook

if [ -s "$GUARD_LOG" ]; then
    fail "acme: no call reached docker, sudo, curl or systemctl"
    sed 's/^/     /' "$GUARD_LOG"
else
    ok "acme: no call reached docker, sudo, curl or systemctl"
fi

echo
printf '%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
