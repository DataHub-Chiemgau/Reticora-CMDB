import type { Page } from '@playwright/test';
import { AT, ORG_ID, expect, list, mockBackend, signIn, test } from './support/backend';

// IPM-19 (WP-918): the tabs VLANs, VRFs and nameserver sets of the IPAM
// page against a mocked backend whose writes the next read returns. A
// domain is created and a site assigned, a VLAN created in the domain, a
// VRF with route distinguisher created and a nameserver set created and
// deleted. Requests and mocks are checked against api/openapi.yaml.

test.use({ locale: 'de-DE' });

const USER = '00000000-0000-0000-0000-000000000001';
const SITE = 'a9180000-0000-4000-8000-000000000001';
const GLOBAL = 'a9180000-0000-4000-8000-000000000002';

let next = 0;
const newId = () => `a9180000-0000-4000-8000-0000000001${String(++next).padStart(2, '0')}`;

interface Stored extends Record<string, unknown> {
  id: string;
}

async function mockTabs(page: Page) {
  const domains: Stored[] = [];
  const vlans: Stored[] = [];
  const sets: Stored[] = [];
  const vrfs: Stored[] = [
    {
      id: GLOBAL,
      organization_id: ORG_ID,
      name: 'global',
      is_default: true,
      import_targets: [],
      export_targets: [],
      section_ids: [],
      created_at: AT,
      updated_at: AT,
    },
  ];
  const created = (body: Record<string, unknown>) => ({
    id: newId(),
    organization_id: ORG_ID,
    ...body,
    created_at: AT,
    updated_at: AT,
  });
  return mockBackend(page, [
    {
      path: '/me/permissions',
      reply: () => ({
        body: { user_id: USER, permissions: ['ipam:read', 'ipam:write', 'vrf:manage'] },
      }),
    },
    {
      path: '/sites',
      reply: () => ({
        body: list([
          {
            id: SITE,
            organization_id: ORG_ID,
            client_id: 'a9180000-0000-4000-8000-000000000009',
            name: 'Werk Nord',
            timezone: 'Europe/Berlin',
            created_at: AT,
            updated_at: AT,
          },
        ]),
      }),
    },
    { path: '/ipam-sections', reply: () => ({ body: list([]) }) },
    { path: '/vrfs', reply: () => ({ body: list(vrfs) }) },
    {
      method: 'POST',
      path: '/vrfs',
      reply: (request) => {
        const body = request.postDataJSON() as Record<string, unknown>;
        const vrf = { ...created(body), is_default: false, section_ids: body.section_ids ?? [] };
        vrfs.push(vrf);
        return { status: 201, body: vrf };
      },
    },
    { path: '/vlan-domains', reply: () => ({ body: list(domains) }) },
    {
      method: 'POST',
      path: '/vlan-domains',
      reply: (request) => {
        const domain = created(request.postDataJSON() as Record<string, unknown>);
        domains.push(domain);
        return { status: 201, body: domain };
      },
    },
    {
      method: 'PATCH',
      path: /^\/vlan-domains\/[^/]+$/,
      reply: (request, url) => {
        const domain = domains.find((d) => url.pathname.endsWith(d.id));
        Object.assign(domain ?? {}, request.postDataJSON());
        return { body: domain };
      },
    },
    {
      path: /^\/vlan-domains\/[^/]+\/vlans$/,
      reply: (_request, url) => ({
        body: list(vlans.filter((v) => url.pathname.includes(String(v.domain_id)))),
      }),
    },
    { path: '/vlans', reply: () => ({ body: list(vlans) }) },
    {
      method: 'POST',
      path: '/vlans',
      reply: (request) => {
        const vlan = created(request.postDataJSON() as Record<string, unknown>);
        vlans.push(vlan);
        return { status: 201, body: vlan };
      },
    },
    { path: '/nameserver-sets', reply: () => ({ body: list(sets) }) },
    {
      method: 'POST',
      path: '/nameserver-sets',
      reply: (request) => {
        const set = created(request.postDataJSON() as Record<string, unknown>);
        sets.push(set);
        return { status: 201, body: set };
      },
    },
    {
      method: 'DELETE',
      path: /^\/nameserver-sets\/[^/]+$/,
      reply: (_request, url) => {
        const at = sets.findIndex((s) => url.pathname.endsWith(s.id));
        sets.splice(at, 1);
        return { status: 204 };
      },
    },
  ]);
}

test('creates a domain with a site and a VLAN in it, a VRF with RD and a nameserver set', async ({
  page,
}) => {
  const calls = await mockTabs(page);
  await signIn(page);
  await page.goto('/ipam?tab=vlans');

  // Domain: create, then assign the site.
  await page.getByRole('button', { name: 'Neue Domäne' }).click();
  let dialog = page.getByRole('dialog', { name: 'Neue Domäne' });
  await dialog.getByLabel('Name der Domäne').fill('campus');
  await dialog.getByRole('button', { name: 'Speichern' }).click();
  await expect(page.getByRole('button', { name: 'Domäne campus bearbeiten' })).toBeVisible();
  await page.getByRole('button', { name: 'Domäne campus bearbeiten' }).click();
  dialog = page.getByRole('dialog', { name: 'Domäne campus bearbeiten' });
  await dialog.getByLabel('Werk Nord').check();
  await dialog.getByRole('button', { name: 'Speichern' }).click();
  await expect(page.getByRole('listitem').filter({ hasText: 'campus' })).toContainText('Werk Nord');

  // VLAN in the domain.
  await page.getByRole('button', { name: 'Neues VLAN' }).click();
  dialog = page.getByRole('dialog', { name: 'Neues VLAN' });
  await dialog.getByLabel('Nummer').fill('100');
  await dialog.getByLabel('Name', { exact: true }).fill('users');
  await dialog.getByLabel('Standort').selectOption({ label: 'Werk Nord' });
  await dialog.getByLabel('Domäne').selectOption({ label: 'campus' });
  await dialog.getByRole('button', { name: 'Speichern' }).click();
  await page
    .getByRole('group', { name: 'Filter der VLANs' })
    .getByLabel('Domäne')
    .selectOption({ label: 'campus' });
  await expect(page.getByRole('cell', { name: 'users' })).toBeVisible();

  // VRF with route distinguisher.
  await page.getByRole('tab', { name: 'VRFs' }).click();
  await expect(page).toHaveURL(/tab=vrfs/);
  await page.getByRole('button', { name: 'Neue VRF' }).click();
  dialog = page.getByRole('dialog', { name: 'Neue VRF' });
  await dialog.getByLabel('Name').fill('kunden');
  await dialog.getByLabel('Route Distinguisher').fill('65000:100');
  await dialog.getByRole('button', { name: 'Speichern' }).click();
  await expect(page.getByRole('cell', { name: '65000:100' })).toBeVisible();

  // Nameserver set: create and delete.
  await page.getByRole('tab', { name: 'Nameserver-Sets' }).click();
  await page.getByRole('button', { name: 'Neues Nameserver-Set' }).click();
  dialog = page.getByRole('dialog', { name: 'Neues Nameserver-Set' });
  await dialog.getByLabel('Name').fill('intern');
  await dialog.getByLabel(/Server/).fill('10.0.0.53\n10.0.0.54');
  await dialog.getByRole('button', { name: 'Speichern' }).click();
  await expect(page.getByRole('cell', { name: '10.0.0.53, 10.0.0.54' })).toBeVisible();
  await page.getByRole('button', { name: 'Nameserver-Set intern löschen' }).click();
  await expect(page.getByText('Keine Nameserver-Sets.')).toBeVisible();

  const writes = calls.filter((c) => c.method !== 'GET').map((c) => `${c.method} ${c.path}`);
  expect(writes).toEqual([
    'POST /vlan-domains',
    expect.stringMatching(/^PATCH \/vlan-domains\//),
    'POST /vlans',
    'POST /vrfs',
    'POST /nameserver-sets',
    expect.stringMatching(/^DELETE \/nameserver-sets\//),
  ]);
  const vlan = calls.find((c) => c.method === 'POST' && c.path === '/vlans');
  expect(vlan?.body).toMatchObject({ vlan_id: 100, name: 'users', site_id: SITE });
});
