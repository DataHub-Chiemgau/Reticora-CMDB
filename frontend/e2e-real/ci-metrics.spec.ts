import { spawn, spawnSync, type ChildProcess } from 'node:child_process';
import { appendFileSync, existsSync, mkdtempSync, readFileSync, statSync } from 'node:fs';
import { createServer, type Server } from 'node:net';
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
  keycloakAdminToken,
  operatorLogin,
  sessionToken,
  signupTenant,
  stamp,
  uiLogin,
  BASE,
  COLLECTOR_BIN,
  KEYCLOAK,
  SERVER,
  type Account,
  type OperatorSession,
} from './support/stack';
import { dbRows, uuid } from './support/locations-racks';

/*
 * Collector metrics end to end on the real stack (WP-426, MON-02; F-046, nothing mocked):
 * collector → mTLS NATS edge (collector.<org>.<id>.metrics) → monitoring ingest →
 * PostgreSQL (metric_sample) → REST API → CI detail. Before WP-426 the collector POSTed its
 * samples to /api/v1/monitoring/metrics without a credential, the API answered 401 and the
 * collector logged "metric sample upload failed": no installation had any MON-02 baseline
 * metric.
 *
 * One organization is created through the self-signup UI; an operator grants it the
 * monitoring feature (POST /admin/orgs/{id}/entitlements, ENT-04), which the trial plan does
 * not include. The organization has the clients 1 and 2. Two simulated devices listen on
 * loopback addresses (TCP 8006, one of the sweep ports of DIS-01); each has its CI of client 1
 * with the device's address as management IP, and a host scope of the collector with the
 * sweep plugin. The collector binary (E2E_COLLECTOR_BIN) is enrolled with a code from the
 * Discovery UI and runs as installed (RETICORA_ENVIRONMENT=production); it fetches its
 * scopes over the edge and sweeps them a minute after the start.
 *
 * Test → requirement IDs proven:
 *  M1 after the sweep metric_sample holds sweep_reachable rows with the ci_id of each
 *     simulated CI, GET /cis/{id}/metrics returns sweep_reachable points (value 1), the CI
 *     detail draws the metric line, the collector log has the delivery over the edge and no
 *     "metric sample upload failed" ......................... MON-02 (WP-190, WP-426, F-046)
 *  M2 a user of client 2 gets 404 for the metrics and the detail of a client-1 CI
 *     ........................................................ MON-02, TEN-06 (WP-402, F-167)
 *
 * A failing test is a product finding: the assertions encode the requirement texts in
 * docs/spec/katalog-v3 (MON-02, TEN-06) and the acceptance criteria of WP-426.
 */

// Requirement checks after a precondition: a violation is reported, the test goes on.
const softExpect = expect.configure({ soft: true });
/** A sweep port of DIS-01 (sweep.DefaultPorts) that needs no privilege. */
const DEVICE_PORT = 8006;
/** Loopback addresses of the simulated devices: no subnet the collector reports covers them. */
const DEVICE_IPS = ['127.0.0.171', '127.0.0.172'];

interface List<T> {
  data: T[] | null;
  total: number;
}

interface Collector {
  id: string;
  name: string;
}

interface CI {
  id: string;
  name: string;
  client_id?: string;
  management_ip?: string;
}

interface CIMetrics {
  ci_id: string;
  series: { name: string; points: { timestamp: string; value: number }[] }[];
}

interface World {
  rq: APIRequestContext;
  ctx: BrowserContext;
  page: Page;
  token: string;
  account: Account;
  orgId: string;
  workDir: string;
  collectorName: string;
  collectorId: string;
  client1: string;
  client2: string;
  /** The CIs of the simulated devices (client 1), in the order of DEVICE_IPS. */
  cis: CI[];
}

let w: World;
let collector: ChildProcess | undefined;
const devices: Server[] = [];

/** A documentation address (198.18.0.0/15) so the signup has its own budget (E-33). */
function realIp(): string {
  return `198.18.${Math.floor(Math.random() * 250) + 1}.${Math.floor(Math.random() * 250) + 1}`;
}

function claims(token: string): { sub: string; org: string } {
  return JSON.parse(Buffer.from(token.split('.')[1] ?? '', 'base64url').toString()) as {
    sub: string;
    org: string;
  };
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
 * startCollector runs the enrolled collector as installed. The environment of the runner is
 * not passed on as configuration (no RETICORA_* variable, no proxy: the collector talks to
 * localhost only); the fallback pass before the scope list arrives is a sweep of TEST-NET-1
 * (192.0.2.1, not routed).
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

/** startDevice is a simulated device: it accepts TCP connections on DEVICE_PORT at ip. */
async function startDevice(ip: string): Promise<Server> {
  const server = createServer((socket) => socket.destroy());
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject);
    server.listen(DEVICE_PORT, ip, () => resolve());
  });
  return server;
}

async function ok<T>(
  method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE',
  path: string,
  data?: unknown,
  token = w.token,
): Promise<T> {
  const res = await api<T>(w.rq, token, method, path, data);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeGreaterThanOrEqual(200);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeLessThan(300);
  return res.body;
}

/** renewToken takes the access token the signed-in page holds now (AUT-02, 15 min). */
async function renewToken() {
  await w.page.goto('/cmdb');
  await expect(w.page.getByLabel('Configuration Items durchsuchen')).toBeVisible();
  w.token = await sessionToken(w.page);
}

/**
 * grantMonitoring lets an operator (realm group operators, password and OTP) grant the
 * organization the monitoring feature, as the operator API does (ENT-04).
 */
async function grantMonitoring(browser: Browser, rq: APIRequestContext, orgId: string) {
  const run = stamp();
  const operator = {
    email: `metrics-operator-${run}@audit-e2e.de`,
    password: `Audit-op-${run}!aA`,
  };
  const admin = await keycloakAdminToken(rq);
  const created = await rq.post(`${KEYCLOAK}/admin/realms/reticora/users`, {
    headers: { Authorization: `Bearer ${admin}` },
    data: {
      username: operator.email,
      email: operator.email,
      emailVerified: true,
      enabled: true,
      firstName: 'Audit',
      lastName: 'Operator',
      groups: ['/operators'],
      credentials: [{ type: 'password', value: operator.password, temporary: false }],
    },
    failOnStatusCode: false,
  });
  expect(created.status(), `create the operator account: ${await created.text()}`).toBe(201);
  const context = await browser.newContext({ locale: 'de-DE' });
  try {
    const login = await operatorLogin(await context.newPage(), context.request, operator);
    expect(login.status, `operator callback: ${JSON.stringify(login.body)}`).toBe(200);
    const session = login.body as OperatorSession;
    const granted = await api(rq, session.token, 'POST', `/admin/orgs/${orgId}/entitlements`, {
      feature_key: 'monitoring',
      plan: 'pro',
    });
    expect(granted.status, `grant monitoring: ${JSON.stringify(granted.body)}`).toBe(200);
  } finally {
    await context.close();
  }
}

/** sampleRows counts the sweep_reachable rows of metric_sample per CI (read-only probe). */
function sampleRows(): Record<string, number> {
  const ids = w.cis.map((c) => `'${uuid(c.id)}'`).join(', ');
  const counts: Record<string, number> = Object.fromEntries(w.cis.map((c) => [c.id, 0]));
  for (const row of dbRows<{ ci_id: string; n: number }>(
    `SELECT ci_id::text AS ci_id, count(*)::int AS n FROM metric_sample
      WHERE ci_id IN (${ids}) AND metric_name = 'sweep_reachable' GROUP BY ci_id`,
  )) {
    counts[row.ci_id] = row.n;
  }
  return counts;
}

test.describe('Collector metrics over the edge: sweep samples of the real collector reach metric_sample, the API and the CI detail', () => {
  test.beforeAll(async ({ browser, playwright }) => {
    test.setTimeout(6 * 60_000);
    expect(COLLECTOR_BIN, 'E2E_COLLECTOR_BIN (collector binary)').toBeTruthy();
    const rq = await playwright.request.newContext();
    const ctx = await browser.newContext({
      baseURL: BASE,
      locale: 'de-DE',
      extraHTTPHeaders: { 'X-Real-IP': realIp() },
    });
    const page = await ctx.newPage();
    const account = await signupTenant(page, rq, 'metrics');
    const token = await uiLogin(page, account);
    const orgId = claims(token).org;
    expect(orgId, 'organization of the session').toBeTruthy();
    w = {
      rq,
      ctx,
      page,
      token,
      account,
      orgId,
      workDir: mkdtempSync(join(tmpdir(), 'reticora-e2e-metrics-')),
      collectorName: `audit-metrics-${stamp()}`,
      collectorId: '',
      client1: '',
      client2: '',
      cis: [],
    };

    // The trial plan has no monitoring; the operator grants it.
    await grantMonitoring(browser, rq, orgId);
    const check = await ok<{ enabled: boolean }>('GET', '/entitlements/check/monitoring');
    expect(check.enabled, 'monitoring after the grant').toBe(true);

    const s = stamp();
    w.client1 = (
      await ok<{ id: string }>('POST', '/clients', { name: `Metrics 1 ${s}`, slug: `met-1-${s}` })
    ).id;
    w.client2 = (
      await ok<{ id: string }>('POST', '/clients', { name: `Metrics 2 ${s}`, slug: `met-2-${s}` })
    ).id;
    const types = await ok<List<{ id: string; key: string }>>('GET', '/ci-types?limit=200');
    const serverType = (types.data ?? []).find((t) => t.key === 'server')?.id ?? '';
    expect(serverType, 'catalog type server').not.toBe('');

    // The simulated devices and their CIs of client 1.
    for (const ip of DEVICE_IPS) {
      devices.push(await startDevice(ip));
      w.cis.push(
        await ok<CI>('POST', '/cis', {
          ci_type_id: serverType,
          name: `audit-metrics-${ip}-${s}`,
          client_id: w.client1,
          management_ip: ip,
        }),
      );
    }

    // The enrollment code is minted in the Discovery UI ("Collector verbinden").
    await page.goto('/discovery');
    await page.getByRole('button', { name: 'Collector verbinden' }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByLabel('Bezeichnung (z. B. Standort)').fill(w.collectorName);
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
        w.collectorName,
        '--credentials',
        credentialsPath(),
      ],
      { encoding: 'utf8', timeout: 60_000 },
    );
    expect(enrolled.status, `collector enroll: ${enrolled.stderr}${enrolled.stdout}`).toBe(0);
    const collectors = (await ok<List<Collector>>('GET', '/collectors?limit=100')).data ?? [];
    w.collectorId = collectors.find((c) => c.name === w.collectorName)?.id ?? '';
    expect(w.collectorId, 'the enrolled collector is registered').not.toBe('');

    // One host scope per device with the sweep plugin, before the collector fetches its
    // scopes.
    for (const ip of DEVICE_IPS) {
      const scope = await ok<{ enabled: boolean; plugins: string[] }>('POST', '/discovery-scopes', {
        collector_id: w.collectorId,
        name: `audit-metrics-${ip}-${s}`,
        kind: 'host',
        target: ip,
        client_id: w.client1,
        plugins: ['sweep'],
      });
      expect(scope).toMatchObject({ enabled: true, plugins: ['sweep'] });
    }
    startCollector();
  });

  test.afterEach(async () => {
    const info = test.info();
    if (info.status === info.expectedStatus) return;
    await info.attach('collector.log', { body: collectorLog(), contentType: 'text/plain' });
  });

  test.afterAll(async () => {
    await stopCollector();
    for (const device of devices.splice(0)) {
      await new Promise<void>((resolve) => device.close(() => resolve()));
    }
    await w?.ctx.close();
    await w?.rq.dispose();
  });

  test('M1 after a sweep of the real collector metric_sample holds sweep_reachable rows of the simulated CIs, GET /cis/{id}/metrics returns them and the CI detail draws the line', async () => {
    test.setTimeout(8 * 60_000);
    const { page } = w;
    await expect
      .poll(() => collectorLog(), { message: 'collector start', timeout: 30_000 })
      .toContain('"msg":"collector configured"');

    // MON-02: the sweep of each host scope reaches metric_sample with the CI's id.
    await expect
      .poll(() => Object.values(sampleRows()).filter((n) => n > 0).length, {
        message: 'metric_sample rows with the ci_id of each simulated CI',
        timeout: 5 * 60_000,
        intervals: [5_000],
      })
      .toBe(DEVICE_IPS.length);

    // The API returns the series of each CI with the reachable points.
    await renewToken();
    for (const ci of w.cis) {
      const res = await api<CIMetrics>(
        w.rq,
        w.token,
        'GET',
        `/cis/${ci.id}/metrics?name=sweep_reachable`,
      );
      expect(res.status, `GET /cis/${ci.id}/metrics: ${JSON.stringify(res.body)}`).toBe(200);
      const series = res.body.series.find((x) => x.name === 'sweep_reachable');
      expect(series?.points.length ?? 0, `sweep_reachable points of ${ci.name}`).toBeGreaterThan(0);
      softExpect(
        series?.points.some((p) => p.value === 1),
        `${ci.name} reachable`,
      ).toBe(true);
    }

    // UI: the CI detail draws the metric line (the name is not asserted: the sweep record
    // of the device may update the CI). The metrics are a collapsed section (details).
    await page.goto(`/cmdb/${w.cis[0]!.id}`);
    await page.locator('#main-content summary', { hasText: /^\s*Metriken\s*$/ }).click();
    const load = page.getByRole('button', { name: 'Metriken laden' });
    await expect(load).toBeVisible();
    const loaded = page.waitForResponse(
      (r) =>
        r.url().includes(`/api/v1/cis/${w.cis[0]!.id}/metrics`) && r.request().method() === 'GET',
    );
    await load.click();
    expect((await loaded).status(), 'GET /cis/{id}/metrics of the CI detail').toBe(200);
    await expect(page.getByRole('img', { name: 'sweep_reachable' })).toBeVisible();

    // F-046: the samples went over the edge, none was refused or lost.
    const log = collectorLog();
    softExpect(log, 'delivery over the edge').toContain('"msg":"metric samples delivered"');
    expect(log, 'collector log').not.toContain('metric sample upload failed');
    softExpect(log, 'collector log').not.toContain('"msg":"metric samples refused"');
    softExpect(log, 'collector log').not.toContain('"msg":"metric samples dropped');
  });

  test('M2 a user of client 2 gets 404 for the metrics and the detail of a client-1 CI', async ({
    browser,
  }) => {
    test.setTimeout(3 * 60_000);
    await renewToken();
    const roles = await ok<List<{ id: string; name: string }>>('GET', '/roles?limit=100');
    const viewerRole = (roles.data ?? []).find((r) => r.name === 'viewer')?.id ?? '';
    expect(viewerRole, 'system role viewer (RBA-02)').not.toBe('');
    const s = stamp();
    const viewer = { email: `metrics-c2-${s}@audit-e2e.de`, password: `Audit-c2-${s}!aA` };
    const invitation = await ok<{ token?: string }>('POST', '/invitations', {
      email: viewer.email,
      role_id: viewerRole,
      scope_type: 'client',
      scope_id: w.client2,
    });
    expect(invitation.token, 'the token is returned once on issue').toBeTruthy();
    const accept = await w.rq.post(`${BASE}/api/v1/invitations/accept`, {
      data: { token: invitation.token, password: viewer.password, display_name: 'Metrics C2' },
      failOnStatusCode: false,
    });
    expect(accept.status(), `accept the invitation: ${await accept.text()}`).toBe(201);

    const ctx = await browser.newContext({ baseURL: BASE, locale: 'de-DE' });
    try {
      const page = await ctx.newPage();
      const token = await uiLogin(page, viewer);
      for (const ci of w.cis) {
        const metrics = await api(page.request, token, 'GET', `/cis/${ci.id}/metrics`);
        expect(metrics.status, `GET /cis/{client-1 CI}/metrics as client 2`).toBe(404);
        expect(JSON.stringify(metrics.body)).not.toContain('sweep_reachable');
        const detail = await api(page.request, token, 'GET', `/cis/${ci.id}`);
        expect(detail.status, `GET /cis/{client-1 CI} as client 2`).toBe(404);
        expect(JSON.stringify(detail.body)).not.toContain(ci.name);
      }
    } finally {
      await ctx.close();
    }
  });
});
