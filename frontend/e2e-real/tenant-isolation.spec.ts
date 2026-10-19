import {
  test,
  expect,
  type APIRequestContext,
  type Browser,
  type BrowserContext,
  type Page,
} from '@playwright/test';
import { api, signupTenant, stamp, uiLogin, BASE, type Account } from './support/stack';

/*
 * Phase-1 acceptance audit – tenant isolation end-to-end (real stack, nothing mocked).
 *
 * Two organizations are created through the self-signup UI. Tenant A creates data
 * (through the UI where the UI offers a form, otherwise through the REST API the UI
 * uses), tenant B then tries every channel to read, change or reference it.
 * Requirement: every cross-organization access yields "not found" (404, or 403) or an
 * empty result, never A's data, never a change of A's data (TEN-06: "eine falsche Org
 * liefert 0 Zeilen"; TEN-09: "Isolationstest über alle Zugriffspfade ... Jobs, Review-
 * Items, Locations. Jede Cross-Org-Exposition ist Critical").
 *
 * Test → WP / requirement IDs proven:
 *  T01 A creates a site in the location tree UI ......... WP-014, WP-026, WP-053, WP-186, WP-208 (TEN-02, TEN-06, TEN-09, LOC-10, SRC-01)
 *  T02 A adds a relationship on the CI detail page ...... WP-010, WP-128, WP-208 (TEN-06, TEN-09, REL-02)
 *  T03 B's lists/search/topology/export/audit/review .... WP-010, WP-015, WP-016, WP-031, WP-032, WP-035, WP-089, WP-132, WP-208
 *                                                         (TEN-06, TEN-09, SRC-01, EXP-01, IMP-06, AUD-04, REC-12)
 *  T04 B's GraphQL BFF .................................. WP-184, WP-185, WP-036, WP-208 (GQL-01, GQL-02, TEN-09)
 *  T05 B's IDOR on A's CI (session + API key) ........... WP-008, WP-009, WP-010, WP-011, WP-067, WP-169, WP-170, WP-208
 *                                                         (TEN-04, TEN-06, TEN-09, CI-05, CI-12, AUT-04)
 *  T06 B on sub-resources/neighbours/impact of A's CI ... WP-010, WP-011, WP-031, WP-180, WP-208 (TEN-06, TEN-09, IMP-05)
 *  T07 B references A's location/client/parent CI ....... WP-014, WP-023, WP-026, WP-054, WP-208 (TEN-05, TEN-09, LOC-10)
 *  T08 B on A's review item, export job and job ......... WP-035, WP-089, WP-208 (TEN-09, EXP-01, REC-12)
 *  T09 organization headers / query parameters .......... WP-008, WP-009, WP-043 (TEN-04, AUT-09)
 *  T10 B's UI: CI URL, list, palette, pages ............. WP-191, WP-208 (UI-04, UI-05, UI-06, UI-09, TEN-09)
 *  T11 B's relationship form with A's CI id (UI) ........ WP-128, WP-208 (TEN-09, REL-02)
 *  T12 A's audit chain after B's attempts ............... WP-016, WP-132, WP-208 (AUD-04, TEN-09)
 *  T13 B on A's contact and shared saved view .......... WP-011, WP-015, WP-186, WP-208 (TEN-06, TEN-09, SRC-01)
 *  T14 B's relationship to A's CI: status of the refusal  WP-010, WP-128, WP-163, WP-208 (TEN-05, TEN-09, REL-02, API-03)
 *  T15 topology rooted at A's CI ........................ WP-031, WP-163, WP-208 (IMP-06, API-03, TEN-09)
 *  T16 A creates a CI through the CMDB form ............. WP-117 (CI-03), WP-239, WP-010, WP-208 (TEN-06, TEN-09)
 *  T17 A's audit viewer shows time and object ........... WP-133, WP-132, WP-191 (AUD-05, AUD-07, UI-01..UI-10 audit viewer)
 *
 * The fixture data of A (beforeAll) is created through the REST API the UI uses: the CI
 * form cannot create CIs (T16) and clients have no UI form; T01/T02/T16 drive the UI forms.
 * Playwright restarts the worker after a failed test and beforeAll then builds a new pair
 * of tenants, so every test proves its claim on its own (each write attempt is followed by
 * an "A unchanged" check in the same test).
 *
 * Signup budget: the server allows 5 signups per client IP and hour (E-33). All real-stack
 * specs reach it from 127.0.0.1 through the frontend proxy, which the server trusts for
 * X-Real-IP; each tenant of this spec therefore signs up with its own documentation
 * address (198.18.0.0/15) so that the two organizations look like two customers.
 */

interface Tenant {
  ctx: BrowserContext;
  page: Page;
  account: Account;
  token: string;
  orgId: string;
  userId: string;
}

interface CI {
  id: string;
  organization_id: string;
  name: string;
  status: string;
  version: number;
  updated_at: string;
  serial_number?: string;
  location_id?: string;
  parent_ci_id?: string;
  client_id?: string;
  manufacturer?: string;
}

interface List<T> {
  data: T[] | null;
  total: number;
}

interface Relationship {
  id: string;
  organization_id: string;
  source_ci_id: string;
  target_ci_id: string;
  rel_type: string;
  verification_state?: string;
}

interface Location {
  id: string;
  organization_id: string;
  client_id?: string;
  kind: string;
  name: string;
  parent_id?: string | null;
}

interface ReviewItem {
  id: string;
  organization_id: string;
  status: string;
  kind: string;
  payload?: Record<string, unknown>;
}

interface World {
  rq: APIRequestContext;
  a: Tenant;
  b: Tenant;
  s: string;
  serverType: string;
  client: { id: string; name: string };
  site: { id: string; name: string };
  ci1: CI;
  ci2: CI;
  ci3: CI;
  ciDel: CI;
  ciM1: CI;
  ciM2: CI;
  rel: Relationship;
  review: ReviewItem;
  exportJobId: string;
  jobId: string;
  bOwn: CI;
  bApiKey: string;
}

let w: World;

const NOT_FOUND = [403, 404];

function claims(token: string): { sub: string; org: string } {
  return JSON.parse(Buffer.from(token.split('.')[1], 'base64url').toString()) as {
    sub: string;
    org: string;
  };
}

/** openTenant signs up a new organization in its own browser context and signs in. */
async function openTenant(browser: Browser, rq: APIRequestContext, label: string): Promise<Tenant> {
  const realIp = `198.18.${Math.floor(Math.random() * 250) + 1}.${Math.floor(Math.random() * 250) + 1}`;
  const ctx = await browser.newContext({
    baseURL: BASE,
    locale: 'de-DE',
    extraHTTPHeaders: { 'X-Real-IP': realIp },
  });
  const page = await ctx.newPage();
  const account = await signupTenant(page, rq, label);
  const token = await uiLogin(page, account);
  const c = claims(token);
  return { ctx, page, account, token, orgId: c.org, userId: c.sub };
}

/** ok asserts a 2xx answer of a setup call and returns its body. */
async function ok<T>(
  rq: APIRequestContext,
  token: string,
  method: 'GET' | 'POST' | 'PATCH' | 'DELETE',
  path: string,
  data?: unknown,
): Promise<T> {
  const res = await api<T>(rq, token, method, path, data);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeGreaterThanOrEqual(200);
  expect(res.status, `${method} ${path}: ${JSON.stringify(res.body)}`).toBeLessThan(300);
  return res.body;
}

/** markersOfA are values only tenant A's data contains. */
function markersOfA(except: string[] = []): string[] {
  return [
    w.a.orgId,
    w.a.userId,
    w.a.account.email,
    w.a.account.org,
    w.client.id,
    w.client.name,
    w.site.id,
    w.site.name,
    w.ci1.id,
    w.ci1.name,
    w.ci1.serial_number ?? '',
    w.ci2.id,
    w.ci2.name,
    w.ci3.id,
    w.ci3.name,
    w.ciDel.id,
    w.ciDel.name,
    w.ciM1.id,
    w.ciM1.name,
    w.ciM2.id,
    w.rel.id,
    w.review.id,
    w.exportJobId,
    w.jobId,
    `Vendor-A-${w.s}`,
  ].filter((m) => m && !except.includes(m));
}

/** expectNoTraceOfA fails with the list of A's values found in `body`. */
function expectNoTraceOfA(where: string, body: unknown, except: string[] = []) {
  const text = typeof body === 'string' ? body : JSON.stringify(body);
  const found = markersOfA(except).filter((m) => text.includes(m));
  expect(found, `${where} must not contain data of tenant A`).toEqual([]);
}

function rows<T>(body: List<T> | null | undefined): T[] {
  return body?.data ?? [];
}

/** snapshotA reads A's fixture objects as A (for "unchanged" checks). */
async function snapshotA() {
  const ci1 = await api<CI>(w.rq, w.a.token, 'GET', `/cis/${w.ci1.id}`);
  const rels = await api<List<Relationship>>(
    w.rq,
    w.a.token,
    'GET',
    `/cis/${w.ci1.id}/relationships`,
  );
  const site = await api<Location>(w.rq, w.a.token, 'GET', `/locations/${w.site.id}`);
  const children = await api<List<Location>>(
    w.rq,
    w.a.token,
    'GET',
    `/locations?parent_id=${w.site.id}`,
  );
  return {
    ci1: {
      status: ci1.status,
      name: ci1.body.name,
      ciStatus: ci1.body.status,
      version: ci1.body.version,
      updated_at: ci1.body.updated_at,
      location_id: ci1.body.location_id,
    },
    rels: rows(rels.body)
      .map((r) => `${r.id}:${r.rel_type}:${r.verification_state}`)
      .sort(),
    site: { status: site.status, name: site.body.name, client: site.body.client_id },
    children: rows(children.body).map((l) => l.id),
  };
}

async function waitForApi(page: Page, fragment: string, method = 'GET') {
  return page.waitForResponse(
    (r) => r.url().includes(`/api/v1${fragment}`) && r.request().method() === method,
  );
}

async function bodyText(page: Page): Promise<string> {
  return page.locator('body').innerText();
}

test.describe('tenant isolation (TEN-06, TEN-09)', () => {
  test.beforeAll(async ({ browser, playwright }) => {
    test.setTimeout(4 * 60_000);
    const rq = await playwright.request.newContext();
    const a = await openTenant(browser, rq, 'iso-a');
    const b = await openTenant(browser, rq, 'iso-b');
    expect(a.orgId, 'two separate organizations').not.toBe(b.orgId);
    const s = stamp();

    // A's fixture world through the REST API the UI uses. The CI form cannot
    // create CIs (T16) and clients have no UI form; T01, T02 and T16 drive
    // the UI forms of A.
    const types = await ok<List<{ id: string; key: string }>>(
      rq,
      a.token,
      'GET',
      '/ci-types?limit=200',
    );
    const serverType = rows(types).find((t) => t.key === 'server')?.id ?? '';
    expect(serverType, 'catalog type server').not.toBe('');
    const client = await ok<{ id: string; name: string }>(rq, a.token, 'POST', '/clients', {
      name: `Iso Client A ${s}`,
      slug: `iso-client-a-${s}`,
    });
    const site = await ok<Location>(rq, a.token, 'POST', '/locations', {
      kind: 'site',
      name: `Iso Site A ${s}`,
      client_id: client.id,
    });
    const newCI = (body: Record<string, unknown>) =>
      ok<CI>(rq, a.token, 'POST', '/cis', { ci_type_id: serverType, ...body });
    const ci1 = await newCI({
      name: `iso-a-ci1-${s}`,
      serial_number: `SN-ISO-A-${s}`,
      location_id: site.id,
      manufacturer: `Vendor-A-${s}`,
    });
    const ci2 = await newCI({ name: `iso-a-ci2-${s}` });
    const ci3 = await newCI({ name: `iso-a-ci3-${s}` });
    const ciDel = await newCI({ name: `iso-a-del-${s}` });
    await ok(rq, a.token, 'DELETE', `/cis/${ciDel.id}`);
    const rel = await ok<Relationship>(rq, a.token, 'POST', '/relationships', {
      source_ci_id: ci1.id,
      target_ci_id: ci2.id,
      rel_type: 'connected_to',
      source: 'manual',
    });
    // A review item of A (REC-12): merging two CIs whose manual overrides of
    // the same field contradict opens an override_conflict review.
    const ciM1 = await newCI({ name: `iso-a-m1-${s}` });
    const ciM2 = await newCI({ name: `iso-a-m2-${s}` });
    await ok(rq, a.token, 'PATCH', `/cis/${ciM1.id}`, { manufacturer: `Vendor-A-${s}-1` });
    await ok(rq, a.token, 'PATCH', `/cis/${ciM2.id}`, { manufacturer: `Vendor-A-${s}-2` });
    await ok(rq, a.token, 'POST', `/cis/${ciM1.id}/merge`, { victim_id: ciM2.id });
    const reviews = await ok<List<ReviewItem>>(rq, a.token, 'GET', '/review-items?limit=200');
    const review = rows(reviews).find((r) => r.payload?.ci_id === ciM1.id);
    expect(review, 'A has an open review item').toBeTruthy();
    const exportJob = await ok<{ id: string }>(rq, a.token, 'POST', '/exports', {
      format: 'json',
    });
    const jobs = await ok<List<{ id: string; ref_id?: string }>>(
      rq,
      a.token,
      'GET',
      '/jobs?limit=50',
    );
    const jobId = rows(jobs).find((j) => j.ref_id === exportJob.id)?.id ?? '';
    expect(jobId, 'job of A’s export').not.toBe('');

    // B's own data and an API key of B (second REST channel, AUT-04).
    const bOwn = await ok<CI>(rq, b.token, 'POST', '/cis', {
      ci_type_id: serverType,
      name: `iso-b-own-${s}`,
    });
    const key = await ok<{ key: string }>(rq, b.token, 'POST', '/api-keys', {
      name: `iso-b-${s}`,
      permissions: [
        'ci:read',
        'ci:write',
        'ci:delete',
        'relationship:read',
        'relationship:write',
        'location:read',
        'location:write',
        'topology:read',
      ],
    });

    w = {
      rq,
      a,
      b,
      s,
      serverType,
      client,
      site: { id: site.id, name: site.name },
      ci1,
      ci2,
      ci3,
      ciDel,
      ciM1,
      ciM2,
      rel,
      review: review!,
      exportJobId: exportJob.id,
      jobId,
      bOwn,
      bApiKey: key.key,
    };
  });

  test.afterAll(async () => {
    await w?.a.ctx.close();
    await w?.b.ctx.close();
    await w?.rq.dispose();
  });

  test('T01 A creates a site in the location tree UI; B can neither see, resolve, change nor reference it', async () => {
    const page = w.a.page;
    const name = `Iso UI Site ${w.s}`;
    await page.goto('/locations');
    await page.getByRole('button', { name: 'Standort anlegen' }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByLabel('Name', { exact: true }).fill(name);
    const clientSelect = dialog.getByLabel('Mandant');
    await expect(clientSelect.locator('option', { hasText: w.client.name })).toHaveCount(1);
    await clientSelect.selectOption({ label: w.client.name });
    const created = waitForApi(page, '/locations', 'POST');
    await dialog.getByRole('button', { name: 'Anlegen' }).click();
    const res = await created;
    expect(res.status(), `POST /locations from the tree: ${await res.text()}`).toBe(201);
    const loc = (await res.json()) as Location;
    await expect(dialog).toBeHidden();
    const tree = page.locator('#main-content');
    await expect(tree.getByText(name, { exact: true })).toBeVisible();

    // UI → API → DB: A's site, A's client.
    const asA = await api<Location>(w.rq, w.a.token, 'GET', `/locations/${loc.id}`);
    expect(asA.status).toBe(200);
    expect(asA.body).toMatchObject({
      organization_id: w.a.orgId,
      client_id: w.client.id,
      kind: 'site',
      name,
    });

    // B: by id – read, rename, delete.
    for (const [method, data] of [
      ['GET', undefined],
      ['PATCH', { name: `${name} (B)` }],
      ['DELETE', undefined],
    ] as const) {
      const r = await api(w.rq, w.b.token, method, `/locations/${loc.id}`, data);
      expect(NOT_FOUND, `${method} /locations/{A} as B → ${r.status}`).toContain(r.status);
      expect(JSON.stringify(r.body)).not.toContain(name);
    }
    const siteB = await api(w.rq, w.b.token, 'GET', `/sites/${loc.id}`);
    expect(NOT_FOUND, `GET /sites/{A} as B → ${siteB.status}`).toContain(siteB.status);
    // B: lists, tree, search.
    for (const path of [
      '/locations',
      '/locations/tree',
      `/locations?search=${encodeURIComponent(name)}`,
      `/locations?parent_id=${loc.id}`,
      '/sites',
      `/search?q=${encodeURIComponent(name)}`,
    ]) {
      const r = await api(w.rq, w.b.token, 'GET', path);
      expect(r.status, `GET ${path} as B`).toBe(200);
      expect(JSON.stringify(r.body), `GET ${path} as B`).not.toContain(name);
      expect(JSON.stringify(r.body), `GET ${path} as B`).not.toContain(loc.id);
    }
    // B: reference it from own data.
    const ciRef = await api(w.rq, w.b.token, 'POST', '/cis', {
      ci_type_id: w.serverType,
      name: `iso-b-ref-${w.s}`,
      location_id: loc.id,
    });
    expect([400, 403, 404, 422], `CI of B in A's site → ${ciRef.status}`).toContain(ciRef.status);
    const child = await api(w.rq, w.b.token, 'POST', '/locations', {
      kind: 'building',
      name: `iso-b-bldg-${w.s}`,
      parent_id: loc.id,
    });
    expect([400, 403, 404, 422], `building of B under A's site → ${child.status}`).toContain(
      child.status,
    );
    // B's UI does not show it.
    const pageB = w.b.page;
    const treeB = waitForApi(pageB, '/locations/tree');
    await pageB.goto('/locations');
    await treeB;
    await expect(pageB.getByRole('heading', { name: 'Standorte & Lager' })).toBeVisible();
    await expect(pageB.locator('#main-content').getByText(name)).toHaveCount(0);
    expect(await bodyText(pageB)).not.toContain(name);

    // A afterwards: unchanged, no foreign children; A's UI still shows it.
    const after = await api<Location>(w.rq, w.a.token, 'GET', `/locations/${loc.id}`);
    expect(after.status).toBe(200);
    expect(after.body.name).toBe(name);
    const children = await api<List<Location>>(
      w.rq,
      w.a.token,
      'GET',
      `/locations?parent_id=${loc.id}`,
    );
    expect(rows(children.body)).toEqual([]);
    await page.reload();
    await expect(tree.getByText(name, { exact: true })).toBeVisible();
  });

  test('T02 A adds a relationship on the CI detail page; B can neither see nor change it', async () => {
    const page = w.a.page;
    await page.goto(`/cmdb/${w.ci1.id}`);
    await expect(page.getByRole('heading', { name: w.ci1.name })).toBeVisible();
    await page.getByRole('button', { name: /Beziehung hinzufügen/ }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByLabel('Beziehungstyp').selectOption('depends_on');
    await dialog.getByLabel('CI-ID der Gegenstelle').fill(w.ci3.id);
    const created = waitForApi(page, '/relationships', 'POST');
    await dialog.getByRole('button', { name: 'Anlegen' }).click();
    const res = await created;
    expect(res.status(), `POST /relationships from the CI page: ${await res.text()}`).toBe(201);
    const rel = (await res.json()) as Relationship;
    await expect(dialog).toBeHidden();
    await expect(page.getByRole('link', { name: w.ci3.id })).toBeVisible();

    const relsA = await api<List<Relationship>>(
      w.rq,
      w.a.token,
      'GET',
      `/cis/${w.ci1.id}/relationships`,
    );
    const stored = rows(relsA.body).find((r) => r.id === rel.id);
    expect(stored, 'relationship stored for A').toBeTruthy();
    expect(stored!.organization_id).toBe(w.a.orgId);

    // B: change or delete the edge, list it.
    const patch = await api(w.rq, w.b.token, 'PATCH', `/relationships/${rel.id}`, {
      confidence: 0.1,
      verification_state: 'disputed',
    });
    expect(NOT_FOUND, `PATCH /relationships/{A} as B → ${patch.status}`).toContain(patch.status);
    const del = await api(w.rq, w.b.token, 'DELETE', `/relationships/${rel.id}`);
    expect(NOT_FOUND, `DELETE /relationships/{A} as B → ${del.status}`).toContain(del.status);
    for (const path of [
      '/relationships?limit=200',
      `/relationships?ci_id=${w.ci1.id}`,
      `/cis/${w.ci1.id}/relationships`,
      `/cis/${w.ci3.id}/relationships`,
    ]) {
      const r = await api<List<Relationship>>(w.rq, w.b.token, 'GET', path);
      if (r.status === 200) {
        expect(rows(r.body), `GET ${path} as B`).toEqual([]);
      } else {
        expect(NOT_FOUND, `GET ${path} as B → ${r.status}`).toContain(r.status);
      }
      expect(JSON.stringify(r.body)).not.toContain(rel.id);
    }

    // A afterwards: the edge is unchanged and still shown.
    const afterA = await api<List<Relationship>>(
      w.rq,
      w.a.token,
      'GET',
      `/cis/${w.ci1.id}/relationships`,
    );
    const again = rows(afterA.body).find((r) => r.id === rel.id);
    expect(again, 'relationship still present for A').toBeTruthy();
    expect(again!.verification_state).toBe(stored!.verification_state);
    await page.reload();
    await expect(page.getByRole('link', { name: w.ci3.id })).toBeVisible();
  });

  // T03 runs before B's write attempts (T05+) so that B's audit trail
  // holds only B's own setup.
  test('T03 B’s lists, search, topology, impact, export, audit and review inbox contain nothing of A', async () => {
    // Positive controls: A sees its own data on the same paths.
    const searchA = await api<List<{ entity_id: string }>>(
      w.rq,
      w.a.token,
      'GET',
      `/search?q=${encodeURIComponent(w.ci1.name)}`,
    );
    expect(rows(searchA.body).map((h) => h.entity_id)).toContain(w.ci1.id);
    const auditA = await api<List<{ resource_id?: string }>>(
      w.rq,
      w.a.token,
      'GET',
      '/audit-log?limit=200',
    );
    expect(rows(auditA.body).map((e) => e.resource_id)).toContain(w.ci1.id);
    const exportA = await api(w.rq, w.a.token, 'GET', '/export/cis?format=json');
    expect(JSON.stringify(exportA.body)).toContain(w.ci1.id);
    const reviewA = await api<List<ReviewItem>>(w.rq, w.a.token, 'GET', '/review-items');
    expect(rows(reviewA.body).map((r) => r.id)).toContain(w.review.id);

    // B: CI list holds exactly B's own CI.
    const cisB = await api<List<CI>>(w.rq, w.b.token, 'GET', '/cis?limit=200');
    expect(cisB.status).toBe(200);
    expect(rows(cisB.body).map((c) => c.id)).toEqual([w.bOwn.id]);
    expect(cisB.body.total).toBe(1);

    for (const path of [
      `/cis?q=${encodeURIComponent(w.ci1.name)}`,
      `/cis?search=${encodeURIComponent(w.ci1.serial_number ?? '')}`,
      `/cis?status=decommissioned`,
      `/search?q=${encodeURIComponent(w.ci1.name)}`,
      `/search?q=${encodeURIComponent(w.ci1.serial_number ?? '')}`,
      `/search?q=${encodeURIComponent(w.site.name)}`,
      `/search?q=${encodeURIComponent(w.client.name)}`,
      '/topology',
      '/locations',
      '/locations/tree',
      '/clients',
      '/sites',
      '/relationships?limit=200',
      '/audit?limit=200',
      '/audit-log?limit=200',
      '/review-items?limit=200',
      '/review-items?status=open&limit=200',
      '/discovery/review-items?limit=200',
      '/exports',
      '/export/jobs',
      '/jobs?limit=200',
      '/export/cis?format=json',
      '/export/cis?format=csv',
      '/export/cis?format=datev',
    ]) {
      const r = await api(w.rq, w.b.token, 'GET', path);
      expect(r.status, `GET ${path} as B`).toBe(200);
      expectNoTraceOfA(`GET ${path} as B`, r.body);
    }
    // Impact of B's own CI never reaches A's graph.
    const impactB = await api(w.rq, w.b.token, 'GET', `/impact/${w.bOwn.id}`);
    expect(impactB.status).toBe(200);
    expectNoTraceOfA('GET /impact/{B} as B', impactB.body);

    // Asynchronous export (EXP-01): B's file holds B's CI only.
    const job = await api<{ id: string }>(w.rq, w.b.token, 'POST', '/exports', { format: 'json' });
    expect(job.status).toBe(202);
    let url = '';
    await expect
      .poll(
        async () => {
          const r = await api<{ status: string; download_url?: string }>(
            w.rq,
            w.b.token,
            'GET',
            `/exports/${job.body.id}`,
          );
          url = r.body.download_url ?? '';
          return r.body.status;
        },
        { timeout: 60_000, message: 'export job of B completes' },
      )
      .toBe('completed');
    const file = await w.rq.get(url);
    expect(file.status()).toBe(200);
    const content = await file.text();
    expect(content).toContain(w.bOwn.id);
    expectNoTraceOfA('export file of B', content);
  });

  test('T04 B’s GraphQL BFF reveals nothing of A', async () => {
    const gql = async (token: string, query: string, variables: Record<string, unknown> = {}) => {
      const res = await w.rq.post(`${BASE}/bff/graphql`, {
        headers: { Authorization: `Bearer ${token}` },
        data: { query, variables },
        failOnStatusCode: false,
      });
      return { status: res.status(), body: (await res.json()) as { data?: unknown } };
    };
    const ciQuery = 'query($id: ID!) { ci(id: $id) { id name serialNumber } }';
    // Positive control: A reads its CI.
    const own = await gql(w.a.token, ciQuery, { id: w.ci1.id });
    expect(JSON.stringify(own.body)).toContain(w.ci1.name);

    const one = await gql(w.b.token, ciQuery, { id: w.ci1.id });
    expect(one.status).toBe(200);
    expect(one.body.data).toEqual({ ci: null });
    for (const [label, query, variables] of [
      ['cis', '{ cis(first: 100) { totalCount nodes { id name } } }', {}],
      [
        'relationships',
        'query($id: ID!) { relationships(ciId: $id) { id sourceCiId targetCiId } }',
        { id: w.ci1.id },
      ],
      ['topology', '{ topology { nodes { id name } edges { id } } }', {}],
      [
        'topology(root)',
        'query($id: ID) { topology(rootCiId: $id) { nodes { id name } } }',
        { id: w.ci1.id },
      ],
      [
        'searchEverything',
        'query($q: String!) { searchEverything(q: $q) { total hits { id } } }',
        { q: w.ci1.name },
      ],
      ['reviewInbox', '{ reviewInbox { id } }', {}],
      ['jobs', '{ jobs { id } }', {}],
    ] as const) {
      const r = await gql(w.b.token, query, variables);
      expect(r.status, `BFF ${label} as B`).toBe(200);
      expectNoTraceOfA(`BFF ${label} as B`, r.body.data ?? null, [w.ci1.id]);
      expect(JSON.stringify(r.body.data ?? null), `BFF ${label} as B`).not.toContain(w.ci1.id);
    }
    const list = await gql(w.b.token, '{ cis(first: 100) { totalCount nodes { id } } }');
    expect(list.body.data).toEqual({ cis: { totalCount: 1, nodes: [{ id: w.bOwn.id }] } });
  });

  test('T05 B cannot read, change, delete, restore, retype or merge A’s CI by id (session and API key)', async () => {
    const before = await snapshotA();
    expect(before.ci1.status).toBe(200);
    const byKey = { 'X-API-Key': w.bApiKey };
    const attempts: [string, 'GET' | 'POST' | 'PATCH' | 'DELETE', string, unknown?][] = [
      ['detail', 'GET', `/cis/${w.ci1.id}`],
      ['detail+include', 'GET', `/cis/${w.ci1.id}?include=relationships,contacts,asset`],
      ['provenance', 'GET', `/cis/${w.ci1.id}/provenance`],
      ['update', 'PATCH', `/cis/${w.ci1.id}`, { name: 'owned-by-b', status: 'inactive' }],
      ['delete', 'DELETE', `/cis/${w.ci1.id}`],
      ['restore (active CI)', 'POST', `/cis/${w.ci1.id}/restore`],
      ['restore (A deleted)', 'POST', `/cis/${w.ciDel.id}/restore`],
      ['detail (A deleted)', 'GET', `/cis/${w.ciDel.id}`],
      ['change type', 'POST', `/cis/${w.ci1.id}/type`, { ci_type_id: w.serverType }],
      ['merge A into B', 'POST', `/cis/${w.bOwn.id}/merge`, { victim_id: w.ci1.id }],
      ['merge B into A', 'POST', `/cis/${w.ci1.id}/merge`, { victim_id: w.bOwn.id }],
      ['unmerge A', 'POST', `/cis/${w.ciM1.id}/unmerge`, {}],
      ['merged victim', 'GET', `/cis/${w.ciM2.id}`],
    ];
    for (const [label, method, path, data] of attempts) {
      const session = await api(w.rq, w.b.token, method, path, data);
      expect(NOT_FOUND, `${label}: ${method} ${path} as B (session) → ${session.status}`).toContain(
        session.status,
      );
      expectNoTraceOfA(`${label} as B`, session.body, [w.ci1.id, w.ciDel.id, w.ciM1.id, w.ciM2.id]);
      const key = await w.rq.fetch(`${BASE}/api/v1${path}`, {
        method,
        headers: byKey,
        data,
        failOnStatusCode: false,
      });
      expect(NOT_FOUND, `${label}: ${method} ${path} as B (API key) → ${key.status()}`).toContain(
        key.status(),
      );
    }
    // Bulk with A's id: the item fails with 404, nothing succeeds.
    const bulk = await api<{ succeeded: number; items: { id: string; code: number }[] }>(
      w.rq,
      w.b.token,
      'POST',
      '/cis/bulk',
      { action: 'set_status', ids: [w.ci1.id], status: 'inactive' },
    );
    if (bulk.status === 200) {
      expect(bulk.body.succeeded).toBe(0);
      expect(NOT_FOUND).toContain(bulk.body.items[0]?.code);
    } else {
      expect(NOT_FOUND).toContain(bulk.status);
    }

    // A afterwards: nothing changed; the deleted CI is still deleted, so A
    // can restore it itself (B's restore did not happen).
    expect(await snapshotA()).toEqual(before);
    const delA = await api(w.rq, w.a.token, 'GET', `/cis/${w.ciDel.id}`);
    expect(delA.status, 'A’s deleted CI stays deleted').toBe(404);
    const merged = await api(w.rq, w.a.token, 'GET', `/cis/${w.ciM1.id}`);
    expect(merged.status).toBe(200);
    const restore = await api<CI>(w.rq, w.a.token, 'POST', `/cis/${w.ciDel.id}/restore`);
    expect(restore.status, 'A restores its own CI').toBe(200);
    expect(restore.body.organization_id).toBe(w.a.orgId);
    await ok(w.rq, w.a.token, 'DELETE', `/cis/${w.ciDel.id}`);
  });

  test('T06 B reads no sub-resource, neighbours, impact or blast radius of A’s CI', async () => {
    for (const path of [
      `/cis/${w.ci1.id}/relationships`,
      `/cis/${w.ci1.id}/changes`,
      `/cis/${w.ci1.id}/contacts`,
      `/cis/${w.ci1.id}/interfaces`,
      `/cis/${w.ci1.id}/fields`,
      `/cis/${w.ci1.id}/blast-radius`,
      `/cis/${w.ci1.id}/dependencies`,
      `/topology/cis/${w.ci1.id}/neighbors`,
      `/topology/cis/${w.ci1.id}/impact`,
      `/impact/${w.ci1.id}`,
      `/impact?ci_ids=${w.ci1.id}`,
      `/impact?ci_ids=${w.bOwn.id},${w.ci1.id}`,
    ]) {
      const r = await api<List<unknown>>(w.rq, w.b.token, 'GET', path);
      // TEN-06: a foreign organization yields no row – either "not found"
      // or an empty collection, never a 2xx with content.
      if (r.status === 200) {
        expect(rows(r.body), `GET ${path} as B`).toEqual([]);
      } else {
        expect(NOT_FOUND, `GET ${path} as B → ${r.status}`).toContain(r.status);
      }
      expectNoTraceOfA(`GET ${path} as B`, r.body, [w.ci1.id]);
    }
  });

  test('T07 B cannot attach A’s location, client or parent CI to its own data', async () => {
    const before = await snapshotA();
    const refs: [string, 'POST' | 'PATCH', string, Record<string, unknown>][] = [
      ['CI in A’s site', 'POST', '/cis', { location_id: w.site.id }],
      ['CI of A’s client', 'POST', '/cis', { client_id: w.client.id }],
      ['CI under A’s CI', 'POST', '/cis', { parent_ci_id: w.ci1.id }],
      ['move B’s CI to A’s site', 'PATCH', `/cis/${w.bOwn.id}`, { location_id: w.site.id }],
      ['B’s CI to A’s client', 'PATCH', `/cis/${w.bOwn.id}`, { client_id: w.client.id }],
      ['B’s CI under A’s CI', 'PATCH', `/cis/${w.bOwn.id}`, { parent_ci_id: w.ci1.id }],
      [
        'building under A’s site',
        'POST',
        '/locations',
        { kind: 'building', name: `iso-b-b-${w.s}`, parent_id: w.site.id },
      ],
      [
        'site of A’s client',
        'POST',
        '/locations',
        { kind: 'site', name: `iso-b-s-${w.s}`, client_id: w.client.id },
      ],
    ];
    for (const [label, method, path, ref] of refs) {
      const data =
        method === 'POST' && path === '/cis'
          ? { ci_type_id: w.serverType, name: `iso-b-${label.length}-${w.s}`, ...ref }
          : ref;
      const r = await api(w.rq, w.b.token, method, path, data);
      expect([400, 403, 404, 422], `${label} → ${r.status} ${JSON.stringify(r.body)}`).toContain(
        r.status,
      );
      expectNoTraceOfA(label, r.body, [w.site.id, w.client.id, w.ci1.id]);
    }
    // B's data still holds B's CI only, without A's references.
    const cisB = await api<List<CI>>(w.rq, w.b.token, 'GET', '/cis?limit=200');
    expect(rows(cisB.body).map((c) => c.id)).toEqual([w.bOwn.id]);
    const own = await api<CI>(w.rq, w.b.token, 'GET', `/cis/${w.bOwn.id}`);
    expect(own.body.location_id ?? null).toBeNull();
    expect(own.body.client_id ?? null).toBeNull();
    expect(own.body.parent_ci_id ?? null).toBeNull();
    const locB = await api<List<Location>>(w.rq, w.b.token, 'GET', '/locations');
    expect(rows(locB.body)).toEqual([]);
    // A unchanged (no foreign child under A's site).
    expect(await snapshotA()).toEqual(before);
  });

  test('T08 B cannot read or resolve A’s review item, export job or job; the download URL cannot be bent to A’s file', async () => {
    for (const [method, path, data] of [
      ['GET', `/review-items/${w.review.id}`, undefined],
      ['POST', `/review-items/${w.review.id}/resolve`, { action: 'dismiss' }],
      ['POST', `/review-items/${w.review.id}/resolve`, { action: 'accept' }],
      ['POST', `/discovery/review-items/${w.review.id}/resolve`, { action: 'dismiss' }],
      ['GET', `/exports/${w.exportJobId}`, undefined],
      ['GET', `/export/jobs/${w.exportJobId}`, undefined],
      ['GET', `/jobs/${w.jobId}`, undefined],
      ['POST', `/jobs/${w.jobId}/cancel`, undefined],
    ] as const) {
      const r = await api(w.rq, w.b.token, method, path, data);
      expect(NOT_FOUND, `${method} ${path} as B → ${r.status}`).toContain(r.status);
      expectNoTraceOfA(`${method} ${path} as B`, r.body, [w.review.id, w.exportJobId, w.jobId]);
    }
    // A afterwards: review still open, export job readable.
    const item = await api<ReviewItem>(w.rq, w.a.token, 'GET', `/review-items/${w.review.id}`);
    expect(item.status).toBe(200);
    expect(item.body.status).toBe('open');
    const jobA = await api<{ status: string; download_url?: string }>(
      w.rq,
      w.a.token,
      'GET',
      `/exports/${w.exportJobId}`,
    );
    expect(jobA.status).toBe(200);

    // The presigned URL of B's export, pointed at A's object, is refused.
    const jobB = await api<{ id: string }>(w.rq, w.b.token, 'POST', '/exports', { format: 'csv' });
    expect(jobB.status).toBe(202);
    let urlB = '';
    await expect
      .poll(
        async () => {
          const r = await api<{ status: string; download_url?: string }>(
            w.rq,
            w.b.token,
            'GET',
            `/exports/${jobB.body.id}`,
          );
          urlB = r.body.download_url ?? '';
          return r.body.status;
        },
        { timeout: 60_000, message: 'export job of B completes' },
      )
      .toBe('completed');
    expect(urlB).toContain(`${w.b.orgId}/${jobB.body.id}`);
    const bent = urlB.replace(
      `${w.b.orgId}/${jobB.body.id}.csv`,
      `${w.a.orgId}/${w.exportJobId}.json`,
    );
    expect(bent).not.toBe(urlB);
    const res = await w.rq.get(bent, { failOnStatusCode: false });
    expect(res.status(), 'bent presigned URL').toBeGreaterThanOrEqual(400);
    // The object store may echo the requested key (A's org and job id, sent by B
    // itself); anything beyond that would be A's data.
    expectNoTraceOfA('bent presigned URL', await res.text(), [w.a.orgId, w.exportJobId]);
  });

  test('T09 organization headers and query parameters do not switch the tenant', async () => {
    const forged = {
      'X-Organization-ID': w.a.orgId,
      'X-Org-ID': w.a.orgId,
      'X-Tenant-ID': w.a.orgId,
      'X-Client-ID': w.client.id,
    };
    const org = await api<{ id: string }>(w.rq, w.b.token, 'GET', '/org', undefined, forged);
    expect(org.status).toBe(200);
    expect(org.body.id).toBe(w.b.orgId);
    const detail = await api(w.rq, w.b.token, 'GET', `/cis/${w.ci1.id}`, undefined, forged);
    expect(NOT_FOUND).toContain(detail.status);
    const patch = await api(w.rq, w.b.token, 'PATCH', `/cis/${w.ci1.id}`, { name: 'x' }, forged);
    expect(NOT_FOUND).toContain(patch.status);
    for (const path of [
      '/cis?limit=200',
      `/cis?organization_id=${w.a.orgId}`,
      `/cis?org_id=${w.a.orgId}`,
      `/cis?client_id=${w.client.id}`,
      `/cis?site_id=${w.site.id}`,
      `/cis?filter[client_id]=${w.client.id}`,
      `/topology?client_id=${w.client.id}`,
      `/topology?site_id=${w.site.id}`,
      `/export/cis?format=json&client_id=${w.client.id}`,
      `/search?q=${encodeURIComponent(w.ci1.name)}&organization_id=${w.a.orgId}`,
      `/audit-log?organization_id=${w.a.orgId}`,
      `/locations?parent_id=${w.site.id}`,
    ]) {
      const r = await api(w.rq, w.b.token, 'GET', path, undefined, forged);
      // 200 with B's data only, or a refusal of the unknown parameter.
      expect([200, 400, 422], `GET ${path} as B`).toContain(r.status);
      expectNoTraceOfA(`GET ${path} as B (forged headers)`, r.body, [
        w.a.orgId,
        w.client.id,
        w.site.id,
      ]);
      const own = JSON.stringify(r.body);
      expect(own.includes(`"organization_id":"${w.a.orgId}"`), `GET ${path}`).toBe(false);
    }
    const cis = await api<List<CI>>(w.rq, w.b.token, 'GET', '/cis?limit=200', undefined, forged);
    expect(rows(cis.body).map((c) => c.organization_id)).toEqual([w.b.orgId]);
    // A's CI is unchanged by the forged PATCH.
    const ci1 = await api<CI>(w.rq, w.a.token, 'GET', `/cis/${w.ci1.id}`);
    expect(ci1.body.name).toBe(w.ci1.name);
  });

  test('T10 B’s UI shows nothing of A: CI URL, CMDB list, command palette, locations, topology, audit, reviews', async () => {
    const pageA = w.a.page;
    const pageB = w.b.page;

    // Direct navigation to A's CI.
    const detailA = waitForApi(pageA, `/cis/${w.ci1.id}`);
    await pageA.goto(`/cmdb/${w.ci1.id}`);
    expect((await detailA).status()).toBe(200);
    await expect(pageA.getByRole('heading', { name: w.ci1.name })).toBeVisible();
    const detailB = waitForApi(pageB, `/cis/${w.ci1.id}`);
    await pageB.goto(`/cmdb/${w.ci1.id}`);
    expect(NOT_FOUND).toContain((await detailB).status());
    await expect(
      pageB.getByText('Das Configuration Item konnte nicht geladen werden'),
    ).toBeVisible();
    expectNoTraceOfA('B’s page /cmdb/{A}', await bodyText(pageB), [w.ci1.id]);

    // CMDB list.
    const listB = waitForApi(pageB, '/cis?');
    await pageB.goto('/cmdb');
    await listB;
    await expect(pageB.getByText(w.bOwn.name)).toBeVisible();
    expectNoTraceOfA('B’s CMDB list', await bodyText(pageB));

    // Command palette search (SRC-01): A finds its CI, B does not.
    const palette = async (page: Page, q: string) => {
      await page.keyboard.press('Control+k');
      const box = page.getByRole('combobox', { name: 'Befehle durchsuchen' });
      await expect(box).toBeVisible();
      const searched = page.waitForResponse(
        (r) => r.url().includes('/api/v1/search') && r.url().includes(encodeURIComponent(q)),
      );
      await box.fill(q);
      expect((await searched).status()).toBe(200);
      return page.getByRole('option', { name: new RegExp(`${q}`) });
    };
    await expect(await palette(pageA, w.ci1.name)).toHaveCount(1);
    await pageA.keyboard.press('Escape');
    const hitsB = await palette(pageB, w.ci1.name);
    await expect(hitsB).toHaveCount(0);
    expectNoTraceOfA('B’s command palette', await bodyText(pageB));
    await pageB.keyboard.press('Escape');

    // Locations, topology, audit, review inbox: A sees its marker, B not.
    // [route, API call of the page, heading, marker of A's data, B's own content]
    const pages: [string, string, string, RegExp, RegExp][] = [
      [
        '/locations',
        '/locations/tree',
        'Standorte & Lager',
        new RegExp(w.site.name),
        /Noch keine Standorte/,
      ],
      ['/topology', '/topology', 'Topologie', new RegExp(w.ci1.name), new RegExp(w.bOwn.name)],
      [
        '/audit',
        '/audit-log',
        'Audit-Protokoll',
        new RegExp(`${w.a.userId}|${w.a.account.email}`),
        new RegExp(`${w.b.userId}|${w.b.account.email}`),
      ],
      ['/reviews', '/review-items', 'Review-Inbox', /Konflikt mit Override/, /Keine Review-Items/],
    ];
    for (const [route, endpoint, heading, markerOfA, contentOfB] of pages) {
      const loadedA = waitForApi(pageA, endpoint);
      await pageA.goto(route);
      expect((await loadedA).status(), `${route} as A`).toBe(200);
      await expect(
        pageA.locator('#main-content').getByText(markerOfA).filter({ visible: true }).first(),
        `${route} as A shows A's data`,
      ).toBeVisible();
      const loadedB = waitForApi(pageB, endpoint);
      await pageB.goto(route);
      expect((await loadedB).status(), `${route} as B`).toBe(200);
      await expect(pageB.getByRole('heading', { name: heading })).toBeVisible();
      await expect(
        pageB.locator('#main-content').getByText(contentOfB).filter({ visible: true }).first(),
        `${route} as B has rendered B's own content`,
      ).toBeVisible();
      await expect(
        pageB.locator('#main-content').getByText(markerOfA).filter({ visible: true }),
      ).toHaveCount(0);
      expectNoTraceOfA(`B’s page ${route}`, await bodyText(pageB));
    }
  });

  test('T11 B’s relationship form rejects A’s CI id and stores nothing', async () => {
    const pageB = w.b.page;
    await pageB.goto(`/cmdb/${w.bOwn.id}`);
    await expect(pageB.getByRole('heading', { name: w.bOwn.name })).toBeVisible();
    await pageB.getByRole('button', { name: /Beziehung hinzufügen/ }).click();
    const dialog = pageB.getByRole('dialog');
    await dialog.getByLabel('CI-ID der Gegenstelle').fill(w.ci1.id);
    const sent = waitForApi(pageB, '/relationships', 'POST');
    await dialog.getByRole('button', { name: 'Anlegen' }).click();
    const res = await sent;
    expect(res.status(), 'relationship to A’s CI').toBeGreaterThanOrEqual(400);
    // The form stays open and reports the refusal.
    await expect(dialog).toBeVisible();
    await expect(
      dialog.getByRole('alert').or(dialog.locator('[aria-invalid="true"]')).first(),
    ).toBeVisible();
    expectNoTraceOfA('B’s relationship form', await bodyText(pageB), [w.ci1.id]);
    await dialog.getByRole('button', { name: 'Abbrechen' }).click();
    await expect(pageB.getByText('Keine Beziehungen vorhanden')).toBeVisible();
    const relsB = await api<List<Relationship>>(
      w.rq,
      w.b.token,
      'GET',
      `/cis/${w.bOwn.id}/relationships`,
    );
    expect(rows(relsB.body)).toEqual([]);
    const relsA = await api<List<Relationship>>(
      w.rq,
      w.a.token,
      'GET',
      `/cis/${w.ci1.id}/relationships`,
    );
    expect(rows(relsA.body).map((r) => r.source_ci_id)).not.toContain(w.bOwn.id);
    expect(rows(relsA.body).map((r) => r.target_ci_id)).not.toContain(w.bOwn.id);
  });

  test('T12 A’s audit trail records none of B’s attempts and its hash chain is intact', async () => {
    // B tries once more (so this test does not depend on the order of the others).
    await api(w.rq, w.b.token, 'PATCH', `/cis/${w.ci1.id}`, { name: 'owned-by-b' });
    await api(w.rq, w.b.token, 'DELETE', `/cis/${w.ci1.id}`);
    const audit = await api<List<{ actor_id?: string; organization_id: string }>>(
      w.rq,
      w.a.token,
      'GET',
      '/audit-log?limit=200',
    );
    expect(audit.status).toBe(200);
    expect(rows(audit.body).length).toBeGreaterThan(0);
    expect(rows(audit.body).filter((e) => e.actor_id === w.b.userId)).toEqual([]);
    expect(rows(audit.body).filter((e) => e.organization_id !== w.a.orgId)).toEqual([]);
    const verify = await api<{ intact: boolean }>(w.rq, w.a.token, 'POST', '/audit-log/verify');
    expect(verify.status).toBe(200);
    expect(verify.body.intact).toBe(true);
    const ci1 = await api<CI>(w.rq, w.a.token, 'GET', `/cis/${w.ci1.id}`);
    expect(ci1.status).toBe(200);
    expect(ci1.body.name).toBe(w.ci1.name);
  });

  test('T13 B can neither read nor change A’s contact and shared saved view', async () => {
    const contactName = `Iso Contact A ${w.s}`;
    const contact = await api<{ id: string }>(w.rq, w.a.token, 'POST', '/contacts', {
      display_name: contactName,
      email: `iso-contact-${w.s}@audit-e2e.de`,
      phone: '+49 30 1234567',
      role: 'Admin',
    });
    expect(contact.status, `A creates a contact: ${JSON.stringify(contact.body)}`).toBe(201);
    const viewName = `Iso View A ${w.s}`;
    const view = await api<{ id: string }>(w.rq, w.a.token, 'POST', '/saved-views', {
      name: viewName,
      entity_kind: 'ci',
      filter_spec: { q: w.ci1.name },
      shared: true,
    });
    expect(view.status, `A creates a shared view: ${JSON.stringify(view.body)}`).toBe(201);
    // Positive control: A reads both.
    expect((await api(w.rq, w.a.token, 'GET', `/contacts/${contact.body.id}`)).status).toBe(200);
    expect((await api(w.rq, w.a.token, 'GET', `/saved-views/${view.body.id}`)).status).toBe(200);

    const secrets = [contactName, `iso-contact-${w.s}`, viewName, contact.body.id, view.body.id];
    for (const [method, path, data] of [
      ['GET', `/contacts/${contact.body.id}`, undefined],
      ['PATCH', `/contacts/${contact.body.id}`, { display_name: 'owned-by-b' }],
      ['DELETE', `/contacts/${contact.body.id}`, undefined],
      ['GET', `/saved-views/${view.body.id}`, undefined],
      ['PATCH', `/saved-views/${view.body.id}`, { name: 'owned-by-b' }],
      ['DELETE', `/saved-views/${view.body.id}`, undefined],
    ] as const) {
      const r = await api(w.rq, w.b.token, method, path, data);
      expect(NOT_FOUND, `${method} ${path} as B → ${r.status}`).toContain(r.status);
      const text = JSON.stringify(r.body);
      expect(secrets.filter((m) => m !== path.split('/')[2] && text.includes(m))).toEqual([]);
    }
    for (const path of [
      '/contacts?limit=200',
      '/saved-views',
      `/search?q=${encodeURIComponent(contactName)}`,
    ]) {
      const r = await api(w.rq, w.b.token, 'GET', path);
      expect(r.status, `GET ${path} as B`).toBe(200);
      const text = JSON.stringify(r.body);
      expect(
        secrets.filter((m) => text.includes(m)),
        `GET ${path} as B`,
      ).toEqual([]);
    }
    // A afterwards: both unchanged.
    const c = await api<{ display_name?: string; name?: string }>(
      w.rq,
      w.a.token,
      'GET',
      `/contacts/${contact.body.id}`,
    );
    expect(c.status).toBe(200);
    expect(c.body.display_name ?? c.body.name).toBe(contactName);
    const v = await api<{ name: string }>(w.rq, w.a.token, 'GET', `/saved-views/${view.body.id}`);
    expect(v.status).toBe(200);
    expect(v.body.name).toBe(viewName);
  });

  test('T14 B’s relationship to A’s CI is rejected with a client error and nothing is stored', async () => {
    const before = await snapshotA();
    const attempts = [
      { source_ci_id: w.bOwn.id, target_ci_id: w.ci1.id },
      { source_ci_id: w.ci1.id, target_ci_id: w.bOwn.id },
      { source_ci_id: w.ci1.id, target_ci_id: w.ci2.id },
    ];
    const statuses: string[] = [];
    for (const ends of attempts) {
      const r = await api(w.rq, w.b.token, 'POST', '/relationships', {
        ...ends,
        rel_type: 'depends_on',
        source: 'manual',
      });
      statuses.push(`${ends.source_ci_id}->${ends.target_ci_id}: ${r.status}`);
      expect(r.status, `relationship ${JSON.stringify(ends)} by B`).not.toBeLessThan(300);
      expectNoTraceOfA('rejection', r.body, [w.ci1.id, w.ci2.id]);
    }
    // Nothing persisted: not for B, not in A's graph.
    const relsB = await api<List<Relationship>>(
      w.rq,
      w.b.token,
      'GET',
      `/cis/${w.bOwn.id}/relationships`,
    );
    expect(rows(relsB.body)).toEqual([]);
    expect(await snapshotA()).toEqual(before);
    // The rejection is a client error naming the unknown reference
    // (API-03: RFC 7807 table – 404/422), not an internal error.
    for (const s of statuses) {
      expect(s, 'status of the rejection').toMatch(/: (400|403|404|422)$/);
    }
  });

  test('T15 the topology rooted at A’s CI answers B with "not found", like neighbours and impact', async () => {
    const neighbours = await api(w.rq, w.b.token, 'GET', `/topology/cis/${w.ci1.id}/neighbors`);
    expect(NOT_FOUND).toContain(neighbours.status);
    const rooted = await api(w.rq, w.b.token, 'GET', `/topology?root_ci_id=${w.ci1.id}`);
    expectNoTraceOfA('GET /topology?root_ci_id={A} as B', rooted.body, [w.ci1.id]);
    expect(NOT_FOUND, `GET /topology?root_ci_id={A} as B → ${rooted.status}`).toContain(
      rooted.status,
    );
  });

  test('T16 A creates a CI through the CMDB form; it is stored in A’s organization and invisible to B', async () => {
    const page = w.a.page;
    const name = `iso-a-form-${w.s}`;
    await page.goto('/cmdb');
    await page.getByRole('button', { name: 'CI erstellen' }).first().click();
    const dialog = page.getByRole('dialog');
    await dialog.getByLabel('Name', { exact: true }).fill(name);
    await dialog.getByLabel('CI-Typ').selectOption({ label: 'Server' });
    const created = waitForApi(page, '/cis', 'POST');
    await dialog.getByRole('button', { name: 'Anlegen' }).click();
    const res = await created;
    expect(res.status(), `POST /cis sent by the CMDB form: ${await res.text()}`).toBe(201);
    const ci = (await res.json()) as CI;
    await expect(dialog).toBeHidden();
    await expect(page.locator('#main-content').getByText(name, { exact: true })).toBeVisible();

    // UI → API → DB: stored in A's organization.
    const asA = await api<CI>(w.rq, w.a.token, 'GET', `/cis/${ci.id}`);
    expect(asA.status).toBe(200);
    expect(asA.body.organization_id).toBe(w.a.orgId);
    // ... and invisible for B.
    const asB = await api(w.rq, w.b.token, 'GET', `/cis/${ci.id}`);
    expect(NOT_FOUND).toContain(asB.status);
    const listB = await api<List<CI>>(w.rq, w.b.token, 'GET', `/cis?q=${name}`);
    expect(rows(listB.body).map((c) => c.id)).not.toContain(ci.id);
  });

  test('T17 A’s audit viewer shows when and on which object each of A’s entries happened', async () => {
    // AUD-07: every change stores who, when and what; the audit viewer
    // (UI-01..10) shows A's own entries with their time and object.
    const page = w.a.page;
    const listed = page.waitForResponse(
      (r) => r.url().includes('/api/v1/audit-log') && r.request().method() === 'GET' && r.ok(),
    );
    await page.goto('/audit');
    const entries = (
      (await (await listed).json()) as {
        data: { action: string; timestamp?: string; resource_id?: string }[];
      }
    ).data;
    expect(entries.length, 'A has audit entries').toBeGreaterThan(0);
    const tableRows = page.locator('#main-content table tbody tr');
    await expect(tableRows).toHaveCount(entries.length);
    for (let i = 0; i < Math.min(entries.length, 5); i++) {
      const entry = entries[i];
      const [time, , action, object] = await tableRows.nth(i).locator('td').allInnerTexts();
      expect(action, `row ${i}`).toBe(entry.action);
      expect(entry.timestamp, `API time of ${entry.action}`).toBeTruthy();
      expect(time, `time of ${entry.action} (API ${entry.timestamp})`).not.toMatch(/invalid/i);
      expect(time, `time of ${entry.action} (API ${entry.timestamp})`).toMatch(
        /\d{1,2}\.\d{1,2}\.\d{4}/,
      );
      if (entry.resource_id) {
        expect(object, `object of ${entry.action}`).toContain(entry.resource_id);
      }
    }
  });
});
