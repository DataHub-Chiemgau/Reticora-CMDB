import { spawn, spawnSync, type ChildProcess } from 'node:child_process';
import { appendFileSync, existsSync, mkdtempSync, readFileSync, statSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import {
  test,
  expect,
  type APIRequestContext,
  type BrowserContext,
  type Page,
} from '@playwright/test';
import {
  api,
  sessionToken,
  signupTenant,
  stamp,
  startFakeSnmp,
  uiLogin,
  BASE,
  COLLECTOR_BIN,
  SERVER,
  type Account,
  type FakeSnmp,
} from './support/stack';

/*
 * Phase-1 acceptance – onboarding with a real collector, end to end on the real stack
 * (WP-384, nothing mocked): UI → REST API → PostgreSQL → mTLS NATS edge → collector →
 * device → ingest → UI. The collector binary (E2E_COLLECTOR_BIN) runs as installed, not
 * simulated (RETICORA_SIMULATE unset, RETICORA_ENVIRONMENT=production, which refuses the
 * simulation): it reports the subnets of the runner, fetches its scopes and its encrypted
 * credential bundle over the edge and discovers a host scope with its SNMP plugin. The
 * device is the SNMPv2c fake of collector/internal/fakesnmp (support/stack.ts,
 * startFakeSnmp) on 127.0.0.161:161: it answers only the community of one credential and
 * records every community it receives.
 *
 * One organization is created through the self-signup UI, with the clients A and B and three
 * snmp_v2c credentials: one organization-wide, one of client A, one of client B. Its
 * collector is enrolled with a code minted in the Discovery UI. "The next bundle" is the one
 * the collector fetches when it connects again (restart; otherwise every 15 min). Before the
 * server's scope list arrives the collector runs its fallback pass once; it is limited to a
 * sweep of TEST-NET-1 (192.0.2.1, RFC 5737, not routed), which carries no secret.
 *
 * Test → requirement IDs (audit WPs, findings, decisions) proven:
 *  O1 enrollment with the code from the Discovery UI; the subnet the collector reports
 *     appears as suggestion in the Discovery UI and is confirmed there: GET
 *     /discovery-scopes?collector_id=… lists the scope (without credential_ids), the review
 *     scope_suggested is resolved/accept by the signed-in user ... INS-06, DIS-09 (WP-156, WP-153)
 *  O2 before the confirmation the bundle is empty although credentials exist (no enabled
 *     scope); the bundle the collector fetches next holds exactly the organization-wide
 *     credential (audit credential.bundle_delivered with its ID) and the collector opens it
 *     ............................. COL-02, DIS-09, INS-06 (WP-147, WP-153, WP-156), retest F-027, E-67
 *  O3 a scope with the credential of another client is refused with 422 (create and change),
 *     nothing is stored; the own client's credential is accepted ..... COL-02, DIS-09 (WP-376, F-264)
 *  O4 host scope with an explicit snmp_v2c credential (client A) on the SNMP fake: the next
 *     bundle holds exactly that credential, after the run the CI exists with source snmp (API
 *     and CI detail), named with the fake's sysName and without the community in its JSON,
 *     the fake saw the community of that credential and no other secret (not the
 *     organization-wide one, not client B's, not the plugin default "public")
 *     ........................................ COL-02, DIS-09 (WP-147, WP-153), E-67
 *
 * The suggested scope covers a real subnet of the runner. So that the collector does not
 * scan it, O2 stops the collector right after the bundle arrived (the first scope pass runs
 * a minute after the start) and O4 disables the scope before the collector starts again.
 *
 * A failing test is a product finding: the assertions encode the requirement texts in
 * docs/spec/katalog-v3 (COL-*, DIS-*, INS-*) and the WP acceptance criteria, not the
 * observed behaviour.
 */

// Requirement checks after a precondition: a violation is reported, the test goes on.
const softExpect = expect.configure({ soft: true });
/** Address of the SNMP fake: loopback, so no subnet the collector reports covers it. */
const FAKE_SNMP_IP = '127.0.0.161';

interface List<T> {
  data: T[] | null;
  total: number;
}

interface Collector {
  id: string;
  name: string;
  status: string;
}

interface Scope {
  id: string;
  collector_id: string;
  client_id?: string;
  name: string;
  kind: string;
  target: string;
  credential_ids: string[];
  plugins: string[];
  enabled: boolean;
  last_run_at?: string;
  last_result?: { records?: number };
}

interface ReviewItem {
  id: string;
  kind: string;
  status: string;
  payload: Record<string, unknown>;
  action?: string;
  resolved_by?: string;
  resolution?: string;
}

interface AuditEntry {
  seq: number;
  timestamp: string;
  action: string;
  resource_id?: string;
  changes?: { credential_ids?: string[] };
}

interface CI {
  id: string;
  name: string;
  management_ip?: string;
  discovery_source?: string;
}

/** An snmp_v2c credential of the run and its community. */
interface Credential {
  id: string;
  community: string;
}

interface World {
  rq: APIRequestContext;
  ctx: BrowserContext;
  page: Page;
  token: string;
  me: string;
  account: Account;
  workDir: string;
  collectorName: string;
  collectorId: string;
  clientA: string;
  clientB: string;
  /** Organization-wide (client_id NULL). */
  orgCred: Credential;
  /** Of client A, bound to the host scope of O4. */
  ownCred: Credential;
  /** Of client B. */
  foreignCred: Credential;
}

let w: World;
let collector: ChildProcess | undefined;
let fake: FakeSnmp | undefined;
/** The scope of the suggestion confirmed in O1 and the time just before the confirmation. */
let accepted: { item: ReviewItem; scope: Scope; at: number } | undefined;

/** A documentation address (198.18.0.0/15) so the signup has its own budget (E-33). */
function realIp(): string {
  return `198.18.${Math.floor(Math.random() * 250) + 1}.${Math.floor(Math.random() * 250) + 1}`;
}

function credentialsPath() {
  return join(w.workDir, 'credentials.json');
}

function logPath() {
  return join(w.workDir, 'collector.log');
}

/** collectorLog returns the collector output written after byte offset from. */
function collectorLog(from = 0): string {
  return existsSync(logPath()) ? readFileSync(logPath(), 'utf8').slice(from) : '';
}

function collectorLogSize(): number {
  return existsSync(logPath()) ? statSync(logPath()).size : 0;
}

/**
 * startCollector runs the enrolled collector as installed. The environment of the runner
 * is not passed on as configuration (no RETICORA_* variable, no proxy: the collector talks
 * to localhost only); the fallback pass before the scope list arrives is a sweep of
 * TEST-NET-1 without any credential, never SNMP with the plugin default "public".
 */
function startCollector(): number {
  const env: NodeJS.ProcessEnv = {};
  for (const [key, value] of Object.entries(process.env)) {
    if (key.startsWith('RETICORA_') || /^(https?|all|no)_proxy$/i.test(key)) continue;
    env[key] = value;
  }
  const offset = collectorLogSize();
  collector = spawn(COLLECTOR_BIN, [], {
    env: {
      ...env,
      RETICORA_ENVIRONMENT: 'production',
      RETICORA_CREDENTIALS_PATH: credentialsPath(),
      RETICORA_SPOOL_DIR: join(w.workDir, 'spool'),
      RETICORA_HEARTBEAT_INTERVAL: '10s',
      RETICORA_SPOOL_FLUSH_INTERVAL: '5s',
      RETICORA_SCAN_SUBNETS: '192.0.2.1/32',
      RETICORA_DISCOVERY_PROTOCOLS: 'sweep',
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  appendFileSync(logPath(), `\n### collector started ${new Date().toISOString()}\n`);
  collector.stdout?.on('data', (chunk: Buffer) => appendFileSync(logPath(), chunk));
  collector.stderr?.on('data', (chunk: Buffer) => appendFileSync(logPath(), chunk));
  return offset;
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

/** restartCollector makes the collector connect again; it returns the log offset of the run. */
async function restartCollector(): Promise<number> {
  await stopCollector();
  return startCollector();
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

/** renewToken takes the access token the signed-in page holds now (AUT-02, 15 min). */
async function renewToken() {
  await w.page.goto('/discovery');
  await expect(collectorCard(w.page)).toBeVisible();
  w.token = await sessionToken(w.page);
}

async function scopes(): Promise<Scope[]> {
  const q = new URLSearchParams({ collector_id: w.collectorId, limit: '100' });
  return (await ok<List<Scope>>('GET', `/discovery-scopes?${q.toString()}`)).data ?? [];
}

/** suggestions are the open scope_suggested items of the run's collector. */
async function suggestions(): Promise<ReviewItem[]> {
  const q = new URLSearchParams({ kind: 'scope_suggested', status: 'open', limit: '100' });
  return ((await ok<List<ReviewItem>>('GET', `/review-items?${q.toString()}`)).data ?? []).filter(
    (item) => item.payload?.collector_id === w.collectorId,
  );
}

/** bundleDeliveries are the audit entries credential.bundle_delivered of the collector. */
async function bundleDeliveries(): Promise<AuditEntry[]> {
  const q = new URLSearchParams({
    action: 'credential.bundle_delivered',
    resource_type: 'collector',
    resource_id: w.collectorId,
    limit: '200',
  });
  return (await ok<List<AuditEntry>>('GET', `/audit-log?${q.toString()}`)).data ?? [];
}

async function lastBundleSeq(): Promise<number> {
  return Math.max(0, ...(await bundleDeliveries()).map((e) => e.seq));
}

/** nextBundle waits for the first bundle delivered after the audit entry afterSeq. */
async function nextBundle(
  afterSeq: number,
  message: string,
  timeout = 60_000,
): Promise<AuditEntry> {
  let next: AuditEntry | undefined;
  await expect
    .poll(
      async () => {
        const later = (await bundleDeliveries()).filter((e) => e.seq > afterSeq);
        next = later.sort((a, b) => a.seq - b.seq)[0];
        return Boolean(next);
      },
      { message, timeout, intervals: [1_000] },
    )
    .toBe(true);
  return next!;
}

async function cis(): Promise<CI[]> {
  return (await ok<List<CI>>('GET', '/cis?limit=100')).data ?? [];
}

/** collectorCard is the card of the run's collector on the Discovery page. */
function collectorCard(page: Page) {
  return page
    .locator('section')
    .filter({ has: page.getByRole('heading', { level: 3, name: w.collectorName, exact: true }) });
}

/** detailValue is the value of a row of the overview card of the CI detail ("Quelle", …). */
function detailValue(page: Page, label: string) {
  return page
    .locator('dl > div')
    .filter({ has: page.locator('dt', { hasText: new RegExp(`^${label}$`) }) })
    .locator('dd')
    .first();
}

async function createCredential(name: string, clientId?: string): Promise<Credential> {
  const community = `audit-${name}-${stamp()}`;
  const created = await ok<{ id: string; kind: string; client_id?: string }>(
    'POST',
    '/credentials',
    {
      name: `audit ${name} ${stamp()}`,
      kind: 'snmp_v2c',
      ...(clientId ? { client_id: clientId } : {}),
      secret: { community },
    },
  );
  expect(created.kind).toBe('snmp_v2c');
  expect(created.client_id ?? '', `client of the credential ${name}`).toBe(clientId ?? '');
  return { id: created.id, community };
}

test.describe('Onboarding with a real collector: scope suggestion, credential bundle, scan with a bundle credential', () => {
  test.beforeAll(async ({ browser, playwright }) => {
    test.setTimeout(5 * 60_000);
    expect(COLLECTOR_BIN, 'E2E_COLLECTOR_BIN (collector binary)').toBeTruthy();
    const rq = await playwright.request.newContext();
    const ctx = await browser.newContext({
      baseURL: BASE,
      locale: 'de-DE',
      extraHTTPHeaders: { 'X-Real-IP': realIp() },
    });
    const page = await ctx.newPage();
    const account = await signupTenant(page, rq, 'onboard');
    const token = await uiLogin(page, account);
    const me = await api<{ sub: string }>(rq, token, 'GET', '/me');
    expect(me.status).toBe(200);
    const collectorName = `audit-onboarding-${stamp()}`;
    w = {
      rq,
      ctx,
      page,
      token,
      me: me.body.sub,
      account,
      workDir: mkdtempSync(join(tmpdir(), 'reticora-e2e-onboarding-')),
      collectorName,
      collectorId: '',
      clientA: '',
      clientB: '',
      orgCred: { id: '', community: '' },
      ownCred: { id: '', community: '' },
      foreignCred: { id: '', community: '' },
    };

    // Two clients and the credentials of the vault, before the collector connects.
    const s = stamp();
    w.clientA = (
      await ok<{ id: string }>('POST', '/clients', {
        name: `Onboarding A ${s}`,
        slug: `onb-a-${s}`,
      })
    ).id;
    w.clientB = (
      await ok<{ id: string }>('POST', '/clients', {
        name: `Onboarding B ${s}`,
        slug: `onb-b-${s}`,
      })
    ).id;
    w.orgCred = await createCredential('org');
    w.ownCred = await createCredential('client-a', w.clientA);
    w.foreignCred = await createCredential('client-b', w.clientB);

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

    const enrolled = spawnSync(
      COLLECTOR_BIN,
      [
        'enroll',
        '--server',
        SERVER,
        '--code',
        code,
        '--name',
        collectorName,
        '--credentials',
        credentialsPath(),
      ],
      { encoding: 'utf8', timeout: 60_000 },
    );
    expect(enrolled.status, `collector enroll: ${enrolled.stderr}${enrolled.stdout}`).toBe(0);
    const collectors = (await ok<List<Collector>>('GET', '/collectors?limit=100')).data ?? [];
    w.collectorId = collectors.find((c) => c.name === collectorName)?.id ?? '';
    expect(w.collectorId, 'the enrolled collector is registered').not.toBe('');
    startCollector();
  });

  test.afterEach(async () => {
    const info = test.info();
    if (info.status === info.expectedStatus) return;
    await info.attach('collector.log', { body: collectorLog(), contentType: 'text/plain' });
    if (fake) {
      await info.attach('fakesnmp-requests.json', {
        body: JSON.stringify(fake.requests(), null, 2),
        contentType: 'application/json',
      });
    }
  });

  test.afterAll(async () => {
    await stopCollector();
    await fake?.stop();
    await w?.ctx.close();
    await w?.rq.dispose();
  });

  test('O1 the subnet a collector enrolled with the code from the Discovery UI reports is suggested and confirmed in the UI: scope created, review resolved by accept', async () => {
    test.setTimeout(4 * 60_000);
    const { page } = w;
    // The collector runs as installed, not with the simulated fleet.
    await expect
      .poll(() => collectorLog(), { message: 'collector start', timeout: 30_000 })
      .toContain('"msg":"collector configured"');
    softExpect(collectorLog(), 'no simulated fleet (RETICORA_SIMULATE empty)').not.toContain(
      'collector.simulate.enabled',
    );

    // INS-06: after connecting, the collector reports its subnets; the server suggests them.
    let open: ReviewItem[] = [];
    await expect
      .poll(
        async () => {
          open = await suggestions();
          return open.length;
        },
        { message: 'scope suggestions of the collector', timeout: 90_000, intervals: [2_000] },
      )
      .toBeGreaterThan(0);
    const item = open[0]!;
    const cidr = String(item.payload.cidr ?? '');
    expect(cidr, 'suggested subnet').toMatch(/^\d{1,3}(\.\d{1,3}){3}\/\d{1,2}$/);
    expect(item.payload.collector_name, 'suggestion names the collector').toBe(w.collectorName);

    // UI: the Discovery page lists the suggestion with its collector; it is confirmed there.
    await page.goto('/discovery');
    const card = page.locator('section').filter({
      has: page.getByRole('heading', { level: 3, name: 'Vorgeschlagene Scopes', exact: true }),
    });
    const row = card.locator('li').filter({ hasText: cidr });
    await expect(row).toHaveCount(1);
    await expect(row).toContainText(`gemeldet von ${w.collectorName}`);
    const resolved = page.waitForResponse(
      (r) =>
        r.url().includes(`/review-items/${item.id}/resolve`) && r.request().method() === 'POST',
    );
    const at = Date.now();
    await row.getByRole('button', { name: 'Als Scope übernehmen' }).click();
    expect((await resolved).status(), 'POST /review-items/{id}/resolve (accept)').toBe(200);
    await expect(row, 'the confirmed suggestion leaves the list').toHaveCount(0);

    // DIS-09: the collector has the scope of the subnet, enabled and without credential_ids
    // (E-67: the organization-wide credentials and those of its client apply).
    const scope = (await scopes()).find((s) => s.kind === 'cidr' && s.target === cidr);
    expect(
      scope,
      `GET /discovery-scopes?collector_id=${w.collectorId}: scope ${cidr}`,
    ).toBeTruthy();
    expect(scope).toMatchObject({ collector_id: w.collectorId, enabled: true, credential_ids: [] });
    // The review is resolved by accept, by the signed-in user, and names the scope.
    const stored = await ok<ReviewItem>('GET', `/review-items/${item.id}`);
    expect(stored).toMatchObject({
      kind: 'scope_suggested',
      status: 'resolved',
      action: 'accept',
      resolved_by: w.me,
    });
    softExpect(stored.resolution, 'resolution names the created scope').toBe(
      `created scope ${scope!.id}`,
    );
    accepted = { item: stored, scope: scope!, at };

    // UI: the card of the collector lists the scope as enabled.
    await expect(
      collectorCard(page)
        .locator('li')
        .filter({ hasText: cidr })
        .getByText('Aktiv', { exact: true }),
    ).toBeVisible();
  });

  test('O2 after the confirmation the next credential bundle holds the organization-wide snmp_v2c credential, before it the bundle was empty (F-027)', async () => {
    test.setTimeout(3 * 60_000);
    expect(accepted, 'the scope of the suggestion confirmed in O1').toBeTruthy();
    const confirmed = accepted!;

    // Before: without an enabled scope the bundle is empty although three credentials exist
    // (E-67; F-027: no oversupply).
    const before = (await bundleDeliveries()).filter((e) => Date.parse(e.timestamp) < confirmed.at);
    softExpect(before.length, 'bundle delivered at the first connection').toBeGreaterThan(0);
    for (const e of before) {
      softExpect(e.changes?.credential_ids, `bundle ${e.seq} before the confirmation`).toEqual([]);
    }

    // The next bundle: the collector connects again.
    const seq = await lastBundleSeq();
    const offset = await restartCollector();
    try {
      // Within 40 s, so that the collector is stopped before its first scope pass.
      const next = await nextBundle(seq, 'credential bundle after the confirmation', 40_000);
      // F-027 retest: confirming the suggestion does not take the credentials away. E-67
      // option A: the scope without credential_ids (no client) gets the organization-wide
      // credential, not those of the clients A and B.
      expect(next.changes?.credential_ids, 'credential IDs of the next bundle').toEqual([
        w.orgCred.id,
      ]);
      // The collector opened the bundle and holds the confirmed scope.
      await softExpect
        .poll(() => collectorLog(offset), {
          message: 'the collector opened the bundle',
          timeout: 10_000,
          intervals: [500],
        })
        .toMatch(/"msg":"credential bundle updated","credentials":1\b/);
      softExpect(collectorLog(offset), 'the collector holds the confirmed scope').toMatch(
        /"msg":"discovery scopes updated","scopes":1\b/,
      );
    } finally {
      // Before its first scope pass a minute after the start: the confirmed subnet is a real
      // network of the runner.
      await stopCollector();
    }
  });

  test('O3 a scope with the credential of another client is refused with 422 and nothing is stored', async () => {
    const target = '127.0.0.162';
    const input = (credentialIds: string[]) => ({
      collector_id: w.collectorId,
      name: `audit-foreign-${stamp()}`,
      kind: 'host',
      target,
      client_id: w.clientA,
      plugins: ['snmp'],
      credential_ids: credentialIds,
      enabled: false,
    });
    // The credential of client B on a scope of client A, alone and next to an own one.
    for (const ids of [[w.foreignCred.id], [w.ownCred.id, w.foreignCred.id]]) {
      const refused = await api<{ field?: string }>(
        w.rq,
        w.token,
        'POST',
        '/discovery-scopes',
        input(ids),
      );
      expect(
        refused.status,
        `POST with ${ids.length} credential(s): ${JSON.stringify(refused.body)}`,
      ).toBe(422);
      expect(refused.body.field).toBe('credential_ids');
    }
    expect(
      (await scopes()).filter((s) => s.target === target),
      'no scope stored',
    ).toEqual([]);

    // Control: the credential of the scope's own client is accepted.
    const created = await ok<Scope>('POST', '/discovery-scopes', input([w.ownCred.id]));
    expect(created).toMatchObject({ client_id: w.clientA, credential_ids: [w.ownCred.id] });
    // Changing it to the credential of client B is refused; the scope stays unchanged.
    const patched = await api<{ field?: string }>(
      w.rq,
      w.token,
      'PATCH',
      `/discovery-scopes/${created.id}`,
      { credential_ids: [w.foreignCred.id] },
    );
    expect(patched.status, `PATCH: ${JSON.stringify(patched.body)}`).toBe(422);
    expect(patched.body.field).toBe('credential_ids');
    expect((await ok<Scope>('GET', `/discovery-scopes/${created.id}`)).credential_ids).toEqual([
      w.ownCred.id,
    ]);
    const deleted = await api(w.rq, w.token, 'DELETE', `/discovery-scopes/${created.id}`);
    expect(deleted.status, 'the control scope is removed again').toBe(204);
  });

  test('O4 a host scope with an explicit snmp_v2c credential discovers the SNMP fake: CI with source snmp, the fake saw exactly that community', async () => {
    test.setTimeout(8 * 60_000);
    const { page } = w;
    await renewToken();
    const sysName = `audit-fake-${stamp()}`;
    fake = await startFakeSnmp({
      ip: FAKE_SNMP_IP,
      community: w.ownCred.community,
      sysName,
      sysDescr: 'Reticora audit SNMP fake',
      sysObjectId: '.1.3.6.1.4.1.8072.3.2.10',
    });

    // Only the host scope is enabled: its credential is the bundle, the fake the only target.
    for (const s of await scopes()) {
      if (s.enabled) await ok('PATCH', `/discovery-scopes/${s.id}`, { enabled: false });
    }
    const scope = await ok<Scope>('POST', '/discovery-scopes', {
      collector_id: w.collectorId,
      name: `audit-snmp-fake-${stamp()}`,
      kind: 'host',
      target: FAKE_SNMP_IP,
      client_id: w.clientA,
      plugins: ['snmp'],
      credential_ids: [w.ownCred.id],
    });
    expect(scope).toMatchObject({
      kind: 'host',
      target: FAKE_SNMP_IP,
      client_id: w.clientA,
      credential_ids: [w.ownCred.id],
      enabled: true,
    });

    const seq = await lastBundleSeq();
    const offset = await restartCollector();
    // COL-02: the bundle holds exactly the host scope's credential – not the
    // organization-wide one (it would apply to a scope without credential_ids), not client B's.
    const bundle = await nextBundle(seq, 'credential bundle with the host scope');
    expect(bundle.changes?.credential_ids, 'credential IDs of the bundle').toEqual([w.ownCred.id]);
    await softExpect
      .poll(() => collectorLog(offset), {
        message: 'the collector opened the bundle',
        timeout: 30_000,
      })
      .toMatch(/"msg":"credential bundle updated","credentials":1\b/);

    // DIS-09: the host scope's pass runs at the collector's next discovery tick (every
    // minute); the SNMP plugin queries the fake with the bundle credential.
    let ci: CI | undefined;
    await expect
      .poll(
        async () => {
          ci = (await cis()).find((c) => c.management_ip === FAKE_SNMP_IP);
          return ci?.discovery_source ?? '';
        },
        { message: `CI of the SNMP fake ${FAKE_SNMP_IP}`, timeout: 4 * 60_000, intervals: [3_000] },
      )
      .toBe('snmp');
    await softExpect
      .poll(
        async () =>
          Number(
            (await ok<Scope>('GET', `/discovery-scopes/${scope.id}`)).last_result?.records ?? 0,
          ),
        { message: 'run of the host scope reported with its record', timeout: 60_000 },
      )
      .toBeGreaterThan(0);

    // UI: the CI detail shows the discovered CI with its source.
    await page.goto(`/cmdb/${ci!.id}`);
    await expect(page.getByRole('heading', { level: 2, name: ci!.name })).toBeVisible();
    await expect(detailValue(page, 'Quelle')).toHaveText('snmp');

    // COL-02: the fake saw the community of the scope's credential and no other secret.
    const seen = fake.requests();
    expect(seen.filter((r) => r.answered).length, 'requests the fake answered').toBeGreaterThan(0);
    expect(
      [...new Set(seen.map((r) => `${r.version}:${r.community}`))],
      'versions and communities the fake received',
    ).toEqual([`2c:${w.ownCred.community}`]);
    for (const [label, secret] of [
      ['organization-wide credential', w.orgCred.community],
      ['credential of client B', w.foreignCred.community],
      ['plugin default', 'public'],
    ] as const) {
      expect(seen.filter((r) => r.community === secret).length, `requests with the ${label}`).toBe(
        0,
      );
    }
    // The CI carries what the device answered, not the secret it was asked with: a v2c
    // response echoes the community in its header (finding during WP-384, the collector
    // stored it as name, serial, sysDescr, …).
    const stored = await ok<CI>('GET', `/cis/${ci!.id}`);
    softExpect(stored.name, 'CI name = sysName of the fake').toBe(sysName);
    expect(
      JSON.stringify(stored),
      `GET /cis/${ci!.id} holds no community of client A`,
    ).not.toContain(w.ownCred.community);
  });
});
