// NFR-02 budgets of the Phase-2 reads and bulk operations at the Phase-2
// load seed (ABN-02, WP-822): the job phase2 of load.yml seeds 10,000 CIs
// and 10,000 assets, tickets and consumables (cmd/seed -objects, -assets,
// -tickets, -consumables) and starts the server with RETICORA_MODULES=P2,
// the release configuration of G2. Like api.js every request type is a
// scenario of its own with the p95 budget of NFR-02 as threshold:
//
//   asset_list, ticket_list, consumable_list: the first page of the lists
//     with total (K23; GET /assets, /tickets, /consumables).
//   asset_list_restricted: the asset list with the key of the role user
//     viewer (API_KEY_RESTRICTED): its field grants hide purchase_price and
//     cost_center (WP-819, the projection of WP-271).
//   search_fields: the global search over the fields of WP-308: inventory
//     number, management IP, MAC of eth0, a tag and the custom field
//     cost_center; the terms are samples of setup().
//   ci_attr_filter: the CI list with a range on cost_center (the equality
//     filter is attr_filter of api.js) or with the computed attribute
//     warranty_days_left <= 30 (WP-718).
//   ci_tag_filter: the CI list filtered on tags (filter[tags][in], WP-281).
//   ast_asset_filter: POST /search/query with a where on asset fields: an
//     asset query and a CI query on the linked asset with the computed
//     attribute (WP-309).
//   view_presets: the filter_spec of the system views warranty_expiring,
//     servers_without_monitoring, my_devices_ci and my_devices_asset
//     (GET /saved-views/presets) over POST /search/query (WP-307).
//   relation_search: POST /search/query with downstream_of and upstream_of
//     (depth MaxRelationshipDepth = 5) at the core switch sw-00001, the
//     worst case, and at the server srv-04242 (SRC-04).
//   bulk_assets, bulk_cis_ops: 500 ids per job until the job ended (NFR-02
//     "Bulk 500 IDs < 30 s"): set_fields on assets (WP-781) and add_tags on
//     CIs (WP-304). The status change of CIs stays in api.js (bulk).
//
// No request may fail (F-388): http_req_failed is 0 in the run and in every
// scenario; every bulk job succeeds (bulk_assets_jobs_succeeded,
// bulk_cis_ops_jobs_succeeded, the checks of the bulk scenarios), and the
// bulk durations record only jobs that succeeded. setup() ends the run when
// a Phase-2 list does not answer 200 (RETICORA_MODULES) and creates the
// load tags of the scenarios in the tag catalog (the seed has none): the
// tag loadphase2 on 500 CIs for the tag filter and the search, the tags
// loadbulk1 to loadbulk3 for bulk_cis_ops.
//
//   k6 run -e BASE_URL=http://localhost:8080 -e API_KEY=$(cat key) \
//     -e API_KEY_RESTRICTED=$(cat viewer.key) -e OBJECTS=10000 tests/load/phase2.js
import http from 'k6/http';
import { check, fail, sleep } from 'k6';
import { Counter, Trend } from 'k6/metrics';
import { BASE_URL, failed, firstID, get, params, post, seedDevice } from './lib.js';

const DURATION = __ENV.DURATION || '1m';
const VUS = Number(__ENV.VUS || 3);
const OBJECTS = Number(__ENV.OBJECTS || 10000);
// The searchable attribute of the seed and its number of values
// (cmd/seed AttributeKey, CostCenters): CI i holds CC-<i mod 50 + 1>.
const ATTR_KEY = __ENV.ATTR_KEY || 'cost_center';
const ATTR_VALUES = Number(__ENV.ATTR_VALUES || 50);

// The ids of one bulk job (NFR-02, ci.MaxBulkIDs).
const BULK_IDS = 500;
// BULK_JOBS jobs per bulk operation, one after the other; each must succeed.
const BULK_JOBS = 3;
// The tag of the tag filter and the tag search, and the tags bulk_cis_ops
// adds (one per job, so every job changes every CI).
const FILTER_TAG = 'loadphase2';
const BULK_TAGS = Array.from({ length: BULK_JOBS }, (_, i) => `loadbulk${i + 1}`);
// The system views of WP-307 the scenario view_presets runs.
const PRESETS = ['warranty_expiring', 'servers_without_monitoring', 'my_devices_ci', 'my_devices_asset'];
// The threshold of warranty_expiring (savedview.WarrantyExpiringDays).
const WARRANTY_DAYS = 30;

const bulkAssetsDuration = new Trend('bulk_assets_500_duration', true);
const bulkAssetsSucceeded = new Counter('bulk_assets_jobs_succeeded');
const bulkCIOpsDuration = new Trend('bulk_cis_ops_500_duration', true);
const bulkCIOpsSucceeded = new Counter('bulk_cis_ops_jobs_succeeded');

const reads = (exec) => ({ executor: 'constant-vus', exec, vus: VUS, duration: DURATION });
const bulk = (exec) => ({ executor: 'per-vu-iterations', exec, vus: 1, iterations: BULK_JOBS, maxDuration: '10m' });

const scenarios = {
  asset_list: reads('assetList'),
  asset_list_restricted: reads('assetListRestricted'),
  ticket_list: reads('ticketList'),
  consumable_list: reads('consumableList'),
  search_fields: reads('searchFields'),
  ci_attr_filter: reads('ciAttrFilter'),
  ci_tag_filter: reads('ciTagFilter'),
  ast_asset_filter: reads('astAssetFilter'),
  view_presets: reads('viewPresets'),
  relation_search: reads('relationSearch'),
  bulk_assets: bulk('bulkAssets'),
  bulk_cis_ops: bulk('bulkCIOps'),
};

// NFR-02 "Listen und Suche" and "indizierte Attributfilter": p95 < 500 ms
// for every read; each read scenario names its requests like itself.
const READS = Object.keys(scenarios).filter((s) => !s.startsWith('bulk_'));

export const options = {
  scenarios,
  thresholds: {
    ...Object.fromEntries(READS.map((name) => [`http_req_duration{name:${name}}`, ['p(95)<500']])),
    // NFR-02 "Bulk 500 IDs < 30 s".
    bulk_assets_500_duration: ['p(95)<30000'],
    bulk_cis_ops_500_duration: ['p(95)<30000'],
    // F-388: every bulk job succeeds (a submetric without samples is not
    // evaluated, so the counters also catch bulk jobs that never ran).
    bulk_assets_jobs_succeeded: [`count==${BULK_JOBS}`],
    'checks{scenario:bulk_assets}': ['rate==1'],
    bulk_cis_ops_jobs_succeeded: [`count==${BULK_JOBS}`],
    'checks{scenario:bulk_cis_ops}': ['rate==1'],
    // F-388: no failed request in the run (setup included) and in every
    // scenario.
    http_req_failed: ['rate==0'],
    ...Object.fromEntries(Object.keys(scenarios).map((s) => [`http_req_failed{scenario:${s}}`, ['rate==0']])),
    checks: ['rate>0.99'],
  },
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'max', 'count'],
};

// waitJob waits until the job ended and returns its status (or the HTTP
// status of a poll that did not answer 200).
function waitJob(job, name) {
  let state = 'queued';
  for (let i = 0; i < 600; i++) {
    const poll = failed(http.get(`${BASE_URL}/api/v1/jobs/${job}`, params(`${name}-poll`)), `${name}-poll`);
    state = poll.status === 200 ? poll.json('status') : `HTTP ${poll.status}`;
    if (!['queued', 'running'].includes(state)) {
      break;
    }
    sleep(0.1);
  }
  return state;
}

// collect reads the ids (and with pick further values) of the first n rows
// of a list, page by page over next_cursor (api.ParsePagination).
function collect(path, n, pick = (row) => row.id) {
  const out = [];
  let cursor = '';
  const sep = path.includes('?') ? '&' : '?';
  while (out.length < n) {
    const res = get(`${path}${sep}limit=200${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`, 'setup');
    if (res.status !== 200) {
      fail(`GET /api/v1${path}: HTTP ${res.status}`);
    }
    out.push(...(res.json('data') || []).map(pick));
    cursor = res.json('next_cursor') || '';
    if (!cursor) {
      break;
    }
  }
  return out.slice(0, n);
}

// ensureTags creates the load tags the catalog does not hold yet (a second
// run against the same database finds them).
function ensureTags(keys) {
  const have = collect('/tags', 1000, (t) => t.key);
  for (const key of keys.filter((k) => !have.includes(k))) {
    const res = post('/tags', { key }, 'setup', [201]);
    if (res.status !== 201) {
      fail(`POST /api/v1/tags ${key}: HTTP ${res.status}`);
    }
  }
}

// setupBulk runs a CI bulk of the setup and ends the run unless it succeeds.
function setupBulk(body) {
  const res = post('/cis/bulk', body, 'setup', [202]);
  const state = res.status === 202 ? waitJob(res.json('job_id'), 'setup') : `HTTP ${res.status}`;
  if (state !== 'succeeded') {
    fail(`setup: bulk ${body.action} did not succeed: ${state}`);
  }
}

export function setup() {
  // The Phase-2 lists are registered only with their modules (ParseModules,
  // server/modules.go; empty RETICORA_MODULES switches them off).
  for (const list of ['assets', 'tickets', 'consumables']) {
    const res = failed(http.get(`${BASE_URL}/api/v1/${list}?limit=1`, params('setup')), 'setup');
    if (res.status !== 200) {
      fail(`GET /api/v1/${list}: HTTP ${res.status}; start the server with RETICORA_MODULES=P2 (tests/load/README.md)`);
    }
  }
  const restricted = failed(http.get(`${BASE_URL}/api/v1/assets?limit=1`, params('setup', { key: 'restricted' })), 'setup');
  if (restricted.status !== 200) {
    fail(`GET /api/v1/assets with API_KEY_RESTRICTED: HTTP ${restricted.status}; issue it with cmd/seed -api-key-role viewer`);
  }

  const assets = collect('/assets', BULK_IDS, (a) => ({ id: a.id, inventory: a.inventory_no }));
  if (assets.length < BULK_IDS) {
    fail(`${assets.length} assets, bulk_assets needs ${BULK_IDS}; seed with cmd/seed -assets 10000`);
  }
  const cis = collect('/cis?sort_by=name', 2 * BULK_IDS);
  if (cis.length < 2 * BULK_IDS) {
    fail(`${cis.length} CIs, the scenarios need ${2 * BULK_IDS}; seed with cmd/seed -objects 10000`);
  }

  // The load tags: loadphase2 on the first 500 CIs (tag filter, search),
  // the tags of bulk_cis_ops on the next 500 while the scenario runs.
  ensureTags([FILTER_TAG, ...BULK_TAGS]);
  setupBulk({ action: 'add_tags', ids: cis.slice(0, BULK_IDS), params: { tags: [FILTER_TAG] } });

  const presets = get('/saved-views/presets', 'setup').json('data') || [];
  const views = PRESETS.map((key) => {
    const p = presets.find((v) => v.key === key);
    if (!p) {
      fail(`no system view ${key} in GET /api/v1/saved-views/presets`);
    }
    return { entity_kind: p.entity_kind, ...p.filter_spec, sort: p.sort, columns: p.columns };
  });

  // The search terms per field of WP-308: inventory numbers of the listed
  // assets, management IP and MAC of eth0 of random seed CIs (lib.js
  // seedDevice), the load tag and the values of the custom field.
  const devices = Array.from({ length: 50 }, () => seedDevice(5 + Math.floor(Math.random() * (OBJECTS - 4))));
  const terms = {
    inventory: assets.slice(0, 50).map((a) => a.inventory),
    ip: devices.map((d) => d.ip),
    mac: devices.map((d) => d.mac),
    tag: [FILTER_TAG],
    custom: Array.from({ length: ATTR_VALUES }, (_, i) => `CC-${String(i + 1).padStart(3, '0')}`),
  };
  for (const [kind, list] of Object.entries(terms)) {
    const res = get(`/search?q=${encodeURIComponent(list[0])}&limit=5`, 'setup');
    if ((res.json('data') || []).length === 0) {
      fail(`the search finds nothing for ${kind} ${list[0]}; rebuild the search index (POST /api/v1/search/reindex)`);
    }
  }

  return {
    // sw-00001 is the core switch of the seed: about half of the CIs hang
    // off it (the worst case of the relationship walk).
    core: firstID('search=sw-00001'),
    server: firstID('search=srv-04242'),
    assets: assets.map((a) => a.id),
    bulkCIs: cis.slice(BULK_IDS, 2 * BULK_IDS),
    views,
    terms,
  };
}

const any = (list) => list[Math.floor(Math.random() * list.length)];
const costCenter = (i) => `CC-${String((i % ATTR_VALUES) + 1).padStart(3, '0')}`;
const rows = (r) => (r.status === 200 ? r.json('data') || [] : []);

// list reads the first page of a Phase-2 list with its total (K23).
function list(path, name, extra = {}) {
  const res = get(path, name, extra);
  check(res, { [`${name} has total`]: (r) => r.status === 200 && typeof r.json('total') === 'number' });
  sleep(0.2);
  return res;
}

export function assetList() {
  list('/assets?limit=50', 'asset_list');
}

// assetListRestricted lists the assets as the role user viewer: its field
// grants hide the purchase price and the cost center.
export function assetListRestricted() {
  const res = list('/assets?limit=50', 'asset_list_restricted', { key: 'restricted' });
  check(res, {
    'restricted asset list hides purchase_price and cost_center': (r) => {
      const data = rows(r);
      return data.length > 0 && data.every((a) => !('purchase_price' in a) && !('cost_center' in a));
    },
  });
}

export function ticketList() {
  list('/tickets?limit=50', 'ticket_list');
}

export function consumableList() {
  list('/consumables?limit=50', 'consumable_list');
}

// searchFields searches one of the fields of WP-308 as the command palette
// does (GET /search).
export function searchFields(data) {
  const kind = any(Object.keys(data.terms));
  const q = any(data.terms[kind]);
  const res = get(`/search?q=${encodeURIComponent(q)}&limit=5`, 'search_fields');
  check(res, { 'search over the fields finds hits': (r) => rows(r).length > 0 });
  sleep(0.2);
}

// ciAttrFilter filters the CI list on a range of three values of the
// searchable attribute or on the computed warranty_days_left.
export function ciAttrFilter() {
  if (__ITER % 2 === 0) {
    const from = Math.floor(Math.random() * (ATTR_VALUES - 2));
    const attr = `filter[attributes.${ATTR_KEY}]`;
    const res = get(`/cis?limit=50&${attr}[gte]=${costCenter(from)}&${attr}[lte]=${costCenter(from + 2)}`, 'ci_attr_filter');
    check(res, { 'attribute range finds CIs': (r) => rows(r).length > 0 });
  } else {
    get(`/cis?limit=50&filter[attributes.warranty_days_left][lte]=${WARRANTY_DAYS}`, 'ci_attr_filter');
  }
  sleep(0.2);
}

// ciTagFilter filters the CI list on tags (at least one of them).
export function ciTagFilter() {
  const res = get(`/cis?limit=50&filter[tags][in]=${FILTER_TAG},archived`, 'ci_tag_filter');
  check(res, { 'tag filter finds CIs': (r) => rows(r).length > 0 });
  sleep(0.2);
}

// astAssetFilter runs the filter AST on asset fields: an asset query on
// manufacturer and cost center, and a CI query on the linked asset with
// the computed attribute warranty_days_left.
export function astAssetFilter() {
  const body =
    __ITER % 2 === 0
      ? {
          entity_kind: 'asset',
          where: {
            and: [
              { field: 'manufacturer', op: 'eq', value: 'Reticora Labs' },
              { field: 'cost_center', op: 'eq', value: costCenter(Math.floor(Math.random() * ATTR_VALUES)) },
            ],
          },
        }
      : {
          entity_kind: 'ci',
          where: {
            and: [
              { field: 'asset.manufacturer', op: 'eq', value: 'Reticora Labs' },
              { field: 'attributes.warranty_days_left', op: 'lte', value: 365 },
            ],
          },
        };
  post('/search/query?limit=50', body, 'ast_asset_filter', [200]);
  sleep(0.2);
}

// viewPresets runs the filter_spec of one of the system views.
export function viewPresets(data) {
  post('/search/query?limit=50', any(data.views), 'view_presets', [200]);
  sleep(0.2);
}

// relationSearch finds the CIs downstream or upstream of the core switch
// or of a server over at most five relationships.
export function relationSearch(data) {
  const body = { entity_kind: 'ci', [any(['downstream_of', 'upstream_of'])]: any([data.core, data.server]) };
  const res = post('/search/query?limit=50', body, 'relation_search', [200]);
  check(res, { 'relation search has total': (r) => r.status === 200 && typeof r.json('total') === 'number' });
  sleep(0.2);
}

// runBulkJob posts a bulk request of 500 ids (202, a job) and waits until
// the job ended. Only a job that succeeded adds its duration to trend and
// counts in succeeded; a refused request, a job that failed or one still
// running after the wait fails the check "<name> job succeeded" (F-388).
function runBulkJob(path, name, body, trend, succeeded) {
  const start = Date.now();
  const res = post(path, body, name, [202]);
  const state = res.status === 202 ? waitJob(res.json('job_id'), name) : `HTTP ${res.status}`;
  if (check(state, { [`${name} job succeeded`]: (s) => s === 'succeeded' })) {
    trend.add(Date.now() - start);
    succeeded.add(1);
  } else {
    console.warn(`${name} job ${__ITER + 1} of ${BULK_JOBS} did not succeed: ${state}`);
  }
}

// bulkAssets sets the notes of 500 assets with set_fields (WP-781): every
// job writes another value, so each asset changes.
export function bulkAssets(data) {
  runBulkJob(
    '/assets/bulk',
    'bulk_assets',
    { action: 'set_fields', ids: data.assets, params: { fields: { notes: `load-${__ITER + 1}` } } },
    bulkAssetsDuration,
    bulkAssetsSucceeded,
  );
}

// bulkCIOps adds a tag to 500 CIs (WP-304): every job adds another tag.
export function bulkCIOps(data) {
  runBulkJob(
    '/cis/bulk',
    'bulk_cis_ops',
    { action: 'add_tags', ids: data.bulkCIs, params: { tags: [BULK_TAGS[__ITER % BULK_JOBS]] } },
    bulkCIOpsDuration,
    bulkCIOpsSucceeded,
  );
}
