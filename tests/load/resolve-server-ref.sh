#!/usr/bin/env bash
#
# resolve-server-ref.sh — resolves the server_ref input of
# .github/workflows/load.yml (F-118, WP-437): the commit the server, the seed
# and the migrations of a load run are built from. tests/load and
# tests/integrity-check.sql always come from the run's commit.
#
# Usage: tests/load/resolve-server-ref.sh <server_ref> [<default>]
#
# An empty server_ref is <default> (load.yml: the run's commit, otherwise
# HEAD). A ref is a commit id (also abbreviated), a tag or a branch, also one
# that only exists as origin/<branch> or on the remote, which is fetched,
# optionally with a parent suffix (<merge>^, <commit>~2). Prints the full
# commit id. A ref that is no commit of the repository, or a commit without
# the server, the seed or the migrations, prints an ::error:: line for the
# workflow log and exits 1, before anything is built or seeded.
set -uo pipefail

ref="${1-}"
default="${2:-HEAD}"
[ -n "$ref" ] || ref="$default"

refuse() {
    printf '::error title=server_ref::%s\n' "$1"
    exit 1
}

# Only the characters of commit ids, tags, branch names and parent
# suffixes: a leading dash would be an option of git, a range is no commit.
case "$ref" in
    -* | *..* | *[!A-Za-z0-9._/~^-]*)
        refuse "server_ref '$ref' is not a commit id, tag or branch name" ;;
esac

commit() {
    git rev-parse --verify --quiet "$1^{commit}" 2>/dev/null
}

sha="$(commit "$ref")" || sha="$(commit "refs/remotes/origin/$ref")" || sha=""
if [ -z "$sha" ] && git fetch --quiet --no-tags origin "$ref" 2>/dev/null; then
    sha="$(commit FETCH_HEAD)" || sha=""
fi
[ -n "$sha" ] || refuse "server_ref '$ref' is no commit of this repository (neither a commit id, tag or branch here nor on origin)"

for path in backend/cmd/server/main.go backend/cmd/seed/main.go backend/migrations; do
    git cat-file -e "$sha:$path" 2>/dev/null ||
        refuse "server_ref '$ref' ($sha) has no $path: the load test builds its server, seed and migrations from it"
done
printf '%s\n' "$sha"
