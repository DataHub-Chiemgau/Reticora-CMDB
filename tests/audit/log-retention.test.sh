#!/usr/bin/env bash
#
# Phase-1 acceptance audit, group T6-epic-b – DEFECT test (fails until fixed).
#
# WP-135 (NFR-06: "Retention: ... Logs 30 Tage"; acceptance criterion
# "Konfiguration mit Mindestwerten aus NFR-06")
# Finding P2 "NFR-06 log retention (30 days) not configured in any
# deployment": deploy/docker-compose/docker-compose.yml has no logging
# section, so Docker's json-file driver keeps container logs without limit;
# no log shipper with a 30-day retention is shipped (no loki/promtail/
# fluent-bit config, no 720h/30d retention anywhere in deploy/ or install.sh).
#
# Requirement encoded (fix-agnostic): the packaged Compose deployment bounds
# the logs of every service – either each service has a logging section with
# a rotating driver configuration (json-file/local with max-size and
# max-file) or a log-forwarding driver (journald, syslog, fluentd, gelf,
# loki), or deploy/ ships a log store configuration with a 30-day retention
# (720h / 30d). Kubernetes log retention is a cluster setting and is not
# asserted here.
#
# Needs python3 with PyYAML. Not wired into CI.
# Retest: ./tests/audit/log-retention.test.sh
set -uo pipefail

cd "$(dirname "$0")/../.." || exit 1

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; }

COMPOSE=deploy/docker-compose/docker-compose.yml
if [ -f "$COMPOSE" ]; then
    ok "compose file $COMPOSE exists"
else
    fail "compose file $COMPOSE missing"
fi

# A shipped log store with a 30-day retention satisfies NFR-06 on its own.
# Only log-store configurations count (the NATS event stream also keeps 30
# days, EVT-01, and must not be mistaken for log retention).
store_retention=$(find deploy -type f \( -iname '*loki*' -o -iname '*promtail*' -o -iname '*fluent*' -o -iname '*vector*' \
    -o -iname '*journald*' -o -iname '*logrotate*' \) -print0 2>/dev/null |
    xargs -0 -r grep -lE '(retention_period|retention|max_age|MaxRetentionSec|rotate)[^#]*(720h|30d|30day|2592000|30)' 2>/dev/null | head -5)

if ! python3 -I -c 'import yaml' 2>/dev/null; then
    fail "python3 with PyYAML is required to read $COMPOSE"
else
    unbounded=$(python3 -I - "$COMPOSE" <<'PY'
import sys, yaml
doc = yaml.safe_load(open(sys.argv[1])) or {}
forwarding = {"journald", "syslog", "fluentd", "gelf", "loki", "awslogs", "gcplogs", "splunk"}
for name, svc in sorted((doc.get("services") or {}).items()):
    log = (svc or {}).get("logging") or {}
    driver = log.get("driver", "json-file")
    opts = log.get("options") or {}
    if driver in forwarding:
        continue
    if driver in ("json-file", "local") and "max-size" in opts and "max-file" in opts:
        continue
    print(name)
PY
    )
    services=$(python3 -I -c 'import sys,yaml; print(len((yaml.safe_load(open(sys.argv[1])) or {}).get("services") or {}))' "$COMPOSE")
    if [ "${services:-0}" -gt 0 ]; then
        ok "compose defines $services services"
    else
        fail "compose defines no services"
    fi
    if [ -z "$unbounded" ]; then
        ok "every compose service bounds its logs (rotation or forwarding driver)"
    elif [ -n "$store_retention" ]; then
        ok "a log store with a 30-day retention is shipped: $store_retention"
    else
        fail "NFR-06 logs 30 days: services without bounded logs and no 30-day log store in deploy/: $(echo "$unbounded" | tr '\n' ' ')"
    fi
fi

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
