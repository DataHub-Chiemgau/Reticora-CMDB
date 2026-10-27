import {
  test,
  expect,
  type APIRequestContext,
  type Browser,
  type BrowserContext,
  type Page,
} from '@playwright/test';
import { execFileSync } from 'node:child_process';
import { api, signupTenant, stamp, uiLogin, BASE, DATABASE_URL } from './support/stack';

/*
 * Job center on the real stack (WP-521, UI-16; F-162 UI part, F-347, JOB-03), nothing mocked:
 * UI → REST API → job service and workers → PostgreSQL → UI.
 *
 * One organization is created through the self-signup UI. Its administrator creates two
 * clients and invites a client_technician of client 1 (the creator of the bulk jobs), a
 * client_technician of client 2 and a user holding only a custom role with job:read and
 * ci:read (organization scope). The CIs are a fixture written with psql (E2E_DATABASE_URL,
 * the migration login, under the organization's RLS setting): 500 CIs of client 1 per bulk
 * job and 40,000 CIs for the export, far beyond the 100 CIs of a trial tenant, so the jobs
 * run long enough to be cancelled while running.
 *
 * Test → requirement IDs proven:
 *  J1 a running bulk job of client 1: the user without the right sees it without a cancel
 *     button, his direct DELETE answers 403 and leaves it running ....... UI-16, JOB-03 (WP-521)
 *  J2 a client-2 technician neither lists nor reads nor cancels the client-1 job (404), his
 *     job center does not show it .................................. UI-16, TEN-09 (WP-521)
 *  J3 a bulk job with 500 ids cancelled in the job center while running: the row shows
 *     "Abgebrochen" and the partial report, fewer than 500 CIs changed (per API), the partial
 *     report is readable in the UI and per API ............ UI-16, JOB-03, BLK-01 (WP-521, F-162)
 *  J4 an export of 40,000 rows cancelled in the job center while running: no file, the
 *     export page shows the cancelled export, the mirrored job is cancelled without result
 *     ..................................................... UI-16, JOB-03, EXP-01 (WP-521, F-162)
 *
 * The cancel is deterministic: before the click, polling the API confirms the job is running;
 * a job that finished earlier fails the case (no skip). A failing test is a product finding.
 */

interface List<T> {
  data: T[] | null;
  total: number;
}

interface Job {
  id: string;
  kind: string;
  status: string;
  created_by?: string;
  ref_id?: string;
  cancellable?: boolean;
}

interface BulkReport {
  total: number;
  succeeded: number;
  failed: number;
  skipped?: number;
  cancelled?: boolean;
  items: { id: string; result: 'ok' | 'error' | 'skipped'; code: number }[];
}

interface ExportJob {
  id: string;
  status: string;
  row_count?: number;
  error_message?: string;
  download_url?: string;
}

interface User {
  email: string;
  password: string;
  id: string;
}

interface Session {
  ctx: BrowserContext;
  page: Page;
  token: string;
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const EXPORT_ROWS = 40_000;
const BULK_IDS = 500;

let rq: APIRequestContext;
let admin: Session;
let orgId = '';
let serverType = '';
let client1 = '';
let client2 = '';
let tech1: Session & { user: User };
let tech2: Session & { user: User };
let reader: Session & { user: User };
/** The bulk job of client 1 that J1 starts and J2 looks for. */
let client1Job = '';
const s = stamp();

/** A documentation address (198.18.0.0/15) so each signup has its own budget (E-33). */
function realIp(): string {
  return `198.18.${Math.floor(Math.random() * 250) + 1}.${Math.floor(Math.random() * 250) + 1}`;
}

function rows<T>(list: List<T> | undefined | null): T[] {
  return list?.data ?? [];
}

/** ok asserts a 2xx answer of a setup call and returns its body. */
async function ok<T>(
  token: string,
  method: 'GET' | 'POST' | 'PATCH' | 'DELETE',
  path: string,
  data?: unknown,
): Promise<T> {
  const res = await api<T>(rq, token, method, path, data);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeGreaterThanOrEqual(200);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeLessThan(300);
  return res.body;
}

/**
 * insertCIs writes count CIs of the organization (optionally of a client) named
 * `<prefix>-<n>` with one statement under the organization's RLS setting and
 * returns their ids, or none when ids is false.
 */
function insertCIs(prefix: string, count: number, clientId: string, ids: boolean): string[] {
  expect(UUID.test(orgId) && UUID.test(serverType), 'organization and type are UUIDs').toBe(true);
  expect(clientId === '' || UUID.test(clientId), 'client is a UUID').toBe(true);
  expect(/^[a-z0-9-]+$/.test(prefix), 'plain prefix').toBe(true);
  const sql = `BEGIN;
SET LOCAL app.org_id = :'org';
INSERT INTO ci (organization_id, ci_type_id, client_id, name)
  SELECT :'org', :'type', NULLIF(:'client', '')::uuid, :'prefix' || '-' || lpad(g::text, 5, '0')
  FROM generate_series(1, ${count}) AS g
  ${ids ? 'RETURNING id' : ''};
COMMIT;`;
  const out = execFileSync(
    'psql',
    [
      DATABASE_URL,
      '-X',
      '-q',
      '-At',
      '-v',
      'ON_ERROR_STOP=1',
      '-v',
      `org=${orgId}`,
      '-v',
      `type=${serverType}`,
      '-v',
      `client=${clientId}`,
      '-v',
      `prefix=${prefix}`,
      '-f',
      '-',
    ],
    { input: sql, encoding: 'utf8', timeout: 120_000 },
  ).trim();
  const found = out ? out.split('\n').filter((line: string) => UUID.test(line)) : [];
  if (ids) expect(found, `ids of the fixture ${prefix}`).toHaveLength(count);
  return found;
}

async function newSession(browser: Browser, account: { email: string; password: string }) {
  const ctx = await browser.newContext({
    baseURL: BASE,
    locale: 'de-DE',
    extraHTTPHeaders: { 'X-Real-IP': realIp() },
  });
  const page = await ctx.newPage();
  const token = await uiLogin(page, account);
  return { ctx, page, token };
}

/** invite issues an invitation with a standard role, accepts it and returns the user. */
async function invite(
  roleId: string,
  label: string,
  scope: { scope_type: 'org' } | { scope_type: 'client'; scope_id: string },
): Promise<User> {
  const email = `jc-${label}-${s}@audit-e2e.de`;
  const password = `Audit-${label}-${s}!aA`;
  const invitation = await ok<{ token?: string }>(admin.token, 'POST', '/invitations', {
    email,
    role_id: roleId,
    ...scope,
  });
  expect(invitation.token, 'the token is returned once on issue').toBeTruthy();
  const accept = await rq.post(`${BASE}/api/v1/invitations/accept`, {
    data: { token: invitation.token, password, display_name: `Jobcenter ${label}` },
    failOnStatusCode: false,
  });
  expect(accept.status(), `accept the invitation: ${await accept.text()}`).toBe(201);
  const id = ((await accept.json()) as { user_id: string }).user_id;
  expect(id, 'the accepted user').toBeTruthy();
  return { email, password, id };
}

async function jobOf(token: string, id: string): Promise<Job> {
  return ok<Job>(token, 'GET', `/jobs/${id}`);
}

/**
 * untilRunning polls the API until the job runs; a job that finished before
 * fails the case, there is no skip.
 */
async function untilRunning(token: string, id: string) {
  await expect
    .poll(
      async () => {
        const status = (await jobOf(token, id)).status;
        if (status !== 'queued' && status !== 'running') {
          throw new Error(`the job ${id} finished (${status}) before it could be cancelled`);
        }
        return status;
      },
      { message: `job ${id} running`, timeout: 60_000, intervals: [100] },
    )
    .toBe('running');
}

/** untilFinished waits until the job left queued and running. */
async function untilFinished(token: string, id: string): Promise<Job> {
  let job: Job | undefined;
  await expect
    .poll(
      async () => {
        job = await jobOf(token, id);
        return job.status !== 'queued' && job.status !== 'running';
      },
      { message: `job ${id} finished`, timeout: 180_000, intervals: [500] },
    )
    .toBe(true);
  return job!;
}

/** startBulk starts a bulk status change of client-1 CIs as the client-1 technician. */
async function startBulk(ids: string[], status: string): Promise<string> {
  const res = await api<{ job_id: string; kind: string }>(rq, tech1.token, 'POST', '/cis/bulk', {
    action: 'set_status',
    ids,
    status,
  });
  expect(res.status, `POST /cis/bulk: ${JSON.stringify(res.body)}`).toBe(202);
  expect(res.body.kind).toBe('ci.bulk');
  return res.body.job_id;
}

/** openJobCenter loads the job center and waits until the user's rights are read. */
async function openJobCenter(page: Page) {
  const rights = page.waitForResponse(
    (r) => new URL(r.url()).pathname === '/api/v1/me/permissions' && r.request().method() === 'GET',
  );
  await page.goto('/jobs');
  expect((await rights).status(), 'GET /me/permissions').toBe(200);
  await expect(
    page.locator('#main-content').getByRole('heading', { name: 'Jobcenter' }),
  ).toBeVisible();
}

function jobRow(page: Page, id: string) {
  return page.locator('#main-content').locator(`tr[data-job-id="${id}"]`);
}

test.describe.configure({ mode: 'serial' });

test.describe('Job center: cancel only where possible, partial report, rights and isolation', () => {
  test.beforeAll(async ({ browser, playwright }) => {
    test.setTimeout(6 * 60_000);
    rq = await playwright.request.newContext();
    const ctx = await browser.newContext({
      baseURL: BASE,
      locale: 'de-DE',
      extraHTTPHeaders: { 'X-Real-IP': realIp() },
    });
    const page = await ctx.newPage();
    const account = await signupTenant(page, rq, 'jobcenter');
    const token = await uiLogin(page, account);
    admin = { ctx, page, token };
    orgId = (
      JSON.parse(Buffer.from(token.split('.')[1], 'base64url').toString()) as { org: string }
    ).org;

    const types = await ok<List<{ id: string; key: string }>>(token, 'GET', '/ci-types?limit=200');
    serverType = rows(types).find((ty) => ty.key === 'server')?.id ?? '';
    expect(serverType, 'catalog type server').not.toBe('');
    client1 = (
      await ok<{ id: string }>(token, 'POST', '/clients', {
        name: `JC C1 ${s}`,
        slug: `jc-c1-${s}`,
      })
    ).id;
    client2 = (
      await ok<{ id: string }>(token, 'POST', '/clients', {
        name: `JC C2 ${s}`,
        slug: `jc-c2-${s}`,
      })
    ).id;

    const roles = await ok<List<{ id: string; name: string }>>(token, 'GET', '/roles?limit=100');
    const roleId = (name: string) => {
      const id = rows(roles).find((r) => r.name === name)?.id ?? '';
      expect(id, `system role ${name} (RBA-02)`).not.toBe('');
      return id;
    };
    const t1 = await invite(roleId('client_technician'), 'c1', {
      scope_type: 'client',
      scope_id: client1,
    });
    const t2 = await invite(roleId('client_technician'), 'c2', {
      scope_type: 'client',
      scope_id: client2,
    });
    // An invitation carries a standard role only (RBA-02): the reader gets the custom role
    // with job:read and ci:read after the acceptance, the viewer role is removed.
    const readOnly = await ok<{ id: string }>(token, 'POST', '/roles', {
      name: `job-reader-${s}`,
      permissions: ['job:read', 'ci:read'],
    });
    const r = await invite(roleId('viewer'), 'reader', { scope_type: 'org' });
    await ok(token, 'POST', '/role-assignments', { user_id: r.id, custom_role_id: readOnly.id });
    const assignments = await ok<List<{ id: string; role_id?: string }>>(
      token,
      'GET',
      `/role-assignments?user_id=${r.id}`,
    );
    const standard = rows(assignments).filter((a) => a.role_id === roleId('viewer'));
    expect(standard, 'the viewer assignment of the invitation').toHaveLength(1);
    await ok(token, 'DELETE', `/role-assignments/${standard[0]!.id}`);

    tech1 = { ...(await newSession(browser, t1)), user: t1 };
    tech2 = { ...(await newSession(browser, t2)), user: t2 };
    reader = { ...(await newSession(browser, r)), user: r };
    const rights = await ok<{ permissions: string[] }>(reader.token, 'GET', '/me/permissions');
    expect(rights.permissions).toContain('job:read');
    expect(rights.permissions).not.toContain('ci:write');
    expect(rights.permissions).not.toContain('export:run');

    // The export fixture: far more rows than a trial tenant may create (ENT-07).
    insertCIs(`jcx-${s}`, EXPORT_ROWS, '', false);
  });

  test.afterAll(async () => {
    await tech1?.ctx.close();
    await tech2?.ctx.close();
    await reader?.ctx.close();
    await admin?.ctx.close();
    await rq?.dispose();
  });

  test('J1 a user without the right sees a running job without a cancel button; his DELETE answers 403', async () => {
    test.setTimeout(5 * 60_000);
    const ids = insertCIs(`jc1-${stamp()}`, BULK_IDS, client1, true);
    await reader.page.goto('/dashboard');
    const id = await startBulk(ids, 'maintenance');
    client1Job = id;

    await openJobCenter(reader.page);
    const row = jobRow(reader.page, id);
    await expect(row).toContainText('ci.bulk');
    // The job is active while the user looks at it: the missing button is not a finished job.
    await expect(row).toContainText(/Läuft|Wartend/, { timeout: 5_000 });
    const job = await jobOf(reader.token, id);
    expect(['queued', 'running'], 'the job is active').toContain(job.status);
    expect(job.created_by).toBe(tech1.user.id);
    await expect(row.getByRole('button', { name: 'Abbrechen' })).toHaveCount(0);

    // The server refuses the cancel of the user (403) and the job goes on.
    const refused = await api(rq, reader.token, 'DELETE', `/jobs/${id}`);
    expect(refused.status, `DELETE as reader: ${JSON.stringify(refused.body)}`).toBe(403);
    expect((await jobOf(tech1.token, id)).status).not.toBe('cancelled');

    // The creator's job center offers the cancel for the same job while it is active.
    await openJobCenter(tech1.page);
    const own = jobRow(tech1.page, id);
    const state = (await jobOf(tech1.token, id)).status;
    if (state === 'queued' || state === 'running') {
      await expect(own.getByRole('button', { name: 'Abbrechen' })).toBeVisible();
    }
    const finished = await untilFinished(tech1.token, id);
    expect(finished.status, 'the job ran to its end, the refused cancel did not stop it').toBe(
      'succeeded',
    );
  });

  test('J2 a client-2 user neither sees nor reads nor cancels the client-1 job', async () => {
    expect(client1Job, 'the client-1 job of J1').not.toBe('');
    await openJobCenter(tech2.page);
    const main = tech2.page.locator('#main-content');
    await expect(main.getByText(/Keine Jobs|Fortschritt/).first()).toBeVisible();
    await expect(jobRow(tech2.page, client1Job)).toHaveCount(0);
    const listed = await ok<List<Job>>(tech2.token, 'GET', '/jobs?limit=100');
    expect(rows(listed).map((j) => j.id)).not.toContain(client1Job);
    expect((await api(rq, tech2.token, 'GET', `/jobs/${client1Job}`)).status).toBe(404);
    expect((await api(rq, tech2.token, 'GET', `/jobs/${client1Job}/result`)).status).toBe(404);
    expect((await api(rq, tech2.token, 'DELETE', `/jobs/${client1Job}`)).status).toBe(404);
    // The organization's reader still sees it: the 404 is the client scope, not a lost job.
    expect((await api(rq, reader.token, 'GET', `/jobs/${client1Job}`)).status).toBe(200);
  });

  test('J3 a bulk job with 500 ids cancelled in the job center shows "Abgebrochen" and its partial report', async () => {
    test.setTimeout(5 * 60_000);
    const prefix = `jc3-${stamp()}`;
    const ids = insertCIs(prefix, BULK_IDS, client1, true);
    await tech1.page.goto('/dashboard');
    const id = await startBulk(ids, 'inactive');

    await openJobCenter(tech1.page);
    const row = jobRow(tech1.page, id);
    const cancel = row.getByRole('button', { name: 'Abbrechen' });
    await expect(cancel).toBeVisible();
    await untilRunning(tech1.token, id);
    const deleted = tech1.page.waitForResponse(
      (r) =>
        new URL(r.url()).pathname === `/api/v1/jobs/${id}` && r.request().method() === 'DELETE',
    );
    await cancel.click();
    expect((await deleted).status(), 'DELETE /jobs/{id} from the job center').toBe(200);

    // The row: "Abgebrochen", no cancel any more, the partial report.
    await expect(row).toContainText('Abgebrochen');
    await expect(tech1.page.getByText('Der Job wurde abgebrochen.')).toBeVisible();
    await expect(row.getByRole('button', { name: 'Abbrechen' })).toHaveCount(0);
    await expect(row).toContainText(
      /Teilbericht: \d+ von 500 erfolgreich, \d+ fehlgeschlagen, \d+ übersprungen/,
      { timeout: 30_000 },
    );

    // API: the job is cancelled, its partial report readable, fewer than 500 CIs changed.
    const job = await jobOf(tech1.token, id);
    expect(job.status).toBe('cancelled');
    const report = await api<BulkReport>(rq, tech1.token, 'GET', `/jobs/${id}/result`);
    expect(report.status, 'GET /jobs/{id}/result of the cancelled job').toBe(200);
    expect(report.body.cancelled).toBe(true);
    expect(report.body.total).toBe(BULK_IDS);
    expect(report.body.items).toHaveLength(BULK_IDS);
    expect(report.body.skipped ?? 0, 'skipped ids').toBeGreaterThan(0);
    expect(report.body.succeeded + report.body.failed + (report.body.skipped ?? 0)).toBe(BULK_IDS);
    const changed = await ok<List<{ id: string }>>(
      tech1.token,
      'GET',
      `/cis?search=${prefix}&status=inactive&limit=1`,
    );
    expect(changed.total, 'CIs changed before the cancel').toBeLessThan(BULK_IDS);
    expect(changed.total, 'the report names every changed CI').toBe(report.body.succeeded);
    await expect(row).toContainText(
      `Teilbericht: ${report.body.succeeded} von 500 erfolgreich, ${report.body.failed} fehlgeschlagen, ${report.body.skipped} übersprungen`,
    );

    // The partial report in the UI.
    await row.getByRole('button', { name: 'Teilbericht' }).click();
    const dialog = tech1.page.getByRole('dialog', { name: 'Teilbericht' });
    await expect(dialog).toContainText('"cancelled": true');
    await expect(dialog).toContainText(`"skipped": ${report.body.skipped}`);
    await tech1.page.keyboard.press('Escape');

    // A finished job cannot be cancelled again (409).
    expect((await api(rq, tech1.token, 'DELETE', `/jobs/${id}`)).status).toBe(409);
  });

  test('J4 an export of 40,000 rows cancelled in the job center leaves no file and the export page shows the cancel', async () => {
    test.setTimeout(5 * 60_000);
    await admin.page.goto('/dashboard');
    const started = await api<ExportJob>(rq, admin.token, 'POST', '/exports', { format: 'csv' });
    expect(started.status, `POST /exports: ${JSON.stringify(started.body)}`).toBe(202);
    const exportId = started.body.id;
    let mirrored: Job | undefined;
    await expect
      .poll(
        async () => {
          const listed = await ok<List<Job>>(admin.token, 'GET', '/jobs?kind=export&limit=100');
          mirrored = rows(listed).find((j) => j.ref_id === exportId);
          return Boolean(mirrored);
        },
        { message: 'the mirrored export job', timeout: 30_000, intervals: [200] },
      )
      .toBe(true);
    const id = mirrored!.id;

    await openJobCenter(admin.page);
    const row = jobRow(admin.page, id);
    await expect(row).toContainText('export');
    const cancel = row.getByRole('button', { name: 'Abbrechen' });
    await expect(cancel).toBeVisible();
    await untilRunning(admin.token, id);
    const deleted = admin.page.waitForResponse(
      (r) =>
        new URL(r.url()).pathname === `/api/v1/jobs/${id}` && r.request().method() === 'DELETE',
    );
    await cancel.click();
    expect((await deleted).status(), 'DELETE /jobs/{id} of the export').toBe(200);
    await expect(row).toContainText('Abgebrochen');
    await expect(row.getByRole('button', { name: 'Abbrechen' })).toHaveCount(0);

    // The export ends without a file; the mirrored job stays cancelled without result.
    let exp: ExportJob | undefined;
    await expect
      .poll(
        async () => {
          exp = await ok<ExportJob>(admin.token, 'GET', `/exports/${exportId}`);
          return exp.status;
        },
        { message: 'the export job ends', timeout: 120_000, intervals: [500] },
      )
      .toBe('failed');
    expect(exp!.error_message).toBe('cancelled by user');
    expect(exp!.download_url, 'no file of the cancelled export').toBeFalsy();
    expect((await jobOf(admin.token, id)).status).toBe('cancelled');
    expect((await api(rq, admin.token, 'GET', `/jobs/${id}/result`)).status).toBe(409);
    await expect(row.getByRole('button', { name: /Ergebnis|Teilbericht/ })).toHaveCount(0);

    // The export page shows the cancelled export (the newest, first row) without a download.
    await admin.page.goto('/export');
    const main = admin.page.locator('#main-content');
    const exportRow = main.getByRole('row').nth(1);
    await expect(exportRow).toContainText('Fehlgeschlagen');
    await expect(exportRow).toContainText('cancelled by user');
    await expect(exportRow.getByRole('link', { name: 'Herunterladen' })).toHaveCount(0);
  });
});
