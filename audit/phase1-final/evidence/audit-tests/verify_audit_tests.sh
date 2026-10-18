#!/usr/bin/env bash
# Independent mechanical verification of the audit tests added since a35f411 (or $BASE).
# Output: $OUT/results.tsv  (kind, package, test, outcome, detail) plus logs.
set -uo pipefail
REPO=/home/user/Reticora-CMDB
SP=/tmp/claude-0/-home-user-Reticora-CMDB/fbd8a132-b968-552a-84eb-515fd3e8f807/scratchpad
BASE=${BASE:-a35f411}
OUT=${OUT:-$SP/verify-tests}
DBU=${DBU:-audit_v1}; DBD=${DBD:-audit_v2}
URLU="postgres://reticora:reticora@localhost:55432/$DBU?sslmode=disable"
URLD="postgres://reticora:reticora@localhost:55432/$DBD?sslmode=disable"
export TEST_NATS_URL=nats://localhost:54222 TEST_REDIS_URL=redis://localhost:56379/0
export TEST_NATS_SERVER_BIN=${TEST_NATS_SERVER_BIN:-$SP/work/audit-B18/nats-server}
mkdir -p "$OUT"; : > "$OUT/results.tsv"
cd "$REPO"
files=$(git diff --name-only --diff-filter=A "$BASE" HEAD)
gofiles=$(echo "$files" | grep -E '_test\.go$' || true)

# 1. gofmt
echo "$gofiles" | xargs -r gofmt -l > "$OUT/gofmt.txt"
echo -e "check\t-\tgofmt\t$([ -s "$OUT/gofmt.txt" ] && echo FAIL || echo ok)\t$(tr '\n' ' ' < "$OUT/gofmt.txt")" >> "$OUT/results.tsv"

# 2. per package: vet (both), untagged tests, tagged defect tests one by one
for dir in $(echo "$gofiles" | xargs -r -n1 dirname | sort -u); do
  mod=${dir%%/*}; pkg="./${dir#*/}"
  ( cd "$REPO/$mod" || exit
    go vet "$pkg" > "$OUT/vet-$(echo $dir | tr / _).log" 2>&1; v1=$?
    go vet -tags auditdefect "$pkg" > "$OUT/vettag-$(echo $dir | tr / _).log" 2>&1; v2=$?
    echo -e "vet\t$dir\t-\t$([ $v1 = 0 ] && echo ok || echo FAIL)/$([ $v2 = 0 ] && echo ok || echo FAIL)\tuntagged/tagged" >> "$OUT/results.tsv"
    log="$OUT/untagged-$(echo $dir | tr / _).log"
    TEST_DATABASE_URL="$URLU" go test -count=1 -p 1 -skip "^(TestMigrationsRoundtrip|TestSchemaBaselineMatchesMigratedSchema)$" "$pkg" > "$log" 2>&1; rc=$?
    echo -e "untagged\t$dir\t(all)\t$([ $rc = 0 ] && echo PASS || echo FAIL)\t$(grep -E '^(--- FAIL|FAIL|panic)' "$log" | head -3 | tr '\n' ' ')" >> "$OUT/results.tsv"
  )
  for f in $(echo "$gofiles" | grep "^$dir/[^/]*$"); do
    head -1 "$REPO/$f" | grep -q 'go:build auditdefect' || continue
    for t in $(grep -oE '^func (Test[A-Za-z0-9_]+)\(t \*testing\.T\)' "$REPO/$f" | awk '{print $2}' | cut -d'(' -f1); do
      log="$OUT/defect-$t.log"
      ( cd "$REPO/$mod" && TEST_DATABASE_URL="$URLD" go test -tags auditdefect -count=1 -p 1 -v -run "^$t\$" "$pkg" > "$log" 2>&1 ); rc=$?
      if grep -q -- "--- PASS: $t " "$log"; then o=UNEXPECTED_PASS
      elif grep -qE 'panic:|build failed|\[setup failed\]|cannot find package|undefined:' "$log"; then o=ERROR
      elif grep -qE '_test\.go:[0-9]+: (seed|setup|fixture|connect|migrate)|: ERROR: (insert|update|relation|permission denied|duplicate)|failed to connect' "$log" && ! grep -qE '_test\.go:[0-9]+: .*(violated|want|got)' "$log"; then o=SETUP_ERROR
      elif grep -q -- "--- FAIL: $t " "$log"; then o=FAILS_AS_EXPECTED
      elif grep -q -- "--- SKIP: $t" "$log"; then o=SKIPPED
      else o=ERROR; fi
      detail=$(grep -E '^\s+[a-z_0-9]+_test\.go:[0-9]+:' "$log" | head -2 | sed 's/^\s*//' | cut -c1-220 | tr '\n' ' ')
      echo -e "defect\t$dir\t$t\t$o\t$detail" >> "$OUT/results.tsv"
    done
  done
done

# 3. lint with tag on touched packages, per module
for mod in backend collector edgecore; do
  pk=$(echo "$gofiles" | grep "^$mod/" | xargs -r -n1 dirname | sort -u | sed "s#^$mod/#./#")
  [ -z "$pk" ] && continue
  ( cd "$REPO/$mod" && $SP/bin/golangci-lint run --config "$REPO/backend/.golangci.yml" --build-tags auditdefect $pk > "$OUT/lint-$mod.log" 2>&1 ); rc=$?
  echo -e "lint\t$mod\t-\t$([ $rc = 0 ] && echo ok || echo FAIL)\t$(tail -1 "$OUT/lint-$mod.log")" >> "$OUT/results.tsv"
done

# 4. shell audit tests (expected to fail = defect)
for s in $(echo "$files" | grep -E '^tests/audit/.*\.test\.sh$' || true); do
  bash "$REPO/$s" > "$OUT/sh-$(basename $s).log" 2>&1; rc=$?
  echo -e "shell\ttests/audit\t$(basename $s)\t$([ $rc = 0 ] && echo PASS || echo FAILS)\t$(grep -E '^(FAIL|not ok)' "$OUT/sh-$(basename $s).log" | head -2 | cut -c1-200 | tr '\n' ' ')" >> "$OUT/results.tsv"
done

# 5. frontend
if echo "$files" | grep -q '^frontend/'; then
  ( cd "$REPO/frontend"
    npx prettier --check $(echo "$files" | grep '^frontend/' | sed 's#^frontend/##') > "$OUT/fe-prettier.log" 2>&1; p=$?
    npx eslint $(echo "$files" | grep '^frontend/' | grep -E '\.(ts|tsx)$' | sed 's#^frontend/##') > "$OUT/fe-eslint.log" 2>&1; e=$?
    npx tsc --noEmit -p . > "$OUT/fe-tsc.log" 2>&1; c=$?
    echo -e "frontend\t-\tprettier/eslint/tsc\t$p/$e/$c\t" >> "$OUT/results.tsv"
    npx vitest run > "$OUT/fe-vitest.log" 2>&1; v=$?
    echo -e "frontend\t-\tvitest default\t$([ $v = 0 ] && echo PASS || echo FAIL)\t$(grep -E 'Test Files|Tests ' "$OUT/fe-vitest.log" | tr '\n' ' ')" >> "$OUT/results.tsv"
    npx vitest run -c vitest.auditdefect.config.ts --reporter=json --outputFile="$OUT/fe-defect.json" > "$OUT/fe-defect.log" 2>&1
    jq -r '.testResults[] | .assertionResults[] | "frontend-defect\t\(.ancestorTitles|join(" > "))\t\(.title)\t\(.status)\t\((.failureMessages[0] // "")|.[0:200]|gsub("\n";" "))"' "$OUT/fe-defect.json" >> "$OUT/results.tsv" 2>/dev/null
  )
  git -C "$REPO" checkout -- frontend/tsconfig.tsbuildinfo 2>/dev/null
fi
echo DONE
