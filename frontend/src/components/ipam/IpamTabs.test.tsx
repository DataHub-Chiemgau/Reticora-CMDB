/**
 * WP-918 (IPM-19): the tabs VLANs, VRFs and nameserver sets of the IPAM
 * page. Without vrf:manage the VRF tab has no write action, without
 * ipam:write neither VLANs nor nameserver sets have one; the default VRF
 * has no delete action; a VLAN number taken in the domain (409) appears at
 * the field number, a site outside the domain (422) at the field site; the
 * domain filter reads /vlan-domains/{id}/vlans; the tab stays in ?tab=;
 * axe finds nothing in any tab.
 *
 * Retest:
 *   cd frontend && npx vitest run src/components/ipam/IpamTabs.test.tsx
 */
import axe from 'axe-core';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../../test/utils';
import { IpamPage } from '../../pages/IpamPage';

type Json = Record<string, unknown>;

function json(body: unknown, status = 200) {
  return new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

const AT = '2026-01-01T00:00:00Z';
const ORG = 'org-1';
const SECTION = 'a0000000-0000-4000-8000-000000000001';
const GLOBAL = 'b0000000-0000-4000-8000-000000000001';
const BLUE = 'b0000000-0000-4000-8000-000000000002';
const DOMAIN = 'd0000000-0000-4000-8000-000000000001';
const SITE_A = 'f0000000-0000-4000-8000-000000000001';
const SITE_B = 'f0000000-0000-4000-8000-000000000002';
const NS = 'e0000000-0000-4000-8000-000000000001';

const page = (data: unknown[]) => ({
  data,
  total: data.length,
  limit: 200,
  offset: 0,
  has_more: false,
});

const vrf = (id: string, name: string, extra: Json = {}) => ({
  id,
  organization_id: ORG,
  name,
  is_default: id === GLOBAL,
  route_distinguisher: id === GLOBAL ? undefined : '65000:1',
  import_targets: id === GLOBAL ? [] : ['65000:10'],
  export_targets: id === GLOBAL ? [] : ['65000:20'],
  section_ids: [SECTION],
  created_at: AT,
  updated_at: AT,
  ...extra,
});

const vlan = (id: string, number: number, extra: Json = {}) => ({
  id,
  organization_id: ORG,
  vlan_id: number,
  name: `vlan-${number}`,
  created_at: AT,
  updated_at: AT,
  ...extra,
});

interface MockOptions {
  permissions?: string[];
  /** The session is restricted to clients (cls) or to sites (sts). */
  restricted?: false | 'clients' | 'sites';
  /** The domain list has two pages; only the second holds a VLAN of SITE_A. */
  domainTwoPages?: boolean;
  onWrite?: (method: string, path: string, body: Json) => Response | undefined;
}

function mockApi({
  permissions = ['ipam:read', 'ipam:write', 'vrf:manage'],
  restricted = false,
  domainTwoPages = false,
  onWrite,
}: MockOptions = {}) {
  return vi.fn().mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), 'http://localhost');
    const method = init?.method ?? 'GET';
    const path = url.pathname.replace(/^\/api\/v1/, '');
    if (method !== 'GET') {
      const body = init?.body ? (JSON.parse(String(init.body)) as Json) : {};
      const answer = onWrite?.(method, path, body);
      if (answer) return answer;
      return json(
        method === 'DELETE' ? null : { id: 'new', ...body },
        method === 'DELETE' ? 204 : 201,
      );
    }
    if (path === '/me/permissions') return json({ user_id: 'u', permissions });
    if (path === '/me') {
      return json({
        sub: 'u',
        org: ORG,
        scopes: [],
        iat: 0,
        exp: 0,
        cls: restricted === 'clients' ? ['c1'] : null,
        sts: restricted === 'sites' ? [SITE_A] : null,
      });
    }
    if (path === '/ipam-sections') {
      return json(
        page([
          {
            id: SECTION,
            organization_id: ORG,
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
      );
    }
    if (path === '/vrfs') return json(page([vrf(GLOBAL, 'global'), vrf(BLUE, 'blue')]));
    if (path === '/vlan-domains') {
      return json(
        page([
          {
            id: DOMAIN,
            organization_id: ORG,
            name: 'campus',
            site_ids: [SITE_A],
            created_at: AT,
            updated_at: AT,
          },
        ]),
      );
    }
    if (path === `/vlan-domains/${DOMAIN}/vlans` && domainTwoPages) {
      if (url.searchParams.get('offset') === '1') {
        const last = [vlan('v-dom', 100, { domain_id: DOMAIN, site_id: SITE_A })];
        return json({ data: last, total: 2, limit: 200, offset: 1, has_more: false });
      }
      const first = [vlan('v-other', 101, { domain_id: DOMAIN, site_id: SITE_B })];
      return json({ data: first, total: 2, limit: 200, offset: 0, has_more: true });
    }
    if (path === `/vlan-domains/${DOMAIN}/vlans`) {
      return json(page([vlan('v-dom', 100, { domain_id: DOMAIN, site_id: SITE_A })]));
    }
    if (path === '/vlans')
      return json(page([vlan('v-1', 10), vlan('v-2', 20, { site_id: SITE_B })]));
    if (path === '/nameserver-sets') {
      return json(
        page([
          {
            id: NS,
            organization_id: ORG,
            name: 'intern',
            servers: ['10.0.0.53'],
            description: 'Rechenzentrum',
            created_at: AT,
            updated_at: AT,
          },
        ]),
      );
    }
    if (path === '/sites') {
      return json(
        page([
          { id: SITE_A, organization_id: ORG, name: 'Werk A', created_at: AT },
          { id: SITE_B, organization_id: ORG, name: 'Werk B', created_at: AT },
        ]),
      );
    }
    if (path === '/clients' || path === '/subnets') return json(page([]));
    return json({ title: 'Not Found', status: 404 }, 404);
  });
}

const gets = (fetchMock: ReturnType<typeof mockApi>, suffix: string) =>
  fetchMock.mock.calls.filter(
    (call) =>
      ((call[1] as RequestInit | undefined)?.method ?? 'GET') === 'GET' &&
      new URL(String(call[0]), 'http://localhost').pathname.endsWith(suffix),
  );

async function noAxeFindings(container: HTMLElement) {
  const result = await axe.run(container, { rules: { 'color-contrast': { enabled: false } } });
  expect(
    result.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.html).join(' | ')}`),
  ).toEqual([]);
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('IPAM tabs', () => {
  it('opens the tab of ?tab= and keeps the chosen tab in the query', async () => {
    vi.stubGlobal('fetch', mockApi());
    renderWithProviders(<IpamPage />, { route: '/ipam?tab=vrfs' });
    expect(await screen.findByRole('tab', { name: 'VRFs' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    expect(await screen.findByTestId(`vrf-row-${BLUE}`)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('tab', { name: 'Nameserver-Sets' }));
    expect(await screen.findByTestId(`nameserver-row-${NS}`)).toHaveTextContent('10.0.0.53');
    expect(screen.getByRole('tab', { name: 'Nameserver-Sets' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
  });

  it('hides the VRF write actions without vrf:manage and those of VLANs and sets without ipam:write', async () => {
    vi.stubGlobal('fetch', mockApi({ permissions: ['ipam:read'] }));
    renderWithProviders(<IpamPage />, { route: '/ipam?tab=vrfs' });
    await screen.findByTestId(`vrf-row-${BLUE}`);
    expect(screen.queryByRole('button', { name: 'Neue VRF' })).toBeNull();
    expect(screen.queryByRole('button', { name: /VRF blue/ })).toBeNull();
    fireEvent.click(screen.getByRole('tab', { name: 'VLANs' }));
    await screen.findByTestId('vlan-row-v-1');
    expect(screen.queryByRole('button', { name: 'Neues VLAN' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Neue Domäne' })).toBeNull();
    expect(screen.queryByRole('button', { name: /VLAN 10/ })).toBeNull();
    fireEvent.click(screen.getByRole('tab', { name: 'Nameserver-Sets' }));
    await screen.findByTestId(`nameserver-row-${NS}`);
    expect(screen.queryByRole('button', { name: 'Neues Nameserver-Set' })).toBeNull();
  });

  it.each([
    ['clients', 'clients' as const],
    ['sites', 'sites' as const],
  ])(
    'hides domain, set and VRF writes for a principal restricted to %s',
    async (_name, restricted) => {
      vi.stubGlobal('fetch', mockApi({ restricted }));
      renderWithProviders(<IpamPage />, { route: '/ipam?tab=vlans' });
      await screen.findByTestId('vlan-row-v-1');
      // VLANs stay writable with ipam:write; the domains do not.
      expect(screen.getByRole('button', { name: 'Neues VLAN' })).toBeInTheDocument();
      expect(screen.queryByRole('button', { name: 'Neue Domäne' })).toBeNull();
      fireEvent.click(screen.getByRole('tab', { name: 'VRFs' }));
      await screen.findByTestId(`vrf-row-${BLUE}`);
      expect(screen.queryByRole('button', { name: 'Neue VRF' })).toBeNull();
      fireEvent.click(screen.getByRole('tab', { name: 'Nameserver-Sets' }));
      await screen.findByTestId(`nameserver-row-${NS}`);
      expect(screen.queryByRole('button', { name: 'Neues Nameserver-Set' })).toBeNull();
      expect(screen.queryByRole('button', { name: /Nameserver-Set intern/ })).toBeNull();
    },
  );

  it('offers no delete action for the default VRF', async () => {
    vi.stubGlobal('fetch', mockApi());
    renderWithProviders(<IpamPage />, { route: '/ipam?tab=vrfs' });
    const global = await screen.findByTestId(`vrf-row-${GLOBAL}`);
    // The session loads before the actions show.
    expect(
      await within(global).findByRole('button', { name: 'VRF global bearbeiten' }),
    ).toBeVisible();
    expect(within(global).getByText('Standard')).toBeInTheDocument();
    expect(within(global).queryByRole('button', { name: 'VRF global löschen' })).toBeNull();
    const blue = screen.getByTestId(`vrf-row-${BLUE}`);
    expect(within(blue).getByRole('button', { name: 'VRF blue löschen' })).toBeInTheDocument();
    // Editing the default VRF keeps RD and targets read-only.
    fireEvent.click(within(global).getByRole('button', { name: 'VRF global bearbeiten' }));
    const dialog = await screen.findByRole('dialog', { name: 'VRF global' });
    expect(within(dialog).getByLabelText('Route Distinguisher')).toBeDisabled();
    expect(within(dialog).getByLabelText('Import-Targets')).toBeDisabled();
  });

  it('shows a taken VLAN number (409) at the field number and a site outside the domain (422) at the field site', async () => {
    let attempt = 0;
    vi.stubGlobal(
      'fetch',
      mockApi({
        onWrite: (method, path) => {
          if (method !== 'POST' || path !== '/vlans') return undefined;
          attempt++;
          if (attempt === 1) {
            return json(
              { title: 'Conflict', status: 409, detail: 'vlan_id 100 already exists' },
              409,
            );
          }
          return json(
            {
              title: 'Unprocessable Entity',
              status: 422,
              detail: 'the site does not belong to the VLAN domain',
              field: 'site_id',
            },
            422,
          );
        },
      }),
    );
    renderWithProviders(<IpamPage />, { route: '/ipam?tab=vlans' });
    fireEvent.click(await screen.findByRole('button', { name: 'Neues VLAN' }));
    const dialog = await screen.findByRole('dialog', { name: 'Neues VLAN' });
    fireEvent.change(within(dialog).getByLabelText('Nummer'), { target: { value: '100' } });
    fireEvent.change(within(dialog).getByLabelText('Name'), { target: { value: 'users' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));
    const number = within(dialog).getByLabelText('Nummer');
    await waitFor(() => expect(number).toHaveAttribute('aria-invalid', 'true'));
    expect(
      within(dialog).getByText('Die Nummer ist in der Domäne bzw. am Standort bereits vergeben.'),
    ).toBeInTheDocument();
    expect(within(dialog).queryByRole('alert')).toBeNull();
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));
    const site = within(dialog).getByLabelText('Standort');
    await waitFor(() => expect(site).toHaveAttribute('aria-invalid', 'true'));
    expect(
      within(dialog).getByText('the site does not belong to the VLAN domain'),
    ).toBeInTheDocument();
  });

  it('loads the VLANs of a domain over /vlan-domains/{id}/vlans', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<IpamPage />, { route: '/ipam?tab=vlans' });
    await screen.findByTestId('vlan-row-v-1');
    const filters = screen.getByRole('group', { name: 'Filter der VLANs' });
    fireEvent.change(within(filters).getByLabelText('Domäne'), { target: { value: DOMAIN } });
    expect(await screen.findByTestId('vlan-row-v-dom')).toHaveTextContent('campus');
    expect(gets(fetchMock, `/vlan-domains/${DOMAIN}/vlans`)).toHaveLength(1);
    expect(screen.queryByTestId('vlan-row-v-1')).toBeNull();
    // The site filter of GET /vlans.
    fireEvent.change(within(filters).getByLabelText('Domäne'), { target: { value: '' } });
    fireEvent.change(within(filters).getByLabelText('Standort'), { target: { value: SITE_B } });
    await waitFor(() =>
      expect(
        gets(fetchMock, '/vlans').some(
          (call) =>
            new URL(String(call[0]), 'http://localhost').searchParams.get('site_id') === SITE_B,
        ),
      ).toBe(true),
    );
  });

  it('follows the domain pages until a VLAN of the chosen site appears', async () => {
    const fetchMock = mockApi({ domainTwoPages: true });
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<IpamPage />, { route: '/ipam?tab=vlans' });
    await screen.findByTestId('vlan-row-v-1');
    const filters = screen.getByRole('group', { name: 'Filter der VLANs' });
    fireEvent.change(within(filters).getByLabelText('Standort'), { target: { value: SITE_A } });
    fireEvent.change(within(filters).getByLabelText('Domäne'), { target: { value: DOMAIN } });
    expect(await screen.findByTestId('vlan-row-v-dom')).toBeInTheDocument();
    expect(screen.queryByTestId('vlan-row-v-other')).toBeNull();
    expect(screen.queryByText('Keine VLANs.')).toBeNull();
    expect(gets(fetchMock, `/vlan-domains/${DOMAIN}/vlans`).length).toBeGreaterThanOrEqual(2);
  });

  it('checks the servers of a nameserver set in the form', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<IpamPage />, { route: '/ipam?tab=nameservers' });
    fireEvent.click(await screen.findByRole('button', { name: 'Neues Nameserver-Set' }));
    const dialog = await screen.findByRole('dialog', { name: 'Neues Nameserver-Set' });
    fireEvent.change(within(dialog).getByLabelText('Name'), { target: { value: 'extern' } });
    fireEvent.change(within(dialog).getByLabelText(/Server/), {
      target: { value: '9.9.9.9\nkein-server' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));
    expect(await within(dialog).findByText('Keine gültige IP-Adresse: kein-server')).toBeVisible();
    fireEvent.change(within(dialog).getByLabelText(/Server/), {
      target: { value: '9.9.9.9\n2620:fe::fe' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.find((c) => (c[1] as RequestInit | undefined)?.method === 'POST'),
      ).toBeDefined(),
    );
    const post = fetchMock.mock.calls.find(
      (c) => (c[1] as RequestInit | undefined)?.method === 'POST',
    );
    expect(JSON.parse(String((post?.[1] as RequestInit).body))).toEqual({
      name: 'extern',
      servers: ['9.9.9.9', '2620:fe::fe'],
      description: '',
    });
  });

  it.each(['vlans', 'vrfs', 'nameservers'])('has no axe findings in the tab %s', async (tab) => {
    vi.stubGlobal('fetch', mockApi());
    const { container } = renderWithProviders(<IpamPage />, { route: `/ipam?tab=${tab}` });
    await screen.findByRole('tabpanel');
    await waitFor(() => expect(container.querySelector('table')).not.toBeNull());
    await noAxeFindings(container);
  });
});
