import { createHash } from 'node:crypto';
import {
  test,
  expect,
  type APIRequestContext,
  type BrowserContext,
  type Page,
  type Response,
} from '@playwright/test';
import {
  api,
  devRealmAccount,
  seedDemo,
  seedId,
  uiLogin,
  BASE,
  type SeededOrg,
} from './support/stack';

/*
 * The SIM-01 demo data on the real stack (WP-433; F-113, F-277, F-278; nothing mocked): the
 * seed (backend/cmd/seed) creates the organizations demo (plan pro) and acme with 40 CIs
 * each, and the development realm (deploy/keycloak/realm-reticora-dev.json) holds their
 * administrators. Before WP-433 the realm had no admin@demo.local, so the seeded demo
 * organization could not be used through the UI, and the seeded CIs had neither a search
 * document nor a location.
 *
 * The setup (support/stack.ts) runs the seed against the stack database (E2E_DATABASE_URL)
 * and signs in as admin@demo.local, the user the development realm imports. An installed
 * stack imports the production realm, which has no users: only then the setup creates the
 * user through the Keycloak admin API with the attributes, groups, realm roles and password
 * of realm-reticora-dev.json and logs it. admin@demo.local holds org_admin and sets up its
 * second factor at the first login (AUT-09).
 *
 * Test → requirement IDs proven:
 *  S1 admin@demo.local signs in to the demo organization; the CI list shows its 40 CIs
 *     ............................................................ SIM-01 (WP-433, F-113)
 *  S2 the global search (command palette, GET /search) finds srv-00021 of demo and opens
 *     it; the CI is located in rack R02 of the server room hq .... SIM-01 (WP-433, F-277, F-278)
 *  S3 nothing of acme is visible: its CI by id (API and UI), its serial number in the
 *     search ...................................................... SIM-01 (WP-433, F-113)
 *
 * A failing test is a product finding: the assertions encode SIM-01 and the acceptance
 * criteria of WP-433.
 */

interface List<T> {
  data: T[] | null;
  total: number;
}

interface CI {
  id: string;
  organization_id: string;
  name: string;
  serial_number?: string;
  location_id?: string;
  site_id?: string;
  room_id?: string;
}

interface SearchHit {
  entity_type: string;
  entity_id: string;
  title: string;
  url: string;
}

interface World {
  rq: APIRequestContext;
  ctx: BrowserContext;
  page: Page;
  token: string;
  demo: SeededOrg;
  acme: SeededOrg;
  /** admin@demo.local was created through the Keycloak admin API (realm import skipped). */
  created: boolean;
}

let w: World;

function rows<T>(list: List<T> | undefined): T[] {
  return list?.data ?? [];
}

/** seedSerial is the serial number the seed gives CI i of an organization. */
function seedSerial(org: string, i: number): string {
  const h = createHash('md5').update(`${org}/serial/${i}`).digest('hex');
  return `SN${h.slice(0, 12).toUpperCase()}`;
}

/** A documentation address (198.18.0.0/15) so the session has its own rate budget. */
function realIp(): string {
  return `198.18.${Math.floor(Math.random() * 250) + 1}.${Math.floor(Math.random() * 250) + 1}`;
}

function waitForApi(page: Page, path: string): Promise<Response> {
  return page.waitForResponse(
    (r) => new URL(r.url()).pathname === `/api/v1${path}` && r.request().method() === 'GET',
  );
}

test.describe('SIM-01 demo seed: admin@demo.local, 40 CIs, search, no acme data', () => {
  test.beforeAll(async ({ browser, playwright }) => {
    // The first run builds the seed with the Go toolchain.
    test.setTimeout(12 * 60_000);
    const orgs = seedDemo();
    const demo = orgs.find((o) => o.slug === 'demo');
    const acme = orgs.find((o) => o.slug === 'acme');
    expect(demo, 'seeded organization demo').toEqual({
      slug: 'demo',
      id: seedId('', 'org', 'demo'),
      admin: 'admin@demo.local',
    });
    expect(acme, 'seeded organization acme').toEqual({
      slug: 'acme',
      id: seedId('', 'org', 'acme'),
      admin: 'admin@acme.local',
    });
    const rq = await playwright.request.newContext();
    const { account, created } = await devRealmAccount(rq, 'admin@demo.local');
    const ctx = await browser.newContext({
      baseURL: BASE,
      locale: 'de-DE',
      extraHTTPHeaders: { 'X-Real-IP': realIp() },
    });
    const page = await ctx.newPage();
    const token = await uiLogin(page, account);
    w = { rq, ctx, page, token, demo: demo!, acme: acme!, created };
  });

  test.afterAll(async () => {
    await w?.ctx.close();
    await w?.rq.dispose();
  });

  test('S1 admin@demo.local signs in to the demo organization and the CI list shows its 40 CIs', async () => {
    if (w.created) {
      test.info().annotations.push({
        type: 'setup',
        description:
          'admin@demo.local created through the Keycloak admin API from realm-reticora-dev.json (realm import skipped)',
      });
    }
    const me = await api<{ org: string }>(w.rq, w.token, 'GET', '/me');
    expect(me.status, 'GET /me').toBe(200);
    expect(me.body.org, 'organization of the session').toBe(w.demo.id);
    const org = await api<{ id: string; slug: string; plan: string }>(w.rq, w.token, 'GET', '/org');
    expect(org.status, 'GET /org').toBe(200);
    expect(org.body).toMatchObject({ id: w.demo.id, slug: 'demo', plan: 'pro' });
    const cis = await api<List<CI>>(w.rq, w.token, 'GET', '/cis?limit=1');
    expect(cis.status, 'GET /cis').toBe(200);
    expect(cis.body.total, 'CIs of the demo organization').toBe(40);

    await w.page.goto('/cmdb');
    const main = w.page.locator('#main-content');
    await expect(main.getByText(/^40 Einträge$/)).toBeVisible();
  });

  test('S2 the global search finds srv-00021 of demo and opens it; it is located in the server room hq', async () => {
    const id = seedId('demo', 'ci', '21');
    const hits = await api<List<SearchHit>>(w.rq, w.token, 'GET', '/search?q=srv-00021');
    expect(hits.status, 'GET /search?q=srv-00021').toBe(200);
    expect(
      rows(hits.body).map((h) => [h.entity_type, h.entity_id, h.title, h.url]),
      'search hits of srv-00021',
    ).toEqual([['ci', id, 'srv-00021', `/cmdb/${id}`]]);

    // Command palette (SRC-01): exactly one hit, it opens the CI of demo.
    await w.page.goto('/dashboard');
    await w.page.locator('body').click();
    await w.page.keyboard.press('Control+k');
    const box = w.page.getByRole('combobox', { name: 'Befehle durchsuchen' });
    await expect(box).toBeFocused();
    await box.fill('srv-00021');
    const option = w.page.getByRole('option', { name: /srv-00021 · ci/ });
    await expect(option).toHaveCount(1);
    await option.click();
    await expect(w.page).toHaveURL(new RegExp(`/cmdb/${id}$`));
    await expect(w.page.getByRole('heading', { name: 'srv-00021', exact: true })).toBeVisible();

    // Location chain (F-278): mounted in rack R02 of the server room hq.
    const ci = await api<CI>(w.rq, w.token, 'GET', `/cis/${id}`);
    expect(ci.status, `GET /cis/${id}`).toBe(200);
    expect(ci.body).toMatchObject({
      organization_id: w.demo.id,
      location_id: seedId('demo', 'rack', '2'),
      room_id: seedId('demo', 'room', 'hq'),
      site_id: seedId('demo', 'site', 'hq'),
    });
  });

  test('S3 nothing of acme is visible to the demo administrator', async () => {
    const foreign = seedId('acme', 'ci', '21');
    const detail = await api(w.rq, w.token, 'GET', `/cis/${foreign}`);
    expect(detail.status, 'GET /cis/{CI 21 of acme}').toBe(404);

    // The serial numbers differ per organization: acme's is not found, demo's is.
    const own = await api<List<SearchHit>>(
      w.rq,
      w.token,
      'GET',
      `/search?q=${seedSerial('demo', 21)}`,
    );
    expect(
      rows(own.body).map((h) => h.entity_id),
      'control: serial number of demo',
    ).toEqual([seedId('demo', 'ci', '21')]);
    const other = await api<List<SearchHit>>(
      w.rq,
      w.token,
      'GET',
      `/search?q=${seedSerial('acme', 21)}`,
    );
    expect(other.status, 'search for the serial number of acme').toBe(200);
    expect(rows(other.body), 'hits of the serial number of acme').toEqual([]);

    // The CI page of the acme CI answers "not found".
    const page = waitForApi(w.page, `/cis/${foreign}`);
    await w.page.goto(`/cmdb/${foreign}`);
    expect((await page).status(), 'CI 21 of acme in the UI').toBe(404);
    await expect(
      w.page.getByText('Das Configuration Item konnte nicht geladen werden'),
    ).toBeVisible();
    expect(await w.page.locator('body').innerText()).not.toContain(seedSerial('acme', 21));
  });
});
