import {
  test,
  expect,
  type APIRequestContext,
  type BrowserContext,
  type Page,
  type Response,
} from '@playwright/test';
import { api, signupTenant, stamp, uiLogin, BASE } from './support/stack';

/*
 * Index flags of attribute definitions in the CI type admin on the real stack (WP-511,
 * F-159, F-325), nothing mocked: UI → REST API → PostgreSQL (index job) → UI.
 *
 * Before WP-511 the field editor sent the flags after saving the field and ignored a
 * refused PATCH /attribute-definitions/{id}: the dialog stayed open without a word and
 * the field was stored without the flag (F-159). Flags of existing attributes could only
 * be changed through the REST API (F-325).
 *
 * One organization is created through the self-signup UI; its administrator creates a CI
 * type through the API.
 *
 * Test → requirement IDs proven:
 *  F1 a new field saved with "Eindeutig" in the UI: PUT and PATCH succeed, the field list
 *     shows the index job, the job succeeds (idx_attr_<id>_u, E-04), and a second CI with
 *     the value of the first is refused in the CMDB form with 409 at the attribute
 *     ................................................... MET-15, DB-04 (WP-142, WP-511)
 *  F2 an existing attribute whose value two CIs share: "Bearbeiten" shows its flags,
 *     "Eindeutig" is refused with the reason in the dialog (409, the attribute stays
 *     non-unique); "Durchsuchbar" saves with PATCH and the field list shows the queued
 *     index job with its status ................. MET-15, DB-04 (WP-142, WP-511, F-325)
 *
 * A failing test is a product finding: the assertions encode MET-15 and DB-04 in
 * docs/spec/katalog-v3 and the acceptance criteria of WP-511.
 */

interface List<T> {
  data: T[] | null;
}

interface Definition {
  id: string;
  key: string;
  searchable: boolean;
  unique: boolean;
  unique_scope: string;
}

interface Job {
  id: string;
  status: string;
  error?: string;
  params?: { definition_id?: string };
}

interface World {
  rq: APIRequestContext;
  ctx: BrowserContext;
  page: Page;
  token: string;
  s: string;
  typeId: string;
  typeName: string;
}

let w: World;

/** A documentation address (198.18.0.0/15) so the signup has its own budget (E-33). */
function realIp(): string {
  return `198.18.${Math.floor(Math.random() * 250) + 1}.${Math.floor(Math.random() * 250) + 1}`;
}

/** ok asserts a 2xx answer of a setup call and returns its body. */
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

function waitFor(page: Page, path: RegExp, method: string): Promise<Response> {
  return page.waitForResponse(
    (r) => path.test(new URL(r.url()).pathname) && r.request().method() === method,
  );
}

async function definition(key: string): Promise<Definition> {
  const list = await ok<List<Definition>>('GET', `/attribute-definitions?ci_type_id=${w.typeId}`);
  const def = (list.data ?? []).find((d) => d.key === key);
  expect(def, `definition ${key}`).toBeTruthy();
  return def!;
}

async function indexJobs(definitionId: string): Promise<Job[]> {
  const list = await ok<List<Job>>('GET', '/jobs?kind=attribute.index&limit=100');
  return (list.data ?? []).filter(
    (j) => j.params?.definition_id?.toLowerCase() === definitionId.toLowerCase(),
  );
}

/** openType opens the CI type admin on the type of this run. */
async function openType(page: Page) {
  await page.goto('/ci-types');
  await page
    .locator('#main-content')
    .getByRole('button', { name: new RegExp(`^${w.typeName}`) })
    .click();
  await expect(page.getByRole('button', { name: 'Feld hinzufügen' })).toBeVisible();
}

/** fieldRow is the entry of a field in the field list of the type. */
function fieldRow(page: Page, name: string) {
  return page.locator('#main-content li').filter({ hasText: name });
}

test.describe('CI type admin: index flags of attributes with visible refusals and job status', () => {
  test.beforeAll(async ({ browser, playwright }) => {
    test.setTimeout(4 * 60_000);
    const rq = await playwright.request.newContext();
    const ctx = await browser.newContext({
      baseURL: BASE,
      locale: 'de-DE',
      extraHTTPHeaders: { 'X-Real-IP': realIp() },
    });
    const page = await ctx.newPage();
    const account = await signupTenant(page, rq, 'attrflags');
    const token = await uiLogin(page, account);
    const s = stamp();
    w = { rq, ctx, page, token, s, typeId: '', typeName: `Flagtyp ${s}` };
    const created = await ok<{ id: string }>('POST', '/ci-types', { name: w.typeName });
    w.typeId = created.id;
  });

  test.afterAll(async () => {
    await w?.ctx.close();
    await w?.rq.dispose();
  });

  test('F1 a unique flag set in the UI builds the index; a second CI with the same value is refused at the field', async () => {
    test.setTimeout(5 * 60_000);
    const page = w.page;
    const key = `inv_${w.s}`;
    await openType(page);
    await page.getByRole('button', { name: 'Feld hinzufügen' }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByLabel('Feldname').fill(key);
    await dialog.getByLabel('Eindeutig', { exact: true }).check();
    const put = waitFor(page, new RegExp(`/api/v1/ci-types/${w.typeId}/fields$`), 'PUT');
    const patch = waitFor(page, /\/api\/v1\/attribute-definitions\/[^/]+$/, 'PATCH');
    await dialog.getByRole('button', { name: 'Speichern' }).click();
    expect((await put).status(), 'PUT field').toBe(200);
    const patched = await patch;
    expect(patched.status(), `PATCH flags: ${await patched.text()}`).toBe(200);
    expect(patched.request().postDataJSON()).toEqual({
      searchable: false,
      unique: true,
      unique_scope: 'org',
    });
    await expect(dialog).toBeHidden();

    // UI → API → DB: the definition is unique, its index job runs on the scheduler
    // (every minute) and the field list follows it to the end.
    const def = await definition(key);
    expect(def).toMatchObject({ unique: true, unique_scope: 'org' });
    const row = fieldRow(page, key);
    await expect(row.getByText('eindeutig', { exact: true })).toBeVisible();
    await expect(row.getByRole('status')).toContainText('Index-Job');
    await expect(row.getByRole('status')).toHaveText('Index-Job: abgeschlossen', {
      timeout: 3 * 60_000,
    });
    const jobs = await indexJobs(def.id);
    expect(jobs[0]?.status, `index job of ${def.id}: ${JSON.stringify(jobs)}`).toBe('succeeded');

    // The first CI holds the value; the CMDB form refuses the second at the attribute.
    await ok('POST', '/cis', {
      ci_type_id: w.typeId,
      name: `flags-a-${w.s}`,
      attributes: { [key]: `INV-${w.s}` },
    });
    await page.goto('/cmdb');
    await page.getByRole('button', { name: 'CI erstellen' }).first().click();
    const form = page.getByRole('dialog');
    await expect(form.getByRole('heading', { name: 'CI erstellen' })).toBeVisible();
    await form.getByLabel('Name', { exact: true }).fill(`flags-b-${w.s}`);
    await form.getByLabel('CI-Typ').selectOption({ label: w.typeName });
    const input = form.getByLabel(key, { exact: true });
    await input.fill(`INV-${w.s}`);
    const posted = waitFor(page, /^\/api\/v1\/cis$/, 'POST');
    await form.getByRole('button', { name: 'Anlegen' }).click();
    const refused = await posted;
    expect(refused.status(), `POST /cis with a taken value: ${await refused.text()}`).toBe(409);
    expect(await refused.json()).toMatchObject({
      violations: [expect.objectContaining({ field: key })],
    });
    await expect(form).toBeVisible();
    await expect(input).toHaveAttribute('aria-invalid', 'true');
    await expect(form.getByText('another CI already holds this value')).toBeVisible();
    const cis = await ok<List<{ name: string }>>('GET', `/cis?search=flags-b-${w.s}`);
    expect((cis.data ?? []).map((c) => c.name)).not.toContain(`flags-b-${w.s}`);
  });

  test('F2 editing the flags of an attribute with duplicates shows the refusal; searchable saves with PATCH and a queued job', async () => {
    const page = w.page;
    const key = `dup_${w.s}`;
    await ok('PUT', `/ci-types/${w.typeId}/fields`, { name: key, data_type: 'text' });
    for (const suffix of ['x', 'y']) {
      await ok('POST', '/cis', {
        ci_type_id: w.typeId,
        name: `flags-${suffix}-${w.s}`,
        attributes: { [key]: 'same' },
      });
    }
    const def = await definition(key);

    await openType(page);
    await fieldRow(page, key)
      .getByRole('button', { name: `Bearbeiten: ${key}` })
      .click();
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByRole('heading', { name: `Feld „${key}“ bearbeiten` })).toBeVisible();
    await expect(dialog.getByLabel('Durchsuchbar (indiziert)')).not.toBeChecked();
    await expect(dialog.getByLabel('Eindeutig', { exact: true })).not.toBeChecked();

    await dialog.getByLabel('Eindeutig', { exact: true }).check();
    let patch = waitFor(page, new RegExp(`/api/v1/attribute-definitions/${def.id}$`), 'PATCH');
    await dialog.getByRole('button', { name: 'Speichern' }).click();
    const refused = await patch;
    expect(refused.status(), `PATCH unique over duplicates: ${await refused.text()}`).toBe(409);
    await expect(dialog).toBeVisible();
    const alert = dialog.getByRole('alert');
    await expect(alert).toContainText('Index-Einstellungen nicht gespeichert');
    await expect(alert).toContainText('Eindeutigkeit nicht aktiviert');
    await expect(alert).toContainText('already share a value');
    expect(await definition(key)).toMatchObject({ unique: false });
    expect(await indexJobs(def.id), 'no index job for the refused flag').toHaveLength(0);

    // Searchable alone is accepted: PATCH, the dialog closes, the job is shown.
    await dialog.getByLabel('Eindeutig', { exact: true }).uncheck();
    await dialog.getByLabel('Durchsuchbar (indiziert)').check();
    patch = waitFor(page, new RegExp(`/api/v1/attribute-definitions/${def.id}$`), 'PATCH');
    await dialog.getByRole('button', { name: 'Speichern' }).click();
    const saved = await patch;
    expect(saved.status(), `PATCH searchable: ${await saved.text()}`).toBe(200);
    expect(saved.request().postDataJSON()).toEqual({
      searchable: true,
      unique: false,
      unique_scope: 'org',
    });
    await expect(dialog).toBeHidden();
    const row = fieldRow(page, key);
    await expect(row.getByText('durchsuchbar', { exact: true })).toBeVisible();
    await expect(row.getByRole('status')).toHaveText(/^Index-Job: (wartet|läuft|abgeschlossen)$/);
    const jobs = await indexJobs(def.id);
    expect(jobs.length, 'the index job of the definition is queued').toBe(1);
    expect(['queued', 'running', 'succeeded']).toContain(jobs[0]?.status);
  });
});
