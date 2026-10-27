import {
  test,
  expect,
  type APIRequestContext,
  type Browser,
  type BrowserContext,
  type Page,
} from '@playwright/test';
import { readFile } from 'node:fs/promises';
import {
  api,
  scopedUser,
  signupTenant,
  stamp,
  uiLogin,
  BASE,
  type Account,
  type ScopedUser,
} from './support/stack';
import { dbRows, uuid } from './support/locations-racks';

/*
 * Client- and site-scoped users on the real stack (WP-535), nothing mocked:
 * UI → REST API → PostgreSQL → UI.
 *
 * The real-stack suite covered the organization dimension only (tenant-isolation T01–T17,
 * roles-authz with organization-wide roles). Here one organization is created through the
 * self-signup UI. Its administrator creates two clients C1 and C2 with one CI each and,
 * through the REST API (support/stack.ts, scopedUser), a Keycloak user holding exactly the
 * standard role client_technician with scope_type client on C1, assigned with
 * POST /api/v1/role-assignments, and a second one on C2. CS04 adds two sites S1 and S2 of the
 * organization with one rack each (site → building → room → rack) and a user holding the
 * role viewer with scope_type site on S1. Database probes (dbRows, psql with
 * E2E_DATABASE_URL) prove that the hidden rows exist and that nothing was written.
 *
 * Test → WP / requirement IDs proven:
 *  CS01 the C1 user signs in; the CMDB list shows only the C1 CI, the URL of the C2 CI
 *       answers "not found" like an unknown id, search (command palette, API) and topology
 *       hold no C2 data; the C2 CI is in the database ......... WP-009, WP-010 (TEN-04, TEN-06)
 *  CS02 the relationship form of the C1 CI with the id of the C2 CI: 422 on target_ci_id,
 *       the field error in the UI language, the dialog stays open; no edge in the database
 *       ............................................... WP-010, WP-401 (TEN-06, REL-02; F-145)
 *  CS03 an export started by the C1 user on the export page: the downloaded file holds the
 *       C1 row only; the job center of the C2 user does not show the export job, his API
 *       answers 404 for it ................................ WP-035, WP-347 (EXP-01, TEN-09)
 *  CS04 the S1 user sees only the S1 rack in the location tree and on the racks page;
 *       GET /racks/{S2 rack} and its layout answer 404 with his session; the rack and location
 *       rows of the S2 rack remain (1/1); the organization-wide administrator sees both racks
 *       ................................................ WP-014, WP-208, WP-340 (TEN-09)
 *
 * A failing test is a product finding: the assertions encode TEN-04, TEN-06, TEN-09 and
 * EXP-01 of docs/spec/katalog-v3 and the acceptance criteria of WP-535. No test of this spec
 * is listed in tests/audit/expected-failures.txt.
 */

interface List<T> {
  data: T[] | null;
  total?: number;
}

interface CI {
  id: string;
  name: string;
  client_id?: string;
}

interface Named {
  id: string;
  name: string;
}

interface ExportJob {
  id: string;
  status: string;
  row_count?: number;
  initiated_by?: string;
  download_url?: string;
}

interface Job {
  id: string;
  kind: string;
  status: string;
  ref_id?: string;
}

interface Session {
  ctx: BrowserContext;
  page: Page;
  token: string;
}

interface World {
  rq: APIRequestContext;
  admin: Session & { account: Account; orgId: string };
  s: string;
  /** A word only the names of this run's CIs contain. */
  word: string;
  serverType: string;
  c1: Named;
  c2: Named;
  ci1: CI;
  ci2: CI;
  user1: ScopedUser;
  user2: ScopedUser;
  session1: Session;
}

let w: World;

/** A documentation address (198.18.0.0/15) so each signup and login has its own budget (E-33). */
function realIp(): string {
  return `198.18.${Math.floor(Math.random() * 250) + 1}.${Math.floor(Math.random() * 250) + 1}`;
}

function rows<T>(list: List<T> | null | undefined): T[] {
  return list?.data ?? [];
}

/** ok asserts a 2xx answer of a setup call as the administrator and returns its body. */
async function ok<T>(
  method: 'GET' | 'POST' | 'PATCH' | 'DELETE',
  path: string,
  data?: unknown,
): Promise<T> {
  const res = await api<T>(w.rq, w.admin.token, method, path, data);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeGreaterThanOrEqual(200);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeLessThan(300);
  return res.body;
}

async function newSession(
  browser: Browser,
  account: { email: string; password: string },
): Promise<Session> {
  const ctx = await browser.newContext({
    baseURL: BASE,
    locale: 'de-DE',
    extraHTTPHeaders: { 'X-Real-IP': realIp() },
  });
  const page = await ctx.newPage();
  const token = await uiLogin(page, account);
  return { ctx, page, token };
}

function claims(token: string): { sub: string; org: string } {
  return JSON.parse(Buffer.from(token.split('.')[1] ?? '', 'base64url').toString()) as {
    sub: string;
    org: string;
  };
}

/** waitForPath waits for the SPA's call of exactly this API path. */
function waitForPath(page: Page, path: string, method = 'GET') {
  return page.waitForResponse(
    (r) => new URL(r.url()).pathname === `/api/v1${path}` && r.request().method() === method,
  );
}

/** markersOfC2 are values only the data of client C2 contains. */
function markersOfC2(): string[] {
  return [w.c2.id, w.c2.name, w.ci2.id, w.ci2.name];
}

/** expectNoTraceOfC2 fails with the list of C2's values found in `body`. */
function expectNoTraceOfC2(where: string, body: unknown) {
  const text = typeof body === 'string' ? body : JSON.stringify(body);
  const found = markersOfC2().filter((m) => text.includes(m));
  expect(found, `${where} must not contain data of client C2`).toEqual([]);
}

async function bodyText(page: Page): Promise<string> {
  return page.locator('body').innerText();
}

/** ciRows reads the CIs of this run from the database (migration login, all clients). */
function ciRows() {
  return dbRows<{ id: string; client_id: string | null; deleted: boolean }>(
    `SELECT id::text, client_id::text, deleted_at IS NOT NULL AS deleted
       FROM ci WHERE organization_id = '${uuid(w.admin.orgId)}'
        AND id IN ('${uuid(w.ci1.id)}', '${uuid(w.ci2.id)}')
      ORDER BY name`,
  );
}

/** edgeRows reads every relationship of the organization touching the CIs of this run. */
function edgeRows() {
  return dbRows<{ id: string }>(
    `SELECT id::text FROM ci_relationship
      WHERE organization_id = '${uuid(w.admin.orgId)}'
        AND (source_ci_id IN ('${uuid(w.ci1.id)}', '${uuid(w.ci2.id)}')
          OR target_ci_id IN ('${uuid(w.ci1.id)}', '${uuid(w.ci2.id)}'))`,
  );
}

/** parseCsv splits an RFC 4180 text with the separator `sep` into rows of fields. */
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

test.describe.configure({ mode: 'serial' });

test.describe('client- and site-scoped users (TEN-04, TEN-06, TEN-09, EXP-01)', () => {
  test.beforeAll(async ({ browser, playwright }) => {
    test.setTimeout(5 * 60_000);
    const rq = await playwright.request.newContext();
    const ctx = await browser.newContext({
      baseURL: BASE,
      locale: 'de-DE',
      extraHTTPHeaders: { 'X-Real-IP': realIp() },
    });
    const page = await ctx.newPage();
    const account = await signupTenant(page, rq, 'clientscope');
    const token = await uiLogin(page, account);
    const s = stamp();
    w = {
      rq,
      admin: { ctx, page, token, account, orgId: claims(token).org },
      s,
      word: `cs${s}`,
    } as World;

    const types = await ok<List<{ id: string; key: string }>>('GET', '/ci-types?limit=200');
    w.serverType = rows(types).find((t) => t.key === 'server')?.id ?? '';
    expect(w.serverType, 'catalog type server').not.toBe('');
    w.c1 = await ok<Named>('POST', '/clients', {
      name: `CS Client Eins ${s}`,
      slug: `cs-client-1-${s}`,
    });
    w.c2 = await ok<Named>('POST', '/clients', {
      name: `CS Client Zwei ${s}`,
      slug: `cs-client-2-${s}`,
    });
    w.ci1 = await ok<CI>('POST', '/cis', {
      ci_type_id: w.serverType,
      name: `${w.word} Server Eins`,
      client_id: w.c1.id,
    });
    w.ci2 = await ok<CI>('POST', '/cis', {
      ci_type_id: w.serverType,
      name: `${w.word} Server Zwei`,
      client_id: w.c2.id,
    });
    expect(w.ci1.client_id).toBe(w.c1.id);
    expect(w.ci2.client_id).toBe(w.c2.id);

    // Positive control: the organization-wide administrator finds both CIs in the search
    // index, so a missing C2 hit of the C1 user is the scope and not a slow index.
    await expect
      .poll(
        async () => {
          const r = await api<List<{ entity_id: string }>>(
            rq,
            token,
            'GET',
            `/search?q=${encodeURIComponent(w.word)}`,
          );
          return rows(r.body)
            .map((h) => h.entity_id)
            .filter((id) => id === w.ci1.id || id === w.ci2.id)
            .sort();
        },
        { message: 'both CIs in the administrator’s search', timeout: 60_000 },
      )
      .toEqual([w.ci1.id, w.ci2.id].sort());

    w.user1 = await scopedUser(rq, token, {
      label: 'cs-c1',
      role: 'client_technician',
      scope_type: 'client',
      scope_id: w.c1.id,
      inviteClientId: w.c1.id,
    });
    w.user2 = await scopedUser(rq, token, {
      label: 'cs-c2',
      role: 'client_technician',
      scope_type: 'client',
      scope_id: w.c2.id,
      inviteClientId: w.c2.id,
    });
    // CS01: the login of the client-scoped user through the SPA and Keycloak.
    w.session1 = await newSession(browser, w.user1);
    expect(claims(w.session1.token)).toMatchObject({ sub: w.user1.userId, org: w.admin.orgId });
  });

  test.afterAll(async () => {
    await w?.session1?.ctx.close();
    await w?.admin?.ctx.close();
    await w?.rq.dispose();
  });

  test('CS01 the C1 user sees only the C1 CI in the CMDB list, search and topology; the C2 CI is "not found"', async () => {
    const page = w.session1.page;
    const token = w.session1.token;
    const main = page.locator('#main-content');

    // DB: both CIs exist, one per client.
    expect(ciRows()).toEqual([
      { id: w.ci1.id, client_id: w.c1.id, deleted: false },
      { id: w.ci2.id, client_id: w.c2.id, deleted: false },
    ]);

    // CMDB list in the UI: the search for the run's word returns the C1 CI only.
    const listed = page.waitForResponse(
      (r) =>
        new URL(r.url()).pathname === '/api/v1/cis' &&
        new URL(r.url()).searchParams.get('search') === w.word &&
        r.request().method() === 'GET',
    );
    await page.goto('/cmdb');
    await page.getByLabel('Configuration Items durchsuchen').fill(w.word);
    const listRes = await listed;
    expect(listRes.status(), 'CMDB list of the C1 user').toBe(200);
    expect(rows((await listRes.json()) as List<CI>).map((c) => c.id)).toEqual([w.ci1.id]);
    await expect(main.getByText(w.ci1.name, { exact: true })).toBeVisible();
    await expect(main.getByText(w.ci2.name, { exact: true })).toHaveCount(0);
    expectNoTraceOfC2('CMDB list of the C1 user', await bodyText(page));
    // The unfiltered list of the same session: exactly the C1 CI.
    const all = await api<List<CI>>(w.rq, token, 'GET', '/cis?limit=200');
    expect(all.status).toBe(200);
    expect(rows(all.body).map((c) => c.id)).toEqual([w.ci1.id]);
    expect(all.body.total).toBe(1);

    // The URL of the C2 CI answers "not found", exactly like an id that does not exist.
    const unknownId = '00000000-0000-4000-8000-0000000005f5';
    const alertOf = async (id: string) => {
      const detail = waitForPath(page, `/cis/${id}`);
      await page.goto(`/cmdb/${id}`);
      expect((await detail).status(), `GET /cis/${id} from the CI page`).toBe(404);
      const alert = main.getByRole('alert').filter({
        hasText: 'Das Configuration Item konnte nicht geladen werden',
      });
      await expect(alert).toBeVisible();
      return (await alert.innerText()).split(id).join('<id>');
    };
    const c2Alert = await alertOf(w.ci2.id);
    expectNoTraceOfC2('CI page of the C2 CI', (await bodyText(page)).split(w.ci2.id).join(''));
    expect(c2Alert, 'the C2 CI is indistinguishable from an unknown id').toBe(
      await alertOf(unknownId),
    );
    const byId = await api(w.rq, token, 'GET', `/cis/${w.ci2.id}`);
    expect(byId.status, 'GET /cis/{C2 CI} as the C1 user').toBe(404);
    expectNoTraceOfC2('GET /cis/{C2 CI}', JSON.stringify(byId.body).split(w.ci2.id).join(''));
    expect((await api(w.rq, token, 'GET', `/cis/${w.ci1.id}`)).status).toBe(200);

    // Search: the command palette finds the C1 CI and not the C2 CI; the API likewise.
    await page.goto('/cmdb');
    await expect(page.getByLabel('Configuration Items durchsuchen')).toBeVisible();
    await page.keyboard.press('Control+k');
    const box = page.getByRole('combobox', { name: 'Befehle durchsuchen' });
    await expect(box).toBeVisible();
    const searched = page.waitForResponse(
      (r) =>
        new URL(r.url()).pathname === '/api/v1/search' &&
        new URL(r.url()).searchParams.get('q') === w.word,
    );
    await box.fill(w.word);
    const searchRes = await searched;
    expect(searchRes.status(), 'search of the C1 user').toBe(200);
    const hits = rows((await searchRes.json()) as List<{ entity_id: string }>).map(
      (h) => h.entity_id,
    );
    expect(hits).toContain(w.ci1.id);
    expect(hits).not.toContain(w.ci2.id);
    await expect(page.getByRole('option', { name: new RegExp(w.ci1.name) })).toHaveCount(1);
    await expect(page.getByRole('option', { name: new RegExp(w.ci2.name) })).toHaveCount(0);
    expectNoTraceOfC2('command palette of the C1 user', await bodyText(page));
    await page.keyboard.press('Escape');
    for (const q of [w.word, w.ci2.name, w.c2.name]) {
      const r = await api(w.rq, token, 'GET', `/search?q=${encodeURIComponent(q)}`);
      expect(r.status, `GET /search?q=${q} as the C1 user`).toBe(200);
      expectNoTraceOfC2(`GET /search?q=${q} as the C1 user`, r.body);
    }

    // Topology: the page shows the C1 CI and nothing of C2; a view rooted at the C2 CI
    // answers "not found or not visible".
    const topo = waitForPath(page, '/topology');
    await page.goto('/topology');
    const topoRes = await topo;
    expect(topoRes.status(), 'GET /topology of the C1 user').toBe(200);
    const topoBody = await topoRes.text();
    expect(topoBody).toContain(w.ci1.id);
    expectNoTraceOfC2('GET /topology of the C1 user', topoBody);
    await expect(page.getByRole('heading', { name: 'Topologie' })).toBeVisible();
    await expect(
      main.getByText(w.ci1.name).filter({ visible: true }).first(),
      'the topology shows the C1 CI',
    ).toBeVisible();
    await expect(main.getByText(w.ci2.name)).toHaveCount(0);
    expectNoTraceOfC2('topology page of the C1 user', await bodyText(page));
    const rooted = waitForPath(page, '/topology');
    await page.goto(`/topology?root=${w.ci2.id}`);
    expect((await rooted).status(), 'GET /topology?root_ci_id={C2 CI}').toBe(404);
    await expect(main.getByText('CI nicht gefunden oder nicht sichtbar')).toBeVisible();
    expectNoTraceOfC2(
      'topology rooted at the C2 CI',
      (await bodyText(page)).split(w.ci2.id).join(''),
    );
  });

  test('CS02 the relationship form with the id of the C2 CI shows the 422 field error and stores no edge', async () => {
    const page = w.session1.page;
    expect(edgeRows(), 'no edge before').toEqual([]);
    await page.goto(`/cmdb/${w.ci1.id}`);
    await expect(page.getByRole('heading', { name: w.ci1.name })).toBeVisible();
    await page.getByRole('button', { name: /Beziehung hinzufügen/ }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toBeVisible();
    await dialog.getByLabel('Beziehungstyp').selectOption('depends_on');
    const target = dialog.getByLabel('CI-ID der Gegenstelle');
    await target.fill(w.ci2.id);
    const sent = waitForPath(page, '/relationships', 'POST');
    await dialog.getByRole('button', { name: 'Anlegen' }).click();
    const res = await sent;
    const problem = (await res.json()) as { violations?: { field: string }[] };
    expect(
      `${res.status()} ${(problem.violations ?? []).map((v) => v.field).join(',')}`,
      `POST /relationships to the C2 CI: ${JSON.stringify(problem)}`,
    ).toBe('422 target_ci_id');
    expectNoTraceOfC2('the refusal', JSON.stringify(problem).split(w.ci2.id).join(''));

    // The dialog stays open and names the error at the field, in the UI language.
    await expect(dialog).toBeVisible();
    await expect(target).toHaveAttribute('aria-invalid', 'true');
    await expect(dialog.getByText('Kein CI mit dieser ID gefunden.')).toBeVisible();
    await expect(dialog.getByText(/target CI not found/i)).toHaveCount(0);
    expectNoTraceOfC2('relationship form', (await bodyText(page)).split(w.ci2.id).join(''));
    await dialog.getByRole('button', { name: 'Abbrechen' }).click();
    await expect(dialog).toBeHidden();
    await expect(page.getByText('Keine Beziehungen vorhanden')).toBeVisible();

    // Nothing stored: the database holds no edge of either CI, the API lists none.
    expect(edgeRows(), 'no edge after the refusal').toEqual([]);
    const rels = await api<List<{ id: string }>>(
      w.rq,
      w.admin.token,
      'GET',
      `/cis/${w.ci1.id}/relationships`,
    );
    expect(rels.status).toBe(200);
    expect(rows(rels.body)).toEqual([]);
  });

  test('CS03 the export of the C1 user holds only C1 rows; the job center of the C2 user does not show it', async ({
    browser,
  }) => {
    const page = w.session1.page;
    const main = page.locator('#main-content');
    await page.goto('/export');
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
          const r = await api<ExportJob>(w.rq, w.session1.token, 'GET', `/exports/${job.id}`);
          expect(r.status, `GET /exports/${job.id}`).toBe(200);
          done = r.body;
          return done.status;
        },
        { message: 'export of the C1 user completes', timeout: 120_000, intervals: [1_000] },
      )
      .toBe('completed');
    expect(done!.initiated_by).toBe(w.user1.userId);
    expect(done!.row_count, 'one row: the C1 CI').toBe(1);
    expect(
      dbRows<{ status: string; row_count: number; initiated_by: string }>(
        `SELECT status, row_count, initiated_by::text FROM export_job
          WHERE organization_id = '${uuid(w.admin.orgId)}' AND id = '${uuid(job.id)}'`,
      ),
    ).toEqual([{ status: 'completed', row_count: 1, initiated_by: w.user1.userId }]);

    // UI: download the file from the job row on the export page.
    await page.reload();
    const jobRow = main.getByRole('row').filter({ hasText: 'Abgeschlossen' }).first();
    await expect(jobRow).toBeVisible();
    const downloadP = page.waitForEvent('download', { timeout: 30_000 });
    await jobRow.getByRole('link', { name: 'Herunterladen' }).click();
    const bytes = await readFile(await (await downloadP).path());
    expect([...bytes.subarray(0, 3)], 'UTF-8 BOM').toEqual([0xef, 0xbb, 0xbf]);
    const table = parseCsv(bytes.subarray(3).toString('utf8'), ';');
    const header = table[0] ?? [];
    const idCol = header.indexOf('id');
    expect(idCol, `id column in ${JSON.stringify(header)}`).toBeGreaterThanOrEqual(0);
    expect(
      table.slice(1).map((line) => line[idCol]),
      'the file holds exactly the C1 CI',
    ).toEqual([w.ci1.id]);
    const text = bytes.toString('utf8');
    expect(text).toContain(w.ci1.name);
    expectNoTraceOfC2('export file of the C1 user', text);

    // The export is a job of the C1 user's job center.
    let mirrored: Job | undefined;
    await expect
      .poll(
        async () => {
          const jobs = await api<List<Job>>(
            w.rq,
            w.session1.token,
            'GET',
            '/jobs?kind=export&limit=100',
          );
          expect(jobs.status).toBe(200);
          mirrored = rows(jobs.body).find((j) => j.ref_id === job.id);
          return mirrored?.status ?? 'not listed';
        },
        { message: 'the export as succeeded job in /jobs of the C1 user', timeout: 60_000 },
      )
      .toBe('succeeded');
    const ownCenter = waitForPath(page, '/jobs');
    await page.goto('/jobs');
    expect((await ownCenter).status()).toBe(200);
    await expect(main.locator(`tr[data-job-id="${mirrored!.id}"]`)).toContainText('Erfolgreich');

    // The C2 user: his job center does not show it, his API answers 404.
    const session2 = await newSession(browser, w.user2);
    try {
      const page2 = session2.page;
      expect(claims(session2.token).sub).toBe(w.user2.userId);
      const center = waitForPath(page2, '/jobs');
      await page2.goto('/jobs');
      const centerRes = await center;
      expect(centerRes.status(), 'GET /jobs of the C2 user').toBe(200);
      const listed = rows((await centerRes.json()) as List<Job>).map((j) => j.id);
      expect(listed).not.toContain(mirrored!.id);
      await expect(
        page2.locator('#main-content').getByRole('heading', { name: 'Jobcenter' }),
      ).toBeVisible();
      await expect(page2.locator(`tr[data-job-id="${mirrored!.id}"]`)).toHaveCount(0);
      for (const path of [
        `/jobs/${mirrored!.id}`,
        `/jobs/${mirrored!.id}/result`,
        `/exports/${job.id}`,
      ]) {
        const r = await api(w.rq, session2.token, 'GET', path);
        expect(r.status, `GET ${path} as the C2 user`).toBe(404);
      }
      for (const path of ['/jobs?limit=200', '/jobs?kind=export&limit=200', '/exports']) {
        const r = await api(w.rq, session2.token, 'GET', path);
        expect(r.status, `GET ${path} as the C2 user`).toBe(200);
        expect(JSON.stringify(r.body), `GET ${path} as the C2 user`).not.toContain(job.id);
        expect(JSON.stringify(r.body), `GET ${path} as the C2 user`).not.toContain(mirrored!.id);
      }
    } finally {
      await session2.ctx.close();
    }
  });

  test('CS04 the S1 user sees only the S1 rack in the location tree and on the racks page; the S2 rack is 404', async ({
    browser,
  }) => {
    test.setTimeout(4 * 60_000);
    const post = (body: Record<string, unknown>) => ok<Named>('POST', '/locations', body);
    const tree = async (tag: string) => {
      const site = await post({ kind: 'site', name: `CS ${tag} Site ${w.s}`, client_id: w.c1.id });
      const building = await post({
        kind: 'building',
        name: `CS ${tag} Gebäude ${w.s}`,
        parent_id: site.id,
      });
      const room = await post({
        kind: 'room',
        name: `CS ${tag} Raum ${w.s}`,
        parent_id: building.id,
      });
      const rack = await post({ kind: 'rack', name: `CS ${tag} Rack ${w.s}`, parent_id: room.id });
      return { site, building, room, rack };
    };
    const s1 = await tree('S1');
    const s2 = await tree('S2');
    const s2Markers = [s2.site, s2.building, s2.room, s2.rack].flatMap((n) => [n.id, n.name]);
    const expectNoTraceOfS2 = (where: string, body: unknown) => {
      const text = typeof body === 'string' ? body : JSON.stringify(body);
      expect(
        s2Markers.filter((m) => text.includes(m)),
        `${where} must not contain the S2 tree`,
      ).toEqual([]);
    };
    const rackOrLocation = () =>
      dbRows<{ rack: number; location: number }>(
        `SELECT (SELECT count(*) FROM rack WHERE organization_id = '${uuid(w.admin.orgId)}'
                   AND id = '${uuid(s2.rack.id)}')::int AS rack,
                (SELECT count(*) FROM location WHERE organization_id = '${uuid(w.admin.orgId)}'
                   AND id = '${uuid(s2.rack.id)}')::int AS location`,
      );
    expect(rackOrLocation(), 'rack and location row of the S2 rack').toEqual([
      { rack: 1, location: 1 },
    ]);

    // Positive control: the organization-wide administrator sees both racks.
    const adminRacks = await ok<List<Named>>('GET', '/racks?limit=100');
    expect(rows(adminRacks).map((r) => r.id)).toEqual(
      expect.arrayContaining([s1.rack.id, s2.rack.id]),
    );
    const adminPage = w.admin.page;
    await adminPage.goto('/racks');
    const adminSelect = adminPage.getByLabel('Rack auswählen');
    await expect(adminSelect.locator('option', { hasText: `${s1.rack.name} (42 HE)` })).toHaveCount(
      1,
    );
    await expect(adminSelect.locator('option', { hasText: `${s2.rack.name} (42 HE)` })).toHaveCount(
      1,
    );

    const siteUser = await scopedUser(w.rq, w.admin.token, {
      label: 'cs-s1',
      role: 'viewer',
      scope_type: 'site',
      scope_id: s1.site.id,
      inviteClientId: w.c1.id,
    });
    const session = await newSession(browser, siteUser);
    try {
      const page = session.page;
      const main = page.locator('#main-content');
      expect(claims(session.token).sub).toBe(siteUser.userId);

      // Location tree: S1 down to its rack, nothing of S2.
      const loaded = waitForPath(page, '/locations/tree');
      await page.goto('/locations');
      const treeRes = await loaded;
      expect(treeRes.status(), 'GET /locations/tree of the S1 user').toBe(200);
      const treeBody = await treeRes.text();
      expect(treeBody).toContain(s1.rack.id);
      expectNoTraceOfS2('GET /locations/tree of the S1 user', treeBody);
      const node = (name: string) =>
        main.locator('li > div').filter({ has: page.getByText(name, { exact: true }) });
      const child = (parent: string, name: string) =>
        node(parent)
          .locator('xpath=following-sibling::ul')
          .locator(':scope > li > div')
          .filter({ has: page.getByText(name, { exact: true }) });
      await expect(node(s1.site.name)).toBeVisible();
      await expect(child(s1.site.name, s1.building.name)).toBeVisible();
      await expect(child(s1.building.name, s1.room.name)).toBeVisible();
      await node(s1.room.name).getByRole('button', { name: 'expand' }).click();
      await expect(child(s1.room.name, s1.rack.name)).toBeVisible();
      for (const n of [s2.site, s2.building, s2.room, s2.rack]) {
        await expect(main.getByText(n.name, { exact: true })).toHaveCount(0);
      }
      expectNoTraceOfS2('location tree page of the S1 user', await bodyText(page));

      // Racks page: the S1 rack is the only one offered.
      const racks = waitForPath(page, '/racks');
      await page.goto('/racks');
      const racksRes = await racks;
      expect(racksRes.status(), 'GET /racks of the S1 user').toBe(200);
      expect(rows((await racksRes.json()) as List<Named>).map((r) => r.id)).toEqual([s1.rack.id]);
      const select = page.getByLabel('Rack auswählen');
      await expect(select.locator('option')).toHaveCount(1);
      await expect(select.locator('option')).toHaveText(`${s1.rack.name} (42 HE)`);
      await expect(
        page.getByRole('heading', { name: `${s1.rack.name} · Vorderseite` }),
      ).toBeVisible();
      expectNoTraceOfS2('racks page of the S1 user', await bodyText(page));

      // The S2 rack by id with the user's session: 404 for the rack and its layout.
      for (const path of [`/racks/${s2.rack.id}`, `/racks/${s2.rack.id}/layout`]) {
        const r = await api(w.rq, session.token, 'GET', path);
        expect(r.status, `GET ${path} as the S1 user`).toBe(404);
        expectNoTraceOfS2(
          `GET ${path} as the S1 user`,
          JSON.stringify(r.body).split(s2.rack.id).join(''),
        );
      }
      expect((await api(w.rq, session.token, 'GET', `/racks/${s1.rack.id}/layout`)).status).toBe(
        200,
      );
    } finally {
      await session.ctx.close();
    }

    // DB: the rows of the S2 rack remain (1/1); the administrator still reads it.
    expect(rackOrLocation(), 'rack and location row of the S2 rack after the attempts').toEqual([
      { rack: 1, location: 1 },
    ]);
    expect((await api(w.rq, w.admin.token, 'GET', `/racks/${s2.rack.id}/layout`)).status).toBe(200);
  });
});
