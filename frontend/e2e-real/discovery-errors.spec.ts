import { spawn, spawnSync, type ChildProcess } from 'node:child_process';
import { appendFileSync, existsSync, mkdtempSync, readFileSync } from 'node:fs';
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
  type FakeSnmp,
} from './support/stack';

/*
 * Phase-1 acceptance – discovery error states from a real run (WP-542, DIS-08, open evidence
 * of WP-155), nothing mocked: UI → REST API → PostgreSQL → mTLS NATS edge → collector →
 * device → scope run report → UI. The collector binary (E2E_COLLECTOR_BIN) runs as installed,
 * not simulated (RETICORA_SIMULATE unset, RETICORA_ENVIRONMENT=production): it fetches its
 * scopes and its encrypted credential bundle over the edge, discovers two host scopes with
 * its SNMP plugin and reports each run over the scope channel.
 *
 * One organization is created through the self-signup UI with one client and one snmp_v3
 * credential (authPriv) whose keys are wrong. Its collector is enrolled with a code minted
 * through the API. Both scopes are created before the collector starts, so the first bundle
 * holds the credential and the first discovery tick runs them:
 *  - a host scope on the SNMPv3 fake of collector/internal/fakesnmp (support/stack.ts,
 *    startFakeSnmp with v3) on 127.0.0.163:161, which answers only its user with the right
 *    keys and reports the wrong key with usmStatsWrongDigests (RFC 3414 3.2, WP-492);
 *  - a host scope on 192.0.2.10 (TEST-NET-1, RFC 5737, not routed): nothing answers there.
 *
 * Test → requirement IDs (audit WPs, findings) proven:
 *  E1 the scope on the SNMPv3 fake: GET /discovery-scopes/{id} holds the error of the run in
 *     last_result with cause auth_failed, protocol snmp and the target; the Discovery page
 *     shows the scope's error badge and the error with cause, protocol and target; the fake
 *     received the credential's user with a wrong key and answered it with
 *     usmStatsWrongDigests only ........................... DIS-08 (WP-155, WP-492, F-105)
 *  E2 the scope on the unreachable address: last_result holds the error with cause
 *     unreachable, protocol snmp and the target; the Discovery page shows the error badge
 *     and the error with cause, protocol and target ............................ DIS-08 (WP-155)
 *
 * A failing test is a product finding: the assertions encode the requirement text DIS-08
 * ("Fehlerzustände in UI, Metriken und Logs sichtbar") and the acceptance criteria of WP-542,
 * not the observed behaviour.
 */

/** Address of the SNMPv3 fake: loopback, so no subnet the collector reports covers it. */
const FAKE_SNMP_IP = '127.0.0.163';
/** TEST-NET-1 (RFC 5737): not routed, nothing answers. */
const UNREACHABLE_IP = '192.0.2.10';
/** The user of the fake; the credential carries it with wrong keys. */
const V3_USER = 'audit-v3';

/** Labels of de-DE.json (discovery.scopes.health / .cause). */
const HEALTH_LABEL: Record<string, string> = {
  ok: 'Letzter Lauf ok',
  partial: 'Teilweise fehlgeschlagen',
  failed: 'Fehlgeschlagen',
  never_run: 'Noch nicht gelaufen',
};
const CAUSE_LABEL: Record<string, string> = {
  auth_failed: 'Anmeldung fehlgeschlagen',
  unreachable: 'Nicht erreichbar',
};

interface List<T> {
  data: T[] | null;
  total: number;
}

interface ErrorState {
  cause: string;
  message?: string;
  protocol?: string;
  target?: string;
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
  last_result?: { status?: string; records?: number; errors?: ErrorState[] };
  health?: { state: string; errors?: ErrorState[] };
}

interface World {
  rq: APIRequestContext;
  ctx: BrowserContext;
  page: Page;
  token: string;
  workDir: string;
  collectorName: string;
  collectorId: string;
  authScope: Scope;
  unreachableScope: Scope;
}

let w: World;
let collector: ChildProcess | undefined;
let fake: FakeSnmp | undefined;

/** A documentation address (198.18.0.0/15) so the signup has its own budget (E-33). */
function realIp(): string {
  return `198.18.${Math.floor(Math.random() * 250) + 1}.${Math.floor(Math.random() * 250) + 1}`;
}

function logPath() {
  return join(w.workDir, 'collector.log');
}

function collectorLog(): string {
  return existsSync(logPath()) ? readFileSync(logPath(), 'utf8') : '';
}

/**
 * startCollector runs the enrolled collector as installed (onboarding-bundle.spec.ts): no
 * RETICORA_* variable or proxy of the runner is passed on; the fallback pass before the
 * scope list arrives is a sweep of TEST-NET-1 without any credential.
 */
function startCollector() {
  const env: NodeJS.ProcessEnv = {};
  for (const [key, value] of Object.entries(process.env)) {
    if (key.startsWith('RETICORA_') || /^(https?|all|no)_proxy$/i.test(key)) continue;
    env[key] = value;
  }
  collector = spawn(COLLECTOR_BIN, [], {
    env: {
      ...env,
      RETICORA_ENVIRONMENT: 'production',
      RETICORA_CREDENTIALS_PATH: join(w.workDir, 'credentials.json'),
      RETICORA_SPOOL_DIR: join(w.workDir, 'spool'),
      RETICORA_HEARTBEAT_INTERVAL: '10s',
      RETICORA_SPOOL_FLUSH_INTERVAL: '5s',
      RETICORA_SCAN_SUBNETS: '192.0.2.1/32',
      RETICORA_DISCOVERY_PROTOCOLS: 'sweep',
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  appendFileSync(logPath(), `### collector started ${new Date().toISOString()}\n`);
  collector.stdout?.on('data', (chunk: Buffer) => appendFileSync(logPath(), chunk));
  collector.stderr?.on('data', (chunk: Buffer) => appendFileSync(logPath(), chunk));
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

/** collectorCard is the card of the run's collector on the Discovery page. */
function collectorCard(page: Page) {
  return page
    .locator('section')
    .filter({ has: page.getByRole('heading', { level: 3, name: w.collectorName, exact: true }) });
}

/** renewToken takes the access token the signed-in page holds now (AUT-02, 15 min). */
async function renewToken() {
  await w.page.goto('/discovery');
  await expect(collectorCard(w.page)).toBeVisible();
  w.token = await sessionToken(w.page);
}

/**
 * expectScopeError asserts the error of the scope's last run in the API (last_result and
 * health of GET /discovery-scopes/{id}) and on the Discovery page (error badge, cause,
 * protocol and target).
 */
async function expectScopeError(scope: Scope, cause: string) {
  await renewToken();
  const stored = await ok<Scope>('GET', `/discovery-scopes/${scope.id}`);
  expect(stored.last_run_at, 'the collector reported a run of the scope').toBeTruthy();
  const want = { cause, protocol: 'snmp', target: scope.target };
  expect(
    (stored.last_result?.errors ?? []).map((e) => ({
      cause: e.cause,
      protocol: e.protocol,
      target: e.target,
    })),
    `last_result of GET /discovery-scopes/${scope.id}: ${JSON.stringify(stored.last_result)}`,
  ).toEqual([want]);
  expect(['partial', 'failed'], `health ${JSON.stringify(stored.health)}`).toContain(
    stored.health?.state,
  );
  expect(stored.last_result?.status, 'state of the run in last_result').toBe(stored.health?.state);

  // UI: the scope's entry in the collector's card shows the error badge of the state and the
  // error with cause, protocol and target.
  const { page } = w;
  await page.goto('/discovery');
  const item = collectorCard(page)
    .getByRole('listitem')
    .filter({ has: page.getByRole('button', { name: `Scope ${scope.name} bearbeiten` }) });
  await expect(item).toHaveCount(1);
  await expect(item.getByText(HEALTH_LABEL[stored.health!.state]!, { exact: true })).toBeVisible();
  const error = item.getByRole('listitem');
  await expect(error).toHaveCount(1);
  await expect(error).toContainText(`${CAUSE_LABEL[cause]} · snmp · ${scope.target}`);
}

test.describe('Discovery error states from a real run (DIS-08)', () => {
  test.beforeAll(async ({ browser, playwright }) => {
    test.setTimeout(9 * 60_000);
    expect(COLLECTOR_BIN, 'E2E_COLLECTOR_BIN (collector binary)').toBeTruthy();
    const rq = await playwright.request.newContext();
    const ctx = await browser.newContext({
      baseURL: BASE,
      locale: 'de-DE',
      extraHTTPHeaders: { 'X-Real-IP': realIp() },
    });
    const page = await ctx.newPage();
    const account = await signupTenant(page, rq, 'discerr');
    const token = await uiLogin(page, account);
    const collectorName = `audit-discovery-errors-${stamp()}`;
    w = {
      rq,
      ctx,
      page,
      token,
      workDir: mkdtempSync(join(tmpdir(), 'reticora-e2e-discovery-errors-')),
      collectorName,
      collectorId: '',
      authScope: undefined as unknown as Scope,
      unreachableScope: undefined as unknown as Scope,
    };

    // The SNMPv3 fake with its user's keys; the credential carries the user with other keys.
    fake = await startFakeSnmp({
      ip: FAKE_SNMP_IP,
      v3: {
        user: V3_USER,
        authPassword: `fake-auth-${stamp()}`,
        privPassword: `fake-priv-${stamp()}`,
      },
      sysName: `audit-v3-fake-${stamp()}`,
      sysDescr: 'Reticora audit SNMPv3 fake',
      sysObjectId: '.1.3.6.1.4.1.8072.3.2.10',
    });
    const s = stamp();
    const clientId = (
      await ok<{ id: string }>('POST', '/clients', {
        name: `Discovery errors ${s}`,
        slug: `disc-err-${s}`,
      })
    ).id;
    const credential = await ok<{ id: string; kind: string }>('POST', '/credentials', {
      name: `audit snmp_v3 wrong keys ${s}`,
      kind: 'snmp_v3',
      client_id: clientId,
      secret: {
        username: V3_USER,
        auth_protocol: 'SHA256',
        auth_password: `wrong-auth-${s}`,
        priv_protocol: 'AES',
        priv_password: `wrong-priv-${s}`,
      },
    });
    expect(credential.kind).toBe('snmp_v3');

    // Enrollment with a code minted through the API.
    const minted = await ok<{ code: string }>('POST', '/collectors/enrollment-codes', {
      label: collectorName,
    });
    expect(minted.code, 'enrollment code').toBeTruthy();
    const enrolled = spawnSync(
      COLLECTOR_BIN,
      [
        'enroll',
        '--server',
        SERVER,
        '--code',
        minted.code,
        '--name',
        collectorName,
        '--credentials',
        join(w.workDir, 'credentials.json'),
      ],
      { encoding: 'utf8', timeout: 60_000 },
    );
    expect(enrolled.status, `collector enroll: ${enrolled.stderr}${enrolled.stdout}`).toBe(0);
    const collectors = (
      await ok<List<{ id: string; name: string }>>('GET', '/collectors?limit=100')
    ).data;
    w.collectorId = (collectors ?? []).find((c) => c.name === collectorName)?.id ?? '';
    expect(w.collectorId, 'the enrolled collector is registered').not.toBe('');

    const hostScope = (label: string, target: string) =>
      ok<Scope>('POST', '/discovery-scopes', {
        collector_id: w.collectorId,
        name: `audit-${label}-${stamp()}`,
        kind: 'host',
        target,
        client_id: clientId,
        plugins: ['snmp'],
        credential_ids: [credential.id],
      });
    w.authScope = await hostScope('snmpv3-wrong-key', FAKE_SNMP_IP);
    w.unreachableScope = await hostScope('unreachable', UNREACHABLE_IP);
    for (const scope of [w.authScope, w.unreachableScope]) {
      expect(scope).toMatchObject({ kind: 'host', enabled: true, credential_ids: [credential.id] });
    }

    startCollector();
    await expect
      .poll(() => collectorLog(), { message: 'collector start', timeout: 30_000 })
      .toContain('"msg":"collector configured"');
    expect(collectorLog(), 'no simulated fleet (RETICORA_SIMULATE empty)').not.toContain(
      'collector.simulate.enabled',
    );
    // The scope passes run at the collector's next discovery tick (every minute) and report
    // their runs over the scope channel.
    await expect
      .poll(
        async () => {
          const ran: string[] = [];
          for (const scope of [w.authScope, w.unreachableScope]) {
            const stored = await ok<Scope>('GET', `/discovery-scopes/${scope.id}`);
            if (stored.last_run_at) ran.push(scope.target);
          }
          return ran.sort();
        },
        { message: 'runs of both scopes reported', timeout: 5 * 60_000, intervals: [5_000] },
      )
      .toEqual([FAKE_SNMP_IP, UNREACHABLE_IP].sort());
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

  test('E1 a host scope with a wrong SNMPv3 key on the SNMPv3 fake shows auth_failed with protocol and target in last_result and on the Discovery page', async () => {
    await expectScopeError(w.authScope, 'auth_failed');

    // The fake saw the credential's user with a wrong key and refused every request of it
    // with usmStatsWrongDigests; no SNMPv1/v2c request (no community) reached it.
    const seen = fake!.requests();
    const ofUser = seen.filter((r) => r.version === '3' && r.user === V3_USER);
    expect(ofUser.length, 'SNMPv3 requests of the credential user').toBeGreaterThan(0);
    expect(
      [...new Set(ofUser.map((r) => `${r.report ?? ''}:${r.answered}`))],
      'answers to the requests of the credential user',
    ).toEqual(['usmStatsWrongDigests:true']);
    expect(
      seen.filter((r) => r.version !== '3'),
      'requests other than SNMPv3',
    ).toEqual([]);
  });

  test('E2 a host scope on an unreachable address shows unreachable with protocol and target in last_result and on the Discovery page', async () => {
    await expectScopeError(w.unreachableScope, 'unreachable');
  });
});
