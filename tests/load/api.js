// NFR-02 latency budgets at the NFR-10 seed size (10,000 objects; WP-204).
// Each request type is its own scenario; the thresholds are the p95 budgets
// of NFR-02, so a missed budget fails the run.
//
//   k6 run -e BASE_URL=http://localhost:8080 -e API_KEY=$(cat key) tests/load/api.js
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Trend } from 'k6/metrics';
import { BASE_URL, firstID, get, params, post } from './lib.js';

const DURATION = __ENV.DURATION || '1m';
const VUS = Number(__ENV.VUS || 5);

// The bulk of 500 ids runs as a job; its duration is the time until the
// job finished.
const bulkDuration = new Trend('bulk_500_duration', true);

const reads = (exec) => ({ executor: 'constant-vus', exec, vus: VUS, duration: DURATION });

export const options = {
  scenarios: {
    list: reads('list'),
    search: reads('search'),
    detail: reads('detail'),
    filter: reads('filter'),
    rack: reads('rack'),
    topology: { ...reads('topology'), vus: 2 },
    impact: { ...reads('impact'), vus: 2 },
    bulk: { executor: 'per-vu-iterations', exec: 'bulk', vus: 1, iterations: 3, maxDuration: '10m' },
  },
  thresholds: {
    'http_req_duration{name:list}': ['p(95)<500'],
    'http_req_duration{name:search}': ['p(95)<500'],
    'http_req_duration{name:detail}': ['p(95)<300'],
    'http_req_duration{name:filter}': ['p(95)<500'],
    'http_req_duration{name:rack}': ['p(95)<300'],
    'http_req_duration{name:topology}': ['p(95)<3000'],
    'http_req_duration{name:impact}': ['p(95)<2000'],
    bulk_500_duration: ['p(95)<30000'],
    checks: ['rate>0.99'],
  },
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'max', 'count'],
};

export function setup() {
  const ids = [];
  for (let offset = 0; ids.length < 500; offset += 200) {
    const page = get(`/cis?limit=200&offset=${offset}&sort_by=name`, 'setup').json('data');
    ids.push(...page.map((c) => c.id));
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
  };
}

const pick = (n) => String(5 + Math.floor(Math.random() * n)).padStart(5, '0');

export function list() {
  get('/cis?limit=50', 'list');
  sleep(0.2);
}

export function search() {
  get(`/cis?limit=50&search=srv-${pick(9000)}`, 'search');
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

export function impact(data) {
  get(`/impact/${data.core}?depth=20`, 'impact');
  sleep(1);
}

export function bulk(data) {
  const status = __ITER % 2 === 0 ? 'maintenance' : 'active';
  const start = Date.now();
  const res = post('/cis/bulk', { ids: data.bulk, status }, 'bulk');
  if (res.status === 202) {
    const job = res.json('job_id');
    for (let i = 0; i < 300; i++) {
      const state = http.get(`${BASE_URL}/api/v1/jobs/${job}`, params('bulk-poll')).json('status');
      if (!['queued', 'running'].includes(state)) {
        check(state, { 'bulk job succeeded': (s) => s === 'succeeded' });
        break;
      }
      sleep(0.1);
    }
  }
  bulkDuration.add(Date.now() - start);
}
