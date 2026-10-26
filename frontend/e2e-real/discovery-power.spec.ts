import { spawn, spawnSync, type ChildProcess } from 'node:child_process';
import { appendFileSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test, expect, type APIRequestContext, type BrowserContext } from '@playwright/test';
import { api, signupTenant, stamp, uiLogin, BASE, COLLECTOR_BIN, SERVER } from './support/stack';

/*
 * WP-498 (IMP-09, F-112; Retest WP-161) – power chain of the simulated fleet on the real stack:
 * the real collector binary (E2E_COLLECTOR_BIN) reports the simulated 30-device fleet
 * (RETICORA_SIMULATE=true) through the real ingest path (mTLS NATS edge → ingest lanes). The
 * fleet has 16 servers, each fed by sim-pdu-a (PSU1) and sim-pdu-b (PSU2). After every
 * discovery cycle all 32 powered_by edges server → PDU carry attributes.redundant = true, read
 * through GET /cis/{id}/relationships, whichever batch or lane each PDU arrived in. Before the
 * fix 0 to 16 of the 32 were redundant (F-112).
 *
 * "Discovery cycle" = start of the collector process: the simulator reports the fleet at start-up
 * with a new observation time. Cycle 2 restarts it.
 */

const SIMULATED_DEVICES = 30;
const SERVERS = 16;
const PDUS = ['sim-pdu-a', 'sim-pdu-b'];

interface CI {
  id: string;
  name: string;
  last_seen_at?: string;
}

interface Relationship {
  id: string;
  source_ci_id: string;
  target_ci_id: string;
  rel_type: string;
  source: string;
  attributes?: Record<string, unknown> | null;
}

interface List<T> {
  data: T[] | null;
  total: number;
}

let rq: APIRequestContext;
let ctx: BrowserContext;
let token: string;
let workDir: string;
let collector: ChildProcess | undefined;

/** A documentation address (198.18.0.0/15) so the signup has its own budget (E-33). */
function realIp(): string {
  return `198.18.${Math.floor(Math.random() * 250) + 1}.${Math.floor(Math.random() * 250) + 1}`;
}

async function ok<T>(method: 'GET' | 'POST', path: string, data?: unknown): Promise<T> {
  const res = await api<T>(rq, token, method, path, data);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeGreaterThanOrEqual(200);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeLessThan(300);
  return res.body;
}

async function fleet(): Promise<Map<string, CI>> {
  const list = await ok<List<CI>>('GET', '/cis?limit=100');
  return new Map(
    (list.data ?? []).filter((ci) => ci.name.startsWith('sim-')).map((ci) => [ci.name, ci]),
  );
}

/** startCollector runs the enrolled collector with the simulated fleet; returns the start time. */
function startCollector(): number {
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
      RETICORA_CREDENTIALS_PATH: join(workDir, 'credentials.json'),
      RETICORA_SPOOL_DIR: join(workDir, 'spool'),
      RETICORA_HEARTBEAT_INTERVAL: '10s',
      RETICORA_SPOOL_FLUSH_INTERVAL: '5s',
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  const log = join(workDir, 'collector.log');
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

/** waitCycle waits until every simulated CI was seen by the run that started at since. */
async function waitCycle(since: number, message: string) {
  // last_seen_at has whole seconds; a run starts on a fresh second (see below).
  const second = Math.floor(since / 1000) * 1000;
  await expect
    .poll(
      async () =>
        [...(await fleet()).values()].filter(
          (ci) => ci.last_seen_at && Date.parse(ci.last_seen_at) >= second,
        ).length,
      { message, timeout: 180_000, intervals: [3_000] },
    )
    .toBe(SIMULATED_DEVICES);
}

/**
 * serverFeeds counts the powered_by edges server → PDU of the 16 simulated servers and how many
 * of them carry attributes.redundant = true.
 */
async function serverFeeds(): Promise<{ edges: number; redundant: number }> {
  const all = await fleet();
  const pdus = new Set(PDUS.map((name) => all.get(name)?.id).filter(Boolean));
  const servers = [...all.values()].filter((ci) => ci.name.startsWith('sim-srv-'));
  expect(servers.length, 'simulated servers').toBe(SERVERS);
  let edges = 0;
  let redundant = 0;
  for (const srv of servers) {
    const rels = (await ok<List<Relationship>>('GET', `/cis/${srv.id}/relationships?limit=100`))
      .data;
    for (const rel of rels ?? []) {
      if (rel.rel_type !== 'powered_by' || rel.source_ci_id !== srv.id) continue;
      if (!pdus.has(rel.target_ci_id)) continue;
      edges++;
      if (rel.attributes?.redundant === true) redundant++;
    }
  }
  return { edges, redundant };
}

async function expectAllRedundant(cycle: string) {
  await expect
    .poll(serverFeeds, {
      message: `${cycle}: powered_by server → PDU edges with attributes.redundant=true`,
      timeout: 120_000,
      intervals: [3_000],
    })
    .toEqual({ edges: 2 * SERVERS, redundant: 2 * SERVERS });
}

test.describe('Power chain of the simulated fleet (WP-498, IMP-09)', () => {
  test.beforeAll(async ({ browser, playwright }) => {
    test.setTimeout(5 * 60_000);
    expect(COLLECTOR_BIN, 'E2E_COLLECTOR_BIN (collector binary)').toBeTruthy();
    rq = await playwright.request.newContext();
    ctx = await browser.newContext({
      baseURL: BASE,
      locale: 'de-DE',
      extraHTTPHeaders: { 'X-Real-IP': realIp() },
    });
    const page = await ctx.newPage();
    const account = await signupTenant(page, rq, 'power');
    token = await uiLogin(page, account);
    workDir = mkdtempSync(join(tmpdir(), 'reticora-e2e-power-'));

    const name = `power-collector-${stamp()}`;
    const minted = await ok<{ code: string }>('POST', '/collectors/enrollment-codes', {
      label: name,
    });
    const enrolled = spawnSync(
      COLLECTOR_BIN,
      [
        'enroll',
        '--server',
        SERVER,
        '--code',
        minted.code,
        '--name',
        name,
        '--credentials',
        join(workDir, 'credentials.json'),
      ],
      { encoding: 'utf8', timeout: 60_000 },
    );
    expect(enrolled.status, `collector enroll: ${enrolled.stderr}${enrolled.stdout}`).toBe(0);
  });

  test.afterAll(async () => {
    await stopCollector();
    await ctx?.close();
    await rq?.dispose();
    if (workDir) rmSync(workDir, { recursive: true, force: true });
  });

  test('P01 after each of two discovery cycles all 32 server → PDU edges are redundant (F-112)', async () => {
    test.setTimeout(10 * 60_000);

    // Cycle 1: the first report of the fleet.
    const first = startCollector();
    await waitCycle(first, 'cycle 1: every simulated CI reported');
    await expectAllRedundant('cycle 1');

    // Cycle 2: the fleet again, with a new observation time, starting on a fresh second.
    await stopCollector();
    await new Promise((resolve) => setTimeout(resolve, 2_000 - (Date.now() % 1000)));
    const second = startCollector();
    await waitCycle(second, 'cycle 2: every simulated CI reported again');
    await expectAllRedundant('cycle 2');
  });
});
