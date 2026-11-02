import { execFileSync, spawn, spawnSync, type ChildProcess } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { appendFileSync, mkdtempSync, readFileSync, existsSync } from 'node:fs';
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
  readComposeEnv,
  restartServer,
  serviceContainer,
  serviceLogs,
  sessionToken,
  signupTenant,
  stamp,
  uiLogin,
  BASE,
  COLLECTOR_BIN,
  DATABASE_URL,
  SERVER,
  type ServiceContainer,
} from './support/stack';

/*
 * WP-547 (SEC-06, WP-141): master key rotation on the installed stack (k-e2e-real-ci),
 * nothing mocked. The operator procedure of docs/ops/workers.md: the new master key in
 * RETICORA_MASTER_KEY, the old one in RETICORA_MASTER_KEY_PREVIOUS (the installation's
 * .env), restart the server only, let rotation.master_key run, then remove the old key and
 * restart again.
 *
 *  K01 with a running collector (simulated fleet, real ingest path over the mTLS NATS edge):
 *      the server restarts with the new key and the old one as previous; the rewrap run
 *      (rotation.master_key, made due through scheduler_run) finishes without error; the
 *      collector process keeps running, its heartbeats go on and a discovery record taken
 *      after the start of the rotation becomes visible; nats-edge was not restarted (same
 *      container, start time, restart count); a paging cursor issued before the rotation
 *      returns the same page afterwards (the cursor key is derived from the derivation
 *      root, WP-459); after the old key is removed the server starts without error and
 *      the collector keeps reporting. (positive)
 *  K02 a server started with a wrong master key does not start and names the reason
 *      ("failed to open the derivation root of the derived secrets"); with the valid key it
 *      starts again. (negative)
 *
 * Every restart goes through restartServer (support/stack.ts): the .env of the installation
 * as the operator changes it and `docker compose up -d --no-deps --force-recreate server`.
 * After each test the server runs again with the last configuration that started, so the
 * specs after this one find a working stack.
 */

const SIMULATED_DEVICES = 30;
const DERIVATION_ROOT_FAILURE = 'failed to open the derivation root of the derived secrets';

interface CI {
  id: string;
  name: string;
  last_seen_at?: string;
}

interface ListPage<T> {
  data: T[] | null;
  has_more?: boolean;
  next_cursor?: string;
}

interface Collector {
  id: string;
  name: string;
  status: string;
  last_heartbeat_at?: string;
}

interface World {
  rq: APIRequestContext;
  ctx: BrowserContext;
  page: Page;
  workDir: string;
  collectorName: string;
  collectorId: string;
}

let w: World;
let collector: ChildProcess | undefined;
/** The master keys of the last configuration the server started with. */
let good: { key: string; previous?: string };

/** A documentation address (198.18.0.0/15) so the signup has its own budget (E-33). */
function realIp(): string {
  return `198.18.${Math.floor(Math.random() * 250) + 1}.${Math.floor(Math.random() * 250) + 1}`;
}

/** A master key as install-cloud.sh draws it: 32 random bytes, base64. */
function masterKey(): string {
  return randomBytes(32).toString('base64');
}

function logPath(): string {
  return join(w.workDir, 'collector.log');
}

function collectorLog(): string {
  return existsSync(logPath()) ? readFileSync(logPath(), 'utf8') : '';
}

function startCollector() {
  const env: NodeJS.ProcessEnv = { ...process.env };
  // The collector talks to localhost only.
  for (const key of ['HTTP_PROXY', 'HTTPS_PROXY', 'http_proxy', 'https_proxy', 'ALL_PROXY']) {
    delete env[key];
  }
  collector = spawn(COLLECTOR_BIN, [], {
    env: {
      ...env,
      RETICORA_ENVIRONMENT: 'development',
      RETICORA_SIMULATE: 'true',
      RETICORA_CREDENTIALS_PATH: join(w.workDir, 'credentials.json'),
      RETICORA_SPOOL_DIR: join(w.workDir, 'spool'),
      RETICORA_HEARTBEAT_INTERVAL: '10s',
      RETICORA_SPOOL_FLUSH_INTERVAL: '5s',
      // The shortest discovery cycle the collector accepts: one more cycle of the same
      // process falls into the rotation.
      RETICORA_DISCOVERY_INTERVAL: '5m',
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  appendFileSync(logPath(), `\n### collector started ${new Date().toISOString()}\n`);
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

/** token takes the access token the signed-in page holds now (the SPA renews it). */
async function token(): Promise<string> {
  await w.page.goto('/discovery');
  return sessionToken(w.page);
}

async function ok<T>(path: string): Promise<T> {
  const res = await api<T>(w.rq, await token(), 'GET', path);
  expect(res.status, `GET ${path}: ${JSON.stringify(res.body)}`).toBe(200);
  return res.body;
}

async function getCollector(): Promise<Collector> {
  const res = await api<ListPage<Collector>>(w.rq, await token(), 'GET', '/collectors?limit=100');
  const found = (res.body.data ?? []).find((c) => c.id === w.collectorId);
  expect(found, `collector ${w.collectorId}`).toBeTruthy();
  return found!;
}

/** maxLastSeen is the newest sighting of the fleet (ms), 0 when the API does not answer. */
async function maxLastSeen(): Promise<number> {
  const res = await api<ListPage<CI>>(w.rq, await token(), 'GET', '/cis?limit=100');
  if (res.status !== 200) return 0;
  return Math.max(0, ...(res.body.data ?? []).map((ci) => Date.parse(ci.last_seen_at ?? '') || 0));
}

/** heartbeatAfter waits until the collector sent a heartbeat after `since`. */
async function heartbeatAfter(since: number, message: string) {
  await expect
    .poll(async () => Date.parse((await getCollector()).last_heartbeat_at ?? '') || 0, {
      message,
      timeout: 90_000,
      intervals: [5_000],
    })
    .toBeGreaterThan(since);
}

function psql(sql: string): string {
  return execFileSync(
    'psql',
    [DATABASE_URL, '-X', '-q', '-At', '-v', 'ON_ERROR_STOP=1', '-f', '-'],
    {
      input: sql,
      encoding: 'utf8',
      timeout: 60_000,
    },
  ).trim();
}

function sameContainer(a: ServiceContainer, b: ServiceContainer): boolean {
  return a.id === b.id && a.startedAt === b.startedAt && a.restartCount === b.restartCount;
}

test.describe('master key rotation on the installed stack (SEC-06)', () => {
  test.beforeAll(async ({ browser, playwright }) => {
    test.setTimeout(6 * 60_000);
    expect(COLLECTOR_BIN, 'E2E_COLLECTOR_BIN (collector binary)').toBeTruthy();
    const env = readComposeEnv();
    expect(
      env.RETICORA_MASTER_KEY,
      'RETICORA_MASTER_KEY in the .env of the installation',
    ).toBeTruthy();
    good = { key: env.RETICORA_MASTER_KEY, previous: env.RETICORA_MASTER_KEY_PREVIOUS };
    const rq = await playwright.request.newContext();
    const ctx = await browser.newContext({
      baseURL: BASE,
      locale: 'de-DE',
      extraHTTPHeaders: { 'X-Real-IP': realIp() },
    });
    const page = await ctx.newPage();
    const account = await signupTenant(page, rq, 'keyrot');
    await uiLogin(page, account);
    w = {
      rq,
      ctx,
      page,
      workDir: mkdtempSync(join(tmpdir(), 'reticora-e2e-keyrot-')),
      collectorName: `keyrot-collector-${stamp()}`,
      collectorId: '',
    };

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
        join(w.workDir, 'credentials.json'),
      ],
      { encoding: 'utf8', timeout: 60_000 },
    );
    expect(enrolled.status, `collector enroll: ${enrolled.stderr}${enrolled.stdout}`).toBe(0);
    const collectors = await api<ListPage<Collector>>(
      rq,
      await token(),
      'GET',
      '/collectors?limit=100',
    );
    w.collectorId = (collectors.body.data ?? []).find((c) => c.name === w.collectorName)?.id ?? '';
    expect(w.collectorId, 'the enrolled collector is registered').not.toBe('');

    startCollector();
    await expect
      .poll(async () => (await ok<ListPage<CI>>('/cis?limit=100')).data?.length ?? 0, {
        message: 'CIs of the simulated fleet',
        timeout: 180_000,
        intervals: [3_000],
      })
      .toBeGreaterThanOrEqual(SIMULATED_DEVICES);
  });

  // The next test and the specs after this one need a server that runs: when a test left
  // it down, it is started again with the last configuration that started.
  test.afterEach(async () => {
    test.setTimeout(test.info().timeout + 5 * 60_000);
    const ready = await fetch(`${SERVER}/readyz`).then(
      (res) => res.ok,
      () => false,
    );
    const env = readComposeEnv();
    if (
      ready &&
      env.RETICORA_MASTER_KEY === good.key &&
      env.RETICORA_MASTER_KEY_PREVIOUS === good.previous
    ) {
      return;
    }
    const restored = await restartServer({
      RETICORA_MASTER_KEY: good.key,
      RETICORA_MASTER_KEY_PREVIOUS: good.previous,
    });
    expect(restored.ready, `server restored with the last working keys:\n${restored.logs}`).toBe(
      true,
    );
  });

  test.afterAll(async () => {
    await stopCollector();
    await w?.ctx.close();
    await w?.rq.dispose();
  });

  test('K01 master key rotation with a running collector: only the server restarts, the rewrap run moves every key, collector and nats-edge keep running, an earlier cursor stays valid, and the server starts without the old key', async () => {
    test.setTimeout(20 * 60_000);
    const pid = collector?.pid;
    expect(pid, 'collector process').toBeTruthy();
    await heartbeatAfter(Date.now() - 30_000, 'collector heartbeats before the rotation');
    const edgeBefore = serviceContainer('nats-edge');
    expect(edgeBefore.running, 'nats-edge runs').toBe(true);
    const serverBefore = serviceContainer('server');

    // A cursor issued before the rotation (page 2 of the fleet by name).
    const sorted = '/cis?limit=10&sort_by=name&sort_dir=asc';
    const first = await ok<ListPage<CI>>(sorted);
    expect(first.has_more && first.next_cursor, 'the first page has a next_cursor').toBeTruthy();
    const cursor = first.next_cursor!;
    const pageTwo =
      (await ok<ListPage<CI>>(`${sorted}&cursor=${encodeURIComponent(cursor)}`)).data ?? [];
    expect(pageTwo.length, 'page 2 before the rotation').toBeGreaterThan(0);

    // 1. The new key current, the old one previous; the server alone restarts.
    const oldKey = good.key;
    const newKey = masterKey();
    const rotationStart = Date.now();
    const rotating = await restartServer({
      RETICORA_MASTER_KEY: newKey,
      RETICORA_MASTER_KEY_PREVIOUS: oldKey,
    });
    expect(rotating.container.id, 'the server container was recreated').not.toBe(serverBefore.id);
    expect(
      {
        key: rotating.container.env.RETICORA_MASTER_KEY === newKey,
        previous: rotating.container.env.RETICORA_MASTER_KEY_PREVIOUS === oldKey,
      },
      'the server got the new master key and the old one as RETICORA_MASTER_KEY_PREVIOUS (docs/ops/workers.md)',
    ).toEqual({ key: true, previous: true });
    expect(
      rotating.ready,
      `the server starts with the new and the previous key:\n${rotating.logs}`,
    ).toBe(true);
    good = { key: newKey, previous: oldKey };

    // 2. The rewrap run rotation.master_key, made due now.
    const due = psql(`UPDATE scheduler_run SET last_due_at = last_due_at - interval '2 days'
  WHERE kind = 'rotation.master_key' RETURNING kind;`);
    expect(due, 'scheduler_run row of rotation.master_key').toBe('rotation.master_key');
    const triggered = new Date(Date.now() - 1_000);
    await expect
      .poll(
        () =>
          psql(`SELECT COALESCE(last_error, 'ok') FROM scheduler_run
  WHERE kind = 'rotation.master_key' AND last_finished_at IS NOT NULL AND last_due_at > now() - interval '1 day';`),
        {
          message: 'the rewrap run rotation.master_key finished',
          timeout: 120_000,
          intervals: [3_000],
        },
      )
      .toBe('ok');
    const rewrapLog = serviceLogs('server', triggered);
    const rewrapped = /master key rewrap.*?rewrapped["=:\s]+(\d+)/.exec(rewrapLog);
    // At least the derivation root and the key of the collector CA moved to the new key.
    expect(Number(rewrapped?.[1] ?? -1), `rewrap run log:\n${rewrapLog}`).toBeGreaterThanOrEqual(2);

    // 3. The collector stayed online and its records arrive.
    expect(collector?.pid, 'the same collector process').toBe(pid);
    expect(collector?.exitCode ?? null, `collector exited:\n${collectorLog()}`).toBeNull();
    await heartbeatAfter(Date.now(), 'collector heartbeats after the rewrap run');
    await expect
      .poll(maxLastSeen, {
        message:
          'a discovery record of the running collector taken after the start of the rotation',
        timeout: 7 * 60_000,
        intervals: [10_000],
      })
      .toBeGreaterThan(rotationStart);
    await w.page.goto('/discovery');
    await expect(
      w.page
        .getByRole('heading', { level: 3, name: w.collectorName, exact: true })
        .locator('xpath=../..')
        .locator('.shrink-0'),
    ).toHaveText('Aktiv');

    // 4. The cursor of before the rotation returns the same page; an altered one is refused.
    const again = await api<ListPage<CI>>(
      w.rq,
      await token(),
      'GET',
      `${sorted}&cursor=${encodeURIComponent(cursor)}`,
    );
    expect(again.status, `cursor of before the rotation: ${JSON.stringify(again.body)}`).toBe(200);
    expect((again.body.data ?? []).map((ci) => ci.id)).toEqual(pageTwo.map((ci) => ci.id));
    const altered = `${cursor.slice(0, -2)}${cursor.endsWith('AA') ? 'BB' : 'AA'}`;
    const forged = await api(
      w.rq,
      await token(),
      'GET',
      `${sorted}&cursor=${encodeURIComponent(altered)}`,
    );
    expect(forged.status, 'an altered cursor (control)').toBe(400);

    // 5. The old key removed: the server starts without it.
    const retired = await restartServer({ RETICORA_MASTER_KEY_PREVIOUS: undefined });
    expect(retired.container.env.RETICORA_MASTER_KEY_PREVIOUS ?? '', 'no previous key').toBe('');
    expect(retired.ready, `the server starts with the new key alone:\n${retired.logs}`).toBe(true);
    good = { key: newKey };
    expect(retired.logs).not.toContain(DERIVATION_ROOT_FAILURE);
    expect(retired.logs, 'no new derivation root drawn').not.toContain(
      'created the derivation root',
    );
    await heartbeatAfter(Date.now(), 'collector heartbeats after the restart without the old key');
    expect(collector?.pid, 'the same collector process').toBe(pid);
    const last = await api<ListPage<CI>>(
      w.rq,
      await token(),
      'GET',
      `${sorted}&cursor=${encodeURIComponent(cursor)}`,
    );
    expect(last.status, 'cursor of before the rotation after the old key is gone').toBe(200);

    // 6. nats-edge kept running throughout.
    const edgeAfter = serviceContainer('nats-edge');
    expect(
      sameContainer(edgeBefore, edgeAfter) && edgeAfter.running,
      `nats-edge not restarted: before ${JSON.stringify({ ...edgeBefore, env: undefined })}, after ${JSON.stringify({ ...edgeAfter, env: undefined })}`,
    ).toBe(true);
  });

  test('K02 a server started with a wrong master key does not start and names the reason', async () => {
    test.setTimeout(10 * 60_000);
    const edgeBefore = serviceContainer('nats-edge');
    const wrong = await restartServer(
      { RETICORA_MASTER_KEY: masterKey(), RETICORA_MASTER_KEY_PREVIOUS: undefined },
      60_000,
    );
    expect(wrong.ready, 'a server with a wrong master key answers /readyz').toBe(false);
    expect(wrong.logs, 'the start names the reason').toContain(DERIVATION_ROOT_FAILURE);
    expect(wrong.logs, 'no new derivation root drawn over the stored one').not.toContain(
      'created the derivation root',
    );
    // The valid key again: the server starts.
    const valid = await restartServer({
      RETICORA_MASTER_KEY: good.key,
      RETICORA_MASTER_KEY_PREVIOUS: good.previous,
    });
    expect(valid.ready, `the server starts again with the valid key:\n${valid.logs}`).toBe(true);
    expect(
      sameContainer(edgeBefore, serviceContainer('nats-edge')),
      'nats-edge not restarted',
    ).toBe(true);
  });
});
