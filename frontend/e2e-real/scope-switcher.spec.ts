import {
  test,
  expect,
  type APIRequestContext,
  type BrowserContext,
  type Page,
} from '@playwright/test';
import { api, signupTenant, stamp, uiLogin, BASE } from './support/stack';

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
 * A failing test is a product finding: the assertions encode the acceptance criteria
 * of WP-524 and decision E-87 (docs/plan/g1-korrektur/entscheidungen.md).
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
