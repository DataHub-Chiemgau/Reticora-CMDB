import type { Page } from '@playwright/test';
import { AT, ORG_ID, expect, list, mockBackend, signIn, test } from './support/backend';

// UI-18/NFR-07 (WP-532): the row and card actions of the location tree, the
// Discovery collector cards, the job center and the review inbox can be
// clicked on a phone. The names are as long as the real stack makes them
// (a name with a 16-digit run id, frontend/e2e-real): before the fix the rows
// and tables of these pages were wider than the 412 px of the mobile-chrome
// project, the page scrolled sideways (WCAG 1.4.10) and in the mobile
// Chromium emulation every click on a button at the right edge landed on its
// neighbour (e2e-real (mobile-chrome) of PR #102). Runs in every browser
// project; the page never scrolls sideways, on the desktop as on the phone.

test.use({ locale: 'de-DE' });

const USER = '00000000-0000-0000-0000-000000000001';
const CLIENT = 'c0000000-0000-4000-8000-000000000001';
const SITE = '51000000-0000-4000-8000-0000000000a1';
const BUILDING = 'b1000000-0000-4000-8000-0000000000a1';
const ROOM = 'a1000000-0000-4000-8000-0000000000a1';
const RACK = 'ac000000-0000-4000-8000-0000000000a1';
const COLLECTOR = 'c1000000-0000-4000-8000-0000000000a1';
const SCOPE = 'd8000000-0000-4000-8000-0000000000a1';
const RUN = '1793143684293347';

/** expectNoSidewaysScroll: the page is not wider than the viewport. */
async function expectNoSidewaysScroll(page: Page) {
  const widths = await page.evaluate(() => ({
    scroll: document.documentElement.scrollWidth,
    client: document.documentElement.clientWidth,
  }));
  expect(
    widths.scroll,
    `page width ${widths.scroll} > viewport ${widths.client}`,
  ).toBeLessThanOrEqual(widths.client);
}

function permissions(perms: string[]) {
  return {
    path: '/me/permissions',
    reply: () => ({ body: { user_id: USER, permissions: perms } }),
  };
}

function node(
  id: string,
  kind: string,
  name: string,
  parent: string | undefined,
  path: string,
  children: unknown[] = [],
) {
  return {
    id,
    organization_id: ORG_ID,
    client_id: CLIENT,
    site_id: SITE,
    ...(parent ? { parent_id: parent } : {}),
    kind,
    name,
    path,
    created_at: AT,
    updated_at: AT,
    children,
  };
}

test('location tree: delete and edit of every row are clickable', async ({ page }) => {
  const p = (...ids: string[]) => ids.map((id) => id.replace(/-/g, '_')).join('.');
  const rack = node(RACK, 'rack', `L09 Rack ${RUN}`, ROOM, p(SITE, BUILDING, ROOM, RACK));
  const room = node(ROOM, 'room', `L11a Room ${RUN}`, BUILDING, p(SITE, BUILDING, ROOM), [rack]);
  const building = node(BUILDING, 'building', `L04 Building ${RUN}`, SITE, p(SITE, BUILDING), [
    room,
  ]);
  const site = node(SITE, 'site', `L04 Site ${RUN}`, undefined, p(SITE), [building]);
  const calls = await mockBackend(page, [
    { path: '/locations/tree', reply: () => ({ body: { data: [site] } }) },
    { method: 'DELETE', path: `/locations/${RACK}`, reply: () => ({ status: 204 }) },
  ]);
  await signIn(page);
  await page.goto('/locations');
  const row = (name: string) =>
    page.locator('#main-content li > div').filter({ has: page.getByText(name, { exact: true }) });
  // The tree opens two levels; the room (depth 2) is expanded by hand.
  await row(room.name).getByRole('button', { name: 'expand' }).click();
  await expect(row(rack.name)).toBeVisible();
  await expectNoSidewaysScroll(page);

  // Every row: "Löschen" opens the confirmation of exactly this node.
  for (const n of [site, building, room, rack]) {
    await row(n.name).getByRole('button', { name: 'Löschen' }).click();
    const confirm = page.getByRole('dialog', { name: `„${n.name}“ löschen?` });
    await expect(confirm).toBeVisible();
    await confirm.getByRole('button', { name: 'Abbrechen' }).click();
    await expect(confirm).toBeHidden();
  }
  // "Bearbeiten" of the room and "+ Unterpunkt" of the building open their forms.
  await row(room.name).getByRole('button', { name: 'Bearbeiten' }).click();
  await expect(page.getByRole('dialog')).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(page.getByRole('dialog')).toBeHidden();
  await row(building.name)
    .getByRole('button', { name: /Unterpunkt/ })
    .click();
  await expect(page.getByRole('dialog')).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(page.getByRole('dialog')).toBeHidden();

  // The leaf is deleted through the confirmation.
  await row(rack.name).getByRole('button', { name: 'Löschen' }).click();
  await page
    .getByRole('dialog', { name: `„${rack.name}“ löschen?` })
    .getByRole('button', { name: 'Löschen' })
    .click();
  await expect
    .poll(() => calls.filter((c) => c.method === 'DELETE').map((c) => c.path))
    .toEqual([`/locations/${RACK}`]);
});

test('Discovery: "Scope anlegen" and the scope actions of a collector card are clickable', async ({
  page,
}) => {
  const collectorName = `collector-a-${RUN}`;
  const scopeName = `scope-${RUN}-lan-segment`;
  const calls = await mockBackend(page, [
    permissions(['discovery:read', 'discovery:manage', 'collector:manage', 'review:resolve']),
    {
      path: '/collectors',
      reply: () => ({
        body: list([
          {
            id: COLLECTOR,
            organization_id: ORG_ID,
            client_id: CLIENT,
            site_id: SITE,
            name: collectorName,
            version: '1.4.0',
            status: 'active',
            last_heartbeat_at: AT,
            cert_not_after: '2026-12-31T08:00:00Z',
            config: {},
            created_at: AT,
            updated_at: AT,
          },
        ]),
      }),
    },
    {
      path: '/discovery-scopes',
      reply: () => ({
        body: list([
          {
            id: SCOPE,
            organization_id: ORG_ID,
            client_id: CLIENT,
            site_id: SITE,
            collector_id: COLLECTOR,
            name: scopeName,
            kind: 'cidr',
            target: '10.200.0.0/24',
            schedule: '@hourly',
            credential_ids: [],
            plugins: ['sweep', 'snmp', 'ssh'],
            enabled: true,
            last_run_at: AT,
            health: { state: 'ok' },
            created_at: AT,
            updated_at: AT,
          },
        ]),
      }),
    },
    { path: '/review-items', reply: () => ({ body: list([]) }) },
    { path: '/credentials', reply: () => ({ body: list([]) }) },
    { path: '/vrfs', reply: () => ({ body: list([]) }) },
  ]);
  await signIn(page);
  await page.goto('/discovery');
  const card = page
    .locator('section')
    .filter({ has: page.getByRole('heading', { name: collectorName, exact: true }) });
  await expect(card.getByText(scopeName)).toBeVisible();
  await expectNoSidewaysScroll(page);

  await card.getByRole('button', { name: 'Scope anlegen' }).click();
  await expect(page.getByRole('dialog')).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(page.getByRole('dialog')).toBeHidden();

  for (const name of [/bearbeiten/i, /löschen/i]) {
    await card.getByRole('button', { name }).click();
    await expect(page.getByRole('dialog')).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog')).toBeHidden();
  }
  expect(calls.filter((c) => c.method !== 'GET')).toEqual([]);
});

test('job center: "Abbrechen" and "Ergebnis" of a job row are clickable', async ({ page }) => {
  const RUNNING = 'd1000000-0000-4000-8000-0000000000a1';
  const DONE = 'd1000000-0000-4000-8000-0000000000a2';
  const job = (id: string, status: string, extra: Record<string, unknown> = {}) => ({
    id,
    kind: 'ci.bulk',
    status,
    progress: status === 'running' ? 40 : 100,
    attempts: 1,
    max_attempts: 3,
    cancellable: status === 'running',
    created_by: USER,
    created_at: AT,
    updated_at: AT,
    started_at: AT,
    ...extra,
  });
  const jobs = [
    job(RUNNING, 'running'),
    job(DONE, 'succeeded', {
      finished_at: AT,
      result: { total: 100, succeeded: 98, failed: 2, items: [] },
    }),
  ];
  const calls = await mockBackend(page, [
    { path: '/jobs', reply: () => ({ body: list(jobs) }) },
    {
      method: 'DELETE',
      path: `/jobs/${RUNNING}`,
      reply: () => {
        jobs[0] = job(RUNNING, 'cancelled', { finished_at: AT });
        return { body: jobs[0] };
      },
    },
  ]);
  await signIn(page);
  await page.goto('/jobs');
  const main = page.locator('#main-content');
  const running = main.locator(`tr[data-job-id="${RUNNING}"]`);
  await expect(running.getByRole('button', { name: 'Abbrechen' })).toBeVisible();
  await expectNoSidewaysScroll(page);

  await main.locator(`tr[data-job-id="${DONE}"]`).getByRole('button', { name: 'Ergebnis' }).click();
  await expect(page.getByRole('dialog')).toContainText('"succeeded": 98');
  await page.keyboard.press('Escape');
  await expect(page.getByRole('dialog')).toBeHidden();

  await running.getByRole('button', { name: 'Abbrechen' }).click();
  await expect(running).toContainText('Abgebrochen');
  expect(calls.filter((c) => c.method === 'DELETE').map((c) => c.path)).toEqual([
    `/jobs/${RUNNING}`,
  ]);
});

test('review inbox: "Zusammenführen" below the comparison is clickable', async ({ page }) => {
  const FIRST = '11111111-1111-4111-8111-1111111111a1';
  const CHOSEN = '11111111-1111-4111-8111-2222222222a2';
  const ci = (id: string, name: string, serial: string) => ({
    id,
    organization_id: ORG_ID,
    ci_type_id: '5e1b0c3a-7d2f-4a10-9c01-000000000001',
    name,
    serial_number: serial,
    status: 'active',
    attributes: {},
    is_manual: false,
    version: 1,
    created_at: AT,
    updated_at: AT,
  });
  const cis = [
    ci(FIRST, `review-candidate-a-${RUN}`, `SN-${RUN}-A`),
    ci(CHOSEN, `review-candidate-b-${RUN}`, `SN-${RUN}-B`),
  ];
  const item = {
    id: 'rev-mobile-1',
    organization_id: ORG_ID,
    kind: 'ambiguous_identity',
    status: 'open',
    payload: {
      name: `review-device-${RUN}`,
      serial_number: `SN-${RUN}-OBSERVED`,
      management_ip: '10.200.0.17',
    },
    candidate_ci_ids: [FIRST, CHOSEN],
    escalated: false,
    created_at: AT,
    updated_at: AT,
  };
  const calls = await mockBackend(page, [
    {
      path: '/review-items',
      reply: () => ({ body: list(item.status === 'open' ? [item] : []) }),
    },
    {
      method: 'POST',
      path: `/review-items/${item.id}/resolve`,
      reply: (request) => {
        item.status = 'resolved';
        return { body: { ...item, action: (request.postDataJSON() as { action: string }).action } };
      },
    },
    {
      path: /^\/cis\/[^/]+$/,
      reply: (_req, url) => ({ body: cis.find((c) => url.pathname.endsWith(`/${c.id}`)) }),
    },
  ]);
  await signIn(page);
  await page.goto('/reviews');
  await page.getByRole('button', { name: new RegExp(`review-device-${RUN}`) }).click();
  const diff = page.getByRole('table', { name: 'Vergleich von Beobachtung und CI' });
  await expect(diff.getByText(`SN-${RUN}-A`)).toBeVisible();
  await page.getByLabel('Kandidat').selectOption(CHOSEN);
  await expect(diff.getByText(`SN-${RUN}-B`)).toBeVisible();
  await expectNoSidewaysScroll(page);

  await page.locator('#main-content').getByRole('button', { name: 'Zusammenführen' }).click();
  await expect(page.getByRole('button', { name: new RegExp(`review-device-${RUN}`) })).toBeHidden();
  expect(calls.filter((c) => c.method === 'POST').map((c) => c.body)).toEqual([
    { action: 'merge', ci_id: CHOSEN },
  ]);
});

test('CI detail: the provenance actions are clickable next to long values', async ({ page }) => {
  // The values of the real stack (e2e-real D02/D03/D07 in mobile-chrome): an
  // identity field holds a JSON object without a break opportunity, so the
  // three value columns of a provenance row were wider than the phone and
  // every click on "Override setzen" landed on the neighbouring row.
  const CI = 'c1000000-0000-4000-8000-0000000000b1';
  const identity = {
    mac_addresses: ['00:1b:21:5e:00:02', '00:1b:21:5e:00:03'],
    serial_numbers: [`SN-${RUN}-PRIMARY`],
  };
  const calls = await mockBackend(page, [
    {
      path: `/cis/${CI}`,
      reply: () => ({
        body: {
          id: CI,
          name: `sim-access-${RUN}`,
          model: `Catalyst-9300-48P-${RUN}`,
          status: 'active',
          ci_type_id: 'c7000000-0000-4000-8000-0000000000b1',
          attributes: {},
          is_manual: false,
          version: 1,
          organization_id: ORG_ID,
          created_at: AT,
          updated_at: AT,
        },
      }),
    },
    {
      path: `/cis/${CI}/provenance`,
      reply: () => ({
        body: {
          data: [
            {
              field: 'identifiers',
              value: identity,
              source: 'snmp',
              rank: 60,
              reason: 'source_rank',
              diverged: false,
            },
            {
              field: 'model',
              value: `Catalyst-9300-48P-${RUN}`,
              source: 'snmp',
              rank: 60,
              reason: 'source_rank',
              diverged: false,
            },
          ],
        },
      }),
    },
    {
      path: /^\/topology\/cis\/[^/]+\/neighbors$/,
      reply: () => ({ body: { nodes: [], edges: [] } }),
    },
  ]);
  await signIn(page);
  await page.goto(`/cmdb/${CI}`);
  await page.getByText('Herkunft & Overrides').click();
  await expect(page.getByText('"mac_addresses"', { exact: false }).first()).toBeVisible();
  await expectNoSidewaysScroll(page);

  const row = page
    .locator('li')
    .filter({ has: page.getByRole('button', { name: 'Herkunft von model', exact: true }) });
  await row.getByRole('button', { name: 'Override setzen' }).click({ timeout: 5_000 });
  await expect(page.getByRole('dialog')).toBeVisible();
  expect(calls.filter((c) => c.method !== 'GET')).toEqual([]);
});
