/**
 * WP-1065 (IPM-11, IPM-12, UI-14): the tab "Scan" of the subnet detail and
 * the view "PTR-Abweichungen". Without the feature monitoring the tab shows
 * the hint and no form; saving sends the configuration with the collector
 * group; a 422 appears at its field; discover_new_hosts is disabled for /21
 * and IPv6; "Übernehmen" makes a found address static and pinned; the PTR
 * list filters by status; without ipam:write there is no write action.
 *
 * Retest:
 *   cd frontend && npx vitest run src/components/ipam/ScanTab.test.tsx
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../../test/utils';
import i18n from '../../i18n';
import { useAuthStore } from '../../auth/authStore';
import type { Subnet } from '../../api/cmdb';
import { ScanTab } from './ScanTab';
import { PtrMismatchList } from './PtrMismatchList';

type Json = Record<string, unknown>;

const AT = '2026-01-01T00:00:00Z';
const ORG = 'a0000000-0000-4000-8000-0000000000aa';
const NET = 'a0000000-0000-4000-8000-000000000001';
const CLIENT = 'c0000000-0000-4000-8000-000000000001';
const OTHER = 'c0000000-0000-4000-8000-000000000002';
const GROUP = 'f0000000-0000-4000-8000-000000000001';
const GROUP_ORG = 'f0000000-0000-4000-8000-000000000002';
const GROUP_OTHER = 'f0000000-0000-4000-8000-000000000003';
const COLLECTOR = 'e0000000-0000-4000-8000-000000000001';
const HOST = 'b0000000-0000-4000-8000-000000000001';
const SECTION = '90000000-0000-4000-8000-000000000001';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

const page = (data: unknown[], extra: Json = {}) => ({
  data,
  total: data.length,
  limit: 50,
  offset: 0,
  has_more: false,
  ...extra,
});

const subnet = (cidr: string, extra: Json = {}): Subnet =>
  ({
    id: NET,
    organization_id: ORG,
    client_id: CLIENT,
    cidr,
    vrf_id: 'v',
    created_at: AT,
    updated_at: AT,
    ...extra,
  }) as unknown as Subnet;

const group = (id: string, name: string, clientId?: string) => ({
  id,
  organization_id: ORG,
  ...(clientId ? { client_id: clientId } : {}),
  name,
  mode: 'failover',
  liveness_timeout_s: 60,
  status: 'reachable',
  status_changed_at: AT,
  members: [
    {
      collector_id: COLLECTOR,
      group_id: id,
      priority: 1,
      weight: 1,
      member_status: 'healthy',
      config_version: 1,
      assigned_probes: 3,
    },
  ],
  created_at: AT,
  updated_at: AT,
});

const SCAN = {
  subnet_id: NET,
  scan_enabled: false,
  scan_interval_s: null,
  discover_new_hosts: false,
  offline_after_s: 600,
  scan_synced_at: null,
  members: [],
  targets: 12,
  owned: 10,
  reused: 2,
  last_applied_at: '2026-01-02T08:00:00Z',
  target_states: { active: 8, offline: 2, unknown: 1, none: 1 },
  new_hosts: 1,
};

interface Options {
  permissions?: string[];
  monitoring?: boolean;
  onWrite?: (method: string, path: string, body: Json) => Response | undefined;
  scan?: Json;
  groupsStatus?: number;
}

function stubApi({
  permissions = ['ipam:read', 'ipam:write'],
  monitoring = true,
  onWrite,
  scan = {},
  groupsStatus = 200,
}: Options = {}) {
  const calls: { method: string; path: string; params: URLSearchParams; body?: Json }[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      const method = init?.method ?? 'GET';
      const path = url.pathname.replace(/^\/api\/v1/, '');
      const body = init?.body ? (JSON.parse(String(init.body)) as Json) : undefined;
      calls.push({ method, path, params: url.searchParams, body });
      if (method !== 'GET') {
        const custom = onWrite?.(method, path, body ?? {});
        if (custom) return custom;
        if (path === `/subnets/${NET}/scan`) return json({ ...SCAN, ...body });
        return json({ id: HOST, ...body });
      }
      if (path === '/me/permissions') return json({ user_id: 'u', permissions });
      if (path === '/entitlements') {
        return json(
          page([
            { feature_key: 'ipam', enabled: true },
            { feature_key: 'monitoring', enabled: monitoring },
          ]),
        );
      }
      if (path === `/subnets/${NET}/scan`) return json({ ...SCAN, ...scan });
      if (path === '/collector-groups') {
        if (groupsStatus !== 200) {
          return json(
            { type: 'about:blank', title: 'Forbidden', status: groupsStatus },
            groupsStatus,
          );
        }
        return json(
          page([
            group(GROUP, 'Nord-Gruppe', CLIENT),
            group(GROUP_ORG, 'Zentrale'),
            group(GROUP_OTHER, 'Fremd', OTHER),
          ]),
        );
      }
      if (path === '/collectors') {
        return json(page([{ id: COLLECTOR, organization_id: ORG, name: 'collector-nord' }]));
      }
      if (path === '/ips') {
        return json(
          page([
            {
              id: HOST,
              organization_id: ORG,
              subnet_id: NET,
              address: '10.9.0.77',
              vrf_id: 'v',
              type: 'discovered',
              origin: 'scan',
              pinned: false,
              state: 'active',
              last_seen_at: AT,
              created_at: AT,
              updated_at: AT,
            },
          ]),
        );
      }
      if (path === '/ipam/ptr-mismatches') {
        const status = url.searchParams.get('status');
        return json(
          page(
            status === 'missing'
              ? [
                  {
                    id: 'p2',
                    address: '10.9.0.9',
                    dns_name: 'gone.example',
                    ptr_status: 'missing',
                    pinned: true,
                  },
                ]
              : [
                  {
                    id: 'p1',
                    address: '10.9.0.5',
                    subnet_id: NET,
                    dns_name: 'sw1.example',
                    ptr_name: 'switch1.example',
                    ptr_status: 'mismatch',
                    ptr_checked_at: AT,
                    pinned: false,
                  },
                ],
          ),
        );
      }
      return json(page([]));
    }),
  );
  return calls;
}

beforeEach(async () => {
  await i18n.changeLanguage('de-DE');
  useAuthStore.setState({
    token: 't',
    expiresAt: new Date(Date.now() + 3600_000).toISOString(),
    user: { sub: 'u' },
  });
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  useAuthStore.setState({ token: null, expiresAt: null, user: null });
});

describe('ScanTab', () => {
  it('gesperrt ohne monitoring', async () => {
    const calls = stubApi({ monitoring: false });
    renderWithProviders(<ScanTab subnet={subnet('10.9.0.0/24')} canWrite />);

    expect(
      await screen.findByText('„Subnetz-Scan“ gehört nicht zu Ihrem Paket'),
    ).toBeInTheDocument();
    expect(screen.queryByRole('form', { name: 'Subnetz-Scan' })).not.toBeInTheDocument();
    expect(screen.queryByLabelText('Scan aktiv')).not.toBeInTheDocument();
    expect(calls.some((c) => c.path.endsWith('/scan'))).toBe(false);
  });

  it('speichert Konfiguration mit Collector-Gruppe', async () => {
    const calls = stubApi();
    renderWithProviders(<ScanTab subnet={subnet('10.9.0.0/24')} canWrite />);

    fireEvent.click(await screen.findByLabelText('Scan aktiv'));
    const select = screen.getByLabelText('Collector-Gruppe');
    await waitFor(() =>
      expect(within(select).getByRole('option', { name: 'Nord-Gruppe' })).toBeInTheDocument(),
    );
    // The groups of the subnet's client and the org-wide ones only.
    expect(
      within(select)
        .getAllByRole('option')
        .map((o) => o.textContent),
    ).toEqual(['Keine Gruppe', 'Nord-Gruppe', 'Zentrale (organisationsweit)']);
    fireEvent.change(select, { target: { value: GROUP } });
    expect(await screen.findByText('collector-nord')).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Intervall in Sekunden'), { target: { value: '60' } });
    fireEvent.click(screen.getByLabelText(/Neue Hosts erkennen/));
    fireEvent.click(screen.getByRole('button', { name: 'Scan-Einstellungen speichern' }));

    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
    const patch = calls.find((c) => c.method === 'PATCH');
    expect(patch?.path).toBe(`/subnets/${NET}/scan`);
    expect(patch?.body).toEqual({
      scan_enabled: true,
      collector_group_id: GROUP,
      scan_interval_s: 60,
      offline_after_s: 600,
      discover_new_hosts: true,
    });
    expect(
      await screen.findByText('Die Scan-Einstellungen wurden gespeichert.'),
    ).toBeInTheDocument();
  });

  it('bietet für ein Subnetz ohne Kunden nur organisationsweite Gruppen', async () => {
    stubApi();
    renderWithProviders(<ScanTab subnet={subnet('10.9.0.0/24', { client_id: null })} canWrite />);

    const select = await screen.findByLabelText('Collector-Gruppe');
    await waitFor(() =>
      expect(
        within(select).getByRole('option', { name: 'Zentrale (organisationsweit)' }),
      ).toBeInTheDocument(),
    );
    expect(
      within(select)
        .getAllByRole('option')
        .map((o) => o.textContent),
    ).toEqual(['Keine Gruppe', 'Zentrale (organisationsweit)']);
  });

  it('zeigt die gespeicherte Gruppe, wenn die Gruppen nicht lesbar sind', async () => {
    const calls = stubApi({ scan: { collector_group_id: GROUP }, groupsStatus: 403 });
    renderWithProviders(<ScanTab subnet={subnet('10.9.0.0/24')} canWrite />);

    expect(await screen.findByTestId('scan-groups-unavailable')).toHaveTextContent(
      'Die Collector-Gruppen sind nicht ladbar',
    );
    const select = screen.getByLabelText('Collector-Gruppe') as HTMLSelectElement;
    expect(select).toHaveValue(GROUP);
    expect(select.selectedOptions[0]?.textContent).not.toBe('Keine Gruppe');
    // "Keine Gruppe" can be chosen on purpose and is what saving sends.
    fireEvent.change(select, { target: { value: '' } });
    fireEvent.click(screen.getByRole('button', { name: 'Scan-Einstellungen speichern' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
    expect(calls.find((c) => c.method === 'PATCH')?.body).toMatchObject({
      collector_group_id: null,
    });
  });

  it('zeigt 422 am Feld', async () => {
    stubApi({
      onWrite: () =>
        json(
          {
            type: 'about:blank',
            title: 'Unprocessable Entity',
            status: 422,
            detail: 'offline_after_s must be at least the effective interval',
            field: 'offline_after_s',
          },
          422,
        ),
    });
    renderWithProviders(<ScanTab subnet={subnet('10.9.0.0/24')} canWrite />);

    fireEvent.change(await screen.findByLabelText('Offline nach Sekunden'), {
      target: { value: '60' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Scan-Einstellungen speichern' }));
    const field = screen.getByLabelText('Offline nach Sekunden');
    await waitFor(() => expect(field).toHaveAttribute('aria-invalid', 'true'));
    expect(
      screen.getByText('offline_after_s must be at least the effective interval'),
    ).toBeInTheDocument();
  });

  it('discover_new_hosts deaktiviert bei /21 und IPv6', async () => {
    stubApi();
    const { unmount } = renderWithProviders(<ScanTab subnet={subnet('10.8.0.0/21')} canWrite />);
    expect(await screen.findByLabelText(/Neue Hosts erkennen/)).toBeDisabled();
    expect(screen.getByText(/Nur für IPv4-Netze bis \/22 möglich/)).toBeInTheDocument();
    unmount();

    renderWithProviders(<ScanTab subnet={subnet('2001:db8::/64')} canWrite />);
    expect(await screen.findByLabelText(/Neue Hosts erkennen/)).toBeDisabled();
    expect(screen.getByText(/Bei IPv6-Netzen nicht möglich/)).toBeInTheDocument();
  });

  it('übernimmt neu gefundene Adresse', async () => {
    const calls = stubApi();
    renderWithProviders(<ScanTab subnet={subnet('10.9.0.0/24')} canWrite />);

    expect(await screen.findByTestId('scan-last-run')).toHaveTextContent('Zuletzt angewendet');
    const row = await screen.findByTestId(`scan-host-${HOST}`);
    expect(within(row).getByText('10.9.0.77')).toBeInTheDocument();
    const list = calls.find((c) => c.path === '/ips');
    expect(list?.params.get('origin')).toBe('scan');
    expect(list?.params.get('type')).toBe('discovered');
    expect(list?.params.get('subnet_id')).toBe(NET);

    fireEvent.click(within(row).getByRole('button', { name: '10.9.0.77 übernehmen' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
    const patch = calls.find((c) => c.method === 'PATCH');
    expect(patch?.path).toBe(`/ips/${HOST}`);
    expect(patch?.body).toEqual({ type: 'static', pinned: true });
  });

  it('ohne ipam:write keine Schreibaktion', async () => {
    const calls = stubApi({ permissions: ['ipam:read'] });
    renderWithProviders(<ScanTab subnet={subnet('10.9.0.0/24')} canWrite={false} />);

    expect(await screen.findByLabelText('Scan aktiv')).toBeDisabled();
    expect(screen.getByLabelText('Collector-Gruppe')).toBeDisabled();
    expect(screen.queryByRole('button', { name: 'Scan-Einstellungen speichern' })).toBeNull();
    await screen.findByTestId(`scan-host-${HOST}`);
    expect(screen.queryByRole('button', { name: /übernehmen/ })).toBeNull();
    expect(calls.filter((c) => c.method !== 'GET')).toEqual([]);
  });
});

describe('PtrMismatchList', () => {
  const SECTIONS = [
    {
      id: SECTION,
      organization_id: ORG,
      name: 'default',
      strict_mode: false,
      show_vlan: true,
      show_vrf: true,
      is_default: true,
      acl_enabled: false,
      ptr_mode: 'report' as const,
      vrf_ids: [],
      created_at: AT,
      updated_at: AT,
    },
  ];

  it('PTR-Abweichungen filtern nach Status', async () => {
    const calls = stubApi();
    renderWithProviders(<PtrMismatchList sections={SECTIONS} />);

    const row = await screen.findByTestId('ptr-row-p1');
    expect(within(row).getByText('sw1.example')).toBeInTheDocument();
    expect(within(row).getByText('switch1.example')).toBeInTheDocument();
    expect(within(row).getByText('nein')).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText('Status'), { target: { value: 'missing' } });
    const missing = await screen.findByTestId('ptr-row-p2');
    expect(within(missing).getByText('PTR fehlt')).toBeInTheDocument();
    expect(screen.queryByTestId('ptr-row-p1')).toBeNull();
    const statuses = calls
      .filter((c) => c.path === '/ipam/ptr-mismatches')
      .map((c) => c.params.get('status'));
    expect(statuses).toEqual(['mismatch', 'missing']);
  });

  it('loads further pages over the cursor', async () => {
    let first = true;
    const calls = stubApi();
    const base = globalThis.fetch;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = new URL(String(input), 'http://localhost');
        if (url.pathname === '/api/v1/ipam/ptr-mismatches' && first) {
          first = false;
          calls.push({ method: 'GET', path: '/ipam/ptr-mismatches', params: url.searchParams });
          return json(
            page(
              [
                {
                  id: 'p0',
                  address: '10.9.0.1',
                  dns_name: 'a.example',
                  ptr_name: 'b.example',
                  ptr_status: 'mismatch',
                  pinned: true,
                },
              ],
              { has_more: true, next_cursor: 'c2' },
            ),
          );
        }
        return base(input, init);
      }),
    );
    renderWithProviders(<PtrMismatchList sections={SECTIONS} />);

    await screen.findByTestId('ptr-row-p0');
    fireEvent.click(screen.getByRole('button', { name: 'Weitere laden' }));
    expect(await screen.findByTestId('ptr-row-p1')).toBeInTheDocument();
    expect(
      calls.filter((c) => c.path === '/ipam/ptr-mismatches').map((c) => c.params.get('cursor')),
    ).toEqual([null, 'c2']);
  });

  it('gesperrt ohne monitoring', async () => {
    const calls = stubApi({ monitoring: false });
    renderWithProviders(<PtrMismatchList sections={SECTIONS} />);
    expect(await screen.findByText(/PTR-Abweichungen/)).toBeInTheDocument();
    await waitFor(() => expect(calls.some((c) => c.path === '/entitlements')).toBe(true));
    expect(calls.some((c) => c.path === '/ipam/ptr-mismatches')).toBe(false);
  });
});
