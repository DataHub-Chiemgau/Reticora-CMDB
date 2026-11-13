import type { Page } from '@playwright/test';
import { AT, ORG_ID, expect, list, mockBackend, signIn, test } from './support/backend';

// IPM-19, UI-14 (WP-916): the IPAM page leads to the subnet detail. A
// subnet of the tree opens /ipam/subnets/:id with usage, address map and
// address list; with ipam:write the next free address is allocated and an
// address is reserved, both visible at once in the list and the usage.
// Requests and mocks are checked against api/openapi.yaml
// (support/contract.ts).

test.use({ locale: 'de-DE' });

const USER = '00000000-0000-0000-0000-000000000001';
const SECTION = 'a9160000-0000-4000-8000-000000000001';
const VRF = 'a9160000-0000-4000-8000-000000000002';
const NET = 'a9160000-0000-4000-8000-000000000003';

interface StoredAddress {
  id: string;
  organization_id: string;
  subnet_id: string;
  address: string;
  vrf_id: string;
  type: 'static' | 'reserved';
  role: 'primary';
  origin: 'manual';
  pinned: boolean;
  state: 'unknown';
  is_gateway?: boolean;
  created_at: string;
  updated_at: string;
}

function address(n: number, ip: string, type: StoredAddress['type']): StoredAddress {
  return {
    id: `a9160000-0000-4000-8000-0000000001${String(n).padStart(2, '0')}`,
    organization_id: ORG_ID,
    subnet_id: NET,
    address: ip,
    vrf_id: VRF,
    type,
    role: 'primary',
    origin: 'manual',
    pinned: false,
    state: 'unknown',
    created_at: AT,
    updated_at: AT,
  };
}

const SUBNET = {
  id: NET,
  organization_id: ORG_ID,
  cidr: '10.91.6.0/24',
  vrf_id: VRF,
  section_id: SECTION,
  name: 'e2e-office',
  gateway: '10.91.6.1',
  effective_dns_servers: ['10.91.0.53'],
  is_management: false,
  is_pool: false,
  child_count: 0,
  created_at: AT,
  updated_at: AT,
};

async function mockIpam(page: Page) {
  const stored: StoredAddress[] = [{ ...address(1, '10.91.6.1', 'static'), is_gateway: true }];
  const calls = await mockBackend(page, [
    {
      path: '/me/permissions',
      reply: () => ({ body: { user_id: USER, permissions: ['ipam:read', 'ipam:write'] } }),
    },
    {
      path: '/ipam-sections',
      reply: () => ({
        body: list([
          {
            id: SECTION,
            organization_id: ORG_ID,
            name: 'default',
            strict_mode: false,
            show_vlan: true,
            show_vrf: true,
            is_default: true,
            acl_enabled: false,
            vrf_ids: [],
            created_at: AT,
            updated_at: AT,
          },
        ]),
      }),
    },
    {
      path: '/vrfs',
      reply: () => ({
        body: list([
          {
            id: VRF,
            organization_id: ORG_ID,
            name: 'global',
            is_default: true,
            import_targets: [],
            export_targets: [],
            section_ids: [],
            created_at: AT,
            updated_at: AT,
          },
        ]),
      }),
    },
    {
      path: `/vrfs/${VRF}`,
      reply: () => ({
        body: {
          id: VRF,
          organization_id: ORG_ID,
          name: 'global',
          is_default: true,
          route_distinguisher: '65000:916',
          import_targets: [],
          export_targets: [],
          section_ids: [],
          created_at: AT,
          updated_at: AT,
        },
      }),
    },
    {
      path: '/subnets',
      reply: (_request, url) => ({
        body: list(url.searchParams.get('parent_id') === 'root' ? [SUBNET] : []),
      }),
    },
    { path: `/subnets/${NET}`, reply: () => ({ body: SUBNET }) },
    {
      path: `/subnets/${NET}/usage`,
      reply: () => ({
        body: {
          subnet_id: NET,
          cidr: SUBNET.cidr,
          is_pool: false,
          total: 254,
          used: stored.length,
          free: 254 - stored.length,
          used_by_state: { unknown: stored.length },
          used_by_type: {
            static: stored.filter((a) => a.type === 'static').length,
            reserved: stored.filter((a) => a.type === 'reserved').length,
          },
          percent: Math.round((stored.length / 254) * 1000) / 10,
        },
      }),
    },
    { path: `/subnets/${NET}/addresses`, reply: () => ({ body: list([...stored]) }) },
    {
      method: 'POST',
      path: `/subnets/${NET}/allocate`,
      reply: () => {
        const created = address(stored.length + 1, `10.91.6.${stored.length + 1}`, 'static');
        stored.push(created);
        return { status: 201, body: created };
      },
    },
    {
      method: 'POST',
      path: `/subnets/${NET}/reservations`,
      reply: (request) => {
        const body = request.postDataJSON() as { address: string };
        const created = address(stored.length + 1, body.address, 'reserved');
        stored.push(created);
        return { status: 201, body: created };
      },
    },
  ]);
  return calls;
}

test('opens a subnet from the tree, allocates the next free address and reserves one', async ({
  page,
}) => {
  const calls = await mockIpam(page);
  await signIn(page);
  await page.goto('/ipam');
  await page.getByRole('link', { name: '10.91.6.0/24' }).click();
  await expect(page).toHaveURL(new RegExp(`/ipam/subnets/${NET}$`));
  await expect(page.getByRole('heading', { level: 1, name: '10.91.6.0/24' })).toBeVisible();
  await expect(page.getByText('global (RD 65000:916)')).toBeVisible();
  await expect(page.getByTestId('subnet-usage')).toHaveText('1 von 254 belegt (0.4 %)');

  await page.getByRole('button', { name: 'Nächste freie Adresse zuweisen' }).click();
  await expect(page.getByText('Adresse 10.91.6.2 zugewiesen.')).toBeVisible();
  await expect(page.getByRole('cell', { name: '10.91.6.2', exact: true })).toBeVisible();
  await expect(page.getByTestId('subnet-usage')).toHaveText('2 von 254 belegt (0.8 %)');

  // A free cell of the map fills the reservation.
  await page.getByRole('button', { name: '10.91.6.40 – Frei' }).click();
  await expect(page.getByLabel('Adresse reservieren')).toHaveValue('10.91.6.40');
  await page.getByRole('button', { name: 'Reservieren', exact: true }).click();
  await expect(page.getByText('Adresse 10.91.6.40 reserviert.')).toBeVisible();
  await expect(
    page.getByRole('button', { name: 'Reservierung 10.91.6.40 freigeben' }),
  ).toBeVisible();
  await expect(page.getByRole('button', { name: '10.91.6.40 – Reserviert' })).toBeVisible();

  const writes = calls.filter((c) => c.method === 'POST').map((c) => [c.path, c.body]);
  expect(writes).toEqual([
    [`/subnets/${NET}/allocate`, { type: 'static' }],
    [`/subnets/${NET}/reservations`, { address: '10.91.6.40' }],
  ]);
});
