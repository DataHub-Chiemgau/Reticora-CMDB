/**
 * WP-915 (IPM-19, UI-14): the page /ipam. The subnet tree loads its levels
 * lazily with parent_id (root, then the id of an opened subnet), each node
 * shows a usage bar with its text; the filters set the parameters of
 * WP-909, and a text, client or site filter (also the scope of the layout)
 * lists the matches flat; contains opens the chain and marks the narrowest subnet. With
 * ipam:write a section is created and its rights kept; without it no
 * write action is shown. Without the feature ipam the page is not a
 * visible navigation entry.
 *
 * Retest:
 *   cd frontend && npx vitest run src/pages/IpamPage.test.tsx
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../test/utils';
import { navFeatureFor } from '../hooks/useEntitlements';
import { NAV_GROUPS, navPageState } from '../components/shell/navigation';
import { useScopeStore } from '../stores/scope';
import { IpamPage } from './IpamPage';

type Json = Record<string, unknown>;

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const AT = '2026-01-01T00:00:00Z';
const ORG = 'org-1';
const DEFAULT = 'a0000000-0000-4000-8000-000000000001';
const LAB = 'a0000000-0000-4000-8000-000000000002';
const VRF = 'b0000000-0000-4000-8000-000000000001';
const ROOT = 'c0000000-0000-4000-8000-000000000001';
const CHILD = 'c0000000-0000-4000-8000-000000000002';
const TEAM = 'd0000000-0000-4000-8000-000000000001';
const PERM = 'e0000000-0000-4000-8000-000000000001';
const CLIENT = 'f0000000-0000-4000-8000-000000000001';
const SITE = 'f0000000-0000-4000-8000-000000000002';
const CLIENT_NET = 'c0000000-0000-4000-8000-000000000003';

function section(id: string, name: string, extra: Json = {}) {
  return {
    id,
    organization_id: ORG,
    name,
    strict_mode: false,
    show_vlan: true,
    show_vrf: true,
    is_default: id === DEFAULT,
    acl_enabled: false,
    vrf_ids: [],
    created_at: AT,
    updated_at: AT,
    ...extra,
  };
}

function subnet(id: string, cidr: string, extra: Json = {}) {
  return {
    id,
    organization_id: ORG,
    cidr,
    vrf_id: VRF,
    section_id: DEFAULT,
    name: `net ${cidr}`,
    is_pool: false,
    child_count: 0,
    created_at: AT,
    updated_at: AT,
    ...extra,
  };
}

const page = (data: unknown[]) => ({
  data,
  total: data.length,
  limit: 200,
  offset: 0,
  has_more: false,
});

interface MockOptions {
  permissions?: string[];
  /** The lab section has no team entries (E-256: locked until opened). */
  noTeams?: boolean;
  /** The lab section has entries only of teams the caller does not see. */
  hiddenTeams?: boolean;
  onPost?: (path: string, body: Json) => Response | undefined;
}

function mockApi({
  permissions = ['ipam:read', 'ipam:write'],
  noTeams = false,
  hiddenTeams = false,
  onPost,
}: MockOptions = {}) {
  return vi.fn().mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), 'http://localhost');
    const method = init?.method ?? 'GET';
    const path = url.pathname.replace(/^\/api\/v1/, '');
    if (method !== 'GET') {
      const body = init?.body ? (JSON.parse(String(init.body)) as Json) : {};
      const answer = onPost?.(path, body);
      if (answer) return answer;
      return json({ id: 'new', ...body }, method === 'POST' ? 201 : 200);
    }
    if (path === '/me/permissions') return json({ user_id: 'u', permissions });
    if (path === '/ipam-sections') {
      return json(page([section(DEFAULT, 'default'), section(LAB, 'lab', { acl_enabled: true })]));
    }
    if (path === `/ipam-sections/${LAB}/permissions` && noTeams) return json(page([]));
    if (path === `/ipam-sections/${LAB}/permissions` && hiddenTeams) {
      return json({ ...page([]), total: 2 });
    }
    if (path === `/ipam-sections/${LAB}/permissions`) {
      return json(
        page([
          {
            id: PERM,
            organization_id: ORG,
            section_id: LAB,
            team_id: TEAM,
            team_name: 'Netz-Team',
            level: 'read',
            created_at: AT,
          },
        ]),
      );
    }
    if (path === '/vrfs') {
      return json(
        page([{ id: VRF, organization_id: ORG, name: 'blue', is_default: true, created_at: AT }]),
      );
    }
    if (path === '/teams') {
      return json(page([{ id: TEAM, organization_id: ORG, name: 'Netz-Team' }]));
    }
    if (path === '/clients') {
      return json(page([{ id: CLIENT, organization_id: ORG, name: 'Kunde A', created_at: AT }]));
    }
    if (path === '/sites') {
      return json(
        page([{ id: SITE, organization_id: ORG, client_id: CLIENT, name: 'Werk 1', created_at: AT }]),
      );
    }
    if (path === '/subnets') {
      if (url.searchParams.get('contains')) {
        return json(
          page([subnet(CHILD, '10.0.1.0/24', { parent_id: ROOT }), subnet(ROOT, '10.0.0.0/16')]),
        );
      }
      if (url.searchParams.get('parent_id') === 'root') {
        return json(page([subnet(ROOT, '10.0.0.0/16', { child_count: 1, is_pool: true })]));
      }
      if (url.searchParams.get('parent_id') === ROOT) {
        return json(page([subnet(CHILD, '10.0.1.0/24', { parent_id: ROOT })]));
      }
      // The flat list of the matches (no parent_id): the server joins the
      // filters with AND; the matching subnets lie below the org-wide pool.
      if (!url.searchParams.has('parent_id')) {
        if (url.searchParams.get('q') === 'kunde-x') {
          return json(
            page([subnet(CHILD, '10.0.1.0/24', { parent_id: ROOT, name: 'kunde-x-lan' })]),
          );
        }
        if (url.searchParams.get('client_id') === CLIENT) {
          return json(
            page([subnet(CLIENT_NET, '10.0.2.0/24', { parent_id: ROOT, client_id: CLIENT })]),
          );
        }
      }
      return json(page([]));
    }
    const usage = path.match(/^\/subnets\/([^/]+)\/usage$/);
    if (usage) {
      return json({
        subnet_id: usage[1],
        cidr: '',
        is_pool: usage[1] === ROOT,
        total: 254,
        used: 127,
        covered: 256,
        percent: 50,
      });
    }
    return json({ title: 'Not Found', status: 404 }, 404);
  });
}

/** subnetCalls are the query strings of the GET /subnets requests. */
const subnetCalls = (fetchMock: ReturnType<typeof mockApi>) =>
  fetchMock.mock.calls
    .map((call) => new URL(String(call[0]), 'http://localhost'))
    .filter((url) => url.pathname.endsWith('/subnets'))
    .map((url) => url.searchParams);

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  useScopeStore.getState().reset();
});

describe('IpamPage', () => {
  it('loads the tree level by level with parent_id and shows the usage with text', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<IpamPage />);
    expect(await screen.findByText('10.0.0.0/16')).toBeInTheDocument();
    // Only the root level so far, of the default section, at most 200.
    const first = subnetCalls(fetchMock);
    expect(first.every((p) => p.get('parent_id') === 'root')).toBe(true);
    expect(first[0]?.get('section_id')).toBe(DEFAULT);
    expect(first[0]?.get('limit')).toBe('200');
    expect(screen.queryByText('10.0.1.0/24')).toBeNull();
    // The pool mark and the usage bar with its text.
    const root = screen.getByTestId(`subnet-node-${ROOT}`);
    expect(within(root).getByText('Pool')).toBeInTheDocument();
    const meter = await within(root).findByRole('meter');
    expect(meter).toHaveAttribute('aria-valuenow', '50');
    expect(meter.getAttribute('aria-valuetext')).toContain('256 von 254');
    expect(within(root).getByText(/VRF blue/)).toBeInTheDocument();
    // Opening the node loads its children.
    fireEvent.click(within(root).getByRole('button', { name: /aufklappen/ }));
    expect(await screen.findByText('10.0.1.0/24')).toBeInTheDocument();
    expect(subnetCalls(fetchMock).some((p) => p.get('parent_id') === ROOT)).toBe(true);
    const child = await within(screen.getByTestId(`subnet-node-${CHILD}`)).findByRole('meter');
    expect(child.getAttribute('aria-valuetext')).toBe('127 von 254 belegt (50.0 %)');
  });

  it('sets the filter parameters and opens the chain of contains', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<IpamPage />);
    await screen.findByText('10.0.0.0/16');
    const vrf = await screen.findByLabelText('VRF');
    await waitFor(() =>
      expect(within(vrf).getByRole('option', { name: 'blue' })).toBeInTheDocument(),
    );
    fireEvent.change(vrf, { target: { value: VRF } });
    fireEvent.change(screen.getByLabelText('Suche'), { target: { value: 'core' } });
    await waitFor(() =>
      expect(
        subnetCalls(fetchMock).some((p) => p.get('vrf_id') === VRF && p.get('q') === 'core'),
      ).toBe(true),
    );
    fireEvent.change(screen.getByLabelText('Suche'), { target: { value: '' } });
    // Client and site reach GET /subnets; a new client resets the site.
    const client = screen.getByLabelText('Kunde');
    await waitFor(() =>
      expect(within(client).getByRole('option', { name: 'Kunde A' })).toBeInTheDocument(),
    );
    fireEvent.change(client, { target: { value: CLIENT } });
    const site = screen.getByLabelText('Standort');
    await waitFor(() =>
      expect(within(site).getByRole('option', { name: 'Werk 1' })).toBeInTheDocument(),
    );
    fireEvent.change(site, { target: { value: SITE } });
    await waitFor(() =>
      expect(
        subnetCalls(fetchMock).some(
          (p) => p.get('client_id') === CLIENT && p.get('site_id') === SITE,
        ),
      ).toBe(true),
    );
    fireEvent.change(client, { target: { value: '' } });
    expect(screen.getByLabelText('Standort')).toHaveValue('');
    fireEvent.change(screen.getByLabelText('Enthält Adresse'), { target: { value: '10.0.1.7' } });
    fireEvent.click(screen.getByRole('button', { name: 'Finden' }));
    await waitFor(() =>
      expect(subnetCalls(fetchMock).some((p) => p.get('contains') === '10.0.1.7')).toBe(true),
    );
    // The chain is opened down to the narrowest subnet, which is marked.
    const narrowest = await screen.findByTestId(`subnet-node-${CHILD}`);
    expect(narrowest).toHaveAttribute('aria-current', 'true');
    expect(within(narrowest).getByText('Engstes Subnetz')).toBeInTheDocument();
  });

  it('lists the matches of a text filter flat, also below a parent that does not match', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<IpamPage />);
    await screen.findByText('10.0.0.0/16');
    fireEvent.change(screen.getByLabelText('Suche'), { target: { value: 'kunde-x' } });
    expect(await screen.findByText('kunde-x-lan')).toBeInTheDocument();
    expect(screen.queryByText('10.0.0.0/16')).toBeNull();
    expect(screen.getByText(/Treffer der Filter/)).toBeInTheDocument();
    const flat = subnetCalls(fetchMock).filter((p) => p.get('q') === 'kunde-x');
    expect(flat.length).toBeGreaterThan(0);
    expect(flat.every((p) => !p.has('parent_id'))).toBe(true);
  });

  it('lists the client subnets below an org-wide pool for the scope of the layout', async () => {
    useScopeStore.getState().setClient(CLIENT);
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<IpamPage />);
    expect(await screen.findByText('10.0.2.0/24')).toBeInTheDocument();
    expect(screen.queryByText('Diese Section enthält keine Subnetze.')).toBeNull();
    expect(
      subnetCalls(fetchMock).some((p) => p.get('client_id') === CLIENT && !p.has('parent_id')),
    ).toBe(true);
  });

  it('creates a section and keeps its rights with ipam:write', async () => {
    const posts: Array<[string, Json]> = [];
    vi.stubGlobal(
      'fetch',
      mockApi({
        onPost: (path, body) => {
          posts.push([path, body]);
          return undefined;
        },
      }),
    );
    renderWithProviders(<IpamPage />);
    fireEvent.click(await screen.findByRole('button', { name: 'Neue Section' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText(/^Name/), { target: { value: 'dmz' } });
    fireEvent.click(within(dialog).getByLabelText('Strikter Modus'));
    fireEvent.click(within(dialog).getByRole('button', { name: 'Anlegen' }));
    await waitFor(() => expect(posts.some(([p]) => p === '/ipam-sections')).toBe(true));
    expect(posts.find(([p]) => p === '/ipam-sections')?.[1]).toMatchObject({
      name: 'dmz',
      strict_mode: true,
      show_vlan: true,
    });

    // The rights of the section lab: change the level, remove, add a team.
    fireEvent.change(await screen.findByLabelText('Section'), { target: { value: LAB } });
    fireEvent.click(screen.getByRole('button', { name: 'Section bearbeiten' }));
    const edit = await screen.findByRole('dialog');
    fireEvent.click(within(edit).getByRole('tab', { name: 'Rechte' }));
    const level = await within(edit).findByLabelText('Stufe von Netz-Team');
    fireEvent.change(level, { target: { value: 'write' } });
    await waitFor(() =>
      expect(
        posts.some(
          ([p, b]) => p === `/ipam-sections/${LAB}/permissions/${PERM}` && b.level === 'write',
        ),
      ).toBe(true),
    );
    fireEvent.click(within(edit).getByRole('button', { name: 'Netz-Team entfernen' }));
    await waitFor(() =>
      expect(posts.some(([p]) => p === `/ipam-sections/${LAB}/permissions/${PERM}`)).toBe(true),
    );
    fireEvent.change(within(edit).getByRole('combobox', { name: /^Team/ }), {
      target: { value: 'Netz' },
    });
    fireEvent.click(await within(edit).findByRole('option', { name: /Netz-Team/ }));
    fireEvent.click(within(edit).getByRole('button', { name: 'Hinzufügen' }));
    await waitFor(() =>
      expect(
        posts.find(([p, b]) => p === `/ipam-sections/${LAB}/permissions` && b.team_id === TEAM),
      ).toBeTruthy(),
    );
  });

  it('says why deleting a section with subnets fails', async () => {
    vi.stubGlobal(
      'fetch',
      mockApi({
        onPost: (path) =>
          path === `/ipam-sections/${LAB}`
            ? json({ title: 'Conflict', status: 409, field: 'id', detail: 'in use' }, 409)
            : undefined,
      }),
    );
    renderWithProviders(<IpamPage />);
    // The sections are loaded before the lab section is chosen.
    await screen.findByText('10.0.0.0/16');
    fireEvent.change(screen.getByLabelText('Section'), { target: { value: LAB } });
    fireEvent.click(screen.getByRole('button', { name: 'Section bearbeiten' }));
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByRole('heading')).toHaveTextContent('lab');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Löschen' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('enthält noch Subnetze');
  });

  it('opens a locked section without teams only explicitly (E-256)', async () => {
    const posts: Array<[string, Json]> = [];
    vi.stubGlobal(
      'fetch',
      mockApi({
        noTeams: true,
        onPost: (path, body) => {
          posts.push([path, body]);
          return undefined;
        },
      }),
    );
    renderWithProviders(<IpamPage />);
    await screen.findByText('10.0.0.0/16');
    fireEvent.change(screen.getByLabelText('Section'), { target: { value: LAB } });
    fireEvent.click(screen.getByRole('button', { name: 'Section bearbeiten' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('tab', { name: 'Rechte' }));
    expect(await within(dialog).findByText(/bleibt gesperrt/)).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole('button', { name: 'Section freigeben' }));
    await waitFor(() =>
      expect(posts.find(([p]) => p === `/ipam-sections/${LAB}`)?.[1]).toEqual({
        acl_enabled: false,
      }),
    );
  });

  it('explains 403 when a scoped principal changes the rights', async () => {
    vi.stubGlobal(
      'fetch',
      mockApi({
        onPost: (path) =>
          path.startsWith(`/ipam-sections/${LAB}/permissions`)
            ? json({ title: 'Forbidden', status: 403 }, 403)
            : undefined,
      }),
    );
    renderWithProviders(<IpamPage />);
    await screen.findByText('10.0.0.0/16');
    fireEvent.change(screen.getByLabelText('Section'), { target: { value: LAB } });
    fireEvent.click(screen.getByRole('button', { name: 'Section bearbeiten' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('tab', { name: 'Rechte' }));
    fireEvent.change(await within(dialog).findByLabelText('Stufe von Netz-Team'), {
      target: { value: 'write' },
    });
    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      'Rechte dürfen nur Benutzer ohne Kunden- und Standortbeschränkung',
    );
  });

  it('offers no release while entries of hidden teams exist', async () => {
    const posts: Array<[string, Json]> = [];
    vi.stubGlobal(
      'fetch',
      mockApi({
        hiddenTeams: true,
        onPost: (path, body) => {
          posts.push([path, body]);
          return path === `/ipam-sections/${LAB}`
            ? json({ title: 'Conflict', status: 409, field: 'acl_enabled' }, 409)
            : undefined;
        },
      }),
    );
    renderWithProviders(<IpamPage />);
    await screen.findByText('10.0.0.0/16');
    fireEvent.change(screen.getByLabelText('Section'), { target: { value: LAB } });
    fireEvent.click(screen.getByRole('button', { name: 'Section bearbeiten' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('tab', { name: 'Rechte' }));
    expect(await within(dialog).findByText(/2 Einträge von Teams/)).toBeInTheDocument();
    expect(within(dialog).queryByText('Keine Teams eingetragen.')).toBeNull();
    expect(within(dialog).queryByRole('button', { name: 'Section freigeben' })).toBeNull();
  });

  it('says why a release with entries meanwhile fails (409 acl_enabled)', async () => {
    vi.stubGlobal(
      'fetch',
      mockApi({
        noTeams: true,
        onPost: (path) =>
          path === `/ipam-sections/${LAB}`
            ? json({ title: 'Conflict', status: 409, field: 'acl_enabled' }, 409)
            : undefined,
      }),
    );
    renderWithProviders(<IpamPage />);
    await screen.findByText('10.0.0.0/16');
    fireEvent.change(screen.getByLabelText('Section'), { target: { value: LAB } });
    fireEvent.click(screen.getByRole('button', { name: 'Section bearbeiten' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('tab', { name: 'Rechte' }));
    fireEvent.click(await within(dialog).findByRole('button', { name: 'Section freigeben' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('noch Einträge');
  });

  it('shows no write action without ipam:write', async () => {
    vi.stubGlobal('fetch', mockApi({ permissions: ['ipam:read'] }));
    renderWithProviders(<IpamPage />);
    await screen.findByText('10.0.0.0/16');
    expect(screen.queryByRole('button', { name: 'Neue Section' })).toBeNull();
    fireEvent.change(screen.getByLabelText('Section'), { target: { value: LAB } });
    fireEvent.click(await screen.findByRole('button', { name: 'Section anzeigen' }));
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).queryByRole('button', { name: 'Speichern' })).toBeNull();
    expect(within(dialog).queryByRole('button', { name: 'Löschen' })).toBeNull();
    fireEvent.click(within(dialog).getByRole('tab', { name: 'Rechte' }));
    expect(await within(dialog).findByText('Netz-Team')).toBeInTheDocument();
    expect(within(dialog).getByText('Lesen')).toBeInTheDocument();
    expect(within(dialog).queryByRole('button', { name: 'Hinzufügen' })).toBeNull();
    expect(within(dialog).queryByRole('button', { name: 'Netz-Team entfernen' })).toBeNull();
  });

  it('is no visible navigation entry without the feature ipam', () => {
    expect(navFeatureFor.ipam).toBe('ipam');
    expect(
      NAV_GROUPS.find((g) => g.id === 'infrastructure')?.items.some((i) => i.page === 'ipam'),
    ).toBe(true);
    const access = (features: string[]) => ({
      isEntitled: (p: string) => !navFeatureFor[p] || features.includes(navFeatureFor[p] ?? ''),
      isPageEnabled: () => true,
      isPermitted: () => true,
    });
    expect(navPageState('ipam', access(['ipam']))).toBe('visible');
    expect(navPageState('ipam', access([]))).not.toBe('visible');
  });
});
