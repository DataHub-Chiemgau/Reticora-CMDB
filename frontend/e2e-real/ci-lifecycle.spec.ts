import {
  test,
  expect,
  type APIRequestContext,
  type Browser,
  type BrowserContext,
  type Page,
  type Response,
  type Route,
} from '@playwright/test';
import { execFileSync } from 'node:child_process';
import { readFile } from 'node:fs/promises';
import {
  api,
  signupTenant,
  stamp,
  uiLogin,
  BASE,
  DATABASE_URL,
  type Account,
} from './support/stack';

/*
 * Phase-1 acceptance audit – the core CMDB process of a user, end to end on the real
 * stack (nothing mocked): UI → REST API → PostgreSQL → UI.
 *
 * One organization is created through the self-signup UI (beforeAll; Playwright repeats it
 * in a fresh worker after a failed test). Every test creates
 * its own CIs with unique names, so a test proves its claim on its own; where the subject
 * of a test is not the creation itself, the fixture CIs are created through the REST API
 * the UI uses (POST /api/v1/cis), so a broken form does not hide the later steps.
 *
 * Test → WP (requirement IDs) proven:
 *  L01  create a CI through the CMDB form, open it ........... WP-120 (CI-01), WP-117 (CI-03), WP-201 (TST-03)
 *  L01b runtime org type in the form, no reload; not in B .... WP-119 (MET-04), WP-117 (CI-03), WP-397
 *  L02  required-field / format validation, nothing stored .... WP-117 (CI-03, MET-11), WP-163 (API-03), WP-201 (TST-03)
 *  L03a double click on the create button of the form ......... WP-201 (TST-03), WP-120 (CI-01)
 *  L03b Idempotency-Key replay, conflicting reuse, parallel ... WP-050 (API-04)
 *  L04  edit on the detail page, reload, persisted, If-Match .. WP-061 (API-07), WP-057 (CI-04, OVR-01), WP-201 (TST-03)
 *  L05  clearing a field in the edit form is persisted ........ WP-057 (CI-04), WP-201 (TST-03)
 *  L05b merge patch: null removes a typed field (RFC 7396) .... WP-057 (CI-04)
 *  L06  history and audit trail of the edit with the actor .... WP-171 (CI-06, AUD-03), WP-133 (AUD-05), WP-134 (AUD-07), WP-132 (AUD-04)
 *  L07  audit viewer UI shows the entry; chain verifies ....... WP-171 (CI-06), WP-133 (AUD-05), WP-132 (AUD-04), WP-191/WP-192 (UI-01..10 audit viewer)
 *  L08  relationship in the UI (double click), neighbours, impact WP-128 (REL-02), WP-127 (REL-01), WP-176 (IMP-01), WP-177 (IMP-02), WP-201 (TST-03)
 *  L09  list filter, full-text search, command palette ........ WP-032 (SRC-01), WP-165 (API-02), WP-191/WP-192 (UI-01..10 palette), WP-201 (TST-03)
 *  L10  export job in the UI, download contains the CI ........ WP-189 (EXP-01), WP-035 (EXP-01), WP-201 (TST-03)
 *  L10b CSV export button of the CMDB list .................... WP-189 (EXP-01), WP-191/WP-192 (UI-01..10 CI list), WP-201 (TST-03)
 *  L11  delete and restore: status, traversal, search, history  WP-170 (CI-05, LCY-01), WP-169 (API-05, CI-05), WP-031 (CI-05), WP-171 (AUD-03), WP-133 (AUD-05)
 *  L12  concurrent edit in two browser sessions, no lost update WP-061 (API-07, API-03)
 *  L12b two sessions, different fields: no loss, field merge .. WP-061 (API-07), WP-057 (CI-04)
 *  L13  ETag / If-Match contract and the org If-Match setting . WP-061 (API-07, API-03), WP-163 (API-03)
 *  L13b instance attributes save with require_if_match ........ WP-061 (API-07, API-03), WP-477 (F-082)
 *  L14  quotes / backslashes in names and fields are stored ... WP-117 (CI-03, MET-11), WP-120 (CI-01), WP-132 (AUD-04)
 *
 * The UI offers no action to delete or restore a CI (useDeleteCI is not used by any page);
 * L11 therefore drives DELETE and restore through the REST API and checks the effect in the UI.
 *
 * L05 reads the stored column with one read-only psql query (E2E_DATABASE_URL), since the
 * API renders NULL and an empty string alike.
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
  version: number;
  is_manual: boolean;
  discovery_source?: string;
  manufacturer?: string;
  model?: string;
  serial_number?: string;
  management_ip?: string;
  firmware_version?: string;
  attributes?: Record<string, unknown>;
  deleted_at?: string;
}

interface List<T> {
  data: T[] | null;
  total: number;
}

interface HistoryEntry {
  id: string;
  entity_id: string;
  actor_id?: string;
  change_type: string;
  field_name?: string;
  old_value?: unknown;
  new_value?: unknown;
  source?: string;
  created_at: string;
}

interface AuditEntry {
  id: string;
  seq: number;
  timestamp: string;
  actor_id?: string;
  actor_type: string;
  action: string;
  resource_type: string;
  resource_id?: string;
  changes?: Record<string, unknown>;
  source: string;
  request_id?: string;
}

interface SearchHit {
  entity_type: string;
  entity_id: string;
  title: string;
  url: string;
}

interface ExportJob {
  id: string;
  status: string;
  row_count?: number;
  download_url?: string;
}

interface World {
  rq: APIRequestContext;
  ctx: BrowserContext;
  page: Page;
  account: Account;
  token: string;
  orgId: string;
  userId: string;
  serverType: string;
  s: string;
}

let w: World;

const NOT_FOUND = [404, 410];

function rows<T>(list: List<T> | undefined): T[] {
  return list?.data ?? [];
}

function claims(token: string): { sub: string; org: string } {
  return JSON.parse(Buffer.from(token.split('.')[1], 'base64url').toString()) as {
    sub: string;
    org: string;
  };
}

/** A documentation address (198.18.0.0/15) so the signup has its own budget (E-33). */
function realIp(): string {
  return `198.18.${Math.floor(Math.random() * 250) + 1}.${Math.floor(Math.random() * 250) + 1}`;
}

/** ok asserts a 2xx answer of a setup call and returns its body. */
async function ok<T>(
  method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE',
  path: string,
  data?: unknown,
  headers: Record<string, string> = {},
): Promise<T> {
  const res = await api<T>(w.rq, w.token, method, path, data, headers);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeGreaterThanOrEqual(200);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeLessThan(300);
  return res.body;
}

/** raw calls the API and also returns the response headers (ETag, content type). */
async function raw<T = unknown>(
  method: 'GET' | 'POST' | 'PATCH' | 'DELETE',
  path: string,
  data?: unknown,
  headers: Record<string, string> = {},
): Promise<{ status: number; headers: Record<string, string>; body: T }> {
  const res = await w.rq.fetch(`${BASE}/api/v1${path}`, {
    method,
    headers: { Authorization: `Bearer ${w.token}`, ...headers },
    data,
    failOnStatusCode: false,
  });
  const text = await res.text();
  let body: unknown = text;
  try {
    body = text ? JSON.parse(text) : null;
  } catch {
    // keep the raw text
  }
  return { status: res.status(), headers: res.headers(), body: body as T };
}

/** newCI creates a CI through the REST API the CMDB form uses. */
async function newCI(body: Record<string, unknown>): Promise<CI> {
  return ok<CI>('POST', '/cis', { ci_type_id: w.serverType, ...body });
}

async function getCI(id: string): Promise<CI> {
  return ok<CI>('GET', `/cis/${id}`);
}

/** ciCount counts the CIs of the tenant whose name contains `needle`. */
async function ciCount(needle: string): Promise<number> {
  const list = await ok<List<CI>>('GET', `/cis?search=${encodeURIComponent(needle)}&limit=200`);
  return rows(list).filter((c) => c.name.includes(needle)).length;
}

function waitForPath(page: Page, path: string, method: string): Promise<Response> {
  return page.waitForResponse(
    (r) => new URL(r.url()).pathname === `/api/v1${path}` && r.request().method() === method,
  );
}

/** detailValue is the value cell next to a label of the CI overview. */
function detailValue(page: Page, label: string) {
  return page
    .locator('dt')
    .filter({ hasText: new RegExp(`^${label}$`) })
    .first()
    .locator('xpath=following-sibling::dd[1]');
}

function escapeRe(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

/** pollHistory waits until the unified history of a CI has an entry matching `match`. */
async function pollHistory(
  ciId: string,
  match: (e: HistoryEntry) => boolean,
  message: string,
): Promise<HistoryEntry> {
  let found: HistoryEntry | undefined;
  await expect
    .poll(
      async () => {
        const h = await api<List<HistoryEntry>>(
          w.rq,
          w.token,
          'GET',
          `/history/ci/${ciId}?limit=200`,
        );
        found = h.status === 200 ? rows(h.body).find(match) : undefined;
        return Boolean(found);
      },
      { message, timeout: 30_000, intervals: [1_000] },
    )
    .toBe(true);
  return found!;
}

/** pollAudit waits until the audit trail has an entry of `action` about the CI. */
async function pollAudit(ciId: string, action: string): Promise<AuditEntry> {
  let found: AuditEntry | undefined;
  await expect
    .poll(
      async () => {
        const a = await api<List<AuditEntry>>(
          w.rq,
          w.token,
          'GET',
          `/audit-log?resource_type=ci&resource_id=${ciId}&limit=200`,
        );
        found = a.status === 200 ? rows(a.body).find((e) => e.action === action) : undefined;
        return Boolean(found);
      },
      { message: `audit entry ${action} for CI ${ciId}`, timeout: 60_000, intervals: [1_000] },
    )
    .toBe(true);
  return found!;
}

/**
 * db runs ONE read-only query against the stack database with psql variables
 * (:'name') and returns its rows as JSON objects.
 */
function db<T>(select: string, vars: Record<string, string>): T[] {
  expect(/^\s*select\b/i.test(select), 'read-only query').toBe(true);
  const args = [DATABASE_URL, '-X', '-At', '-v', 'ON_ERROR_STOP=1'];
  for (const [name, value] of Object.entries(vars)) args.push('-v', `${name}=${value}`);
  args.push('-f', '-');
  const out = execFileSync('psql', args, {
    input: `SELECT COALESCE(json_agg(q), '[]'::json) FROM (${select}) q;`,
    encoding: 'utf8',
    timeout: 15_000,
  }).trim();
  return JSON.parse(out) as T[];
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
  return out;
}

async function openSession(
  browser: Browser,
  account: Account,
): Promise<{
  ctx: BrowserContext;
  page: Page;
  token: string;
}> {
  const ctx = await browser.newContext({
    baseURL: BASE,
    locale: 'de-DE',
    extraHTTPHeaders: { 'X-Real-IP': realIp() },
  });
  const page = await ctx.newPage();
  const token = await uiLogin(page, account);
  return { ctx, page, token };
}

async function openCreateForm(page: Page) {
  await page.goto('/cmdb');
  await page.getByRole('button', { name: 'CI erstellen' }).first().click();
  const dialog = page.getByRole('dialog');
  await expect(dialog.getByRole('heading', { name: 'CI erstellen' })).toBeVisible();
  return dialog;
}

test.describe('CI lifecycle: create, edit, history, audit, relations, search, export, delete', () => {
  test.beforeAll(async ({ browser, playwright }) => {
    test.setTimeout(4 * 60_000);
    const rq = await playwright.request.newContext();
    const ctx = await browser.newContext({
      baseURL: BASE,
      locale: 'de-DE',
      extraHTTPHeaders: { 'X-Real-IP': realIp() },
    });
    const page = await ctx.newPage();
    const account = await signupTenant(page, rq, 'cilife');
    const token = await uiLogin(page, account);
    const c = claims(token);
    w = {
      rq,
      ctx,
      page,
      account,
      token,
      orgId: c.org,
      userId: c.sub,
      serverType: '',
      s: stamp(),
    };
    const me = await ok<{ sub: string; org: string }>('GET', '/me');
    expect(me.sub, 'the session subject is the signed-in user').toBe(c.sub);
    expect(me.org, 'the session organization').toBe(c.org);
    const types = await ok<List<{ id: string; key: string }>>('GET', '/ci-types?limit=200');
    w.serverType = rows(types).find((t) => t.key === 'server')?.id ?? '';
    expect(w.serverType, 'catalog type server').not.toBe('');
  });

  test.afterAll(async () => {
    await w?.ctx.close();
    await w?.rq.dispose();
  });

  test('L01 a CI created through the CMDB form is stored with all fields in the own organization and opens on its detail page', async () => {
    const page = w.page;
    const name = `lc-form-${w.s}`;
    const dialog = await openCreateForm(page);
    await dialog.getByLabel('Name', { exact: true }).fill(name);
    await dialog.getByLabel('CI-Typ').selectOption({ label: 'Server' });
    await dialog.getByLabel('Status').selectOption({ label: 'Wartung' });
    await dialog.getByLabel('Hersteller').fill(`Vendor ${w.s}`);
    await dialog.getByLabel('Modell').fill('PowerEdge R740');
    await dialog.getByLabel('Seriennummer').fill(`SN-LC-${w.s}`);
    await dialog.getByLabel('Management-IP').fill('10.20.30.40');
    await dialog.getByLabel('Firmware-Version').fill('2.1.0');
    const created = waitForPath(page, '/cis', 'POST');
    await dialog.getByRole('button', { name: 'Anlegen' }).click();
    const res = await created;
    expect(res.status(), `POST /cis sent by the CMDB form: ${await res.text()}`).toBe(201);
    const ci = (await res.json()) as CI;
    await expect(dialog).toBeHidden();

    // UI → API → DB: every declared field of the form is stored (CI-01, CI-03),
    // the CI is manual, in the own organization, at version 1.
    const stored = await getCI(ci.id);
    expect(stored).toMatchObject({
      organization_id: w.orgId,
      ci_type_id: w.serverType,
      name,
      status: 'maintenance',
      manufacturer: `Vendor ${w.s}`,
      model: 'PowerEdge R740',
      serial_number: `SN-LC-${w.s}`,
      management_ip: '10.20.30.40',
      firmware_version: '2.1.0',
      discovery_source: 'manual',
      is_manual: true,
      version: 1,
    });

    // DB → UI: the list shows it, the detail page opens with the stored values.
    const main = page.locator('#main-content');
    await expect(main.getByRole('link', { name, exact: true })).toBeVisible();
    await main.getByRole('link', { name, exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`/cmdb/${ci.id}$`));
    await expect(page.getByRole('heading', { name, exact: true })).toBeVisible();
    await expect(detailValue(page, 'ID')).toHaveText(ci.id);
    await expect(detailValue(page, 'Hersteller')).toHaveText(`Vendor ${w.s}`);
    await expect(detailValue(page, 'Modell')).toHaveText('PowerEdge R740');
    await expect(detailValue(page, 'Seriennummer')).toHaveText(`SN-LC-${w.s}`);
    await expect(detailValue(page, 'Management-IP')).toHaveText('10.20.30.40');
    await expect(detailValue(page, 'Firmware')).toHaveText('2.1.0');
    await expect(main.getByText('Wartung', { exact: true }).first()).toBeVisible();
  });

  test('L01b an organization type created at runtime is offered by the CMDB form without a reload, the CI is stored with its attribute, another organization is not offered the type', async ({
    browser,
  }) => {
    test.setTimeout(4 * 60_000);
    const page = w.page;
    const label = `Laufzeittyp ${w.s}`;

    // The SPA is loaded and the dialog has read the types before the new type exists.
    let dialog = await openCreateForm(page);
    let typeSelect = dialog.getByLabel('CI-Typ');
    await expect(typeSelect.locator('option', { hasText: 'Server' }).first()).toBeAttached();
    await expect(typeSelect.locator('option', { hasText: label })).toHaveCount(0);
    await dialog.getByRole('button', { name: 'Abbrechen' }).click();
    await expect(dialog).toBeHidden();
    await page.evaluate(() => {
      (window as unknown as { l01bLoaded?: boolean }).l01bLoaded = true;
    });

    // MET-04: an organization type with an attribute, created at runtime through the API.
    const created = await ok<{ id: string; organization_id?: string; is_active: boolean }>(
      'POST',
      '/ci-types',
      {
        key: `lc_runtime_${w.s}`,
        name: `lc-runtime-${w.s}`,
        display_name: { de: label, en: `Runtime type ${w.s}` },
        fields: [
          { name: 'rack_units', label: 'Höheneinheiten', data_type: 'integer', required: true },
        ],
      },
    );
    expect(created.organization_id, 'the type belongs to the own organization').toBe(w.orgId);
    expect(created.is_active).toBe(true);

    // No reload, no release: the dialog offers the type, renders its attribute
    // and posts the type id (CI-03) with the attribute value.
    await page.getByRole('button', { name: 'CI erstellen' }).first().click();
    dialog = page.getByRole('dialog');
    await expect(dialog.getByRole('heading', { name: 'CI erstellen' })).toBeVisible();
    const name = `lc-runtime-ci-${w.s}`;
    await dialog.getByLabel('Name', { exact: true }).fill(name);
    typeSelect = dialog.getByLabel('CI-Typ');
    await typeSelect.selectOption({ label });
    await dialog.getByLabel('Höheneinheiten').fill('2');
    const posted = waitForPath(page, '/cis', 'POST');
    await dialog.getByRole('button', { name: 'Anlegen' }).click();
    const res = await posted;
    expect(res.status(), `POST /cis with the runtime type: ${await res.text()}`).toBe(201);
    expect(res.request().postDataJSON()).toMatchObject({
      ci_type_id: created.id,
      attributes: { rack_units: 2 },
    });
    const ci = (await res.json()) as CI;
    await expect(dialog).toBeHidden();
    expect(
      await page.evaluate(() => (window as unknown as { l01bLoaded?: boolean }).l01bLoaded),
      'the SPA was not reloaded',
    ).toBe(true);
    const stored = (await getCI(ci.id)) as CI & { attributes?: Record<string, unknown> };
    expect(stored).toMatchObject({
      organization_id: w.orgId,
      ci_type_id: created.id,
      name,
      is_manual: true,
    });
    expect(stored.attributes?.rack_units).toBe(2);

    // Negative: another organization neither lists nor offers the type.
    const ctxB = await browser.newContext({
      baseURL: BASE,
      locale: 'de-DE',
      extraHTTPHeaders: { 'X-Real-IP': realIp() },
    });
    try {
      const pageB = await ctxB.newPage();
      const accountB = await signupTenant(pageB, w.rq, 'cilifeb');
      const tokenB = await uiLogin(pageB, accountB);
      expect(claims(tokenB).org, 'a second organization').not.toBe(w.orgId);
      const typesB = await api<List<{ id: string }>>(w.rq, tokenB, 'GET', '/ci-types?limit=200');
      expect(typesB.status).toBe(200);
      expect(rows(typesB.body).map((t) => t.id)).not.toContain(created.id);
      const dialogB = await openCreateForm(pageB);
      const typeSelectB = dialogB.getByLabel('CI-Typ');
      await expect(typeSelectB.locator('option', { hasText: 'Server' }).first()).toBeAttached();
      await expect(typeSelectB.locator('option', { hasText: label })).toHaveCount(0);
      await expect(typeSelectB.locator(`option[value="${created.id}"]`)).toHaveCount(0);
    } finally {
      await ctxB.close();
    }
  });

  test('L02 required-field and format errors are shown in the form and nothing is stored', async () => {
    const page = w.page;
    const name = `lc-invalid-${w.s}`;
    const posts: string[] = [];
    const onRequest = (r: { url(): string; method(): string }) => {
      if (new URL(r.url()).pathname === '/api/v1/cis' && r.method() === 'POST') posts.push(r.url());
    };
    page.on('request', onRequest);
    try {
      const dialog = await openCreateForm(page);
      const nameInput = dialog.getByLabel('Name', { exact: true });
      const typeSelect = dialog.getByLabel('CI-Typ');

      // 1) Empty form: the user is told that name and type are required; no request.
      await dialog.getByRole('button', { name: 'Anlegen' }).click();
      await expect(dialog).toBeVisible();
      const requiredMessage = async (field: typeof nameInput, text: string): Promise<string> => {
        // Either the form's own message (aria-invalid + text) or the browser's
        // constraint validation message of the required field.
        const own = await dialog.getByText(text, { exact: true }).isVisible();
        if (own) return text;
        return field.evaluate((el) => (el as HTMLInputElement).validationMessage);
      };
      expect(await requiredMessage(nameInput, 'Name ist erforderlich.')).not.toBe('');
      await nameInput.fill(name);
      await dialog.getByRole('button', { name: 'Anlegen' }).click();
      expect(await requiredMessage(typeSelect, 'CI-Typ ist erforderlich.')).not.toBe('');
      expect(posts, 'no POST /cis for an incomplete form').toHaveLength(0);

      // 2) A name of blanks only is no name.
      await nameInput.fill('   ');
      await typeSelect.selectOption({ label: 'Server' });
      await dialog.getByRole('button', { name: 'Anlegen' }).click();
      await expect(dialog.getByText('Name ist erforderlich.', { exact: true })).toBeVisible();
      await expect(nameInput).toHaveAttribute('aria-invalid', 'true');
      expect(posts, 'no POST /cis for a blank name').toHaveLength(0);

      // 3) Format: an address that is no IP is refused by the server as a field
      //    violation (422, RFC 7807) and shown at the Management-IP field.
      await nameInput.fill(name);
      const ipInput = dialog.getByLabel('Management-IP');
      await ipInput.fill('300.1.2.3');
      const refused = waitForPath(page, '/cis', 'POST');
      await dialog.getByRole('button', { name: 'Anlegen' }).click();
      const res = await refused;
      const text = await res.text();
      expect(res.status(), `invalid management IP: ${text}`).toBe(422);
      expect(res.headers()['content-type'] ?? '').toContain('application/problem+json');
      const problem = JSON.parse(text) as {
        trace_id?: string;
        violations?: { field: string }[];
      };
      expect(problem.trace_id, 'problem carries a trace_id (API-03)').toBeTruthy();
      expect((problem.violations ?? []).map((v) => v.field)).toContain('management_ip');
      await expect(dialog, 'the form stays open on a refusal').toBeVisible();
      await expect(ipInput).toHaveAttribute('aria-invalid', 'true');
      await expect(ipInput).toHaveAccessibleDescription(/.+/);

      // 4) Format: an IP with letters, same rule.
      await ipInput.fill('10.0.0.x');
      const refused2 = waitForPath(page, '/cis', 'POST');
      await dialog.getByRole('button', { name: 'Anlegen' }).click();
      expect((await refused2).status()).toBe(422);
      await expect(ipInput).toHaveAttribute('aria-invalid', 'true');

      await dialog.getByRole('button', { name: 'Abbrechen' }).click();
      await expect(dialog).toBeHidden();
    } finally {
      page.off('request', onRequest);
    }

    // DB: nothing was stored, neither through the form nor partially.
    expect(await ciCount(name)).toBe(0);
    // API: a CI without name or type is a client error as well (CI-03).
    const noName = await api(w.rq, w.token, 'POST', '/cis', { ci_type_id: w.serverType });
    expect(noName.status).toBeGreaterThanOrEqual(400);
    expect(noName.status).toBeLessThan(500);
    const badType = await api(w.rq, w.token, 'POST', '/cis', {
      name,
      ci_type_id: '00000000-0000-0000-0000-00000000beef',
    });
    expect(
      badType.status,
      `unknown CI type: ${JSON.stringify(badType.body)}`,
    ).toBeGreaterThanOrEqual(400);
    expect(badType.status).toBeLessThan(500);
    expect(await ciCount(name)).toBe(0);
  });

  test('L03a a double click on the create button of the CMDB form creates exactly one CI', async () => {
    const page = w.page;
    const name = `lc-double-${w.s}`;
    const statuses: number[] = [];
    const onResponse = (r: Response) => {
      if (new URL(r.url()).pathname === '/api/v1/cis' && r.request().method() === 'POST') {
        statuses.push(r.status());
      }
    };
    page.on('response', onResponse);
    try {
      const dialog = await openCreateForm(page);
      await dialog.getByLabel('Name', { exact: true }).fill(name);
      await dialog.getByLabel('CI-Typ').selectOption({ label: 'Server' });
      await dialog.getByRole('button', { name: 'Anlegen' }).dblclick();
      await expect(dialog, 'the form closes after the create').toBeHidden({ timeout: 10_000 });
      await expect(
        page.locator('#main-content').getByRole('link', { name, exact: true }),
      ).toHaveCount(1);
    } finally {
      page.off('response', onResponse);
    }
    expect(statuses, 'the form created the CI').toContain(201);
    expect(await ciCount(name), 'one CI after a double click').toBe(1);
  });

  test('L03b repeated API submits with one Idempotency-Key create exactly one CI, also in parallel', async () => {
    // API-04: the same Idempotency-Key replays the first answer instead of
    // creating a second CI.
    const keyName = `lc-idem-${w.s}`;
    const key = `lc-key-${w.s}`;
    const body = { name: keyName, ci_type_id: w.serverType };
    const first = await raw<CI>('POST', '/cis', body, { 'Idempotency-Key': key });
    expect(first.status, JSON.stringify(first.body)).toBe(201);
    const second = await raw<CI>('POST', '/cis', body, { 'Idempotency-Key': key });
    expect(second.status).toBe(201);
    expect(second.body.id, 'the replay names the same CI').toBe(first.body.id);
    expect(second.headers['idempotency-replayed']).toBe('true');
    expect(await ciCount(keyName)).toBe(1);
    // The same key with another body is refused and creates nothing.
    const other = await raw(
      'POST',
      '/cis',
      { ...body, name: `${keyName}-x` },
      { 'Idempotency-Key': key },
    );
    expect(other.status).toBeGreaterThanOrEqual(400);
    expect(other.status).toBeLessThan(500);
    expect(await ciCount(`${keyName}-x`)).toBe(0);

    // Parallel duplicates with one key (atomic reservation): one CI.
    const parName = `lc-par-${w.s}`;
    const parKey = `lc-par-key-${w.s}`;
    const answers = await Promise.all(
      [0, 1, 2, 3].map(() =>
        raw<CI>(
          'POST',
          '/cis',
          { name: parName, ci_type_id: w.serverType },
          { 'Idempotency-Key': parKey },
        ),
      ),
    );
    for (const a of answers) expect([201, 409]).toContain(a.status);
    const ids = new Set(answers.filter((a) => a.status === 201).map((a) => a.body.id));
    expect(ids.size, 'all successful answers name the same CI').toBe(1);
    expect(await ciCount(parName)).toBe(1);
  });

  test('L04 editing on the detail page sends If-Match, raises the version and survives a reload', async () => {
    const page = w.page;
    const ci = await newCI({
      name: `lc-edit-${w.s}`,
      manufacturer: 'Old Vendor',
      model: 'Old Model',
      management_ip: '10.1.1.1',
      firmware_version: '1.0.0',
    });
    expect(ci.version).toBe(1);
    const etag = await raw('GET', `/cis/${ci.id}`);
    expect(etag.headers['etag'], 'GET returns the version as ETag (API-07)').toBe('"1"');

    await page.goto(`/cmdb/${ci.id}`);
    await expect(page.getByRole('heading', { name: ci.name, exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Bearbeiten', exact: true }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByRole('heading', { name: 'CI bearbeiten' })).toBeVisible();
    await expect(dialog.getByLabel('Name', { exact: true })).toHaveValue(ci.name);
    await expect(
      dialog.getByLabel('CI-Typ'),
      'the type changes through its own action',
    ).toBeDisabled();
    const renamed = `lc-edited-${w.s}`;
    await dialog.getByLabel('Name', { exact: true }).fill(renamed);
    await dialog.getByLabel('Status').selectOption({ label: 'Wartung' });
    await dialog.getByLabel('Hersteller').fill('New Vendor');
    await dialog.getByLabel('Modell').fill('New Model');
    await dialog.getByLabel('Management-IP').fill('10.2.2.2');
    const patched = waitForPath(page, `/cis/${ci.id}`, 'PATCH');
    await dialog.getByRole('button', { name: 'Speichern' }).click();
    const res = await patched;
    expect(res.status(), `PATCH from the edit form: ${await res.text()}`).toBe(200);
    expect(res.request().headers()['if-match'], 'the form sends the version it read').toBe('"1"');
    expect(res.headers()['etag']).toBe('"2"');
    await expect(dialog).toBeHidden();
    await expect(page.getByRole('heading', { name: renamed, exact: true })).toBeVisible();

    // DB: stored, version raised once, manual.
    const stored = await getCI(ci.id);
    expect(stored).toMatchObject({
      name: renamed,
      status: 'maintenance',
      manufacturer: 'New Vendor',
      model: 'New Model',
      management_ip: '10.2.2.2',
      firmware_version: '1.0.0',
      version: 2,
    });

    // Reload: the detail page reads the stored state.
    await page.reload();
    await expect(page.getByRole('heading', { name: renamed, exact: true })).toBeVisible();
    await expect(detailValue(page, 'Hersteller')).toHaveText('New Vendor');
    await expect(detailValue(page, 'Modell')).toHaveText('New Model');
    await expect(detailValue(page, 'Management-IP')).toHaveText('10.2.2.2');
    await expect(detailValue(page, 'Firmware')).toHaveText('1.0.0');
    await expect(
      page.locator('#main-content').getByText('Wartung', { exact: true }).first(),
    ).toBeVisible();

    // CI-04/OVR-01: every manually changed field is a protected override.
    const prov = await ok<{ data?: { field: string; source?: string; protected?: boolean }[] }>(
      'GET',
      `/cis/${ci.id}/provenance`,
    );
    for (const field of ['name', 'manufacturer', 'model', 'management_ip']) {
      const entry = (prov.data ?? []).find((p) => p.field === field);
      expect(entry, `provenance of ${field}`).toBeTruthy();
      expect(entry!.protected, `${field} is protected by a manual override`).toBe(true);
    }
  });

  test('L05 clearing a field in the edit form removes the stored value (merge patch)', async () => {
    const page = w.page;
    const ci = await newCI({
      name: `lc-clear-${w.s}`,
      manufacturer: 'Vendor To Keep',
      firmware_version: '9.9.9',
    });
    await page.goto(`/cmdb/${ci.id}`);
    await expect(page.getByRole('heading', { name: ci.name, exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Bearbeiten', exact: true }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByLabel('Firmware-Version')).toHaveValue('9.9.9');
    await dialog.getByLabel('Firmware-Version').fill('');
    const patched = waitForPath(page, `/cis/${ci.id}`, 'PATCH');
    await dialog.getByRole('button', { name: 'Speichern' }).click();
    const res = await patched;
    expect(res.status(), `PATCH from the edit form: ${await res.text()}`).toBe(200);
    // The form sends only the change, the cleared field as null (RFC 7396).
    expect(res.request().postDataJSON(), 'PATCH body of the edit form').toEqual({
      firmware_version: null,
    });
    expect(res.request().headers()['if-match']).toBe(`"${ci.version}"`);
    await expect(dialog).toBeHidden();
    await page.reload();
    await expect(page.getByRole('heading', { name: ci.name, exact: true })).toBeVisible();
    await expect(detailValue(page, 'Hersteller')).toHaveText('Vendor To Keep');
    await expect(
      detailValue(page, 'Firmware'),
      'the cleared value is gone after a reload',
    ).toHaveText('—', { timeout: 10_000 });
    const stored = await getCI(ci.id);
    expect(stored.firmware_version, 'cleared through the API').toBeUndefined();
    expect(stored.manufacturer).toBe('Vendor To Keep');
    expect(stored.version, 'one change, one version').toBe(ci.version + 1);

    // DB: the column is NULL, not an empty string; the other field is kept.
    expect(
      db<{ firmware_is_null: boolean; manufacturer: string }>(
        `SELECT firmware_version IS NULL AS firmware_is_null, manufacturer
           FROM ci WHERE id = :'id' AND organization_id = :'org'`,
        { id: ci.id, org: w.orgId },
      ),
    ).toEqual([{ firmware_is_null: true, manufacturer: 'Vendor To Keep' }]);

    // CI-06: the clearing is a ci_change of the editor with the old value.
    const change = await pollHistory(
      ci.id,
      (e) => e.field_name === 'firmware_version' && e.old_value === '9.9.9',
      'history entry of the cleared firmware version',
    );
    expect([null, undefined, ''], 'new value of the clearing').toContain(change.new_value);
    expect(change.actor_id, 'the acting user').toBe(w.userId);
  });

  test('L05b PATCH is a JSON merge patch: null removes a typed field, absent members stay', async () => {
    // API: JSON Merge Patch (RFC 7396) – null removes a member, absent keeps it (CI-04).
    const ci2 = await newCI({ name: `lc-null-${w.s}`, model: 'M1', firmware_version: '1.2.3' });
    const nulled = await raw<CI>('PATCH', `/cis/${ci2.id}`, { firmware_version: null });
    expect(nulled.status).toBe(200);
    const after = await getCI(ci2.id);
    expect(after.firmware_version ?? '', 'null removes the value (RFC 7396)').toBe('');
    expect(after.model, 'absent members stay').toBe('M1');
  });

  test('L06 the edit appears in the CI history and the audit trail with the acting user, old and new value', async () => {
    const page = w.page;
    const ci = await newCI({ name: `lc-hist-${w.s}`, model: 'H1' });
    await page.goto(`/cmdb/${ci.id}`);
    await expect(page.getByRole('heading', { name: ci.name, exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Bearbeiten', exact: true }).click();
    const dialog = page.getByRole('dialog');
    const renamed = `lc-hist-new-${w.s}`;
    await dialog.getByLabel('Name', { exact: true }).fill(renamed);
    const patched = waitForPath(page, `/cis/${ci.id}`, 'PATCH');
    await dialog.getByRole('button', { name: 'Speichern' }).click();
    const res = await patched;
    expect(res.status()).toBe(200);
    const requestId = res.headers()['x-request-id'] ?? '';
    await expect(dialog).toBeHidden();

    // API: the unified history holds the creation and the field change with
    // who, when, what, old and new (CI-06, AUD-03, AUD-07).
    const created = await pollHistory(
      ci.id,
      (e) => /creat/i.test(e.change_type),
      'history entry of the creation',
    );
    expect(created.actor_id).toBe(w.userId);
    const change = await pollHistory(
      ci.id,
      (e) => e.field_name === 'name' && e.new_value === renamed,
      'history entry of the rename',
    );
    expect(change.old_value).toBe(ci.name);
    expect(change.actor_id, 'the acting user').toBe(w.userId);
    expect(Number.isNaN(Date.parse(change.created_at))).toBe(false);
    const changes = await ok<List<HistoryEntry>>('GET', `/cis/${ci.id}/changes`);
    expect(
      rows(changes).some((e) => e.field_name === 'name' && e.actor_id === w.userId),
      'GET /cis/{id}/changes lists the rename with its actor',
    ).toBe(true);

    // UI: the history section of the detail page lists the rename.
    await page.reload();
    await expect(page.getByRole('heading', { name: renamed, exact: true })).toBeVisible();
    await page.locator('summary', { hasText: 'Historie' }).click();
    const history = page.getByRole('list', { name: 'Historie' });
    await expect(history).toBeVisible();
    await expect(history.getByRole('listitem').filter({ hasText: /·\s*name\b/ })).not.toHaveCount(
      0,
    );
    await expect(history.locator('time').first()).toHaveAttribute('datetime', /\d{4}-\d{2}-\d{2}T/);

    // Audit trail (AUD-04/05/07): the change with actor, old/new, request id.
    const audit = await pollAudit(ci.id, 'ci.updated');
    expect(audit.actor_id, 'audit actor').toBe(w.userId);
    expect(audit.actor_type).toBe('user');
    expect(audit.resource_type).toBe('ci');
    expect(audit.source).toMatch(/^(ui|api)$/);
    expect(JSON.stringify(audit.changes ?? {})).toContain(ci.name);
    expect(JSON.stringify(audit.changes ?? {})).toContain(renamed);
    if (requestId) expect(audit.request_id).toBe(requestId);
    const createdAudit = await pollAudit(ci.id, 'ci.created');
    expect(createdAudit.actor_id).toBe(w.userId);
    expect(createdAudit.seq).toBeLessThan(audit.seq);
  });

  test('L07 the audit viewer shows the CI change with time, actor, action and object; the chain verifies', async () => {
    const page = w.page;
    const ci = await newCI({ name: `lc-auditui-${w.s}` });
    await ok('PATCH', `/cis/${ci.id}`, { name: `lc-auditui-new-${w.s}` }, { 'If-Match': '"1"' });
    const entry = await pollAudit(ci.id, 'ci.updated');

    await page.goto('/audit');
    await expect(page.getByRole('heading', { name: 'Audit-Protokoll' })).toBeVisible();
    const table = page.locator('#main-content table');
    await expect(table).toBeVisible();
    // Newest first: the first ci.updated row is the change just made (the
    // tests of this file run one after another in their own organization).
    const latest = table.locator('tbody tr').filter({ hasText: 'ci.updated' }).first();
    await expect(latest).toBeVisible();
    const cells = latest.locator('td');
    await expect(cells.nth(2)).toHaveText('ci.updated');
    await expect(cells.nth(1), 'actor of the change').toHaveText(entry.actor_id ?? w.userId);
    // When: a readable point in time, not "Invalid Date" or empty.
    await expect
      .soft(cells.nth(0), 'time of the audit entry')
      .not.toHaveText(/Invalid Date|^\s*—?\s*$/, { timeout: 5_000 });
    // What: the object of the change is the CI.
    await expect
      .soft(cells.nth(3), 'object of the audit entry')
      .toContainText(ci.id, { timeout: 5_000 });
    await expect
      .soft(
        table.locator('tbody tr').filter({ hasText: ci.id }).filter({ hasText: 'ci.updated' }),
        'audit row of the CI update with the CI as object',
      )
      .toHaveCount(1, { timeout: 5_000 });

    // AUD-04: the per-organization hash chain verifies intact.
    await page.getByRole('button', { name: 'Kette jetzt verifizieren' }).click();
    await expect(page.getByText('Intakt', { exact: true })).toBeVisible();
  });

  test('L08 a relationship created on the detail page shows as neighbour and in the impact view along its direction', async () => {
    const page = w.page;
    const app = await newCI({ name: `lc-app-${w.s}` });
    const db = await newCI({ name: `lc-db-${w.s}` });
    let sent = 0;
    const statuses: number[] = [];
    const isCreate = (r: { url(): string; method(): string }) =>
      new URL(r.url()).pathname === '/api/v1/relationships' && r.method() === 'POST';
    const onRequest = (r: { url(): string; method(): string }) => {
      if (isCreate(r)) sent++;
    };
    // The dialog sends one create per double click (F-314). Should a second
    // POST still be sent, the server answers the creates in the order they
    // arrive: it is passed on only after the first one has been answered, so
    // the repeat is always the one refused with 409.
    let firstAnswered = () => {};
    const firstDone = new Promise<void>((resolve) => (firstAnswered = resolve));
    let routed = 0;
    const holdRepeat = async (route: Route) => {
      if (route.request().method() === 'POST' && ++routed > 1) await firstDone;
      await route.continue();
    };
    const onResponse = (r: Response) => {
      if (isCreate(r.request())) {
        statuses.push(r.status());
        firstAnswered();
      }
    };

    await page.goto(`/cmdb/${app.id}`);
    await expect(page.getByRole('heading', { name: app.name, exact: true })).toBeVisible();
    await expect(page.getByText('Keine Beziehungen vorhanden')).toBeVisible();
    await page.getByRole('button', { name: /Beziehung hinzufügen/ }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByLabel('Richtung').selectOption({ label: 'Dieses CI → Ziel' });
    await dialog.getByLabel('Beziehungstyp').selectOption('depends_on');
    await dialog.getByLabel('CI-ID der Gegenstelle').fill(db.id);
    page.on('request', onRequest);
    page.on('response', onResponse);
    await page.route('**/api/v1/relationships', holdRepeat);
    try {
      // A double click must not create the edge twice. Both clicks land in one
      // task, before the pending state disables the button: the worst case of
      // a real double click (Playwright's dblclick only sometimes beats the
      // re-render, which made F-314 intermittent).
      await dialog.getByRole('button', { name: 'Anlegen' }).evaluate((b: HTMLButtonElement) => {
        b.click();
        b.click();
      });
      await expect
        .poll(() => sent > 0 && statuses.length === sent, { message: 'answers to every POST' })
        .toBe(true);
    } finally {
      await page.unroute('**/api/v1/relationships', holdRepeat);
      page.off('request', onRequest);
      page.off('response', onResponse);
    }
    expect(sent, `the double click sends one create (answers ${statuses.join(',')})`).toBe(1);
    expect(statuses, 'the create succeeds').toEqual([201]);
    const rels = await ok<
      List<{ id: string; source_ci_id: string; target_ci_id: string; rel_type: string }>
    >('GET', `/cis/${app.id}/relationships`);
    const edges = rows(rels).filter(
      (r) => r.source_ci_id === app.id && r.target_ci_id === db.id && r.rel_type === 'depends_on',
    );
    expect(edges, 'exactly one stored edge app depends_on db').toHaveLength(1);
    // Feedback: the user's create succeeded, so the dialog closes and no error
    // is reported for it.
    await expect(dialog, 'the dialog closes after the successful create').toBeHidden({
      timeout: 10_000,
    });
    await expect(
      page.getByRole('alert').filter({ hasText: /\S/ }),
      'no error shown for a successful create',
    ).toHaveCount(0, { timeout: 5_000 });

    // UI after reload: the relationship card and the neighbours show db.
    await page.reload();
    await expect(page.getByRole('link', { name: db.id })).toBeVisible();
    const neighbours = page.locator('section').filter({ hasText: 'Nachbarn' });
    await expect(neighbours.getByRole('link', { name: db.name, exact: true })).toBeVisible();

    // IMP-01: app depends on db, so a failure of db affects app – not vice versa.
    const impactDb = await ok<unknown>('GET', `/impact/${db.id}`);
    expect(JSON.stringify(impactDb), 'impact of db contains app').toContain(app.id);
    const impactApp = await ok<unknown>('GET', `/impact/${app.id}`);
    expect(JSON.stringify(impactApp), 'impact of app does not contain db').not.toContain(db.id);

    // UI impact view on db's detail page.
    await page.goto(`/cmdb/${db.id}`);
    await expect(page.getByRole('heading', { name: db.name, exact: true })).toBeVisible();
    await page.locator('summary', { hasText: 'Auswirkungsanalyse' }).click();
    await page.getByRole('button', { name: 'Blast Radius berechnen' }).click();
    const impact = page.locator('details').filter({ hasText: 'Auswirkungsanalyse' });
    await expect(impact.getByText(/Betroffene CIs:\s*1\b/)).toBeVisible();
    // The affected list names app with its type: "• <name> (<type>)".
    await expect(
      impact.getByRole('listitem').filter({ hasText: new RegExp(`${escapeRe(app.name)} \\(`) }),
    ).toBeVisible();
  });

  test('L09 the CI is found through the list filter, the full-text search and the command palette', async () => {
    const page = w.page;
    const token = `findme${w.s}`;
    const ci = await newCI({ name: `lc-${token}-alpha`, manufacturer: 'Contoso' });
    const other = await newCI({ name: `lc-other-${w.s}`, manufacturer: 'Contoso' });

    // List filter: the query narrows the list and is kept in the URL.
    await page.goto('/cmdb');
    const main = page.locator('#main-content');
    await main.getByLabel('Configuration Items durchsuchen').fill(token);
    await expect(page).toHaveURL(new RegExp(`[?&]q=${token}`));
    await expect(main.getByRole('link', { name: ci.name, exact: true })).toBeVisible();
    await expect(main.getByRole('link', { name: other.name, exact: true })).toHaveCount(0);
    await expect(main.getByText(/^1 Einträge$/)).toBeVisible();
    await page.reload();
    await expect(main.getByLabel('Configuration Items durchsuchen')).toHaveValue(token);
    await expect(main.getByRole('link', { name: ci.name, exact: true })).toBeVisible();
    // Status filter combined with the query: the active CI is not decommissioned.
    await main.getByLabel('CI-Status filtern').selectOption({ label: 'Außer Betrieb' });
    await expect(main.getByRole('link', { name: ci.name, exact: true })).toHaveCount(0);
    await expect(main.getByText('Noch keine Configuration Items')).toBeVisible();
    await main.getByLabel('CI-Status filtern').selectOption({ label: 'Aktiv' });
    await expect(main.getByRole('link', { name: ci.name, exact: true })).toBeVisible();

    // Full-text search (SRC-01): the hit names the CI and its page.
    let hits: List<SearchHit> | undefined;
    await expect
      .poll(
        async () => {
          hits = await ok<List<SearchHit>>('GET', `/search?q=${token}`);
          return rows(hits).map((h) => h.entity_id);
        },
        { message: 'full-text search finds the CI', timeout: 30_000 },
      )
      .toContain(ci.id);
    const hit = rows(hits).find((h) => h.entity_id === ci.id);
    expect(hit, 'search hit of the CI').toBeTruthy();
    expect(hit).toMatchObject({ entity_type: 'ci', title: ci.name, url: `/cmdb/${ci.id}` });
    expect(rows(hits).some((h) => h.entity_id === other.id)).toBe(false);

    // Command palette: Ctrl+K, type, pick the hit, land on the detail page.
    await page.goto('/dashboard');
    await page.locator('body').click();
    await page.keyboard.press('Control+k');
    const box = page.getByRole('combobox', { name: 'Befehle durchsuchen' });
    await expect(box).toBeFocused();
    await box.fill(token);
    const option = page.getByRole('option', { name: new RegExp(`${escapeRe(ci.name)} · ci`) });
    await expect(option).toBeVisible();
    await option.click();
    await expect(page).toHaveURL(new RegExp(`/cmdb/${ci.id}$`));
    await expect(page.getByRole('heading', { name: ci.name, exact: true })).toBeVisible();

    // A rename is searchable under the new name only.
    const newToken = `renamed${w.s}`;
    await ok('PATCH', `/cis/${ci.id}`, { name: `lc-${newToken}-beta` });
    await expect
      .poll(
        async () =>
          rows(await ok<List<SearchHit>>('GET', `/search?q=${newToken}`)).map((h) => h.entity_id),
        {
          message: 'search finds the new name',
        },
      )
      .toContain(ci.id);
    const stale = await ok<List<SearchHit>>('GET', `/search?q=${token}`);
    expect(
      rows(stale).map((h) => h.entity_id),
      'the old name is no longer a hit',
    ).not.toContain(ci.id);
  });

  test('L10 an export job started in the UI completes and its download contains the CI', async () => {
    const page = w.page;
    // A name with the CSV separator, double quotes and umlauts must survive the export.
    const name = `lc-export-${w.s} Müller; "Rack 7"`;
    const ci = await newCI({ name, manufacturer: 'Exportwerk', serial_number: `SN-EXP-${w.s}` });
    const total = (await ok<List<CI>>('GET', '/cis?limit=1')).total;

    await page.goto('/export');
    await expect(page.getByRole('heading', { name: 'Export', exact: true })).toBeVisible();
    await page.getByLabel('Format').selectOption('csv');
    const created = waitForPath(page, '/exports', 'POST');
    await page.getByRole('button', { name: 'Auftrag erstellen' }).click();
    const res = await created;
    expect(res.status(), `POST /exports: ${await res.text()}`).toBe(202);
    const job = (await res.json()) as ExportJob;

    // The job list follows the worker until the job is completed.
    const main = page.locator('#main-content');
    await expect(main.getByText('Abgeschlossen', { exact: true }).first()).toBeVisible({
      timeout: 120_000,
    });
    const done = await ok<ExportJob>('GET', `/exports/${job.id}`);
    expect(done.status).toBe('completed');
    expect(done.row_count, 'every CI of the tenant is exported').toBe(total);
    const link = main.getByRole('link', { name: 'Herunterladen' }).first();
    await expect(link).toBeVisible();
    const href = await link.getAttribute('href');
    expect(href, 'download URL').toBeTruthy();

    const file = await w.rq.get(new URL(href!, BASE).toString());
    expect(file.ok(), `download ${file.status()}`).toBe(true);
    const bytes = await file.body();
    // EXP-01: UTF-8 with BOM, semicolon separated.
    expect([...bytes.subarray(0, 3)], 'UTF-8 BOM').toEqual([0xef, 0xbb, 0xbf]);
    const text = bytes.subarray(3).toString('utf8');
    // RFC 4180: the field is quoted and a double quote inside it is doubled.
    expect(text, 'the name is one quoted field with doubled quotes').toContain(
      `;"${name.replace(/"/g, '""')}";`,
    );
    const table = parseCsv(text, ';').filter((r) => r.length > 1 || r[0] !== '');
    const header = table[0] ?? [];
    expect(header, 'semicolon-separated header with the name column').toContain('name');
    const nameCol = header.indexOf('name');
    const idCol = header.indexOf('id');
    const line = table.find((r) => r[nameCol] === name);
    expect(line, `the CI ${name} is a row of the export`).toBeTruthy();
    if (idCol >= 0) expect(line![idCol]).toBe(ci.id);
    expect(table.length - 1, 'one row per CI').toBe(total);
  });

  test('L10b the CSV export button of the CMDB list downloads the CIs of the list', async () => {
    const page = w.page;
    const name = `lc-listexport-${w.s}`;
    const ci = await newCI({ name, serial_number: `SN-LEXP-${w.s}` });
    await page.goto('/cmdb');
    const link = page.locator('#main-content').getByRole('link', { name: 'CSV exportieren' });
    await expect(link).toBeVisible();
    const download = page.waitForEvent('download', { timeout: 15_000 }).catch(() => null);
    const response = page
      .waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/export/cis', {
        timeout: 15_000,
      })
      .catch(() => null);
    await link.click();
    const [file, res] = await Promise.all([download, response]);
    if (!file) {
      // No download: the browser navigated to the export URL itself.
      const status = res ? res.status() : 0;
      const body = res ? (await res.text().catch(() => '')).slice(0, 300) : '';
      expect(status, `the export button answered ${status}: ${body}`).toBe(200);
    }
    expect(file, 'the export button downloads a file').toBeTruthy();
    const bytes = await readFile(await file!.path());
    expect([...bytes.subarray(0, 3)], 'UTF-8 BOM').toEqual([0xef, 0xbb, 0xbf]);
    const rowsOfFile = parseCsv(bytes.subarray(3).toString('utf8'), ';');
    const header = rowsOfFile[0] ?? [];
    const line = rowsOfFile.find((r) => r[header.indexOf('name')] === name);
    expect(line, `the CI ${name} is a row of the list export`).toBeTruthy();
    expect(line![header.indexOf('id')]).toBe(ci.id);
  });

  test('L11 delete decommissions the CI, hides it from traversal and search; restore brings it back with its relations', async () => {
    const page = w.page;
    const token = `gone${w.s}`;
    const x = await newCI({ name: `lc-${token}`, status: 'maintenance' });
    const y = await newCI({ name: `lc-base-${w.s}` });
    const rel = await ok<{ id: string }>('POST', '/relationships', {
      source_ci_id: x.id,
      target_ci_id: y.id,
      rel_type: 'depends_on',
      source: 'manual',
    });
    expect(JSON.stringify(await ok('GET', `/impact/${y.id}`))).toContain(x.id);
    await expect
      .poll(async () => rows(await ok<List<SearchHit>>('GET', `/search?q=${token}`)).length)
      .toBe(1);

    // DELETE (CI-05): 204; the CI is decommissioned and soft-deleted.
    const del = await api(w.rq, w.token, 'DELETE', `/cis/${x.id}`);
    expect(del.status).toBe(204);
    const read = await api<CI>(w.rq, w.token, 'GET', `/cis/${x.id}`);
    if (read.status === 200) {
      expect(read.body.status).toBe('decommissioned');
      expect(read.body.deleted_at).toBeTruthy();
    } else {
      expect(NOT_FOUND).toContain(read.status);
    }
    // AUD-03: the deletion is a change of the CI history (not only an audit
    // entry) that records the new status decommissioned, with its actor.
    const deletion = await pollHistory(
      x.id,
      (e) =>
        /delet/i.test(e.change_type) &&
        e.source !== 'audit' &&
        JSON.stringify(e.new_value ?? {}).includes('decommissioned'),
      'deletion with status decommissioned in the CI history',
    );
    expect(deletion.actor_id).toBe(w.userId);
    // Traversal ignores the deleted node (CI-05, IMP-07); search no longer finds it.
    expect(JSON.stringify(await ok('GET', `/impact/${y.id}`))).not.toContain(x.id);
    expect(rows(await ok<List<SearchHit>>('GET', `/search?q=${token}`))).toHaveLength(0);
    // UI: the active inventory no longer lists it.
    await page.goto(`/cmdb?q=${token}`);
    const main = page.locator('#main-content');
    await expect(main.getByText('Noch keine Configuration Items')).toBeVisible();
    await expect(main.getByRole('link', { name: x.name, exact: true })).toHaveCount(0);
    // Repeating the delete changes nothing and is no server error.
    const again = await api(w.rq, w.token, 'DELETE', `/cis/${x.id}`);
    expect(NOT_FOUND).toContain(again.status);

    // RESTORE within the retention: the CI, its status and its relation come back.
    const restored = await api<CI>(w.rq, w.token, 'POST', `/cis/${x.id}/restore`);
    expect(restored.status, JSON.stringify(restored.body)).toBe(200);
    expect(restored.body.status, 'the status before the deletion').toBe('maintenance');
    expect(restored.body.deleted_at ?? null).toBeNull();
    const back = await getCI(x.id);
    expect(back.status).toBe('maintenance');
    const relsBack = await ok<List<{ id: string }>>('GET', `/cis/${x.id}/relationships`);
    expect(
      rows(relsBack).map((r) => r.id),
      'relationships remain (CI-05)',
    ).toContain(rel.id);
    expect(JSON.stringify(await ok('GET', `/impact/${y.id}`))).toContain(x.id);
    await expect
      .poll(
        async () =>
          rows(await ok<List<SearchHit>>('GET', `/search?q=${token}`)).map((h) => h.entity_id),
        {
          message: 'the restored CI is searchable again',
        },
      )
      .toContain(x.id);
    // Restoring twice is refused without effect.
    const twice = await api(w.rq, w.token, 'POST', `/cis/${x.id}/restore`);
    expect([404, 409]).toContain(twice.status);
    // Unknown id.
    const unknown = await api(
      w.rq,
      w.token,
      'POST',
      '/cis/00000000-0000-0000-0000-00000000dead/restore',
    );
    expect(NOT_FOUND).toContain(unknown.status);

    // UI: the restored CI is listed and opens with its status and relation.
    await page.goto(`/cmdb?q=${token}`);
    await expect(main.getByRole('link', { name: x.name, exact: true })).toBeVisible();
    await main.getByRole('link', { name: x.name, exact: true }).click();
    await expect(page.getByRole('heading', { name: x.name, exact: true })).toBeVisible();
    await expect(main.getByText('Wartung', { exact: true }).first()).toBeVisible();
    await expect(page.getByRole('link', { name: y.id })).toBeVisible();

    // History and audit: deletion and restore with the actor.
    const restoreEntry = await pollHistory(
      x.id,
      (e) => e.field_name === 'deleted_at' || /restor/i.test(e.change_type),
      'restore in history',
    );
    expect(restoreEntry.actor_id).toBe(w.userId);
    expect((await pollAudit(x.id, 'ci.deleted')).actor_id).toBe(w.userId);
    expect((await pollAudit(x.id, 'ci.restored')).actor_id).toBe(w.userId);
  });

  test('L12 two sessions edit the same CI: the later save on a stale version is refused and nothing is lost', async ({
    browser,
  }) => {
    const s1 = w.page;
    const ci = await newCI({ name: `lc-conc-${w.s}`, model: 'M-0', manufacturer: 'Shared' });
    const s2 = await openSession(browser, w.account);
    try {
      // Session 1 opens the editor on version 1.
      await s1.goto(`/cmdb/${ci.id}`);
      await expect(s1.getByRole('heading', { name: ci.name, exact: true })).toBeVisible();
      await s1.getByRole('button', { name: 'Bearbeiten', exact: true }).click();
      const d1 = s1.getByRole('dialog');
      await expect(d1.getByLabel('Modell')).toHaveValue('M-0');

      // Session 2 changes the model and saves first.
      await s2.page.goto(`/cmdb/${ci.id}`);
      await expect(s2.page.getByRole('heading', { name: ci.name, exact: true })).toBeVisible();
      await s2.page.getByRole('button', { name: 'Bearbeiten', exact: true }).click();
      const d2 = s2.page.getByRole('dialog');
      await d2.getByLabel('Modell').fill('M-session2');
      const p2 = waitForPath(s2.page, `/cis/${ci.id}`, 'PATCH');
      await d2.getByRole('button', { name: 'Speichern' }).click();
      expect((await p2).status()).toBe(200);
      await expect(d2).toBeHidden();

      // Session 1 saves its own model on the stale version: refused.
      await d1.getByLabel('Modell').fill('M-session1');
      const p1 = waitForPath(s1, `/cis/${ci.id}`, 'PATCH');
      await d1.getByRole('button', { name: 'Speichern' }).click();
      const r1 = await p1;
      expect(r1.request().headers()['if-match']).toBe('"1"');
      expect([409, 412], `stale save answered ${r1.status()}: ${await r1.text()}`).toContain(
        r1.status(),
      );
      await expect(d1, 'the editor stays open with the refusal').toBeVisible();
      await expect(d1.getByRole('alert')).toBeVisible();
      await expect(d1.getByRole('alert')).not.toBeEmpty();

      // DB: session 2's change is kept, session 1's is not applied.
      const stored = await getCI(ci.id);
      expect(stored.model).toBe('M-session2');
      expect(stored.version).toBe(2);

      // Session 1 reloads, sees the current value and can save on it.
      await d1.getByRole('button', { name: 'Abbrechen' }).click();
      await s1.reload();
      await expect(detailValue(s1, 'Modell')).toHaveText('M-session2');
      await s1.getByRole('button', { name: 'Bearbeiten', exact: true }).click();
      await d1.getByLabel('Modell').fill('M-session1');
      const p3 = waitForPath(s1, `/cis/${ci.id}`, 'PATCH');
      await d1.getByRole('button', { name: 'Speichern' }).click();
      const r3 = await p3;
      expect(r3.status()).toBe(200);
      expect(r3.request().headers()['if-match']).toBe('"2"');
      expect((await getCI(ci.id)).model).toBe('M-session1');
    } finally {
      await s2.ctx.close();
    }
  });

  test('L12b two sessions edit different fields of the same CI: no change is lost and the edits merge field by field', async ({
    browser,
  }) => {
    const s1 = w.page;
    const ci = await newCI({ name: `lc-merge-${w.s}`, model: 'M-0', manufacturer: 'Vendor-0' });
    const s2 = await openSession(browser, w.account);
    try {
      await s1.goto(`/cmdb/${ci.id}`);
      await expect(s1.getByRole('heading', { name: ci.name, exact: true })).toBeVisible();
      await s1.getByRole('button', { name: 'Bearbeiten', exact: true }).click();
      const d1 = s1.getByRole('dialog');
      await expect(d1.getByLabel('Hersteller')).toHaveValue('Vendor-0');

      // Session 2 changes the model and saves first.
      await s2.page.goto(`/cmdb/${ci.id}`);
      await s2.page.getByRole('button', { name: 'Bearbeiten', exact: true }).click();
      const d2 = s2.page.getByRole('dialog');
      await d2.getByLabel('Modell').fill('M-session2');
      const p2 = waitForPath(s2.page, `/cis/${ci.id}`, 'PATCH');
      await d2.getByRole('button', { name: 'Speichern' }).click();
      const r2 = await p2;
      expect(r2.status()).toBe(200);
      expect(r2.request().postDataJSON(), 'session 2 sends only its change').toEqual({
        model: 'M-session2',
      });

      // Session 1 changes only the manufacturer on its stale editor.
      await d1.getByLabel('Hersteller').fill('Vendor-session1');
      const p1 = waitForPath(s1, `/cis/${ci.id}`, 'PATCH');
      await d1.getByRole('button', { name: 'Speichern' }).click();
      const r1 = await p1;
      expect(r1.request().headers()['if-match'], 'session 1 read version 1').toBe('"1"');
      expect(r1.request().postDataJSON(), 'session 1 sends only its change (F-228)').toEqual({
        manufacturer: 'Vendor-session1',
      });

      // Never a lost update: session 2's model survives whatever happens.
      const stored = await getCI(ci.id);
      expect(stored.model, "session 2's model is kept").toBe('M-session2');
      // API-07: PATCH is field-granular, 409 only when a field of the change
      // was changed in between – the manufacturer was not, so it is saved.
      expect(
        r1.status(),
        `stale save of an untouched field answered ${r1.status()}: ${await r1.text()}`,
      ).toBe(200);
      expect(stored.manufacturer, "session 1's manufacturer is saved").toBe('Vendor-session1');
      expect(stored.version, 'two changes, two versions').toBe(3);
      await expect(d1, 'the editor closes after saving').toBeHidden();
    } finally {
      await s2.ctx.close();
    }
  });

  test('L13 ETag and If-Match: unknown versions 412, conflicting fields 409, other fields merge, the org setting enforces If-Match', async () => {
    const ci = await newCI({ name: `lc-etag-${w.s}`, model: 'E0', firmware_version: 'F0' });
    const get1 = await raw<CI>('GET', `/cis/${ci.id}`);
    expect(get1.headers['etag']).toBe(`"${get1.body.version}"`);

    const p1 = await raw<CI>('PATCH', `/cis/${ci.id}`, { model: 'E1' }, { 'If-Match': '"1"' });
    expect(p1.status).toBe(200);
    expect(p1.headers['etag']).toBe('"2"');

    // Stale version, field changed since → 409 naming the field.
    const conflict = await raw<{ fields?: string[]; current_version?: number; trace_id?: string }>(
      'PATCH',
      `/cis/${ci.id}`,
      { model: 'E-stale' },
      { 'If-Match': '"1"' },
    );
    expect(conflict.status).toBe(409);
    expect(conflict.body.fields).toContain('model');
    expect(conflict.body.current_version).toBe(2);
    expect(conflict.body.trace_id).toBeTruthy();
    expect((await getCI(ci.id)).model).toBe('E1');

    // Stale version, untouched field → field-granular merge succeeds (API-07).
    const merged = await raw<CI>(
      'PATCH',
      `/cis/${ci.id}`,
      { firmware_version: 'F1' },
      { 'If-Match': '"1"' },
    );
    expect(merged.status).toBe(200);
    const now = await getCI(ci.id);
    expect(now).toMatchObject({ model: 'E1', firmware_version: 'F1', version: 3 });

    // A version the CI never had, or no version at all, is 412 problem+json.
    for (const tag of ['"999"', '"abc"']) {
      const bad = await raw<{ type?: string; trace_id?: string }>(
        'PATCH',
        `/cis/${ci.id}`,
        { model: 'E-bad' },
        { 'If-Match': tag },
      );
      expect(bad.status, `If-Match ${tag}`).toBe(412);
      expect(bad.headers['content-type'] ?? '').toContain('application/problem+json');
      expect(bad.body.trace_id).toBeTruthy();
    }
    expect((await getCI(ci.id)).model).toBe('E1');

    // Org setting require_if_match: a change without If-Match is 412.
    const org = await ok<{ settings?: Record<string, unknown> }>('GET', '/org');
    const settings = org.settings ?? {};
    await ok('PATCH', '/org', { settings: { ...settings, require_if_match: true } });
    try {
      const missing = await raw('PATCH', `/cis/${ci.id}`, { model: 'E-none' });
      expect(missing.status, 'If-Match required by the organization').toBe(412);
      expect((await getCI(ci.id)).model).toBe('E1');
      const withTag = await raw('PATCH', `/cis/${ci.id}`, { model: 'E2' }, { 'If-Match': '"3"' });
      expect(withTag.status).toBe(200);
      // The UI edit sends If-Match and keeps working.
      const page = w.page;
      await page.goto(`/cmdb/${ci.id}`);
      await page.getByRole('button', { name: 'Bearbeiten', exact: true }).click();
      const dialog = page.getByRole('dialog');
      await dialog.getByLabel('Modell').fill('E3');
      const patched = waitForPath(page, `/cis/${ci.id}`, 'PATCH');
      await dialog.getByRole('button', { name: 'Speichern' }).click();
      expect((await patched).status()).toBe(200);
      await expect(dialog).toBeHidden();
      expect((await getCI(ci.id)).model).toBe('E3');
    } finally {
      await ok('PATCH', '/org', { settings: { ...settings, require_if_match: false } });
    }
  });

  test('L13b with require_if_match the instance attribute edit on the detail page saves with If-Match', async () => {
    const page = w.page;
    const ci = await newCI({ name: `lc-inst-${w.s}`, model: 'I0' });
    await ok('PUT', `/cis/${ci.id}/field-definitions`, {
      name: 'ilo_ip',
      label: 'iLO-IP',
      data_type: 'text',
    });
    const before = await getCI(ci.id);

    const org = await ok<{ settings?: Record<string, unknown> }>('GET', '/org');
    const settings = org.settings ?? {};
    await ok('PATCH', '/org', { settings: { ...settings, require_if_match: true } });
    try {
      await page.goto(`/cmdb/${ci.id}`);
      await expect(page.getByRole('heading', { name: ci.name, exact: true })).toBeVisible();
      await page.locator('summary', { hasText: 'Instanzfelder' }).click();
      const field = page.getByLabel('iLO-IP', { exact: true });
      await expect(field).toBeVisible();

      // F-082: the inline save carries If-Match with the shown version.
      const first = waitForPath(page, `/cis/${ci.id}`, 'PATCH');
      await field.fill('10.9.9.1');
      const r1 = await first;
      expect(r1.status(), `instance attribute save: ${await r1.text()}`).toBe(200);
      expect(r1.request().headers()['if-match']).toBe(`"${before.version}"`);
      expect(r1.request().postDataJSON()).toMatchObject({ attributes: { ilo_ip: '10.9.9.1' } });
      await expect.poll(async () => (await getCI(ci.id)).attributes?.ilo_ip).toBe('10.9.9.1');

      // A further change saves on the version the first save produced.
      const second = waitForPath(page, `/cis/${ci.id}`, 'PATCH');
      await field.fill('10.9.9.2');
      const r2 = await second;
      expect(r2.status(), `second instance attribute save: ${await r2.text()}`).toBe(200);
      expect(r2.request().headers()['if-match']).toBe(`"${before.version + 1}"`);
      const stored = await getCI(ci.id);
      expect(stored.attributes?.ilo_ip).toBe('10.9.9.2');
      expect(stored.version).toBe(before.version + 2);
      await expect(page.getByText(/Speichern abgelehnt/)).toHaveCount(0);
    } finally {
      await ok('PATCH', '/org', { settings: { ...settings, require_if_match: false } });
    }
  });

  test('L14 names and fields with quotes or backslashes are stored like any other text (API create, edit form)', async () => {
    const page = w.page;
    // CI-03 / MET-11: the name is free text, so is a model designation. The
    // audit trail of the change (AUD-04) must not refuse what the CI accepts.
    for (const name of [
      `lc-apos-${w.s} O'Brien`,
      `lc-bslash-${w.s} C:\\Racks`,
      `lc-quote-${w.s} "Rack 7"`,
    ]) {
      const created = await raw<CI>('POST', '/cis', { name, ci_type_id: w.serverType });
      expect.soft(created.status, `create ${name}: ${JSON.stringify(created.body)}`).toBe(201);
      if (created.status === 201) expect((await getCI(created.body.id)).name).toBe(name);
    }

    // The edit form: a model designation with double quotes.
    const ci = await newCI({ name: `lc-quote-edit-${w.s}`, model: 'Plain' });
    await page.goto(`/cmdb/${ci.id}`);
    await expect(page.getByRole('heading', { name: ci.name, exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Bearbeiten', exact: true }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByLabel('Modell').fill('R740 "XL"');
    const patched = waitForPath(page, `/cis/${ci.id}`, 'PATCH');
    await dialog.getByRole('button', { name: 'Speichern' }).click();
    const res = await patched;
    expect.soft(res.status(), `PATCH from the edit form: ${await res.text()}`).toBe(200);
    await expect.soft(dialog, 'the editor closes after saving').toBeHidden({ timeout: 10_000 });
    if (await dialog.isVisible()) await dialog.getByRole('button', { name: 'Abbrechen' }).click();
    await page.reload();
    await expect(page.getByRole('heading', { name: ci.name, exact: true })).toBeVisible();
    await expect.soft(detailValue(page, 'Modell')).toHaveText('R740 "XL"', { timeout: 5_000 });
    expect((await getCI(ci.id)).model, 'stored model').toBe('R740 "XL"');
  });
});
