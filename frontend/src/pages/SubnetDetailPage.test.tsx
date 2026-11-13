/**
 * WP-916 (IPM-19, UI-14): the subnet detail /ipam/subnets/:id. The head
 * shows the usage, the effective DNS servers, the VRF with its route
 * distinguisher and the parent chain as breadcrumb; allocating the next
 * free address calls allocate and refreshes list and usage; a full subnet
 * (409 subnet-exhausted) says so; a reservation is created and released;
 * without ipam:write no write action is shown.
 *
 * Retest:
 *   cd frontend && npx vitest run src/pages/SubnetDetailPage.test.tsx
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { Route, Routes } from 'react-router';
import { renderWithProviders } from '../test/utils';
import { SubnetDetailPage } from './SubnetDetailPage';

type Json = Record<string, unknown>;

function json(body: unknown, status = 200) {
  return new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

const AT = '2026-01-01T00:00:00Z';
const ORG = 'org-1';
const VRF = 'b0000000-0000-4000-8000-000000000001';
const ROOT = 'c0000000-0000-4000-8000-000000000001';
const NET = 'c0000000-0000-4000-8000-000000000002';
const NS = 'e0000000-0000-4000-8000-000000000001';
const CI = 'f0000000-0000-4000-8000-000000000009';

const page = (data: unknown[]) => ({
  data,
  total: data.length,
  limit: 200,
  offset: 0,
  has_more: false,
});

function address(id: string, ip: string, extra: Json = {}) {
  return {
    id,
    organization_id: ORG,
    subnet_id: NET,
    address: ip,
    vrf_id: VRF,
    type: 'static',
    role: 'primary',
    origin: 'manual',
    pinned: false,
    state: 'active',
    created_at: AT,
    updated_at: AT,
    ...extra,
  };
}

interface MockOptions {
  permissions?: string[];
  exhausted?: boolean;
  /** The address list of NET has a second page (offset 200) with ip-p2. */
  twoPages?: boolean;
  /** ROOT is an ordinary subnet instead of a pool (navigation test). */
  rootIsSubnet?: boolean;
}

function mockApi({
  permissions = ['ipam:read', 'ipam:write'],
  exhausted = false,
  twoPages = false,
  rootIsSubnet = false,
}: MockOptions = {}) {
  const secondPage = [address('ip-p2', '10.0.1.240')];
  const addresses: Json[] = [
    address('ip-gw', '10.0.1.1', { is_gateway: true }),
    address('ip-ci', '10.0.1.10', { ci_id: CI, interface_id: 'if-1', dns_name: 'srv1' }),
    address('ip-res', '10.0.1.20', { type: 'reserved' }),
  ];
  const usage = () => ({
    subnet_id: NET,
    cidr: '10.0.1.0/24',
    is_pool: false,
    total: 254,
    used: addresses.length,
    free: 254 - addresses.length,
    used_by_state: { active: addresses.length },
    used_by_type: { static: addresses.length - 1, reserved: 1 },
    percent: Math.round((addresses.length / 254) * 1000) / 10,
  });
  return vi.fn().mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), 'http://localhost');
    const method = init?.method ?? 'GET';
    const path = url.pathname.replace(/^\/api\/v1/, '');
    const body = init?.body ? (JSON.parse(String(init.body)) as Json) : {};
    if (path === '/me/permissions') return json({ user_id: 'u', permissions });
    if (method === 'POST' && path === `/subnets/${NET}/allocate`) {
      if (exhausted) {
        return json(
          {
            type: 'urn:reticora:problem:subnet-exhausted',
            title: 'Subnet exhausted',
            status: 409,
            detail: 'no free address',
          },
          409,
        );
      }
      const created = address('ip-new', '10.0.1.2', { type: body.type ?? 'static' });
      addresses.push(created);
      return json(created, 201);
    }
    if (method === 'POST' && path === `/subnets/${NET}/reservations`) {
      const created = address('ip-res2', String(body.address), { type: 'reserved' });
      addresses.push(created);
      return json(created, 201);
    }
    const release = path.match(new RegExp(`^/subnets/${NET}/reservations/([^/]+)$`));
    if (method === 'DELETE' && release) {
      const at = addresses.findIndex((a) => a.id === release[1]);
      if (at >= 0) addresses.splice(at, 1);
      return json(null, 204);
    }
    if (path === `/subnets/${NET}`) {
      return json({
        id: NET,
        organization_id: ORG,
        cidr: '10.0.1.0/24',
        vrf_id: VRF,
        name: 'Büro',
        gateway: '10.0.1.1',
        nameserver_set_id: NS,
        effective_dns_servers: ['10.0.0.53', '10.0.0.54'],
        parent_id: ROOT,
        is_management: false,
        is_pool: false,
        created_at: AT,
        updated_at: AT,
      });
    }
    if (path === `/subnets/${ROOT}`) {
      return json({
        id: ROOT,
        organization_id: ORG,
        cidr: '10.0.0.0/16',
        vrf_id: VRF,
        is_management: false,
        is_pool: !rootIsSubnet,
        created_at: AT,
        updated_at: AT,
      });
    }
    if (path === `/subnets/${NET}/usage`) return json(usage());
    if (path === `/subnets/${NET}/addresses`) {
      if (!twoPages) return json(page([...addresses]));
      const total = addresses.length + secondPage.length;
      if (url.searchParams.get('offset') === '200') {
        return json({ data: secondPage, total, limit: 200, offset: 200, has_more: false });
      }
      return json({ data: [...addresses], total, limit: 200, offset: 0, has_more: true });
    }
    if (path === `/subnets/${ROOT}/addresses`) return json(page([]));
    if (path === `/subnets/${ROOT}/usage`) {
      return json({
        subnet_id: ROOT,
        cidr: '10.0.0.0/16',
        is_pool: !rootIsSubnet,
        total: 65534,
        used: 0,
        free: 65534,
        used_by_state: {},
        used_by_type: {},
        percent: 0,
      });
    }
    const ip = path.match(/^\/ips\/([^/]+)$/);
    if (ip) {
      const found = [...addresses, ...secondPage].find((a) => a.id === ip[1]);
      if (found) return json({ ...found, nat_translations: [] });
    }
    if (path === `/vrfs/${VRF}`) {
      return json({
        id: VRF,
        organization_id: ORG,
        name: 'blue',
        is_default: false,
        route_distinguisher: '65000:1',
        import_targets: [],
        export_targets: [],
        section_ids: [],
        created_at: AT,
        updated_at: AT,
      });
    }
    if (path === '/clients' || path === '/sites') return json(page([]));
    return json({ title: 'Not Found', status: 404 }, 404);
  });
}

function renderPage(search = '') {
  return renderWithProviders(
    <Routes>
      <Route path="/ipam/subnets/:id" element={<SubnetDetailPage />} />
    </Routes>,
    { route: `/ipam/subnets/${NET}${search}` },
  );
}

const calls = (fetchMock: ReturnType<typeof mockApi>, method: string, suffix: string) =>
  fetchMock.mock.calls.filter((call) => {
    const url = new URL(String(call[0]), 'http://localhost');
    return (
      ((call[1] as RequestInit | undefined)?.method ?? 'GET') === method &&
      url.pathname.endsWith(suffix)
    );
  });

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('SubnetDetailPage', () => {
  it('shows the head with usage, effective DNS servers, VRF with RD and the breadcrumb', async () => {
    vi.stubGlobal('fetch', mockApi());
    renderPage();
    expect(await screen.findByRole('heading', { level: 1, name: '10.0.1.0/24' })).toBeVisible();
    expect(await screen.findByText('10.0.0.53, 10.0.0.54')).toBeInTheDocument();
    expect(await screen.findByText('blue (RD 65000:1)')).toBeInTheDocument();
    const crumbs = screen.getByRole('navigation', { name: 'Brotkrumen-Navigation' });
    expect(await within(crumbs).findByRole('link', { name: '10.0.0.0/16' })).toHaveAttribute(
      'href',
      `/ipam/subnets/${ROOT}`,
    );
    expect(within(crumbs).getByRole('link', { name: 'IP-Adressverwaltung' })).toHaveAttribute(
      'href',
      '/ipam',
    );
    expect(await screen.findByTestId('subnet-usage')).toHaveTextContent('3 von 254 belegt (1.2 %)');
    // Occupied per state and per type from the usage.
    const facts = screen.getByTestId('subnet-usage').parentElement as HTMLElement;
    expect(within(facts).getByText('Statisch').nextSibling).toHaveTextContent('2');
    expect(within(facts).getByText('Reserviert').nextSibling).toHaveTextContent('1');
    // The list: state, type, role, gateway and the link to the CI.
    const row = await screen.findByTestId('address-row-ip-ci');
    expect(within(row).getByText('Aktiv')).toBeInTheDocument();
    expect(within(row).getByText('Primär')).toBeInTheDocument();
    expect(within(row).getByRole('link', { name: 'Gerät (Interface)' })).toHaveAttribute(
      'href',
      `/cmdb/${CI}`,
    );
    expect(within(screen.getByTestId('address-row-ip-gw')).getByText('Gateway')).toBeVisible();
  });

  it('allocates the next free address and refreshes list and usage', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    renderPage();
    const button = await screen.findByRole('button', { name: 'Nächste freie Adresse zuweisen' });
    await screen.findByTestId('address-row-ip-ci');
    const usageBefore = calls(fetchMock, 'GET', '/usage').length;
    fireEvent.click(button);
    expect(await screen.findByText('Adresse 10.0.1.2 zugewiesen.')).toBeInTheDocument();
    const post = calls(fetchMock, 'POST', `/subnets/${NET}/allocate`);
    expect(post).toHaveLength(1);
    expect(JSON.parse(String((post[0]?.[1] as RequestInit).body))).toEqual({ type: 'static' });
    expect(await screen.findByTestId('address-row-ip-new')).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.getByTestId('subnet-usage')).toHaveTextContent('4 von 254 belegt'),
    );
    expect(calls(fetchMock, 'GET', '/usage').length).toBeGreaterThan(usageBefore);
  });

  it('says so when the subnet is exhausted (409)', async () => {
    vi.stubGlobal('fetch', mockApi({ exhausted: true }));
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Nächste freie Adresse zuweisen' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Das Subnetz ist voll');
  });

  it('reserves an address and releases a reservation', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    renderPage();
    const input = await screen.findByLabelText('Adresse reservieren');
    fireEvent.change(input, { target: { value: '10.0.1.30' } });
    fireEvent.click(screen.getByRole('button', { name: 'Reservieren' }));
    expect(await screen.findByText('Adresse 10.0.1.30 reserviert.')).toBeInTheDocument();
    const post = calls(fetchMock, 'POST', `/subnets/${NET}/reservations`);
    expect(JSON.parse(String((post[0]?.[1] as RequestInit).body))).toEqual({
      address: '10.0.1.30',
    });
    fireEvent.click(
      await screen.findByRole('button', { name: 'Reservierung 10.0.1.20 freigeben' }),
    );
    await waitFor(() =>
      expect(calls(fetchMock, 'DELETE', `/subnets/${NET}/reservations/ip-res`)).toHaveLength(1),
    );
    await waitFor(() => expect(screen.queryByTestId('address-row-ip-res')).toBeNull());
  });

  it('fills the reservation from a free cell of the address map', async () => {
    vi.stubGlobal('fetch', mockApi());
    renderPage();
    const map = await screen.findByRole('group', { name: 'Adresskarte von 10.0.1.0/24' });
    fireEvent.click(within(map).getByRole('button', { name: '10.0.1.5 – Frei' }));
    expect(screen.getByLabelText('Adresse reservieren')).toHaveValue('10.0.1.5');
  });

  it('marks the address of a search hit (?address=)', async () => {
    vi.stubGlobal('fetch', mockApi());
    renderPage('?address=ip-ci');
    expect(await screen.findByTestId('address-row-ip-ci')).toHaveAttribute('aria-current', 'true');
  });

  it('marks a search hit that lies on a later page of the address list', async () => {
    vi.stubGlobal('fetch', mockApi({ twoPages: true }));
    renderPage('?address=ip-p2');
    const row = await screen.findByTestId('address-row-ip-p2');
    expect(row).toHaveAttribute('aria-current', 'true');
    expect(within(row).getByText('10.0.1.240')).toBeInTheDocument();
    expect(within(row).getByText('Suchtreffer (andere Seite)')).toBeInTheDocument();
    // The first page stays loaded beside the hit.
    expect(await screen.findByTestId('address-row-ip-ci')).toBeInTheDocument();
    // The map marks the hit as well.
    const map = await screen.findByRole('group', { name: 'Adresskarte von 10.0.1.0/24' });
    expect(within(map).getByRole('button', { name: /^10\.0\.1\.240 / })).toHaveAttribute(
      'aria-pressed',
      'true',
    );
  });

  it('starts another subnet on the first address page with an empty reservation', async () => {
    const fetchMock = mockApi({ twoPages: true, rootIsSubnet: true });
    vi.stubGlobal('fetch', fetchMock);
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Weiter' }));
    expect(await screen.findByTestId('address-row-ip-p2')).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Adresse reservieren'), {
      target: { value: '10.0.1.30' },
    });
    const crumbs = screen.getByRole('navigation', { name: 'Brotkrumen-Navigation' });
    fireEvent.click(await within(crumbs).findByRole('link', { name: '10.0.0.0/16' }));
    expect(await screen.findByRole('heading', { level: 1, name: '10.0.0.0/16' })).toBeVisible();
    await waitFor(() =>
      expect(calls(fetchMock, 'GET', `/subnets/${ROOT}/addresses`)).not.toEqual([]),
    );
    for (const call of calls(fetchMock, 'GET', `/subnets/${ROOT}/addresses`)) {
      const offset = new URL(String(call[0]), 'http://localhost').searchParams.get('offset');
      expect(offset ?? '0').toBe('0');
    }
    expect(await screen.findByLabelText('Adresse reservieren')).toHaveValue('');
  });

  it('shows no write action without ipam:write', async () => {
    vi.stubGlobal('fetch', mockApi({ permissions: ['ipam:read'] }));
    renderPage();
    await screen.findByTestId('address-row-ip-res');
    expect(screen.queryByRole('button', { name: 'Nächste freie Adresse zuweisen' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Subnetz bearbeiten' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Teilnetz anlegen' })).toBeNull();
    expect(screen.queryByLabelText('Adresse reservieren')).toBeNull();
    expect(screen.queryByRole('button', { name: /freigeben/ })).toBeNull();
  });
});
