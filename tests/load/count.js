// Exact counts of the ingest scenarios over GET /cis, whose total is capped
// since WP-839 (E-104): exact up to 10,000, beyond it total is 10,000 with
// total_capped true. A run of ingest.js load offers 12,000 records
// (writes.js beside it 24,000), so the plain total stopped at 10,000 and the
// run counted 2,000 records as missing that were all there (load runs
// 41566049639 and 41760522355). No k6 module here: count.test.sh runs it
// with node.

// SERIAL_CHARS follow the prefix of a run in the serial numbers and names
// the scenarios count: LOAD-<run>-<vu>-<iter>-<i>, SPOOL-<run>-<n> and the
// servers srv-<n> of the seed.
export const SERIAL_CHARS = '0123456789-';

// exactCount returns how many CIs match prefix: count(p) returns the list
// total of the CIs whose value starts with p, count(p, true) of those whose
// value is p, as { total, capped } (null on an error). A capped total is
// split into the value equal to the prefix and the values by the character
// after it until every part counts exactly; the parts are disjoint and
// cover every value whose characters after the prefix are in chars (SPOOL-
// <run>-1 beside SPOOL-<run>-10 … -19). It returns -1 on an error and when
// the parts hold no more than the capped total: a value with another
// character after the prefix, which the parts miss.
export function exactCount(count, prefix, chars = SERIAL_CHARS) {
  const res = count(prefix, false);
  if (!res) {
    return -1;
  }
  if (!res.capped) {
    return res.total;
  }
  const equal = count(prefix, true);
  if (!equal) {
    return -1;
  }
  let n = equal.total;
  for (const c of chars) {
    const part = exactCount(count, prefix + c, chars);
    if (part < 0) {
      return -1;
    }
    n += part;
  }
  // A capped list holds more than total CIs, and a count only grows while
  // the run waits.
  if (n <= res.total) {
    console.error(`${prefix}*: the total is capped at ${res.total}, the parts by [${chars}] hold ${n}: ` +
      'a value has another character after the prefix');
    return -1;
  }
  return n;
}
