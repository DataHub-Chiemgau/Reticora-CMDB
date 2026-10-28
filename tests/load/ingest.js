// NFR-03 ingest at the NFR-10 seed size (WP-204, WP-334, WP-437). Three
// scenarios, selected with SCENARIO:
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
//   rediscover The re-discovery of the seeded devices (F-118): RATE
//         SNMP-like DeviceRecords per second like load, each for another
//         server of the seed (OBJECTS, cmd/seed -objects) with its serial
//         number, name and management IP, a chassis MAC as primary MAC,
//         the interfaces eth0 (the seeded MAC and address) and eth1 (a
//         second MAC) and its LLDP neighbor, the switch the seed connects
//         it to. DURATION_S defaults to 45 s: 9,000 records, every one a
//         device of its own, the probes take further servers. A record is
//         visible when its CI was seen in the run (last_seen_at). Budgets:
//         every delay < VISIBLE_BUDGET_S, every request sent and accepted,
//         every record visible and no new CI (ingest_new_cis).
//
// A record that never becomes visible fails every scenario
// (ingest_records_missing): once the queue is empty and the count stopped
// growing, the run stops waiting and counts what is missing (dead letters,
// records held as unlicensed_ci, dropped messages).
//
// No request may fail (F-388): http_req_failed is 0 in the run and in every
// scenario of it (ingest, spool and the probe with its polls and /readyz);
// every failed request is logged with its status (lib.js failed).
//
// Every scenario records the peak backlog of /readyz (details.ingest.total,
// WP-334; the server samples it every 30 s) while it waits, and counts the
// records accepted with 202 (ingest_records_accepted; load.yml divides the
// database cost of the run by it).
//
//   k6 run -e BASE_URL=http://localhost:8080 -e API_KEY=$(cat key) tests/load/ingest.js
//   k6 run -e BASE_URL=http://localhost:8080 -e API_KEY=$(cat key) -e SCENARIO=spool tests/load/ingest.js
//   k6 run -e BASE_URL=http://localhost:8080 -e API_KEY=$(cat key) -e SCENARIO=rediscover tests/load/ingest.js
import http from 'k6/http';
import exec from 'k6/execution';
import { check, fail, sleep } from 'k6';
import { Counter, Gauge, Trend } from 'k6/metrics';
import { BASE_URL, failed, get, params, post, seedDevice, total, uuid } from './lib.js';

const SCENARIO = __ENV.SCENARIO || 'load';
const RATE = Number(__ENV.RATE || 200);
const BATCH = Number(__ENV.BATCH || 20);
const DURATION_S = Number(__ENV.DURATION_S || (SCENARIO === 'rediscover' ? 45 : 60));
const PROBE_EVERY_S = Number(__ENV.PROBE_EVERY_S || 5);
const VISIBLE_BUDGET_S = Number(__ENV.VISIBLE_BUDGET_S || 30);
const SPOOL_RECORDS = Number(__ENV.SPOOL_RECORDS || 10000);
const SPOOL_BATCH = Number(__ENV.SPOOL_BATCH || 64);
const SPOOL_BUDGET_S = Number(__ENV.SPOOL_BUDGET_S || 300);
const WAIT_S = Number(__ENV.WAIT_S || 900);
// The throughput NFR-03 requires per instance in Phase 1.
const TARGET = Number(__ENV.TARGET || 200);
// The seed size of the demo organization (rediscover).
const OBJECTS = Number(__ENV.OBJECTS || 10000);

// Records the scenarios load and rediscover offer: RATE · DURATION_S.
const OFFERED = Math.max(1, Math.round(RATE / BATCH)) * DURATION_S * BATCH;
// rediscover: the load takes the servers 5 … 4 + OFFERED of the seed, the
// probes the servers from OBJECTS downwards.
const PROBES = Math.ceil(DURATION_S / PROBE_EVERY_S) + 1;

const visibleDelay = new Trend('ingest_visible_delay', true);
const drainRate = new Gauge('ingest_drain_records_per_s');
const drainInclusive = new Gauge('ingest_drain_inclusive_records_per_s');
const spoolFlush = new Trend('ingest_spool_flush', true);
const backlogPeak = new Gauge('ingest_backlog_peak');
const missing = new Counter('ingest_records_missing');
const accepted = new Counter('ingest_records_accepted');
const newCIs = new Counter('ingest_new_cis');

const offered = (fn) => ({
  ingest: {
    executor: 'constant-arrival-rate',
    exec: fn,
    rate: Math.max(1, Math.round(RATE / BATCH)),
    timeUnit: '1s',
    duration: `${DURATION_S}s`,
    preAllocatedVUs: 10,
    maxVUs: 50,
  },
});
const scenarios = {
  load: {
    ...offered('ingest'),
    probe: { executor: 'per-vu-iterations', exec: 'probe', vus: 1, iterations: 1, maxDuration: `${DURATION_S + WAIT_S}s` },
  },
  spool: {
    spool: { executor: 'per-vu-iterations', exec: 'spool', vus: 1, iterations: 1, maxDuration: `${WAIT_S}s` },
  },
  rediscover: {
    ...offered('rediscover'),
    probe: { executor: 'per-vu-iterations', exec: 'rediscoverProbe', vus: 1, iterations: 1, maxDuration: `${DURATION_S + WAIT_S}s` },
  },
};
const offeredThresholds = {
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
};
const thresholds = {
  load: offeredThresholds,
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
  // A re-discovered device is matched, never created anew.
  rediscover: { ...offeredThresholds, ingest_new_cis: ['count==0'] },
};
if (!scenarios[SCENARIO]) {
  fail(`SCENARIO must be load, spool or rediscover, not ${SCENARIO}`);
}
if (SCENARIO === 'rediscover' && OFFERED + PROBES > OBJECTS - 4) {
  fail(`rediscover needs ${OFFERED + PROBES} seeded servers for ${RATE} records/s over ${DURATION_S} s, ` +
    `the seed has ${OBJECTS - 4}: lower DURATION_S or seed more objects`);
}

export const options = {
  scenarios: scenarios[SCENARIO],
  thresholds: {
    ...thresholds[SCENARIO],
    // F-388: no failed request in the run (setup included) and in every
    // scenario.
    http_req_failed: ['rate==0'],
    ...Object.fromEntries(Object.keys(scenarios[SCENARIO]).map((s) => [`http_req_failed{scenario:${s}}`, ['rate==0']])),
  },
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'max', 'count'],
};

// since formats the start of the second of t for filter[last_seen_at][gte]:
// the server stores last_seen_at to the second.
const since = (t) => encodeURIComponent(`${new Date(Math.floor(t / 1000) * 1000).toISOString().slice(0, 19)}Z`);

// seenSince selects the servers seen since t (rediscover; the switches of
// the LLDP neighbors do not count).
const seenSince = (t) => `filter[name][like]=srv-*&filter[last_seen_at][gte]=${since(t)}`;

// setup names the run once for every VU: the serial numbers of the run share
// its prefix. For rediscover it checks that the records describe the seed
// and notes the CIs before the run.
export function setup() {
  const data = { run: __ENV.RUN_ID || String(Date.now()), start: Date.now() };
  if (SCENARIO === 'rediscover') {
    const d = seedDevice(5);
    const ci = (get(`/cis?limit=1&filter[name]=${d.name}`, 'setup').json('data') || [])[0];
    if (!ci || ci.serial_number !== d.serial || !String(ci.management_ip).startsWith(d.ip)) {
      fail(`${d.name} of the seed is ${JSON.stringify(ci)}, the records describe ${d.serial} at ${d.ip}: ` +
        'seed the demo organization with backend/cmd/seed');
    }
    const last = seedDevice(OBJECTS);
    if (total(`filter[name]=${last.name}`, 'setup') !== 1) {
      fail(`the seed has no ${last.name}: seed it with -objects ${OBJECTS}`);
    }
    data.cis = total('', 'setup');
    data.seen = total(seenSince(data.start), 'setup');
  }
  return data;
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

// rediscoverRecord is the DeviceRecord the SNMP collector sends when it
// sees device n of the seed again: the seed's identity with a chassis MAC
// as primary MAC, two interfaces with MACs and the LLDP neighbor on eth0,
// the switch the seed connects the server to (connected_to 1 + n mod 2).
function rediscoverRecord(n, observedAt) {
  const d = seedDevice(n);
  const sw = seedDevice(1 + (n % 2));
  return {
    record_id: uuid(),
    observed_at: observedAt.toISOString(),
    source: 'snmp',
    target_ip: d.ip,
    identity: { serial_number: d.serial, primary_mac: d.chassisMAC, hostname: d.name },
    classification: { ci_type: d.type, manufacturer: 'Reticora Labs', model: d.model, sys_object_id: '1.3.6.1.4.1.99999.1.1' },
    system: { name: d.name, description: `Reticora Labs ${d.model}` },
    interfaces: [
      { name: 'eth0', if_index: 1, mac: d.mac, speed_mbps: 1000, admin_status: 'up', oper_status: 'up',
        ips: [{ address: d.ip, prefix_length: 16 }] },
      { name: 'eth1', if_index: 2, mac: d.eth1MAC, speed_mbps: 1000, admin_status: 'up', oper_status: 'up' },
    ],
    neighbors: [{ protocol: 'lldp', local_interface: 'eth0', remote_mac: sw.chassisMAC, remote_ip: sw.ip,
      remote_hostname: sw.name, remote_interface: `Gi1/0/${(n % 48) + 1}` }],
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

// rediscover sends the next BATCH servers of the seed: iteration k of the
// run takes the servers 5 + k · BATCH onwards. The iteration k6 may start
// beyond RATE · DURATION_S sends nothing.
export function rediscover() {
  const first = exec.scenario.iterationInTest * BATCH;
  const records = [];
  for (let i = first; i < Math.min(first + BATCH, OFFERED); i++) {
    records.push(rediscoverRecord(5 + i, new Date()));
  }
  if (records.length > 0 && post('/ingest/bulk', { records }, 'ingest', [202]).status === 202) {
    accepted.add(records.length);
  }
}

// visibleCount returns how many CIs have a serial number with prefix.
function visibleCount(prefix) {
  const res = failed(http.get(`${BASE_URL}/api/v1/cis?limit=1&filter[serial_number][like]=${prefix}*`, params('visible-poll')),
    'visible-poll');
  return res.status === 200 ? Number(res.json('total')) : -1;
}

// backlog reads the ingest backlog of /readyz: total is the server's last
// sample (details.ingest.total, taken every 30 s), at when it was taken
// (sampled_at; now for a server that reads it per request); total -1
// without the detail.
function backlog() {
  const res = failed(http.get(`${BASE_URL}/readyz`, { tags: { name: 'readyz' }, timeout: '10s' }), 'readyz');
  try {
    const total = res.json('details.ingest.total');
    if (typeof total === 'number') {
      backlogPeak.add(total);
      const at = Date.parse(res.json('details.ingest.sampled_at') || '');
      return { total, at: Number.isNaN(at) ? Date.now() : at };
    }
  } catch (e) {
    // no JSON body
  }
  return { total: -1, at: 0 };
}

// waitVisible polls until want records are visible (count) and returns the
// time t and the count n. A shortfall is a failure (t = -1): it stops
// waiting after WAIT_S, or as soon as the queue is empty and the count
// stopped growing, since the missing records will never become visible.
// Empty counts only by a backlog sample taken after the count last grew:
// an older one may predate the records still waiting. The records missing
// are added to ingest_records_missing either way.
function waitVisible(label, count, want) {
  const deadline = Date.now() + WAIT_S * 1000;
  let last = -1;
  let grown = Date.now();
  let idle = 0;
  let t = -1;
  while (Date.now() < deadline) {
    const n = count();
    if (n >= want) {
      t = Date.now();
      last = n;
      break;
    }
    const queue = backlog();
    if (n !== last) {
      grown = Date.now();
    }
    idle = n === last && queue.total === 0 && queue.at > grown ? idle + 1 : 0;
    last = n;
    if (idle >= 3) {
      console.error(`${label}: only ${n} of ${want} records visible and the queue is empty`);
      break;
    }
    sleep(0.5);
  }
  missing.add(Math.max(0, want - Math.max(last, 0)));
  return { t, n: last };
}

// probeDuringLoad sends a probe every PROBE_EVERY_S until end, at most max,
// and adds its delay record -> visible: send(i) sends probe i and returns
// the test whether it is visible. Then it sleeps until end and returns the
// number of probes sent.
function probeDuringLoad(end, max, send) {
  let i = 0;
  for (; Date.now() < end && i < max; i++) {
    const sent = Date.now();
    const visible = send(i);
    let delay = -1;
    for (let j = 0; j < WAIT_S * 4 && delay < 0; j++) {
      if (visible()) {
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
  return i;
}

// tail waits until want records are visible after the load ended at end,
// adds the tail delay and the drain rates and returns how many are visible.
function tail(label, count, want, end) {
  const atEnd = count();
  const all = waitVisible(label, count, want);
  check(all, { [`every ${label} record visible`]: (v) => v.t >= 0 && v.n >= want });
  const delay = all.t >= 0 ? all.t - end : WAIT_S * 1000;
  visibleDelay.add(delay);
  const n = count();
  drainRate.add((n - atEnd) / Math.max(delay / 1000, 0.001));
  drainInclusive.add(n / ((DURATION_S * 1000 + delay) / 1000));
  console.log(`${label}: ${n} of ${want} records visible, ${atEnd} at the end of sending, ` +
    (all.t >= 0 ? `tail ${delay} ms` : 'not all became visible'));
  return n;
}

// probe measures the delay record -> visible every PROBE_EVERY_S during the
// load, then the tail until every record of the run is visible.
export function probe(data) {
  const end = Date.now() + DURATION_S * 1000;
  probeDuringLoad(end, Infinity, (i) => {
    const serial = `PROBE-${data.run}-${i}`;
    post('/ingest/bulk', { records: [record(serial, `10.99.${Math.floor(i / 256) % 256}.${i % 256}`, new Date())] }, 'ingest', [202]);
    return () => total(`filter[serial_number]=${serial}`, 'probe-poll') > 0;
  });
  const prefix = `LOAD-${data.run}-`;
  tail('load', () => visibleCount(prefix), OFFERED, end);
}

// rediscoverProbe re-discovers a server from the top of the seed every
// PROBE_EVERY_S during the load; it is visible when its CI was seen after
// the probe was sent. Then it waits until every server of the run was seen
// and counts the CIs that came up new.
export function rediscoverProbe(data) {
  const end = Date.now() + DURATION_S * 1000;
  const probes = probeDuringLoad(end, PROBES, (i) => {
    const d = seedDevice(OBJECTS - i);
    const sent = Date.now();
    if (post('/ingest/bulk', { records: [rediscoverRecord(d.n, new Date())] }, 'ingest', [202]).status === 202) {
      accepted.add(1);
    }
    return () => total(`filter[serial_number]=${d.serial}&filter[last_seen_at][gte]=${since(sent)}`, 'probe-poll') > 0;
  });
  const seen = () => total(seenSince(data.start), 'visible-poll') - data.seen;
  tail('rediscover', seen, OFFERED + probes, end);
  const cis = total('', 'visible-poll');
  newCIs.add(Math.max(0, cis - data.cis));
  console.log(`rediscover: ${cis} CIs, ${data.cis} before the run`);
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
    const res = failed(http.post(`${BASE_URL}/api/v1/discovery/ingest`, JSON.stringify({ records }),
      Object.assign(params('spool'), { compression: 'gzip' })), 'spool');
    if (!check(res, { 'spool 202': (r) => r.status === 202 })) {
      fail(`spooled upload at record ${i}: ${res.status} ${res.body}`);
    }
    accepted.add(records.length);
  }
  const sent = Date.now();
  const atEnd = visibleCount(prefix);
  const backlogAtEnd = backlog();
  const all = waitVisible('spool', () => visibleCount(prefix), SPOOL_RECORDS);
  check(all, { 'every spooled record visible': (v) => v.t >= 0 && v.n >= SPOOL_RECORDS });
  // A shortfall counts as a flush that never ended.
  const done = all.t >= 0 ? all.t : first + WAIT_S * 1000;
  const visible = visibleCount(prefix);
  spoolFlush.add(done - first);
  drainRate.add((visible - atEnd) / Math.max((done - sent) / 1000, 0.001));
  drainInclusive.add(visible / ((done - first) / 1000));
  console.log(`spool: ${visible} of ${SPOOL_RECORDS} records visible; sending took ${sent - first} ms, ` +
    `${atEnd} visible and backlog ${backlogAtEnd.total} at its end ` +
    `(sampled ${Math.round((sent - backlogAtEnd.at) / 1000)} s before), ` +
    (all.t >= 0 ? `all visible ${done - sent} ms later` : 'not all became visible'));
}
