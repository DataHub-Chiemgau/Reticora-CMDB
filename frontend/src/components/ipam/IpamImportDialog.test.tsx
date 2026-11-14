/**
 * WP-922 (IPM-09, UI-14): the dialog Import/Export of the IPAM page. A
 * missing phpIPAM profile is created once and reused afterwards; the import
 * leads to /imports?profile=<id>; the export takes the filters of the page
 * into filters.ipam and sends format csv; without ipam:write there is no
 * import tab; a 403 appears in the dialog; axe finds nothing.
 *
 * Retest:
 *   cd frontend && npx vitest run src/components/ipam/IpamImportDialog.test.tsx
 */
import axe from 'axe-core';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { Route, Routes, useLocation } from 'react-router';
import { renderWithProviders } from '../../test/utils';
import i18n from '../../i18n';
import {
  IpamImportDialog,
  exportFilters,
  phpIPAMProfile,
  type IpamPageFilters,
} from './IpamImportDialog';

type Json = Record<string, unknown>;

const AT = '2026-11-01T08:00:00Z';
const SECTION = 'a0000000-0000-4000-8000-000000000001';
const VRF = 'b0000000-0000-4000-8000-000000000001';
const CLIENT = 'c0000000-0000-4000-8000-000000000001';
const SITE = 'd0000000-0000-4000-8000-000000000001';

const ALL = ['import:run', 'import:manage', 'ipam:write', 'ipam:read', 'export:run'];

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

const page = (data: unknown[]) => ({
  data,
  total: data.length,
  limit: 200,
  offset: 0,
  has_more: false,
});

function stubApi({
  permissions = ALL,
  profiles: initial = [],
  onWrite,
  pageSize = 200,
}: {
  permissions?: string[];
  profiles?: Json[];
  onWrite?: (method: string, path: string, body: Json) => Response | undefined;
  pageSize?: number;
} = {}) {
  const profiles = [...initial];
  const calls: { method: string; path: string; body?: Json }[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      const method = init?.method ?? 'GET';
      const path = url.pathname.replace(/^\/api\/v1/, '');
      const body = init?.body ? (JSON.parse(String(init.body)) as Json) : undefined;
      calls.push({ method, path, body });
      const answer = method !== 'GET' ? onWrite?.(method, path, body ?? {}) : undefined;
      if (answer) return answer;
      if (path === '/me/permissions') return json({ user_id: 'u', permissions });
      if (path === '/import-profiles' && method === 'GET') {
        const from = Number(url.searchParams.get('cursor') ?? '0');
        const slice = profiles.slice(from, from + pageSize);
        const more = from + pageSize < profiles.length;
        return json({
          ...page(slice),
          total: profiles.length,
          has_more: more,
          ...(more ? { next_cursor: String(from + pageSize) } : {}),
        });
      }
      if (path === '/import-profiles' && method === 'POST') {
        const created = {
          ...body,
          id: `prof-${profiles.length + 1}`,
          organization_id: 'org-1',
          created_by: 'u',
          created_at: AT,
          updated_at: AT,
        };
        profiles.push(created);
        return json(created, 201);
      }
      if (path === '/exports' && method === 'POST') {
        return json(
          {
            id: 'job-9',
            organization_id: 'org-1',
            format: 'csv',
            status: 'pending',
            filters: body?.filters ?? {},
            created_at: AT,
            updated_at: AT,
          },
          202,
        );
      }
      return json({ status: 404, title: 'Not Found' }, 404);
    }),
  );
  return calls;
}

const FILTERS: IpamPageFilters = {
  section_id: SECTION,
  vrf_id: VRF,
  client_id: CLIENT,
  site_id: SITE,
  q: 'office',
  contains: '10.0.0.1',
};

function Where() {
  const location = useLocation();
  return <p data-testid="where">{location.pathname + location.search}</p>;
}

function renderDialog(filters: IpamPageFilters = FILTERS) {
  return renderWithProviders(
    <Routes>
      <Route
        path="/ipam"
        element={
          <>
            <IpamImportDialog
              open
              onOpenChange={() => {}}
              filters={filters}
              labels={{
                section_id: 'default',
                vrf_id: 'global',
                client_id: 'ACME',
                site_id: 'RZ Nord',
              }}
            />
            <Where />
          </>
        }
      />
      <Route path="/imports" element={<Where />} />
    </Routes>,
    { route: '/ipam' },
  );
}

beforeEach(async () => {
  await i18n.changeLanguage('de-DE');
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('phpIPAMProfile and exportFilters', () => {
  it('maps every phpIPAM column to the target of the same name with the identity of WP-920', () => {
    const subnet = phpIPAMProfile('subnet');
    expect(subnet).toMatchObject({
      name: 'phpIPAM Subnetze',
      entity: 'subnet',
      format: 'csv',
      mode: 'both',
      identity_keys: ['vrf', 'cidr'],
    });
    expect(subnet.mapping.columns.mask).toEqual({ target: 'mask', transforms: [] });
    expect(Object.keys(subnet.mapping.columns)).toContain('section');
    expect(phpIPAMProfile('ip_address')).toMatchObject({
      name: 'phpIPAM Adressen',
      identity_keys: ['vrf', 'ip_addr'],
    });
  });

  it('sends the subnet filters for subnets and only client and text for addresses', () => {
    expect(exportFilters('subnet', FILTERS)).toEqual({
      section_id: SECTION,
      vrf_id: VRF,
      client_id: CLIENT,
      site_id: SITE,
      q: 'office',
      contains: '10.0.0.1',
    });
    expect(exportFilters('ip_address', FILTERS)).toEqual({ client_id: CLIENT, q: 'office' });
  });
});

describe('IpamImportDialog', () => {
  it('creates the missing phpIPAM profile once and leads to /imports?profile=<id>', async () => {
    const calls = stubApi();
    const first = renderDialog();

    expect(
      await screen.findByText(
        'Das Profil „phpIPAM Subnetze“ wird mit den phpIPAM-Spalten angelegt.',
      ),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Zum Importassistenten' }));
    await waitFor(() =>
      expect(screen.getByTestId('where')).toHaveTextContent('/imports?profile=prof-1'),
    );
    const posts = calls.filter((c) => c.method === 'POST' && c.path === '/import-profiles');
    expect(posts).toHaveLength(1);
    expect(posts[0]?.body).toMatchObject({
      name: 'phpIPAM Subnetze',
      entity: 'subnet',
      format: 'csv',
      mode: 'both',
    });
    first.unmount();

    // A second visit finds the stored profile and creates none.
    renderDialog();
    expect(
      await screen.findByText('Das Profil „phpIPAM Subnetze“ wird verwendet.'),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Zum Importassistenten' }));
    await waitFor(() =>
      expect(screen.getByTestId('where')).toHaveTextContent('/imports?profile=prof-1'),
    );
    expect(calls.filter((c) => c.method === 'POST' && c.path === '/import-profiles')).toHaveLength(
      1,
    );
  });

  it('finds the phpIPAM profile beyond the first page of profiles', async () => {
    const other = (i: number) => ({
      ...phpIPAMProfile('subnet'),
      name: `Profil ${String(i).padStart(3, '0')}`,
      id: `other-${i}`,
      organization_id: 'org-1',
      created_by: 'u',
      created_at: AT,
      updated_at: AT,
    });
    const calls = stubApi({
      pageSize: 2,
      profiles: [
        other(1),
        other(2),
        other(3),
        { ...other(4), ...phpIPAMProfile('subnet'), id: 'prof-late' },
      ],
    });
    renderDialog();

    expect(
      await screen.findByText('Das Profil „phpIPAM Subnetze“ wird verwendet.'),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Zum Importassistenten' }));
    await waitFor(() =>
      expect(screen.getByTestId('where')).toHaveTextContent('/imports?profile=prof-late'),
    );
    expect(calls.some((c) => c.method === 'POST')).toBe(false);
  });

  it('says the name is taken when a profile of another entity carries it', async () => {
    const calls = stubApi({
      profiles: [
        {
          ...phpIPAMProfile('subnet'),
          entity: 'ci',
          id: 'prof-ci',
          organization_id: 'org-1',
          created_by: 'u',
          created_at: AT,
          updated_at: AT,
        },
      ],
    });
    renderDialog();

    expect(
      await screen.findByText(/Der Name „phpIPAM Subnetze“ gehört bereits einem Importprofil/),
    ).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Zum Importassistenten' })).toBeDisabled();
    expect(calls.some((c) => c.method === 'POST')).toBe(false);
  });

  it('reuses a profile created in the meantime when POST answers 409', async () => {
    let raced = false;
    const calls = stubApi({
      onWrite: (method, path) => {
        if (method !== 'POST' || path !== '/import-profiles') return undefined;
        raced = true;
        return json({ status: 409, title: 'Conflict' }, 409);
      },
    });
    // After the 409 the list carries the profile another session created.
    const fetchMock = globalThis.fetch as ReturnType<typeof vi.fn>;
    const inner = fetchMock.getMockImplementation() as (
      input: RequestInfo | URL,
      init?: RequestInit,
    ) => Promise<Response>;
    fetchMock.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      if (raced && url.pathname.endsWith('/import-profiles') && (init?.method ?? 'GET') === 'GET') {
        return json(
          page([
            {
              ...phpIPAMProfile('subnet'),
              id: 'prof-race',
              organization_id: 'org-1',
              created_by: 'u',
              created_at: AT,
              updated_at: AT,
            },
          ]),
        );
      }
      return inner(input, init);
    });
    renderDialog();

    await screen.findByText('Das Profil „phpIPAM Subnetze“ wird mit den phpIPAM-Spalten angelegt.');
    fireEvent.click(screen.getByRole('button', { name: 'Zum Importassistenten' }));
    await waitFor(() =>
      expect(screen.getByTestId('where')).toHaveTextContent('/imports?profile=prof-race'),
    );
    expect(calls.filter((c) => c.method === 'POST')).toHaveLength(1);
  });

  it('explains a 409 when the name stays taken by another entity', async () => {
    stubApi({
      onWrite: (method, path) =>
        method === 'POST' && path === '/import-profiles'
          ? json({ status: 409, title: 'Conflict' }, 409)
          : undefined,
    });
    renderDialog();

    await screen.findByText('Das Profil „phpIPAM Subnetze“ wird mit den phpIPAM-Spalten angelegt.');
    fireEvent.click(screen.getByRole('button', { name: 'Zum Importassistenten' }));
    expect(
      await screen.findByText(/Der Name „phpIPAM Subnetze“ gehört bereits einem Importprofil/),
    ).toBeInTheDocument();
  });

  it('creates the address profile for the kind addresses', async () => {
    const calls = stubApi({
      profiles: [
        {
          ...phpIPAMProfile('subnet'),
          id: 'prof-s',
          organization_id: 'org-1',
          created_by: 'u',
          created_at: AT,
          updated_at: AT,
        },
      ],
    });
    renderDialog();
    fireEvent.change(await screen.findByLabelText('Art'), { target: { value: 'ip_address' } });
    fireEvent.click(screen.getByRole('button', { name: 'Zum Importassistenten' }));
    await waitFor(() =>
      expect(screen.getByTestId('where')).toHaveTextContent('/imports?profile=prof-2'),
    );
    expect(calls.find((c) => c.method === 'POST')?.body).toMatchObject({
      name: 'phpIPAM Adressen',
      entity: 'ip_address',
    });
  });

  it('shows a notice instead of leading on without import:manage and without a profile', async () => {
    const calls = stubApi({ permissions: ALL.filter((p) => p !== 'import:manage') });
    renderDialog();

    expect(
      await screen.findByText(
        'Das Profil „phpIPAM Subnetze“ fehlt; zum Anlegen ist das Recht import:manage nötig.',
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Zum Importassistenten' })).toBeDisabled();
    expect(calls.some((c) => c.method === 'POST')).toBe(false);
  });

  it('exports with the filters of the page in filters.ipam and format csv', async () => {
    const calls = stubApi();
    renderDialog();

    fireEvent.click(await screen.findByRole('tab', { name: 'Export' }));
    const filters = screen.getByTestId('ipam-io-filters');
    expect(within(filters).getByText('global')).toBeInTheDocument();
    expect(within(filters).getByText('RZ Nord')).toBeInTheDocument();
    expect(within(filters).getByText('10.0.0.1')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Export starten' }));

    expect(await screen.findByRole('link', { name: 'Im Jobcenter ansehen' })).toHaveAttribute(
      'href',
      '/jobs',
    );
    // The job center has no deep link; the status names the job (AK4, Nachtrag).
    expect(screen.getByRole('status')).toHaveTextContent('Der Exportjob job-9 wurde angelegt.');
    expect(calls.find((c) => c.method === 'POST' && c.path === '/exports')?.body).toEqual({
      format: 'csv',
      filters: {
        entity: 'subnet',
        ipam: {
          section_id: SECTION,
          vrf_id: VRF,
          client_id: CLIENT,
          site_id: SITE,
          q: 'office',
          contains: '10.0.0.1',
        },
      },
    });
  });

  it('exports addresses with the address filters only and marks the others', async () => {
    const calls = stubApi();
    renderDialog();

    fireEvent.click(await screen.findByRole('tab', { name: 'Export' }));
    fireEvent.change(screen.getByLabelText('Art'), { target: { value: 'ip_address' } });
    expect(screen.getAllByText('(gilt nicht für Adressen)')).toHaveLength(4);
    fireEvent.click(screen.getByRole('button', { name: 'Export starten' }));
    await screen.findByRole('link', { name: 'Im Jobcenter ansehen' });
    expect(calls.find((c) => c.path === '/exports')?.body).toEqual({
      format: 'csv',
      filters: { entity: 'ip_address', ipam: { client_id: CLIENT, q: 'office' } },
    });
  });

  it('offers no import tab without ipam:write', async () => {
    stubApi({ permissions: ['import:run', 'export:run', 'ipam:read'] });
    renderDialog();

    expect(await screen.findByRole('tab', { name: 'Export' })).toBeInTheDocument();
    expect(screen.queryByRole('tab', { name: 'Import' })).not.toBeInTheDocument();
  });

  it('shows a 403 of the export in the dialog', async () => {
    stubApi({
      onWrite: (_method, path) =>
        path === '/exports'
          ? json({ type: 'about:blank', title: 'Forbidden', status: 403, detail: 'ipam:read' }, 403)
          : undefined,
    });
    renderDialog();

    fireEvent.click(await screen.findByRole('tab', { name: 'Export' }));
    fireEvent.click(screen.getByRole('button', { name: 'Export starten' }));
    expect(
      await screen.findByText('Für diese Aktion fehlt das Recht (z. B. ipam:read).'),
    ).toHaveAttribute('role', 'alert');
  });

  it('shows a 400 of the export in the dialog', async () => {
    stubApi({
      onWrite: (_method, path) =>
        path === '/exports'
          ? json(
              {
                type: 'about:blank',
                title: 'Bad Request',
                status: 400,
                detail: 'contains: no IP address',
              },
              400,
            )
          : undefined,
    });
    renderDialog();

    fireEvent.click(await screen.findByRole('tab', { name: 'Export' }));
    fireEvent.click(screen.getByRole('button', { name: 'Export starten' }));
    expect(
      await screen.findByText('Die Anfrage wurde abgelehnt: contains: no IP address'),
    ).toBeInTheDocument();
  });

  it('has no axe findings', async () => {
    stubApi();
    renderDialog();
    const dialog = await screen.findByRole('dialog', { name: 'Import und Export' });
    await screen.findByRole('tab', { name: 'Import' });
    const result = await axe.run(dialog, { rules: { 'color-contrast': { enabled: false } } });
    expect(result.violations).toEqual([]);

    fireEvent.click(screen.getByRole('tab', { name: 'Export' }));
    const exported = await axe.run(dialog, { rules: { 'color-contrast': { enabled: false } } });
    expect(exported.violations).toEqual([]);
  });
});
