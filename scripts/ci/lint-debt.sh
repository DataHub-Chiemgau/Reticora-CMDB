#!/usr/bin/env bash
#
# lint-debt.sh — the lint backlog per go.work module as a ratchet (WP-499,
# F-123, TST-04).
#
# backend/.golangci.yml limits the lint job to findings in code changed
# since issues.new-from-rev (WP-001). This script counts the findings of
# each module with the same configuration but without that baseline and
# compares the number with tests/lint-debt-baseline.txt: a number above its
# baseline fails, an equal or lower number passes (a lower one is reported
# so that the baseline can be lowered). The backlog is cleared by the
# follow-up work packages "Lint-Altlast" in docs/plan/phase2/
# implementierungsplan.md; the last of them removes new-from-rev and sets
# every baseline to 0.
#
# With GITHUB_STEP_SUMMARY set the numbers are appended to the job summary.
#
# Environment: GOLANGCI_LINT (default golangci-lint, the version pinned in
# the Makefile and ci.yml), LINT_DEBT_BASELINE (default
# tests/lint-debt-baseline.txt).
#
# Exit codes: 0 no number above its baseline; 1 a number above its
# baseline; 2 usage, missing baseline or golangci-lint error.
#
# Usage: scripts/ci/lint-debt.sh [module ...]   (default: every module of
#        the baseline file)
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
GOLANGCI_LINT="${GOLANGCI_LINT:-golangci-lint}"
baseline="${LINT_DEBT_BASELINE:-$root/tests/lint-debt-baseline.txt}"
config="$root/backend/.golangci.yml"

if [ ! -s "$baseline" ]; then
    echo "::error::lint debt baseline $baseline missing" >&2
    exit 2
fi
# baseline_of <module>: the number of the module in the baseline file.
baseline_of() { awk -v m="$1" '$1 == m && $2 ~ /^[0-9]+$/ { print $2; f = 1 } END { exit !f }' "$baseline"; }

if [ "$#" -gt 0 ]; then
    modules=("$@")
else
    mapfile -t modules < <(awk '$1 !~ /^#/ && NF >= 2 { print $1 }' "$baseline")
fi
[ "${#modules[@]}" -gt 0 ] || { echo "::error::no module to check" >&2; exit 2; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

status=0
rows=()
for m in "${modules[@]}"; do
    if ! want="$(baseline_of "$m")"; then
        echo "::error::module $m has no line in $baseline" >&2
        exit 2
    fi
    if [ ! -d "$root/$m" ]; then
        echo "::error::module directory $m not found" >&2
        exit 2
    fi
    out="$tmp/$m.json"
    # --new-from-rev= clears the baseline of the configuration; findings
    # never change the exit code, only real errors do.
    if ! (cd "$root/$m" && "$GOLANGCI_LINT" run --config="$config" --new-from-rev= \
            --issues-exit-code=0 --show-stats=false --output.json.path="$out" --output.text.path=stderr ./... 2>"$tmp/$m.log"); then
        echo "::error::golangci-lint failed for $m" >&2
        tail -n 20 "$tmp/$m.log" >&2
        exit 2
    fi
    if ! got="$(jq -er '(.Issues // []) | length' "$out")"; then
        echo "::error::golangci-lint wrote no JSON report for $m" >&2
        exit 2
    fi
    by_linter="$(jq -r '[(.Issues // [])[] | .FromLinter] | group_by(.) | map("\(.[0])=\(length)") | join(" ")' "$out")"
    if [ "$got" -gt "$want" ]; then
        verdict="above baseline"
        echo "::error::lint debt of $m rose: $got findings, baseline $want ($by_linter)"
        status=1
    elif [ "$got" -lt "$want" ]; then
        verdict="below baseline"
        echo "::notice::lint debt of $m fell: $got findings, baseline $want; lower the line in tests/lint-debt-baseline.txt ($by_linter)"
    else
        verdict="at baseline"
        echo "lint debt of $m: $got findings, baseline $want ($by_linter)"
    fi
    rows+=("| $m | $got | $want | $verdict | $by_linter |")
done

if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    {
        echo "### Lint debt without new-from-rev (F-123)"
        echo
        echo "| module | findings | baseline | verdict | by linter |"
        echo "|---|---:|---:|---|---|"
        printf '%s\n' "${rows[@]}"
    } >> "$GITHUB_STEP_SUMMARY"
fi

exit "$status"
