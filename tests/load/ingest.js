// NFR-03 ingest at the NFR-10 seed size (WP-204): RATE DeviceRecords per
// second (Phase 1: 200/s) for DURATION through POST /api/v1/ingest/bulk in
// batches of BATCH, as a collector sends them. After the load a probe record
// measures the delay record -> visible (< 30 s) behind the backlog, and the
// drain rate is the records the queue processed per second.
//
//   k6 run -e BASE_URL=http://localhost:8080 -e API_KEY=$(cat key) tests/load/ingest.js
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Gauge, Trend } from 'k6/metrics';
import { BASE_URL, params, post, uuid } from './lib.js';

const RATE = Number(__ENV.RATE || 200);
const BATCH = Number(__ENV.BATCH || 20);
const DURATION_S = Number(__ENV.DURATION_S || 60);
const RUN = __ENV.RUN_ID || String(Date.now());
// The throughput NFR-03 requires per instance in Phase 1.
const TARGET = Number(__ENV.TARGET || 200);

const visibleDelay = new Trend('ingest_visible_delay', true);
const drainRate = new Gauge('ingest_drain_records_per_s');

export const options = {
  scenarios: {
    ingest: {
      executor: 'constant-arrival-rate',
      exec: 'ingest',
      rate: Math.max(1, Math.round(RATE / BATCH)),
      timeUnit: '1s',
      duration: `${DURATION_S}s`,
      preAllocatedVUs: 10,
      maxVUs: 50,
    },
    probe: { executor: 'per-vu-iterations', exec: 'probe', vus: 1, iterations: 1, startTime: `${DURATION_S}s`, maxDuration: '10m' },
  },
  thresholds: {
    'http_req_duration{name:ingest}': ['p(95)<1000'],
    ingest_visible_delay: ['max<30000'],
    ingest_drain_records_per_s: [`value>=${TARGET}`],
    checks: ['rate>0.99'],
  },
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'max', 'count'],
};

function record(serial, n) {
  return {
    record_id: uuid(),
    observed_at: new Date().toISOString(),
    source: 'snmp',
    // A unique address per device, outside the seed's 10.10/10.11 ranges.
    target_ip: `10.${100 + (Math.floor(n / 65536) % 100)}.${Math.floor(n / 256) % 256}.${n % 256}`,
    identity: { serial_number: serial, hostname: serial.toLowerCase() },
    classification: { ci_type: 'server', manufacturer: 'Reticora Labs', model: 'L-1U' },
    system: { name: serial.toLowerCase() },
  };
}

export function ingest() {
  const records = [];
  for (let i = 0; i < BATCH; i++) {
    const n = (__VU * 100000) + (__ITER * BATCH) + i;
    records.push(record(`LOAD-${RUN}-${__VU}-${__ITER}-${i}`, n));
  }
  post('/ingest/bulk', { records }, 'ingest', [202]);
}

// visible polls until a CI with the serial exists and returns the delay.
function visible(serial, since) {
  for (let i = 0; i < 600; i++) {
    const res = http.get(`${BASE_URL}/api/v1/cis?limit=1&filter[serial_number]=${serial}`, params('probe-poll'));
    if (res.status === 200 && (res.json('data') || []).length > 0) {
      return Date.now() - since;
    }
    sleep(0.5);
  }
  return -1;
}

export function probe() {
  const serial = `LOAD-${RUN}-probe`;
  const sent = Date.now();
  post('/ingest/bulk', { records: [record(serial, 0)] }, 'ingest', [202]);
  const delay = visible(serial, sent);
  check(delay, { 'probe record visible': (d) => d >= 0 });
  visibleDelay.add(delay >= 0 ? delay : 600000);
  // The backlog of the load drained when the probe behind it was visible.
  const records = RATE * DURATION_S;
  const seconds = DURATION_S + (delay >= 0 ? delay : 600000) / 1000;
  drainRate.add(records / seconds);
}
