import {
  test,
  expect,
  type APIRequestContext,
  type BrowserContext,
  type Locator,
  type Page,
} from '@playwright/test';
import {
  api,
  scopedUser,
  signupTenant,
  stamp,
  uiLogin,
  BASE,
  type ScopedUser,
} from './support/stack';

/*
 * The scope control and the CI detail against real API data (WP-524, binding retest of
 * F-169, WP-191), nothing mocked: UI → REST API → PostgreSQL → UI.
 *
 * Before WP-524 the CI detail of a CI outside the client chosen in the scope control
 * loaded without any notice, and its relationship list showed every edge, whatever
 * client the other CI belonged to (F-169).
 *
 * One organization is created through the self-signup UI; its administrator is
 * org-wide. It creates client A and client B and, through the API, the CIs a1 (client
 * A), b1 and b2 (client B) and the edges a1 → b1 and b1 → b2. The behaviour follows E-87
 * option 1 (transitional rule): notice, scoped relationships, access stays with RLS.
 *
 * Test → requirement IDs proven:
 *  S1 with client A chosen in the scope control, the CI b1 opened by URL loads with the
 *     notice "Dieses CI liegt außerhalb des gewählten Bereichs (<Kunde A>)"; its
 *     relationship list shows the edge to a1 and hides the edge to b2 with the counter
 *     "1 außerhalb des Bereichs ausgeblendet"; the toggle shows it ... UI-01 … UI-05,
 *     UI-07 … UI-10 (WP-191)
 *  S2 "Bereich wechseln" sets client B: the notice disappears, the scope control shows
 *     client B, and the relationship list now hides the edge to a1 ... UI-01, UI-02
 *  S3 "Alle Kunden" drops the selection: no notice, every edge listed ... UI-01, UI-02
 *
 * The list part (WP-556, open evidence of WP-191) uses a second organization: its
 * administrator creates the clients A, B and C, the sites sA (client A) and sB (client B)
 * and the CIs a1, a2 (client A), b1 (client B) and c1 (client C). A user is invited
 * (support/stack.ts, scopedUser) and holds the standard role viewer in the scopes of
 * client A and client B only (POST /role-assignments), so the server's row-level
 * security hides client C from him.
 *
 *  L1 the scope control of the client-restricted user offers exactly "Alle Kunden",
 *     client A and client B, as GET /clients of his session ... UI-01, UI-02 (WP-191)
 *  L2 choosing client A narrows /cmdb through the real API: the list request carries
 *     client_id of A, its answer holds a1 and a2 only, the page lists them and not b1,
 *     and the counter equals the total of GET /cis?client_id=A ... UI-01 … UI-05
 *  L3 choosing client B after client A and site sA resets the site: the next list
 *     request carries client_id of B and no site_id, the site control shows "Alle
 *     Standorte" and offers sB only ... UI-01, UI-02
 *  L4 client B and site sB hold after a reload; the list request after the reload
 *     carries both ... UI-01, UI-02, UI-07 … UI-10
 *
 * A failing test is a product finding: the assertions encode the acceptance criteria
 * of WP-524, WP-556 and decision E-87 (docs/plan/g1-korrektur/entscheidungen.md).
 */

interface Named {
  id: string;
  name: string;
}

interface World {
  rq: APIRequestContext;
  ctx: BrowserContext;
  page: Page;
  token: string;
  s: string;
  clientA: Named;
  clientB: Named;
  a1: Named;
  b1: Named;
  b2: Named;
}

let w: World;

/** A documentation address (198.18.0.0/15) so the signup has its own budget (E-33). */
function realIp(): string {
  return `198.18.${Math.floor(Math.random() * 250) + 1}.${Math.floor(Math.random() * 250) + 1}`;
}

/** ok asserts a 2xx answer of a setup call and returns its body. */
async function ok<T>(method: 'GET' | 'POST', path: string, data?: unknown): Promise<T> {
  const res = await api<T>(w.rq, w.token, method, path, data);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeGreaterThanOrEqual(200);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeLessThan(300);
  return res.body;
}

function clientSelect(page: Page) {
  return page
    .getByRole('group', { name: 'Arbeitsbereich' })
    .getByRole('combobox', { name: 'Kunde' });
}

function banner(page: Page) {
  return page.getByRole('status', { name: 'Arbeitsbereich' });
}

function relationshipsCard(page: Page) {
  return page
    .locator('#main-content section')
    .filter({ has: page.getByRole('heading', { name: 'Beziehungen', exact: true }) });
}

/** openB1 chooses the client in the scope control and opens b1 by URL. */
async function openB1(page: Page, clientId: string): Promise<void> {
  await page.goto('/cmdb');
  const select = clientSelect(page);
  await expect(select.getByRole('option', { name: w.clientA.name })).toBeAttached();
  await select.selectOption(clientId);
  await expect(select).toHaveValue(clientId);
  await page.goto(`/cmdb/${w.b1.id}`);
  await expect(page.getByRole('heading', { name: w.b1.name })).toBeVisible();
}

test.describe('CI detail under the chosen client scope (F-169)', () => {
  test.beforeAll(async ({ browser, playwright }) => {
    test.setTimeout(4 * 60_000);
    const rq = await playwright.request.newContext();
    const ctx = await browser.newContext({
      baseURL: BASE,
      locale: 'de-DE',
      extraHTTPHeaders: { 'X-Real-IP': realIp() },
    });
    const page = await ctx.newPage();
    const account = await signupTenant(page, rq, 'scope');
    const token = await uiLogin(page, account);
    const s = stamp();
    w = { rq, ctx, page, token, s } as World;

    w.clientA = await ok<Named>('POST', '/clients', {
      name: `Scope Kunde A ${s}`,
      slug: `scope-kunde-a-${s}`,
    });
    w.clientB = await ok<Named>('POST', '/clients', {
      name: `Scope Kunde B ${s}`,
      slug: `scope-kunde-b-${s}`,
    });
    const types = await ok<{ data: { id: string; key: string }[] }>('GET', '/ci-types?limit=200');
    const server = types.data.find((t) => t.key === 'server')?.id ?? '';
    expect(server, 'catalog type server').not.toBe('');
    const ci = (name: string, clientId: string) =>
      ok<Named>('POST', '/cis', { ci_type_id: server, name, client_id: clientId });
    w.a1 = await ci(`scope-a1-${s}`, w.clientA.id);
    w.b1 = await ci(`scope-b1-${s}`, w.clientB.id);
    w.b2 = await ci(`scope-b2-${s}`, w.clientB.id);
    for (const [source, target] of [
      [w.a1, w.b1],
      [w.b1, w.b2],
    ] as const) {
      await ok('POST', '/relationships', {
        source_ci_id: source.id,
        target_ci_id: target.id,
        rel_type: 'depends_on',
        source: 'manual',
      });
    }
  });

  test.afterAll(async () => {
    await w?.ctx.close();
    await w?.rq.dispose();
  });

  test('S1 a CI of client B opened under client A shows the notice and hides the client-B edge', async () => {
    const page = w.page;
    await openB1(page, w.clientA.id);

    // The CI loads (deep link, RLS decides) with the notice naming the chosen client.
    await expect(banner(page)).toContainText(
      `Dieses CI liegt außerhalb des gewählten Bereichs (${w.clientA.name}).`,
    );
    await expect(banner(page).getByRole('button', { name: 'Bereich wechseln' })).toBeVisible();
    await expect(banner(page).getByRole('button', { name: 'Alle Kunden' })).toBeVisible();

    const card = relationshipsCard(page);
    await expect(card.getByText('1 außerhalb des Bereichs ausgeblendet')).toBeVisible();
    await expect(card.getByRole('link', { name: w.a1.id })).toBeVisible();
    await expect(card.getByRole('link', { name: w.b2.id })).toHaveCount(0);

    // The toggle shows the hidden edge, marked as outside.
    await card.getByRole('button', { name: 'Alle anzeigen' }).click();
    await expect(card.getByRole('link', { name: w.b2.id })).toBeVisible();
    await expect(card.getByText('Außerhalb des Bereichs', { exact: true })).toHaveCount(1);
    await card.getByRole('button', { name: 'Nur Bereich anzeigen' }).click();
    await expect(card.getByRole('link', { name: w.b2.id })).toHaveCount(0);
  });

  test('S2 "Bereich wechseln" sets client B: no notice, the control shows client B', async () => {
    const page = w.page;
    await openB1(page, w.clientA.id);
    await expect(banner(page)).toBeVisible();

    await banner(page).getByRole('button', { name: 'Bereich wechseln' }).click();

    await expect(banner(page)).toHaveCount(0);
    await expect(clientSelect(page)).toHaveValue(w.clientB.id);
    await expect(clientSelect(page).locator('option:checked')).toHaveText(w.clientB.name);
    const card = relationshipsCard(page);
    await expect(card.getByRole('link', { name: w.b2.id })).toBeVisible();
    await expect(card.getByRole('link', { name: w.a1.id })).toHaveCount(0);
    await expect(card.getByText('1 außerhalb des Bereichs ausgeblendet')).toBeVisible();

    // The choice holds after a reload.
    await page.reload();
    await expect(page.getByRole('heading', { name: w.b1.name })).toBeVisible();
    await expect(clientSelect(page)).toHaveValue(w.clientB.id);
    await expect(banner(page)).toHaveCount(0);
  });

  test('S3 "Alle Kunden" drops the selection: no notice, every edge listed', async () => {
    const page = w.page;
    await openB1(page, w.clientA.id);
    await expect(banner(page)).toBeVisible();

    await banner(page).getByRole('button', { name: 'Alle Kunden' }).click();

    await expect(banner(page)).toHaveCount(0);
    await expect(clientSelect(page)).toHaveValue('');
    const card = relationshipsCard(page);
    await expect(card.getByRole('link', { name: w.a1.id })).toBeVisible();
    await expect(card.getByRole('link', { name: w.b2.id })).toBeVisible();
    await expect(card.getByText(/außerhalb des Bereichs ausgeblendet/)).toHaveCount(0);
  });
});

interface ListOf<T> {
  data: T[] | null;
  total: number;
}

interface CIRow {
  id: string;
  name: string;
  client_id?: string | null;
}

interface ListWorld {
  rq: APIRequestContext;
  adminCtx: BrowserContext;
  adminToken: string;
  clientA: Named;
  clientB: Named;
  clientC: Named;
  siteA: Named;
  siteB: Named;
  a1: Named;
  a2: Named;
  b1: Named;
  c1: Named;
  user: ScopedUser;
  ctx: BrowserContext;
  page: Page;
  token: string;
}

let l: ListWorld;

/** adminOk asserts a 2xx answer of a setup call of the list part and returns its body. */
async function adminOk<T>(method: 'GET' | 'POST', path: string, data?: unknown): Promise<T> {
  const res = await api<T>(l.rq, l.adminToken, method, path, data);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeGreaterThanOrEqual(200);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeLessThan(300);
  return res.body;
}

function siteSelect(page: Page) {
  return page
    .getByRole('group', { name: 'Arbeitsbereich' })
    .getByRole('combobox', { name: 'Standort' });
}

/** optionValues reads the values of the options of a select, sorted. */
async function optionValues(select: Locator): Promise<string[]> {
  const values = await select
    .locator('option')
    .evaluateAll((options) => options.map((o) => (o as HTMLOptionElement).value));
  return values.sort();
}

/** cisRequest waits for the CMDB list request whose query matches `match`. */
function cisRequest(page: Page, match: (query: URLSearchParams) => boolean) {
  return page.waitForResponse((r) => {
    const url = new URL(r.url());
    return (
      url.pathname === '/api/v1/cis' && r.request().method() === 'GET' && match(url.searchParams)
    );
  });
}

/** openList opens /cmdb with the scope control loaded for the user. */
async function openList(page: Page): Promise<void> {
  await page.goto('/cmdb');
  await expect(clientSelect(page)).toBeEnabled();
  await expect(clientSelect(page).getByRole('option', { name: l.clientA.name })).toBeAttached();
}

test.describe('scope control lists of a client-restricted user (WP-556, WP-191)', () => {
  test.describe.configure({ mode: 'serial' });

  test.beforeAll(async ({ browser, playwright }) => {
    test.setTimeout(5 * 60_000);
    const rq = await playwright.request.newContext();
    const adminCtx = await browser.newContext({
      baseURL: BASE,
      locale: 'de-DE',
      extraHTTPHeaders: { 'X-Real-IP': realIp() },
    });
    const adminPage = await adminCtx.newPage();
    const account = await signupTenant(adminPage, rq, 'scopelist');
    const adminToken = await uiLogin(adminPage, account);
    const s = stamp();
    l = { rq, adminCtx, adminToken } as ListWorld;

    const client = (key: string) =>
      adminOk<Named>('POST', '/clients', {
        name: `Liste Kunde ${key} ${s}`,
        slug: `liste-kunde-${key.toLowerCase()}-${s}`,
      });
    l.clientA = await client('A');
    l.clientB = await client('B');
    l.clientC = await client('C');
    l.siteA = await adminOk<Named>('POST', '/sites', {
      client_id: l.clientA.id,
      name: `Liste Standort A ${s}`,
    });
    l.siteB = await adminOk<Named>('POST', '/sites', {
      client_id: l.clientB.id,
      name: `Liste Standort B ${s}`,
    });
    const types = await adminOk<ListOf<{ id: string; key: string }>>('GET', '/ci-types?limit=200');
    const server = (types.data ?? []).find((t) => t.key === 'server')?.id ?? '';
    expect(server, 'catalog type server').not.toBe('');
    const ci = (name: string, clientId: string) =>
      adminOk<Named>('POST', '/cis', { ci_type_id: server, name, client_id: clientId });
    l.a1 = await ci(`liste-a1-${s}`, l.clientA.id);
    l.a2 = await ci(`liste-a2-${s}`, l.clientA.id);
    l.b1 = await ci(`liste-b1-${s}`, l.clientB.id);
    l.c1 = await ci(`liste-c1-${s}`, l.clientC.id);

    // Positive control: the organization-wide administrator reads all three clients.
    const adminClients = await adminOk<ListOf<Named>>('GET', '/clients?limit=200');
    expect((adminClients.data ?? []).map((c) => c.id).sort()).toEqual(
      [l.clientA.id, l.clientB.id, l.clientC.id].sort(),
    );

    // The user: viewer in the scope of client A (scopedUser) and of client B.
    l.user = await scopedUser(rq, adminToken, {
      label: 'scope-list',
      role: 'viewer',
      scope_type: 'client',
      scope_id: l.clientA.id,
      inviteClientId: l.clientB.id,
    });
    const roles = await adminOk<ListOf<{ id: string; name: string }>>('GET', '/roles?limit=100');
    const viewer = (roles.data ?? []).find((r) => r.name === 'viewer')?.id ?? '';
    expect(viewer, 'system role viewer').not.toBe('');
    await adminOk('POST', '/role-assignments', {
      user_id: l.user.userId,
      role_id: viewer,
      scope_type: 'client',
      scope_id: l.clientB.id,
    });
    const assignments = await adminOk<
      ListOf<{ role_id?: string; scope_type: string; scope_id?: string }>
    >('GET', `/role-assignments?user_id=${l.user.userId}`);
    expect(
      (assignments.data ?? []).map((a) => `${a.role_id}/${a.scope_type}/${a.scope_id}`).sort(),
      'the user holds viewer in client A and client B only',
    ).toEqual([`${viewer}/client/${l.clientA.id}`, `${viewer}/client/${l.clientB.id}`].sort());

    l.ctx = await browser.newContext({
      baseURL: BASE,
      locale: 'de-DE',
      extraHTTPHeaders: { 'X-Real-IP': realIp() },
    });
    l.page = await l.ctx.newPage();
    l.token = await uiLogin(l.page, l.user);
  });

  test.afterAll(async () => {
    await l?.ctx?.close();
    await l?.adminCtx?.close();
    await l?.rq.dispose();
  });

  test('L1 the scope control offers only the clients of the user', async () => {
    const page = l.page;
    await openList(page);

    const expected = ['', l.clientA.id, l.clientB.id].sort();
    await expect.poll(() => optionValues(clientSelect(page))).toEqual(expected);
    await expect(clientSelect(page).getByRole('option', { name: l.clientC.name })).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Weitere Kunden laden' })).toHaveCount(0);

    // The same clients as the API returns to his session.
    const own = await api<ListOf<Named>>(l.rq, l.token, 'GET', '/clients?limit=200');
    expect(own.status, 'GET /clients as the client-restricted user').toBe(200);
    expect(['', ...(own.body.data ?? []).map((c) => c.id)].sort()).toEqual(expected);
  });

  test('L2 client A narrows /cmdb through the real API', async () => {
    const page = l.page;
    await openList(page);

    const listed = cisRequest(page, (q) => q.get('client_id') === l.clientA.id);
    await clientSelect(page).selectOption(l.clientA.id);
    const res = await listed;
    expect(res.status(), 'CMDB list under client A').toBe(200);
    const body = (await res.json()) as ListOf<CIRow>;
    const rows = body.data ?? [];
    expect(rows.map((c) => c.id).sort()).toEqual([l.a1.id, l.a2.id].sort());
    expect(rows.every((c) => c.client_id === l.clientA.id)).toBe(true);

    const main = page.locator('#main-content');
    await expect(main.getByText(l.a1.name, { exact: true })).toBeVisible();
    await expect(main.getByText(l.a2.name, { exact: true })).toBeVisible();
    await expect(main.getByText(l.b1.name, { exact: true })).toHaveCount(0);
    await expect(main.getByText(l.c1.name, { exact: true })).toHaveCount(0);

    // The counter is the total of the API for the same scope and session.
    const fromApi = await api<ListOf<CIRow>>(
      l.rq,
      l.token,
      'GET',
      `/cis?client_id=${l.clientA.id}&limit=200`,
    );
    expect(fromApi.status).toBe(200);
    expect(fromApi.body.total).toBe(2);
    expect(body.total).toBe(fromApi.body.total);
    expect((fromApi.body.data ?? []).map((c) => c.id).sort()).toEqual(rows.map((c) => c.id).sort());
    await expect(main.getByText(`${fromApi.body.total} Einträge`, { exact: true })).toBeVisible();
  });

  test('L3 changing the client resets the site', async () => {
    const page = l.page;
    await openList(page);
    await clientSelect(page).selectOption(l.clientA.id);
    await expect(siteSelect(page).getByRole('option', { name: l.siteA.name })).toBeAttached();
    await expect.poll(() => optionValues(siteSelect(page))).toEqual(['', l.siteA.id].sort());

    const withSite = cisRequest(
      page,
      (q) => q.get('client_id') === l.clientA.id && q.get('site_id') === l.siteA.id,
    );
    await siteSelect(page).selectOption(l.siteA.id);
    expect((await withSite).status()).toBe(200);

    const afterChange = cisRequest(page, (q) => q.get('client_id') === l.clientB.id);
    await clientSelect(page).selectOption(l.clientB.id);
    const res = await afterChange;
    expect(res.status()).toBe(200);
    expect(new URL(res.url()).searchParams.get('site_id'), 'no site after the change').toBeNull();
    await expect(siteSelect(page)).toHaveValue('');
    await expect(siteSelect(page).locator('option:checked')).toHaveText('Alle Standorte');
    await expect.poll(() => optionValues(siteSelect(page))).toEqual(['', l.siteB.id].sort());
    const body = (await res.json()) as ListOf<CIRow>;
    expect((body.data ?? []).map((c) => c.id)).toEqual([l.b1.id]);
  });

  test('L4 the selection holds after a reload', async () => {
    const page = l.page;
    await openList(page);
    await clientSelect(page).selectOption(l.clientB.id);
    await expect(siteSelect(page).getByRole('option', { name: l.siteB.name })).toBeAttached();
    const withSite = cisRequest(
      page,
      (q) => q.get('client_id') === l.clientB.id && q.get('site_id') === l.siteB.id,
    );
    await siteSelect(page).selectOption(l.siteB.id);
    expect((await withSite).status()).toBe(200);
    await expect(siteSelect(page)).toHaveValue(l.siteB.id);

    // Registered after the answer above, so only a request of the reloaded page matches.
    const afterReload = cisRequest(
      page,
      (q) => q.get('client_id') === l.clientB.id && q.get('site_id') === l.siteB.id,
    );
    await page.reload();
    expect((await afterReload).status()).toBe(200);
    await expect(clientSelect(page)).toHaveValue(l.clientB.id);
    await expect(clientSelect(page).locator('option:checked')).toHaveText(l.clientB.name);
    await expect(siteSelect(page)).toHaveValue(l.siteB.id);
    await expect(siteSelect(page).locator('option:checked')).toHaveText(l.siteB.name);
  });
});
