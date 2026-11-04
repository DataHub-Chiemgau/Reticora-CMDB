// NFR-02 latency budgets at the NFR-10 seed size (10,000 objects; WP-204).
// Each request type is its own scenario; the thresholds are the p95 budgets
// of NFR-02, so a missed budget fails the run. OBJECTS is the size the
// demo organization was seeded with (cmd/seed -objects, default 10000).
// The search budget is measured at the global search the UI uses
// (global_search, GET /search over the index the workflow rebuilds after
// the seed), next to the name filter of the CI list (list_name_filter), a
// filter on a searchable JSONB attribute (attr_filter, the seed's
// cost_center) and the relationships of a CI (relationships; WP-437,
// F-116).
//
// blast_radius is the blast radius of the core switch with its service
// chain (GET /cis/{id}/blast-radius, IMP-08, WP-297): the seed has no
// services (its servers hang off the switches over connected_to only), so
// the scenario measures the time and, in blast_radius_bytes, the size of
// the answer; the load of the SPOF simulation is covered by
// TestServiceImpactWorstCase.
//
// list_sorted reads the first page of the CI list sorted by name (WP-839,
// idx_ci_list_name); its total has the form of E-104 (exact up to 10,000,
// beyond 10,000 with total_capped). LATENCY_BUDGETS selects the p95
// budgets of the run (E-17, WP-839): all (default, the NFR-02 budgets at
// 10,000 objects), lists (only list, filter and list_sorted, the job
// api-large at 28,000 objects) or none (measured values without a budget,
// api-large at 50,000 objects). The failure thresholds stay in every run.
//
// No request may fail (F-388): http_req_failed is 0 in the run and in every
// scenario, every bulk job succeeds (bulk_jobs_succeeded, the checks of
// the scenario bulk), and bulk_500_duration records only bulk jobs that
// succeeded; a refused bulk request (a few ms) never counts as a fast bulk.
// bulk_fields does the same with set_fields on 500 other CIs (WP-304,
// bulk_fields_500_duration, bulk_fields_jobs_succeeded and the checks of
// the scenario bulk_fields).
// Every failed request is logged with its status (lib.js failed).
//
//   k6 run -e BASE_URL=http://localhost:8080 -e API_KEY=$(cat key) tests/load/api.js
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Trend } from 'k6/metrics';
import { BASE_URL, failed, firstID, get, params, post } from './lib.js';

const DURATION = __ENV.DURATION || '1m';
const VUS = Number(__ENV.VUS || 5);
const OBJECTS = Number(__ENV.OBJECTS || 10000);
const LATENCY_BUDGETS = __ENV.LATENCY_BUDGETS || 'all';
if (!['all', 'lists', 'none'].includes(LATENCY_BUDGETS)) {
  throw new Error(`LATENCY_BUDGETS=${LATENCY_BUDGETS}: all, lists or none`);
}
// TOTAL_CAP is where GET /cis stops counting (E-104).
const TOTAL_CAP = 10000;
// The searchable attribute of the seed and its number of values
// (cmd/seed AttributeKey, CostCenters): CI i holds CC-<i mod 50 + 1>.
const ATTR_KEY = __ENV.ATTR_KEY || 'cost_center';
const ATTR_VALUES = Number(__ENV.ATTR_VALUES || 50);

// The bulk of 500 ids runs as a job; its duration is the time until the
// job succeeded. BULK_JOBS jobs run one after the other, each must succeed.
const BULK_JOBS = 3;
const bulkDuration = new Trend('bulk_500_duration', true);
const bulkSucceeded = new Counter('bulk_jobs_succeeded');
// The bulk set_fields of 500 ids (WP-304) in the same way.
const bulkFieldsDuration = new Trend('bulk_fields_500_duration', true);
const bulkFieldsSucceeded = new Counter('bulk_fields_jobs_succeeded');
// The size of the blast radius answers of the core switch (WP-297).
const blastRadiusBytes = new Trend('blast_radius_bytes');

const reads = (exec) => ({ executor: 'constant-vus', exec, vus: VUS, duration: DURATION });

const scenarios = {
  list: reads('list'),
  list_name_filter: reads('listNameFilter'),
  // WP-839 (E-17): the list sorted by name, two VUs like WP-437's.
  list_sorted: { ...reads('listSorted'), vus: 2 },
  detail: reads('detail'),
  filter: reads('filter'),
  rack: reads('rack'),
  // The scenarios of WP-437 join the mixed load with two VUs each: the
  // concurrent load of the run grows by about a fifth (32 to 38 VUs).
  global_search: { ...reads('globalSearch'), vus: 2 },
  attr_filter: { ...reads('attrFilter'), vus: 2 },
  relationships: { ...reads('relationships'), vus: 2 },
  topology: { ...reads('topology'), vus: 2 },
  impact: { ...reads('impact'), vus: 2 },
  impact_full: { ...reads('impactFull'), vus: 2 },
  blast_radius: { ...reads('blastRadius'), vus: 2 },
  bulk: { executor: 'per-vu-iterations', exec: 'bulk', vus: 1, iterations: BULK_JOBS, maxDuration: '10m' },
  bulk_fields: { executor: 'per-vu-iterations', exec: 'bulkFields', vus: 1, iterations: BULK_JOBS, maxDuration: '10m' },
};

// The p95 budgets of NFR-02 by request name; LATENCY_BUDGETS keeps all of
// them, those of the lists (E-17: list, filter and list_sorted hold at
// 28,000 objects) or none.
const LIST_BUDGETS = ['list', 'filter', 'list_sorted'];
const latency = {
  list: 500,
  list_name_filter: 500,
  list_sorted: 500,
  global_search: 500,
  attr_filter: 500,
  relationships: 500,
  detail: 300,
  filter: 500,
  rack: 300,
  topology: 3000,
  impact: 2000,
  impact_full: 2000,
  blast_radius: 2000,
};
const latencyThresholds = Object.fromEntries(
  Object.entries(latency)
    .filter(([name]) => LATENCY_BUDGETS === 'all' || (LATENCY_BUDGETS === 'lists' && LIST_BUDGETS.includes(name)))
    .map(([name, ms]) => [`http_req_duration{name:${name}}`, [`p(95)<${ms}`]]),
);

export const options = {
  scenarios,
  thresholds: {
    ...latencyThresholds,
    ...(LATENCY_BUDGETS === 'all'
      ? { bulk_500_duration: ['p(95)<30000'], bulk_fields_500_duration: ['p(95)<30000'] }
      : {}),
    // F-388: every bulk job succeeds (a submetric without samples is not
    // evaluated, so the counter also catches bulk jobs that never ran).
    bulk_jobs_succeeded: [`count==${BULK_JOBS}`],
    'checks{scenario:bulk}': ['rate==1'],
    bulk_fields_jobs_succeeded: [`count==${BULK_JOBS}`],
    'checks{scenario:bulk_fields}': ['rate==1'],
    // F-388: no failed request in the run (setup included) and in every
    // scenario.
    http_req_failed: ['rate==0'],
    ...Object.fromEntries(Object.keys(scenarios).map((s) => [`http_req_failed{scenario:${s}}`, ['rate==0']])),
    checks: ['rate>0.99'],
  },
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'max', 'count'],
};

export function setup() {
  const ids = [];
  for (let offset = 0; ids.length < 1000; offset += 200) {
    const page = get(`/cis?limit=200&offset=${offset}&sort_by=name`, 'setup').json('data');
    ids.push(...page.map((c) => c.id));
  }
  // The relationships are asked for 500 CIs from five pages spread over
  // the organization; the hubs (the PDUs) are among them at their share.
  const spread = [];
  for (let k = 0; k < 5; k++) {
    const offset = Math.floor((k * OBJECTS) / 5);
    spread.push(...get(`/cis?limit=100&offset=${offset}&sort_by=name`, 'setup').json('data').map((c) => c.id));
  }
  const racks = get('/racks?limit=1', 'setup').json('data');
  return {
    // sw-00001 is the core switch of the seed: about half of the CIs hang
    // off it, so its topology (depth 1) has more than 2,000 nodes and its
    // impact reaches the whole organization.
    core: firstID('search=sw-00001'),
    server: firstID('search=srv-04242'),
    rack: racks[0].id,
    bulk: ids.slice(0, 500),
    // bulk_fields writes 500 other CIs, so the two bulk scenarios do not
    // wait for each other's row locks.
    bulkFields: ids.slice(500, 1000),
    spread,
  };
}

const pick = (n) => String(5 + Math.floor(Math.random() * n)).padStart(5, '0');

export function list() {
  get('/cis?limit=50', 'list');
  sleep(0.2);
}

// listSorted reads the first page of the CI list sorted by name (WP-839).
// total is exact up to TOTAL_CAP; beyond it is TOTAL_CAP with total_capped
// (E-104).
export function listSorted() {
  const res = get('/cis?limit=50&sort_by=name', 'list_sorted');
  check(res, {
    'list sorted by name has total after E-104': (r) => {
      if (r.status !== 200) {
        return false;
      }
      const total = r.json('total');
      const capped = r.json('total_capped') === true;
      return OBJECTS > TOTAL_CAP ? total === TOTAL_CAP && capped : total <= TOTAL_CAP && !capped;
    },
  });
  sleep(0.2);
}

// listNameFilter is the name filter of the CI list (name ILIKE), formerly
// the scenario search.
export function listNameFilter() {
  get(`/cis?limit=50&search=srv-${pick(9000)}`, 'list_name_filter');
  sleep(0.2);
}

// globalSearch searches a server by its name as the command palette of the
// UI does (GET /search, five hits).
export function globalSearch() {
  const name = `srv-${pick(9000)}`;
  const res = get(`/search?q=${name}&limit=5`, 'global_search');
  check(res, { 'global search finds the CI': (r) => r.status === 200 && (r.json('data') || []).some((h) => h.title === name) });
  sleep(0.2);
}

// attrFilter filters the CI list on a value of the searchable attribute
// (200 CIs per value in the NFR-10 seed).
export function attrFilter() {
  const value = `CC-${String((Math.floor(Math.random() * OBJECTS) + 1) % ATTR_VALUES + 1).padStart(3, '0')}`;
  const res = get(`/cis?limit=50&filter[attributes.${ATTR_KEY}]=${value}`, 'attr_filter');
  check(res, { 'attribute filter finds CIs': (r) => r.status === 200 && (r.json('data') || []).length > 0 });
  sleep(0.2);
}

// relationships lists the relationships of a CI as its detail view does.
export function relationships(data) {
  const id = data.spread[Math.floor(Math.random() * data.spread.length)];
  const res = get(`/cis/${id}/relationships`, 'relationships');
  check(res, { 'relationships of the CI': (r) => r.status === 200 && (r.json('data') || []).length > 0 });
  sleep(0.2);
}

export function detail(data) {
  get(`/cis/${data.server}`, 'detail');
  sleep(0.2);
}

export function filter() {
  get(`/cis?limit=50&filter[status]=active&filter[hostname]=srv-${pick(9000)}`, 'filter');
  sleep(0.2);
}

export function rack(data) {
  get(`/racks/${data.rack}/layout`, 'rack');
  sleep(0.2);
}

export function topology(data) {
  get(`/topology?root_ci_id=${data.core}&depth=1`, 'topology');
  sleep(1);
}

// The fields of the impacted CIs the topology view asks for (frontend
// IMPACT_COMPACT_FIELDS): hops and via instead of paths (NFR-02, WP-335).
const IMPACT_FIELDS = 'name,ci_type,status,heuristic,hops,via,relay';

// impactCount reads count without parsing megabytes of JSON: the keys of
// the answer are sorted, so the top-level count comes before the CIs.
const impactCount = (res) => Number((/"count":(\d+)/.exec(res.body || '') || [])[1] || 0);

// The failure of the core switch reaches every CI of the seed but itself
// and the two PDUs (cmd/seed relationships), at most the CIs the walk
// keeps besides the root (relationship.MaxTraversalNodes, 10,000): 9,999
// in the jobs api-large (WP-839).
const MAX_TRAVERSAL_NODES = 10000;
const IMPACT_ALL = Math.min(OBJECTS - 3, MAX_TRAVERSAL_NODES - 1);

// impact is the impact of the core switch as the topology view asks for it:
// the whole organization at depth 20, at most 250 bytes per impacted CI.
export function impact(data) {
  const res = get(`/impact/${data.core}?depth=20&fields=${IMPACT_FIELDS}`, 'impact');
  const count = impactCount(res);
  check(res, {
    'impact covers the organization': () => count >= IMPACT_ALL,
    'impact <= 250 B per impacted CI': (r) => count > 0 && r.body.length / count <= 250,
  });
  sleep(1);
}

// impactFull is the same impact in the full answer (every field and the
// paths), the default for API clients (API-01).
export function impactFull(data) {
  const res = get(`/impact/${data.core}?depth=20`, 'impact_full');
  const count = impactCount(res);
  check(res, { 'full impact covers the organization': () => count >= IMPACT_ALL });
  sleep(1);
}

// blastRadius is the blast radius of the core switch with the service chain
// (WP-297): the affected CIs, services, clients, sites and SPOF. It records
// the size of the answer.
export function blastRadius(data) {
  const res = get(`/cis/${data.core}/blast-radius`, 'blast_radius');
  if (res.status === 200) {
    blastRadiusBytes.add((res.body || '').length);
  }
  check(res, { 'blast radius of the core switch': (r) => r.status === 200 });
  sleep(1);
}

// runBulkJob posts a bulk request of 500 ids (202, a job) and waits until
// the job ended. Only a job that succeeded adds its duration to trend and
// counts in succeeded; a refused request, a job that failed or one still
// running after the wait fails the check "<name> job succeeded" (F-388).
function runBulkJob(name, body, trend, succeeded) {
  const start = Date.now();
  const res = post('/cis/bulk', body, name);
  let state = `HTTP ${res.status}`;
  if (res.status === 202) {
    const job = res.json('job_id');
    for (let i = 0; i < 300; i++) {
      const poll = failed(http.get(`${BASE_URL}/api/v1/jobs/${job}`, params(`${name}-poll`)), `${name}-poll`);
      state = poll.status === 200 ? poll.json('status') : `HTTP ${poll.status}`;
      if (!['queued', 'running'].includes(state)) {
        break;
      }
      sleep(0.1);
    }
  }
  if (check(state, { [`${name} job succeeded`]: (s) => s === 'succeeded' })) {
    trend.add(Date.now() - start);
    succeeded.add(1);
  } else {
    console.warn(`${name} job ${__ITER + 1} of ${BULK_JOBS} did not succeed: ${state}`);
  }
}

// bulk sets the status of 500 CIs (202, a job) and waits until the job
// ended (runBulkJob, bulk_500_duration, bulk_jobs_succeeded).
export function bulk(data) {
  const status = __ITER % 2 === 0 ? 'maintenance' : 'active';
  runBulkJob('bulk', { ids: data.bulk, status }, bulkDuration, bulkSucceeded);
}

// bulkFields sets a typed field of 500 other CIs with set_fields (WP-304,
// NFR-02; bulk_fields_500_duration, bulk_fields_jobs_succeeded): every
// iteration writes another value, so each CI changes (override, version,
// history, audit, event).
export function bulkFields(data) {
  const fields = { firmware_version: `load-${__ITER + 1}` };
  runBulkJob(
    'bulk_fields',
    { action: 'set_fields', ids: data.bulkFields, params: { fields } },
    bulkFieldsDuration,
    bulkFieldsSucceeded,
  );
}
