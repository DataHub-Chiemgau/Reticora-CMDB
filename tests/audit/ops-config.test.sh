#!/usr/bin/env bash
#
# ops-config.test.sh — Phase-1 acceptance audit, group T5, DEFECT tests for
# CI, release, backup, monitoring and outage configuration (fail until the
# configuration is fixed; not wired into CI).
#
# Findings encoded (one case each):
#   WP-004 (SEC-11) P2 "Released image reticora-postgres is never
#          vulnerability-scanned" + gap high "Coverage of released images by
#          the image scan is not checked": every Dockerfile of the release
#          build-push matrix must be in the ci.yml image-scan (Trivy) matrix.
#                                                          -> case image_scan_*
#   WP-100 (NFR-05, OPS-04) P2 "K8s backup component provisions no WAL
#          archiving": the prod overlay must configure WAL archiving
#          (archive_mode / archive-push).                  -> case k8s_wal_archive
#   WP-100 (NFR-05) P2 "Backup bucket versioning/replication not enforced;
#          Terraform 'production requires replica' is warning-only": a
#          production plan without a replica region, or with the replica in
#          the backup region, must fail.                   -> case terraform_*
#   WP-101 (NFR-05) P2 "RPO limit check in restore-pitr.sh cannot fail with
#          default parameters": the continuous-write window of the RPO
#          scenario (420 s) is shorter than the RPO limit (900 s), so a data
#          loss above the limit is not observable.        -> case restore_rpo_window
#   WP-102 (TEC-15) P2 "No signature verification before installation":
#          the installers must verify the signed release artefacts (cosign)
#          before installing.                              -> case install_signature_*
#   WP-103 (OPS-05) P2 "alertmanager.yaml is rejected by Alertmanager's config
#          validation (no global SMTP smarthost)" + gap high "Alertmanager
#          config validation in CI".                       -> case alertmanager_*
#   WP-104 (OPS-06) P2 "SMTP outage scenario reported N/A although SMTP
#          delivery exists" (notify/smtp.go, WP-137).      -> case outage_smtp
#
# Tools: python3 with PyYAML (as tests/rep01-structure.test.sh), plus
#   kustomize v5 (KUSTOMIZE=<path> or PATH; CI: go install
#   sigs.k8s.io/kustomize/kustomize/v5@v5.8.1), terraform >= 1.5
#   (TERRAFORM=<path> or PATH), optional amtool (AMTOOL=<path> or PATH).
#   A missing required tool is reported as a FAIL of its case.
#
# Retest:
#   KUSTOMIZE=<kustomize> TERRAFORM=<terraform> AMTOOL=<amtool> ./tests/audit/ops-config.test.sh
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

# ─── WP-004: every released image is scanned ────────────────────────────────
scan_gap="$(python3 - <<'PY'
import yaml
rel = yaml.safe_load(open(".github/workflows/release.yml"))
ci = yaml.safe_load(open(".github/workflows/ci.yml"))
def matrix(job):
    return [(e.get("dockerfile"), e.get("image")) for e in job["strategy"]["matrix"]["include"]]
released = [m for job in rel["jobs"].values() if "strategy" in job and "include" in job["strategy"].get("matrix", {})
            for m in matrix(job) if m[0]]
scanned = {d for d, _ in matrix(ci["jobs"]["image-scan"])}
print(" ".join(f"{img}({d})" for d, img in released if d not in scanned))
PY
)"
if [ -z "$scan_gap" ]; then
    ok "image_scan_coverage: every Dockerfile of the release image matrix is in the ci.yml image-scan matrix"
else
    fail "image_scan_coverage: every Dockerfile of the release image matrix is in the ci.yml image-scan matrix" "not scanned: $scan_gap"
fi

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

# ─── WP-100: Terraform enforces backup replication for production ───────────
TF_BIN="$(tool TERRAFORM terraform || true)"
if [ -z "$TF_BIN" ]; then
    fail "terraform_replica: terraform available (set TERRAFORM)" "terraform not found"
else
    cp -r deploy/terraform "$TMP_ROOT/tf"
    if ! (cd "$TMP_ROOT/tf" && "$TF_BIN" init -no-color -backend=false -input=false >"$TMP_ROOT/tf-init.log" 2>&1); then
        fail "terraform_replica: terraform init" "$(tail -n 5 "$TMP_ROOT/tf-init.log")"
    else
        tf_plan() { (cd "$TMP_ROOT/tf" && "$TF_BIN" plan -no-color -input=false -lock=false "$@" >"$TMP_ROOT/tf-plan.log" 2>&1); }
        if tf_plan -var environment=production; then
            fail "terraform_replica_missing: a production plan without backup_replica_region fails" \
                "plan exit 0; $(grep -m1 -E 'Warning|Error' "$TMP_ROOT/tf-plan.log")"
        else
            ok "terraform_replica_missing: a production plan without backup_replica_region fails"
        fi
        if tf_plan -var environment=production -var region=eu-central-1 -var backup_replica_region=eu-central-1; then
            fail "terraform_replica_same: a production plan with the replica in the backup region fails" \
                "plan exit 0; $(grep -m1 -E 'Warning|Error' "$TMP_ROOT/tf-plan.log")"
        else
            ok "terraform_replica_same: a production plan with the replica in the backup region fails"
        fi
        if tf_plan -var environment=production -var region=eu-central-1 -var backup_replica_region=eu-west-1; then
            ok "terraform_replica_eu (control): a production plan with a distinct EU replica passes"
        else
            fail "terraform_replica_eu (control): a production plan with a distinct EU replica passes" "$(grep -m3 -E 'Error' "$TMP_ROOT/tf-plan.log")"
        fi
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

# ─── WP-102: signature verification before installation (TEC-15) ────────────
for f in install-vm.sh install-cloud.sh; do
    if grep -qE 'cosign[^#]*verify(-blob)?' "$f"; then
        ok "install_signature_${f%.sh}: ${f} verifies the cosign signature of what it installs"
    else
        fail "install_signature_${f%.sh}: ${f} verifies the cosign signature of what it installs" \
            "no 'cosign verify'/'cosign verify-blob' in ${f}; $(grep -nE 'curl -fsSL "\$(tarball|base)|git clone|compose_cmd pull' "$f" | head -n 3 | cut -c1-120)"
    fi
done

# ─── WP-103: the Alertmanager configuration is loadable ─────────────────────
am_static="$(python3 - <<'PY'
import yaml
cfg = yaml.safe_load(open("deploy/monitoring/alertmanager.yaml"))
g = cfg.get("global") or {}
problems = []
for r in cfg.get("receivers", []):
    for e in r.get("email_configs") or []:
        if not (e.get("smarthost") or g.get("smtp_smarthost")):
            problems.append(f"receiver {r['name']}: email_configs without smarthost and no global smtp_smarthost")
        if not (e.get("from") or g.get("smtp_from")):
            problems.append(f"receiver {r['name']}: email_configs without from and no global smtp_from")
print("\n".join(problems))
PY
)"
if [ -z "$am_static" ]; then
    ok "alertmanager_smtp: every email receiver has an SMTP smarthost and sender"
else
    fail "alertmanager_smtp: every email receiver has an SMTP smarthost and sender" "$am_static"
fi
AMTOOL_BIN="$(tool AMTOOL amtool || true)"
if [ -n "$AMTOOL_BIN" ]; then
    if "$AMTOOL_BIN" check-config deploy/monitoring/alertmanager.yaml >"$TMP_ROOT/amtool.log" 2>&1; then
        ok "alertmanager_amtool: amtool check-config accepts deploy/monitoring/alertmanager.yaml"
    else
        fail "alertmanager_amtool: amtool check-config accepts deploy/monitoring/alertmanager.yaml" "$(grep -E 'FAILED|error' "$TMP_ROOT/amtool.log" | head -n 3)"
    fi
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
