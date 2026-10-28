import {
  test,
  expect,
  type APIRequestContext,
  type Browser,
  type BrowserContext,
  type Page,
} from '@playwright/test';
import {
  api,
  keycloakLogin,
  signupTenant,
  stamp,
  uiLogin,
  BASE,
  type Account,
} from './support/stack';

/*
 * Deactivating and reactivating a user in the UI on the real stack (TLC-04; F-196,
 * WP-571), nothing mocked: UI → REST API → PostgreSQL → UI.
 *
 * One organization is created through the self-signup UI. Its administrator invites
 * user B as viewer through the API; B accepts (the server creates the Keycloak account)
 * and signs in. The administrator then deactivates B on the users page, B's session ends
 * at once and B's next login is refused with the localized text of WP-570
 * (`t2-first-login`, F-197); after the reactivation in the UI B signs in again.
 *
 * Test → requirement → work package
 *  UL01 the users page offers deactivation only for other users; the confirmed
 *       deactivation sends PATCH status inactive and shows "Inaktiv"; B's token answers
 *       401 at the next API call and the refresh cookie is refused ..... TLC-04 → WP-571, WP-359
 *  UL02 B's next login is refused (403 login-user-inactive) with the localized text,
 *       without a session ............................................. TLC-04, AUT-01 → WP-571, WP-570
 *  UL03 the confirmed reactivation in the UI shows "Aktiv"; B signs in again and holds
 *       the viewer rights ............................................. TLC-04 → WP-571
 *
 * The assertions encode TLC-04 of docs/spec/katalog-v3 and the acceptance criteria of
 * WP-571; a failure is a product finding. No test of this spec is listed in
 * tests/audit/expected-failures.txt.
 */

interface Session {
  ctx: BrowserContext;
  page: Page;
  token: string;
}

interface AppUser {
  id: string;
  email: string;
  status: string;
}

interface World {
  rq: APIRequestContext;
  admin: Session & { account: Account };
  b: { email: string; password: string; userId: string };
  /** B's session from before the deactivation. */
  bSession: Session;
}

let w: World;

/** A documentation address (198.18.0.0/15) so each signup and login has its own budget (E-33). */
function realIp(): string {
  return `198.18.${Math.floor(Math.random() * 250) + 1}.${Math.floor(Math.random() * 250) + 1}`;
}

async function newContext(browser: Browser): Promise<{ ctx: BrowserContext; page: Page }> {
  const ctx = await browser.newContext({
    baseURL: BASE,
    locale: 'de-DE',
    extraHTTPHeaders: { 'X-Real-IP': realIp() },
  });
  return { ctx, page: await ctx.newPage() };
}

async function newSession(
  browser: Browser,
  account: { email: string; password: string },
): Promise<Session> {
  const { ctx, page } = await newContext(browser);
  const token = await uiLogin(page, account);
  return { ctx, page, token };
}

/** ok asserts a 2xx answer of a setup call as the administrator and returns its body. */
async function ok<T>(method: 'GET' | 'POST', path: string, data?: unknown): Promise<T> {
  const res = await api<T>(w.rq, w.admin.token, method, path, data);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeGreaterThanOrEqual(200);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeLessThan(300);
  return res.body;
}

/** userRow is B's (or any address's) row in the table of the users page. */
function userRow(page: Page, email: string) {
  return page.locator('#main-content').getByRole('row').filter({ hasText: email });
}

/** openUsers opens the users page of the administrator and waits for B's row. */
async function openUsers(page: Page) {
  await page.goto('/users');
  await expect(userRow(page, w.b.email)).toBeVisible();
}

/**
 * confirmStatus clicks `action` in B's row, confirms it in the dialog titled `title`
 * and returns the answer of the PATCH it sends.
 */
async function confirmStatus(page: Page, action: string, title: string) {
  await userRow(page, w.b.email).getByRole('button', { name: action, exact: true }).click();
  const dialog = page.getByRole('dialog', { name: title });
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText(w.b.email);
  const patch = page.waitForResponse(
    (r) =>
      new URL(r.url()).pathname === `/api/v1/users/${w.b.userId}` &&
      r.request().method() === 'PATCH',
  );
  await dialog.getByRole('button', { name: action, exact: true }).click();
  const res = await patch;
  await expect(dialog).toBeHidden();
  return res;
}

async function adminStatusOfB(): Promise<string> {
  return (await ok<AppUser>('GET', `/users/${w.b.userId}`)).status;
}

test.describe.configure({ mode: 'serial' });

test.describe('user lifecycle in the UI (TLC-04, F-196)', () => {
  test.beforeAll(async ({ browser, playwright }) => {
    test.setTimeout(5 * 60_000);
    const rq = await playwright.request.newContext();
    const { ctx, page } = await newContext(browser);
    const account = await signupTenant(page, rq, 'lifecycle');
    const token = await uiLogin(page, account);
    w = { rq, admin: { ctx, page, token, account } } as World;

    const roles = await ok<{ data: { id: string; name: string }[] | null }>(
      'GET',
      '/roles?limit=100',
    );
    const viewer = (roles.data ?? []).find((r) => r.name === 'viewer')?.id ?? '';
    expect(viewer, 'system role viewer (RBA-02)').not.toBe('');
    const s = stamp();
    const email = `lifecycle-b-${s}@audit-e2e.de`;
    const password = `Audit-b-${s}!aA`;
    const invitation = await ok<{ token?: string }>('POST', '/invitations', {
      email,
      role_id: viewer,
    });
    expect(invitation.token, 'the token is returned once on issue').toBeTruthy();
    const accept = await rq.post(`${BASE}/api/v1/invitations/accept`, {
      data: { token: invitation.token, password, display_name: 'Audit Lifecycle B' },
      failOnStatusCode: false,
    });
    expect(accept.status(), `accept the invitation: ${await accept.text()}`).toBe(201);
    const userId = ((await accept.json()) as { user_id?: string }).user_id ?? '';
    expect(userId, 'the accepted user').not.toBe('');
    w.b = { email, password, userId };

    w.bSession = await newSession(browser, w.b);
    const perms = await api<{ permissions: string[] }>(
      rq,
      w.bSession.token,
      'GET',
      '/me/permissions',
    );
    expect(perms.status, 'B is signed in before the deactivation').toBe(200);
    expect(await adminStatusOfB()).toBe('active');
  });

  test.afterAll(async () => {
    await w?.bSession?.ctx.close();
    await w?.admin?.ctx.close();
    await w?.rq.dispose();
  });

  test('UL01 the admin deactivates B in the UI; B’s token answers 401 at the next API call', async () => {
    const page = w.admin.page;
    await openUsers(page);

    // The own account is not deactivatable (TLC-04): its row offers no action.
    const own = userRow(page, w.admin.account.email);
    await expect(own).toContainText('Ihr Konto');
    await expect(own.getByRole('button')).toHaveCount(0);

    const row = userRow(page, w.b.email);
    await expect(row.getByText('Aktiv', { exact: true })).toBeVisible();
    await expect(row.getByRole('button', { name: 'Löschen', exact: true })).toBeVisible();
    const res = await confirmStatus(page, 'Deaktivieren', 'Benutzer deaktivieren?');
    expect(res.status(), `PATCH status inactive: ${await res.text()}`).toBe(200);
    expect(((await res.json()) as AppUser).status).toBe('inactive');
    await expect(row.getByText('Inaktiv', { exact: true })).toBeVisible();
    await expect(row.getByRole('button', { name: 'Reaktivieren', exact: true })).toBeVisible();
    expect(await adminStatusOfB(), 'the database holds the deactivation').toBe('inactive');

    // B's session ends at once: the access token and the refresh cookie are refused.
    const next = await api(w.rq, w.bSession.token, 'GET', '/me/permissions');
    expect(next.status, `B's next API call: ${JSON.stringify(next.body)}`).toBe(401);
    const refresh = await w.bSession.page.request.post(`${BASE}/api/v1/auth/refresh`, {
      headers: { 'Content-Type': 'application/json' },
      data: {},
      failOnStatusCode: false,
    });
    expect(refresh.status(), `B's refresh: ${await refresh.text()}`).toBe(401);
  });

  test('UL02 B’s next login is refused with the localized text and gets no session', async ({
    browser,
  }) => {
    expect(await adminStatusOfB(), 'UL01 deactivated B').toBe('inactive');
    const { ctx, page } = await newContext(browser);
    try {
      await page.goto('/login', { waitUntil: 'domcontentloaded' });
      await page.getByRole('button', { name: /anmelden|sign in/i }).click();
      const callback = page.waitForResponse(
        (r) => r.url().endsWith('/api/v1/auth/callback') && r.request().method() === 'POST',
        { timeout: 90_000 },
      );
      await keycloakLogin(page, w.b, /\/auth\/callback/);
      const response = await callback;
      expect(response.status(), 'login of a deactivated user').toBe(403);
      expect(((await response.json()) as { type?: string }).type).toBe(
        'https://reticora.io/problems/login-user-inactive',
      );
      await expect(
        page.getByText(
          'Ihr Konto ist deaktiviert. Wenden Sie sich an die Administration Ihrer Organisation.',
          { exact: true },
        ),
      ).toBeVisible();
      await expect(page.getByText(/identity:|user is deactivated/i)).toHaveCount(0);
      await expect(page).not.toHaveURL(/\/dashboard/);
      const session = await page.evaluate(() =>
        window.sessionStorage.getItem('reticora-auth-session'),
      );
      expect(session, 'no session after the refusal').toBeNull();
    } finally {
      await ctx.close();
    }
  });

  test('UL03 the admin reactivates B in the UI; B signs in again', async ({ browser }) => {
    const page = w.admin.page;
    await openUsers(page);
    const row = userRow(page, w.b.email);
    await expect(row.getByText('Inaktiv', { exact: true })).toBeVisible();
    const res = await confirmStatus(page, 'Reaktivieren', 'Benutzer reaktivieren?');
    expect(res.status(), `PATCH status active: ${await res.text()}`).toBe(200);
    expect(((await res.json()) as AppUser).status).toBe('active');
    await expect(row.getByText('Aktiv', { exact: true })).toBeVisible();
    await expect(row.getByRole('button', { name: 'Deaktivieren', exact: true })).toBeVisible();
    expect(await adminStatusOfB(), 'the database holds the reactivation').toBe('active');

    const again = await newSession(browser, w.b);
    try {
      const perms = await api<{ permissions: string[] }>(
        w.rq,
        again.token,
        'GET',
        '/me/permissions',
      );
      expect(perms.status, 'B is signed in after the reactivation').toBe(200);
      expect(perms.body.permissions).toContain('ci:read');
      expect(perms.body.permissions).not.toContain('user:manage');
    } finally {
      await again.ctx.close();
    }
  });
});
