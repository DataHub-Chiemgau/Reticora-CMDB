import {
  test,
  expect,
  type APIRequestContext,
  type Browser,
  type BrowserContext,
  type Locator,
  type Page,
} from '@playwright/test';
import {
  api,
  raiseCILimit,
  scopedUser,
  signupTenant,
  stamp,
  uiLogin,
  BASE,
  type ScopedUser,
} from './support/stack';

/*
 * Topology view on the real stack (WP-552; UI-06, IMP-05, IMP-06), nothing mocked:
 * UI → REST API → PostgreSQL → UI.
 *
 * One organization is created through the self-signup UI; an operator raises its max_cis
 * (support/stack.ts, raiseCILimit). The administrator seeds through the REST API:
 *  - client C1 with site S1: the PDUs pdu-a and pdu-b (type pdu), the server srv-red powered
 *    by both (powered_by is a redundant type, IMP-04) and the server srv-single powered by
 *    pdu-a only;
 *  - client C2 with site S2: 2,001 servers (type server).
 * The organization holds 2,005 CIs, client C2 alone 2,001: both are above the 2,000 CIs a
 * topology without a root may hold (IMP-06), so /topology opens the start dialog.
 *
 * Test → WP / requirement IDs proven:
 *  TP01 /topology without a root shows the start dialog; the site path sends site_id, the
 *       node list holds the nodes of the API answer; the type badge shows the type label, not
 *       the type id ......................................... WP-195, WP-552 (UI-06, IMP-06; F-344)
 *  TP02 the type path sends ci_type=<type UUID>, not the type key; node count = API answer
 *       ................................................................ WP-180, WP-552 (IMP-06)
 *  TP03 ?simulate=a,b shows exactly the CIs of GET /api/v1/impact?ci_ids=a,b; srv-red is
 *       impacted only when both PDUs fail ............... WP-180, WP-195, WP-552 (IMP-05, UI-06)
 *  TP04 101 failed CIs: the 422 of /impact is a visible error, no client-side impact
 *       ...................................................... WP-180, WP-552 (IMP-05; F-326)
 *  TP05 a client technician of C2 gets the start dialog without the sites of C1
 *       ......................................................... WP-552 (UI-06, TEN-04, TEN-06)
 *
 * A failing test is a product finding: the assertions encode UI-06, IMP-05 and IMP-06 of
 * docs/spec/katalog-v3 and the acceptance criteria of WP-552. No test of this spec is listed
 * in tests/audit/expected-failures.txt.
 */

/** CIs of client C2: one more than a topology without a root holds (IMP-06). */
const BULK_CIS = 2_001;
/** Parallel POST /cis calls while seeding. */
const SEED_PARALLEL = 25;

interface List<T> {
  data: T[] | null;
  total?: number;
}

interface Named {
  id: string;
  name: string;
}

interface CI extends Named {
  client_id?: string;
  site_id?: string;
  ci_type_id?: string;
}

interface TopologyGraph {
  nodes: { id: string; name: string; ci_type: string }[];
  edges: { id: string }[];
}

interface Impact {
  impacted: { id: string; name?: string }[];
}

interface Session {
  ctx: BrowserContext;
  page: Page;
  token: string;
}

interface World {
  rq: APIRequestContext;
  admin: Session;
  s: string;
  pduType: string;
  serverType: string;
  c1: Named;
  c2: Named;
  s1: Named;
  s2: Named;
  pduA: CI;
  pduB: CI;
  srvRed: CI;
  srvSingle: CI;
  bulk: CI[];
}

let w: World;

/** A documentation address (198.18.0.0/15) so each signup and login has its own budget (E-33). */
function realIp(): string {
  return `198.18.${Math.floor(Math.random() * 250) + 1}.${Math.floor(Math.random() * 250) + 1}`;
}

function rows<T>(list: List<T> | null | undefined): T[] {
  return list?.data ?? [];
}

function claims(token: string): { sub: string; org: string } {
  return JSON.parse(Buffer.from(token.split('.')[1] ?? '', 'base64url').toString()) as {
    sub: string;
    org: string;
  };
}

/** ok asserts a 2xx answer of a call as the administrator and returns its body. */
async function ok<T>(method: 'GET' | 'POST', path: string, data?: unknown): Promise<T> {
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

/** topologyResponse waits for the page's GET /api/v1/topology. */
function topologyResponse(page: Page) {
  return page.waitForResponse(
    (r) => new URL(r.url()).pathname === '/api/v1/topology' && r.request().method() === 'GET',
  );
}

/** impactResponse waits for the page's GET /api/v1/impact (the joint analysis, IMP-05). */
function impactResponse(page: Page) {
  return page.waitForResponse(
    (r) => new URL(r.url()).pathname === '/api/v1/impact' && r.request().method() === 'GET',
  );
}

function startDialog(page: Page): Locator {
  return page.getByRole('form', { name: 'Ausschnitt wählen' });
}

function nodeList(page: Page): Locator {
  return page
    .locator('#main-content section')
    .filter({ has: page.getByRole('heading', { name: 'Knotenliste' }) });
}

function simulationPanel(page: Page): Locator {
  return page
    .locator('#main-content section')
    .filter({ has: page.getByRole('heading', { name: 'Ausfallsimulation' }) });
}

/** openStartDialog opens /topology without a root and expects the 422 and the dialog. */
async function openStartDialog(page: Page): Promise<Locator> {
  const loaded = topologyResponse(page);
  await page.goto('/topology');
  const res = await loaded;
  expect(res.status(), 'GET /topology without a root and filter (IMP-06)').toBe(422);
  expect(new URL(res.url()).searchParams.toString(), 'no filter sent').toBe('');
  const dialog = startDialog(page);
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText('mehr als 2.000 CIs');
  await expect(page.getByRole('heading', { name: 'Knotenliste' })).toHaveCount(0);
  return dialog;
}

/** seedCIs creates the CIs of `bodies` through POST /cis, SEED_PARALLEL at a time. */
async function seedCIs(bodies: Record<string, unknown>[]): Promise<CI[]> {
  const out: CI[] = [];
  for (let i = 0; i < bodies.length; i += SEED_PARALLEL) {
    out.push(
      ...(await Promise.all(
        bodies.slice(i, i + SEED_PARALLEL).map((body) => ok<CI>('POST', '/cis', body)),
      )),
    );
  }
  return out;
}

/** impactOf runs GET /api/v1/impact?ci_ids= as the administrator. */
async function impactOf(ids: string[]): Promise<Impact> {
  return ok<Impact>('GET', `/impact?ci_ids=${ids.join(',')}`);
}

/**
 * expectSimulation opens the simulation of `failed` in site S1 and expects exactly the CIs of
 * the server analysis `expected` in the panel, after the analysis answered (no preview).
 */
async function expectSimulation(page: Page, failed: CI[], expected: Impact) {
  const analysed = impactResponse(page);
  await page.goto(`/topology?site=${w.s1.id}&simulate=${failed.map((c) => c.id).join(',')}`);
  const res = await analysed;
  expect(res.status(), 'GET /impact of the page').toBe(200);
  const sent = new URL(res.url()).searchParams.get('ci_ids') ?? '';
  expect(sent.split(',').sort(), 'ci_ids of the page').toEqual(failed.map((c) => c.id).sort());
  const fromPage = ((await res.json()) as Impact).impacted.map((n) => n.id).sort();
  expect(fromPage, 'the page asks the same analysis').toEqual(
    expected.impacted.map((n) => n.id).sort(),
  );

  const panel = simulationPanel(page);
  await expect(panel).toBeVisible();
  // The server answer replaced the preview of the client walk.
  await expect(panel.getByText('Vorschau', { exact: true })).toHaveCount(0);
  await expect(panel.getByRole('alert')).toHaveCount(0);
  const names = expected.impacted.map((n) => n.name ?? n.id).sort();
  await expect
    .poll(
      async () => (await panel.getByRole('listitem').allInnerTexts()).map((t) => t.trim()).sort(),
      {
        message: `impacted CIs shown for the failure of ${failed.map((c) => c.name).join(', ')}`,
      },
    )
    .toEqual(names);
}

test.describe.configure({ mode: 'serial' });

test.describe('topology view: start dialog, joint simulation, isolation (UI-06, IMP-05, IMP-06)', () => {
  test.beforeAll(async ({ browser, playwright }) => {
    test.setTimeout(15 * 60_000);
    const rq = await playwright.request.newContext();
    const ctx = await browser.newContext({
      baseURL: BASE,
      locale: 'de-DE',
      extraHTTPHeaders: { 'X-Real-IP': realIp() },
    });
    const page = await ctx.newPage();
    const account = await signupTenant(page, rq, 'topology');
    const token = await uiLogin(page, account);
    const s = stamp();
    w = { rq, admin: { ctx, page, token }, s } as World;

    // A trial tenant holds 100 CIs (ENT-07).
    await raiseCILimit(browser, rq, claims(token).org, BULK_CIS + 100);

    const types = rows(await ok<List<{ id: string; key: string }>>('GET', '/ci-types?limit=200'));
    w.pduType = types.find((t) => t.key === 'pdu')?.id ?? '';
    w.serverType = types.find((t) => t.key === 'server')?.id ?? '';
    expect(w.pduType, 'catalog type pdu').not.toBe('');
    expect(w.serverType, 'catalog type server').not.toBe('');
    const before = await ok<{ total: number }>('GET', '/cis?limit=1');

    w.c1 = await ok<Named>('POST', '/clients', { name: `Topo C1 ${s}`, slug: `topo-c1-${s}` });
    w.c2 = await ok<Named>('POST', '/clients', { name: `Topo C2 ${s}`, slug: `topo-c2-${s}` });
    w.s1 = await ok<Named>('POST', '/sites', { name: `Topo RZ Eins ${s}`, client_id: w.c1.id });
    w.s2 = await ok<Named>('POST', '/sites', { name: `Topo RZ Zwei ${s}`, client_id: w.c2.id });

    const inS1 = (name: string, type: string) => ({
      ci_type_id: type,
      name: `${name}-${s}`,
      client_id: w.c1.id,
      location_id: w.s1.id,
    });
    w.pduA = await ok<CI>('POST', '/cis', inS1('pdu-a', w.pduType));
    w.pduB = await ok<CI>('POST', '/cis', inS1('pdu-b', w.pduType));
    w.srvRed = await ok<CI>('POST', '/cis', inS1('srv-red', w.serverType));
    w.srvSingle = await ok<CI>('POST', '/cis', inS1('srv-single', w.serverType));
    for (const ci of [w.pduA, w.pduB, w.srvRed, w.srvSingle]) {
      expect(ci.site_id, `${ci.name} in site S1`).toBe(w.s1.id);
      expect(ci.client_id, `${ci.name} of client C1`).toBe(w.c1.id);
    }
    for (const [source, target] of [
      [w.srvRed, w.pduA],
      [w.srvRed, w.pduB],
      [w.srvSingle, w.pduA],
    ] as const) {
      await ok('POST', '/relationships', {
        source_ci_id: source.id,
        target_ci_id: target.id,
        rel_type: 'powered_by',
      });
    }

    w.bulk = await seedCIs(
      Array.from({ length: BULK_CIS }, (_, i) => ({
        ci_type_id: w.serverType,
        name: `bulk-${String(i).padStart(4, '0')}-${s}`,
        client_id: w.c2.id,
        location_id: w.s2.id,
      })),
    );
    expect(w.bulk).toHaveLength(BULK_CIS);
    expect(w.bulk[0]?.site_id, 'bulk CIs in site S2').toBe(w.s2.id);
    const all = await ok<{ total: number }>('GET', '/cis?limit=1');
    expect(all.total - before.total, 'CIs seeded into the organization').toBe(BULK_CIS + 4);
    expect(all.total, 'more CIs than a topology without a root holds').toBeGreaterThan(2_000);
  });

  test.afterAll(async () => {
    await w?.admin?.ctx.close();
    await w?.rq.dispose();
  });

  test('TP01 /topology without a root opens the start dialog; the site path sends site_id and lists the nodes of the API answer with their type label', async () => {
    const page = w.admin.page;
    const dialog = await openStartDialog(page);
    const show = dialog.getByRole('button', { name: 'Topologie anzeigen' });
    await expect(show).toBeDisabled();

    await dialog.getByLabel('Standort').selectOption({ label: w.s1.name });
    const loaded = topologyResponse(page);
    await show.click();
    const res = await loaded;
    expect(res.status(), 'GET /topology?site_id=S1').toBe(200);
    const params = new URL(res.url()).searchParams;
    expect(params.get('site_id'), 'site path sends site_id').toBe(w.s1.id);
    expect(params.has('ci_type'), 'the site path sends no type').toBe(false);
    const graph = (await res.json()) as TopologyGraph;
    expect(graph.nodes.map((n) => n.id).sort(), 'the CIs of S1').toEqual(
      [w.pduA.id, w.pduB.id, w.srvRed.id, w.srvSingle.id].sort(),
    );
    expect(graph.edges, 'the three powered_by edges of S1').toHaveLength(3);

    const list = nodeList(page);
    await expect(list.getByRole('listitem')).toHaveCount(graph.nodes.length);
    for (const node of graph.nodes) {
      await expect(list.getByRole('button', { name: node.name, exact: true })).toBeVisible();
    }
    // F-344: the badge shows the label of the type, as the type filter lists it, not its id.
    const pduLabel = (
      await page
        .getByLabel('Nach CI-Typ filtern')
        .locator(`option[value="${w.pduType}"]`)
        .textContent()
    )?.trim();
    expect(pduLabel, 'label of the type pdu in the type filter').toBeTruthy();
    const pduRow = list
      .getByRole('listitem')
      .filter({ has: page.getByRole('button', { name: w.pduA.name, exact: true }) });
    await expect(pduRow.getByText(pduLabel!, { exact: true })).toBeVisible();
    await expect(pduRow).not.toContainText(w.pduType);
  });

  test('TP02 the type path of the start dialog sends ci_type with the type UUID and shows the nodes of the API answer', async () => {
    const page = w.admin.page;
    const dialog = await openStartDialog(page);
    await dialog.getByLabel('CI-Typ').selectOption(w.pduType);
    const loaded = topologyResponse(page);
    await dialog.getByRole('button', { name: 'Topologie anzeigen' }).click();
    const res = await loaded;
    expect(res.status(), 'GET /topology?ci_type=<pdu>').toBe(200);
    const params = new URL(res.url()).searchParams;
    expect(params.get('ci_type'), 'type path sends the CI type id (IMP-06)').toBe(w.pduType);
    expect(params.get('ci_type'), 'not the type key').not.toBe('pdu');
    expect(params.has('site_id'), 'the type path sends no site').toBe(false);
    const graph = (await res.json()) as TopologyGraph;
    expect(graph.nodes.map((n) => n.id).sort(), 'the PDUs of the organization').toEqual(
      [w.pduA.id, w.pduB.id].sort(),
    );
    await expect(nodeList(page).getByRole('listitem')).toHaveCount(graph.nodes.length);
    await expect(page).toHaveURL(new RegExp(`[?&]type=${w.pduType}`));
  });

  test('TP03 ?simulate=a,b shows exactly the CIs of /api/v1/impact; the redundantly fed server fails only with both PDUs', async () => {
    const page = w.admin.page;
    // The API answers first: srv-red keeps pdu-b (IMP-04), srv-single has pdu-a only.
    const onlyA = await impactOf([w.pduA.id]);
    expect(
      onlyA.impacted.map((n) => n.id),
      'GET /impact?ci_ids=pdu-a',
    ).toEqual([w.srvSingle.id]);
    const onlyB = await impactOf([w.pduB.id]);
    expect(
      onlyB.impacted.map((n) => n.id),
      'GET /impact?ci_ids=pdu-b',
    ).toEqual([]);
    const both = await impactOf([w.pduA.id, w.pduB.id]);
    expect(both.impacted.map((n) => n.id).sort(), 'GET /impact?ci_ids=pdu-a,pdu-b').toEqual(
      [w.srvRed.id, w.srvSingle.id].sort(),
    );

    await expectSimulation(page, [w.pduA, w.pduB], both);
    await expect(simulationPanel(page)).toContainText(
      `Fallen 2 CIs (${w.pduA.name}, ${w.pduB.name}) gemeinsam aus, sind 2 weitere CIs betroffen`,
    );
    await expectSimulation(page, [w.pduA], onlyA);
    await expect(simulationPanel(page)).toContainText(
      `Fällt „${w.pduA.name}“ aus, sind 1 weitere CIs betroffen`,
    );
    await expectSimulation(page, [w.pduB], onlyB);
    await expect(simulationPanel(page)).toContainText('Dieses CI hat keine abhängigen CIs');
  });

  test('TP04 101 failed CIs: the page shows the 422 of /impact as an error and no client-side impact', async () => {
    const page = w.admin.page;
    const failed = [w.pduA, ...w.bulk.slice(0, 100)];
    expect(failed).toHaveLength(101);
    const direct = await api<{ field?: string }>(
      w.rq,
      w.admin.token,
      'GET',
      `/impact?ci_ids=${failed.map((c) => c.id).join(',')}`,
    );
    expect(direct.status, 'GET /impact with 101 CIs').toBe(422);
    expect(direct.body?.field).toBe('ci_ids');

    const analysed = impactResponse(page);
    await page.goto(`/topology?site=${w.s1.id}&simulate=${failed.map((c) => c.id).join(',')}`);
    expect((await analysed).status(), 'GET /impact of the page with 101 CIs').toBe(422);
    const panel = simulationPanel(page);
    const alert = panel.getByRole('alert');
    await expect(alert).toContainText('Die Auswirkung konnte nicht berechnet werden.');
    await expect(alert).toContainText('höchstens 100 CIs je Analyse');
    await expect(alert.getByRole('button', { name: 'Erneut versuchen' })).toBeVisible();
    // F-326: no impact from the client walk (srv-red and srv-single depend on pdu-a).
    await expect(panel.getByRole('listitem')).toHaveCount(0);
    await expect(panel).not.toContainText('weitere CIs betroffen');
    await expect(panel).not.toContainText(w.srvSingle.name);
    await expect(panel.getByText('Vorschau', { exact: true })).toHaveCount(0);
    await expect(nodeList(page).getByRole('listitem')).toHaveCount(4);
  });

  test('TP05 a client technician of C2 gets the start dialog without the sites of client C1', async ({
    browser,
  }) => {
    test.setTimeout(5 * 60_000);
    const user: ScopedUser = await scopedUser(w.rq, w.admin.token, {
      label: 'topo-c2',
      role: 'client_technician',
      scope_type: 'client',
      scope_id: w.c2.id,
      inviteClientId: w.c2.id,
    });
    const session = await newSession(browser, user);
    try {
      const page = session.page;
      expect(claims(session.token).sub).toBe(user.userId);
      // The API of the C2 user: S2, not S1.
      const sites = await api<List<Named>>(w.rq, session.token, 'GET', '/sites?limit=200');
      expect(sites.status, 'GET /sites as the C2 user').toBe(200);
      const siteIds = rows(sites.body).map((x) => x.id);
      expect(siteIds, 'GET /sites as the C2 user').toContain(w.s2.id);
      expect(siteIds, 'GET /sites as the C2 user').not.toContain(w.s1.id);

      const sitesLoaded = page.waitForResponse(
        (r) => new URL(r.url()).pathname === '/api/v1/sites' && r.request().method() === 'GET',
      );
      // The 2,001 CIs of C2 alone exceed the limit: the C2 user gets the start dialog too.
      const dialog = await openStartDialog(page);
      const sitesBody = JSON.stringify(await (await sitesLoaded).json());
      expect(sitesBody, 'sites of the start dialog').not.toContain(w.s1.id);
      expect(sitesBody, 'sites of the start dialog').not.toContain(w.s1.name);
      const siteSelect = dialog.getByLabel('Standort');
      await expect(siteSelect.locator('option', { hasText: w.s2.name })).toHaveCount(1);
      await expect(siteSelect.locator('option', { hasText: w.s1.name })).toHaveCount(0);
      await expect(siteSelect.locator(`option[value="${w.s1.id}"]`)).toHaveCount(0);
      await expect(page.locator('#main-content')).not.toContainText(w.s1.name);
      await expect(page.locator('#main-content')).not.toContainText(w.c1.name);

      // The site filter of the page lists the same sites.
      const filter = page.getByLabel('Nach Standort filtern');
      await expect(filter.locator('option', { hasText: w.s1.name })).toHaveCount(0);
      await expect(filter.locator('option', { hasText: w.s2.name })).toHaveCount(1);
    } finally {
      await session.ctx.close();
    }
  });
});
