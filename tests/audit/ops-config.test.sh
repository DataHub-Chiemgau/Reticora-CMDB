#!/usr/bin/env bash
#
# ops-config.test.sh — Phase-1 acceptance audit, group T5, DEFECT tests for
# CI, release, backup, monitoring and outage configuration (fail until the
# configuration is fixed; not wired into CI).
#
# Findings encoded (one case each):
#   WP-004 (SEC-11) P2 "Released image reticora-postgres is never
#          vulnerability-scanned": fixed by WP-389; the case
#          image_scan_coverage is in tests/ci-config.test.sh.
#   WP-100 (NFR-05, OPS-04) P2 "K8s backup component provisions no WAL
#          archiving": the prod overlay must configure WAL archiving
#          (archive_mode / archive-push).                  -> case k8s_wal_archive
#   WP-100 (NFR-05) P2 "Backup bucket versioning/replication not enforced;
#          Terraform 'production requires replica' is warning-only": fixed
#          by WP-443; the cases terraform_replica_* are runs of
#          deploy/terraform/tests/backup.tftest.hcl (terraform test, CI job
#          terraform), the bucket versioning is in tests/backup-config.test.sh.
#   WP-101 (NFR-05) P2 "RPO limit check in restore-pitr.sh cannot fail with
#          default parameters": the continuous-write window of the RPO
#          scenario (420 s) is shorter than the RPO limit (900 s), so a data
#          loss above the limit is not observable.        -> case restore_rpo_window
#   WP-102 (TEC-15) P2 "No signature verification before installation":
#          fixed by WP-387; the cases install_signature_* are in
#          tests/install-signature.test.sh.
#   WP-103 (OPS-05) P2 "alertmanager.yaml is rejected by Alertmanager's config
#          validation (no global SMTP smarthost)": fixed by WP-446; the cases
#          alertmanager_smtp and alertmanager_amtool are in
#          tests/monitoring-config.test.sh.
#   WP-104 (OPS-06) P2 "SMTP outage scenario reported N/A although SMTP
#          delivery exists" (notify/smtp.go, WP-137).      -> case outage_smtp
#
# Tools: python3 with PyYAML (as tests/rep01-structure.test.sh), plus
#   kustomize v5 (KUSTOMIZE=<path> or PATH; CI: go install
#   sigs.k8s.io/kustomize/kustomize/v5@v5.8.1). A missing tool is reported
#   as a FAIL of its case.
#
# Retest:
#   KUSTOMIZE=<kustomize> ./tests/audit/ops-config.test.sh
set -uo pipefail

cd "$(dirname "$0")/../.." || exit 1

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; [ -n "${2:-}" ] && printf '%s\n' "$2" | sed 's/^/     /' | head -n 10; return 0; }

tool() { # tool <ENVVAR> <name>
    local v="${!1:-}"
    if [ -n "$v" ]; then printf '%s' "$v"; return 0; fi
    command -v "$2" 2>/dev/null
}

# ─── WP-100: WAL archiving in the Kubernetes production overlay ─────────────
KUSTOMIZE_BIN="$(tool KUSTOMIZE kustomize || true)"
if [ -z "$KUSTOMIZE_BIN" ]; then
    fail "k8s_wal_archive: kustomize available (set KUSTOMIZE)" "kustomize not found"
elif ! "$KUSTOMIZE_BIN" build deploy/k8s/overlays/prod > "$TMP_ROOT/prod.yaml" 2>"$TMP_ROOT/prod.err"; then
    fail "k8s_wal_archive: kustomize build deploy/k8s/overlays/prod" "$(tail -n 3 "$TMP_ROOT/prod.err")"
else
    hits="$(grep -cE 'archive_mode|archive-push' "$TMP_ROOT/prod.yaml" || true)"
    if [ "${hits:-0}" -gt 0 ]; then
        ok "k8s_wal_archive: the prod overlay configures WAL archiving (archive_mode/archive-push)"
    else
        fail "k8s_wal_archive: the prod overlay configures WAL archiving (archive_mode/archive-push)" \
            "0 matches; backup resources: $(python3 -c 'import sys,yaml; print(" ".join(d["kind"]+"/"+d["metadata"]["name"] for d in yaml.safe_load_all(open(sys.argv[1])) if d and d.get("kind") in ("CronJob","StatefulSet","Job")))' "$TMP_ROOT/prod.yaml")"
    fi
fi

# ─── WP-101: the RPO scenario can detect a loss above the limit ─────────────
rpo_limit="$(sed -nE 's/^RPO_LIMIT="\$\{RETICORA_RPO_LIMIT_SECONDS:-([0-9]+)\}"/\1/p' tests/restore-pitr.sh)"
write_window="$(sed -nE 's/^WRITE_SECONDS="\$\{RESTORE_WRITE_SECONDS:-([0-9]+)\}"/\1/p' tests/restore-pitr.sh)"
if [ -z "$rpo_limit" ] || [ -z "$write_window" ]; then
    fail "restore_rpo_window: restore-pitr.sh defines RPO_LIMIT and WRITE_SECONDS defaults" "limit=${rpo_limit:-?} window=${write_window:-?}"
elif [ "$write_window" -gt "$rpo_limit" ]; then
    ok "restore_rpo_window: the write window (${write_window}s) exceeds the RPO limit (${rpo_limit}s)"
else
    fail "restore_rpo_window: the write window exceeds the RPO limit, so an RPO violation is observable" \
        "WRITE_SECONDS=${write_window}s <= RPO_LIMIT=${rpo_limit}s: rpo = last_commit - restored_last stays below the limit whenever the restore succeeds"
fi

# ─── WP-104: the SMTP outage scenario is exercised ──────────────────────────
smtp_case="$(grep -nE '^ *smtp\)' tests/outage/run.sh || true)"
if [ -n "$smtp_case" ] && ! printf '%s' "$smtp_case" | grep -q 'report N/A'; then
    ok "outage_smtp: tests/outage/run.sh simulates the SMTP outage instead of reporting N/A"
elif [ -z "$smtp_case" ]; then
    fail "outage_smtp: tests/outage/run.sh has an SMTP outage scenario" "<no smtp) case>"
else
    fail "outage_smtp: tests/outage/run.sh simulates the SMTP outage instead of reporting N/A" \
        "$smtp_case; SMTP delivery exists: $(ls backend/internal/notify/smtp.go 2>/dev/null)"
fi

echo
printf '%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
