import { randomUUID } from 'node:crypto';
import {
  test,
  expect,
  type APIRequestContext,
  type Browser,
  type BrowserContext,
  type Page,
} from '@playwright/test';
import { api, signupTenant, stamp, uiLogin, BASE } from './support/stack';

/*
 * Review inbox on the real stack (WP-557, UI-16; open proof of WP-196): merge and new
 * through the UI, a second resolution of the same item and a user without review:resolve.
 * Nothing is mocked: the discovery records go through the ingest of the stack
 * (POST /api/v1/ingest/bulk → ingest.> → reconciliation), the resolutions through the
 * review inbox UI (POST /api/v1/review-items/{id}/resolve), the result is read back
 * through the API.
 *
 * One organization is created through the self-signup UI. Discovery reports two servers
 * A and B with one MAC each, then a device C whose MACs are those of A and B (REC-02 step
 * 3: several CIs share the MAC → ambiguous_identity with A and B as candidates), and
 * devices of a CI type the organization does not know (unclassified_device, reason
 * unknown_ci_type).
 *
 * Test → requirement IDs proven:
 *  R1 ambiguous_identity with two candidates: the candidate selection names the CIs
 *     (F-346), merge into the chosen candidate B in the UI → the item is resolved, B
 *     carries the management IP of C, no CI was created ................ UI-16, REC-08
 *  R2 unclassified_device: "Neu anlegen" with the type chosen in the UI → the CI exists
 *     with the chosen type .................................................. UI-16, REC-08
 *  R3 a second resolution of the same item (resolved in between through the API) shows
 *     the 409 of the server in the UI; the first resolution stays ........ UI-16, REC-08
 *  R4 a viewer (discovery:read, no review:resolve) sees the item but no action; a direct
 *     POST of the resolution answers 403, the item stays open ...... UI-16, RBA-01, REC-08
 *
 * A failing test is a product finding: the assertions encode UI-16/REC-08 in
 * docs/spec/katalog-v3 and the acceptance criteria of WP-557.
 */

interface List<T> {
  data: T[] | null;
  total: number;
}

interface CI {
  id: string;
  name: string;
  ci_type_id: string;
  management_ip?: string;
  serial_number?: string;
}

interface ReviewItem {
  id: string;
  kind: string;
  status: 'open' | 'resolved' | 'dismissed';
  payload: Record<string, unknown>;
  candidate_ci_ids?: string[];
  action?: string;
}

/** A device as discovery reports it (TEST-NET-1 addresses, universally administered MACs). */
interface Device {
  name: string;
  ip: string;
  serial: string;
  mac: string;
  type: string;
  /** Further MACs of the device (interfaces). */
  ifMacs?: string[];
}

interface World {
  rq: APIRequestContext;
  ctx: BrowserContext;
  page: Page;
  token: string;
  serverType: string;
  a: CI;
  b: CI;
  c: Device;
  created: Device;
  twice: Device;
  viewerItem: Device;
  viewer: { email: string; password: string };
}

let w: World;

/** A documentation address (198.18.0.0/15) so the signup has its own budget (E-33). */
function realIp(): string {
  return `198.18.${Math.floor(Math.random() * 250) + 1}.${Math.floor(Math.random() * 250) + 1}`;
}

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

/** record is a DeviceRecord (api/devicerecord.schema.json) of the device observed now. */
function record(device: Device) {
  return {
    record_id: randomUUID(),
    observed_at: new Date().toISOString(),
    source: 'snmp',
    target_ip: device.ip,
    identity: { serial_number: device.serial, primary_mac: device.mac, hostname: device.name },
    classification: { ci_type: device.type },
    system: { name: device.name },
    ...(device.ifMacs
      ? { interfaces: device.ifMacs.map((mac, i) => ({ name: `eth${i + 1}`, mac })) }
      : {}),
  };
}

async function discover(devices: Device[]) {
  const res = await api(w.rq, w.token, 'POST', '/ingest/bulk', { records: devices.map(record) });
  expect(res.status, `POST /ingest/bulk: ${JSON.stringify(res.body)}`).toBe(202);
}

async function cis(): Promise<CI[]> {
  return (await ok<List<CI>>('GET', '/cis?limit=200')).data ?? [];
}

async function ciByName(name: string): Promise<CI | undefined> {
  return (await cis()).find((ci) => ci.name === name);
}

/** itemOf returns the review item of a device by kind and reported name. */
async function itemOf(kind: string, name: string): Promise<ReviewItem | undefined> {
  const list = await ok<List<ReviewItem>>('GET', `/review-items?kind=${kind}&limit=200`);
  return (list.data ?? []).find((item) => item.payload?.name === name);
}

async function waitForItem(kind: string, name: string): Promise<ReviewItem> {
  await expect
    .poll(async () => (await itemOf(kind, name))?.status ?? '', {
      message: `${kind} item of ${name}`,
      timeout: 120_000,
      intervals: [2_000],
    })
    .toBe('open');
  return (await itemOf(kind, name))!;
}

/** openItem shows the item of a device in the review inbox, filtered by its kind. */
async function openItem(page: Page, kind: string, name: string) {
  await page.goto('/reviews');
  const main = page.locator('#main-content');
  await expect(main.getByRole('heading', { name: 'Review-Inbox' })).toBeVisible();
  await main.getByLabel('Typ', { exact: true }).selectOption(kind);
  await main.getByRole('button', { name: new RegExp(name) }).click();
  await expect(main.getByRole('table', { name: 'Vergleich von Beobachtung und CI' })).toBeVisible();
  return main;
}

function waitForResolve(page: Page, id: string) {
  return page.waitForResponse(
    (r) =>
      new URL(r.url()).pathname === `/api/v1/review-items/${id}/resolve` &&
      r.request().method() === 'POST',
  );
}

/** invite issues an organization-wide invitation with roleId and accepts it. */
async function invite(roleId: string, label: string) {
  const s = stamp();
  const user = { email: `ri-${label}-${s}@audit-e2e.de`, password: `Audit-${label}-${s}!aA` };
  const invitation = await ok<{ token?: string }>('POST', '/invitations', {
    email: user.email,
    role_id: roleId,
    scope_type: 'org',
  });
  expect(invitation.token, 'the token is returned once on issue').toBeTruthy();
  const accept = await w.rq.post(`${BASE}/api/v1/invitations/accept`, {
    data: { token: invitation.token, password: user.password, display_name: `Review ${label}` },
    failOnStatusCode: false,
  });
  expect(accept.status(), `accept the invitation: ${await accept.text()}`).toBe(201);
  return user;
}

async function signIn(browser: Browser, user: { email: string; password: string }) {
  const ctx = await browser.newContext({
    baseURL: BASE,
    locale: 'de-DE',
    extraHTTPHeaders: { 'X-Real-IP': realIp() },
  });
  const page = await ctx.newPage();
  const token = await uiLogin(page, user);
  return { ctx, page, token };
}

test.describe.serial('Review inbox: merge, new, second resolution and missing right', () => {
  test.beforeAll(async ({ browser, playwright }) => {
    test.setTimeout(6 * 60_000);
    const rq = await playwright.request.newContext();
    const ctx = await browser.newContext({
      baseURL: BASE,
      locale: 'de-DE',
      extraHTTPHeaders: { 'X-Real-IP': realIp() },
    });
    const page = await ctx.newPage();
    const account = await signupTenant(page, rq, 'reviewinbox');
    const token = await uiLogin(page, account);
    const s = stamp();
    // The last three bytes of the MACs differ per run (Intel OUI 00:1b:21).
    const hex = (n: number) => (n % 256).toString(16).padStart(2, '0');
    const r = Math.floor(Math.random() * 65_536);
    const mac = (n: number) => `00:1b:21:${hex(r >> 8)}:${hex(r)}:${hex(n)}`;
    const unknownType = `pv-review-gadget-${s}`;
    const deviceA: Device = {
      name: `ri-a-${s}`,
      ip: '192.0.2.31',
      serial: `RI-A-${s}`,
      mac: mac(1),
      type: 'server',
    };
    const deviceB: Device = {
      name: `ri-b-${s}`,
      ip: '192.0.2.32',
      serial: `RI-B-${s}`,
      mac: mac(2),
      type: 'server',
    };
    w = {
      rq,
      ctx,
      page,
      token,
      serverType: '',
      a: { id: '', name: deviceA.name, ci_type_id: '' },
      b: { id: '', name: deviceB.name, ci_type_id: '' },
      // C reports the MACs of A and B: both CIs match (REC-02 step 3).
      c: {
        name: `ri-c-${s}`,
        ip: '192.0.2.33',
        serial: `RI-C-${s}`,
        mac: mac(1),
        type: 'server',
        ifMacs: [mac(2)],
      },
      created: {
        name: `ri-new-${s}`,
        ip: '192.0.2.34',
        serial: `RI-N-${s}`,
        mac: mac(4),
        type: unknownType,
      },
      twice: {
        name: `ri-twice-${s}`,
        ip: '192.0.2.35',
        serial: `RI-T-${s}`,
        mac: mac(5),
        type: unknownType,
      },
      viewerItem: {
        name: `ri-viewer-${s}`,
        ip: '192.0.2.36',
        serial: `RI-V-${s}`,
        mac: mac(6),
        type: unknownType,
      },
      viewer: { email: '', password: '' },
    };

    const types = await ok<List<{ id: string; key: string }>>('GET', '/ci-types?limit=200');
    w.serverType = (types.data ?? []).find((t) => t.key === 'server')?.id ?? '';
    expect(w.serverType, 'catalog type server').not.toBe('');
    expect(
      (types.data ?? []).some((t) => t.key === unknownType),
      'the reported type is unknown',
    ).toBe(false);

    await discover([deviceA, deviceB]);
    await expect
      .poll(
        async () => [Boolean(await ciByName(deviceA.name)), Boolean(await ciByName(deviceB.name))],
        {
          message: 'CIs A and B of the first discovery',
          timeout: 120_000,
          intervals: [2_000],
        },
      )
      .toEqual([true, true]);
    w.a = (await ciByName(deviceA.name))!;
    w.b = (await ciByName(deviceB.name))!;

    await discover([w.c, w.created, w.twice, w.viewerItem]);
    const ambiguous = await waitForItem('ambiguous_identity', w.c.name);
    expect([...(ambiguous.candidate_ci_ids ?? [])].sort()).toEqual([w.a.id, w.b.id].sort());
    for (const device of [w.created, w.twice, w.viewerItem]) {
      const item = await waitForItem('unclassified_device', device.name);
      expect(item.payload.reason).toBe('unknown_ci_type');
    }
    expect(await ciByName(w.c.name), 'C is no CI while its identity is ambiguous').toBeUndefined();

    const roles = await ok<List<{ id: string; name: string }>>('GET', '/roles?limit=100');
    const viewerRole = (roles.data ?? []).find((role) => role.name === 'viewer')?.id ?? '';
    expect(viewerRole, 'system role viewer (RBA-02)').not.toBe('');
    w.viewer = await invite(viewerRole, 'viewer');
  });

  test.afterAll(async () => {
    await w?.ctx.close();
    await w?.rq.dispose();
  });

  test('R1 ambiguous_identity: merge into the chosen candidate in the UI; the candidate carries the management IP, no CI is created', async () => {
    const item = (await itemOf('ambiguous_identity', w.c.name))!;
    const countBefore = (await cis()).length;
    const main = await openItem(w.page, 'ambiguous_identity', w.c.name);

    // The candidates are named by their CIs, not by ids (F-346).
    const candidate = main.getByLabel('Kandidat');
    await expect(candidate.locator('option', { hasText: w.b.name })).toHaveCount(1);
    await expect(candidate.locator('option', { hasText: w.a.name })).toHaveCount(1);
    await expect(candidate.locator('option', { hasText: w.b.id })).toHaveCount(0);
    await candidate.selectOption(w.b.id);

    const resolved = waitForResolve(w.page, item.id);
    await main.getByRole('button', { name: 'Zusammenführen' }).click();
    const res = await resolved;
    expect(res.status(), `merge: ${await res.text()}`).toBe(200);

    const stored = await ok<ReviewItem>('GET', `/review-items/${item.id}`);
    expect(stored).toMatchObject({ status: 'resolved', action: 'merge' });
    const b = await ok<CI>('GET', `/cis/${w.b.id}`);
    expect(b.management_ip, 'B carries the management IP of the merged device').toBe(w.c.ip);
    const a = await ok<CI>('GET', `/cis/${w.a.id}`);
    expect(a.management_ip, 'A is not the chosen candidate').not.toBe(w.c.ip);
    expect((await cis()).length, 'merge creates no CI').toBe(countBefore);
    expect(await ciByName(w.c.name)).toBeUndefined();
  });

  test('R2 unclassified_device: "Neu anlegen" with the chosen type creates the CI of that type', async () => {
    const item = (await itemOf('unclassified_device', w.created.name))!;
    expect(await ciByName(w.created.name)).toBeUndefined();
    const main = await openItem(w.page, 'unclassified_device', w.created.name);

    const create = main.getByRole('button', { name: 'Neu anlegen' });
    await expect(create).toBeDisabled();
    await main.getByLabel('CI-Typ des neuen CIs').selectOption(w.serverType);
    await expect(create).toBeEnabled();
    const resolved = waitForResolve(w.page, item.id);
    await create.click();
    const res = await resolved;
    expect(res.status(), `new: ${await res.text()}`).toBe(200);

    expect(await ok<ReviewItem>('GET', `/review-items/${item.id}`)).toMatchObject({
      status: 'resolved',
      action: 'new',
    });
    const ci = await ciByName(w.created.name);
    expect(ci, 'the CI created by new').toBeDefined();
    expect(ci!.ci_type_id).toBe(w.serverType);
  });

  test('R3 a second resolution of the same item shows the 409 of the server', async () => {
    const item = (await itemOf('unclassified_device', w.twice.name))!;
    const main = await openItem(w.page, 'unclassified_device', w.twice.name);
    const dismiss = main.getByRole('button', { name: 'Verwerfen' });
    await expect(dismiss).toBeEnabled();

    // Another session resolves the item while the inbox still shows it open.
    await ok('POST', `/review-items/${item.id}/resolve`, { action: 'dismiss' });

    const resolved = waitForResolve(w.page, item.id);
    await dismiss.click();
    const res = await resolved;
    expect(res.status(), `second resolution: ${await res.text()}`).toBe(409);
    await expect(main.getByRole('alert')).toContainText('already resolved');
    expect(await ok<ReviewItem>('GET', `/review-items/${item.id}`)).toMatchObject({
      status: 'dismissed',
      action: 'dismiss',
    });
  });

  test('R4 a user without review:resolve sees no action; a direct POST answers 403', async ({
    browser,
  }) => {
    const item = (await itemOf('unclassified_device', w.viewerItem.name))!;
    const { ctx, page, token } = await signIn(browser, w.viewer);
    try {
      const rights = await api<{ permissions: string[] }>(
        page.request,
        token,
        'GET',
        '/me/permissions',
      );
      expect(rights.status).toBe(200);
      expect(rights.body.permissions).toContain('discovery:read');
      expect(rights.body.permissions).not.toContain('review:resolve');

      const main = await openItem(page, 'unclassified_device', w.viewerItem.name);
      for (const name of ['Neu anlegen', 'Verwerfen', 'Zusammenführen', 'Übernehmen']) {
        await expect(main.getByRole('button', { name, exact: true })).toHaveCount(0);
      }
      // No selection for a bulk resolution (the filter "Nur eskalierte" stays).
      await expect(main.getByRole('checkbox', { name: /auswählen/ })).toHaveCount(0);
      await expect(main.getByRole('group', { name: 'Sammelaktion' })).toHaveCount(0);
      await expect(main.getByLabel('CI-Typ des neuen CIs')).toHaveCount(0);

      const post = await api(page.request, token, 'POST', `/review-items/${item.id}/resolve`, {
        action: 'dismiss',
      });
      expect(post.status, `POST resolve as viewer: ${JSON.stringify(post.body)}`).toBe(403);
    } finally {
      await ctx.close();
    }
    expect((await ok<ReviewItem>('GET', `/review-items/${item.id}`)).status).toBe('open');
  });
});
