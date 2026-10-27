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
 * Discovery scopes maintained in the UI on the real stack (WP-496, F-107, F-267), nothing
 * mocked: UI → REST API → PostgreSQL → API.
 *
 * Before WP-496 the scope form of the Discovery page knew only collector, kind, target and
 * schedule: site, VRF, credentials, plugins and name could be set through the raw API only,
 * and a scope was deleted without a confirmation.
 *
 * One organization is created through the self-signup UI. Its administrator creates two
 * clients A and B, two sites of A, a VRF, an organization-wide credential and one of A, a
 * credential of B, a collector of A and one of B with a scope of B, and invites a user as
 * viewer of client B; after the acceptance the user gets a custom role holding only
 * discovery:read in the scope of client B instead (an invitation carries standard roles only,
 * RBA-02).
 *
 * Test → requirement IDs proven:
 *  D1 the administrator creates a scope of collector A in the scope form with name, site,
 *     VRF, plugins and both credentials; GET /discovery-scopes/{id} returns exactly these
 *     fields, the client of the site, and the credential of B is not offered .. DIS-09 (F-107)
 *  D2 editing the scope in the form changes name, site, schedule (cron), plugins and
 *     credentials and removes the VRF; GET returns the new values ............. DIS-09 (F-107)
 *  D3 disabling the scope in the list; GET returns enabled false ............. DIS-09 (F-107)
 *  D4 the principal of client B reads its own scope (200) but gets 404 for the scope of
 *     client A, which its list does not contain ........................ DIS-09, TEN-05 (F-107)
 *  D5 deleting asks for a confirmation: cancel keeps the scope, the confirmation deletes it;
 *     GET answers 404 ................................................. DIS-09, DIS-08 (F-267)
 *
 * A failing test is a product finding: the assertions encode DIS-09 and DIS-08 in
 * docs/spec/katalog-v3 and the acceptance criteria of WP-496.
 */

interface List<T> {
  data: T[] | null;
  total: number;
}

interface Scope {
  id: string;
  client_id?: string;
  site_id?: string;
  collector_id: string;
  name: string;
  kind: string;
  target: string;
  vrf_id?: string;
  schedule: string;
  credential_ids: string[];
  plugins: string[];
  enabled: boolean;
}

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
  siteA1: Named;
  siteA2: Named;
  vrf: Named;
  credOrg: Named;
  credA: Named;
  credB: Named;
  collectorA: Named;
  scopeB: Scope;
  foreign: { email: string; password: string };
  scopeId: string;
  scopeName: string;
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

/** stored reads the scope through the API, as the database holds it. */
async function stored(id: string): Promise<Scope> {
  const res = await api<Scope>(w.rq, w.token, 'GET', `/discovery-scopes/${id}`);
  expect(res.status, `GET /discovery-scopes/${id}: ${JSON.stringify(res.body)}`).toBe(200);
  return res.body;
}

function scopeWrite(page: Page, method: string, id = ''): Promise<Response> {
  const path = id ? `/api/v1/discovery-scopes/${id}` : '/api/v1/discovery-scopes';
  return page.waitForResponse(
    (r) => new URL(r.url()).pathname === path && r.request().method() === method,
  );
}

/** collectorCard is the card of collector A on the Discovery page. */
function collectorCard(page: Page) {
  return page
    .locator('section')
    .filter({ has: page.getByRole('heading', { name: w.collectorA.name, exact: true }) });
}

async function openDiscovery(page: Page) {
  await page.goto('/discovery');
  await expect(
    page.locator('#main-content').getByRole('heading', { name: 'Discovery', exact: true }),
  ).toBeVisible();
  await expect(collectorCard(page)).toBeVisible();
}

test.describe.configure({ mode: 'serial' });

test.describe('Discovery scopes: the scope form sets every field of DIS-09', () => {
  test.beforeAll(async ({ browser, playwright }) => {
    test.setTimeout(4 * 60_000);
    const rq = await playwright.request.newContext();
    const ctx = await browser.newContext({
      baseURL: BASE,
      locale: 'de-DE',
      extraHTTPHeaders: { 'X-Real-IP': realIp() },
    });
    const page = await ctx.newPage();
    const account = await signupTenant(page, rq, 'scopes');
    const token = await uiLogin(page, account);
    const s = stamp();
    w = { rq, ctx, page, token, s } as World;

    w.clientA = await ok<Named>('POST', '/clients', {
      name: `Scope Client A ${s}`,
      slug: `scope-client-a-${s}`,
    });
    w.clientB = await ok<Named>('POST', '/clients', {
      name: `Scope Client B ${s}`,
      slug: `scope-client-b-${s}`,
    });
    w.siteA1 = await ok<Named>('POST', '/sites', {
      name: `Werk Nord ${s}`,
      client_id: w.clientA.id,
    });
    w.siteA2 = await ok<Named>('POST', '/sites', {
      name: `Werk Süd ${s}`,
      client_id: w.clientA.id,
    });
    w.vrf = await ok<Named>('POST', '/vrfs', { name: `kunde-a-${s}` });
    const credential = (name: string, clientId?: string) =>
      ok<Named>('POST', '/credentials', {
        name,
        kind: 'snmp_v2c',
        ...(clientId ? { client_id: clientId } : {}),
        secret: { community: `c-${name}` },
      });
    w.credOrg = await credential(`snmp-org-${s}`);
    w.credA = await credential(`snmp-a-${s}`, w.clientA.id);
    w.credB = await credential(`snmp-b-${s}`, w.clientB.id);
    w.collectorA = await ok<Named>('POST', '/collectors', {
      name: `collector-a-${s}`,
      client_id: w.clientA.id,
    });
    const collectorB = await ok<Named>('POST', '/collectors', {
      name: `collector-b-${s}`,
      client_id: w.clientB.id,
    });
    w.scopeB = await ok<Scope>('POST', '/discovery-scopes', {
      collector_id: collectorB.id,
      kind: 'cidr',
      target: '10.98.0.0/24',
      enabled: false,
    });

    const role = await ok<{ id: string }>('POST', '/roles', {
      name: `discovery-read-${s}`,
      permissions: ['discovery:read'],
    });
    const roles = await ok<List<{ id: string; name: string }>>('GET', '/roles?limit=100');
    const viewerRole = (roles.data ?? []).find((r) => r.name === 'viewer')?.id ?? '';
    expect(viewerRole, 'system role viewer (RBA-02)').not.toBe('');
    // An invitation carries a standard role only (RBA-02): the user is invited as viewer of
    // client B, gets the custom role with discovery:read in the scope of client B after the
    // acceptance, and the viewer assignment is removed. He holds exactly discovery:read for
    // client B, as D4 requires.
    w.foreign = { email: `scopes-b-${s}@audit-e2e.de`, password: `Audit-b-${s}!aA` };
    const invitation = await ok<{ token?: string }>('POST', '/invitations', {
      email: w.foreign.email,
      role_id: viewerRole,
      scope_type: 'client',
      scope_id: w.clientB.id,
    });
    expect(invitation.token, 'the token is returned once on issue').toBeTruthy();
    const accept = await rq.post(`${BASE}/api/v1/invitations/accept`, {
      data: { token: invitation.token, password: w.foreign.password, display_name: 'Client B' },
      failOnStatusCode: false,
    });
    expect(accept.status(), `accept the invitation: ${await accept.text()}`).toBe(201);
    const foreignID = ((await accept.json()) as { user_id?: string }).user_id ?? '';
    expect(foreignID, 'the accepted user').not.toBe('');
    await ok('POST', '/role-assignments', {
      user_id: foreignID,
      custom_role_id: role.id,
      scope_type: 'client',
      scope_id: w.clientB.id,
    });
    const assignments = await ok<List<{ id: string; role_id?: string }>>(
      'GET',
      `/role-assignments?user_id=${foreignID}`,
    );
    const standard = (assignments.data ?? []).filter((a) => a.role_id === viewerRole);
    expect(standard, 'the viewer assignment of the invitation').toHaveLength(1);
    await ok('DELETE', `/role-assignments/${standard[0].id}`);
  });

  test.afterAll(async () => {
    await w?.ctx.close();
    await w?.rq.dispose();
  });

  test('D1 the scope form creates a scope with name, site, VRF, plugins and credentials', async () => {
    const page = w.page;
    await openDiscovery(page);
    await collectorCard(page).getByRole('button', { name: 'Scope anlegen' }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toBeVisible();
    await expect(
      dialog.getByText('Ohne Auswahl gelten die Credentials des Clients und der Organisation.'),
    ).toBeVisible();
    await expect(dialog.getByText(/Cron in der Zeitzone der Organisation/)).toBeVisible();

    w.scopeName = `Lager ${w.s}`;
    await dialog.getByLabel('Name', { exact: true }).fill(w.scopeName);
    await dialog.getByLabel('Ziel', { exact: true }).fill('10.97.0.0/24');
    await dialog.getByLabel('Site', { exact: true }).selectOption({ label: w.siteA1.name });
    await dialog.getByLabel('VRF', { exact: true }).selectOption({ label: w.vrf.name });
    await dialog.getByLabel('SSH', { exact: true }).check();
    await dialog.getByLabel(new RegExp(w.credOrg.name)).check();
    await dialog.getByLabel(new RegExp(w.credA.name)).check();
    // Only organization-wide credentials and those of the scope's client.
    await expect(dialog.getByLabel(new RegExp(w.credB.name))).toHaveCount(0);

    const created = scopeWrite(page, 'POST');
    await dialog.getByRole('button', { name: 'Speichern' }).click();
    const res = await created;
    expect(res.status(), `POST /discovery-scopes: ${await res.text()}`).toBe(201);
    w.scopeId = ((await res.json()) as Scope).id;
    await expect(dialog).toBeHidden();
    await expect(page.getByText(w.scopeName)).toBeVisible();

    const scope = await stored(w.scopeId);
    expect(scope).toMatchObject({
      collector_id: w.collectorA.id,
      client_id: w.clientA.id,
      site_id: w.siteA1.id,
      vrf_id: w.vrf.id,
      name: w.scopeName,
      kind: 'cidr',
      target: '10.97.0.0/24',
      schedule: '24h',
      enabled: true,
    });
    expect([...scope.credential_ids].sort()).toEqual([w.credOrg.id, w.credA.id].sort());
    expect([...scope.plugins].sort()).toEqual(['snmp', 'ssh', 'sweep']);
  });

  test('D2 editing the scope changes site, schedule, plugins and credentials and removes the VRF', async () => {
    const page = w.page;
    await openDiscovery(page);
    await page.getByRole('button', { name: `Scope ${w.scopeName} bearbeiten` }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByLabel('Name', { exact: true })).toHaveValue(w.scopeName);
    await expect(dialog.getByLabel(new RegExp(w.credA.name))).toBeChecked();

    w.scopeName = `Lager Ost ${w.s}`;
    await dialog.getByLabel('Name', { exact: true }).fill(w.scopeName);
    await dialog.getByLabel('Site', { exact: true }).selectOption({ label: w.siteA2.name });
    await dialog.getByLabel('VRF', { exact: true }).selectOption({ value: '' });
    await dialog.getByLabel(/Zeitplan/).fill('15 3 * * *');
    await dialog.getByLabel('Redfish', { exact: true }).check();
    await dialog.getByLabel('SSH', { exact: true }).uncheck();
    await dialog.getByLabel(new RegExp(w.credOrg.name)).uncheck();

    const changed = scopeWrite(page, 'PATCH', w.scopeId);
    await dialog.getByRole('button', { name: 'Speichern' }).click();
    const res = await changed;
    expect(res.status(), `PATCH /discovery-scopes/${w.scopeId}: ${await res.text()}`).toBe(200);
    await expect(dialog).toBeHidden();

    const scope = await stored(w.scopeId);
    expect(scope).toMatchObject({
      client_id: w.clientA.id,
      site_id: w.siteA2.id,
      name: w.scopeName,
      target: '10.97.0.0/24',
      schedule: '15 3 * * *',
      credential_ids: [w.credA.id],
      enabled: true,
    });
    expect(scope.vrf_id ?? '', 'the VRF is removed').toBe('');
    expect([...scope.plugins].sort()).toEqual(['redfish', 'snmp', 'sweep']);
  });

  test('D3 disabling the scope in the list stores enabled false', async () => {
    const page = w.page;
    await openDiscovery(page);
    const changed = scopeWrite(page, 'PATCH', w.scopeId);
    await page.getByRole('button', { name: `Scope ${w.scopeName} deaktivieren` }).click();
    expect((await changed).status(), 'PATCH enabled false').toBe(200);
    await expect(
      page.getByRole('button', { name: `Scope ${w.scopeName} aktivieren` }),
    ).toBeVisible();

    const scope = await stored(w.scopeId);
    expect(scope).toMatchObject({ enabled: false, name: w.scopeName, site_id: w.siteA2.id });
    expect(scope.credential_ids).toEqual([w.credA.id]);
  });

  test('D4 a principal of another client gets 404 for the scope', async ({ browser }) => {
    const ctx = await browser.newContext({ baseURL: BASE, locale: 'de-DE' });
    try {
      const page = await ctx.newPage();
      const token = await uiLogin(page, w.foreign);
      const own = await api<Scope>(w.rq, token, 'GET', `/discovery-scopes/${w.scopeB.id}`);
      expect(own.status, `own scope of client B: ${JSON.stringify(own.body)}`).toBe(200);
      const foreign = await api(w.rq, token, 'GET', `/discovery-scopes/${w.scopeId}`);
      expect(foreign.status, `scope of client A: ${JSON.stringify(foreign.body)}`).toBe(404);
      const list = await api<List<Scope>>(w.rq, token, 'GET', '/discovery-scopes?limit=200');
      expect(list.status).toBe(200);
      const ids = (list.body.data ?? []).map((sc) => sc.id);
      expect(ids).toContain(w.scopeB.id);
      expect(ids).not.toContain(w.scopeId);
    } finally {
      await ctx.close();
    }
  });

  test('D5 deleting asks for a confirmation and then deletes the scope', async () => {
    const page = w.page;
    await openDiscovery(page);
    const deleteButton = page.getByRole('button', { name: `Scope ${w.scopeName} löschen` });
    let deletes = 0;
    const count = (r: Response) => {
      if (
        r.request().method() === 'DELETE' &&
        new URL(r.url()).pathname === `/api/v1/discovery-scopes/${w.scopeId}`
      ) {
        deletes++;
      }
    };
    page.on('response', count);
    try {
      await deleteButton.click();
      const dialog = page.getByRole('dialog', { name: 'Scope löschen?' });
      await expect(dialog).toContainText(w.scopeName);
      await dialog.getByRole('button', { name: 'Abbrechen' }).click();
      await expect(dialog).toBeHidden();
      expect(deletes, 'no DELETE without the confirmation').toBe(0);
      expect((await stored(w.scopeId)).id).toBe(w.scopeId);

      await deleteButton.click();
      const removed = scopeWrite(page, 'DELETE', w.scopeId);
      await page
        .getByRole('dialog', { name: 'Scope löschen?' })
        .getByRole('button', { name: 'Endgültig löschen' })
        .click();
      expect((await removed).status(), 'DELETE /discovery-scopes/{id}').toBe(204);
      await expect(page.getByText(w.scopeName)).toHaveCount(0);
      expect(deletes).toBe(1);
    } finally {
      page.off('response', count);
    }

    const gone = await api(w.rq, w.token, 'GET', `/discovery-scopes/${w.scopeId}`);
    expect(gone.status, 'GET after the delete').toBe(404);
  });
});
