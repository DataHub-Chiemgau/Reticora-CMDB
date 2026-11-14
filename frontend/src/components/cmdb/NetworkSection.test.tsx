/**
 * WP-919 (IPM-13, UI-14): the section "Netzwerk" of the CI detail. The
 * interfaces list their addresses, addresses without an interface form
 * their own group; the head shows the management IP with subnet and VRF,
 * "nicht in IPAM" without an address and a conflict notice while a
 * duplicate_ip item is open. "Adresse zuweisen" allocates with the
 * interface and binds with PATCH ci_id and role; a failed PATCH releases
 * the address with DELETE; 409 subnet-exhausted appears at the subnet;
 * without ipam:write there is no action; axe finds nothing.
 *
 * Retest:
 *   cd frontend && npx vitest run src/components/cmdb/NetworkSection.test.tsx
 */
import axe from 'axe-core';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { useAuthStore } from '../../auth/authStore';
import { renderWithProviders } from '../../test/utils';
import i18n from '../../i18n';
import { NetworkSection } from './NetworkSection';

type Json = Record<string, unknown>;

const AT = '2026-01-01T00:00:00Z';
const ORG = 'org-1';
const CI = 'd0000000-0000-4000-8000-000000000001';
const NET = 'a0000000-0000-4000-8000-000000000001';
const NET2 = 'a0000000-0000-4000-8000-000000000002';
const VRF = 'b0000000-0000-4000-8000-000000000001';
const IF_A = 'e0000000-0000-4000-8000-000000000001';
const IF_B = 'e0000000-0000-4000-8000-000000000002';
const VLAN = 'f0000000-0000-4000-8000-000000000001';
const IP_A = 'c0000000-0000-4000-8000-000000000001';
const IP_MGMT = 'c0000000-0000-4000-8000-000000000002';
const IP_VIP = 'c0000000-0000-4000-8000-000000000003';
const IP_NEW = 'c0000000-0000-4000-8000-000000000009';

function json(body: unknown, status = 200) {
  return new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

const page = (data: unknown[]) => ({
  data,
  total: data.length,
  limit: 100,
  offset: 0,
  has_more: false,
});

const nic = (id: string, name: string, extra: Json = {}) => ({
  id,
  organization_id: ORG,
  ci_id: CI,
  name,
  if_type: 'ethernet',
  is_management: false,
  is_uplink: false,
  admin_status: 'up',
  oper_status: 'up',
  miss_count: 0,
  created_at: AT,
  updated_at: AT,
  ...extra,
});

const addr = (id: string, address: string, extra: Json = {}) => ({
  id,
  organization_id: ORG,
  subnet_id: NET,
  subnet_cidr: '10.9.0.0/24',
  address,
  vrf_id: VRF,
  type: 'static',
  role: 'primary',
  origin: 'manual',
  pinned: false,
  state: 'active',
  ci_id: CI,
  created_at: AT,
  updated_at: AT,
  ...extra,
});

const ADDRESSES = [
  addr(IP_A, '10.9.0.5', { interface_id: IF_A, interface_name: 'eth0' }),
  addr(IP_MGMT, '10.9.0.2', { interface_id: IF_B, interface_name: 'mgmt0', role: 'management' }),
  addr(IP_VIP, '10.9.0.100', { role: 'vip', state: 'unknown' }),
];

interface Options {
  permissions?: string[];
  addresses?: Json[];
  interfaces?: Json[];
  reviews?: Json[];
  /** Whether the plan of the organization holds the feature ipam. */
  ipam?: boolean;
  onWrite?: (method: string, path: string, body: Json) => Response | undefined;
}

function stubApi({
  permissions = ['ipam:read', 'ipam:write', 'discovery:read'],
  addresses = ADDRESSES,
  interfaces = [
    nic(IF_A, 'eth0', { vlan_id: VLAN, mac: '00:11:22:33:44:55' }),
    nic(IF_B, 'mgmt0', { is_management: true, is_uplink: true }),
  ],
  reviews = [],
  ipam = true,
  onWrite,
}: Options = {}) {
  const calls: { method: string; path: string; params: URLSearchParams; body?: Json }[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), 'http://localhost');
    const method = init?.method ?? 'GET';
    const path = url.pathname.replace(/^\/api\/v1/, '');
    const body = init?.body ? (JSON.parse(String(init.body)) as Json) : undefined;
    calls.push({ method, path, params: url.searchParams, body });
    if (method !== 'GET') {
      const answer = onWrite?.(method, path, body ?? {});
      if (answer) return answer;
      if (method === 'POST') return json(addr(IP_NEW, '10.9.0.7', { ci_id: undefined }), 201);
      if (method === 'DELETE') return json(null, 204);
      return json(addr(IP_NEW, '10.9.0.7', body ?? {}));
    }
    if (path === '/me/permissions') return json({ user_id: 'u', permissions });
    if (path === '/entitlements') {
      return json(page([{ feature_key: 'ipam', enabled: ipam, plan: 'pro' }]));
    }
    if (path === '/vrfs') {
      return json(page([{ id: VRF, organization_id: ORG, name: 'global', is_default: true }]));
    }
    if (path === `/vlans/${VLAN}`) {
      return json({
        id: VLAN,
        organization_id: ORG,
        vlan_id: 42,
        name: 'office',
        created_at: AT,
        updated_at: AT,
      });
    }
    if (path === `/cis/${CI}/interfaces`) return json(page(interfaces));
    if (path === `/cis/${CI}/addresses`) return json(page(addresses));
    if (path === '/subnets') {
      return json(
        page([
          { id: NET, organization_id: ORG, cidr: '10.9.0.0/24', vrf_id: VRF, name: 'office' },
          { id: NET2, organization_id: ORG, cidr: '10.9.1.0/24', vrf_id: VRF, name: 'lab' },
        ]),
      );
    }
    if (path === '/review-items') {
      const kind = url.searchParams.get('kind');
      return json(page(reviews.filter((r) => r.kind === kind)));
    }
    return json(page([]));
  });
  vi.stubGlobal('fetch', fetchMock);
  return calls;
}

function renderSection(managementIp: string | undefined = '10.9.0.2') {
  return renderWithProviders(<NetworkSection ciId={CI} managementIp={managementIp} />);
}

async function openAssign() {
  fireEvent.click(await screen.findByRole('button', { name: 'Adresse zuweisen' }));
  const dialog = await screen.findByRole('dialog', { name: 'Adresse zuweisen' });
  fireEvent.change(within(dialog).getByLabelText('Subnetz suchen'), {
    target: { value: 'office' },
  });
  const subnet = within(dialog).getByLabelText(/^Subnetz/, { selector: 'select' });
  await waitFor(() =>
    expect(
      within(subnet).getByRole('option', { name: '10.9.0.0/24 – office' }),
    ).toBeInTheDocument(),
  );
  fireEvent.change(subnet, { target: { value: NET } });
  return dialog;
}

beforeEach(async () => {
  // Signed in, so the plan (GET /entitlements) is read as in the app.
  useAuthStore.setState({
    token: 'header.payload.signature',
    expiresAt: new Date(Date.now() + 3600_000).toISOString(),
    user: { sub: 'user-1' },
  });
  await i18n.changeLanguage('de-DE');
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  useAuthStore.setState({ token: null, expiresAt: null, user: null });
});

describe('NetworkSection', () => {
  it('lists the interfaces with their addresses and a group without interface', async () => {
    stubApi();
    renderSection();

    const eth0 = await screen.findByTestId(`ci-interface-${IF_A}`);
    expect(within(eth0).getByText('eth0')).toBeInTheDocument();
    expect(within(eth0).getByText('00:11:22:33:44:55')).toBeInTheDocument();
    expect(within(eth0).getByText('Admin up · Betrieb up')).toBeInTheDocument();
    expect(await within(eth0).findByText('VLAN 42 office')).toBeInTheDocument();
    const row = within(eth0).getByTestId(`ci-address-${IP_A}`);
    expect(within(row).getByText('10.9.0.5')).toBeInTheDocument();
    expect(within(row).getByText('Primär')).toBeInTheDocument();
    expect(within(row).getByText('Aktiv')).toBeInTheDocument();
    expect(within(row).getByRole('link', { name: '10.9.0.0/24' })).toHaveAttribute(
      'href',
      `/ipam/subnets/${NET}`,
    );
    expect(await within(row).findByText('VRF: global')).toBeInTheDocument();

    const mgmt = screen.getByTestId(`ci-interface-${IF_B}`);
    expect(within(mgmt).getByText('Uplink')).toBeInTheDocument();
    expect(within(mgmt).getByText('10.9.0.2')).toBeInTheDocument();

    const group = screen.getByRole('region', { name: 'ohne Interface' });
    expect(within(group).getByText('10.9.0.100')).toBeInTheDocument();
    expect(within(group).getByText('VIP')).toBeInTheDocument();
    expect(within(eth0).queryByText('10.9.0.100')).not.toBeInTheDocument();
  });

  it('shows the management IP with subnet and VRF of its management address', async () => {
    stubApi();
    renderSection('10.9.0.2');

    const head = await screen.findByTestId('ci-network-management');
    expect(within(head).getByText('10.9.0.2')).toBeInTheDocument();
    expect(within(head).getByRole('link', { name: '10.9.0.0/24' })).toHaveAttribute(
      'href',
      `/ipam/subnets/${NET}`,
    );
    expect(await within(head).findByText('VRF: global')).toBeInTheDocument();
    expect(within(head).queryByText('nicht in IPAM')).not.toBeInTheDocument();
  });

  it('matches the management address in the backend format with prefix length', async () => {
    // address::text of an inet column carries the prefix (/32, discovery rows /24).
    stubApi({
      addresses: [
        addr(IP_A, '10.9.0.5/24', { interface_id: IF_A, interface_name: 'eth0' }),
        addr(IP_MGMT, '10.9.0.2/32', {
          interface_id: IF_B,
          interface_name: 'mgmt0',
          role: 'management',
        }),
      ],
    });
    renderSection('10.9.0.2');

    const head = await screen.findByTestId('ci-network-management');
    expect(within(head).getByRole('link', { name: '10.9.0.0/24' })).toHaveAttribute(
      'href',
      `/ipam/subnets/${NET}`,
    );
    expect(await within(head).findByText('VRF: global')).toBeInTheDocument();
    expect(within(head).queryByText('nicht in IPAM')).not.toBeInTheDocument();
  });

  it('finds the management IP by host when only a discovery row with /24 exists', async () => {
    stubApi({ addresses: [addr(IP_A, '10.9.0.5/24', { origin: 'discovery' })] });
    renderSection('10.9.0.5/32');

    const head = await screen.findByTestId('ci-network-management');
    expect(within(head).getByRole('link', { name: '10.9.0.0/24' })).toBeInTheDocument();
    expect(within(head).queryByText('nicht in IPAM')).not.toBeInTheDocument();
  });

  it('marks a management IP without an address as not in IPAM', async () => {
    stubApi({ addresses: [ADDRESSES[0]!] });
    renderSection('10.9.9.9');

    const head = await screen.findByTestId('ci-network-management');
    expect(within(head).getByText('10.9.9.9')).toBeInTheDocument();
    expect(within(head).getByText('nicht in IPAM')).toBeInTheDocument();
  });

  it('points out an open duplicate_ip item of the management address', async () => {
    stubApi({
      reviews: [
        {
          id: 'r-1',
          kind: 'duplicate_ip',
          status: 'open',
          object_key: `ip:${VRF}/10.9.0.2`,
          payload: {},
        },
      ],
    });
    renderSection('10.9.0.2');

    const head = await screen.findByTestId('ci-network-management');
    expect(await within(head).findByText('Adresskonflikt (doppelte IP)')).toBeInTheDocument();
    expect(within(head).getByRole('link', { name: 'In der Review-Inbox öffnen' })).toHaveAttribute(
      'href',
      '/reviews?kind=duplicate_ip',
    );
  });

  it('shows empty states without interfaces and addresses', async () => {
    stubApi({ interfaces: [], addresses: [] });
    renderSection(undefined);

    expect(await screen.findByText('Keine Interfaces')).toBeInTheDocument();
    expect(screen.getByText('Keine Adressen')).toBeInTheDocument();
  });

  it('shows an error state with retry', async () => {
    const calls = stubApi();
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const path = new URL(String(input), 'http://localhost').pathname;
        calls.push({ method: 'GET', path, params: new URLSearchParams() });
        if (path.endsWith('/me/permissions'))
          return json({ user_id: 'u', permissions: ['ipam:read'] });
        if (path.endsWith('/entitlements'))
          return json(page([{ feature_key: 'ipam', enabled: true, plan: 'pro' }]));
        return json({ type: 'about:blank', title: 'Internal', status: 500 }, 500);
      }),
    );
    renderSection();

    expect(
      await screen.findByText('Interfaces und Adressen konnten nicht geladen werden.'),
    ).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Erneut versuchen' })).toBeInTheDocument();
  });

  it('assigns an address: allocate with interface_id, then PATCH ci_id and role', async () => {
    const calls = stubApi();
    renderSection();
    const dialog = await openAssign();

    fireEvent.change(within(dialog).getByLabelText('Interface'), { target: { value: IF_A } });
    fireEvent.change(within(dialog).getByLabelText('Rolle'), { target: { value: 'secondary' } });
    fireEvent.change(within(dialog).getByLabelText('Typ'), { target: { value: 'reserved' } });
    fireEvent.change(within(dialog).getByLabelText('DNS-Name'), {
      target: { value: 'sw.example' },
    });
    const before = calls.filter((c) => c.path === `/cis/${CI}/addresses`).length;
    fireEvent.click(within(dialog).getByRole('button', { name: 'Zuweisen' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    const writes = calls.filter((c) => c.method !== 'GET').map((c) => [c.method, c.path, c.body]);
    expect(writes).toEqual([
      [
        'POST',
        `/subnets/${NET}/allocate`,
        { type: 'reserved', interface_id: IF_A, dns_name: 'sw.example' },
      ],
      ['PATCH', `/ips/${IP_NEW}`, { ci_id: CI, role: 'secondary' }],
    ]);
    // Interfaces and addresses reload.
    await waitFor(() =>
      expect(calls.filter((c) => c.path === `/cis/${CI}/addresses`).length).toBeGreaterThan(before),
    );
  });

  it('does not offer the management role, which follows ci.management_ip', async () => {
    stubApi();
    renderSection();
    const dialog = await openAssign();

    const role = within(dialog).getByLabelText('Rolle');
    expect(within(role).queryByRole('option', { name: 'Management' })).not.toBeInTheDocument();
    expect(within(role).getByRole('option', { name: 'Sekundär' })).toBeInTheDocument();
    expect(within(dialog).getByText(/folgt der Management-IP des CI/)).toBeInTheDocument();
  });

  it('releases the address with DELETE when PATCH fails and shows the error', async () => {
    const calls = stubApi({
      onWrite: (method) =>
        method === 'PATCH'
          ? json(
              {
                type: 'about:blank',
                title: 'Unprocessable',
                status: 422,
                detail: 'role conflict',
                field: 'role',
              },
              422,
            )
          : undefined,
    });
    renderSection();
    const dialog = await openAssign();
    fireEvent.click(within(dialog).getByRole('button', { name: 'Zuweisen' }));

    expect(
      await within(dialog).findByText(
        'Die Adresse konnte nicht an das CI gebunden werden und wurde wieder freigegeben: role conflict',
      ),
    ).toHaveAttribute('role', 'alert');
    const writes = calls.filter((c) => c.method !== 'GET').map((c) => [c.method, c.path]);
    expect(writes).toEqual([
      ['POST', `/subnets/${NET}/allocate`],
      ['PATCH', `/ips/${IP_NEW}`],
      ['DELETE', `/ips/${IP_NEW}`],
    ]);
  });

  it('shows 409 subnet-exhausted at the subnet field', async () => {
    const calls = stubApi({
      onWrite: (method) =>
        method === 'POST'
          ? json(
              {
                type: 'urn:reticora:problem:subnet-exhausted',
                title: 'Subnet exhausted',
                status: 409,
              },
              409,
            )
          : undefined,
    });
    renderSection();
    const dialog = await openAssign();
    fireEvent.click(within(dialog).getByRole('button', { name: 'Zuweisen' }));

    const subnet = within(dialog).getByLabelText(/^Subnetz/, { selector: 'select' });
    await waitFor(() => expect(subnet).toHaveAccessibleDescription(/keine freie Adresse mehr/));
    expect(calls.some((c) => c.method === 'PATCH')).toBe(false);
  });

  it('offers no assignment without ipam:write', async () => {
    stubApi({ permissions: ['ipam:read'] });
    renderSection();

    expect(await screen.findByTestId(`ci-interface-${IF_A}`)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Adresse zuweisen' })).not.toBeInTheDocument();
  });

  it('sends no IPAM request without ipam:read', async () => {
    const calls = stubApi({ permissions: ['ci:read'] });
    const { container } = renderSection();

    await waitFor(() => expect(calls.some((c) => c.path === '/me/permissions')).toBe(true));
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(container).toBeEmptyDOMElement();
    expect(calls.filter((c) => c.path !== '/me/permissions' && c.path !== '/entitlements')).toEqual(
      [],
    );
  });

  it('sends no IPAM request when the plan lacks the feature ipam', async () => {
    const calls = stubApi({ ipam: false });
    const { container } = renderSection();

    await waitFor(() => expect(calls.some((c) => c.path === '/entitlements')).toBe(true));
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(container).toBeEmptyDOMElement();
    expect(calls.filter((c) => c.path !== '/me/permissions' && c.path !== '/entitlements')).toEqual(
      [],
    );
  });

  it('has no axe findings', async () => {
    stubApi();
    const { container } = renderSection();
    await screen.findByTestId(`ci-interface-${IF_A}`);
    const result = await axe.run(container, { rules: { 'color-contrast': { enabled: false } } });
    expect(result.violations).toEqual([]);

    const dialog = await openAssign();
    const dialogResult = await axe.run(dialog, {
      rules: { 'color-contrast': { enabled: false } },
    });
    expect(dialogResult.violations).toEqual([]);
  });
});
