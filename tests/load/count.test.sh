#!/usr/bin/env bash
#
# count.test.sh — tests for tests/load/count.js, the exact counts of
# tests/load/ingest.js over the capped total of GET /cis (E-104, WP-839):
# a stub of the list caps its total like api.CapTotal (at CAP, with
# total_capped beyond it) over the serial numbers of a run.
#
#   - 12 records of a run (VUs 1, 2, 10 and 11 as ingest.js names them)
#     with a cap of 5: the plain total stops at 5 (as the load runs
#     41566049639 and 41760522355 stopped at 10,000 of 12,000 records),
#     exactCount counts 12.
#   - Without a capped total exactCount asks once and returns it, also for
#     exactly CAP records; a run without records counts 0.
#   - The spool serial numbers (SPOOL-<run>-<n>: SPOOL-<run>-1 beside
#     SPOOL-<run>-10 … -19) and the server names of the seed (srv-<n>)
#     count exactly beyond the cap.
#   - A failed count (null) anywhere is -1, so the run never takes it for
#     visible records.
#   - A value with another character after the prefix, which the parts by
#     SERIAL_CHARS miss, is -1, not a count below the true one.
#
# Needs node (ES modules).
#
# Usage: ./tests/load/count.test.sh
set -uo pipefail

cd "$(dirname "$0")/../.." || exit 1

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

# An .mjs copy: node loads it as an ES module without a package.json.
cp tests/load/count.js "$TMP_ROOT/count.mjs"
cat >"$TMP_ROOT/test.mjs" <<'EOF'
import { exactCount, SERIAL_CHARS } from './count.mjs';

const CAP = 5;
let pass = 0;
let fail = 0;
const expect = (name, got, want) => {
  if (got === want) {
    pass++;
    console.log(`ok   ${name}`);
  } else {
    fail++;
    console.log(`FAIL ${name}: got ${got}, want ${want}`);
  }
};

// list is the stub of GET /cis?filter[...][like]=<p>* (equal: of
// filter[...]=<p>): the total of the values that start with p (are p),
// capped like api.CapTotal; requests counts the calls, broken makes the
// count of one prefix fail.
const list = (values, broken = '') => {
  const stub = (p, equal) => {
    stub.requests++;
    if (broken && p === broken) {
      return null;
    }
    const n = values.filter((v) => (equal ? v === p : v.startsWith(p))).length;
    return n > CAP ? { total: CAP, capped: true } : { total: n, capped: false };
  };
  stub.requests = 0;
  return stub;
};

const run = '1762223195000';
const load = [];
for (const vu of [1, 2, 10, 11]) {
  for (let i = 0; i < 3; i++) {
    load.push(`LOAD-${run}-${vu}-0-${i}`);
  }
}
const prefix = `LOAD-${run}-`;

expect('the plain total of 12 records stops at the cap', list(load)(prefix).total, CAP);
expect('exactCount counts 12 records beyond the cap', exactCount(list(load), prefix), 12);

const few = list(load.slice(0, 4));
expect('4 records below the cap', exactCount(few, prefix), 4);
expect('below the cap one request', few.requests, 1);
expect('exactly the cap is exact', exactCount(list(load.slice(0, CAP)), prefix), CAP);
expect('no records of the run', exactCount(list(load), 'LOAD-other-'), 0);

const spool = Array.from({ length: 23 }, (_, n) => `SPOOL-${run}-${n}`);
expect('spool serial numbers beyond the cap', exactCount(list(spool), `SPOOL-${run}-`), 23);
const servers = Array.from({ length: 17 }, (_, n) => `srv-${String(n + 5).padStart(5, '0')}`);
expect('server names of the seed beyond the cap', exactCount(list([...servers, 'sw-00001', 'sw-00002']), 'srv-'), 17);

expect('a failed count is -1', exactCount(list(load, prefix), prefix), -1);
expect('a failed count of a part is -1', exactCount(list(load, `${prefix}1`), prefix), -1);

const odd = [...load.slice(0, 4), ...Array.from({ length: 4 }, (_, i) => `${prefix}x${i}`)];
const before = console.error;
console.error = () => {};
expect('a character outside SERIAL_CHARS is -1', exactCount(list(odd), prefix), -1);
console.error = before;
expect('SERIAL_CHARS are the digits and the dash', SERIAL_CHARS, '0123456789-');

console.log(`${pass} passed, ${fail} failed`);
process.exit(fail === 0 ? 0 : 1);
EOF
node "$TMP_ROOT/test.mjs"
