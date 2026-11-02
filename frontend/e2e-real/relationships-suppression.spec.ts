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
 *      "Entfernen und unterdrücken" deletes only after the confirmation dialog with a reason
 *      (no DELETE before, the confirmation is disabled without a reason); the DELETE carries
 *      the reason, relationship_suppression stores it with the acting user (created_by) and
 *      the audit log holds relationship.suppression_created with actor and reason; GET
 *      /relationship-suppressions?ci_id= and the CI detail ("Unterdrückte Beziehungen
 *      anzeigen") list the suppression with reason and actor. "Aufheben" lifts it (DELETE
 *      /relationship-suppressions/{id} → 204, relationship.suppression_lifted audited, the
 *      row is gone), and the following discovery cycle creates the edge again (a new
 *      discovery edge, shown in the CI detail) ............................ WP-584 (REL-03, REL-04)
 *
 * A failing test is a product finding: the assertions encode REL-03/REL-04 of
 * docs/spec/katalog-v3 and the acceptance criteria of WP-543 and WP-584, not the observed
 * behaviour.
 */

const SIMULATED_DEVICES = 30;
const CI_NAME = 'sim-access-01';
const REMOVED_PEER = 'sim-core-01';
/** The reason entered in the confirmation (WP-584). */
const REASON = 'E2E: alter LLDP-Nachbar & Umbau';
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

/** A row of relationship_suppression with its actor and reason (WP-584). */
interface SuppressionRow extends Suppression {
  id: string;
  reason: string | null;
  created_by: string | null;
}

/** An item of GET /relationship-suppressions (WP-584). */
interface ListedSuppression {
  id: string;
  source_ci_id: string;
  target_ci_id: string;
  rel_type: string;
  reason?: string;
  created_by?: string;
  created_at: string;
}

interface AuditRow {
  actor_id: string | null;
  reason: string | null;
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

/** suppressionRows are the rows of relationship_suppression between two CIs with actor and reason. */
function suppressionRows(sourceId: string, targetId: string): SuppressionRow[] {
  return dbRows<SuppressionRow>(`SELECT s.id::text, s.organization_id::text, s.source_ci_id::text,
      s.target_ci_id::text, rt.key AS rel_type, s.reason, s.created_by::text
    FROM relationship_suppression s JOIN relationship_type rt ON rt.id = s.relationship_type_id
    WHERE s.source_ci_id = '${uuid(sourceId)}' AND s.target_ci_id = '${uuid(targetId)}'`);
}

/** auditOf are the audit entries of an action on a suppression (database probe). */
function auditOf(action: string, suppressionId: string): AuditRow[] {
  return dbRows<AuditRow>(`SELECT actor_id::text, changes->>'reason' AS reason FROM audit_log
    WHERE action = '${action}' AND resource_type = 'relationship_suppression'
      AND resource_id = '${uuid(suppressionId)}'`);
}

/**
 * relationshipRow is the line of the relationships list of the card that links the other CI;
 * the list of suppressions (WP-584) is a list of its own.
 */
function relationshipRow(page: Page, otherId: string) {
  return page
    .getByRole('list', { name: 'Beziehungen', exact: true })
    .getByRole('listitem')
    .filter({ has: page.getByRole('link', { name: otherId, exact: true }) });
}

/** suppressionRow is the line of the shown suppressions of the CI that links the other CI. */
function suppressionRow(page: Page, otherId: string) {
  return page
    .getByRole('list', { name: 'Unterdrückte Beziehungen', exact: true })
    .getByRole('listitem')
    .filter({ has: page.getByRole('link', { name: otherId, exact: true }) });
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
    test.setTimeout(12 * 60_000);
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

    await test.step('REL-04: "Entfernen und unterdrücken" in the CI detail deletes the edge with force after the confirmation with a reason', async () => {
      await openCI(page, access);
      const row = relationshipRow(page, core.id);
      await expect(row, `relationship line of ${REMOVED_PEER}`).toHaveCount(1);
      await expect(row.getByText('connected_to', { exact: true })).toBeVisible();
      const button = row.getByRole('button', { name: 'Entfernen und unterdrücken', exact: true });
      await expect(button).toBeVisible();
      // WP-584: no DELETE leaves the page before the confirmation.
      const deletes: string[] = [];
      const track = (request: { method(): string; url(): string }) => {
        if (request.method() === 'DELETE') deletes.push(request.url());
      };
      page.on('request', track);
      await button.click();
      const dialog = page.getByRole('dialog');
      await expect(dialog, 'confirmation dialog').toBeVisible();
      await expect(dialog).toContainText(core.id);
      const confirm = dialog.getByRole('button', { name: 'Entfernen und unterdrücken' });
      await expect(confirm, 'confirmation without a reason').toBeDisabled();
      // Cancelling deletes nothing.
      await dialog.getByRole('button', { name: 'Abbrechen' }).click();
      await expect(dialog).toBeHidden();
      await expect(relationshipRow(page, core.id)).toHaveCount(1);
      expect(deletes, 'no DELETE before the confirmation').toEqual([]);

      await button.click();
      await expect(dialog).toBeVisible();
      await dialog.getByLabel('Grund').fill(REASON);
      await expect(confirm).toBeEnabled();
      const deleted = page.waitForResponse(
        (r) =>
          r.request().method() === 'DELETE' &&
          new URL(r.url()).pathname === `/api/v1/relationships/${removed!.id}`,
      );
      await confirm.click();
      const response = await deleted;
      page.off('request', track);
      expect(new URL(response.url()).searchParams.get('force'), 'DELETE with force=true').toBe(
        'true',
      );
      expect(new URL(response.url()).searchParams.get('reason'), 'DELETE with the reason').toBe(
        REASON,
      );
      expect(response.status(), 'DELETE /relationships/{id}?force=true').toBe(204);
      await expect(dialog).toBeHidden();
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

    let suppressionId = '';
    await test.step('WP-584: the suppression holds the reason and the actor, is audited and listed (API, UI)', async () => {
      const [row] = suppressionRows(access.id, core.id);
      expect(row, 'relationship_suppression row').toBeTruthy();
      expect({ reason: row!.reason, created_by: row!.created_by }, 'reason and actor').toEqual({
        reason: REASON,
        created_by: t.userId,
      });
      suppressionId = row!.id;
      expect(
        auditOf('relationship.suppression_created', suppressionId),
        'audit relationship.suppression_created',
      ).toEqual([{ actor_id: t.userId, reason: REASON }]);

      const listed = rows(
        await get<List<ListedSuppression>>(`/relationship-suppressions?ci_id=${access.id}`),
      );
      expect(
        listed.map((s) => ({
          id: s.id,
          source_ci_id: s.source_ci_id,
          target_ci_id: s.target_ci_id,
          rel_type: s.rel_type,
          reason: s.reason,
          created_by: s.created_by,
        })),
        'GET /relationship-suppressions?ci_id=',
      ).toEqual([
        {
          id: suppressionId,
          source_ci_id: access.id,
          target_ci_id: core.id,
          rel_type: 'connected_to',
          reason: REASON,
          created_by: t.userId,
        },
      ]);

      await page.getByRole('button', { name: 'Unterdrückte Beziehungen anzeigen' }).click();
      const line = suppressionRow(page, core.id);
      await expect(line, `suppression line of ${REMOVED_PEER}`).toHaveCount(1);
      await expect(line.getByText('connected_to', { exact: true })).toBeVisible();
      await expect(line).toContainText(`Grund: ${REASON}`);
      await expect(line).toContainText(`Unterdrückt von ${t.userId}`);
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

    await test.step('WP-584: "Aufheben" lifts the suppression; the following discovery cycle creates the edge again', async () => {
      await openCI(page, access);
      await page.getByRole('button', { name: 'Unterdrückte Beziehungen anzeigen' }).click();
      const line = suppressionRow(page, core.id);
      await expect(line, `suppression line of ${REMOVED_PEER}`).toHaveCount(1);
      const lifted = page.waitForResponse(
        (r) =>
          r.request().method() === 'DELETE' &&
          new URL(r.url()).pathname === `/api/v1/relationship-suppressions/${suppressionId}`,
      );
      await line.getByRole('button', { name: 'Aufheben', exact: true }).click();
      expect((await lifted).status(), 'DELETE /relationship-suppressions/{id}').toBe(204);
      await expect(page.getByText('Keine unterdrückten Beziehungen.')).toBeVisible();
      expect(suppressions(access.id, core.id), 'relationship_suppression after lifting').toEqual(
        [],
      );
      expect(
        rows(await get<List<ListedSuppression>>(`/relationship-suppressions?ci_id=${access.id}`)),
        'GET /relationship-suppressions?ci_id= after lifting',
      ).toEqual([]);
      expect(
        auditOf('relationship.suppression_lifted', suppressionId),
        'audit relationship.suppression_lifted',
      ).toEqual([{ actor_id: t.userId, reason: REASON }]);

      const cycle = await nextCycle();
      await expect
        .poll(
          async () =>
            (await lldpEdges(access.id))
              .filter(
                (r) =>
                  r.source_ci_id === access.id &&
                  r.target_ci_id === core.id &&
                  (Date.parse(r.last_confirmed_at ?? '') || 0) >= cycle,
              )
              .map((r) => r.id !== removed!.id),
          {
            message: `edge ${CI_NAME} → ${REMOVED_PEER} created again by the cycle after lifting`,
            timeout: 120_000,
            intervals: [2_000],
          },
        )
        .toEqual([true]);
      await expect
        .poll(async () => edgeKeys(await lldpEdges(access.id), names), {
          message: `LLDP edges of ${CI_NAME} after lifting`,
          timeout: 60_000,
          intervals: [2_000],
        })
        .toEqual(expected);

      // The CI detail lists the edge again after a reload.
      await openCI(page, access);
      await expect(relationshipRow(page, core.id), `line of ${REMOVED_PEER}`).toHaveCount(1);
    });
  });
});
