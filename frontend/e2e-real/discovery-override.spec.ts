import { spawn, spawnSync, type ChildProcess } from 'node:child_process';
import {
  appendFileSync,
  existsSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  statSync,
  writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import {
  test,
  expect,
  type APIRequestContext,
  type Browser,
  type BrowserContext,
  type Page,
} from '@playwright/test';
import {
  api,
  sessionToken,
  signupTenant,
  stamp,
  uiLogin,
  BASE,
  COLLECTOR_BIN,
  SERVER,
  type Account,
} from './support/stack';

/*
 * Phase-1 acceptance audit – discovery → reconciliation → override protection → review,
 * end to end on the real stack (nothing mocked): UI → REST API → PostgreSQL → UI, with the
 * real collector binary (E2E_COLLECTOR_BIN) reporting the simulated 30-device fleet
 * (RETICORA_SIMULATE=true, SIM-02) through the real ingest path (HTTP 401 → spool → mTLS
 * NATS edge → ingest consumer).
 *
 * One organization is created through the self-signup UI; its collector is enrolled with a
 * code minted in the Discovery UI. "Run discovery again" = restart the collector process:
 * the simulator reports the fleet at start-up with a new observation time (new record ids).
 * Every test works on its own CIs of the fleet, so a test proves its claim on its own.
 * Should a test fail, Playwright starts a new worker; the shared tenant and collector are
 * then re-used from a state file of this run instead of enrolling a second collector.
 *
 * Test → WP (requirement IDs) proven:
 *  D01 enrollment code from the Discovery UI, collector enrolls once (code single-use, no
 *      second registration), client certificate (fingerprint, 90 days), status active after
 *      its heartbeats, 30 simulated CIs persisted with their discovered fields and shown in
 *      the UI (collector card, CI detail) ........................... WP-144 (COL-04), WP-145 (COL-04),
 *      WP-156 (INS-06), WP-143 (COL-01), WP-083 (DIS-05), WP-085 (REC-01)
 *  D02 provenance of every typed field of a discovered CI: endpoint (source, rank, recorded
 *      and observed time, reason) and the tooltip in the CI detail ... WP-198 (REC-10), WP-084 (REC-05, GLO-12),
 *      WP-060 (OVR-01: OpenAPI provenance fields)
 *  D03 manual override through the "Override setzen" dialog: required reason, value
 *      persisted with author/time/reason, effective value in detail and list, tooltip,
 *      structural columns refused ...................................... WP-057 (OVR-01, CI-04), WP-060 (OVR-01, CI-10),
 *      WP-198 (REC-10)
 *  D04 discovery runs again: the manual values (override dialog and edit form) are NOT
 *      overwritten; the deviating observation is kept (_observed, provenance) and surfaced
 *      as exactly one override_conflict per field, also in the review inbox diff
 *      ................................................................. WP-057 (OVR-01, CI-04), WP-058 (REC-03, OVR-01, CI-10, REC-12),
 *      WP-089 (REC-12), WP-060 (OVR-01, CI-10), WP-084 (REC-05), WP-196 (UI-16),
 *      WP-090 (REC-07: every sighting sets last_seen_at – the run marker of all tests)
 *  D05 review inbox UI: accept (override removed, observed value taken) and dismiss
 *      (override kept); persisted after reload; a further discovery run keeps the accepted
 *      value, keeps the dismissed value suppressed and does not duplicate an open item;
 *      repeated / disallowed resolutions are refused ................. WP-196 (UI-16), WP-088 (REC-08, API-05),
 *      WP-089 (REC-12), WP-058 (OVR-01), WP-201 (TST-03)
 *  D06 bulk resolution in the review inbox: two items accepted with one action, result
 *      reported, persisted ............................................. WP-196 (UI-16), WP-088 (REC-08), WP-089 (REC-12)
 *  D07 "Override aufheben" in the CI detail returns the field to the observed value, the
 *      page shows it without a reload (overview and provenance agree), the clear is audited
 *      ................................................................. WP-060 (OVR-01, CI-10), WP-413 (CI-10, F-100)
 *  D08 a disabled collector can no longer deliver: status shown in the UI; without a
 *      restart the running collector is cut off within 10 s (no request over the NATS edge
 *      answered, last_seen_at and CI count unchanged over two discovery cycles); after a
 *      restart heartbeats and discovery results are refused, nothing new persisted
 *      (positive control first: last_seen_at rises) ...................... WP-145 (COL-04), WP-143 (COL-01), WP-085 (REC-01),
 *      WP-381 (COL-04, F-025)
 *  D09 override against an open editor: the edit form is open, a second session of the
 *      same user sets an override on model ("Override setzen", the version rises); saving
 *      the form with a changed model is refused with 409, the form shows the conflict and
 *      the override stays (value, provenance, version, UI) ............. WP-061 (API-07, OVR-01), WP-412/WP-413 (F-021)
 *
 * The Discovery UI has no control to disable a collector (CollectorCard shows only the
 * status); D08 therefore disables it through PATCH /collectors/{id} (collector:manage, the
 * documented route) and checks the status in the UI.
 *
 * A failing test is a product finding: the assertions encode the requirement texts in
 * docs/spec/katalog-v3 (COL-*, DIS-*, REC-*, OVR-*) and the WP acceptance criteria, not the
 * observed behaviour.
 */

const SIMULATED_DEVICES = 30;
// Requirement checks after a precondition: a violation is reported, the test goes on.
const softExpect = expect.configure({ soft: true });
// Values the simulator reports (collector/plugins/simulate/simulate.go).
const SIM = {
  switchModel: 'C9300-48P',
  switchVendor: 'Cisco',
  firewallModel: 'FortiGate-100F',
  pduModel: 'AP8959EU3',
  switchSource: 'snmp',
  switchRank: 80, // REC-03: snmp 80
  manualRank: 100, // REC-03: manual/override 100
};

interface CI {
  id: string;
  name: string;
  status: string;
  version: number;
  ci_type_id: string;
  manufacturer?: string;
  model?: string;
  serial_number?: string;
  management_ip?: string;
  hostname?: string;
  discovery_source?: string;
  last_seen_at?: string;
  [field: string]: unknown;
}

interface List<T> {
  data: T[] | null;
  total: number;
}

interface Provenance {
  field: string;
  value: unknown;
  source: string;
  rank: number;
  reason: 'override' | 'source_rank' | 'asset' | 'record';
  recorded_at?: string;
  observed_at?: string;
  override_author?: string;
  override_reason?: string;
  protected?: boolean;
  discovered_value?: unknown;
  discovered_source?: string;
  diverged: boolean;
}

interface ReviewItem {
  id: string;
  kind: string;
  status: 'open' | 'resolved' | 'dismissed';
  payload: Record<string, unknown>;
  candidate_ci_ids?: string[];
  object_key?: string;
  action?: string;
  resolved_by?: string;
  resolved_at?: string;
  created_at: string;
}

interface Collector {
  id: string;
  name: string;
  status: string;
  last_heartbeat_at?: string;
  cert_fingerprint?: string;
  cert_not_after?: string;
}

/** What the tests of one run share; persisted so a restarted worker re-uses it. */
interface SharedState {
  account: Account;
  workDir: string;
  collectorName: string;
  collectorId: string;
  enrollCode: string;
  ip: string;
  startedAt: number;
}

interface World extends SharedState {
  rq: APIRequestContext;
  ctx: BrowserContext;
  page: Page;
  token: string;
  me: string;
}

let w: World;
let collector: ChildProcess | undefined;

// The runner process is the parent of every worker of this run: its pid (and, on Linux,
// its start time) keys the state.
function runnerKey(): string {
  try {
    const stat = readFileSync(`/proc/${process.ppid}/stat`, 'utf8');
    return `${process.ppid}-${stat.slice(stat.lastIndexOf(')') + 2).split(' ')[19]}`;
  } catch {
    return String(process.ppid);
  }
}
const STATE_FILE = join(tmpdir(), `reticora-e2e-discovery-override-${runnerKey()}.json`);

function loadState(): SharedState | undefined {
  if (!existsSync(STATE_FILE)) return undefined;
  if (Date.now() - statSync(STATE_FILE).mtimeMs > 60 * 60_000) return undefined;
  return JSON.parse(readFileSync(STATE_FILE, 'utf8')) as SharedState;
}

/** A documentation address (198.18.0.0/15) so the signup has its own budget (E-33). */
function realIp(): string {
  return `198.18.${Math.floor(Math.random() * 250) + 1}.${Math.floor(Math.random() * 250) + 1}`;
}

function credentialsPath(dir: string) {
  return join(dir, 'credentials.json');
}

function logPath(dir: string) {
  return join(dir, 'collector.log');
}

/** collectorLog returns the collector output written after byte offset from. */
function collectorLog(from = 0): string {
  const path = logPath(w.workDir);
  if (!existsSync(path)) return '';
  return readFileSync(path, 'utf8').slice(from);
}

function collectorLogSize(): number {
  const path = logPath(w.workDir);
  return existsSync(path) ? statSync(path).size : 0;
}

function enroll(code: string, name: string, credentials: string) {
  return spawnSync(
    COLLECTOR_BIN,
    ['enroll', '--server', SERVER, '--code', code, '--name', name, '--credentials', credentials],
    { encoding: 'utf8', timeout: 60_000 },
  );
}

/**
 * startCollector runs the enrolled collector with the simulated fleet; env adds settings
 * (e.g. RETICORA_DISCOVERY_INTERVAL for further discovery cycles of the same process).
 */
function startCollector(dir: string, extra: NodeJS.ProcessEnv = {}): number {
  const env: NodeJS.ProcessEnv = { ...process.env };
  // The collector talks to localhost only.
  for (const key of ['HTTP_PROXY', 'HTTPS_PROXY', 'http_proxy', 'https_proxy', 'ALL_PROXY']) {
    delete env[key];
  }
  const startedAt = Date.now();
  collector = spawn(COLLECTOR_BIN, [], {
    env: {
      ...env,
      RETICORA_ENVIRONMENT: 'development',
      RETICORA_SIMULATE: 'true',
      RETICORA_CREDENTIALS_PATH: credentialsPath(dir),
      RETICORA_SPOOL_DIR: join(dir, 'spool'),
      RETICORA_HEARTBEAT_INTERVAL: '10s',
      RETICORA_SPOOL_FLUSH_INTERVAL: '5s',
      ...extra,
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  const log = logPath(dir);
  appendFileSync(log, `\n### collector started ${new Date(startedAt).toISOString()}\n`);
  collector.stdout?.on('data', (chunk: Buffer) => appendFileSync(log, chunk));
  collector.stderr?.on('data', (chunk: Buffer) => appendFileSync(log, chunk));
  return startedAt;
}

async function stopCollector() {
  const proc = collector;
  collector = undefined;
  if (!proc || proc.exitCode !== null || proc.signalCode !== null) return;
  const exited = new Promise<void>((resolve) => proc.once('exit', () => resolve()));
  proc.kill('SIGTERM');
  const timer = setTimeout(() => proc.kill('SIGKILL'), 10_000);
  await exited;
  clearTimeout(timer);
}

/**
 * restartCollector = "run discovery again"; returns the start time. The start waits for a
 * fresh second, so a sighting in that second (last_seen_at has whole seconds) is this run's.
 */
async function restartCollector(extra: NodeJS.ProcessEnv = {}): Promise<number> {
  await stopCollector();
  await new Promise((resolve) => setTimeout(resolve, 2_000 - (Date.now() % 1000)));
  return startCollector(w.workDir, extra);
}

/**
 * renewToken takes the access token the signed-in page holds now: the SPA renews it before
 * it expires (AUT-02, 15 min), a test that runs longer uses the renewed one.
 */
async function renewToken() {
  await w.page.goto('/discovery');
  await expect(collectorBadge(w.page, w.collectorName)).toBeVisible();
  w.token = await sessionToken(w.page);
}

async function ok<T>(
  method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE',
  path: string,
  data?: unknown,
): Promise<T> {
  const res = await api<T>(w.rq, w.token, method, path, data);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeGreaterThanOrEqual(200);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeLessThan(300);
  return res.body;
}

/** getCollector reads the collector of this run (the API has no GET /collectors/{id}). */
async function getCollector(): Promise<Collector> {
  const list = (await ok<List<Collector>>('GET', '/collectors?limit=100')).data ?? [];
  const found = list.find((c) => c.id === w.collectorId);
  expect(found, `collector ${w.collectorId}`).toBeTruthy();
  return found!;
}

async function fleet(): Promise<Map<string, CI>> {
  const list = await ok<List<CI>>('GET', '/cis?limit=100');
  return new Map((list.data ?? []).map((ci) => [ci.name, ci]));
}

async function ciByName(name: string): Promise<CI> {
  const ci = (await fleet()).get(name);
  expect(ci, `simulated CI ${name}`).toBeTruthy();
  return ci!;
}

async function getCI(id: string): Promise<CI> {
  return ok<CI>('GET', `/cis/${id}`);
}

async function provenance(id: string): Promise<Provenance[]> {
  return (await ok<{ data: Provenance[] }>('GET', `/cis/${id}/provenance`)).data ?? [];
}

async function provField(id: string, field: string): Promise<Provenance | undefined> {
  return (await provenance(id)).find((p) => p.field === field);
}

async function reviewItems(status = ''): Promise<ReviewItem[]> {
  const q = new URLSearchParams({ kind: 'override_conflict', limit: '100' });
  if (status) q.set('status', status);
  return (await ok<List<ReviewItem>>('GET', `/review-items?${q.toString()}`)).data ?? [];
}

/** conflicts are the override_conflict items of one field of one CI. */
async function conflicts(ciId: string, field: string, status = ''): Promise<ReviewItem[]> {
  return (await reviewItems(status)).filter(
    (item) => item.payload?.ci_id === ciId && item.payload?.field === field,
  );
}

/**
 * waitSeen waits until every CI was seen by a discovery run that started at since.
 * last_seen_at is stored with whole seconds; restartCollector starts a run on a fresh second.
 */
async function waitSeen(ids: string[], since: number, message: string) {
  const second = Math.floor(since / 1000) * 1000;
  await expect
    .poll(
      async () => {
        const all = await fleet();
        const seen = [...all.values()].filter((ci) => ids.includes(ci.id));
        return seen.filter((ci) => ci.last_seen_at && Date.parse(ci.last_seen_at) >= second).length;
      },
      { message, timeout: 120_000, intervals: [2_000] },
    )
    .toBe(ids.length);
}

async function maxLastSeen(): Promise<number> {
  const all = await fleet();
  return Math.max(...[...all.values()].map((ci) => Date.parse(ci.last_seen_at ?? '') || 0));
}

interface AuditEntry {
  action: string;
  actor_id?: string;
  resource_id?: string;
}

/** pollAudit waits until the audit trail has an entry of `action` about the CI. */
async function pollAudit(ciId: string, action: string): Promise<AuditEntry> {
  let found: AuditEntry | undefined;
  await expect
    .poll(
      async () => {
        const res = await api<List<AuditEntry>>(
          w.rq,
          w.token,
          'GET',
          `/audit-log?resource_type=ci&resource_id=${ciId}&limit=200`,
        );
        found =
          res.status === 200 ? (res.body.data ?? []).find((e) => e.action === action) : undefined;
        return Boolean(found);
      },
      { message: `audit entry ${action} of CI ${ciId}`, timeout: 30_000, intervals: [1_000] },
    )
    .toBe(true);
  return found!;
}

async function setOverrideApi(ciId: string, field: string, value: string, reason: string) {
  await ok('PUT', `/cis/${ciId}/fields/${field}/override`, { value, reason, protected: true });
}

// ─── UI helpers ──────────────────────────────────────────────────────────────

/** collectorBadge is the status badge in the header of a collector card (Discovery page). */
function collectorBadge(page: Page, name: string) {
  return page
    .getByRole('heading', { level: 3, name, exact: true })
    .locator('xpath=../..')
    .locator('.shrink-0');
}

async function openCI(page: Page, ci: CI) {
  await page.goto(`/cmdb/${ci.id}`);
  await expect(page.getByRole('heading', { level: 2, name: ci.name })).toBeVisible();
}

/** detailValue is the value of a row of the overview card ("Modell", "Hersteller", …). */
function detailValue(page: Page, label: string) {
  return page
    .locator('dl > div')
    .filter({ has: page.locator('dt', { hasText: new RegExp(`^${label}$`) }) })
    .locator('dd')
    .first();
}

async function openProvenance(page: Page) {
  const section = page.locator('details').filter({
    has: page.locator('summary', { hasText: 'Herkunft & Overrides' }),
  });
  if (!(await section.getAttribute('open').then((v) => v !== null))) {
    await section.locator('summary').click();
  }
  return section;
}

async function provenanceRow(page: Page, field: string) {
  const section = await openProvenance(page);
  const row = section
    .locator('li')
    .filter({ has: page.getByRole('button', { name: `Herkunft von ${field}`, exact: true }) });
  await expect(row, `provenance row of ${field}`).toHaveCount(1);
  return row;
}

/** provenanceCell is the value of a column (Discovery / Override / Effektiv) of a row. */
function provenanceCell(row: ReturnType<Page['locator']>, column: string) {
  return row
    .locator('dl > div')
    .filter({ has: row.page().locator('dt', { hasText: new RegExp(`^${column}$`) }) })
    .locator('dd');
}

async function tooltip(page: Page, field: string) {
  const button = page.getByRole('button', { name: `Herkunft von ${field}`, exact: true });
  await button.hover();
  const tip = page.getByRole('tooltip');
  await expect(tip).toBeVisible();
  return tip;
}

function tooltipValue(tip: ReturnType<Page['locator']>, label: string) {
  return tip.locator(`dt:text-is("${label}") + dd`);
}

/**
 * setOverrideUI sets a manual value through the "Override setzen" dialog of the CI detail and
 * returns the response of the PUT.
 */
async function setOverrideUI(page: Page, field: string, value: string, reason: string) {
  const row = await provenanceRow(page, field);
  await row.getByRole('button', { name: 'Override setzen' }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog.getByRole('heading', { name: `Override setzen: ${field}` })).toBeVisible();
  await dialog.getByLabel('Wert').fill(value);
  await dialog.getByLabel('Begründung').fill(reason);
  const saved = page.waitForResponse(
    (r) => r.url().includes(`/fields/${field}/override`) && r.request().method() === 'PUT',
  );
  await dialog.getByRole('button', { name: 'Speichern' }).click();
  const response = await saved;
  expect(response.status(), 'PUT override').toBe(200);
  await expect(dialog).toBeHidden();
  return response;
}

/**
 * selectReview opens an override_conflict item in the review inbox list (kind filter
 * "Konflikt mit Override", status filter `status`). The list shows the items in the order of
 * GET /review-items with the same filter and names each by its field, so the item is the
 * n-th entry of its field; the diff header must then name its CI.
 */
async function selectReview(page: Page, item: ReviewItem, ciName: string, status: string) {
  const field = String(item.payload.field);
  const shown = (await reviewItems(status)).filter((i) => i.payload?.field === field);
  const index = shown.findIndex((i) => i.id === item.id);
  expect(index, `item ${item.id} in GET /review-items?status=${status}`).toBeGreaterThanOrEqual(0);
  const buttons = page.locator('main ul li button', { hasText: field });
  // The list is loaded and up to date with the API.
  await expect(buttons).toHaveCount(shown.length);
  await buttons.nth(index).click();
  await expect(page.locator('main table thead th').nth(2)).toHaveText(ciName);
  return page.locator('main table').locator('..');
}

async function openSession(browser: Browser, account: Account, ip: string) {
  const ctx = await browser.newContext({
    baseURL: BASE,
    locale: 'de-DE',
    extraHTTPHeaders: { 'X-Real-IP': ip },
  });
  const page = await ctx.newPage();
  const token = await uiLogin(page, account);
  return { ctx, page, token };
}

test.describe('Discovery → reconciliation → override protection → review', () => {
  test.beforeAll(async ({ browser, playwright }) => {
    test.setTimeout(5 * 60_000);
    expect(COLLECTOR_BIN, 'E2E_COLLECTOR_BIN (collector binary)').toBeTruthy();
    const rq = await playwright.request.newContext();
    const saved = loadState();
    if (saved) {
      // A restarted worker: re-use the tenant and the enrolled collector of this run.
      const { ctx, page, token } = await openSession(browser, saved.account, saved.ip);
      const me = await api<{ sub: string }>(rq, token, 'GET', '/me');
      w = { ...saved, rq, ctx, page, token, me: me.body.sub };
      if ((await getCollector()).status !== 'disabled') startCollector(w.workDir);
      return;
    }

    const ip = realIp();
    const ctx = await browser.newContext({
      baseURL: BASE,
      locale: 'de-DE',
      extraHTTPHeaders: { 'X-Real-IP': ip },
    });
    const page = await ctx.newPage();
    const account = await signupTenant(page, rq, 'discovr');
    const token = await uiLogin(page, account);
    const me = await api<{ sub: string }>(rq, token, 'GET', '/me');
    expect(me.status).toBe(200);
    const workDir = mkdtempSync(join(tmpdir(), 'reticora-e2e-discovery-'));
    const collectorName = `audit-collector-${stamp()}`;

    // The enrollment code is minted in the Discovery UI ("Collector verbinden").
    await page.goto('/discovery');
    await page.getByRole('button', { name: 'Collector verbinden' }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByLabel('Bezeichnung (z. B. Standort)').fill(collectorName);
    await dialog.getByRole('button', { name: 'Code erzeugen' }).click();
    const command = (await dialog.locator('pre').textContent()) ?? '';
    const code = /--code\s+(\S+)/.exec(command)?.[1] ?? '';
    expect(code, `enrollment command shown in the UI: ${command}`).not.toBe('');
    await dialog.getByRole('button', { name: 'Fertig' }).click();

    const enrolled = enroll(code, collectorName, credentialsPath(workDir));
    expect(enrolled.status, `collector enroll: ${enrolled.stderr}${enrolled.stdout}`).toBe(0);

    w = {
      account,
      workDir,
      collectorName,
      collectorId: '',
      enrollCode: code,
      ip,
      startedAt: 0,
      rq,
      ctx,
      page,
      token,
      me: me.body.sub,
    };
    const collectors = await ok<List<Collector>>('GET', '/collectors?limit=100');
    w.collectorId = (collectors.data ?? []).find((c) => c.name === collectorName)?.id ?? '';
    expect(w.collectorId, 'the enrolled collector is registered').not.toBe('');

    w.startedAt = startCollector(workDir);
    await expect
      .poll(async () => (await fleet()).size, {
        message: 'CIs of the simulated fleet',
        timeout: 180_000,
        intervals: [3_000],
      })
      .toBeGreaterThanOrEqual(SIMULATED_DEVICES);
    const state: SharedState = {
      account,
      workDir,
      collectorName,
      collectorId: w.collectorId,
      enrollCode: code,
      ip,
      startedAt: w.startedAt,
    };
    writeFileSync(STATE_FILE, JSON.stringify(state));
  });

  test.afterAll(async () => {
    await stopCollector();
    await w?.ctx.close();
    await w?.rq.dispose();
  });

  test('D01 a collector enrolled with a code from the Discovery UI reports the simulated fleet; the code is single-use', async () => {
    const { page } = w;
    // COL-01/COL-04: registered once, certificate bound to the collector, 90 days, active.
    const collectors = (await ok<List<Collector>>('GET', '/collectors?limit=100')).data ?? [];
    expect(collectors.map((c) => c.name)).toEqual([w.collectorName]);
    await expect
      .poll(async () => (await getCollector()).status, {
        message: 'collector status after its heartbeats (COL-01)',
        timeout: 60_000,
      })
      .toBe('active');
    const col = await getCollector();
    expect(col.cert_fingerprint, 'certificate fingerprint (COL-04)').toBeTruthy();
    const validDays = (Date.parse(col.cert_not_after ?? '') - Date.now()) / 86_400_000;
    expect(validDays, 'certificate validity 90 days (COL-04)').toBeGreaterThan(89);
    expect(validDays, 'certificate validity 90 days (COL-04)').toBeLessThanOrEqual(90.01);
    expect(Date.parse(col.last_heartbeat_at ?? '')).toBeGreaterThan(0);

    // COL-04 / WP-144: the code is consumed – a second enrollment with it is refused.
    const again = enroll(w.enrollCode, `${w.collectorName}-again`, join(w.workDir, 'again.json'));
    expect(again.status, 'second enrollment with the same code').not.toBe(0);
    const after = (await ok<List<Collector>>('GET', '/collectors?limit=100')).data ?? [];
    expect(
      after.map((c) => c.name),
      'no second collector registered',
    ).toEqual([w.collectorName]);

    // DIS-05 / REC-01: every simulated device is one CI with its discovered fields.
    const all = await fleet();
    const sims = [...all.keys()].filter((name) => name.startsWith('sim-'));
    expect(sims.length, 'one CI per simulated device').toBe(SIMULATED_DEVICES);
    const core = all.get('sim-core-01')!;
    expect(core).toMatchObject({
      model: SIM.switchModel,
      manufacturer: SIM.switchVendor,
      serial_number: 'SIM00001',
      management_ip: '198.51.100.1',
      discovery_source: SIM.switchSource,
    });
    const srv = all.get('sim-srv-01')!;
    expect(srv.discovery_source).toBe('redfish');
    expect(srv.model).toBe('PowerEdge R650');

    // UI: the collector card shows the status; the CI detail the discovered values.
    await page.goto('/discovery');
    await expect(collectorBadge(page, w.collectorName)).toHaveText('Aktiv');
    await openCI(page, core);
    await expect(detailValue(page, 'Modell')).toHaveText(SIM.switchModel);
    await expect(detailValue(page, 'Hersteller')).toHaveText(SIM.switchVendor);
    await expect(detailValue(page, 'Quelle')).toHaveText(SIM.switchSource);
  });

  test('D02 every typed field of a discovered CI has provenance with source, rank and time; the tooltip shows it', async () => {
    const { page } = w;
    const ci = await getCI((await ciByName('sim-core-02')).id);
    const rows = await provenance(ci.id);
    const byField = new Map(rows.map((p) => [p.field, p]));

    // REC-10 / WP-198: provenance for ALL typed fields that carry a value.
    const typed = [
      'name',
      'status',
      'manufacturer',
      'model',
      'serial_number',
      'hardware_uuid',
      'management_ip',
      'primary_mac',
      'hostname',
      'fqdn',
      'os_name',
      'os_version',
      'firmware_version',
      'sys_object_id',
    ];
    for (const field of typed) {
      const value = ci[field];
      if (value === undefined || value === null || value === '') continue;
      const p = byField.get(field);
      expect.soft(p, `provenance of typed field ${field}`).toBeTruthy();
      if (!p) continue;
      expect.soft(p.source, `${field}: source`).toBeTruthy();
      expect.soft(p.rank, `${field}: rank`).toBeGreaterThan(0);
      expect.soft(p.recorded_at ?? p.observed_at, `${field}: time`).toBeTruthy();
    }

    // The discovered fields name the source, its REC-03 rank and the observation time.
    for (const field of ['model', 'manufacturer', 'serial_number', 'management_ip']) {
      const p = byField.get(field);
      expect.soft(p, `provenance of ${field}`).toMatchObject({
        source: SIM.switchSource,
        rank: SIM.switchRank,
        reason: 'source_rank',
        diverged: false,
      });
      const observed = Date.parse(p?.observed_at ?? '');
      expect
        .soft(observed, `${field}: observed_at from this run`)
        .toBeGreaterThanOrEqual(w.startedAt - 5_000);
      expect
        .soft(observed, `${field}: observed_at not in the future`)
        .toBeLessThanOrEqual(Date.now() + 1_000);
      expect.soft(Date.parse(p?.recorded_at ?? ''), `${field}: recorded_at`).toBeGreaterThan(0);
    }
    expect(byField.get('model')?.value).toBe(SIM.switchModel);

    // UI: the tooltip of the field shows reason, source, rank and both times.
    await openCI(page, ci);
    const row = await provenanceRow(page, 'model');
    await expect(provenanceCell(row, 'Effektiv')).toHaveText(JSON.stringify(SIM.switchModel));
    await expect(row.getByText(`Quelle: ${SIM.switchSource}`)).toBeVisible();
    const tip = await tooltip(page, 'model');
    await expect(tip).toContainText('Höchstrangige Quelle');
    await expect(tooltipValue(tip, 'Quelle')).toHaveText(SIM.switchSource);
    await expect(tooltipValue(tip, 'Rang')).toHaveText(String(SIM.switchRank));
    await expect(tooltipValue(tip, 'Beobachtet')).toHaveText(/\d{1,2}\.\d{1,2}\.\d{4}/);
    await expect(tooltipValue(tip, 'Erfasst')).toHaveText(/\d{1,2}\.\d{1,2}\.\d{4}/);
  });

  test('D03 a manual value set in the "Override setzen" dialog is stored with author, time and reason and becomes the effective value', async () => {
    const { page } = w;
    const ci = await ciByName('sim-access-03');
    const value = `Audit-Model-${stamp()}`;
    const reason = 'Typenschild vor Ort geprüft';
    await openCI(page, ci);

    // Negative: the reason is required – without it nothing is stored.
    const row = await provenanceRow(page, 'model');
    await row.getByRole('button', { name: 'Override setzen' }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByLabel('Wert').fill(value);
    await dialog.getByRole('button', { name: 'Speichern' }).click();
    await expect(dialog, 'the dialog stays open without a reason').toBeVisible();
    expect((await provField(ci.id, 'model'))?.reason, 'nothing stored without reason').not.toBe(
      'override',
    );
    await dialog.getByRole('button', { name: 'Abbrechen' }).click();
    await expect(dialog).toBeHidden();

    const before = Date.now() - 1_000;
    await setOverrideUI(page, 'model', value, reason);

    // OVR-01: author, time and reason are stored; both values are kept.
    const p = await provField(ci.id, 'model');
    expect(p).toMatchObject({
      value,
      source: 'manual_override',
      rank: SIM.manualRank,
      reason: 'override',
      override_reason: reason,
      override_author: w.me,
      protected: true,
      discovered_value: SIM.switchModel,
      discovered_source: SIM.switchSource,
    });
    const recorded = Date.parse(p?.recorded_at ?? '');
    expect(recorded, 'override time').toBeGreaterThanOrEqual(before);
    expect(recorded, 'override time').toBeLessThanOrEqual(Date.now() + 1_000);

    // CI-10 / WP-060: the effective value in GET /cis/{id} and in the list.
    expect((await getCI(ci.id)).model).toBe(value);
    expect((await fleet()).get(ci.name)?.model).toBe(value);

    // UI after a reload: effective value, override column, protected badge, tooltip.
    await page.reload();
    await expect(detailValue(page, 'Modell')).toHaveText(value);
    const saved = await provenanceRow(page, 'model');
    await expect(provenanceCell(saved, 'Override')).toHaveText(JSON.stringify(value));
    await expect(provenanceCell(saved, 'Discovery')).toHaveText(JSON.stringify(SIM.switchModel));
    await expect(provenanceCell(saved, 'Effektiv')).toHaveText(JSON.stringify(value));
    await expect(saved.getByText('geschützt', { exact: true })).toBeVisible();
    await expect(saved.getByRole('button', { name: 'Override aufheben' })).toBeVisible();
    const tip = await tooltip(page, 'model');
    await expect(tip).toContainText('Manueller Override');
    await expect(tooltipValue(tip, 'Begründung')).toHaveText(reason);
    await expect(tooltipValue(tip, 'Autor')).not.toHaveText('—');
    await expect(tooltipValue(tip, 'Überdeckt')).toContainText(SIM.switchModel);
    await expect(tooltipValue(tip, 'Erfasst')).toHaveText(/\d{1,2}\.\d{1,2}\.\d{4}/);

    // Negative: structural columns cannot be overridden (422), nothing changes.
    const refused = await api(w.rq, w.token, 'PUT', `/cis/${ci.id}/fields/ci_type_id/override`, {
      value: ci.ci_type_id,
      reason: 'structural',
      protected: true,
    });
    expect(refused.status, 'override of a structural column').toBe(422);
    expect((await provField(ci.id, 'ci_type_id'))?.reason ?? '').not.toBe('override');

    // The override is audited with the acting user.
    const audit = await pollAudit(ci.id, 'ci.override_set');
    expect(audit.actor_id, 'audit actor of the override').toBe(w.me);
  });

  test('D04 a new discovery run does not overwrite manual values; the deviation is kept and surfaced once per field', async () => {
    const { page } = w;
    const core = await ciByName('sim-core-01');
    const access = await ciByName('sim-access-01');
    const model = `Audit-Core-${stamp()}`;
    const vendor = `Audit-Vendor-${stamp()}`;

    // Manual value 1: the override dialog.
    await openCI(page, core);
    await setOverrideUI(page, 'model', model, 'Austausch-Chassis laut Wartungsvertrag');

    // Manual value 2: the ordinary edit form ("Bearbeiten") – a manual write path (WP-057).
    await openCI(page, access);
    // The first "Bearbeiten" is the page action; the others belong to relationships.
    await page.getByRole('button', { name: 'Bearbeiten', exact: true }).first().click();
    const form = page.getByRole('dialog');
    await expect(form.getByRole('heading', { name: 'CI bearbeiten' })).toBeVisible();
    await form.getByLabel('Hersteller').fill(vendor);
    const patched = page.waitForResponse(
      (r) => r.url().includes(`/cis/${access.id}`) && r.request().method() === 'PATCH',
    );
    await form.getByRole('button', { name: 'Speichern' }).click();
    expect((await patched).status(), 'PATCH from the edit form').toBe(200);
    await expect(form).toBeHidden();
    expect((await getCI(access.id)).manufacturer).toBe(vendor);
    // WP-057: the manual change created a protected override in the same transaction.
    expect.soft(await provField(access.id, 'manufacturer'), 'override of the edit').toMatchObject({
      reason: 'override',
      value: vendor,
      override_author: w.me,
    });

    // Discovery runs again.
    const since = await restartCollector();
    await waitSeen([core.id, access.id], since, 'both CIs seen by the new discovery run');
    // The run's records are processed field by field after last_seen_at; give the
    // provenance of the run time to land before reading it.
    await softExpect
      .poll(async () => Date.parse((await provField(core.id, 'model'))?.observed_at ?? ''), {
        message: 'observed value of model updated by the new run (REC-12: _observed)',
        timeout: 60_000,
      })
      .toBeGreaterThan(since);

    // OVR-01 / REC-03: the manual values are not overwritten – detail and list.
    expect.soft((await getCI(core.id)).model, 'override survives discovery').toBe(model);
    expect.soft((await getCI(access.id)).manufacturer, 'edit survives discovery').toBe(vendor);
    const listed = await fleet();
    expect.soft(listed.get(core.name)?.model, 'list: effective value').toBe(model);
    expect.soft(listed.get(access.name)?.manufacturer, 'list: effective value').toBe(vendor);

    // REC-12 / OVR-01: both values kept; the observation is surfaced as diverged.
    expect.soft(await provField(core.id, 'model')).toMatchObject({
      value: model,
      reason: 'override',
      discovered_value: SIM.switchModel,
      discovered_source: SIM.switchSource,
      diverged: true,
    });
    expect.soft(await provField(access.id, 'manufacturer')).toMatchObject({
      value: vendor,
      reason: 'override',
      discovered_value: SIM.switchVendor,
      diverged: true,
    });

    // UI: the CI detail shows the manual value and the deviating discovery value.
    await openCI(page, core);
    await expect.soft(detailValue(page, 'Modell')).toHaveText(model);
    const row = await provenanceRow(page, 'model');
    await expect.soft(row.getByText('abweichend', { exact: true })).toBeVisible();
    await expect.soft(provenanceCell(row, 'Discovery')).toHaveText(JSON.stringify(SIM.switchModel));
    await expect.soft(provenanceCell(row, 'Effektiv')).toHaveText(JSON.stringify(model));

    // REC-12: exactly one open override_conflict per field with the reported value.
    await softExpect
      .poll(async () => (await conflicts(core.id, 'model', 'open')).length, {
        message: 'override_conflict of sim-core-01/model',
        timeout: 30_000,
      })
      .toBeGreaterThan(0);
    const coreItems = await conflicts(core.id, 'model');
    expect.soft(coreItems.length, 'one item for the field').toBe(1);
    expect.soft(coreItems[0]?.payload.observed_value).toBe(SIM.switchModel);
    expect.soft(coreItems[0]?.status).toBe('open');
    const accessItems = await conflicts(access.id, 'manufacturer');
    expect.soft(accessItems.length, 'one item for the edited field').toBe(1);
    expect.soft(accessItems[0]?.payload.observed_value).toBe(SIM.switchVendor);
    expect(coreItems.length, 'an item to show in the review inbox').toBeGreaterThan(0);

    // UI: the review inbox lists the conflict with the diff of observation and CI.
    await page.goto('/reviews');
    await page.getByRole('combobox', { name: 'Typ' }).selectOption('override_conflict');
    const detail = await selectReview(page, coreItems[0], core.name, 'open');
    const diffRow = detail.locator('tbody tr').filter({ hasText: 'model' });
    await expect(diffRow.locator('td').nth(1)).toHaveText(SIM.switchModel);
    await expect(diffRow.locator('td').nth(2)).toContainText(model);
    await expect(diffRow.getByText('abweichend')).toBeVisible();
    await expect(detail.getByRole('button', { name: 'Übernehmen' })).toBeVisible();
    await expect(detail.getByRole('button', { name: 'Verwerfen' })).toBeVisible();
  });

  test('D05 the review inbox accepts and dismisses override conflicts; the result persists and holds in the next discovery run', async () => {
    const { page } = w;
    const accepted = await ciByName('sim-core-02');
    const dismissed = await ciByName('sim-access-02');
    const stays = await ciByName('sim-access-04');
    const acceptValue = `Audit-Accept-${stamp()}`;
    const dismissValue = `Audit-Dismiss-${stamp()}`;
    const openValue = `Audit-Open-${stamp()}`;
    // Preconditions through the override endpoint the dialog uses (UI path: D03/D04).
    await setOverrideApi(accepted.id, 'model', acceptValue, 'audit accept');
    await setOverrideApi(dismissed.id, 'model', dismissValue, 'audit dismiss');
    await setOverrideApi(stays.id, 'manufacturer', openValue, 'audit open');

    const since = await restartCollector();
    await waitSeen([accepted.id, dismissed.id, stays.id], since, 'CIs seen by the run');
    await expect
      .poll(
        async () =>
          (await conflicts(accepted.id, 'model', 'open')).length +
          (await conflicts(dismissed.id, 'model', 'open')).length +
          (await conflicts(stays.id, 'manufacturer', 'open')).length,
        { message: 'three override_conflict items (precondition)', timeout: 60_000 },
      )
      .toBe(3);
    const [acceptItem] = await conflicts(accepted.id, 'model', 'open');
    const [dismissItem] = await conflicts(dismissed.id, 'model', 'open');

    // Accept and dismiss in the review inbox UI.
    await page.goto('/reviews');
    await page.getByRole('combobox', { name: 'Typ' }).selectOption('override_conflict');
    let detail = await selectReview(page, acceptItem, accepted.name, 'open');
    const resolvedAccept = page.waitForResponse(
      (r) => r.url().includes(`/review-items/${acceptItem.id}/resolve`) && r.ok(),
    );
    await detail.getByRole('button', { name: 'Übernehmen' }).click();
    await resolvedAccept;
    detail = await selectReview(page, dismissItem, dismissed.name, 'open');
    const resolvedDismiss = page.waitForResponse(
      (r) => r.url().includes(`/review-items/${dismissItem.id}/resolve`) && r.ok(),
    );
    await detail.getByRole('button', { name: 'Verwerfen' }).click();
    await resolvedDismiss;

    // Persisted: after a reload the items are listed under their new status.
    await page.reload();
    await page.getByRole('combobox', { name: 'Typ' }).selectOption('override_conflict');
    await page.getByRole('combobox', { name: 'Status' }).selectOption('resolved');
    await selectReview(page, acceptItem, accepted.name, 'resolved');
    await page.getByRole('combobox', { name: 'Status' }).selectOption('dismissed');
    await selectReview(page, dismissItem, dismissed.name, 'dismissed');
    const a = await ok<ReviewItem>('GET', `/review-items/${acceptItem.id}`);
    expect(a).toMatchObject({ status: 'resolved', action: 'accept' });
    expect(a.resolved_at, 'resolution time').toBeTruthy();
    expect(a.resolved_by, 'resolved by the signed-in user').toBe(w.me);
    const d = await ok<ReviewItem>('GET', `/review-items/${dismissItem.id}`);
    expect(d).toMatchObject({ status: 'dismissed', action: 'dismiss' });

    // REC-12: accept removes the override and takes the observed value.
    expect.soft((await getCI(accepted.id)).model, 'accepted: observed value').toBe(SIM.switchModel);
    expect.soft((await provField(accepted.id, 'model'))?.reason).not.toBe('override');
    // REC-12: dismiss keeps the override.
    expect.soft((await getCI(dismissed.id)).model, 'dismissed: override kept').toBe(dismissValue);
    expect.soft((await provField(dismissed.id, 'model'))?.reason).toBe('override');
    await openCI(page, accepted);
    await expect.soft(detailValue(page, 'Modell')).toHaveText(SIM.switchModel);
    await openCI(page, dismissed);
    await expect.soft(detailValue(page, 'Modell')).toHaveText(dismissValue);

    // Negative: an item is resolved once; a disallowed action is refused.
    const twice = await api(w.rq, w.token, 'POST', `/review-items/${acceptItem.id}/resolve`, {
      action: 'dismiss',
    });
    expect.soft(twice.status, 'resolving a resolved item again').toBeGreaterThanOrEqual(400);
    expect.soft(twice.status).toBeLessThan(500);
    expect
      .soft((await ok<ReviewItem>('GET', `/review-items/${acceptItem.id}`)).status)
      .toBe('resolved');
    const [openItem] = await conflicts(stays.id, 'manufacturer', 'open');
    const merge = await api(w.rq, w.token, 'POST', `/review-items/${openItem.id}/resolve`, {
      action: 'merge',
      ci_id: stays.id,
    });
    expect
      .soft(merge.status, 'merge is no action of override_conflict')
      .toBeGreaterThanOrEqual(400);
    expect.soft(merge.status).toBeLessThan(500);
    expect.soft((await ok<ReviewItem>('GET', `/review-items/${openItem.id}`)).status).toBe('open');

    // The next discovery run reports the same values again.
    const again = await restartCollector();
    await waitSeen([accepted.id, dismissed.id, stays.id], again, 'CIs seen by the next run');
    await expect
      .poll(async () => Date.parse((await provField(dismissed.id, 'model'))?.observed_at ?? ''), {
        message: 'observation of the dismissed field recorded by the next run',
        timeout: 60_000,
      })
      .toBeGreaterThan(again);
    // Accepted: the observed value stays, no new conflict (no override any more).
    expect.soft((await getCI(accepted.id)).model).toBe(SIM.switchModel);
    expect.soft((await conflicts(accepted.id, 'model', 'open')).length, 'accepted').toBe(0);
    // Dismissed: the override stays and the same value is suppressed (no new item).
    expect.soft((await getCI(dismissed.id)).model).toBe(dismissValue);
    expect.soft((await conflicts(dismissed.id, 'model', 'open')).length, 'suppressed').toBe(0);
    // Open: still exactly one item for the field (deduplicated per field).
    const still = await conflicts(stays.id, 'manufacturer');
    expect.soft(still.length, 'deduplicated per field').toBe(1);
    expect.soft(still[0]?.status).toBe('open');
    expect.soft((await getCI(stays.id)).manufacturer).toBe(openValue);
  });

  test('D06 several override conflicts are accepted at once in the review inbox (bulk)', async () => {
    const { page } = w;
    const a = await ciByName('sim-pdu-a');
    const b = await ciByName('sim-pdu-b');
    await setOverrideApi(a.id, 'model', `Audit-PDU-A-${stamp()}`, 'audit bulk');
    await setOverrideApi(b.id, 'model', `Audit-PDU-B-${stamp()}`, 'audit bulk');
    const since = await restartCollector();
    await waitSeen([a.id, b.id], since, 'both PDUs seen by the run');
    await expect
      .poll(
        async () =>
          (await conflicts(a.id, 'model', 'open')).length +
          (await conflicts(b.id, 'model', 'open')).length,
        { message: 'two override_conflict items (precondition)', timeout: 60_000 },
      )
      .toBe(2);
    const [itemA] = await conflicts(a.id, 'model', 'open');
    const [itemB] = await conflicts(b.id, 'model', 'open');

    // Select both items in the list and accept them with one action.
    await page.goto('/reviews');
    await page.getByRole('combobox', { name: 'Typ' }).selectOption('override_conflict');
    const shown = (await reviewItems('open')).filter((i) => i.payload?.field === 'model');
    const boxes = page.getByRole('checkbox', { name: '„model“ auswählen' });
    await expect(boxes).toHaveCount(shown.length);
    await boxes.nth(shown.findIndex((i) => i.id === itemA.id)).check();
    await boxes.nth(shown.findIndex((i) => i.id === itemB.id)).check();
    const bulk = page.getByRole('group', { name: 'Sammelaktion' });
    await expect(bulk.getByText('2 ausgewählt')).toBeVisible();
    await bulk.getByRole('combobox', { name: 'Aktion' }).selectOption('accept');
    await bulk.getByRole('button', { name: 'Ausführen' }).click();
    await expect(
      page.getByRole('status').filter({ hasText: 'erledigt' }),
      'bulk result in the UI',
    ).toHaveText('2 erledigt, 0 fehlgeschlagen.');

    // Persisted: both resolved by accept, both CIs back on the observed value.
    for (const [item, ci] of [
      [itemA, a],
      [itemB, b],
    ] as const) {
      const stored = await ok<ReviewItem>('GET', `/review-items/${item.id}`);
      expect.soft(stored, `${ci.name}: item`).toMatchObject({
        status: 'resolved',
        action: 'accept',
        resolved_by: w.me,
      });
      expect.soft((await getCI(ci.id)).model, `${ci.name}: observed value`).toBe(SIM.pduModel);
      expect
        .soft((await provField(ci.id, 'model'))?.reason, `${ci.name}: no override`)
        .not.toBe('override');
    }
    await page.reload();
    await page.getByRole('combobox', { name: 'Typ' }).selectOption('override_conflict');
    await page.getByRole('combobox', { name: 'Status' }).selectOption('resolved');
    await selectReview(page, itemA, a.name, 'resolved');
    await selectReview(page, itemB, b.name, 'resolved');
  });

  test('D07 "Override aufheben" in the CI detail returns the field to the observed value', async () => {
    const { page } = w;
    const ci = await ciByName('sim-fw-01');
    const value = `Audit-FW-${stamp()}`;
    await setOverrideApi(ci.id, 'model', value, 'audit clear');
    expect((await getCI(ci.id)).model).toBe(value);

    await openCI(page, ci);
    await expect(detailValue(page, 'Modell')).toHaveText(value);
    const row = await provenanceRow(page, 'model');
    const cleared = page.waitForResponse(
      (r) =>
        r.url().includes(`/cis/${ci.id}/fields/model/override`) &&
        r.request().method() === 'DELETE',
    );
    await row.getByRole('button', { name: 'Override aufheben' }).click();
    expect((await cleared).status(), 'DELETE override').toBe(200);
    // The page shows the effective value at once, not the cleared override (CI-10, F-100):
    // overview and provenance agree without a reload.
    await expect(
      detailValue(page, 'Modell'),
      'overview after "Override aufheben" without reload',
    ).toHaveText(SIM.firewallModel);
    await expect(row.getByRole('button', { name: 'Override setzen' })).toBeVisible();
    await expect(provenanceCell(row, 'Effektiv')).toHaveText(JSON.stringify(SIM.firewallModel));

    // WP-060: the effective value is the current observed value again.
    expect((await getCI(ci.id)).model).toBe(SIM.firewallModel);
    expect((await fleet()).get(ci.name)?.model).toBe(SIM.firewallModel);
    const p = await provField(ci.id, 'model');
    expect(p?.reason).not.toBe('override');
    expect(p?.value).toBe(SIM.firewallModel);
    await page.reload();
    await expect(detailValue(page, 'Modell')).toHaveText(SIM.firewallModel);
    const after = await provenanceRow(page, 'model');
    await expect(after.getByRole('button', { name: 'Override setzen' })).toBeVisible();
    await expect(provenanceCell(after, 'Effektiv')).toHaveText(JSON.stringify(SIM.firewallModel));
    // WP-060: the clear is audited with the acting user.
    const audit = await pollAudit(ci.id, 'ci.override_cleared');
    expect(audit.actor_id, 'audit actor of the clear').toBe(w.me);
  });

  test('D08 a disabled collector can no longer deliver: cut off within 10 s without a restart, refused after a restart, nothing new is persisted', async () => {
    // Two further discovery cycles of the running collector (RETICORA_DISCOVERY_INTERVAL,
    // at least 5 min, checked every minute) after the disable.
    const cycle = 5 * 60_000;
    test.setTimeout(4 * cycle);
    const { page } = w;
    await renewToken();
    // Positive control: with the collector enabled a restart delivers, last_seen_at rises
    // and the heartbeats go over its NATS channel.
    const seenBeforeControl = await maxLastSeen();
    const controlOffset = collectorLogSize();
    const control = await restartCollector({ RETICORA_DISCOVERY_INTERVAL: '5m' });
    const ids = [...(await fleet()).values()].map((ci) => ci.id);
    await waitSeen(ids, control, 'the enabled collector delivers the whole fleet');
    expect(
      await maxLastSeen(),
      'last_seen_at rises while the collector is enabled',
    ).toBeGreaterThan(seenBeforeControl);
    await expect
      .poll(async () => Date.parse((await getCollector()).last_heartbeat_at ?? ''), {
        message: 'heartbeat of the enabled collector',
        timeout: 60_000,
      })
      .toBeGreaterThan(control);
    await expect
      .poll(() => collectorLog(controlOffset), {
        message: 'heartbeat of the enabled collector over the NATS edge',
        timeout: 60_000,
        intervals: [2_000],
      })
      .toMatch(/"msg":"heartbeat completed","status":20\d,"transport":"edge"/);

    // An administrator disables the running collector (PATCH /collectors/{id},
    // collector:manage).
    const disabled = await ok<Collector>('PATCH', `/collectors/${w.collectorId}`, {
      status: 'disabled',
    });
    const disabledAt = Date.now();
    expect(disabled.status).toBe('disabled');
    await page.goto('/discovery');
    await expect(collectorBadge(page, w.collectorName)).toHaveText('Deaktiviert');

    // Without a restart (WP-381, F-025): 10 s after the disable the server has cut the
    // collector's NATS connection; over two further discovery cycles of the same process
    // nothing is answered over the edge and nothing is persisted.
    await new Promise((resolve) =>
      setTimeout(resolve, Math.max(0, disabledAt + 10_000 - Date.now())),
    );
    const cutOffset = collectorLogSize();
    const seenAtCut = await maxLastSeen();
    const countAtCut = (await ok<List<CI>>('GET', '/cis?limit=1')).total;
    const heartbeatAtCut = (await getCollector()).last_heartbeat_at;
    await expect
      .poll(() => (collectorLog(cutOffset).match(/"msg":"discovery started"/g) ?? []).length, {
        message: 'two discovery cycles of the running, disabled collector',
        timeout: 3 * cycle,
        intervals: [5_000],
      })
      .toBeGreaterThanOrEqual(2);
    // The spool flush (every 5 s) tried to deliver the second cycle's results.
    await new Promise((resolve) => setTimeout(resolve, 20_000));
    expect(collector?.exitCode ?? null, 'the collector process kept running').toBeNull();
    const running = collectorLog(cutOffset);
    softExpect(running, 'no request over the NATS edge answered after the cut').not.toContain(
      '"transport":"edge"',
    );
    softExpect(running, 'no spooled result delivered').not.toContain(
      'spooled discovery results delivered',
    );
    softExpect(running, 'no heartbeat accepted').not.toMatch(
      /"msg":"heartbeat completed","status":(20\d|"ok"|"active")/,
    );
    await renewToken();
    softExpect(await maxLastSeen(), 'last_seen_at unchanged over two discovery cycles').toBe(
      seenAtCut,
    );
    softExpect(
      (await ok<List<CI>>('GET', '/cis?limit=1')).total,
      'CI count unchanged over two discovery cycles',
    ).toBe(countAtCut);
    softExpect((await getCollector()).last_heartbeat_at, 'no heartbeat recorded').toBe(
      heartbeatAtCut,
    );

    // After a restart: baseline after the running process got the chance to finish
    // in-flight work.
    await stopCollector();
    const seenBefore = await maxLastSeen();
    const heartbeatBefore = (await getCollector()).last_heartbeat_at;
    const countBefore = (await ok<List<CI>>('GET', '/cis?limit=1')).total;
    const reviewsBefore = (await ok<List<ReviewItem>>('GET', '/review-items?limit=1')).total;

    // The disabled collector starts and tries to deliver.
    const offset = collectorLogSize();
    const restarted = startCollector(w.workDir);
    await expect
      .poll(
        () => {
          const out = collectorLog(offset);
          return (
            out.includes('spooled discovery results delivered') ||
            (out.match(/spool flush postponed/g) ?? []).length >= 3
          );
        },
        {
          message: 'the collector tried to deliver its spooled results repeatedly',
          timeout: 90_000,
          intervals: [2_000],
        },
      )
      .toBe(true);
    const log = collectorLog(offset);

    // COL-04: refused – no delivery, no heartbeat, nothing persisted.
    expect
      .soft(log, 'no spooled result delivered')
      .not.toContain('spooled discovery results delivered');
    expect
      .soft(log, 'no heartbeat accepted')
      .not.toMatch(/"msg":"heartbeat completed","status":(200|"ok"|"active")/);
    // NATS account locked (COL-04): no request over the edge is answered. (The collector
    // logs "connected over mTLS" also when the endpoint refuses it – RetryOnFailedConnect.)
    expect.soft(log, 'no heartbeat over the NATS edge').not.toContain('"transport":"edge"');
    expect.soft(log, 'no credential bundle over NATS').not.toContain('credential bundle updated');
    expect.soft(log, 'no subnets reported over NATS').not.toContain('local subnets reported');
    expect.soft(await maxLastSeen(), 'no CI seen by the disabled collector').toBe(seenBefore);
    expect.soft(seenBefore).toBeLessThan(restarted);
    const col = await getCollector();
    expect.soft(col.status, 'stays disabled').toBe('disabled');
    expect.soft(col.last_heartbeat_at, 'no heartbeat recorded').toBe(heartbeatBefore);
    expect.soft((await ok<List<CI>>('GET', '/cis?limit=1')).total).toBe(countBefore);
    expect
      .soft((await ok<List<ReviewItem>>('GET', '/review-items?limit=1')).total)
      .toBe(reviewsBefore);
    await stopCollector();
    // The run is over: the next run enrolls its own collector.
    rmSync(STATE_FILE, { force: true });
  });

  test('D09 an override set in a second session while the edit form is open: saving the form with a changed model is refused with 409, the override stays', async ({
    browser,
  }) => {
    const { page } = w;
    await renewToken();
    const ci = await getCI((await ciByName('sim-fw-02')).id);
    const manual = `Audit-FW2-${stamp()}`;
    const stale = `Audit-Stale-${stamp()}`;
    const reason = 'Austauschgerät vor Ort geprüft';
    expect(ci.model, 'the discovered model (precondition)').toBe(SIM.firewallModel);

    // Session 1 opens the edit form on the version it read.
    await openCI(page, ci);
    // The first "Bearbeiten" is the page action; the others belong to relationships.
    await page.getByRole('button', { name: 'Bearbeiten', exact: true }).first().click();
    const form = page.getByRole('dialog');
    await expect(form.getByRole('heading', { name: 'CI bearbeiten' })).toBeVisible();
    await expect(form.getByLabel('Modell')).toHaveValue(SIM.firewallModel);

    // Session 2 (the same user, signed in a second time) sets an override on model.
    const s2 = await openSession(browser, w.account, realIp());
    try {
      await openCI(s2.page, ci);
      const put = await setOverrideUI(s2.page, 'model', manual, reason);
      // API-07 (WP-412): the override is a manual write, the CI version rises.
      expect(put.headers()['etag'], 'ETag of the override').toBe(`"${ci.version + 1}"`);
      // CI-10: the detail of session 2 shows the manual value without a reload.
      await expect(detailValue(s2.page, 'Modell')).toHaveText(manual);
    } finally {
      await s2.ctx.close();
    }

    // Session 1 saves its open form with a changed model: refused, nothing is overwritten.
    await form.getByLabel('Modell').fill(stale);
    const patched = page.waitForResponse(
      (r) => r.url().includes(`/cis/${ci.id}`) && r.request().method() === 'PATCH',
    );
    await form.getByRole('button', { name: 'Speichern' }).click();
    const res = await patched;
    expect(res.request().headers()['if-match'], 'the form sends the version it read').toBe(
      `"${ci.version}"`,
    );
    expect(
      res.status(),
      `save of the stale form answered ${res.status()}: ${await res.text()}`,
    ).toBe(409);
    expect(await res.json(), '409 names the field and the current version').toMatchObject({
      fields: ['model'],
      current_version: ci.version + 1,
    });
    // The form shows the conflict and stays open with the input.
    await expect(form, 'the form stays open with the refusal').toBeVisible();
    await expect(form.getByRole('alert'), 'conflict hint in the form').toContainText('model');
    await expect(form.getByLabel('Modell')).toHaveValue(stale);

    // OVR-01: the override stays – effective value, provenance, version, list.
    const stored = await getCI(ci.id);
    expect(stored.model, 'the override is not overwritten').toBe(manual);
    expect(stored.version, 'the refused save wrote nothing').toBe(ci.version + 1);
    expect(await provField(ci.id, 'model')).toMatchObject({
      value: manual,
      reason: 'override',
      override_reason: reason,
      override_author: w.me,
      protected: true,
      discovered_value: SIM.firewallModel,
    });
    expect((await fleet()).get(ci.name)?.model, 'list: effective value').toBe(manual);

    // Session 1 closes the form; after a reload it shows the override.
    await form.getByRole('button', { name: 'Abbrechen' }).click();
    await expect(form).toBeHidden();
    await page.reload();
    await expect(detailValue(page, 'Modell')).toHaveText(manual);
    const row = await provenanceRow(page, 'model');
    await expect(provenanceCell(row, 'Override')).toHaveText(JSON.stringify(manual));
  });
});
