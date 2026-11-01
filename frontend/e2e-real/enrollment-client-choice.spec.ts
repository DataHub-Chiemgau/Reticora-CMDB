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
 * The client choice of the enrollment code on the real stack (WP-696, F-425), nothing mocked:
 * UI → REST API → PostgreSQL.
 *
 * Since WP-677 the server binds an enrollment code to the client in which its creator holds
 * collector:manage and refuses a creator restricted to several clients without client_id
 * (422, createCollectorEnrollmentCode). Before WP-696 the dialog "Collector verbinden" of the
 * Discovery page and the collector step of the onboarding sent only a label: such a user got
 * the 422 and saw "Ein Fehler ist aufgetreten".
 *
 * One organization is created through the self-signup UI. Its administrator (org-wide)
 * creates the clients A, B and C and a custom role with collector:manage, discovery:read and
 * location:read, and invites a user as viewer of client C; after the acceptance the user also
 * holds the custom role in client A and in client B (an invitation carries standard roles
 * only, RBA-02). His session scope holds A, B and C, his collector:manage scope A and B.
 *
 * Test → requirement IDs proven:
 *  E1 without client_id the API refuses the user's code (422, field client_id); the dialog
 *     offers exactly A and B (not C), creates nothing without a choice, and with B chosen
 *     the code is bound to B (201, client_id B) and shown ........ COL-04, TEN-09 (F-425)
 *  E2 the onboarding step offers the same choice; with A chosen the code is bound to A
 *     ............................................................ UI-17, COL-04 (F-425)
 *  E3 the org-wide administrator gets no choice: the dialog sends only the label and the
 *     code stays unbound (201 without client_id) ............................. COL-04
 *
 * A failing test is a product finding: the assertions encode COL-04, TEN-09 and UI-17 in
 * docs/spec/katalog-v3 and the acceptance criteria of WP-696.
 */

interface List<T> {
  data: T[] | null;
  total: number;
}

interface Named {
  id: string;
  name: string;
}

interface EnrollmentCode {
  id: string;
  code: string;
  client_id?: string;
}

interface World {
  rq: APIRequestContext;
  adminCtx: BrowserContext;
  adminPage: Page;
  adminToken: string;
  ctx: BrowserContext;
  page: Page;
  token: string;
  s: string;
  clientA: Named;
  clientB: Named;
  clientC: Named;
}

let w: World;

/** A documentation address (198.18.0.0/15) so the signup has its own budget (E-33). */
function realIp(): string {
  return `198.18.${Math.floor(Math.random() * 250) + 1}.${Math.floor(Math.random() * 250) + 1}`;
}

/** ok asserts a 2xx answer of a setup call of the administrator and returns its body. */
async function ok<T>(
  method: 'GET' | 'POST' | 'PATCH' | 'DELETE',
  path: string,
  data?: unknown,
): Promise<T> {
  const res = await api<T>(w.rq, w.adminToken, method, path, data);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeGreaterThanOrEqual(200);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeLessThan(300);
  return res.body;
}

function enrollmentCodeCreated(page: Page): Promise<Response> {
  return page.waitForResponse(
    (r) =>
      new URL(r.url()).pathname === '/api/v1/collectors/enrollment-codes' &&
      r.request().method() === 'POST',
  );
}

function sessionRead(page: Page): Promise<Response> {
  return page.waitForResponse(
    (r) => new URL(r.url()).pathname === '/api/v1/me' && r.request().method() === 'GET',
  );
}

async function openEnrollDialog(page: Page) {
  await page.goto('/discovery');
  await expect(
    page.locator('#main-content').getByRole('heading', { name: 'Discovery', exact: true }),
  ).toBeVisible();
  const session = sessionRead(page);
  await page.getByRole('button', { name: 'Collector verbinden' }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  expect((await session).status(), 'GET /me of the dialog').toBe(200);
  return dialog;
}

test.describe.configure({ mode: 'serial' });

test.describe('Enrollment code: a user restricted to several clients chooses the client', () => {
  test.beforeAll(async ({ browser, playwright }) => {
    test.setTimeout(4 * 60_000);
    const rq = await playwright.request.newContext();
    const adminCtx = await browser.newContext({
      baseURL: BASE,
      locale: 'de-DE',
      extraHTTPHeaders: { 'X-Real-IP': realIp() },
    });
    const adminPage = await adminCtx.newPage();
    const account = await signupTenant(adminPage, rq, 'enrollchoice');
    const adminToken = await uiLogin(adminPage, account);
    const s = stamp();
    w = { rq, adminCtx, adminPage, adminToken, s } as World;

    const client = (key: string) =>
      ok<Named>('POST', '/clients', {
        name: `Enroll Kunde ${key} ${s}`,
        slug: `enroll-kunde-${key.toLowerCase()}-${s}`,
      });
    w.clientA = await client('A');
    w.clientB = await client('B');
    w.clientC = await client('C');

    const role = await ok<{ id: string }>('POST', '/roles', {
      name: `collector-manage-${s}`,
      permissions: ['collector:manage', 'discovery:read', 'location:read'],
    });
    const roles = await ok<List<{ id: string; name: string }>>('GET', '/roles?limit=100');
    const viewerRole = (roles.data ?? []).find((r) => r.name === 'viewer')?.id ?? '';
    expect(viewerRole, 'system role viewer (RBA-02)').not.toBe('');
    const email = `enroll-ab-${s}@audit-e2e.de`;
    const password = `Audit-ab-${s}!aA`;
    const invitation = await ok<{ token?: string }>('POST', '/invitations', {
      email,
      role_id: viewerRole,
      scope_type: 'client',
      scope_id: w.clientC.id,
    });
    expect(invitation.token, 'the token is returned once on issue').toBeTruthy();
    const accept = await rq.post(`${BASE}/api/v1/invitations/accept`, {
      data: { token: invitation.token, password, display_name: 'Kunden A und B' },
      failOnStatusCode: false,
    });
    expect(accept.status(), `accept the invitation: ${await accept.text()}`).toBe(201);
    const userId = ((await accept.json()) as { user_id?: string }).user_id ?? '';
    expect(userId, 'the accepted user').not.toBe('');
    for (const c of [w.clientA, w.clientB]) {
      await ok('POST', '/role-assignments', {
        user_id: userId,
        custom_role_id: role.id,
        scope_type: 'client',
        scope_id: c.id,
      });
    }
    const assignments = await ok<
      List<{ role_id?: string; custom_role_id?: string; scope_type: string; scope_id?: string }>
    >('GET', `/role-assignments?user_id=${userId}`);
    expect(
      (assignments.data ?? [])
        .map((a) => `${a.custom_role_id ?? a.role_id}/${a.scope_type}/${a.scope_id}`)
        .sort(),
      'viewer in client C, collector:manage in clients A and B',
    ).toEqual(
      [
        `${viewerRole}/client/${w.clientC.id}`,
        `${role.id}/client/${w.clientA.id}`,
        `${role.id}/client/${w.clientB.id}`,
      ].sort(),
    );

    w.ctx = await browser.newContext({
      baseURL: BASE,
      locale: 'de-DE',
      extraHTTPHeaders: { 'X-Real-IP': realIp() },
    });
    w.page = await w.ctx.newPage();
    w.token = await uiLogin(w.page, { email, password });
  });

  test.afterAll(async () => {
    await w?.ctx?.close();
    await w?.adminCtx?.close();
    await w?.rq.dispose();
  });

  test('E1 the Discovery dialog offers clients A and B and binds the code to the choice', async () => {
    // The premise (WP-677): without client_id the server refuses the user's code.
    const refused = await api<{ field?: string }>(
      w.rq,
      w.token,
      'POST',
      '/collectors/enrollment-codes',
      { label: `premise-${w.s}` },
    );
    expect(refused.status, JSON.stringify(refused.body)).toBe(422);
    expect(refused.body.field).toBe('client_id');

    const page = w.page;
    const dialog = await openEnrollDialog(page);
    const select = dialog.getByLabel('Kunde', { exact: true });
    await expect(select).toBeVisible();
    await expect
      .poll(async () => (await select.locator('option').allTextContents()).sort())
      .toEqual(['Kunde wählen', w.clientA.name, w.clientB.name].sort());
    await expect(
      dialog.getByText(
        'Ihr Zugriff umfasst mehrere Kunden: Wählen Sie den Kunden, an den der Collector gebunden wird.',
      ),
    ).toBeVisible();
    const generate = dialog.getByRole('button', { name: 'Code erzeugen' });
    await expect(generate).toBeDisabled();

    const label = `standort-b-${w.s}`;
    await dialog.getByLabel('Bezeichnung (z. B. Standort)').fill(label);
    await select.selectOption(w.clientB.id);
    const created = enrollmentCodeCreated(page);
    await generate.click();
    const res = await created;
    expect(res.status()).toBe(201);
    expect(res.request().postDataJSON()).toEqual({ label, client_id: w.clientB.id });
    const code = (await res.json()) as EnrollmentCode;
    expect(code.client_id, 'the code is bound to client B').toBe(w.clientB.id);
    await expect(dialog.getByText(`--code ${code.code}`)).toBeVisible();
    await expect(dialog.getByText('Ein Fehler ist aufgetreten')).toHaveCount(0);
  });

  test('E2 the onboarding step offers the same choice and binds the code to client A', async () => {
    const page = w.page;
    const session = sessionRead(page);
    await page.goto('/onboarding');
    expect((await session).status(), 'GET /me of the collector step').toBe(200);
    const main = page.locator('#main-content');
    const select = main.getByLabel('Kunde', { exact: true });
    await expect(select).toBeVisible();
    await expect
      .poll(async () => (await select.locator('option').allTextContents()).sort())
      .toEqual(['Kunde wählen', w.clientA.name, w.clientB.name].sort());
    const create = main.getByRole('button', { name: 'Enrollment-Code erzeugen' });
    await expect(create).toBeDisabled();

    await select.selectOption(w.clientA.id);
    const created = enrollmentCodeCreated(page);
    await create.click();
    const res = await created;
    expect(res.status()).toBe(201);
    expect(res.request().postDataJSON()).toEqual({ label: 'onboarding', client_id: w.clientA.id });
    const code = (await res.json()) as EnrollmentCode;
    expect(code.client_id, 'the code is bound to client A').toBe(w.clientA.id);
    await expect(main.getByText(code.code, { exact: true })).toBeVisible();
  });

  test('E3 the org-wide administrator gets no choice and an unbound code', async () => {
    const page = w.adminPage;
    const dialog = await openEnrollDialog(page);
    await expect(dialog.getByText('Bezeichnung (z. B. Standort)')).toBeVisible();
    await expect(dialog.locator('select')).toHaveCount(0);

    const label = `org-${w.s}`;
    await dialog.getByLabel('Bezeichnung (z. B. Standort)').fill(label);
    const created = enrollmentCodeCreated(page);
    await dialog.getByRole('button', { name: 'Code erzeugen' }).click();
    const res = await created;
    expect(res.status()).toBe(201);
    expect(res.request().postDataJSON()).toEqual({ label });
    const code = (await res.json()) as EnrollmentCode;
    expect(code.client_id, 'an org-wide creator leaves the code unbound').toBeUndefined();
    await expect(dialog.getByText(`--code ${code.code}`)).toBeVisible();
  });
});
