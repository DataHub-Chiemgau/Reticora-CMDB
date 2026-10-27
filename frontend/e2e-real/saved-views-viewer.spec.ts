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
 * Saved views run by read-only roles on the real stack (WP-460, F-055; open proof of WP-034),
 * nothing mocked: UI → REST API → PostgreSQL → UI.
 *
 * Before WP-460 POST /api/v1/search/query required search:write, so a viewer saw the shared
 * views of the organization but every run answered 403, and the page showed a generic error
 * whose retry did nothing. The structured search now needs search:read plus the read
 * permission of the queried entity kind (ci:read or asset:read); rebuilding the index
 * (POST /search/reindex) stays search:write.
 *
 * One organization is created through the self-signup UI. Its administrator creates a CI
 * and an asset with a common name part and two shared views selecting them by name (entity
 * kind ci and asset), then invites two users with the organization scope: one with the
 * system role viewer and one with a custom role holding ci:read, saved_view:read and
 * search:read but no asset:read.
 *
 * Test → requirement IDs proven:
 *  S1 the viewer opens the saved-views page, runs both shared views and sees the CI and the
 *     asset; the API answers 200 for the search and 403 for the index rebuild
 *     .......................................................... SRC-03 (WP-034, WP-460, F-055)
 *  S2 the user without asset:read sees the CI of the CI view and, for the asset view, the
 *     permission hint instead of the asset (403, no retry) ..... SRC-03 (WP-034, WP-460, F-055)
 *
 * A failing test is a product finding: the assertions encode SRC-03 in docs/spec/katalog-v3
 * and the acceptance criteria of WP-460.
 */

interface List<T> {
  data: T[] | null;
  total: number;
}

interface User {
  email: string;
  password: string;
}

interface World {
  rq: APIRequestContext;
  ctx: BrowserContext;
  token: string;
  account: Account;
  ciName: string;
  assetName: string;
  ciView: string;
  assetView: string;
  /** Whether the inventory module (P2, off in G1 by default) serves /assets. */
  assets: boolean;
  viewer: User;
  ciOnly: User;
}

let w: World;

/** A documentation address (198.18.0.0/15) so the signup has its own budget (E-33). */
function realIp(): string {
  return `198.18.${Math.floor(Math.random() * 250) + 1}.${Math.floor(Math.random() * 250) + 1}`;
}

/** ok asserts a 2xx answer of a setup call and returns its body. */
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
  const user = { email: `sv-${label}-${s}@audit-e2e.de`, password: `Audit-${label}-${s}!aA` };
  const invitation = await ok<{ token?: string }>('POST', '/invitations', {
    email: user.email,
    role_id: roleId,
    scope_type: 'org',
  });
  expect(invitation.token, 'the token is returned once on issue').toBeTruthy();
  const accept = await w.rq.post(`${BASE}/api/v1/invitations/accept`, {
    data: {
      token: invitation.token,
      password: user.password,
      display_name: `Saved views ${label}`,
    },
    failOnStatusCode: false,
  });
  expect(accept.status(), `accept the invitation: ${await accept.text()}`).toBe(201);
  return user;
}

function waitForSearch(page: Page): Promise<Response> {
  return page.waitForResponse(
    (r) => new URL(r.url()).pathname === '/api/v1/search/query' && r.request().method() === 'POST',
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

test.describe('Saved views run by read-only roles: search:read and the entity read right suffice', () => {
  test.beforeAll(async ({ browser, playwright }) => {
    test.setTimeout(4 * 60_000);
    const rq = await playwright.request.newContext();
    const ctx = await browser.newContext({
      baseURL: BASE,
      locale: 'de-DE',
      extraHTTPHeaders: { 'X-Real-IP': realIp() },
    });
    const page = await ctx.newPage();
    const account = await signupTenant(page, rq, 'savedviews');
    const token = await uiLogin(page, account);
    const s = stamp();
    w = {
      rq,
      ctx,
      token,
      account,
      ciName: `sv-${s}-ci`,
      assetName: `sv-${s}-asset`,
      ciView: `Viewer CI ${s}`,
      assetView: `Viewer Asset ${s}`,
      assets: false,
      viewer: { email: '', password: '' },
      ciOnly: { email: '', password: '' },
    };

    const types = await ok<List<{ id: string; key: string }>>('GET', '/ci-types?limit=200');
    const serverType = (types.data ?? []).find((t) => t.key === 'server')?.id ?? '';
    expect(serverType, 'catalog type server').not.toBe('');
    await ok('POST', '/cis', { ci_type_id: serverType, name: w.ciName });
    // The inventory module is a P2 module and switched off in G1 by default:
    // its routes answer 404 (WP-214). The asset view is saved either way; it
    // carries the asset-kind permission check of S2, a hit only exists with
    // the module on.
    const created = await api(w.rq, w.token, 'POST', '/assets', {
      asset_tag: `sv-${s}`,
      name: w.assetName,
    });
    expect([201, 404], `POST /assets: ${JSON.stringify(created.body)}`).toContain(created.status);
    w.assets = created.status === 201;
    for (const [name, kind] of [
      [w.ciView, 'ci'],
      [w.assetView, 'asset'],
    ] as const) {
      await ok('POST', '/saved-views', {
        name,
        entity_kind: kind,
        filter_spec: { search: `sv-${s}` },
        shared: true,
      });
    }

    const roles = await ok<List<{ id: string; name: string }>>('GET', '/roles?limit=100');
    const viewerRole = (roles.data ?? []).find((r) => r.name === 'viewer')?.id ?? '';
    expect(viewerRole, 'system role viewer (RBA-02)').not.toBe('');
    const ciOnlyRole = await ok<{ id: string }>('POST', '/roles', {
      name: `ci-search-${s}`,
      permissions: ['ci:read', 'saved_view:read', 'search:read'],
    });
    w.viewer = await invite(viewerRole, 'viewer');
    // An invitation carries a standard role only (RBA-02); the custom role
    // without asset:read is assigned after the acceptance and the standard
    // role removed, so the user holds exactly ci:read, saved_view:read and
    // search:read.
    w.ciOnly = await invite(viewerRole, 'cionly');
    const users = await ok<List<{ id: string; email: string }>>('GET', '/users?limit=200');
    const ciOnlyID = (users.data ?? []).find((u) => u.email === w.ciOnly.email)?.id ?? '';
    expect(ciOnlyID, 'the accepted ci-only user').not.toBe('');
    await ok('POST', '/role-assignments', { user_id: ciOnlyID, custom_role_id: ciOnlyRole.id });
    const assignments = await ok<List<{ id: string; role_id?: string }>>(
      'GET',
      `/role-assignments?user_id=${ciOnlyID}`,
    );
    const standard = (assignments.data ?? []).filter((a) => a.role_id === viewerRole);
    expect(standard, 'the viewer assignment of the invitation').toHaveLength(1);
    await ok('DELETE', `/role-assignments/${standard[0].id}`);
  });

  test.afterAll(async () => {
    await w?.ctx.close();
    await w?.rq.dispose();
  });

  test('S1 a viewer runs the shared CI and asset views in the UI and sees their hits; the index rebuild stays refused', async ({
    browser,
  }) => {
    const { ctx, page, token } = await signIn(browser, w.viewer);
    try {
      const main = page.locator('#main-content');
      const ranCI = waitForSearch(page);
      await viewButton(page, w.ciView).click();
      const ciRes = await ranCI;
      expect(ciRes.status(), `POST /search/query (ci) as viewer: ${await ciRes.text()}`).toBe(200);
      await expect(main.getByRole('link', { name: w.ciName, exact: true })).toBeVisible();
      await expect(main.getByRole('alert')).toHaveCount(0);

      const ranAsset = waitForSearch(page);
      await viewButton(page, w.assetView).click();
      const assetRes = await ranAsset;
      expect(assetRes.status(), `POST /search/query (asset) as viewer`).toBe(200);
      if (w.assets) {
        await expect(main.getByRole('link', { name: w.assetName, exact: true })).toBeVisible();
      }
      await expect(main.getByRole('alert')).toHaveCount(0);

      // Rebuilding the index is a write operation (search:write).
      const reindex = await api(page.request, token, 'POST', '/search/reindex', {});
      expect(
        reindex.status,
        `POST /search/reindex as viewer: ${JSON.stringify(reindex.body)}`,
      ).toBe(403);
    } finally {
      await ctx.close();
    }
  });

  test('S2 without asset:read the CI view shows its hit and the asset view the permission hint', async ({
    browser,
  }) => {
    const { ctx, page } = await signIn(browser, w.ciOnly);
    try {
      const main = page.locator('#main-content');
      const ranCI = waitForSearch(page);
      await viewButton(page, w.ciView).click();
      expect((await ranCI).status(), 'POST /search/query (ci) without asset:read').toBe(200);
      await expect(main.getByRole('link', { name: w.ciName, exact: true })).toBeVisible();

      const ranAsset = waitForSearch(page);
      await viewButton(page, w.assetView).click();
      expect((await ranAsset).status(), 'POST /search/query (asset) without asset:read').toBe(403);
      const alert = main.getByRole('alert');
      await expect(alert).toContainText('Keine Berechtigung für diese Ansicht');
      await expect(alert).toContainText('Leserecht');
      await expect(alert.getByRole('button', { name: 'Erneut versuchen' })).toHaveCount(0);
      await expect(main.getByRole('link', { name: w.assetName, exact: true })).toHaveCount(0);
    } finally {
      await ctx.close();
    }
  });
});
