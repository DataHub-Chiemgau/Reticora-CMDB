import { spawn, spawnSync, type ChildProcess } from 'node:child_process';
import { appendFileSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import { COLLECTOR_BIN, SERVER, stamp } from './support/stack';
import {
  dbRows,
  ok,
  openTenant,
  rows,
  uuid,
  type List,
  type Tenant,
} from './support/locations-racks';

/*
 * Phase-1 acceptance audit – "Entfernen und unterdrücken" of a discovery edge, end to end on
 * the real stack (nothing mocked): UI → REST API → PostgreSQL → discovery → API, with the real
 * collector binary (E2E_COLLECTOR_BIN) reporting the simulated 30-device fleet
 * (RETICORA_SIMULATE=true, SIM-02) through the real ingest path, started as in
 * discovery-override.spec.ts.
 *
 * One organization is created through the self-signup UI; its collector is enrolled with a
 * code minted in the Discovery UI. "Next discovery cycle" = restart the collector process:
 * the simulator reports the fleet at start-up with a new observation time, which every CI it
 * reports carries as last_seen_at (REC-07), and re-confirms every discovery edge it sees again
 * (last_confirmed_at, TOP-02).
 *
 * The CI is sim-access-01. Its LLDP neighbors in the simulator
 * (collector/plugins/simulate/simulate.go) give seven connected_to edges of source discovery:
 * sim-access-01 → sim-core-01 and → sim-core-02 (its uplinks), sim-srv-01/05/09/13 and
 * sim-nas-01 → sim-access-01 (their uplinks).
 *
 * The topology is read after the first cycle, whichever ingest lanes the devices go through:
 * a reporting device stores its LLDP neighbors, and a CI created later takes the edges the
 * devices processed before it reported to it (WP-645, F-374; TOP-01, NFR-03).
 *
 * Test → WP (requirement IDs) proven:
 *  R01 the edge sim-access-01 → sim-core-01 is removed in the CI detail with "Entfernen und
 *      unterdrücken" (CIRelationshipsCard, DELETE /relationships/{id}?force=true → 204); GET
 *      /cis/{id}/relationships no longer lists it and relationship_suppression holds
 *      (source, target, connected_to) of the organization. After the next discovery cycle
 *      (last_seen_at of the reported CIs risen) the edge is not created again (API, UI after
 *      reload), the six other LLDP edges of the CI are the same edges, re-confirmed in this
 *      cycle (positive control: the cycle did derive the topology of sim-access-01), and the
 *      suppression is still there .................................... WP-160, WP-543 (REL-03, REL-04)
 *      precondition: the seven LLDP edges of the CI after the first cycle ........ WP-645 (TOP-01)
 *
 * The confirmation dialog, the reason and lifting a suppression are not part of this spec
 * (WP-584). A failing test is a product finding: the assertions encode REL-03/REL-04 of
 * docs/spec/katalog-v3 and the acceptance criteria of WP-543, not the observed behaviour.
 */

const SIMULATED_DEVICES = 30;
const CI_NAME = 'sim-access-01';
const REMOVED_PEER = 'sim-core-01';
/** The LLDP edges of sim-access-01 the simulator reports, as source → target names. */
const LLDP_EDGES: [string, string][] = [
  [CI_NAME, 'sim-core-01'],
  [CI_NAME, 'sim-core-02'],
  ['sim-srv-01', CI_NAME],
  ['sim-srv-05', CI_NAME],
  ['sim-srv-09', CI_NAME],
  ['sim-srv-13', CI_NAME],
  ['sim-nas-01', CI_NAME],
];

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
  projected?: boolean;
  last_confirmed_at?: string;
}

interface Collector {
  id: string;
  name: string;
}

interface Suppression {
  organization_id: string;
  source_ci_id: string;
  target_ci_id: string;
  rel_type: string;
}

let t: Tenant;
let rq: APIRequestContext;
let workDir = '';
let collector: ChildProcess | undefined;

function credentialsPath() {
  return join(workDir, 'credentials.json');
}

function enroll(code: string, name: string) {
  return spawnSync(
    COLLECTOR_BIN,
    [
      'enroll',
      '--server',
      SERVER,
      '--code',
      code,
      '--name',
      name,
      '--credentials',
      credentialsPath(),
    ],
    { encoding: 'utf8', timeout: 60_000 },
  );
}

/** startCollector runs the enrolled collector with the simulated fleet (discovery-override.spec.ts). */
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
      RETICORA_CREDENTIALS_PATH: credentialsPath(),
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

/**
 * nextCycle = "run discovery again"; returns the start of the cycle on a whole second
 * (last_seen_at has whole seconds), so every sighting at or after it is this cycle's.
 */
async function nextCycle(): Promise<number> {
  await stopCollector();
  await new Promise((resolve) => setTimeout(resolve, 2_000 - (Date.now() % 1000)));
  return Math.floor(startCollector() / 1000) * 1000;
}

function get<T>(path: string): Promise<T> {
  return ok<T>(rq, t.token, 'GET', path);
}

async function fleet(): Promise<Map<string, CI>> {
  const list = await get<List<CI>>('/cis?limit=100');
  return new Map(rows(list).map((ci) => [ci.name, ci]));
}

/** lldpEdges are the connected_to edges of source discovery of a CI (GET /cis/{id}/relationships). */
async function lldpEdges(ciId: string): Promise<Relationship[]> {
  const list = await get<List<Relationship>>(`/cis/${ciId}/relationships?limit=100`);
  return rows(list).filter(
    (r) => !r.projected && r.rel_type === 'connected_to' && r.source === 'discovery',
  );
}

/** edgeKeys names the edges by their CI names, sorted. */
function edgeKeys(edges: Relationship[], names: Map<string, string>): string[] {
  return edges
    .map(
      (r) =>
        `${names.get(r.source_ci_id) ?? r.source_ci_id} → ${names.get(r.target_ci_id) ?? r.target_ci_id}`,
    )
    .sort();
}

/** suppressions are the rows of relationship_suppression between two CIs (database probe). */
function suppressions(sourceId: string, targetId: string): Suppression[] {
  return dbRows<Suppression>(`SELECT s.organization_id::text, s.source_ci_id::text, s.target_ci_id::text, rt.key AS rel_type
    FROM relationship_suppression s JOIN relationship_type rt ON rt.id = s.relationship_type_id
    WHERE s.source_ci_id = '${uuid(sourceId)}' AND s.target_ci_id = '${uuid(targetId)}'`);
}

/** relationshipRow is the line of the relationships card that links the other CI. */
function relationshipRow(page: Page, otherId: string) {
  return page.locator('li').filter({ has: page.getByRole('link', { name: otherId, exact: true }) });
}

async function openCI(page: Page, ci: CI) {
  await page.goto(`/cmdb/${ci.id}`);
  await expect(page.getByRole('heading', { level: 2, name: ci.name })).toBeVisible();
}

test.describe('Relationships: remove and suppress a discovery edge (REL-03, REL-04)', () => {
  test.beforeAll(async ({ browser, playwright }) => {
    test.setTimeout(5 * 60_000);
    expect(COLLECTOR_BIN, 'E2E_COLLECTOR_BIN (collector binary)').toBeTruthy();
    rq = await playwright.request.newContext();
    t = await openTenant(browser, rq, 'suppress');
    workDir = mkdtempSync(join(tmpdir(), 'reticora-e2e-suppression-'));
    const collectorName = `audit-suppression-${stamp()}`;

    // The enrollment code is minted in the Discovery UI ("Collector verbinden").
    const { page } = t;
    await page.goto('/discovery');
    await page.getByRole('button', { name: 'Collector verbinden' }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByLabel('Bezeichnung (z. B. Standort)').fill(collectorName);
    await dialog.getByRole('button', { name: 'Code erzeugen' }).click();
    const command = (await dialog.locator('pre').textContent()) ?? '';
    const code = /--code\s+(\S+)/.exec(command)?.[1] ?? '';
    expect(code, `enrollment command shown in the UI: ${command}`).not.toBe('');
    await dialog.getByRole('button', { name: 'Fertig' }).click();

    const enrolled = enroll(code, collectorName);
    expect(enrolled.status, `collector enroll: ${enrolled.stderr}${enrolled.stdout}`).toBe(0);
    const collectors = rows(await get<List<Collector>>('/collectors?limit=100'));
    expect(
      collectors.map((c) => c.name),
      'the enrolled collector is registered',
    ).toEqual([collectorName]);

    startCollector();
    await expect
      .poll(async () => (await fleet()).size, {
        message: 'CIs of the simulated fleet',
        timeout: 180_000,
        intervals: [3_000],
      })
      .toBeGreaterThanOrEqual(SIMULATED_DEVICES);
  });

  test.afterAll(async () => {
    await stopCollector();
    if (workDir) rmSync(workDir, { recursive: true, force: true });
    await t?.page.context().close();
    await rq?.dispose();
  });

  test('R01 an LLDP edge removed with "Entfernen und unterdrücken" is suppressed and not recreated by the next discovery cycle', async () => {
    test.setTimeout(8 * 60_000);
    const { page } = t;
    const all = await fleet();
    const names = new Map([...all.values()].map((ci) => [ci.id, ci.name]));
    const byName = (name: string) => {
      const ci = all.get(name);
      expect(ci, `simulated CI ${name}`).toBeTruthy();
      return ci!;
    };
    const access = byName(CI_NAME);
    const core = byName(REMOVED_PEER);
    const expected = LLDP_EDGES.map(([s, d]) => `${s} → ${d}`).sort();

    // Precondition: the topology of the first cycle holds the seven LLDP edges of the CI.
    await expect
      .poll(async () => edgeKeys(await lldpEdges(access.id), names), {
        message: `LLDP edges of ${CI_NAME} after the first discovery cycle`,
        timeout: 120_000,
        intervals: [2_000],
      })
      .toEqual(expected);
    const before = await lldpEdges(access.id);
    const removed = before.find((r) => r.source_ci_id === access.id && r.target_ci_id === core.id);
    expect(removed, `edge ${CI_NAME} → ${REMOVED_PEER}`).toBeTruthy();
    const kept = before.filter((r) => r.id !== removed!.id);
    expect(suppressions(access.id, core.id), 'no suppression before the removal').toEqual([]);

    await test.step('REL-04: "Entfernen und unterdrücken" in the CI detail deletes the edge with force', async () => {
      await openCI(page, access);
      const row = relationshipRow(page, core.id);
      await expect(row, `relationship line of ${REMOVED_PEER}`).toHaveCount(1);
      await expect(row.getByText('connected_to', { exact: true })).toBeVisible();
      const button = row.getByRole('button', { name: 'Entfernen und unterdrücken', exact: true });
      await expect(button).toBeVisible();
      const deleted = page.waitForResponse(
        (r) =>
          r.request().method() === 'DELETE' &&
          new URL(r.url()).pathname === `/api/v1/relationships/${removed!.id}`,
      );
      await button.click();
      const response = await deleted;
      expect(new URL(response.url()).searchParams.get('force'), 'DELETE with force=true').toBe(
        'true',
      );
      expect(response.status(), 'DELETE /relationships/{id}?force=true').toBe(204);
      // The card drops the line; the other edges stay listed.
      await expect(relationshipRow(page, core.id)).toHaveCount(0);
      for (const r of kept) {
        const other = r.source_ci_id === access.id ? r.target_ci_id : r.source_ci_id;
        await expect(relationshipRow(page, other), `line of ${names.get(other)}`).toHaveCount(1);
      }
    });

    await test.step('REL-03: the edge is gone (API) and the suppression is stored', async () => {
      const after = await lldpEdges(access.id);
      expect(
        after.map((r) => r.id),
        'GET /cis/{id}/relationships after the removal',
      ).not.toContain(removed!.id);
      expect(edgeKeys(after, names)).toEqual(
        expected.filter((k) => k !== `${CI_NAME} → ${REMOVED_PEER}`),
      );
      const all = await get<List<Relationship>>(`/cis/${access.id}/relationships?limit=100`);
      expect(
        rows(all).filter(
          (r) =>
            r.source_ci_id === access.id &&
            r.target_ci_id === core.id &&
            r.rel_type === 'connected_to',
        ),
        `no connected_to edge ${CI_NAME} → ${REMOVED_PEER} of any source`,
      ).toEqual([]);
      expect(suppressions(access.id, core.id), 'relationship_suppression').toEqual([
        {
          organization_id: t.orgId,
          source_ci_id: access.id,
          target_ci_id: core.id,
          rel_type: 'connected_to',
        },
      ]);
    });

    await test.step('REL-03: the next discovery cycle does not create the edge again', async () => {
      const seenBefore = new Map(
        [...all.values()].map((ci) => [ci.id, Date.parse(ci.last_seen_at ?? '') || 0]),
      );
      const reported = [...new Set(LLDP_EDGES.flat())].map(byName);
      const cycle = await nextCycle();

      // The cycle ran: every CI of the edges was seen again (last_seen_at risen).
      await expect
        .poll(
          async () => {
            const now = await fleet();
            return reported
              .filter((ci) => {
                const seen = Date.parse(now.get(ci.name)?.last_seen_at ?? '') || 0;
                return seen >= cycle && seen > (seenBefore.get(ci.id) ?? 0);
              })
              .map((ci) => ci.name)
              .sort();
          },
          {
            message: 'CIs seen again by the next discovery cycle (last_seen_at)',
            timeout: 120_000,
            intervals: [2_000],
          },
        )
        .toEqual(reported.map((ci) => ci.name).sort());

      // Positive control: the cycle derived the topology of the CI – the six other LLDP
      // edges are the same edges, re-confirmed in this cycle.
      await expect
        .poll(
          async () =>
            edgeKeys(
              (await lldpEdges(access.id)).filter(
                (r) =>
                  kept.some((k) => k.id === r.id) &&
                  (Date.parse(r.last_confirmed_at ?? '') || 0) >= cycle,
              ),
              names,
            ),
          {
            message: `LLDP edges of ${CI_NAME} re-confirmed by the next cycle (last_confirmed_at)`,
            timeout: 120_000,
            intervals: [2_000],
          },
        )
        .toEqual(edgeKeys(kept, names));

      const after = await lldpEdges(access.id);
      expect(
        edgeKeys(after, names),
        `LLDP edges of ${CI_NAME} after the next cycle: ${CI_NAME} → ${REMOVED_PEER} not recreated`,
      ).toEqual(edgeKeys(kept, names));
      expect(
        after.map((r) => r.id).sort(),
        'the other edges are the same edges (not recreated)',
      ).toEqual(kept.map((r) => r.id).sort());
      const any = await get<List<Relationship>>(`/cis/${access.id}/relationships?limit=100`);
      expect(
        rows(any).filter(
          (r) =>
            r.source_ci_id === access.id &&
            r.target_ci_id === core.id &&
            r.rel_type === 'connected_to',
        ),
        `no connected_to edge ${CI_NAME} → ${REMOVED_PEER} after the next cycle`,
      ).toEqual([]);
      expect(suppressions(access.id, core.id), 'relationship_suppression after the cycle').toEqual([
        {
          organization_id: t.orgId,
          source_ci_id: access.id,
          target_ci_id: core.id,
          rel_type: 'connected_to',
        },
      ]);

      // The CI detail shows the same after a reload.
      await openCI(page, access);
      for (const r of kept) {
        const other = r.source_ci_id === access.id ? r.target_ci_id : r.source_ci_id;
        await expect(relationshipRow(page, other), `line of ${names.get(other)}`).toHaveCount(1);
      }
      await expect(relationshipRow(page, core.id)).toHaveCount(0);
    });
  });
});
