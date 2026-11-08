// Mass write jobs of Phase 2 beside the ingest (ABN-02, NFR-03, WP-823).
// load.yml runs each scenario as a job of its own (import, template,
// computed): a fresh database, the seed, ANALYZE, four ingest lanes and
// the modules of phase P2. First writes.js with PHASE=prepare prepares
// the scenario (not measured, a k6 run of its own); then tests/load/
// ingest.js (SCENARIO=load, 200 records/s for DURATION_S = 120 s) runs in
// the background while writes.js (PHASE=measure, the default) measures
// the write job until it ended. Three scenarios, selected with SCENARIO:
//
//   import  (default) The preparation creates the import profile
//           load-writes (POST /api/v1/import-profiles; entity ci, format
//           json, identity serial_number). Measured: IMPORT_ROWS (10,000)
//           new server rows over POST /api/v1/imports with rows and
//           profile_id, in calls of at most 5,000 rows (E-49), mode
//           create_missing, until every job ended (GET /api/v1/jobs/{id};
//           import_10000_duration, target < 5 min). Then the same rows
//           with a new Idempotency-Key and mode update_existing, without a
//           manual ANALYZE (import_update_duration, item 8 of
//           lasttest-g1.md); the update rows carry another model, so every
//           CI is written. Every job succeeds and GET /api/v1/imports/{id}
//           reports every row read and none failed.
//   template The preparation creates the global attribute
//           load_template_tier (the system type server takes no attribute
//           of the organization), the template load_writes of the type server
//           with version 1 (the attribute) and version 2 (the attribute
//           with the default "migrated"), both published, and assigns
//           version 1 to every server of the seed (srv-*; PUT
//           /api/v1/cis/{id}/templates, ASSIGN_PARALLEL requests at a
//           time): POST /cis/bulk change_template migrates or replaces the
//           templates a CI holds and adds none (WP-780), and the seed
//           templates belong to the derived types blade_chassis and
//           blade_server, outside the type chain of server. Measured: POST
//           /api/v1/templates/{id}/migrations from version 1 to version 2
//           with a where on these CIs until the job ended
//           (template_migrate_duration); totals.migrated of GET
//           /api/v1/jobs/{id}/result is every server of the seed
//           (template_migrated).
//   computed Measured: POST /api/v1/attribute-definitions creates the
//           global computed attribute load_asset_age_days (days from the
//           purchase date of the linked asset to today, like
//           warranty_days_left) and the job attribute.computed_backfill
//           over every CI runs until it ended (GET
//           /api/v1/jobs?kind=attribute.computed_backfill, the job of the
//           definition; computed_backfill_duration). The preparation only
//           checks that the attribute does not exist yet.
//
// The durations are measures without a budget (WP-327 reports them); a
// duration records only a run whose jobs all succeeded. No request may
// fail (F-388): http_req_failed is 0 in the run and in every scenario; the
// counter <scenario>_jobs_succeeded counts the jobs that succeeded (import
// 4, template and computed 1) and every check holds.
//
//   k6 run -e BASE_URL=http://localhost:8080 -e API_KEY=$(cat key) -e SCENARIO=import -e PHASE=prepare tests/load/writes.js
//   k6 run -e BASE_URL=http://localhost:8080 -e API_KEY=$(cat key) -e SCENARIO=import tests/load/writes.js
import http from 'k6/http';
import { check, fail, sleep } from 'k6';
import { Counter, Trend } from 'k6/metrics';
import { BASE_URL, failed, get, params, post } from './lib.js';

const SCENARIO = __ENV.SCENARIO || 'import';
const PHASE = __ENV.PHASE || 'measure';
// The seed size of the demo organization: CIs 5 … OBJECTS are its servers
// srv-00005 … (lib.js seedDevice).
const OBJECTS = Number(__ENV.OBJECTS || 10000);
const SERVERS = OBJECTS - 4;
const IMPORT_ROWS = Number(__ENV.IMPORT_ROWS || 10000);
// E-49: at most 5,000 rows per API call.
const IMPORT_CHUNK = 5000;
const ASSIGN_PARALLEL = Number(__ENV.ASSIGN_PARALLEL || 20);
// How long a job may run until the scenario gives up on it.
const WAIT_S = Number(__ENV.WAIT_S || 1800);

const PROFILE = 'load-writes';
const TEMPLATE_KEY = 'load_writes';
const TEMPLATE_ATTRIBUTE = 'load_template_tier';
const TEMPLATE_DEFAULT = 'migrated';
const COMPUTED_KEY = 'load_asset_age_days';
// The servers of the seed (name srv-<n>).
const SERVER_FILTER = { field: 'name', op: 'like', value: 'srv-*' };

// The jobs every scenario runs; each must succeed.
const JOBS = { import: 2 * Math.ceil(IMPORT_ROWS / IMPORT_CHUNK), template: 1, computed: 1 };
if (!JOBS[SCENARIO]) {
  fail(`SCENARIO must be import, template or computed, not ${SCENARIO}`);
}
if (PHASE !== 'prepare' && PHASE !== 'measure') {
  fail(`PHASE must be prepare or measure, not ${PHASE}`);
}

const importDuration = new Trend('import_10000_duration', true);
const importUpdateDuration = new Trend('import_update_duration', true);
const templateDuration = new Trend('template_migrate_duration', true);
const templateMigrated = new Counter('template_migrated');
const computedDuration = new Trend('computed_backfill_duration', true);
const succeeded = new Counter(`${SCENARIO}_jobs_succeeded`);

// One scenario per run: the preparation (prepare) or the scenario itself.
const name = PHASE === 'prepare' ? 'prepare' : SCENARIO;
const title = `${SCENARIO[0].toUpperCase()}${SCENARIO.slice(1)}`;
const exec = PHASE === 'prepare' ? `prepare${title}` : `run${title}`;

export const options = {
  scenarios: {
    [name]: { executor: 'per-vu-iterations', exec, vus: 1, iterations: 1, maxDuration: `${2 * WAIT_S}s` },
  },
  thresholds: {
    // F-388: no failed request in the run (setup included) and in the
    // scenario, every check holds.
    http_req_failed: ['rate==0'],
    [`http_req_failed{scenario:${name}}`]: ['rate==0'],
    checks: ['rate==1'],
    // Every job of the scenario succeeds (a counter without samples is
    // evaluated, so it also catches jobs that never ran).
    ...(PHASE === 'measure' ? { [`${SCENARIO}_jobs_succeeded`]: [`count==${JOBS[SCENARIO]}`] } : {}),
  },
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'max', 'count'],
};

// request sends a request named name with the API key, logs it when it
// failed (lib.js failed) and returns the answer.
function request(method, path, body, name, extra = {}) {
  const res = http.request(method, `${BASE_URL}/api/v1${path}`, body === undefined ? null : JSON.stringify(body),
    params(name, extra));
  return failed(res, name);
}

// must ends the run unless res has one of the statuses ok.
function must(res, what, ok = [200]) {
  if (!ok.includes(res.status)) {
    fail(`${what}: HTTP ${res.status} ${String(res.body).slice(0, 300)}`);
  }
  return res;
}

// waitJob waits until the job ended and returns it (status "HTTP <n>" for
// a poll that did not answer 200).
function waitJob(id, name) {
  const deadline = Date.now() + WAIT_S * 1000;
  let job = { id, status: 'queued' };
  while (Date.now() < deadline) {
    const res = request('GET', `/jobs/${id}`, undefined, `${name}-poll`);
    job = res.status === 200 ? res.json() : { id, status: `HTTP ${res.status}` };
    if (!['queued', 'running'].includes(job.status)) {
      return job;
    }
    sleep(0.5);
  }
  return job;
}

// all reads every row of a list page by page over next_cursor.
function all(path, name = 'setup') {
  const out = [];
  let cursor = '';
  const sep = path.includes('?') ? '&' : '?';
  for (;;) {
    const res = must(get(`${path}${sep}limit=200${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`, name), `GET ${path}`);
    out.push(...(res.json('data') || []));
    cursor = res.json('next_cursor') || '';
    if (!cursor) {
      return out;
    }
  }
}

// serverType returns the id of the CI type server.
function serverType() {
  const t = all('/ci-types').find((x) => x.key === 'server');
  if (!t) {
    fail('no CI type server; seed the demo organization with backend/cmd/seed');
  }
  return t.id;
}

// ---- import ----

export function prepareImport() {
  if (all('/import-profiles').some((p) => p.name === PROFILE)) {
    return;
  }
  const columns = Object.fromEntries(['ci_type', 'name', 'serial_number', 'model'].map((c) => [c, { target: c }]));
  must(post('/import-profiles', { name: PROFILE, entity: 'ci', format: 'json', mode: 'create_missing',
    mapping: { columns }, identity_keys: ['serial_number'] }, 'prepare', [201]), 'POST /api/v1/import-profiles', [201]);
}

// importRows starts the import of rows in calls of IMPORT_CHUNK rows and
// waits until every job ended. It returns whether every job succeeded
// with every row read and none failed.
function importRows(data, rows, mode, label) {
  const ids = [];
  for (let i = 0; i < rows.length; i += IMPORT_CHUNK) {
    const res = post('/imports', { profile_id: data.profile, mode, rows: rows.slice(i, i + IMPORT_CHUNK) }, 'import', [202],
      { headers: { 'Idempotency-Key': `load-${data.run}-${label}-${i}` }, timeout: '300s' });
    ids.push(res.status === 202 ? res.json('job_id') : '');
  }
  let ok = true;
  ids.forEach((id, k) => {
    const job = id ? waitJob(id, 'import') : { status: 'not started' };
    const done = check(job, { [`import ${label} job succeeded`]: (j) => j.status === 'succeeded' });
    const run = done ? must(get(`/imports/${id}`, 'import-status'), `GET /api/v1/imports/${id}`).json() : {};
    const want = Math.min(IMPORT_CHUNK, rows.length - k * IMPORT_CHUNK);
    const clean = done && check(run, {
      [`import ${label} reads every row`]: (r) => r.rows_total === want,
      [`import ${label} has no failed row`]: (r) => r.rows_failed === 0,
    });
    if (clean) {
      succeeded.add(1);
    } else {
      console.warn(`import ${label} job ${k + 1} of ${ids.length} ${id}: ${job.status}, ` +
        `${run.rows_failed} of ${run.rows_total} rows failed ${job.error || ''}`);
    }
    ok = ok && clean;
  });
  return ok;
}

function importSetup() {
  const profile = all('/import-profiles').find((p) => p.name === PROFILE);
  if (!profile) {
    fail(`no import profile ${PROFILE}; run writes.js with PHASE=prepare first`);
  }
  return { profile: profile.id };
}

export function runImport(data) {
  const rows = (model) => Array.from({ length: IMPORT_ROWS }, (_, i) => ({ ci_type: 'server',
    name: `imp-${data.run}-${String(i).padStart(5, '0')}`, serial_number: `IMP-${data.run}-${i}`, model }));
  let start = Date.now();
  if (importRows(data, rows('L-1U'), 'create_missing', 'create')) {
    importDuration.add(Date.now() - start);
  }
  start = Date.now();
  if (importRows(data, rows('L-2U'), 'update_existing', 'update')) {
    importUpdateDuration.add(Date.now() - start);
  }
}

// ---- template ----

// publishVersion replaces the items of draft version v and publishes it.
function publishVersion(template, v, items) {
  must(request('PUT', `/templates/${template}/versions/${v}`, { items }, 'prepare'), `PUT template version ${v}`);
  must(request('POST', `/templates/${template}/versions/${v}/publish`, {}, 'prepare'), `publish template version ${v}`);
}

export function prepareTemplate() {
  const type = serverType();
  // A global attribute: the system type server takes no attribute of the
  // organization.
  let attr = all('/attribute-definitions').find((d) => d.key === TEMPLATE_ATTRIBUTE);
  if (!attr) {
    attr = must(post('/attribute-definitions', { key: TEMPLATE_ATTRIBUTE, data_type: 'text',
      display_name: { de: 'Laststufe', en: 'Load tier' } }, 'prepare', [201]), 'POST /api/v1/attribute-definitions', [201]).json();
  }
  let template = all(`/templates?ci_type_id=${type}`).find((t) => t.key === TEMPLATE_KEY);
  if (!template) {
    template = must(post('/templates', { key: TEMPLATE_KEY, ci_type_id: type,
      display_name: { de: 'Lasttest', en: 'Load test' } }, 'prepare', [201]), 'POST /api/v1/templates', [201]).json();
  }
  if (!template.current_version) {
    publishVersion(template.id, 1, [{ attribute_definition_id: attr.id, position: 0 }]);
  }
  if ((template.current_version || 1) < 2) {
    must(post(`/templates/${template.id}/versions`, {}, 'prepare', [201]), 'POST template version 2', [201]);
    publishVersion(template.id, 2, [{ attribute_definition_id: attr.id, position: 0, default_override: TEMPLATE_DEFAULT }]);
  }
  // Version 1 on every server of the seed, ASSIGN_PARALLEL at a time.
  const servers = all('/cis?filter[name][like]=srv-*').map((c) => c.id);
  if (servers.length !== SERVERS) {
    fail(`${servers.length} servers, the seed of -objects ${OBJECTS} has ${SERVERS}`);
  }
  const body = JSON.stringify({ templates: [{ template_id: template.id, version: 1 }] });
  for (let i = 0; i < servers.length; i += ASSIGN_PARALLEL) {
    const batch = servers.slice(i, i + ASSIGN_PARALLEL)
      .map((id) => ['PUT', `${BASE_URL}/api/v1/cis/${id}/templates`, body, params('prepare')]);
    http.batch(batch).forEach((res) => must(failed(res, 'prepare'), `PUT /api/v1/cis/{id}/templates at server ${i}`));
  }
}

function templateSetup() {
  const type = serverType();
  const template = all(`/templates?ci_type_id=${type}`).find((t) => t.key === TEMPLATE_KEY);
  if (!template || template.current_version !== 2) {
    fail(`no template ${TEMPLATE_KEY} with version 2; run writes.js with PHASE=prepare first`);
  }
  return { template: template.id };
}

export function runTemplate(data) {
  const start = Date.now();
  const res = post(`/templates/${data.template}/migrations`, { from_version: 1, to_version: 2, where: SERVER_FILTER },
    'template', [202]);
  const job = res.status === 202 ? waitJob(res.json('job_id'), 'template') : { status: `HTTP ${res.status}` };
  const end = Date.now();
  const done = check(job, { 'template migration job succeeded': (j) => j.status === 'succeeded' });
  const report = done ? must(get(`/jobs/${job.id}/result`, 'template-result'), 'GET the migration report').json() : {};
  const migrated = (report.totals || {}).migrated;
  if (done && check(migrated, { 'template migration migrated every server': (n) => n === SERVERS })) {
    templateDuration.add(end - start);
    succeeded.add(1);
  } else {
    console.warn(`template migration ${job.id}: ${job.status}, ${migrated} of ${SERVERS} migrated ${job.error || ''} ` +
      JSON.stringify(report.totals || {}));
  }
  templateMigrated.add(migrated || 0);
}

// ---- computed ----

export function prepareComputed() {
  if (all('/attribute-definitions').some((d) => d.key === COMPUTED_KEY)) {
    fail(`the attribute ${COMPUTED_KEY} exists already; the scenario needs a fresh database`);
  }
}

function computedSetup() {
  prepareComputed();
  return {};
}

// backfillOf returns the backfill job of the definition, null while there
// is none.
function backfillOf(definition) {
  const res = get('/jobs?kind=attribute.computed_backfill&limit=50', 'computed-poll');
  return (res.json('data') || []).find((j) => (j.params || {}).definition_id === definition) || null;
}

export function runComputed() {
  const start = Date.now();
  const res = post('/attribute-definitions', { key: COMPUTED_KEY, data_type: 'integer',
    display_name: { de: 'Alter des Assets (Tage)', en: 'Asset age (days)' },
    computed: { op: 'days_between', args: [{ op: 'field', field: 'asset.purchase_date' }, { op: 'today' }] } },
  'computed', [201]);
  let job = { status: `HTTP ${res.status}` };
  if (res.status === 201) {
    const definition = res.json('id');
    const deadline = Date.now() + WAIT_S * 1000;
    let found = null;
    while (!found && Date.now() < deadline) {
      found = backfillOf(definition);
      if (!found) {
        sleep(0.5);
      }
    }
    job = found ? waitJob(found.id, 'computed') : { status: 'no backfill job' };
  }
  const end = Date.now();
  if (check(job, { 'computed backfill job succeeded': (j) => j.status === 'succeeded' })) {
    computedDuration.add(end - start);
    succeeded.add(1);
  } else {
    console.warn(`computed backfill ${job.id}: ${job.status} ${job.error || ''}`);
  }
}

// setup gives the scenario its data; the run names the import rows.
export function setup() {
  const data = { run: __ENV.RUN_ID || String(Date.now()) };
  if (PHASE === 'prepare') {
    return data;
  }
  const setups = { import: importSetup, template: templateSetup, computed: computedSetup };
  return { ...data, ...setups[SCENARIO]() };
}

