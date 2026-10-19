import {
  test,
  expect,
  type APIRequestContext,
  type Browser,
  type BrowserContext,
  type Page,
  type Response,
} from '@playwright/test';
import { createServer, type Server } from 'node:http';
import { readFile } from 'node:fs/promises';
import type { AddressInfo } from 'node:net';
import { api, signupTenant, stamp, uiLogin, BASE, SERVER, type Account } from './support/stack';

/*
 * Phase-1 acceptance audit – bulk operations, jobs, job center, notifications, saved views,
 * export and webhook egress protection, end to end on the real stack (nothing mocked):
 * UI → REST API → PostgreSQL → UI.
 *
 * Tenants are created through the self-signup UI: tenant A (main) and tenant B (foreign data
 * for the isolation checks) in beforeAll, a third tenant inside J01 because a trial tenant
 * holds at most 100 CIs and the job path needs 100 ids. Every test creates its own CIs with
 * unique names; fixtures are created through the REST API the UI uses (POST /api/v1/cis).
 *
 * Test → WP (requirement IDs) proven:
 *  B01 bulk status change in the CMDB list: one request, report, reload, history, audit
 *      ...................................... WP-181 (API-08, BLK-01), WP-171 (AUD-03, CI-06), WP-133 (AUD-05)
 *  B02 bulk with one invalid item (CI deleted meanwhile): per-item error in the UI, the
 *      others applied, nothing lost silently ...................... WP-181 (API-08, BLK-01)
 *  B03 bulk limits and validation (0 / 501 ids, unknown status/action, duplicates, CI of
 *      another tenant) ........................... WP-181 (API-08, BLK-01), WP-015 (TEN-06)
 *  B04 a malformed id is reported with the status the single change answers (400, not 500)
 *      ....................................................................... WP-181 (API-08)
 *  J01 100 ids run as job: 202 + Location, job center follows it to "Erfolgreich", result
 *      with per-id errors, persistence after reload, history, cancel/revert 409, job
 *      metrics, foreign tenant 404 ....... WP-181 (API-08, BLK-01), WP-182 (API-09, JOB-03),
 *      WP-197 (UI-16), WP-129 (JOB-01), WP-171 (AUD-03)
 *  V01 saved view created from the saved-views page, persisted across reload, filter spec
 *      stored and re-run identically, private to the tenant, deleted in the UI
 *      ......................................... WP-188 (VIE-01), WP-187 (SRC-03), WP-015 (TEN-06)
 *  E01 CSV export from the export page: 202 job, completed, manifest, download file (BOM,
 *      semicolon) equals the API data row by row, only the tenant's CIs, presign 15 min,
 *      export listed in the job center, foreign tenant 404
 *      .... WP-189 (EXP-01), WP-035 (EXP-01, TEN-06), WP-182 (API-09), WP-201 (TST-03)
 *  N01 export finished → in-app notification in the notification center, mark read,
 *      persisted, not visible to another tenant ...... WP-140 (NTF-05), WP-139 (NTF-04),
 *      WP-197 (UI-16)
 *  W01 webhook to a local catcher (127.0.0.1) in the webhooks page is refused by the egress
 *      protection, not stored, nothing delivered, the refusal is explained in the UI
 *      ..................................................... WP-049 (SEC-08), WP-191/192 (UI-01..10)
 *  W02 egress matrix of the API: private, loopback, link-local/metadata, IPv6 loopback/ULA,
 *      *.internal, non-http scheme refused with an explanation; a public host is accepted
 *      ........................................................................ WP-049 (SEC-08)
 *
 * Notes: the CMDB list selects at most the 25 rows of its visible page, so a bulk job
 * (≥ 100 ids) cannot be started from the UI; J01 starts it through the endpoint the UI uses
 * (POST /api/v1/cis/bulk) and follows it in the job center UI. A fresh tenant has no
 * notification channel and the UI offers no page to create one; N01 creates the in-app
 * channel through REST (POST /api/v1/notification-channels).
 *
 * A failing test is a product finding: the assertions encode the requirement texts in
 * docs/spec/katalog-v3 and the WP acceptance criteria, not the observed behaviour.
 */

interface CI {
  id: string;
  organization_id: string;
  ci_type_id: string;
  name: string;
  status: string;
  manufacturer?: string;
  model?: string;
  serial_number?: string;
  management_ip?: string;
  firmware_version?: string;
  discovery_source?: string;
  created_at: string;
  updated_at: string;
}

interface List<T> {
  data: T[] | null;
  total: number;
  has_more?: boolean;
  next_cursor?: string;
}

interface BulkItem {
  id: string;
  result: 'ok' | 'error';
  code: number;
  error?: string;
}

interface BulkReport {
  action: string;
  total: number;
  succeeded: number;
  failed: number;
  items: BulkItem[];
}

interface Job {
  id: string;
  organization_id: string;
  kind: string;
  status: string;
  progress: number;
  created_by?: string;
  ref_id?: string;
  started_at?: string;
  finished_at?: string;
}

interface ExportJob {
  id: string;
  status: string;
  row_count?: number;
  initiated_by?: string;
  download_url?: string;
  manifest?: { format: string; fields: string[]; omitted: { field: string; reason: string }[] };
}

interface Change {
  ci_id: string;
  actor_id?: string;
  change_type: string;
  field_name?: string;
  old_value?: unknown;
  new_value?: unknown;
}

interface AuditEntry {
  action: string;
  actor_id?: string;
  resource_id?: string;
  changes?: { fields?: { field: string; old_value?: unknown; new_value?: unknown }[] };
}

interface SavedView {
  id: string;
  name: string;
  owner_id?: string;
  entity_kind: string;
  filter_spec: Record<string, unknown>;
  shared: boolean;
}

interface Notification {
  id: string;
  category: string;
  subject: string;
  body: string;
  data?: Record<string, unknown>;
  read_at?: string;
}

interface Tenant {
  ctx: BrowserContext;
  page: Page;
  account: Account;
  token: string;
  orgId: string;
  userId: string;
  serverType: string;
}

let rq: APIRequestContext;
let a: Tenant;
let b: Tenant;
/** foreign is a CI of tenant B that tenant A must never see or change. */
let foreign: CI;
const s = stamp();

function rows<T>(list: List<T> | undefined | null): T[] {
  return list?.data ?? [];
}

function claims(token: string): { sub: string; org: string } {
  return JSON.parse(Buffer.from(token.split('.')[1], 'base64url').toString()) as {
    sub: string;
    org: string;
  };
}

/** A documentation address (198.18.0.0/15) so each signup has its own budget (E-33). */
function realIp(): string {
  return `198.18.${Math.floor(Math.random() * 250) + 1}.${Math.floor(Math.random() * 250) + 1}`;
}

/** ok asserts a 2xx answer of a setup call and returns its body. */
async function ok<T>(
  t: Tenant,
  method: 'GET' | 'POST' | 'PATCH' | 'DELETE',
  path: string,
  data?: unknown,
): Promise<T> {
  const res = await api<T>(rq, t.token, method, path, data);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeGreaterThanOrEqual(200);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeLessThan(300);
  return res.body;
}

async function newCI(t: Tenant, body: Record<string, unknown>): Promise<CI> {
  return ok<CI>(t, 'POST', '/cis', { ci_type_id: t.serverType, ...body });
}

async function getCI(t: Tenant, id: string): Promise<CI> {
  return ok<CI>(t, 'GET', `/cis/${id}`);
}

/** allCIs pages through the CI list of a tenant. */
async function allCIs(t: Tenant): Promise<CI[]> {
  const out: CI[] = [];
  let cursor = '';
  for (let i = 0; i < 20; i++) {
    const page = await ok<List<CI>>(
      t,
      'GET',
      `/cis?limit=200${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`,
    );
    out.push(...rows(page));
    if (!page.has_more || !page.next_cursor) break;
    cursor = page.next_cursor;
  }
  return out;
}

async function openTenant(browser: Browser, label: string): Promise<Tenant> {
  const ctx = await browser.newContext({
    baseURL: BASE,
    locale: 'de-DE',
    acceptDownloads: true,
    extraHTTPHeaders: { 'X-Real-IP': realIp() },
  });
  const page = await ctx.newPage();
  const account = await signupTenant(page, rq, label);
  const token = await uiLogin(page, account);
  const c = claims(token);
  const t: Tenant = {
    ctx,
    page,
    account,
    token,
    orgId: c.org,
    userId: c.sub,
    serverType: '',
  };
  const types = await ok<List<{ id: string; key: string }>>(t, 'GET', '/ci-types?limit=200');
  t.serverType = rows(types).find((ty) => ty.key === 'server')?.id ?? '';
  expect(t.serverType, 'catalog type server').not.toBe('');
  return t;
}

function waitForPath(page: Page, path: string, method: string): Promise<Response> {
  return page.waitForResponse(
    (r) => new URL(r.url()).pathname === `/api/v1${path}` && r.request().method() === method,
  );
}

/** changesOf returns the ci_change rows of a CI (AUD-03). */
async function changesOf(t: Tenant, id: string): Promise<Change[]> {
  return rows(await ok<List<Change>>(t, 'GET', `/cis/${id}/changes?limit=200`));
}

/** pollAudit waits for the audit entry `action` about the CI (asynchronous audit runner). */
async function pollAudit(t: Tenant, ciId: string, action: string): Promise<AuditEntry> {
  let found: AuditEntry | undefined;
  await expect
    .poll(
      async () => {
        const res = await api<List<AuditEntry>>(
          rq,
          t.token,
          'GET',
          `/audit-log?resource_type=ci&resource_id=${ciId}&limit=200`,
        );
        found = res.status === 200 ? rows(res.body).find((e) => e.action === action) : undefined;
        return Boolean(found);
      },
      { message: `audit entry ${action} of CI ${ciId}`, timeout: 60_000, intervals: [1_000] },
    )
    .toBe(true);
  return found!;
}

/** expectStatusChange asserts the history (ci_change) and audit entries of a status change. */
async function expectStatusChange(t: Tenant, ciId: string, from: string, to: string) {
  const change = (await changesOf(t, ciId)).find(
    (c) => c.change_type === 'status_change' && c.new_value === to,
  );
  expect(change, `ci_change status ${from} → ${to} of ${ciId}`).toBeTruthy();
  expect(change!.field_name).toBe('status');
  expect(change!.old_value).toBe(from);
  expect(change!.actor_id, 'the history names the acting user').toBe(t.userId);
  const audit = await pollAudit(t, ciId, 'ci.status_changed');
  expect(audit.actor_id, 'the audit entry names the acting user').toBe(t.userId);
  expect(audit.changes?.fields ?? []).toContainEqual({
    field: 'status',
    old_value: from,
    new_value: to,
  });
}

/** listRow is the row of the CMDB list that links to the CI. */
function listRow(page: Page, name: string) {
  return page
    .locator('#main-content')
    .getByRole('row')
    .filter({ has: page.getByRole('link', { name, exact: true }) });
}

/** parseCsv splits a CSV text (RFC 4180 quoting) into rows of fields. */
function parseCsv(text: string, sep: string): string[][] {
  const out: string[][] = [];
  let row: string[] = [];
  let field = '';
  let quoted = false;
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (quoted) {
      if (c === '"' && text[i + 1] === '"') {
        field += '"';
        i++;
      } else if (c === '"') {
        quoted = false;
      } else {
        field += c;
      }
    } else if (c === '"') {
      quoted = true;
    } else if (c === sep) {
      row.push(field);
      field = '';
    } else if (c === '\n' || c === '\r') {
      if (c === '\r' && text[i + 1] === '\n') i++;
      row.push(field);
      out.push(row);
      row = [];
      field = '';
    } else {
      field += c;
    }
  }
  if (field !== '' || row.length > 0) {
    row.push(field);
    out.push(row);
  }
  return out.filter((r) => r.length > 1 || r[0] !== '');
}

/** metricCount reads a counter sample of the server's Prometheus endpoint. */
async function metricCount(name: string, labels: string): Promise<number> {
  const res = await rq.get(`${SERVER}/metrics`, { failOnStatusCode: false });
  if (!res.ok()) return -1;
  const line = (await res.text())
    .split('\n')
    .find((l) => l.startsWith(`${name}{`) && l.includes(labels));
  return line ? Number(line.split(' ').pop()) : 0;
}

test.describe('Bulk operations, jobs, notifications, saved views, export, webhook egress', () => {
  test.beforeAll(async ({ browser, playwright }) => {
    test.setTimeout(4 * 60_000);
    rq = await playwright.request.newContext();
    a = await openTenant(browser, 'bulka');
    b = await openTenant(browser, 'bulkb');
    foreign = await newCI(b, {
      name: `foreign-${s}`,
      manufacturer: 'Fremdhersteller',
      serial_number: `SN-FOREIGN-${s}`,
    });
  });

  test.afterAll(async () => {
    await a?.ctx.close();
    await b?.ctx.close();
    await rq?.dispose();
  });

  test('B01 a bulk status change in the CMDB list is one request, persists after reload and leaves history and audit entries', async () => {
    const page = a.page;
    const tag = `bk1x${s}`;
    const selected = [
      await newCI(a, { name: `${tag}-a` }),
      await newCI(a, { name: `${tag}-b` }),
      await newCI(a, { name: `${tag}-c` }),
    ];
    const control = await newCI(a, { name: `${tag}-control` });

    await page.goto(`/cmdb?q=${tag}`);
    const main = page.locator('#main-content');
    for (const ci of [...selected, control]) {
      await expect(main.getByRole('link', { name: ci.name, exact: true })).toBeVisible();
    }
    for (const ci of selected) {
      await main.getByRole('checkbox', { name: `${ci.name} auswählen` }).check();
    }
    const toolbar = main.getByRole('toolbar', { name: 'Massenbearbeitung' });
    await expect(toolbar).toContainText('3 ausgewählt');
    await toolbar.getByLabel('Status setzen…').selectOption('maintenance');

    // API-08: the UI sends ONE bulk request instead of a fan-out of single updates.
    const singlePatches: string[] = [];
    const onRequest = (r: { url(): string; method(): string }) => {
      if (r.method() === 'PATCH' && /\/api\/v1\/cis\/[^/]+$/.test(new URL(r.url()).pathname)) {
        singlePatches.push(r.url());
      }
    };
    page.on('request', onRequest);
    const responded = waitForPath(page, '/cis/bulk', 'POST');
    await toolbar.getByRole('button', { name: 'Anwenden' }).click();
    const res = await responded;
    page.off('request', onRequest);
    const sent = res.request().postDataJSON() as { action?: string; ids: string[]; status: string };
    expect([...sent.ids].sort()).toEqual(selected.map((c) => c.id).sort());
    expect(sent.status).toBe('maintenance');
    expect(res.status(), 'below 100 ids the report answers synchronously').toBe(200);
    const report = (await res.json()) as BulkReport;
    expect(report.total).toBe(3);
    expect(report.succeeded).toBe(3);
    expect(report.failed).toBe(0);
    expect(report.items.map((i) => [i.id, i.result, i.code]).sort()).toEqual(
      selected.map((c) => [c.id, 'ok', 200]).sort(),
    );
    expect(singlePatches, 'no single PATCH requests besides the bulk request').toEqual([]);
    await expect(page.getByText('Status von 3 CIs aktualisiert')).toBeVisible();

    // Persistence: after a reload the list shows the new status of exactly the selected CIs.
    await page.reload();
    for (const ci of selected) {
      await expect(listRow(page, ci.name)).toContainText('Wartung');
    }
    await expect(listRow(page, control.name)).toContainText('Aktiv');
    for (const ci of selected) {
      expect((await getCI(a, ci.id)).status, `stored status of ${ci.name}`).toBe('maintenance');
    }
    expect((await getCI(a, control.id)).status, 'the unselected CI is untouched').toBe('active');

    // History (ci_change, AUD-03) and audit trail (AUD-05) for every changed CI.
    for (const ci of selected) {
      await expectStatusChange(a, ci.id, 'active', 'maintenance');
    }
    expect(
      (await changesOf(a, control.id)).filter((c) => c.change_type === 'status_change'),
      'no status change recorded for the unselected CI',
    ).toEqual([]);

    // The history is visible on the detail page of a changed CI.
    await page.goto(`/cmdb/${selected[0].id}`);
    await expect(page.getByRole('heading', { name: selected[0].name, exact: true })).toBeVisible();
    await page.locator('summary', { hasText: 'Historie' }).click();
    await expect(page.getByRole('list', { name: 'Historie' })).toContainText(
      'status_change · status',
    );
  });

  test('B02 a bulk action with one item that became invalid reports that item and applies the others (no silent loss)', async () => {
    const page = a.page;
    const tag = `bk2x${s}`;
    const keep1 = await newCI(a, { name: `${tag}-keep1` });
    const keep2 = await newCI(a, { name: `${tag}-keep2` });
    const victim = await newCI(a, { name: `${tag}-victim` });

    await page.goto(`/cmdb?q=${tag}`);
    const main = page.locator('#main-content');
    for (const ci of [keep1, keep2, victim]) {
      await expect(main.getByRole('link', { name: ci.name, exact: true })).toBeVisible();
    }
    await main.getByRole('checkbox', { name: 'Alle sichtbaren CIs auswählen' }).check();
    const toolbar = main.getByRole('toolbar', { name: 'Massenbearbeitung' });
    await expect(toolbar).toContainText('3 ausgewählt');
    await toolbar.getByLabel('Status setzen…').selectOption('inactive');

    // Another user deletes one of the selected CIs while the list is open.
    expect((await api(rq, a.token, 'DELETE', `/cis/${victim.id}`)).status).toBe(204);

    const responded = waitForPath(page, '/cis/bulk', 'POST');
    await toolbar.getByRole('button', { name: 'Anwenden' }).click();
    const res = await responded;
    expect(res.status()).toBe(200);
    const report = (await res.json()) as BulkReport;
    expect(report.total).toBe(3);
    expect(report.succeeded).toBe(2);
    expect(report.failed).toBe(1);
    const failedItem = report.items.find((i) => i.id === victim.id);
    expect(failedItem, 'the deleted CI is reported').toMatchObject({
      result: 'error',
      code: 404,
    });
    expect(failedItem!.error, 'the report explains the error').toBeTruthy();
    for (const ci of [keep1, keep2]) {
      expect(report.items.find((i) => i.id === ci.id)).toMatchObject({ result: 'ok', code: 200 });
    }

    // UI: partial result toast and the failing CI with its reason.
    await expect(page.getByText('2 CIs aktualisiert, 1 fehlgeschlagen')).toBeVisible();
    const failures = main.getByRole('list', { name: 'Fehlgeschlagene CIs' });
    await expect(failures).toBeVisible();
    await expect(failures.getByRole('listitem')).toHaveCount(1);
    await expect(failures).toContainText(failedItem!.error!);
    await expect(failures).toContainText(new RegExp(`${victim.name}|${victim.id}`));

    // The valid items are persisted, the deleted CI stays deleted.
    await page.reload();
    for (const ci of [keep1, keep2]) {
      await expect(listRow(page, ci.name)).toContainText('Inaktiv');
      expect((await getCI(a, ci.id)).status).toBe('inactive');
      await expectStatusChange(a, ci.id, 'active', 'inactive');
    }
    expect((await api(rq, a.token, 'GET', `/cis/${victim.id}`)).status).toBe(404);
  });

  test('B03 bulk limits and validation: 0 and 501 ids, unknown status or action, duplicates, CIs of another tenant', async () => {
    const tag = `bk3x${s}`;
    const one = await newCI(a, { name: `${tag}-one` });
    const two = await newCI(a, { name: `${tag}-two` });

    // No ids: 400; nothing to change.
    const empty = await api<{ field?: string }>(rq, a.token, 'POST', '/cis/bulk', {
      ids: [],
      status: 'inactive',
    });
    expect(empty.status).toBe(400);
    expect(empty.body.field).toBe('ids');

    // More than 500 ids: 422, and no CI of the request is changed.
    const tooMany = [
      one.id,
      ...Array.from(
        { length: 500 },
        (_, i) => `00000000-0000-4000-8000-${String(i).padStart(12, '0')}`,
      ),
    ];
    const over = await api<{ field?: string; detail?: string }>(rq, a.token, 'POST', '/cis/bulk', {
      ids: tooMany,
      status: 'inactive',
    });
    expect(over.status, JSON.stringify(over.body)).toBe(422);
    expect(over.body.field).toBe('ids');
    expect((await getCI(a, one.id)).status, '501 ids change nothing').toBe('active');

    // Unknown target status and unknown action: 422, nothing changed.
    const badStatus = await api<{ field?: string }>(rq, a.token, 'POST', '/cis/bulk', {
      ids: [one.id],
      status: 'scrapped',
    });
    expect(badStatus.status).toBe(422);
    expect(badStatus.body.field).toBe('status');
    const badAction = await api<{ field?: string }>(rq, a.token, 'POST', '/cis/bulk', {
      action: 'delete',
      ids: [one.id],
      status: 'inactive',
    });
    expect(badAction.status).toBe(422);
    expect(badAction.body.field).toBe('action');
    expect((await getCI(a, one.id)).status).toBe('active');

    // Duplicate ids are one CI; a CI of another tenant is "not found" and stays unchanged.
    const mixed = await api<BulkReport>(rq, a.token, 'POST', '/cis/bulk', {
      ids: [one.id, one.id, two.id, foreign.id],
      status: 'maintenance',
    });
    expect(mixed.status).toBe(200);
    expect(mixed.body.total, 'duplicates count once').toBe(3);
    expect(mixed.body.succeeded).toBe(2);
    expect(mixed.body.failed).toBe(1);
    expect(mixed.body.items.find((i) => i.id === foreign.id)).toMatchObject({
      result: 'error',
      code: 404,
    });
    expect((await getCI(a, one.id)).status).toBe('maintenance');
    expect((await getCI(a, two.id)).status).toBe('maintenance');
    const theirs = await getCI(b, foreign.id);
    expect(theirs.status, 'the CI of the other tenant is unchanged').toBe('active');
    expect(
      (await changesOf(b, foreign.id)).filter((c) => c.change_type === 'status_change'),
      'no history entry in the other tenant',
    ).toEqual([]);
  });

  test('B04 a malformed id in a bulk request is reported with the status the single change answers (400, not 500)', async () => {
    const valid = await newCI(a, { name: `bk4x${s}-valid` });
    const single = await api(rq, a.token, 'PATCH', '/cis/not-a-uuid', { status: 'inactive' });
    expect(single.status, 'the single change of a malformed id is a client error').toBe(400);

    const res = await api<BulkReport>(rq, a.token, 'POST', '/cis/bulk', {
      ids: ['not-a-uuid', valid.id],
      status: 'inactive',
    });
    expect(res.status).toBe(200);
    expect(res.body.items.find((i) => i.id === valid.id)).toMatchObject({ result: 'ok' });
    expect((await getCI(a, valid.id)).status).toBe('inactive');
    const item = res.body.items.find((i) => i.id === 'not-a-uuid');
    expect(item?.result).toBe('error');
    // OpenAPI bulkCIs: "every id with ok or its error and the HTTP status the single change
    // would have answered" – a malformed id is the caller's error, not an internal one.
    expect(item?.code, `per-id status of a malformed id: ${JSON.stringify(item)}`).toBe(
      single.status,
    );
  });

  test('J01 100 ids run as a job that the job center follows to the end; the result reports every id and the change persists', async ({
    browser,
  }) => {
    test.setTimeout(5 * 60_000);
    // A trial tenant holds 100 CIs: the job gets its own tenant.
    const c = await openTenant(browser, 'bulkjob');
    try {
      const page = c.page;
      const tag = `bkjx${s}`;
      const cis: CI[] = [];
      for (let i = 0; i < 98; i += 14) {
        cis.push(
          ...(await Promise.all(
            Array.from({ length: Math.min(14, 98 - i) }, (_, k) =>
              newCI(c, { name: `${tag}-${String(i + k).padStart(3, '0')}` }),
            ),
          )),
        );
      }
      const victim = await newCI(c, { name: `${tag}-victim` });
      expect((await api(rq, c.token, 'DELETE', `/cis/${victim.id}`)).status).toBe(204);
      const missing = '00000000-0000-4000-8000-00000000beef';
      const ids = [...cis.map((ci) => ci.id), victim.id, missing];
      expect(ids).toHaveLength(100);
      const metricBefore = await metricCount(
        'reticora_job_duration_seconds_count',
        'kind="ci.bulk",status="succeeded"',
      );

      // From 100 ids on the bulk change answers 202 with the job (API-08, JOB-04).
      const started = await rq.fetch(`${BASE}/api/v1/cis/bulk`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${c.token}` },
        data: { action: 'set_status', ids, status: 'inactive' },
        failOnStatusCode: false,
      });
      expect(started.status(), await started.text()).toBe(202);
      const accepted = (await started.json()) as { job_id: string; kind: string; status: string };
      expect(accepted.kind).toBe('ci.bulk');
      expect(started.headers()['location']).toBe(`/api/v1/jobs/${accepted.job_id}`);

      // Job center (UI-16, JOB-03): the job is listed and reaches "Erfolgreich".
      await page.goto('/jobs');
      const main = page.locator('#main-content');
      await expect(main.getByRole('heading', { name: 'Jobcenter' })).toBeVisible();
      const row = main.getByRole('row').filter({ hasText: 'ci.bulk' });
      await expect(async () => {
        await page.reload();
        await expect(row).toHaveCount(1, { timeout: 3_000 });
        await expect(row).toContainText('Erfolgreich', { timeout: 3_000 });
      }).toPass({ timeout: 90_000 });
      await expect(
        row.getByRole('progressbar', { name: 'Fortschritt von ci.bulk' }),
      ).toHaveAttribute('aria-valuenow', '100');
      // The status filter of the job center.
      await main.getByLabel('Status', { exact: true }).selectOption('failed');
      await expect(main.getByText('Keine Jobs')).toBeVisible();
      await main.getByLabel('Status', { exact: true }).selectOption('succeeded');
      await expect(row).toBeVisible();

      // The result of the job in the UI names the per-id errors.
      await row.getByRole('button', { name: 'Ergebnis' }).click();
      const dialog = page.getByRole('dialog');
      await expect(dialog).toContainText('"succeeded": 98');
      await expect(dialog).toContainText('"failed": 2');
      await expect(dialog).toContainText(missing);
      await expect(dialog).toContainText('CI not found');
      await page.keyboard.press('Escape');

      // API: job record (JOB-01) and its result: one entry per id.
      const job = await ok<Job>(c, 'GET', `/jobs/${accepted.job_id}`);
      expect(job.status).toBe('succeeded');
      expect(job.progress).toBe(100);
      expect(job.created_by, 'the job records its creator').toBe(c.userId);
      expect(job.started_at).toBeTruthy();
      expect(job.finished_at).toBeTruthy();
      const result = await ok<BulkReport>(c, 'GET', `/jobs/${accepted.job_id}/result`);
      expect(result.total).toBe(100);
      expect(result.succeeded).toBe(98);
      expect(result.failed).toBe(2);
      expect(result.items).toHaveLength(100);
      expect(new Set(result.items.map((i) => i.id))).toEqual(new Set(ids));
      for (const id of [victim.id, missing]) {
        expect(result.items.find((i) => i.id === id)).toMatchObject({ result: 'error', code: 404 });
      }
      expect(result.items.filter((i) => i.result === 'ok')).toHaveLength(98);

      // Persistence: every CI changed, visible after reload in the filtered CMDB list.
      await page.goto('/cmdb?status=inactive');
      await expect(main.getByText(/^98 Einträge$/)).toBeVisible();
      await page.reload();
      await expect(main.getByText(/^98 Einträge$/)).toBeVisible();
      await expect(main.getByLabel('CI-Status filtern')).toHaveValue('inactive');
      const stored = await allCIs(c);
      expect(stored).toHaveLength(98);
      expect(stored.filter((ci) => ci.status !== 'inactive').map((ci) => ci.name)).toEqual([]);
      for (const ci of [cis[0], cis[49], cis[97]]) {
        await expectStatusChange(c, ci.id, 'active', 'inactive');
      }

      // A finished job cannot be cancelled; phase 1 defines no bulk revert (E-25, BLK-02 [P2]).
      expect((await api(rq, c.token, 'DELETE', `/jobs/${accepted.job_id}`)).status).toBe(409);
      expect((await api(rq, c.token, 'POST', `/jobs/${accepted.job_id}/revert`)).status).toBe(409);

      // Job metrics (JOB-03): the duration of the finished ci.bulk job is observed.
      if (metricBefore >= 0) {
        expect(
          await metricCount(
            'reticora_job_duration_seconds_count',
            'kind="ci.bulk",status="succeeded"',
          ),
        ).toBeGreaterThan(metricBefore);
      }

      // Another tenant neither reads nor lists the job.
      expect((await api(rq, a.token, 'GET', `/jobs/${accepted.job_id}`)).status).toBe(404);
      expect((await api(rq, a.token, 'GET', `/jobs/${accepted.job_id}/result`)).status).toBe(404);
      const theirs = await ok<List<Job>>(a, 'GET', '/jobs?limit=100');
      expect(rows(theirs).map((j) => j.id)).not.toContain(accepted.job_id);
    } finally {
      await c.ctx.close();
    }
  });

  test('V01 a saved view created on the saved-views page persists across reload with its filter and stays private to the tenant', async () => {
    const page = a.page;
    const tag = `vw1x${s}`;
    const preset = 'Production servers without backup relationship';
    const presets = await ok<List<SavedView>>(a, 'GET', '/saved-views/presets');
    const presetSpec = rows(presets).find((p) => p.name === preset)?.filter_spec;
    expect(presetSpec, `preset ${preset}`).toBeTruthy();

    // CIs the preset distinguishes: type physical_server, environment production vs staging.
    const phys = await ok<{ id: string }>(a, 'POST', '/ci-types', {
      key: 'physical_server',
      name: `Physical Server ${tag}`,
    });
    const prod = await newCI(a, {
      ci_type_id: phys.id,
      name: `${tag}-prod`,
      attributes: { environment: 'production' },
    });
    const staging = await newCI(a, {
      ci_type_id: phys.id,
      name: `${tag}-staging`,
      attributes: { environment: 'staging' },
    });

    await page.goto('/saved-views');
    const main = page.locator('#main-content');
    await expect(main.getByRole('heading', { name: 'Gespeicherte Ansichten' })).toBeVisible();
    const ranPreset = waitForPath(page, '/search/query', 'POST');
    await main.getByRole('button', { name: new RegExp(preset) }).click();
    expect((await ranPreset).status()).toBe(200);
    await expect(main.getByRole('link', { name: prod.name, exact: true })).toBeVisible();
    await expect(main.getByRole('link', { name: staging.name, exact: true })).toHaveCount(0);

    // Save the active filter as an own view (name asked in a prompt).
    const viewName = `Ansicht ${tag}`;
    page.once('dialog', (dialog) => void dialog.accept(viewName));
    const saved = waitForPath(page, '/saved-views', 'POST');
    await main.getByRole('button', { name: 'Als Ansicht speichern' }).click();
    const savedRes = await saved;
    expect(savedRes.status(), await savedRes.text()).toBe(201);
    const view = (await savedRes.json()) as SavedView;
    await expect(main.getByRole('button', { name: viewName, exact: true })).toBeVisible();

    // Reload: the view is still there and runs with the stored filter.
    await page.reload();
    const ownView = main.getByRole('button', { name: viewName, exact: true });
    await expect(ownView).toBeVisible();
    const rerun = waitForPath(page, '/search/query', 'POST');
    await ownView.click();
    const rerunRes = await rerun;
    expect(rerunRes.request().postDataJSON()).toEqual({ ...presetSpec, entity_kind: 'ci' });
    await expect(main.getByRole('link', { name: prod.name, exact: true })).toBeVisible();
    await expect(main.getByRole('link', { name: staging.name, exact: true })).toHaveCount(0);

    // Stored record (VIE-01): owner, entity kind, filter spec, private by default.
    const stored = await ok<SavedView>(a, 'GET', `/saved-views/${view.id}`);
    expect(stored.name).toBe(viewName);
    expect(stored.entity_kind).toBe('ci');
    expect(stored.owner_id).toBe(a.userId);
    expect(stored.shared).toBe(false);
    expect(stored.filter_spec).toEqual(presetSpec);

    // Another tenant neither lists, reads, changes nor deletes it.
    expect(
      rows(await ok<List<SavedView>>(b, 'GET', '/saved-views')).map((v) => v.id),
    ).not.toContain(view.id);
    expect((await api(rq, b.token, 'GET', `/saved-views/${view.id}`)).status).toBe(404);
    expect(
      (await api(rq, b.token, 'PATCH', `/saved-views/${view.id}`, { name: 'hijacked' })).status,
    ).toBe(404);
    expect((await api(rq, b.token, 'DELETE', `/saved-views/${view.id}`)).status).toBe(404);
    expect((await ok<SavedView>(a, 'GET', `/saved-views/${view.id}`)).name).toBe(viewName);

    // An invalid filter AST is refused with the node named (SRC-03).
    const invalid = await api<{ field?: string }>(rq, a.token, 'POST', '/saved-views', {
      name: `invalid ${tag}`,
      entity_kind: 'ci',
      filter_spec: { where: { op: 'bogus' } },
    });
    expect(invalid.status).toBe(422);
    expect(invalid.body.field).toBe('filter_spec.where');

    // Delete in the UI: gone after reload and in the API.
    const deleted = waitForPath(page, `/saved-views/${view.id}`, 'DELETE');
    await main.getByRole('button', { name: `Löschen: ${viewName}` }).click();
    expect((await deleted).status()).toBe(204);
    await page.reload();
    await expect(main.getByRole('button', { name: preset })).toBeVisible();
    await expect(main.getByRole('button', { name: viewName, exact: true })).toHaveCount(0);
    expect((await api(rq, a.token, 'GET', `/saved-views/${view.id}`)).status).toBe(404);
  });

  test('E01 a CSV export started on the export page downloads exactly the API data of the own tenant', async () => {
    const page = a.page;
    const tag = `ex1x${s}`;
    await newCI(a, {
      name: `${tag} Müller; Rack 7`,
      manufacturer: 'Exportwerk',
      model: 'R-7',
      serial_number: `SN-${tag}`,
      management_ip: '10.20.30.40',
      firmware_version: '1.2.3',
    });
    await newCI(a, { name: `${tag}-plain`, status: 'maintenance' });
    const expected = await allCIs(a);

    await page.goto('/export');
    const main = page.locator('#main-content');
    await expect(main.getByRole('heading', { name: 'Export', exact: true })).toBeVisible();
    await main.getByLabel('Format').selectOption('csv');
    const created = waitForPath(page, '/exports', 'POST');
    await main.getByRole('button', { name: 'Auftrag erstellen' }).click();
    const createdRes = await created;
    expect(createdRes.status(), 'POST /exports answers 202 (job)').toBe(202);
    const job = (await createdRes.json()) as ExportJob;

    let done: ExportJob | undefined;
    await expect
      .poll(
        async () => {
          done = await ok<ExportJob>(a, 'GET', `/exports/${job.id}`);
          return done.status;
        },
        { timeout: 120_000, intervals: [1_000] },
      )
      .toBe('completed');
    expect(done!.row_count, 'one row per CI of the tenant').toBe(expected.length);
    expect(done!.initiated_by).toBe(a.userId);
    // EXP-01: download through a presigned URL valid for 15 minutes.
    expect(new URL(done!.download_url!).searchParams.get('X-Amz-Expires')).toBe('900');
    const manifest = done!.manifest!;
    expect(manifest.fields.length).toBeGreaterThan(0);
    expect(manifest.omitted.length, 'the manifest lists the omitted fields').toBeGreaterThan(0);

    // UI: the job row shows the completion, the row count, the omitted fields and the link.
    await page.reload();
    const jobRow = main
      .getByRole('row')
      .filter({ hasText: 'Abgeschlossen' })
      .filter({ hasText: `${manifest.omitted.length} Felder nicht enthalten` })
      .first();
    await expect(jobRow).toBeVisible();
    await expect(jobRow).toContainText(String(expected.length));
    const downloadP = page.waitForEvent('download', { timeout: 30_000 });
    await jobRow.getByRole('link', { name: 'Herunterladen' }).click();
    const download = await downloadP;
    const bytes = await readFile(await download.path());

    // File format (EXP-01): UTF-8 with BOM, semicolon, header = manifest fields.
    expect([...bytes.subarray(0, 3)], 'UTF-8 BOM').toEqual([0xef, 0xbb, 0xbf]);
    const table = parseCsv(bytes.subarray(3).toString('utf8'), ';');
    const header = table[0];
    expect(header, 'header = manifest fields').toEqual(manifest.fields);
    const body = table.slice(1);
    expect(body, 'one row per CI').toHaveLength(expected.length);

    // Every row equals the API record of the CI, field by field; nothing else is in the file.
    const byId = new Map(expected.map((ci) => [ci.id, ci]));
    const seen = new Set<string>();
    for (const line of body) {
      const record = Object.fromEntries(header.map((h, i) => [h, line[i] ?? '']));
      const ci = byId.get(record.id);
      expect(ci, `row ${record.id} (${record.name}) is a CI of the tenant`).toBeTruthy();
      seen.add(record.id);
      for (const field of header) {
        const value = (ci as unknown as Record<string, unknown>)[field];
        expect(record[field], `${field} of ${ci!.name}`).toBe(
          value === undefined || value === null ? '' : String(value),
        );
      }
    }
    expect(seen.size).toBe(expected.length);
    const text = bytes.toString('utf8');
    expect(text, 'no CI of another tenant').not.toContain(foreign.id);
    expect(text).not.toContain(foreign.name);
    expect(text).not.toContain(b.orgId);

    // The export is a job of the generic job model (API-09) and shown in the job center.
    const jobs = rows(await ok<List<Job>>(a, 'GET', '/jobs?kind=export&limit=100'));
    const mirrored = jobs.find((j) => j.ref_id === job.id);
    expect(mirrored, 'the export is listed in /jobs').toBeTruthy();
    expect(mirrored!.status).toBe('succeeded');
    await page.goto('/jobs');
    await expect(main.getByRole('row').filter({ hasText: 'export' }).first()).toContainText(
      'Erfolgreich',
    );

    // Another tenant can neither read the export job nor see it.
    expect((await api(rq, b.token, 'GET', `/exports/${job.id}`)).status).toBe(404);
    expect(rows(await ok<List<ExportJob>>(b, 'GET', '/exports')).map((j) => j.id)).not.toContain(
      job.id,
    );
    expect((await api(rq, b.token, 'GET', `/jobs/${mirrored!.id}`)).status).toBe(404);
  });

  test('N01 a finished export notifies its initiator in the notification center; read state persists', async () => {
    const page = a.page;
    // The UI has no page for notification channels: the in-app channel is set up via REST.
    const channels = rows(await ok<List<{ kind: string }>>(a, 'GET', '/notification-channels'));
    if (!channels.some((ch) => ch.kind === 'inapp')) {
      await ok(a, 'POST', '/notification-channels', {
        name: `In-App ${s}`,
        kind: 'inapp',
        provider: 'inapp',
        enabled: true,
        config: {},
      });
    }
    // One more CI than in any earlier export, so this export's notification text is unique.
    await newCI(a, { name: `nt1x${s}-extra` });

    await page.goto('/export');
    const main = page.locator('#main-content');
    await main.getByLabel('Format').selectOption('csv');
    const created = waitForPath(page, '/exports', 'POST');
    await main.getByRole('button', { name: 'Auftrag erstellen' }).click();
    const job = (await (await created).json()) as ExportJob;
    let done: ExportJob | undefined;
    await expect
      .poll(
        async () => {
          done = await ok<ExportJob>(a, 'GET', `/exports/${job.id}`);
          return done.status;
        },
        { timeout: 120_000, intervals: [1_000] },
      )
      .toBe('completed');

    // NTF-05 "Export fertig": an in-app notification for the initiator.
    let note: Notification | undefined;
    await expect
      .poll(
        async () => {
          const list = await ok<List<Notification>>(a, 'GET', '/notifications?limit=50');
          note = rows(list).find((n) => n.data?.id === job.id);
          return Boolean(note);
        },
        { message: 'notification of the finished export', timeout: 90_000, intervals: [2_000] },
      )
      .toBe(true);
    expect(note!.category).toBe('export');
    expect(note!.subject).toBe('Export fertig (csv)');
    expect(note!.read_at).toBeFalsy();
    const unreadTotal = async () =>
      (await ok<List<Notification>>(a, 'GET', '/notifications?unread=true&limit=50')).total;
    const bell = (count: number) =>
      page.getByRole('button', { name: `Benachrichtigungen öffnen (${count} ungelesen)` });

    // Notification center (UI-16): the bell shows the unread count of the API.
    await expect(async () => {
      await page.reload();
      await expect(bell(await unreadTotal())).toBeVisible({ timeout: 5_000 });
    }).toPass({ timeout: 60_000 });
    await page.getByRole('button', { name: /^Benachrichtigungen öffnen/ }).click();
    const panel = page.getByRole('dialog', { name: 'Benachrichtigungen' });
    const entry = panel
      .getByRole('listitem')
      .filter({ hasText: `Ihr Export (csv, ${done!.row_count} Zeilen)` });
    await expect(entry).toHaveCount(1);
    await expect(entry).toContainText('Export fertig (csv)');
    await expect(entry).toHaveAttribute('data-unread', 'true');

    // Mark it read: the entry and the stored notification are read.
    const marked = waitForPath(page, `/notifications/${note!.id}/read`, 'POST');
    await entry.getByRole('button', { name: 'Als gelesen markieren' }).click();
    expect((await marked).status()).toBe(204);
    await expect(entry).not.toHaveAttribute('data-unread', 'true');
    await expect(entry.getByRole('button', { name: 'Als gelesen markieren' })).toHaveCount(0);
    const after = rows(await ok<List<Notification>>(a, 'GET', '/notifications?limit=50')).find(
      (n) => n.id === note!.id,
    );
    expect(after?.read_at, 'read_at is stored').toBeTruthy();
    expect(
      rows(await ok<List<Notification>>(a, 'GET', '/notifications?unread=true&limit=50')).map(
        (n) => n.id,
      ),
    ).not.toContain(note!.id);

    // Persisted: after a reload the bell counts without it and the entry stays read.
    await expect(async () => {
      await page.reload();
      await expect(bell(await unreadTotal())).toBeVisible({ timeout: 5_000 });
    }).toPass({ timeout: 60_000 });
    await page.getByRole('button', { name: /^Benachrichtigungen öffnen/ }).click();
    await expect(entry).toHaveCount(1);
    await expect(entry).not.toHaveAttribute('data-unread', 'true');

    // Another tenant's user does not get it and cannot mark it.
    expect(
      rows(await ok<List<Notification>>(b, 'GET', '/notifications?limit=50')).map((n) => n.id),
    ).not.toContain(note!.id);
    expect((await api(rq, b.token, 'POST', `/notifications/${note!.id}/read`)).status).toBe(404);
  });

  test('W01 a webhook to a local catcher is refused by the egress protection, not stored, nothing delivered, and the UI explains the refusal', async () => {
    const page = a.page;
    const hits: string[] = [];
    const catcher: Server = createServer((req, res) => {
      hits.push(`${req.method} ${req.url}`);
      res.statusCode = 204;
      res.end();
    });
    await new Promise<void>((resolve) => catcher.listen(0, '127.0.0.1', resolve));
    const port = (catcher.address() as AddressInfo).port;
    const name = `catcher-${s}`;
    const url = `http://127.0.0.1:${port}/hook`;
    try {
      await page.goto('/webhooks');
      const main = page.locator('#main-content');
      await expect(main.getByRole('heading', { name: 'Webhooks' })).toBeVisible();
      await main.getByLabel('Name', { exact: true }).fill(name);
      await main.getByLabel('URL', { exact: true }).fill(url);
      await main.getByLabel('Geheimnis (HMAC-Signatur)').fill(`secret-${s}-0123456789`);
      await main.getByRole('checkbox', { name: 'ci.created' }).check();
      const posted = waitForPath(page, '/webhooks', 'POST');
      await main.getByRole('button', { name: 'Speichern' }).click();
      const res = await posted;

      // SEC-08 / WP-049: URL validation already when storing, with the reason.
      expect(res.status(), 'a loopback destination is refused').toBe(400);
      const problem = (await res.json()) as { detail?: string };
      expect(problem.detail ?? '').toMatch(/not allowed/i);
      expect(problem.detail ?? '').toContain('127.0.0.1');

      // WP-049 / SEC-08 with UI-01..10 (webhooks page): the user is told why the
      // subscription was not saved, not left with a silently unchanged form. Checked right
      // after the answer, so a transient toast (default lifetime 6 s) counts too; soft, so the
      // storage and delivery checks below still run and report.
      await expect
        .soft(
          page
            .getByText(/not allowed|nicht erlaubt|nicht zulässig|blockiert|blocked|egress/i)
            .filter({ visible: true })
            .first(),
          'the refusal is explained on the webhooks page',
        )
        .toBeVisible({ timeout: 5_000 });

      // Not stored, and an event of the tenant reaches nothing on the local address.
      const subs = rows(await ok<List<{ name: string; url: string }>>(a, 'GET', '/webhooks'));
      expect(subs.filter((w) => w.name === name || w.url === url)).toEqual([]);
      await newCI(a, { name: `wh1x${s}-event` });
      await page.waitForTimeout(5_000);
      expect(hits, 'the local catcher received nothing').toEqual([]);
    } finally {
      await new Promise<void>((resolve) => catcher.close(() => resolve()));
    }
  });

  test('W02 the egress protection refuses private, loopback, link-local, metadata, IPv6 ULA and non-http targets with a reason and accepts a public host', async () => {
    const blocked: [string, string][] = [
      ['http://10.1.2.3/hook', '10.1.2.3'],
      ['http://172.16.5.4/hook', '172.16.5.4'],
      ['http://192.168.1.10/hook', '192.168.1.10'],
      ['http://127.0.0.1:8080/hook', '127.0.0.1'],
      ['http://169.254.169.254/latest/meta-data', '169.254.169.254'],
      ['http://[::1]:8080/hook', '::1'],
      ['http://[fd00::1]/hook', 'fd00::1'],
      ['http://[fe80::1]/hook', 'fe80::1'],
      ['http://localhost:8080/hook', 'localhost'],
      ['http://metadata.google.internal/computeMetadata/v1', 'metadata.google.internal'],
      ['ftp://example.org/hook', 'ftp'],
      ['http://user:pw@example.org/hook', 'credentials'],
    ];
    for (const [url, reason] of blocked) {
      const res = await api<{ detail?: string }>(rq, a.token, 'POST', '/webhooks', {
        name: `blocked-${s}`,
        url,
        secret: `secret-${s}-0123456789`,
        events: ['discovery.completed'],
      });
      expect(res.status, `${url}: ${JSON.stringify(res.body)}`).toBe(400);
      expect(res.body.detail ?? '', `${url} is refused with its reason`).toContain(reason);
    }
    const stored = rows(await ok<List<{ name: string }>>(a, 'GET', '/webhooks'));
    expect(stored.filter((w) => w.name === `blocked-${s}`)).toEqual([]);

    // Control: a public destination is accepted (the check does not refuse everything).
    // The .invalid TLD never resolves, so no request leaves the stack.
    const allowed = await api<{ id: string }>(rq, a.token, 'POST', '/webhooks', {
      name: `public-${s}`,
      url: `https://hooks-${s}.example.invalid/hook`,
      secret: `secret-${s}-0123456789`,
      events: ['discovery.completed'],
    });
    expect(allowed.status, JSON.stringify(allowed.body)).toBe(201);
    expect((await api(rq, a.token, 'DELETE', `/webhooks/${allowed.body.id}`)).status).toBe(204);
  });
});
