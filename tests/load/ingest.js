// NFR-03 ingest at the NFR-10 seed size (WP-204, WP-334). Two scenarios,
// selected with SCENARIO:
//
//   load  (default) RATE DeviceRecords per second (Phase 1: 200/s) for
//         DURATION_S through POST /api/v1/ingest/bulk in batches of BATCH.
//         During the load a probe record every PROBE_EVERY_S measures the
//         delay record -> visible; after the load the time until every
//         record of the run is visible is added as the tail delay. Budget:
//         every delay < VISIBLE_BUDGET_S (30 s).
//   spool One collector delivers its spool after an outage (COL-05):
//         SPOOL_RECORDS records (10,000), observed an hour ago, one spooled
//         upload after the other as flushSpool sends them (POST
//         /api/v1/discovery/ingest, gzip, SPOOL_BATCH records per upload).
//         Then it waits until every record is visible. Budgets: the flush
//         is done within SPOOL_BUDGET_S (5 min) and the queue drains at
//         least TARGET records/s (200), measured from the end of sending;
//         the rate including the sending is reported as information.
//
// Both scenarios record the peak backlog of /readyz (details.ingest.total,
// WP-334) while they wait.
//
//   k6 run -e BASE_URL=http://localhost:8080 -e API_KEY=$(cat key) tests/load/ingest.js
//   k6 run -e BASE_URL=http://localhost:8080 -e API_KEY=$(cat key) -e SCENARIO=spool tests/load/ingest.js
import http from 'k6/http';
import { check, fail, sleep } from 'k6';
import { Gauge, Trend } from 'k6/metrics';
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
    ingest_visible_delay: [`max<${VISIBLE_BUDGET_S * 1000}`],
    checks: ['rate>0.99'],
  },
  spool: {
    'http_req_duration{name:spool}': ['p(95)<1000'],
    ingest_spool_flush: [`max<${SPOOL_BUDGET_S * 1000}`],
    ingest_drain_records_per_s: [`value>=${TARGET}`],
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
  post('/ingest/bulk', { records }, 'ingest', [202]);
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

// waitVisible polls until want CIs with prefix are visible, or the queue
// is empty and the count stopped growing (dropped iterations), and returns
// the time; -1 after WAIT_S.
function waitVisible(prefix, want) {
  const deadline = Date.now() + WAIT_S * 1000;
  let last = -1;
  let idle = 0;
  while (Date.now() < deadline) {
    const n = visibleCount(prefix);
    const now = Date.now();
    if (n >= want) {
      return now;
    }
    idle = backlog() === 0 && n === last && n > 0 ? idle + 1 : 0;
    if (idle >= 3) {
      console.warn(`${prefix}: ${n} of ${want} visible and the queue is empty`);
      return now;
    }
    last = n;
    sleep(0.5);
  }
  return -1;
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
  const tAll = waitVisible(prefix, want);
  check(tAll, { 'every load record visible': (t) => t >= 0 });
  const tail = tAll >= 0 ? tAll - end : WAIT_S * 1000;
  visibleDelay.add(tail);
  const total = visibleCount(prefix);
  drainRate.add((total - atEnd) / Math.max(tail / 1000, 0.001));
  drainInclusive.add(total / ((DURATION_S * 1000 + tail) / 1000));
  console.log(`load: ${total} of ${want} records visible, ${atEnd} at the end of sending, tail ${tail} ms`);
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
  const tAll = waitVisible(prefix, SPOOL_RECORDS);
  check(tAll, { 'every spooled record visible': (t) => t >= 0 });
  const done = tAll >= 0 ? tAll : first + WAIT_S * 1000;
  const total = visibleCount(prefix);
  spoolFlush.add(done - first);
  drainRate.add((total - atEnd) / Math.max((done - sent) / 1000, 0.001));
  drainInclusive.add(total / ((done - first) / 1000));
  console.log(`spool: ${total} of ${SPOOL_RECORDS} records visible; sending took ${sent - first} ms, ` +
    `${atEnd} visible and backlog ${backlogAtEnd} at its end, all visible ${done - sent} ms later`);
}
