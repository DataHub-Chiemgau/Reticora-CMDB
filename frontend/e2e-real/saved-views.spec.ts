import {
  test,
  expect,
  type APIRequestContext,
  type Browser,
  type BrowserContext,
  type Page,
  type Response,
} from '@playwright/test';
import { api, signupTenant, stamp, uiLogin, BASE, type Account } from './support/stack';

/*
 * Saved views with sort, columns and role sharing on the real stack (WP-555, VIE-01, F-336;
 * open proof of WP-188), nothing mocked: UI → REST API → PostgreSQL → UI.
 *
 * Before WP-555 sort and columns of a view were stored but never applied (the query engine
 * ordered by name), the saved-views page saved only name, entity kind and filter, and a view
 * could be shared with roles through the API only.
 *
 * One organization is created through the self-signup UI. Its administrator (the owner)
 * creates three CIs with a common name part and serial numbers, a private base view selecting
 * them and a custom role with ci:read, saved_view:read, saved_view:write and search:read; then
 * invites two users with the organization scope: the member gets the custom role (and loses the
 * viewer role of the invitation), the outsider keeps the system role viewer, which may read views
 * but holds neither the custom role nor an org-wide sharing. The role holds saved_view:write so
 * that the member passes the permission check of PATCH and DELETE (without it they answer 403
 * before the view is looked at) and R2 proves the owner rule of E-86: a view shared with the
 * member's role stays read-only for the member although the member may write saved views.
 *
 * Test → requirement IDs proven:
 *  R1 the owner runs the base view, sets sort name descending, the columns name and serial
 *     number and the custom role in the UI and saves it as a new view; the stored view (API)
 *     carries exactly that sort, those columns and the role, is not shared org-wide, and the
 *     results show the rows in that order with those columns only ........... VIE-01 (WP-188)
 *  R2 the member of the role sees the view, runs it and gets the rows in the saved order with
 *     the saved columns; the member has no delete button, DELETE and PATCH answer 404 and the
 *     view stays unchanged ............................................. VIE-01 (WP-188, E-86)
 *  R3 the outsider without the role does not see the view in the UI or the list, GET answers
 *     404 ...................................................................... VIE-01 (WP-188)
 *
 * A failing test is a product finding: the assertions encode VIE-01 in docs/spec/katalog-v3 and
 * the acceptance criteria of WP-555.
 */

interface List<T> {
  data: T[] | null;
  total: number;
}

interface User {
  email: string;
  password: string;
}

interface SavedView {
  id: string;
  owner_id?: string;
  name: string;
  entity_kind: string;
  filter_spec: Record<string, unknown>;
  sort: { field: string; dir?: string }[];
  columns: string[];
  shared: boolean;
  shared_with_roles: string[];
}

interface World {
  rq: APIRequestContext;
  ctx: BrowserContext;
  page: Page;
  token: string;
  account: Account;
  ownerId: string;
  tag: string;
  /** The CI names in ascending order; the saved view lists them descending. */
  names: string[];
  serials: string[];
  baseView: string;
  viewName: string;
  roleId: string;
  roleName: string;
  member: User;
  outsider: User;
  /** The view saved in R1. */
  view?: SavedView;
}

let w: World;

/** A documentation address (198.18.0.0/15) so the signup has its own budget (E-33). */
function realIp(): string {
  return `198.18.${Math.floor(Math.random() * 250) + 1}.${Math.floor(Math.random() * 250) + 1}`;
}

/** ok asserts a 2xx answer of a setup call of the owner and returns its body. */
async function ok<T>(
  method: 'GET' | 'POST' | 'PATCH' | 'DELETE',
  path: string,
  data?: unknown,
): Promise<T> {
  const res = await api<T>(w.rq, w.token, method, path, data);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeGreaterThanOrEqual(200);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeLessThan(300);
  return res.body;
}

/** invite issues an organization-wide invitation with roleId and accepts it. */
async function invite(roleId: string, label: string): Promise<User> {
  const s = stamp();
  const user = { email: `svr-${label}-${s}@audit-e2e.de`, password: `Audit-${label}-${s}!aA` };
  const invitation = await ok<{ token?: string }>('POST', '/invitations', {
    email: user.email,
    role_id: roleId,
    scope_type: 'org',
  });
  expect(invitation.token, 'the token is returned once on issue').toBeTruthy();
  const accept = await w.rq.post(`${BASE}/api/v1/invitations/accept`, {
    data: { token: invitation.token, password: user.password, display_name: `Views ${label}` },
    failOnStatusCode: false,
  });
  expect(accept.status(), `accept the invitation: ${await accept.text()}`).toBe(201);
  return user;
}

function waitForPath(page: Page, path: string, method: string): Promise<Response> {
  return page.waitForResponse(
    (r) => new URL(r.url()).pathname === `/api/v1${path}` && r.request().method() === method,
  );
}

/** viewButton is the run button of a view (the delete button is named "Löschen: …"). */
function viewButton(page: Page, name: string) {
  return page.locator('#main-content').getByRole('button', { name: new RegExp(`^${name}`) });
}

/** signIn opens a browser context of user on the saved-views page and returns its token. */
async function signIn(browser: Browser, user: User) {
  const ctx = await browser.newContext({ baseURL: BASE, locale: 'de-DE' });
  const page = await ctx.newPage();
  const token = await uiLogin(page, user);
  await page.goto('/saved-views');
  await expect(
    page.locator('#main-content').getByRole('heading', { name: 'Gespeicherte Ansichten' }),
  ).toBeVisible();
  return { ctx, page, token };
}

/**
 * expectSavedResults checks the result table of the saved view: the headers of the two saved
 * columns only and the rows in descending name order with their serial numbers.
 */
async function expectSavedResults(page: Page) {
  const table = page.locator('#main-content').getByRole('table');
  await expect(table.getByRole('columnheader')).toHaveText(['Name', 'Seriennummer']);
  const rows = table.locator('tbody tr');
  await expect(rows).toHaveCount(3);
  const names = [...w.names].reverse();
  const serials = [...w.serials].reverse();
  for (let i = 0; i < 3; i++) {
    await expect(rows.nth(i).getByRole('cell')).toHaveText([names[i]!, serials[i]!]);
  }
  await expect(table.getByRole('link', { name: names[0]!, exact: true })).toBeVisible();
}

test.describe.configure({ mode: 'serial' });

test.describe('Saved views: sort, columns and role sharing set in the UI (VIE-01, F-336)', () => {
  test.beforeAll(async ({ browser, playwright }) => {
    test.setTimeout(4 * 60_000);
    const rq = await playwright.request.newContext();
    const ctx = await browser.newContext({
      baseURL: BASE,
      locale: 'de-DE',
      extraHTTPHeaders: { 'X-Real-IP': realIp() },
    });
    const page = await ctx.newPage();
    const account = await signupTenant(page, rq, 'svroles');
    const token = await uiLogin(page, account);
    const s = stamp();
    const tag = `svr${s}`;
    w = {
      rq,
      ctx,
      page,
      token,
      account,
      ownerId: '',
      tag,
      names: [`${tag}-a`, `${tag}-b`, `${tag}-c`],
      serials: [`SN-${s}-1`, `SN-${s}-2`, `SN-${s}-3`],
      baseView: `Basis ${tag}`,
      viewName: `Rollensicht ${tag}`,
      roleId: '',
      roleName: `view-readers-${s}`,
      member: { email: '', password: '' },
      outsider: { email: '', password: '' },
    };
    w.ownerId = (await ok<{ user_id: string }>('GET', '/me/permissions')).user_id;
    expect(w.ownerId, 'the owner id').toBeTruthy();

    const types = await ok<List<{ id: string; key: string }>>('GET', '/ci-types?limit=200');
    const serverType = (types.data ?? []).find((t) => t.key === 'server')?.id ?? '';
    expect(serverType, 'catalog type server').not.toBe('');
    // Created out of order, so neither creation order nor the default name order is the
    // saved descending order.
    for (const i of [1, 0, 2]) {
      await ok('POST', '/cis', {
        ci_type_id: serverType,
        name: w.names[i],
        serial_number: w.serials[i],
      });
    }
    await ok('POST', '/saved-views', {
      name: w.baseView,
      entity_kind: 'ci',
      filter_spec: { search: tag },
    });

    const role = await ok<{ id: string }>('POST', '/roles', {
      name: w.roleName,
      // saved_view:write: the member may write own views, so R2's 404 on PATCH and DELETE is
      // the owner rule (E-86), not the 403 of a missing permission.
      permissions: ['ci:read', 'saved_view:read', 'saved_view:write', 'search:read'],
    });
    w.roleId = role.id;
    const roles = await ok<List<{ id: string; name: string }>>('GET', '/roles?limit=100');
    const viewerRole = (roles.data ?? []).find((r) => r.name === 'viewer')?.id ?? '';
    expect(viewerRole, 'system role viewer (RBA-02)').not.toBe('');
    // An invitation carries a standard role only (RBA-02); the member gets the custom role
    // after the acceptance and loses the viewer role, so the role sharing alone shows the view.
    w.member = await invite(viewerRole, 'member');
    w.outsider = await invite(viewerRole, 'outsider');
    const users = await ok<List<{ id: string; email: string }>>('GET', '/users?limit=200');
    const memberId = (users.data ?? []).find((u) => u.email === w.member.email)?.id ?? '';
    expect(memberId, 'the accepted member').not.toBe('');
    await ok('POST', '/role-assignments', { user_id: memberId, custom_role_id: w.roleId });
    const assignments = await ok<List<{ id: string; role_id?: string }>>(
      'GET',
      `/role-assignments?user_id=${memberId}`,
    );
    const standard = (assignments.data ?? []).filter((a) => a.role_id === viewerRole);
    expect(standard, 'the viewer assignment of the invitation').toHaveLength(1);
    await ok('DELETE', `/role-assignments/${standard[0]!.id}`);
  });

  test.afterAll(async () => {
    await w?.ctx.close();
    await w?.rq.dispose();
  });

  test('R1 the owner saves a view with sort desc, two columns and a role sharing in the UI', async () => {
    const page = w.page;
    const main = page.locator('#main-content');
    await page.goto('/saved-views');
    await expect(main.getByRole('heading', { name: 'Gespeicherte Ansichten' })).toBeVisible();

    const ranBase = waitForPath(page, '/search/query', 'POST');
    await viewButton(page, w.baseView).click();
    expect((await ranBase).status()).toBe(200);
    await expect(main.getByRole('link', { name: w.names[0]!, exact: true })).toBeVisible();

    await main.getByLabel('Sortieren nach').selectOption('name');
    await main.getByLabel('Richtung').selectOption('desc');
    await main.getByRole('checkbox', { name: 'Name', exact: true }).check();
    await main.getByRole('checkbox', { name: 'Seriennummer', exact: true }).check();
    await main.getByRole('checkbox', { name: w.roleName, exact: true }).check();
    await expect(
      main.getByRole('checkbox', { name: 'Für die ganze Organisation freigeben' }),
    ).not.toBeChecked();

    // The run of the changed presentation already follows it.
    await expectSavedResults(page);

    page.once('dialog', (dialog) => void dialog.accept(w.viewName));
    const saved = waitForPath(page, '/saved-views', 'POST');
    await main.getByRole('button', { name: 'Als Ansicht speichern' }).click();
    const savedRes = await saved;
    expect(savedRes.status(), await savedRes.text()).toBe(201);
    expect(savedRes.request().postDataJSON()).toEqual({
      name: w.viewName,
      entity_kind: 'ci',
      filter_spec: { search: w.tag },
      sort: [{ field: 'name', dir: 'desc' }],
      columns: ['name', 'serial_number'],
      shared: false,
      shared_with_roles: [w.roleId],
    });
    const view = (await savedRes.json()) as SavedView;
    await expect(viewButton(page, w.viewName)).toBeVisible();

    // Persistence (API): owner, sort, columns, sharing as set in the UI.
    const stored = await ok<SavedView>('GET', `/saved-views/${view.id}`);
    expect(stored.owner_id).toBe(w.ownerId);
    expect(stored.filter_spec).toEqual({ search: w.tag });
    expect(stored.sort).toEqual([{ field: 'name', dir: 'desc' }]);
    expect(stored.columns).toEqual(['name', 'serial_number']);
    expect(stored.shared).toBe(false);
    expect(stored.shared_with_roles).toEqual([w.roleId]);
    w.view = stored;

    // After a reload the saved view runs with its sort and columns.
    await page.reload();
    const rerun = waitForPath(page, '/search/query', 'POST');
    await viewButton(page, w.viewName).click();
    const rerunRes = await rerun;
    expect(rerunRes.status()).toBe(200);
    expect(rerunRes.request().postDataJSON()).toEqual({
      search: w.tag,
      entity_kind: 'ci',
      sort: [{ field: 'name', dir: 'desc' }],
      columns: ['name', 'serial_number'],
    });
    await expectSavedResults(page);
  });

  test('R2 a member of the role runs the view in the saved order and columns but cannot delete it', async ({
    browser,
  }) => {
    expect(w.view, 'the view of R1').toBeTruthy();
    const view = w.view!;
    const { ctx, page, token } = await signIn(browser, w.member);
    try {
      const main = page.locator('#main-content');
      const ran = waitForPath(page, '/search/query', 'POST');
      await viewButton(page, w.viewName).click();
      const res = await ran;
      expect(res.status(), `POST /search/query as member: ${await res.text()}`).toBe(200);
      expect(res.request().postDataJSON()).toMatchObject({
        sort: [{ field: 'name', dir: 'desc' }],
        columns: ['name', 'serial_number'],
      });
      await expectSavedResults(page);

      // Only the owner changes or deletes a view (E-86): no delete, no update in the UI, and
      // although the member holds saved_view:write, DELETE and PATCH of the shared view answer
      // 404 (the view is not the member's) and leave it unchanged.
      await expect(
        main.getByRole('button', { name: `Löschen: ${w.viewName}`, exact: true }),
      ).toHaveCount(0);
      await expect(main.getByRole('button', { name: 'Änderungen speichern' })).toHaveCount(0);
      expect((await api(page.request, token, 'DELETE', `/saved-views/${view.id}`)).status).toBe(
        404,
      );
      expect(
        (await api(page.request, token, 'PATCH', `/saved-views/${view.id}`, { name: 'gekapert' }))
          .status,
      ).toBe(404);
      const member = await api<SavedView>(page.request, token, 'GET', `/saved-views/${view.id}`);
      expect(member.status).toBe(200);
      expect(member.body.name).toBe(w.viewName);
    } finally {
      await ctx.close();
    }
    // The view is unchanged for its owner.
    const stored = await ok<SavedView>('GET', `/saved-views/${view.id}`);
    expect(stored.name).toBe(w.viewName);
    expect(stored.sort).toEqual([{ field: 'name', dir: 'desc' }]);
    expect(stored.columns).toEqual(['name', 'serial_number']);
  });

  test('R3 a user without the role and without an org-wide sharing does not see the view', async ({
    browser,
  }) => {
    expect(w.view, 'the view of R1').toBeTruthy();
    const view = w.view!;
    const { ctx, page, token } = await signIn(browser, w.outsider);
    try {
      const main = page.locator('#main-content');
      await expect(main.getByText('Meine Ansichten')).toBeVisible();
      // The list has loaded once the empty state or a view is there; the role view is not.
      await expect(main.getByText('Noch keine eigenen Ansichten.')).toBeVisible();
      await expect(viewButton(page, w.viewName)).toHaveCount(0);
      const list = await api<List<SavedView>>(page.request, token, 'GET', '/saved-views');
      expect(list.status).toBe(200);
      expect((list.body.data ?? []).map((v) => v.id)).not.toContain(view.id);
      expect((await api(page.request, token, 'GET', `/saved-views/${view.id}`)).status).toBe(404);
    } finally {
      await ctx.close();
    }
  });
});
