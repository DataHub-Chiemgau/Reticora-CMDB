#!/usr/bin/env bash
#
# install-cloud-acme.test.sh — Phase-1 acceptance audit, group T5, DEFECT test
# (fails until the installer is fixed; not wired into CI).
#
# WP-094 (INS-03, TEC-15 context): "TLS/ACME im Installer: Erkennung,
# Kettenprüfung, funktionsfähiger Standardpfad".
#
# Findings encoded here:
#   P1 "certbot certonly cannot store the Let's Encrypt certificate because the
#       bootstrap placeholder occupies letsencrypt/live/<domain>/":
#       issue_tls_certificate runs `certbot certonly` while bootstrap_tls_cert's
#       self-signed placeholder still sits in live/<domain>/. certbot 2.11.0
#       (storage.py RenewableCert.new_lineage) first creates
#       renewal/<name>.conf, then refuses a non-empty live/<name> with
#       "CertStorageError: live directory exists for <name>" (one ACME order
#       consumed, a 0-byte renewal conf left behind). On the installer re-run
#       certbot picks the unique name <domain>-0001 (second order), while
#       live/<domain>/fullchain.pem stays self-signed, so
#       validate_tls_material <domain> 1 dies.
#   P1 "Certbot-issued key and chain are unreadable for the unprivileged nginx
#       user of the frontend container": certbot (root in its container)
#       creates letsencrypt/archive/ 0700 and privkey1.pem 0600; the frontend
#       runs nginx as uid 101 (docker-compose.yml `user: nginx`) and reads
#       /etc/letsencrypt/live/<domain>/{fullchain,privkey}.pem through the
#       bind mount. Nothing (chmod, deploy hook) adapts the modes.
# Test gaps closed: critical "No test runs real certbot; the ACME test stubs
#   certbot with a file copy" (as far as possible without network: the stub
#   below reproduces certbot 2.11.0's storage semantics instead of copying
#   files into place), high "Readability of certbot-managed material by the
#   unprivileged frontend is untested".
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
#     live/<name>/*.pem are relative symlinks into archive/<name>/.
#
# Retest:
#   ./tests/audit/install-cloud-acme.test.sh
set -uo pipefail

cd "$(dirname "$0")/../.."
REPO_ROOT="$(pwd)"

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

HARNESS="$TMP_ROOT/harness.sh"
head -n -1 install-cloud.sh > "$HARNESS"
cp install-preflight.sh "$TMP_ROOT/"

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

# fake_certbot_certonly <args…> — certbot 2.11.0 `certonly --webroot` storage
# behaviour (see header). Config dir: $(letsencrypt_dir), the host side of the
# /etc/letsencrypt bind mount. Issued material: $ACME_ISSUED. Every ACME order
# appends a line to $ACME_ORDERS.
fake_certbot_certonly() {
    local domain="" certname="" prev=""
    for a in "$@"; do
        case "$prev" in
            -d) [ -n "$domain" ] || domain="$a" ;;
            --cert-name) certname="$a" ;;
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
    echo "Successfully received certificate."
    echo "Certificate is saved at: /etc/letsencrypt/live/$name/fullchain.pem"
}

# can_read_as_uid101 <path> — 0 when the frontend's nginx user (uid 101, gid
# 101, no supplementary groups, no capabilities) can open <path>: every
# directory on the literal and on the resolved path, from the bind-mounted
# letsencrypt/ directory down, must be searchable and the target readable by
# 101 under owner/group/other permission bits.
perm_allows() {
    local p="$1" bit="$2" uid gid mode
    IFS=' ' read -r uid gid mode < <(stat -L -c '%u %g %a' "$p") || return 1
    [ -n "$mode" ] || return 1
    mode=$((8#$mode))
    if [ "$uid" = "101" ]; then
        [ $(( (mode >> 6) & bit )) -ne 0 ]
    elif [ "$gid" = "101" ]; then
        [ $(( (mode >> 3) & bit )) -ne 0 ]
    else
        [ $(( mode & bit )) -ne 0 ]
    fi
}

dirs_searchable_from_le() {
    local le="$1" target_dir="$2" rel cur part
    case "$target_dir" in "$le"|"$le"/*) ;; *) return 1 ;; esac
    perm_allows "$le" 1 || return 1
    rel="${target_dir#"$le"}"
    cur="$le"
    local -a parts=()
    IFS=/ read -r -a parts <<< "$rel"
    for part in "${parts[@]}"; do
        [ -n "$part" ] || continue
        cur="$cur/$part"
        perm_allows "$cur" 1 || return 1
    done
}

can_read_as_uid101() {
    local path="$1" le resolved
    le="$(cd "$(letsencrypt_dir)" && pwd -P)"
    [ -e "$path" ] || return 1
    resolved="$(readlink -f "$path")"
    dirs_searchable_from_le "$le" "$(cd "$(dirname "$path")" && pwd -P)" || return 1
    dirs_searchable_from_le "$le" "$(dirname "$resolved")" || return 1
    perm_allows "$resolved" 4
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
    chmod 755 "$COMPOSE_DIR/letsencrypt" "$COMPOSE_DIR/letsencrypt/live"
    ACME_ISSUED="$TMP_ROOT/$name/issued"
    ACME_ORDERS="$TMP_ROOT/$name/orders"
    COMPOSE_CALLS="$TMP_ROOT/$name/compose-calls"
    : > "$ACME_ORDERS"
    : > "$COMPOSE_CALLS"
    make_ca_chain "$ACME_ISSUED" "$RETICORA_TLS_DOMAIN"
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
            *"exec -T frontend nginx -s reload"*)
                # nginx (uid 101) re-reads the served pair on reload.
                local live; live="$(tls_live_dir)"
                can_read_as_uid101 "$live/fullchain.pem" && can_read_as_uid101 "$live/privkey.pem"
                return $?
                ;;
        esac
        return 0
    }
}

orders() { wc -l < "$ACME_ORDERS" | tr -d ' '; }

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

# ─── P1: fresh installation with the bootstrap placeholder ──────────────────
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
    [ "$(orders)" = "1" ] || { echo "ACME orders: $(orders), expected 1"; return 1; }
}

# ─── P1: installer re-run after the first issuance attempt ──────────────────
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

# ─── P1: issued material readable by the frontend's nginx user (uid 101) ────
# Isolated from the placeholder defect: no bootstrap certificate exists (a
# missing certificate also triggers ACME), so certbot can store its lineage
# in live/<domain>/. nginx in the frontend container (uid 101) must be able
# to read the chain and key it is configured with, otherwise reload and every
# later frontend restart fail.
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
# Issuance fails: the installer only warns, the placeholder stays usable and
# no empty renewal configuration is left behind.
test_failed_challenge_keeps_placeholder() {
    setup_case nochallenge cmdb.example.info
    bootstrap_tls_cert
    ACME_CHALLENGE_SERVED=0 issue_tls_certificate
    validate_tls_material "$RETICORA_TLS_DOMAIN" >/dev/null || return 1
    no_empty_renewal_conf || return 1
    [ "$(orders)" = "0" ] || return 1
}

run_test "acme: fresh install serves the issued certificate from live/<domain>" test_fresh_install_serves_issued_certificate
run_test "acme: installer re-run is idempotent (no -0001 lineage, no 2nd order)" test_rerun_is_idempotent
run_test "acme: issued key and chain are readable by nginx (uid 101)"           test_issued_material_readable_by_nginx
run_test "acme: failed challenge keeps the placeholder (control)"               test_failed_challenge_keeps_placeholder

echo
printf '%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
