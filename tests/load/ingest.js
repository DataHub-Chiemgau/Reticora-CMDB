// NFR-03 ingest at the NFR-10 seed size (WP-204, WP-334). Two scenarios,
// selected with SCENARIO:
//
//   load  (default) RATE DeviceRecords per second (Phase 1: 200/s) for
//         DURATION_S through POST /api/v1/ingest/bulk in batches of BATCH.
//         During the load a probe record every PROBE_EVERY_S measures the
//         delay record -> visible; after the load the time until every
//         record of the run is visible is added as the tail delay. Budget:
//         every delay < VISIBLE_BUDGET_S (30 s), every offered request
//         sent (no dropped iterations) and accepted (202), and every record
//         visible.
//   spool One collector delivers its spool after an outage (COL-05):
//         SPOOL_RECORDS records (10,000), observed an hour ago, one spooled
//         upload after the other as flushSpool sends them (POST
//         /api/v1/discovery/ingest, gzip, SPOOL_BATCH records per upload).
//         Then it waits until every record is visible. Budgets: every
//         upload is accepted (202), every record becomes visible, the
//         flush is done within SPOOL_BUDGET_S
//         (5 min) and the queue drains at least TARGET records/s (200),
//         measured from the end of sending; the rate including the sending
//         is reported as information.
//
// A record that never becomes visible fails both scenarios
// (ingest_records_missing): once the queue is empty and the count stopped
// growing, the run stops waiting and counts what is missing (dead letters,
// records held as unlicensed_ci, dropped messages).
//
// Both scenarios record the peak backlog of /readyz (details.ingest.total,
// WP-334) while they wait.
//
//   k6 run -e BASE_URL=http://localhost:8080 -e API_KEY=$(cat key) tests/load/ingest.js
//   k6 run -e BASE_URL=http://localhost:8080 -e API_KEY=$(cat key) -e SCENARIO=spool tests/load/ingest.js
import http from 'k6/http';
import { check, fail, sleep } from 'k6';
import { Counter, Gauge, Trend } from 'k6/metrics';
import { BASE_URL, params, post, uuid } from './lib.js';

const SCENARIO = __ENV.SCENARIO || 'load';
const RATE = Number(__ENV.RATE || 200);
const BATCH = Number(__ENV.BATCH || 20);
const DURATION_S = Number(__ENV.DURATION_S || 60);
const PROBE_EVERY_S = Number(__ENV.PROBE_EVERY_S || 5);
const VISIBLE_BUDGET_S = Number(__ENV.VISIBLE_BUDGET_S || 30);
const SPOOL_RECORDS = Number(__ENV.SPOOL_RECORDS || 10000);
const SPOOL_BATCH = Number(__ENV.SPOOL_BATCH || 64);
const SPOOL_BUDGET_S = Number(__ENV.SPOOL_BUDGET_S || 300);
const WAIT_S = Number(__ENV.WAIT_S || 900);
// The throughput NFR-03 requires per instance in Phase 1.
const TARGET = Number(__ENV.TARGET || 200);

const visibleDelay = new Trend('ingest_visible_delay', true);
const drainRate = new Gauge('ingest_drain_records_per_s');
const drainInclusive = new Gauge('ingest_drain_inclusive_records_per_s');
const spoolFlush = new Trend('ingest_spool_flush', true);
const backlogPeak = new Gauge('ingest_backlog_peak');
const missing = new Counter('ingest_records_missing');
const accepted = new Counter('ingest_records_accepted');

const scenarios = {
  load: {
    ingest: {
      executor: 'constant-arrival-rate',
      exec: 'ingest',
      rate: Math.max(1, Math.round(RATE / BATCH)),
      timeUnit: '1s',
      duration: `${DURATION_S}s`,
      preAllocatedVUs: 10,
      maxVUs: 50,
    },
    probe: { executor: 'per-vu-iterations', exec: 'probe', vus: 1, iterations: 1, maxDuration: `${DURATION_S + WAIT_S}s` },
  },
  spool: {
    spool: { executor: 'per-vu-iterations', exec: 'spool', vus: 1, iterations: 1, maxDuration: `${WAIT_S}s` },
  },
};
const thresholds = {
  load: {
    'http_req_duration{name:ingest}': ['p(95)<1000'],
    // Every batch is accepted (202): k6 may start one iteration more than
    // RATE · DURATION_S, so a single rejected batch would not show as
    // missing records.
    'http_req_failed{name:ingest}': ['rate==0'],
    ingest_visible_delay: [`max<${VISIBLE_BUDGET_S * 1000}`],
    // The RATE is offered in full: an iteration k6 could not start would
    // lower the load.
    dropped_iterations: ['count==0'],
    ingest_records_missing: ['count==0'],
    checks: ['rate>0.99'],
  },
  spool: {
    'http_req_duration{name:spool}': ['p(95)<1000'],
    // Every upload is accepted (202); a rejected one ends the iteration
    // before the records are counted.
    'http_req_failed{name:spool}': ['rate==0'],
    ingest_spool_flush: [`max<${SPOOL_BUDGET_S * 1000}`],
    ingest_drain_records_per_s: [`value>=${TARGET}`],
    ingest_records_missing: ['count==0'],
    checks: ['rate>0.99'],
  },
};
if (!scenarios[SCENARIO]) {
  fail(`SCENARIO must be load or spool, not ${SCENARIO}`);
}

export const options = {
  scenarios: scenarios[SCENARIO],
  thresholds: thresholds[SCENARIO],
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'max', 'count'],
};

// setup names the run once for every VU: the serial numbers of the run share
// its prefix.
export function setup() {
  return { run: __ENV.RUN_ID || String(Date.now()) };
}

// record is a DeviceRecord as the SNMP collector sends it for a new device.
function record(serial, ip, observedAt) {
  return {
    record_id: uuid(),
    observed_at: observedAt.toISOString(),
    source: 'snmp',
    target_ip: ip,
    identity: { serial_number: serial, hostname: serial.toLowerCase() },
    classification: { ci_type: 'server', manufacturer: 'Reticora Labs', model: 'L-1U' },
    system: { name: serial.toLowerCase() },
  };
}

// loadIP is a unique address per device, outside the seed's 10.10/10.11
// ranges.
function loadIP(n) {
  return `10.${100 + (Math.floor(n / 65536) % 100)}.${Math.floor(n / 256) % 256}.${n % 256}`;
}

export function ingest(data) {
  const records = [];
  for (let i = 0; i < BATCH; i++) {
    const n = __VU * 100000 + __ITER * BATCH + i;
    records.push(record(`LOAD-${data.run}-${__VU}-${__ITER}-${i}`, loadIP(n), new Date()));
  }
  if (post('/ingest/bulk', { records }, 'ingest', [202]).status === 202) {
    accepted.add(records.length);
  }
}

// visibleCount returns how many CIs have a serial number with prefix.
function visibleCount(prefix) {
  const res = http.get(`${BASE_URL}/api/v1/cis?limit=1&filter[serial_number][like]=${prefix}*`, params('visible-poll'));
  return res.status === 200 ? Number(res.json('total')) : -1;
}

// backlog samples the ingest backlog of /readyz (-1 without the detail).
function backlog() {
  const res = http.get(`${BASE_URL}/readyz`, { tags: { name: 'readyz' }, timeout: '10s' });
  try {
    const total = res.json('details.ingest.total');
    if (typeof total === 'number') {
      backlogPeak.add(total);
      return total;
    }
  } catch (e) {
    // no JSON body
  }
  return -1;
}

// waitVisible polls until want CIs with prefix are visible and returns the
// time t and the count n. A shortfall is a failure (t = -1): it stops
// waiting after WAIT_S, or as soon as the queue is empty and the count
// stopped growing, since the missing records will never become visible.
// The records missing are added to ingest_records_missing either way.
function waitVisible(prefix, want) {
  const deadline = Date.now() + WAIT_S * 1000;
  let last = -1;
  let idle = 0;
  let t = -1;
  while (Date.now() < deadline) {
    const n = visibleCount(prefix);
    if (n >= want) {
      t = Date.now();
      last = n;
      break;
    }
    idle = backlog() === 0 && n === last ? idle + 1 : 0;
    last = n;
    if (idle >= 3) {
      console.error(`${prefix}: only ${n} of ${want} records visible and the queue is empty`);
      break;
    }
    sleep(0.5);
  }
  missing.add(Math.max(0, want - Math.max(last, 0)));
  return { t, n: last };
}

// probe measures the delay record -> visible every PROBE_EVERY_S during the
// load, then the tail until every record of the run is visible.
export function probe(data) {
  const start = Date.now();
  const end = start + DURATION_S * 1000;
  for (let i = 0; Date.now() < end; i++) {
    const serial = `PROBE-${data.run}-${i}`;
    const sent = Date.now();
    post('/ingest/bulk', { records: [record(serial, `10.99.${Math.floor(i / 256) % 256}.${i % 256}`, new Date())] }, 'ingest', [202]);
    let delay = -1;
    for (let j = 0; j < WAIT_S * 4 && delay < 0; j++) {
      const res = http.get(`${BASE_URL}/api/v1/cis?limit=1&filter[serial_number]=${serial}`, params('probe-poll'));
      if (res.status === 200 && (res.json('data') || []).length > 0) {
        delay = Date.now() - sent;
      } else {
        sleep(0.25);
      }
    }
    check(delay, { 'probe record visible': (d) => d >= 0 });
    visibleDelay.add(delay >= 0 ? delay : WAIT_S * 1000);
    backlog();
    const next = sent + PROBE_EVERY_S * 1000;
    if (next > Date.now()) {
      sleep((next - Date.now()) / 1000);
    }
  }
  // The tail: the last records of every lane.
  if (end > Date.now()) {
    sleep((end - Date.now()) / 1000);
  }
  const prefix = `LOAD-${data.run}-`;
  const want = Math.max(1, Math.round(RATE / BATCH)) * DURATION_S * BATCH;
  const atEnd = visibleCount(prefix);
  const all = waitVisible(prefix, want);
  check(all, { 'every load record visible': (v) => v.t >= 0 && v.n >= want });
  const tail = all.t >= 0 ? all.t - end : WAIT_S * 1000;
  visibleDelay.add(tail);
  const total = visibleCount(prefix);
  drainRate.add((total - atEnd) / Math.max(tail / 1000, 0.001));
  drainInclusive.add(total / ((DURATION_S * 1000 + tail) / 1000));
  console.log(`load: ${total} of ${want} records visible, ${atEnd} at the end of sending, ` +
    (all.t >= 0 ? `tail ${tail} ms` : 'not all became visible'));
}

// spool sends SPOOL_RECORDS records as a collector flushes its spool: one
// upload after the other, each acknowledged before the next.
export function spool(data) {
  const observed = Date.now() - 3600 * 1000;
  const prefix = `SPOOL-${data.run}-`;
  const first = Date.now();
  for (let i = 0; i < SPOOL_RECORDS; i += SPOOL_BATCH) {
    const records = [];
    for (let n = i; n < Math.min(i + SPOOL_BATCH, SPOOL_RECORDS); n++) {
      const ip = `172.${16 + (Math.floor(n / 65536) % 16)}.${Math.floor(n / 256) % 256}.${n % 256}`;
      records.push(record(`${prefix}${n}`, ip, new Date(observed + n)));
    }
    const res = http.post(`${BASE_URL}/api/v1/discovery/ingest`, JSON.stringify({ records }),
      Object.assign(params('spool'), { compression: 'gzip' }));
    if (!check(res, { 'spool 202': (r) => r.status === 202 })) {
      fail(`spooled upload at record ${i}: ${res.status} ${res.body}`);
    }
  }
  const sent = Date.now();
  const atEnd = visibleCount(prefix);
  const backlogAtEnd = backlog();
  const all = waitVisible(prefix, SPOOL_RECORDS);
  check(all, { 'every spooled record visible': (v) => v.t >= 0 && v.n >= SPOOL_RECORDS });
  // A shortfall counts as a flush that never ended.
  const done = all.t >= 0 ? all.t : first + WAIT_S * 1000;
  const total = visibleCount(prefix);
  spoolFlush.add(done - first);
  drainRate.add((total - atEnd) / Math.max((done - sent) / 1000, 0.001));
  drainInclusive.add(total / ((done - first) / 1000));
  console.log(`spool: ${total} of ${SPOOL_RECORDS} records visible; sending took ${sent - first} ms, ` +
    `${atEnd} visible and backlog ${backlogAtEnd} at its end, ` +
    (all.t >= 0 ? `all visible ${done - sent} ms later` : 'not all became visible'));
}
