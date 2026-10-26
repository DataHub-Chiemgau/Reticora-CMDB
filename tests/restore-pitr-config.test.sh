#!/usr/bin/env bash
#
# restore-pitr-config.test.sh — the parts of the restore proof that need no
# backup chain (WP-445, NFR-05, OPS-04). Runs in the ci.yml job gate; the
# proof itself (tests/restore-pitr.sh) runs in
# .github/workflows/restore-test.yml.
#
#   - restore_rpo_window: the default write window of restore-pitr.sh is at
#     least 1000 s and longer than both RPO limits, so a data loss above
#     them is observable (retest F-130, case moved from
#     tests/audit/ops-config.test.sh), and ends 2 min before the
#     archive_timeout of the rpo-negative job, so that run archives nothing
#     of it;
#   - rpo_eval_*: rpo_result of restore-pitr.sh with fixture numbers: the
#     line "RPO … PASS|FAIL" with both limits; PASS up to archive_timeout
#     300 s + 60 s, FAIL above it, above 15 min and when nothing was
#     measured;
#   - stub_run_*: restore-pitr.sh against stubs (Compose and psql answer
#     from fixture numbers; no Docker): the default run passes with every
#     check in the protocol, the RPO of the negative self-test fails exactly
#     the RPO line, an archive_timeout that did not take effect aborts, the
#     seed runs migrate and cmd/seed before the first base backup without
#     the password on a command line;
#   - rpo_negative_*: the step of the job rpo-negative (restore-test.yml)
#     against a stub restore-pitr.sh: it passes only for exit 1 with the
#     line 'RPO … FAIL' above both limits and no other failed check, also
#     with the protocol of the stub run above;
#   - restore_workflow: restore-test.yml runs the default proof, the seeded
#     run and the negative self-test with archive_timeout=1200, uploads the
#     protocols, every timeout leaves room for the write window; the ci.yml
#     job gate runs this file;
#   - restore_override: the Compose override of the throwaway project: no
#     host port, the postgres command of docker-compose.yml with another
#     archive_timeout, a loopback port for the seed; docker compose config
#     renders it as intended;
#   - restore_syntax / restore_shellcheck: bash -n and shellcheck -x.
#
# Tools: python3 with PyYAML, shellcheck (SHELLCHECK=<path> or PATH),
# openssl and docker compose (config only). A missing tool is a FAIL of its
# case.
#
# Usage: [SHELLCHECK=<shellcheck>] ./tests/restore-pitr-config.test.sh
#
# Single quotes hold patterns literally (SC2016); the stubs replace
# functions of restore-pitr.sh and are called from it (SC2317, from version
# 0.11 SC2329).
# shellcheck disable=SC2016,SC2317,SC2329
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; [ -z "${2:-}" ] || printf '%s\n' "$2" | head -n 30 | sed 's/^/     /'; return 0; }

tool() { # tool <ENVVAR> <name>
    local v="${!1:-}"
    if [ -n "$v" ]; then printf '%s' "$v"; return 0; fi
    command -v "$2" 2>/dev/null
}

PITR=tests/restore-pitr.sh
WORKFLOW=.github/workflows/restore-test.yml
COMPOSE=deploy/docker-compose/docker-compose.yml
RPO_LINE='| RPO: data loss after losing the data volume (latest restore) |'
LIMITS='360s (archive_timeout 300s + 60s), 900s (NFR-05)'
# Settings of restore-pitr.sh from the environment; the cases run with the
# defaults.
SETTINGS=(RESTORE_ARCHIVE_TIMEOUT RESTORE_SEED_OBJECTS RESTORE_SEED_BIN RESTORE_MIGRATE_BIN RESTORE_WRITE_SECONDS RESTORE_ROWS
    RESTORE_REPORT RETICORA_RPO_LIMIT_SECONDS RETICORA_RPO_MARGIN_SECONDS RETICORA_RTO_LIMIT_SECONDS RETICORA_POSTGRES_IMAGE)

# Sourcing a restore-pitr.sh without the guard would run the proof.
SOURCEABLE=0
grep -q '^if \[ "${BASH_SOURCE\[0\]}" = "$0" \]; then' "$PITR" && SOURCEABLE=1
not_sourceable() { fail "$1" "restore-pitr.sh runs the proof when sourced (no BASH_SOURCE guard): not sourced"; }

# ─── The step of the job rpo-negative (restore-test.yml) ────────────────────
HAVE_YAML=0
python3 -c 'import yaml' 2>/dev/null && HAVE_YAML=1
# negative_step_files <script> <env>: the run script of the rpo-negative
# step that sets RESTORE_ARCHIVE_TIMEOUT, and its env as KEY=VALUE lines
# (${{ github.workspace }} as @WS@). Fails without such a step.
negative_step_files() {
    python3 - "$WORKFLOW" "$1" "$2" <<'PY'
import sys, yaml
wf, script, env = sys.argv[1:4]
steps = (yaml.safe_load(open(wf)).get("jobs", {}).get("rpo-negative") or {}).get("steps") or []
workspace = "${{ github.workspace }}"
for s in steps:
    e = s.get("env") or {}
    if "RESTORE_ARCHIVE_TIMEOUT" in e and s.get("run"):
        open(script, "w").write(s["run"])
        open(env, "w").write("".join(k + "=" + str(v).replace(workspace, "@WS@") + "\n" for k, v in e.items()))
        sys.exit(0)
sys.exit(1)
PY
}

# ─── WP-101/WP-445: the write window can observe an RPO violation (F-130) ───
rpo_limit="$(sed -nE 's/^RPO_LIMIT="\$\{RETICORA_RPO_LIMIT_SECONDS:-([0-9]+)\}"/\1/p' "$PITR")"
margin="$(sed -nE 's/^RPO_MARGIN="\$\{RETICORA_RPO_MARGIN_SECONDS:-([0-9]+)\}"/\1/p' "$PITR")"
write_window="$(sed -nE 's/^WRITE_SECONDS="\$\{RESTORE_WRITE_SECONDS:-([0-9]+)\}"/\1/p' "$PITR")"
archive_timeout="$(awk '$0 == "  postgres:" {f=1; next} f && /^  [^ #]/ {exit} f' "$COMPOSE" | sed -nE 's/^ *- archive_timeout=([0-9]+)$/\1/p')"
negative_timeout=""
if [ "$HAVE_YAML" = 1 ] && negative_step_files "$TMP_ROOT/negative-step.sh" "$TMP_ROOT/negative-step.env"; then
    negative_timeout="$(sed -n 's/^RESTORE_ARCHIVE_TIMEOUT=//p' "$TMP_ROOT/negative-step.env")"
fi
problems=""
if [ -z "$rpo_limit" ] || [ -z "$margin" ] || [ -z "$write_window" ] || [ -z "$archive_timeout" ] || ! [[ "$negative_timeout" =~ ^[0-9]+$ ]]; then
    problems="defaults RPO_LIMIT=${rpo_limit:-?} RPO_MARGIN=${margin:-?} WRITE_SECONDS=${write_window:-?}, archive_timeout of $COMPOSE=${archive_timeout:-?}, RESTORE_ARCHIVE_TIMEOUT of the job rpo-negative=${negative_timeout:-?}"
else
    window=$((archive_timeout + margin))
    [ "$write_window" -ge 1000 ] || problems="$problems WRITE_SECONDS=${write_window}s < 1000s;"
    [ "$write_window" -gt "$rpo_limit" ] || problems="$problems WRITE_SECONDS=${write_window}s <= RPO_LIMIT=${rpo_limit}s: rpo = last_commit - restored_last stays below the limit whenever the restore succeeds;"
    [ "$write_window" -gt "$window" ] || problems="$problems WRITE_SECONDS=${write_window}s <= archive cycle ${window}s;"
    [ $((write_window + 120)) -le "$negative_timeout" ] || problems="$problems WRITE_SECONDS=${write_window}s + 120s > archive_timeout ${negative_timeout}s of rpo-negative: that run may archive the window and pass;"
fi
if [ -z "$problems" ]; then
    ok "restore_rpo_window: the write window (${write_window}s) exceeds both RPO limits (${window}s archive cycle, ${rpo_limit}s NFR-05) and ends $((negative_timeout - write_window))s before the archive_timeout of rpo-negative (${negative_timeout}s)"
else
    fail "restore_rpo_window: the write window exceeds the RPO limits, so an RPO violation is observable" "$problems"
fi

# ─── The RPO line against fixture numbers ───────────────────────────────────
# rpo_row <rpo> <lost> <committed> <archive_timeout>: the protocol line of
# rpo_result and then FAILED, from a sourced restore-pitr.sh with defaults.
rpo_row() {
    (
        unset "${SETTINGS[@]}"
        # shellcheck source=tests/restore-pitr.sh
        . "$PITR"
        REPORT="$TMP_ROOT/rpo-row.md"
        : > "$REPORT"
        FAILED=0
        rpo_result "$@" >/dev/null
        cat "$REPORT"
        printf '%s\n' "$FAILED"
    )
}
# expect_row <case> <rpo> <lost> <committed> <archive_timeout> <expected line> <expected FAILED>
expect_row() {
    local name="$1" want="$6"$'\n'"$7" got
    got="$(rpo_row "$2" "$3" "$4" "$5" 2>&1)"
    if [ "$got" = "$want" ]; then ok "$name"; else fail "$name" "got:"$'\n'"$got"$'\n'"want:"$'\n'"$want"; fi
}
if [ "$SOURCEABLE" = 0 ]; then
    not_sourceable "rpo_eval: rpo_result of restore-pitr.sh"
else
    expect_row "rpo_eval_pass: 140.2 s → PASS, the line names both limits" 140.2 7050 86000 300 \
        "$RPO_LINE 140.2s (7050 of 86000 rows lost) | $LIMITS | PASS |" 0
    expect_row "rpo_eval_boundary: 360.0 s (archive_timeout 300 s + 60 s) → PASS" 360.0 1 2 300 \
        "$RPO_LINE 360.0s (1 of 2 rows lost) | $LIMITS | PASS |" 0
    expect_row "rpo_eval_archive_cycle: 360.1 s → FAIL above the archive cycle, below 15 min" 360.1 1 2 300 \
        "$RPO_LINE 360.1s, above 360s (1 of 2 rows lost) | $LIMITS | FAIL |" 1
    expect_row "rpo_eval_nfr: 1043.5 s → FAIL above both limits" 1043.5 52150 87150 300 \
        "$RPO_LINE 1043.5s, above 360s and 900s (52150 of 87150 rows lost) | $LIMITS | FAIL |" 1
    expect_row "rpo_eval_unmeasured: no measured value → FAIL" "" 0 0 300 \
        "$RPO_LINE not measured ('') | $LIMITS | FAIL |" 1
    expect_row "rpo_eval_negative_value: a negative RPO is no measurement → FAIL" -3.0 0 10 300 \
        "$RPO_LINE not measured ('-3.0') | $LIMITS | FAIL |" 1
fi

# ─── restore-pitr.sh against stubs ──────────────────────────────────────────
LAST_COMMIT=1761472800.0
# stub_run <case dir> [VAR=value …]: main of restore-pitr.sh in a subshell
# with the settings given and Docker replaced: compose records its calls,
# sql answers from the FAKE_* fixture, restore prints FAKE_RTO and marks the
# data volume as restored, the write window is 1 s. Prints the exit code;
# protocol <dir>/report.md, calls <dir>/calls, env file and override as
# Compose got them <dir>/env, <dir>/override.yml.
stub_run() {
    local dir="$1"; shift
    mkdir -p "$dir"
    : > "$dir/calls"
    (
        S="$dir"
        unset "${SETTINGS[@]}"
        for kv in "$@"; do export "${kv?}"; done
        # Nothing of the stub run may reach Docker.
        docker() { echo "docker $*" >> "$S/calls"; echo "config test: docker is not called in a stub run" >&2; return 1; }
        # shellcheck source=tests/restore-pitr.sh
        . "$PITR"
        REPORT="$S/report.md"
        WRITE_SECONDS=1
        sleep() { command sleep 0.05; }
        wait_healthy() { echo "wait_healthy $*" >> "$S/calls"; }
        compose() {
            echo "compose $*" >> "$S/calls"
            case "$*" in
                "up -d --no-build postgres") cp "$ENV_FILE" "$S/env"; cp "$OVERRIDE" "$S/override.yml" ;;
                "port postgres 5432") echo "127.0.0.1:55999" ;;
                "exec -T backup "*) return "${FAKE_CHECK_RC:-0}" ;;
            esac
            return 0
        }
        restore() {
            echo "restore $*" >> "$S/calls"
            [ "$1" != kill ] || : > "$S/restored"
            printf '%s' "${FAKE_RTO:-42}"
        }
        sql() {
            local restored=0
            [ ! -e "$S/restored" ] || restored=1
            case "$1" in
                *"name = 'archive_timeout'"*) echo "${FAKE_RUNNING:-300}" ;;
                *pg_database_size*) echo 7 ;;
                *"to_char(clock_timestamp()"*) echo "2026-10-26 10:00:00.000000+00" ;;
                *"WHERE phase = 'A'"*) echo "${FAKE_PHASE_A:-50000}" ;;
                *"WHERE phase = 'B'"*) echo "${FAKE_PHASE_B:-0}" ;;
                *"extract(epoch FROM max(at))"*)
                    if [ "$restored" = 1 ]; then awk -v a="$LAST_COMMIT" -v r="${FAKE_RPO:-140.2}" 'BEGIN { printf "%.6f\n", a - r }'; else echo "$LAST_COMMIT"; fi ;;
                *"SELECT count(*) FROM pitr_probe"*)
                    if [ "$restored" = 1 ]; then echo "${FAKE_RESTORED:-79000}"; else echo "${FAKE_COMMITTED:-86050}"; fi ;;
            esac
            echo "sql $1" >> "$S/calls"
        }
        set -euo pipefail
        main
    ) > "$dir/out.log" 2>&1
    printf '%s' "$?"
}

# expect_run <case> <want exit> <dir> <rc> <grep -E pattern of the protocol …>
expect_run() {
    local name="$1" want="$2" dir="$3" rc="$4" missing=""; shift 4
    for p in "$@"; do
        grep -qE -- "$p" "$dir/report.md" 2>/dev/null || missing="$missing"$'\n'"  missing: $p"
    done
    if grep -q '^docker ' "$dir/calls"; then missing="$missing"$'\n'"  docker was called"; fi
    if [ "$rc" = "$want" ] && [ -z "$missing" ]; then
        ok "$name"
    else
        fail "$name" "exit $rc (want $want)$missing"$'\n'"$(cat "$dir/report.md" 2>/dev/null; tail -n 8 "$dir/out.log")"
    fi
}

q() { printf '%s' "$1" | sed -E 's/[][\.|$*+?(){}^]/\\&/g'; } # literal text as grep -E pattern

if [ "$SOURCEABLE" = 0 ]; then
    not_sourceable "stub_run: restore-pitr.sh against stubs"
elif ! command -v openssl >/dev/null; then
    fail "stub_run: openssl available" "openssl not found"
else
    d="$TMP_ROOT/run-default"
    rc="$(stub_run "$d")"
    expect_run "stub_run_default: defaults, RPO 140.2 s → exit 0, every check PASS, both RPO limits in the protocol" 0 "$d" "$rc" \
        '^- Limits: RPO 360s \(archive_timeout 300s of docker-compose\.yml \+ 60s\) and 900s \(NFR-05\), RTO 14400s$' \
        '^- Database under test: archive_timeout 300s, write window 1s, data: pitr_probe workload only$' \
        '^\| PITR to 2026-10-26 10:00:00\.000000\+00: rows before the target \| 50000 \| 50000 \| PASS \|$' \
        '^\| PITR to 2026-10-26 10:00:00\.000000\+00: rows after the target \| 0 \| 0 \| PASS \|$' \
        '^\| RTO: point in time restore \| 42s \| 14400s \| PASS \|$' \
        "^$(q "$RPO_LINE 140.2s (7050 of 86050 rows lost) | $LIMITS | PASS |")\$" \
        '^\| RTO: restore of the latest state \| 42s \| 14400s \| PASS \|$' \
        '^\| WAL archiving after the restore \| pgbackrest check ok \| ok \| PASS \|$' \
        '^- Database size at the first base backup: 7 MB$' \
        '^\*\*Result:\*\* passed$'
    # The stack: no host port, the shipped command, the daily backup 12 h away.
    hour="$(sed -n 's/^RETICORA_BACKUP_HOUR=//p' "$d/env" 2>/dev/null)"
    want_hour=$(((10#$(date -u +%H) + 12) % 24))
    if [ "$(cat "$d/override.yml" 2>/dev/null)" = $'services:\n  postgres:\n    ports: !reset []' ] \
        && { [ "$hour" = "$want_hour" ] || [ "$hour" = $(((want_hour + 1) % 24)) ]; } \
        && grep -q '^compose exec -T postgres pg_isready -q -h 127\.0\.0\.1 -U reticora$' "$d/calls"; then
        ok "stub_run_default_stack: no host port, no command override, RETICORA_BACKUP_HOUR ${hour} (12 h away), readiness over TCP"
    else
        fail "stub_run_default_stack: no host port, no command override, RETICORA_BACKUP_HOUR 12 h away, readiness over TCP" \
            "override:"$'\n'"$(cat "$d/override.yml" 2>/dev/null)"$'\n'"RETICORA_BACKUP_HOUR=${hour:-?} (want $want_hour)"
    fi

    d="$TMP_ROOT/run-negative"
    rc="$(stub_run "$d" RESTORE_ARCHIVE_TIMEOUT=1200 FAKE_RUNNING=1200 FAKE_RPO=1043.5 FAKE_RESTORED=34900)"
    expect_run "stub_run_negative: archive_timeout 1200 s, RPO 1043.5 s → exit 1, the RPO line FAIL above both limits, every other check PASS" 1 "$d" "$rc" \
        '^- Database under test: archive_timeout 1200s \(RESTORE_ARCHIVE_TIMEOUT, negative self-test\), write window 1s' \
        "^$(q "$RPO_LINE 1043.5s, above 360s and 900s (51150 of 86050 rows lost) | $LIMITS | FAIL |")\$" \
        '^\| RTO: restore of the latest state \| 42s \| 14400s \| PASS \|$' \
        '^\| WAL archiving after the restore \| pgbackrest check ok \| ok \| PASS \|$' \
        '^\*\*Result:\*\* FAILED$'
    if [ "$(grep -c ' | FAIL |$' "$d/report.md" 2>/dev/null)" != 1 ] || ! grep -qF 'command: ["postgres", "-c", "archive_mode=on", "-c", "archive_command=pgbackrest --stanza=reticora archive-push %p", "-c", "archive_timeout=1200"]' "$d/override.yml"; then
        fail "stub_run_negative_override: only the RPO line fails; the override runs the postgres command with archive_timeout=1200" \
            "$(cat "$d/override.yml" 2>/dev/null)"$'\n'"$(grep ' | FAIL |$' "$d/report.md" 2>/dev/null)"
    else
        ok "stub_run_negative_override: only the RPO line fails; the override runs the postgres command of docker-compose.yml with archive_timeout=1200"
    fi

    d="$TMP_ROOT/run-window"
    rc="$(stub_run "$d" FAKE_RPO=412.0)"
    expect_run "stub_run_window: shipped archive_timeout, RPO 412.0 s (archive cycle missed, below 15 min) → exit 1" 1 "$d" "$rc" \
        "^$(q "$RPO_LINE 412.0s, above 360s (7050 of 86050 rows lost) | $LIMITS | FAIL |")\$" '^\*\*Result:\*\* FAILED$'

    d="$TMP_ROOT/run-pitr"
    rc="$(stub_run "$d" RESTORE_ARCHIVE_TIMEOUT=1200 FAKE_RUNNING=1200 FAKE_RPO=1043.5 FAKE_PHASE_B=1000)"
    expect_run "stub_run_pitr_inexact: rows after the PITR target → that line FAIL as well" 1 "$d" "$rc" \
        '^\| PITR to [^|]*: rows after the target \| 1000 \| 0 \| FAIL \|$' "^$(q "$RPO_LINE 1043.5s, above 360s and 900s")"

    d="$TMP_ROOT/run-not-applied"
    rc="$(stub_run "$d" RESTORE_ARCHIVE_TIMEOUT=1200 FAKE_RUNNING=300)"
    if [ "$rc" = 2 ] && ! grep -q '^| RPO: ' "$d/report.md" &&! grep -q 'up -d --no-build backup' "$d/calls" \
        && grep -q 'the database runs with archive_timeout 300s, expected 1200s' "$d/out.log"; then
        ok "stub_run_not_applied: archive_timeout 1200 s requested, the database runs with 300 s → exit 2 before any measurement"
    else
        fail "stub_run_not_applied: archive_timeout 1200 s requested, the database runs with 300 s → exit 2 before any measurement" \
            "exit $rc"$'\n'"$(tail -n 5 "$d/out.log")"
    fi

    d="$TMP_ROOT/run-seed"
    mkdir -p "$d/bin"
    for b in migrate seed; do
        printf '#!/bin/sh\necho "%s $*" >> "%s/calls"\nprintf "%%s" "$PGPASSWORD" > "%s/%s.pgpassword"\n' "$b" "$d" "$d" "$b" > "$d/bin/$b"
        chmod +x "$d/bin/$b"
    done
    rc="$(stub_run "$d" RESTORE_SEED_OBJECTS=10000 RESTORE_SEED_BIN="$d/bin/seed" RESTORE_MIGRATE_BIN="$d/bin/migrate")"
    expect_run "stub_run_seed: seed -objects 10000 → exit 0, the data in the protocol" 0 "$d" "$rc" \
        '^- Database under test: archive_timeout 300s, write window 1s, data: demo organization with 10000 CIs \(seed -objects 10000\) and the pitr_probe workload$' \
        '^\*\*Result:\*\* passed$'
    password="$(sed -n 's/^RETICORA_DB_PASSWORD=//p' "$d/env" 2>/dev/null)"
    url="postgres://reticora@127.0.0.1:55999/reticora?sslmode=disable"
    order="$(grep -nE "^(migrate|seed|compose up -d --no-build backup|sql CREATE TABLE pitr_probe)" "$d/calls" | cut -d: -f2- | cut -c1-40)"
    if grep -qxF "migrate -path $PWD/backend/migrations -database $url up" "$d/calls" \
        && grep -qxF "seed -database-url $url -objects 10000" "$d/calls" \
        && [ "$(printf '%s\n' "$order" | cut -d' ' -f1 | tr '\n' ' ')" = "migrate seed sql compose " ] \
        && [ -n "$password" ] && [ "$(cat "$d/migrate.pgpassword")" = "$password" ] && [ "$(cat "$d/seed.pgpassword")" = "$password" ] \
        && ! grep -qF -- "$password" "$d/calls" \
        && grep -qx '    ports: !override \["127.0.0.1::5432"\]' "$d/override.yml"; then
        ok "stub_run_seed_order: migrate and seed against the loopback port before pitr_probe and the first base backup, password only in PGPASSWORD"
    else
        fail "stub_run_seed_order: migrate and seed against the loopback port before pitr_probe and the first base backup, password only in PGPASSWORD" \
            "$(grep -E '^(migrate|seed|compose up|compose port)' "$d/calls")"$'\n'"$(cat "$d/override.yml" 2>/dev/null)"
    fi

    d="$TMP_ROOT/run-seed-nobin"
    rc="$(stub_run "$d" RESTORE_SEED_OBJECTS=10)"
    if [ "$rc" = 2 ] && ! grep -q '^compose ' "$d/calls" && grep -q 'RESTORE_SEED_OBJECTS needs RESTORE_SEED_BIN' "$d/out.log"; then
        ok "stub_run_seed_missing: RESTORE_SEED_OBJECTS without RESTORE_SEED_BIN → exit 2 before Compose"
    else
        fail "stub_run_seed_missing: RESTORE_SEED_OBJECTS without RESTORE_SEED_BIN → exit 2 before Compose" "exit $rc"$'\n'"$(tail -n 3 "$d/out.log")"
    fi
fi

# ─── The expectation of the job rpo-negative ────────────────────────────────
# negative_step <case> <protocol or ""> <exit code>: the run script of the
# rpo-negative step in a scratch workspace whose tests/restore-pitr.sh writes
# that protocol to RESTORE_REPORT and exits with that code; env as in the
# workflow, bash -e as on the runner. Prints the step's exit code; output in
# <workspace>/step.log.
negative_step() {
    local ws="$TMP_ROOT/negative-$1" envs=() k v
    mkdir -p "$ws/tests"
    printf '#!/usr/bin/env bash\n[ -z "%s" ] || cp "%s" "$RESTORE_REPORT"\nexit %s\n' "$2" "$2" "$3" > "$ws/tests/restore-pitr.sh"
    chmod +x "$ws/tests/restore-pitr.sh"
    while IFS='=' read -r k v; do envs+=("$k=${v//@WS@/$ws}"); done < "$TMP_ROOT/negative-step.env"
    (cd "$ws" && env "${envs[@]}" bash -e "$TMP_ROOT/negative-step.sh") > "$ws/step.log" 2>&1
    printf '%s' "$?"
}
# expect_step <case> <protocol or ""> <exit code> <want: pass|fail> [message the step prints]
expect_step() {
    local got
    got="$(negative_step "$(printf '%s' "$1" | cut -d: -f1)" "$2" "$3")"
    if { [ "$4" = pass ] && [ "$got" = 0 ]; } || { [ "$4" = fail ] && [ "$got" != 0 ] && grep -qF -- "${5:-::error::}" "$TMP_ROOT/negative-$(printf '%s' "$1" | cut -d: -f1)/step.log"; }; then
        ok "$1"
    else
        fail "$1" "step exit $got (want $4)"$'\n'"$(cat "$TMP_ROOT/negative-$(printf '%s' "$1" | cut -d: -f1)/step.log")"
    fi
}
if [ "$HAVE_YAML" = 0 ]; then
    fail "rpo_negative: python3 with PyYAML available" "import yaml failed"
elif [ ! -s "$TMP_ROOT/negative-step.sh" ]; then
    fail "rpo_negative: restore-test.yml has a job rpo-negative with a step that sets RESTORE_ARCHIVE_TIMEOUT" "no such step in $WORKFLOW"
elif [ "$SOURCEABLE" = 0 ] || [ ! -s "$TMP_ROOT/run-negative/report.md" ]; then
    fail "rpo_negative: protocols of the stub runs available" "no stub runs"
else
    expect_step "rpo_negative_expected: exit 1, the RPO line FAIL above both limits (protocol of stub_run_negative) → the job passes" \
        "$TMP_ROOT/run-negative/report.md" 1 pass
    expect_step "rpo_negative_script_passed: restore-pitr.sh passes → the job fails" \
        "$TMP_ROOT/run-default/report.md" 0 fail "passed although the WAL was archived only every 1200 s"
    expect_step "rpo_negative_window_only: the RPO line FAIL only above the archive cycle → the job fails" \
        "$TMP_ROOT/run-window/report.md" 1 fail "no line 'RPO … FAIL' above both limits"
    expect_step "rpo_negative_other_check: the RPO line and the PITR check FAIL → the job fails" \
        "$TMP_ROOT/run-pitr/report.md" 1 fail "checks other than the RPO failed"
    expect_step "rpo_negative_aborted: exit 2 after a partial protocol → the job fails" \
        "$TMP_ROOT/run-not-applied/report.md" 2 fail "aborted with exit 2"
    expect_step "rpo_negative_no_protocol: exit 1 without a protocol → the job fails" "" 1 fail "aborted with exit 1"
    sed 's/ | FAIL |$/ | PASS |/' "$TMP_ROOT/run-negative/report.md" > "$TMP_ROOT/negative-rpo-pass.md"
    expect_step "rpo_negative_rpo_pass: exit 1 but the RPO line PASS → the job fails" \
        "$TMP_ROOT/negative-rpo-pass.md" 1 fail "no line 'RPO … FAIL' above both limits"
fi

# ─── restore-test.yml and the ci.yml job gate ───────────────────────────────
if [ "$HAVE_YAML" = 0 ]; then
    fail "restore_workflow: python3 with PyYAML available" "import yaml failed"
elif out="$(python3 - "$WORKFLOW" "${write_window:-0}" <<'PY'
import sys, yaml
wf_path, write_window = sys.argv[1], int(sys.argv[2])
wf = yaml.safe_load(open(wf_path))
problems = []
on = wf.get("on", wf.get(True)) or {}
if "workflow_dispatch" not in on:
    problems.append("no workflow_dispatch trigger")
paths = ((on.get("pull_request") or {}).get("paths")) or []
for p in ("tests/restore-pitr.sh", "deploy/docker-compose/docker-compose.yml", "deploy/docker-compose/postgres/**", ".github/workflows/restore-test.yml"):
    if p not in paths:
        problems.append("pull_request paths without " + p)
jobs = wf.get("jobs") or {}
# The overrides a job may set: everything else runs with the defaults.
ALLOWED = {
    "pitr": {"RESTORE_REPORT": None},
    "pitr-seeded": {"RESTORE_REPORT": None, "RESTORE_SEED_OBJECTS": "10000", "RESTORE_SEED_BIN": None},
    "rpo-negative": {"RESTORE_REPORT": None, "RESTORE_ARCHIVE_TIMEOUT": "1200"},
}
REPORTS = {"pitr": "restore-pitr-report.md", "pitr-seeded": "restore-pitr-seeded-report.md",
           "rpo-negative": "restore-pitr-negative-report.md"}
# Image build, base backup, seed and two restores besides the write window.
HEADROOM = {"pitr": 1200, "pitr-seeded": 1800, "rpo-negative": 1200}
for name, allowed in ALLOWED.items():
    job = jobs.get(name)
    if not job:
        problems.append("no job " + name)
        continue
    steps = job.get("steps") or []
    runs = [s for s in steps if "./tests/restore-pitr.sh" in (s.get("run") or "")]
    if len(runs) != 1:
        problems.append(f"{name}: {len(runs)} steps run tests/restore-pitr.sh")
        continue
    env = runs[0].get("env") or {}
    if set(env) != set(allowed):
        problems.append(f"{name}: env {sorted(env)}, want {sorted(allowed)}")
    for k, v in allowed.items():
        if v is not None and str(env.get(k)) != v:
            problems.append(f"{name}: {k}={env.get(k)!r}, want {v!r}")
    report = REPORTS[name]
    if env.get("RESTORE_REPORT") != "${{ github.workspace }}/" + report:
        problems.append(f"{name}: RESTORE_REPORT {env.get('RESTORE_REPORT')!r}, want ${{{{ github.workspace }}}}/{report}")
    if name != "rpo-negative" and runs[0]["run"].strip() != "./tests/restore-pitr.sh":
        problems.append(f"{name}: run {runs[0]['run']!r}")
    uploads = [s for s in steps if str(s.get("uses", "")).startswith("actions/upload-artifact@")]
    if not uploads or (uploads[0].get("with") or {}).get("path") != report or uploads[0].get("if") != "always()" \
            or (uploads[0].get("with") or {}).get("name") != report[:-3]:
        problems.append(f"{name}: the protocol {report} is not uploaded as artifact {report[:-3]} (if: always())")
    timeout = job.get("timeout-minutes")
    if not isinstance(timeout, int) or timeout * 60 < write_window + HEADROOM[name]:
        problems.append(f"{name}: timeout-minutes {timeout!r} < write window {write_window}s + {HEADROOM[name]}s")
seeded = jobs.get("pitr-seeded") or {}
runs = " ".join(s.get("run") or "" for s in seeded.get("steps") or [])
if 'go build -o "$RUNNER_TEMP/seed" ./cmd/seed' not in runs or "golang-migrate/migrate/v4/cmd/migrate@" not in runs:
    problems.append("pitr-seeded: builds no seed or installs no migrate")
neg = jobs.get("rpo-negative") or {}
run = next((s.get("run") or "" for s in neg.get("steps") or [] if "RESTORE_ARCHIVE_TIMEOUT" in (s.get("env") or {})), "")
if not run.lstrip().startswith("set +e") or "rc=$?" not in run:
    problems.append("rpo-negative: the step does not catch the exit code of restore-pitr.sh")
ci = yaml.safe_load(open(".github/workflows/ci.yml"))
if not any((s.get("run") or "").strip() == "./tests/restore-pitr-config.test.sh" for s in ci["jobs"]["gate"]["steps"]):
    problems.append("ci.yml job gate does not run tests/restore-pitr-config.test.sh")
print("\n".join(problems))
PY
)" && [ -z "$out" ]; then
    ok "restore_workflow: default proof, seeded run (10000 CIs) and rpo-negative (archive_timeout 1200) with defaults otherwise, protocols uploaded, timeouts above the write window; gate runs this file"
else
    fail "restore_workflow: restore-test.yml and the ci.yml job gate" "$out"
fi

# ─── The Compose override of the throwaway project ──────────────────────────
# render <compose files …>: docker compose config as JSON; the variables
# docker-compose.yml requires (:?) get dummy values.
render() {
    local required envs=() v files=()
    required="$(grep -ohE '\$\{[A-Z0-9_]+:\?' "$@" | sed -E 's/^\$\{//; s/:\?$//' | sort -u)"
    for v in $required; do envs+=("$v=config-test"); done
    for v in "$@"; do files+=(-f "$v"); done
    env "${envs[@]}" docker compose --project-directory "$(dirname "$1")" --env-file /dev/null "${files[@]}" config --format json
}
if [ "$SOURCEABLE" = 0 ]; then
    not_sourceable "restore_override: write_override of restore-pitr.sh"
elif ! docker compose version >/dev/null 2>&1; then
    fail "restore_override: docker compose available" "docker compose not found"
elif [ "$HAVE_YAML" = 0 ]; then
    fail "restore_override: python3 available" "import yaml failed"
else
    o="$TMP_ROOT/override"
    mkdir -p "$o"
    # A fixture compose file whose command needs YAML quoting and keeps a
    # Compose escape ($$, which docker compose config prints escaped).
    cat > "$o/fixture.yml" <<'YML'
services:
  postgres:
    image: postgres:16-alpine
    command:
      - postgres
      - -c
      - archive_command=sh -c "test -f \x && cp %p /a/$$HOME"
      - -c
      - archive_timeout=300
    ports:
      - "5432:5432"
YML
    if out="$(
        unset "${SETTINGS[@]}"
        # shellcheck source=tests/restore-pitr.sh
        . "$PITR"
        write_override "$o/default.yml" "" 0
        write_override "$o/negative.yml" 1200 0
        write_override "$o/seed.yml" "" 1
        COMPOSE_FILE="$o/fixture.yml" write_override "$o/fixture-override.yml" 900 0
        base=(deploy/docker-compose/docker-compose.yml deploy/docker-compose/docker-compose.override.yml)
        { render "${base[@]}" > "$o/shipped.json" &&
            render "${base[@]}" "$o/default.yml" > "$o/default.json" &&
            render "${base[@]}" "$o/negative.yml" > "$o/negative.json" &&
            render "${base[@]}" "$o/seed.yml" > "$o/seed.json" &&
            render "$o/fixture.yml" > "$o/fixture.json" &&
            render "$o/fixture.yml" "$o/fixture-override.yml" > "$o/fixture-override.json"; } 2> "$o/err" || { tail -n 5 "$o/err"; exit 1; }
        python3 - "$o" <<'PY'
import json, sys
d = sys.argv[1]
j = {n: json.load(open(f"{d}/{n}.json"))["services"]["postgres"] for n in ("shipped", "default", "negative", "seed", "fixture", "fixture-override")}
problems = []
def swapped(cmd, value):
    return [f"archive_timeout={value}" if a.startswith("archive_timeout=") else a for a in cmd]
shipped = j["shipped"]["command"]
if "archive_timeout=300" not in shipped:
    problems.append(f"shipped command without archive_timeout=300: {shipped}")
if j["default"]["command"] != shipped or j["default"].get("ports"):
    problems.append(f"default: command {j['default']['command']}, ports {j['default'].get('ports')}")
if j["negative"]["command"] != swapped(shipped, 1200) or j["negative"].get("ports"):
    problems.append(f"negative: command {j['negative']['command']}, ports {j['negative'].get('ports')}")
ports = j["seed"].get("ports") or []
if j["seed"]["command"] != shipped or len(ports) != 1 or ports[0].get("host_ip") != "127.0.0.1" \
        or int(ports[0].get("target", 0)) != 5432 or ports[0].get("published") not in (None, "", "0"):
    problems.append(f"seed: command {j['seed']['command']}, ports {ports}")
want = swapped(j["fixture"]["command"], 900)
if j["fixture"]["command"][2] != 'archive_command=sh -c "test -f \\x && cp %p /a/$$HOME"' or j["fixture-override"]["command"] != want:
    problems.append(f"fixture: {j['fixture']['command']} → {j['fixture-override']['command']}, want {want}")
print("\n".join(problems))
PY
    )" && [ -z "$out" ]; then
        ok "restore_override: no host port; the postgres command of docker-compose.yml with archive_timeout=1200 (quoting and Compose escapes kept); loopback port for the seed (docker compose config)"
    else
        fail "restore_override: the Compose override of restore-pitr.sh" "$out"
    fi
fi

# ─── Syntax and shellcheck ──────────────────────────────────────────────────
problems=""
for f in "$PITR" tests/restore-pitr-config.test.sh; do
    out="$(bash -n "$f" 2>&1)" || problems="$problems$f: $out"$'\n'
done
if [ -z "$problems" ]; then ok "restore_syntax: bash -n"; else fail "restore_syntax: bash -n" "$problems"; fi
SHELLCHECK_BIN="$(tool SHELLCHECK shellcheck || true)"
if [ -z "$SHELLCHECK_BIN" ]; then
    fail "restore_shellcheck: shellcheck available (set SHELLCHECK)" "shellcheck not found"
elif out="$("$SHELLCHECK_BIN" -x "$PITR" tests/restore-pitr-config.test.sh 2>&1)"; then
    ok "restore_shellcheck: shellcheck -x ($("$SHELLCHECK_BIN" --version | sed -n 's/^version: //p'))"
else
    fail "restore_shellcheck: shellcheck -x" "$out"
fi

echo
printf '%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
