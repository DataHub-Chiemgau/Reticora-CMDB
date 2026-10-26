#!/usr/bin/env bash
#
# resolve-server-ref.test.sh — tests for tests/load/resolve-server-ref.sh,
# the server_ref step of .github/workflows/load.yml (F-118, WP-437), in a
# throwaway repository with a clone as the runner's checkout: an empty ref is
# the run's commit; commit ids, tags, branches and parent suffixes resolve,
# also a branch only on origin and a commit the checkout has not fetched; an
# unknown ref, an option, a range, another revision expression and a commit
# without the load harness's server, seed or migrations are refused with an
# ::error:: line before anything is built.
#
# Usage: ./tests/load/resolve-server-ref.test.sh
set -uo pipefail

cd "$(dirname "$0")/../.." || exit 1
SCRIPT="$(pwd)/tests/load/resolve-server-ref.sh"

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

PASS=0
FAIL=0

g() { git -c user.name=t -c user.email=t@example.invalid -c init.defaultBranch=main "$@"; }

# indent prints the output of a failed case below it.
indent() { while IFS= read -r line; do printf '     %s\n' "$line"; done <<<"$1"; }

# origin: c0 without the harness, c1 with server, seed and migrations (tag
# v1, branch feature), c2 on main; the checkout clones it, then origin gets
# the branch late and the commit c3 (branch side), which the checkout has
# not fetched.
ORIGIN="$TMP_ROOT/origin"
g init -q "$ORIGIN"
g -C "$ORIGIN" config uploadpack.allowAnySHA1InWant true
echo readme > "$ORIGIN/README"
g -C "$ORIGIN" add -A && g -C "$ORIGIN" commit -qm c0
C0="$(g -C "$ORIGIN" rev-parse HEAD)"
mkdir -p "$ORIGIN/backend/cmd/server" "$ORIGIN/backend/cmd/seed" "$ORIGIN/backend/migrations"
echo 'package main' > "$ORIGIN/backend/cmd/server/main.go"
echo 'package main' > "$ORIGIN/backend/cmd/seed/main.go"
echo 'SELECT 1;' > "$ORIGIN/backend/migrations/000001_init.up.sql"
g -C "$ORIGIN" add -A && g -C "$ORIGIN" commit -qm c1
C1="$(g -C "$ORIGIN" rev-parse HEAD)"
g -C "$ORIGIN" tag v1 && g -C "$ORIGIN" branch feature
echo change >> "$ORIGIN/README"
g -C "$ORIGIN" commit -qam c2
C2="$(g -C "$ORIGIN" rev-parse HEAD)"
TREE1="$(g -C "$ORIGIN" rev-parse "$C1^{tree}")"

CHECKOUT="$TMP_ROOT/checkout"
g clone -q "$ORIGIN" "$CHECKOUT"
g -C "$ORIGIN" branch late "$C1"
g -C "$ORIGIN" checkout -q -b side "$C1"
echo side >> "$ORIGIN/README"
g -C "$ORIGIN" commit -qam c3
C3="$(g -C "$ORIGIN" rev-parse HEAD)"
g -C "$ORIGIN" checkout -q main

# expect <name> <want: sha or "refuse"> <args…>
expect() {
    local name="$1" want="$2" rc=0 out
    shift 2
    out="$(cd "$CHECKOUT" && "$SCRIPT" "$@" 2>&1)" || rc=$?
    if { [ "$want" = refuse ] && [ "$rc" -ne 0 ] && grep -q '^::error title=server_ref::' <<<"$out"; } ||
        { [ "$want" != refuse ] && [ "$rc" -eq 0 ] && [ "$out" = "$want" ]; }; then
        PASS=$((PASS + 1)); printf 'ok   %s\n' "$name"
    else
        FAIL=$((FAIL + 1)); printf 'FAIL %s (exit %d, want %s)\n' "$name" "$rc" "$want"; indent "$out"
    fi
}

expect "empty server_ref is the run's commit" "$C2" "" "$C2"
expect "empty server_ref without a default is HEAD" "$C2" ""
expect "full commit id" "$C1" "$C1" "$C2"
expect "abbreviated commit id" "$C1" "${C1:0:10}" "$C2"
expect "tag" "$C1" v1 "$C2"
expect "remote branch of the checkout" "$C1" feature "$C2"
expect "branch only on origin is fetched" "$C1" late "$C2"
expect "commit the checkout has not fetched" "$C3" "$C3" "$C2"
expect "unknown ref is refused" refuse no-such-ref "$C2"
expect "unknown commit id is refused" refuse 0123456789abcdef0123456789abcdef01234567 "$C2"
expect "option is refused" refuse "--upload-pack=touch $TMP_ROOT/pwned" "$C2"
expect "range is refused" refuse "$C1..$C2" "$C2"
expect "parent of a commit" "$C1" "$C2^" "$C2"
expect "ancestor of a commit" "$C1" "${C2:0:10}~1" "$C2"
expect "parent of a commit without the harness is refused" refuse "$C1^" "$C2"
expect "revision expression is refused" refuse "HEAD@{1}" "$C2"
expect "path in a commit is refused" refuse "$C1:README" "$C2"
expect "tree is no commit" refuse "$TREE1" "$C2"
expect "commit without server, seed and migrations is refused" refuse "$C0" "$C2"

# The refusal names the ref and the reason.
out="$(cd "$CHECKOUT" && "$SCRIPT" "$C0" 2>&1)"
if grep -q "server_ref '$C0' ($C0) has no backend/cmd/server/main.go" <<<"$out" && [ ! -e "$TMP_ROOT/pwned" ]; then
    PASS=$((PASS + 1)); printf 'ok   %s\n' "the refusal names the missing path; no option reached git"
else
    FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "the refusal names the missing path; no option reached git"; indent "$out"
fi

echo
echo "passed: $PASS, failed: $FAIL"
[ "$FAIL" -eq 0 ]
