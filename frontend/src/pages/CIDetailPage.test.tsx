import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { Route, Routes } from 'react-router';
import i18n from '../i18n';
import { CIDetailPage, buildNeighborList } from './CIDetailPage';
import type { TopologyGraphData } from '../api/client';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';
import { useScopeStore } from '../stores/scope';

const ci = {
  id: 'ci-1',
  organization_id: 'org-1',
  ci_type_id: 'switch',
  name: 'core-sw-01',
  status: 'active',
  manufacturer: 'Acme',
  model: 'AC-48',
  attributes: { ports: 48 },
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-02T00:00:00Z',
};

// The location tree of the CI (GET /locations/tree): site › building › room › rack.
const SITE = '10c00000-0000-4000-8000-000000000001';
const BUILDING = '10c00000-0000-4000-8000-000000000002';
const ROOM = '10c00000-0000-4000-8000-000000000003';
const RACK = '10c00000-0000-4000-8000-000000000004';
const ASSET = 'a55e7000-0000-4000-8000-000000000001';
const SUPPLIER = '5a990000-0000-4000-8000-000000000001';
const PARENT_CI = 'c1000000-0000-4000-8000-000000000001';
const SOURCE_CI = 'c1000000-0000-4000-8000-000000000002';

function node(
  id: string,
  kind: string,
  name: string,
  parent: string | undefined,
  children: unknown[],
) {
  return {
    id,
    organization_id: 'org-1',
    client_id: 'client-1',
    site_id: SITE,
    ...(parent ? { parent_id: parent } : {}),
    kind,
    name,
    path: id,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    children,
  };
}

const locationTree = node(SITE, 'site', 'RZ Nord', undefined, [
  node(BUILDING, 'building', 'Gebäude A', SITE, [
    node(ROOM, 'room', 'Raum 1.01', BUILDING, [node(RACK, 'rack', 'Rack 07', ROOM, [])]),
  ]),
]);

/** detailValue is the value cell next to the first label of the CI overview. */
function detailValue(label: string): HTMLElement {
  return screen.getAllByText(label, { selector: 'dt' })[0]!.nextElementSibling as HTMLElement;
}

/** assetCard is the card around the title of the asset card. */
function assetCard(title: HTMLElement): HTMLElement {
  let el: HTMLElement | null = title;
  while (el && !el.querySelector('dl')) el = el.parentElement;
  return el!;
}

function renderDetail() {
  return renderWithProviders(
    <Routes>
      <Route path="/cmdb/:id" element={<CIDetailPage />} />
    </Routes>,
    { route: '/cmdb/ci-1' },
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('buildNeighborList', () => {
  const graph: TopologyGraphData = {
    nodes: [
      { id: 'ci-1', name: 'core-sw-01', ci_type: 'switch', status: 'active' },
      { id: 'ci-2', name: 'srv-01', ci_type: 'server', status: 'maintenance' },
    ],
    edges: [
      { id: 'e-1', source_ci_id: 'ci-1', target_ci_id: 'ci-2', rel_type: 'connected_to' },
      { id: 'e-2', source_ci_id: 'ci-3', target_ci_id: 'ci-1', rel_type: 'powers' },
      { id: 'e-3', source_ci_id: 'ci-4', target_ci_id: 'ci-5', rel_type: 'depends_on' },
    ],
  };

  it('returns the other endpoint with its direction', () => {
    expect(buildNeighborList(graph, 'ci-1')).toEqual([
      {
        id: 'ci-2',
        name: 'srv-01',
        ciType: 'server',
        status: 'maintenance',
        relType: 'connected_to',
        direction: 'outgoing',
      },
    ]);
  });

  it('drops edges whose endpoint node is missing or unrelated to the CI', () => {
    expect(buildNeighborList(graph, 'ci-9')).toEqual([]);
  });
});

describe('CIDetailPage', () => {
  it('renders the CI, its relationships and its neighbors', async () => {
    stubFetchRoutes({
      '/cis/ci-1/relationships': {
        data: [
          {
            id: 'rel-1',
            organization_id: 'org-1',
            source_ci_id: 'ci-1',
            target_ci_id: 'ci-2',
            rel_type: 'connected_to',
            attributes: {},
            source: 'discovery',
            created_at: '2026-01-01T00:00:00Z',
            updated_at: '2026-01-01T00:00:00Z',
          },
        ],
        total: 1,
        limit: 25,
        offset: 0,
        has_more: false,
      },
      '/topology/cis/ci-1/neighbors': {
        nodes: [
          { id: 'ci-1', name: 'core-sw-01', ci_type: 'switch', status: 'active' },
          { id: 'ci-2', name: 'srv-01', ci_type: 'server', status: 'active' },
        ],
        edges: [
          { id: 'e-1', source_ci_id: 'ci-1', target_ci_id: 'ci-2', rel_type: 'connected_to' },
        ],
      },
      '/cis/ci-1': ci,
    });

    renderDetail();

    expect(await screen.findByRole('heading', { name: 'core-sw-01' })).toBeInTheDocument();
    expect(await screen.findByRole('link', { name: 'srv-01' })).toHaveAttribute(
      'href',
      '/cmdb/ci-2',
    );
    await waitFor(() => expect(screen.getAllByText('connected_to').length).toBeGreaterThan(0));
    expect(screen.getByText(/"ports": 48/)).toBeInTheDocument();
  });

  it('shows time and location by name and the unified history (CI-06, DB-05, F-237)', async () => {
    const fetchMock = stubFetchRoutes({
      '/history/ci/ci-1': {
        data: [
          {
            id: 'a-1',
            entity_type: 'ci',
            entity_id: 'ci-1',
            change_type: 'ci.updated',
            created_at: '2026-01-03T12:00:00Z',
            source: 'audit',
          },
          {
            id: 'c-1',
            entity_type: 'ci',
            entity_id: 'ci-1',
            change_type: 'update',
            field_name: 'name',
            created_at: '2026-01-02T12:00:00Z',
            source: 'ci_change',
            site_id: SITE,
          },
          {
            id: 'c-2',
            entity_type: 'ci',
            entity_id: 'ci-1',
            change_type: 'update',
            change_kind: 'updated',
            change_set_id: '5e700000-0000-4000-8000-000000000001',
            field_changes: [{ field: 'location_id', old: ROOM, new: RACK }],
            created_at: '2026-01-02T11:00:00Z',
            source: 'ci_change',
            site_id: SITE,
          },
        ],
        total: 3,
        limit: 50,
        offset: 0,
        has_more: false,
      },
      '/locations/tree': { data: [locationTree] },
      '/cis/ci-1/relationships': { data: [], total: 0, limit: 25, offset: 0, has_more: false },
      '/topology/cis/ci-1/neighbors': { nodes: [], edges: [] },
      '/cis/ci-1': {
        ...ci,
        location_id: RACK,
        site_id: SITE,
        room_id: ROOM,
        first_seen_at: '2026-01-01T12:00:00Z',
      },
    });

    renderDetail();

    expect(await screen.findByRole('heading', { name: 'core-sw-01' })).toBeInTheDocument();
    // The location as its path from the site, site and room by name.
    await waitFor(() =>
      expect(detailValue('Standort')).toHaveTextContent(
        'RZ Nord › Gebäude A › Raum 1.01 › Rack 07',
      ),
    );
    expect(detailValue('Site')).toHaveTextContent(/^RZ Nord$/);
    expect(detailValue('Raum')).toHaveTextContent(/^Raum 1\.01$/);
    // Times in the locale format, not as ISO strings.
    expect(detailValue('Zuerst gesehen')).toHaveTextContent(/^01\.01\.2026, \d{2}:\d{2}$/);
    expect(detailValue('Erstellt')).toHaveTextContent(/^\d{2}\.\d{2}\.20\d{2}, \d{2}:\d{2}$/);
    const history = await screen.findByRole('list', { name: 'Historie' });
    expect(history).toHaveTextContent('update · name');
    expect(history).toHaveTextContent('Audit');
    await waitFor(() => expect(history).toHaveTextContent('Site RZ Nord'));
    expect(history).toHaveTextContent(
      'location_id: RZ Nord › Gebäude A › Raum 1.01 → RZ Nord › Gebäude A › Raum 1.01 › Rack 07',
    );
    const text = document.body.textContent ?? '';
    for (const id of [SITE, BUILDING, ROOM, RACK]) expect(text).not.toContain(id);
    expect(text).not.toContain('2026-01-01T12:00:00Z');
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/locations/tree'))).toBe(
      true,
    );

    await act(async () => {
      await i18n.changeLanguage('en-US');
    });
    await waitFor(() =>
      expect(detailValue('First seen')).toHaveTextContent(/^Jan 1, 2026, \d{1,2}:\d{2}\s?[AP]M$/),
    );
    expect(detailValue('Location')).toHaveTextContent('RZ Nord › Gebäude A › Raum 1.01 › Rack 07');
    await act(async () => {
      await i18n.changeLanguage('de-DE');
    });
  });

  it('shows the full asset card read-only with names and formatted dates (DB-05, AST-04, F-238)', async () => {
    stubFetchRoutes({
      '/locations/tree': { data: [locationTree] },
      [`/suppliers/${SUPPLIER}`]: { id: SUPPLIER, name: 'Acme Distribution' },
      '/cis/ci-1/relationships': { data: [], total: 0, limit: 25, offset: 0, has_more: false },
      '/topology/cis/ci-1/neighbors': { nodes: [], edges: [] },
      '/cis/ci-1': {
        ...ci,
        serial_number: 'SN-OBSERVED',
        location_id: RACK,
        site_id: SITE,
        room_id: ROOM,
        asset_id: ASSET,
        asset: {
          id: ASSET,
          inventory_no: 'INV-0042',
          name: 'core-sw-01 hardware',
          status: 'assigned',
          serial_number: 'SN-ASSET-42',
          location_id: RACK,
          lifecycle_state: 'in_use',
          rfid_tag: 'RFID-E200-42',
          barcode: 'BC-4711',
          purchase_date: '2025-03-15',
          warranty_until: '2028-03-14',
          supplier_id: SUPPLIER,
          invoice_number: 'RE-2025-0815',
        },
      },
    });

    renderDetail();

    const card = assetCard(await screen.findByText('Asset (kanonische Daten)'));
    const value = (label: string) =>
      within(card).getByText(label, { selector: 'dt' }).nextElementSibling as HTMLElement;
    await waitFor(() =>
      expect(value('Standort')).toHaveTextContent('RZ Nord › Gebäude A › Raum 1.01 › Rack 07'),
    );
    expect(value('Inventarnummer')).toHaveTextContent('INV-0042');
    expect(value('Seriennummer')).toHaveTextContent('SN-ASSET-42');
    expect(value('Physischer Lebenszyklus')).toHaveTextContent('in_use');
    expect(value('RFID-Tag')).toHaveTextContent('RFID-E200-42');
    expect(value('Barcode')).toHaveTextContent('BC-4711');
    expect(value('Kaufdatum')).toHaveTextContent(/^15\.03\.2025$/);
    expect(value('Garantieende')).toHaveTextContent(/^14\.03\.2028$/);
    await waitFor(() => expect(value('Lieferant')).toHaveTextContent('Acme Distribution'));
    expect(value('Rechnungsnummer')).toHaveTextContent('RE-2025-0815');
    // Read-only: the card has no input and no action.
    expect(within(card).queryAllByRole('textbox')).toHaveLength(0);
    expect(within(card).queryAllByRole('button')).toHaveLength(0);
    // The overview names the observed serial number apart from the asset's.
    expect(detailValue('Beobachtete Seriennummer')).toHaveTextContent('SN-OBSERVED');

    await act(async () => {
      await i18n.changeLanguage('en-US');
    });
    await waitFor(() => expect(value('Purchase date')).toHaveTextContent(/^Mar 15, 2025$/));
    expect(value('Warranty end')).toHaveTextContent(/^Mar 14, 2028$/);
    expect(value('RFID tag')).toHaveTextContent('RFID-E200-42');
    expect(value('Invoice number')).toHaveTextContent('RE-2025-0815');
    await act(async () => {
      await i18n.changeLanguage('de-DE');
    });
  });

  // UI-14, WP-810: the asset card is the one asset display of the CI and
  // follows its child contract (AST-05, WP-273); the deprecated parent path
  // GET /cis/{id}/parent is no longer read.
  describe('asset card by child_mode (UI-14, WP-810)', () => {
    const asset = {
      id: ASSET,
      inventory_no: 'INV-0042',
      name: 'chassis hardware',
      status: 'assigned',
      serial_number: 'SN-CHASSIS-42',
      warranty_until: '2028-03-14',
      supplier_id: SUPPLIER,
    };
    const routes = (detail: unknown, extra: Record<string, unknown> = {}) => ({
      ...extra,
      '/cis/ci-1/relationships': { data: [], total: 0, limit: 25, offset: 0, has_more: false },
      '/topology/cis/ci-1/neighbors': { nodes: [], edges: [] },
      '/cis/ci-1': detail,
    });
    const parentCalls = (fetchMock: ReturnType<typeof stubFetchRoutes>) =>
      fetchMock.mock.calls.filter(([url]) => /\/cis\/[^/?]+\/parent/.test(String(url)));

    it('shows the inherited asset of a configuration child with the hint and both links', async () => {
      const fetchMock = stubFetchRoutes(
        routes(
          {
            ...ci,
            parent_ci_id: PARENT_CI,
            child_mode: 'configuration',
            asset_source_ci_id: SOURCE_CI,
            asset,
          },
          { [`/suppliers/${SUPPLIER}`]: { id: SUPPLIER, name: 'Acme Distribution' } },
        ),
      );

      renderDetail();

      const card = assetCard(await screen.findByText('Asset (kanonische Daten)'));
      const value = (label: string) =>
        within(card).getByText(label, { selector: 'dt' }).nextElementSibling as HTMLElement;
      expect(value('Inventarnummer')).toHaveTextContent('INV-0042');
      expect(value('Seriennummer')).toHaveTextContent('SN-CHASSIS-42');
      expect(value('Garantieende')).toHaveTextContent(/^14\.03\.2028$/);
      await waitFor(() => expect(value('Lieferant')).toHaveTextContent('Acme Distribution'));
      expect(within(card).getByRole('link', { name: 'vom übergeordneten CI' })).toHaveAttribute(
        'href',
        `/cmdb/${SOURCE_CI}`,
      );
      expect(within(card).getByText('schreibgeschützt')).toBeInTheDocument();
      expect(within(card).getByRole('link', { name: 'Asset öffnen' })).toHaveAttribute(
        'href',
        `/assets/${ASSET}`,
      );
      expect(within(card).queryByRole('link', { name: 'Übergeordnetes CI' })).toBeNull();
      expect(
        fetchMock.mock.calls.some(([url]) => String(url).includes(`/suppliers/${SUPPLIER}`)),
      ).toBe(true);
      expect(parentCalls(fetchMock)).toHaveLength(0);
      // The card is the only asset display of the CI.
      expect(screen.getAllByText('Asset (kanonische Daten)')).toHaveLength(1);
    });

    it('shows a dash for the supplier when GET /suppliers/{id} answers 404', async () => {
      const fetchMock = stubFetchRoutes(
        routes({
          ...ci,
          parent_ci_id: PARENT_CI,
          child_mode: 'configuration',
          asset_source_ci_id: SOURCE_CI,
          asset,
        }),
      );

      renderDetail();

      const card = assetCard(await screen.findByText('Asset (kanonische Daten)'));
      await waitFor(() =>
        expect(
          fetchMock.mock.calls.some(([url]) => String(url).includes(`/suppliers/${SUPPLIER}`)),
        ).toBe(true),
      );
      await act(async () => {
        await new Promise((resolve) => setTimeout(resolve, 50));
      });
      const supplier = within(card).getByText('Lieferant', { selector: 'dt' })
        .nextElementSibling as HTMLElement;
      expect(supplier).toHaveTextContent(/^—$/);
      expect(parentCalls(fetchMock)).toHaveLength(0);
    });

    it('shows a dash for the supplier when GET /suppliers/{id} answers 403', async () => {
      stubFetchRoutes(
        routes(
          { ...ci, asset_id: ASSET, asset },
          {
            [`/suppliers/${SUPPLIER}`]: new Response(JSON.stringify({ status: 403 }), {
              status: 403,
              headers: { 'Content-Type': 'application/json' },
            }),
          },
        ),
      );

      renderDetail();

      const card = assetCard(await screen.findByText('Asset (kanonische Daten)'));
      await act(async () => {
        await new Promise((resolve) => setTimeout(resolve, 50));
      });
      const supplier = within(card).getByText('Lieferant', { selector: 'dt' })
        .nextElementSibling as HTMLElement;
      expect(supplier).toHaveTextContent(/^—$/);
      // A CI without parent shows neither the hint nor a parent link.
      expect(within(card).queryByText('vom übergeordneten CI')).toBeNull();
      expect(within(card).queryByRole('link', { name: 'Übergeordnetes CI' })).toBeNull();
    });

    it('shows the own asset of a standalone child with a link to its parent CI', async () => {
      stubFetchRoutes(
        routes({
          ...ci,
          parent_ci_id: PARENT_CI,
          child_mode: 'standalone',
          asset_id: ASSET,
          asset: { ...asset, supplier_id: undefined },
        }),
      );

      renderDetail();

      const card = assetCard(await screen.findByText('Asset (kanonische Daten)'));
      expect(within(card).getByRole('link', { name: 'Übergeordnetes CI' })).toHaveAttribute(
        'href',
        `/cmdb/${PARENT_CI}`,
      );
      expect(within(card).queryByText('vom übergeordneten CI')).toBeNull();
      expect(within(card).queryByText('schreibgeschützt')).toBeNull();
      expect(within(card).getByRole('link', { name: 'Asset öffnen' })).toHaveAttribute(
        'href',
        `/assets/${ASSET}`,
      );
    });

    it('shows no asset card for a CI without asset and reads no parent', async () => {
      const fetchMock = stubFetchRoutes(
        routes({ ...ci, parent_ci_id: PARENT_CI, child_mode: 'standalone' }),
      );

      renderDetail();

      expect(await screen.findByRole('heading', { name: 'core-sw-01' })).toBeInTheDocument();
      await act(async () => {
        await new Promise((resolve) => setTimeout(resolve, 50));
      });
      expect(screen.queryByText('Asset (kanonische Daten)')).toBeNull();
      expect(parentCalls(fetchMock)).toHaveLength(0);
      expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/suppliers/'))).toBe(false);
    });

    it('labels the hints in English', async () => {
      stubFetchRoutes(
        routes({
          ...ci,
          parent_ci_id: PARENT_CI,
          child_mode: 'configuration',
          asset_source_ci_id: SOURCE_CI,
          asset: { ...asset, supplier_id: undefined },
        }),
      );
      await act(async () => {
        await i18n.changeLanguage('en-US');
      });
      try {
        renderDetail();
        const card = assetCard(await screen.findByText('Asset (canonical data)'));
        expect(within(card).getByRole('link', { name: 'from the parent CI' })).toBeInTheDocument();
        expect(within(card).getByText('read-only')).toBeInTheDocument();
        expect(within(card).getByRole('link', { name: 'Open asset' })).toBeInTheDocument();
      } finally {
        await act(async () => {
          await i18n.changeLanguage('de-DE');
        });
      }
    });
  });

  it('does not read the location tree for a CI without a location', async () => {
    const fetchMock = stubFetchRoutes({
      '/cis/ci-1/relationships': { data: [], total: 0, limit: 25, offset: 0, has_more: false },
      '/topology/cis/ci-1/neighbors': { nodes: [], edges: [] },
      '/cis/ci-1': ci,
    });

    renderDetail();

    expect(await screen.findByRole('heading', { name: 'core-sw-01' })).toBeInTheDocument();
    expect(detailValue('Standort')).toHaveTextContent(/^—$/);
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 50));
    });
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/locations/tree'))).toBe(
      false,
    );
  });

  it('shows an error state with a retry action when the CI cannot be loaded', async () => {
    stubFetchRoutes({});

    renderDetail();

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('Das Configuration Item konnte nicht geladen werden');
    expect(screen.getByRole('button', { name: 'Erneut versuchen' })).toBeInTheDocument();
  });
});

// GLO-10, UI-18 (F-175, moved from CITypeLabels.auditdefect.tsx): the detail
// page shows the type by its label in the UI language, never the type UUID.
describe('CI type labels on the CI detail page', () => {
  const SERVER_TYPE_ID = '7a0c1e2d-0000-4000-8000-0000000000a1';
  const CI_ID = '7a0c1e2d-0000-4000-8000-0000000000c1';

  const serverType = {
    id: SERVER_TYPE_ID,
    key: 'server',
    name: 'server',
    display_name: { de: 'Server (physisch)', en: 'Server (physical)' },
    is_builtin: true,
    is_system: true,
    is_active: true,
    is_logical: false,
    is_observable: true,
    schema_version: 1,
    version: 1,
  };

  const serverCI = {
    id: CI_ID,
    organization_id: 'org-1',
    ci_type_id: SERVER_TYPE_ID,
    name: 'srv-label-01',
    status: 'active',
    attributes: {},
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-02T00:00:00Z',
  };

  function json(body: unknown, status = 200) {
    return new Response(JSON.stringify(body), {
      status,
      headers: { 'Content-Type': 'application/json' },
    });
  }

  const emptyList = { data: [], total: 0, limit: 50, offset: 0, has_more: false };

  /**
   * Answers by exact API path; types are what GET /ci-types lists. routes
   * answer further paths (before the empty lists of the CI's sections) and
   * ci replaces the CI record.
   */
  function stubBackend(
    types: unknown[] = [serverType],
    { ci = serverCI, routes = {} }: { ci?: unknown; routes?: Record<string, unknown> } = {},
  ) {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const raw = typeof input === 'string' ? input : input.toString();
      const path = new URL(raw, 'http://localhost').pathname.replace(/^\/api\/v1/, '');
      const method = (init?.method ?? 'GET').toUpperCase();
      if (path === '/ci-types') {
        return json({ data: types, total: types.length, limit: 200, offset: 0, has_more: false });
      }
      if (path === `/cis/${CI_ID}`) {
        return json(ci);
      }
      if (method === 'GET' && path in routes) {
        return json(routes[path]);
      }
      if (path === `/topology/cis/${CI_ID}/neighbors`) {
        return json({ nodes: [], edges: [] });
      }
      if (method === 'GET' && path.startsWith(`/cis/${CI_ID}/`)) {
        return json(emptyList);
      }
      return json({ detail: `unexpected request: ${method} ${path}` }, 404);
    });
    vi.stubGlobal('fetch', fetchMock);
    return fetchMock;
  }

  function renderServer() {
    return renderWithProviders(
      <Routes>
        <Route path="/cmdb/:id" element={<CIDetailPage />} />
      </Routes>,
      { route: `/cmdb/${CI_ID}` },
    );
  }

  afterEach(async () => {
    await act(async () => {
      await i18n.changeLanguage('de-DE');
    });
  });

  it('shows the localized type label on the CI detail page, not the type UUID', async () => {
    stubBackend();
    renderServer();

    expect(await screen.findByRole('heading', { name: 'srv-label-01' })).toBeInTheDocument();
    await waitFor(
      () => {
        expect(
          screen.queryAllByText('Server (physisch)').length,
          'detail shows the type label display_name.de',
        ).toBeGreaterThan(0);
      },
      { timeout: 2000 },
    );
    expect(document.body.textContent ?? '').not.toContain(SERVER_TYPE_ID);
  });

  it('labels the type in the badge and the detail row and follows a language switch', async () => {
    const fetchMock = stubBackend();
    renderServer();

    await waitFor(() => expect(screen.getAllByText('Server (physisch)')).toHaveLength(2));
    // Deactivated types are listed too: a CI can still carry one.
    const typeRequest = fetchMock.mock.calls
      .map(([input]) => String(input))
      .find((url) => url.includes('/ci-types'));
    expect(typeRequest).toContain('include_inactive=true');
    expect(screen.getByText('CI-Typ').closest('div')).toHaveTextContent('Server (physisch)');

    await act(async () => {
      await i18n.changeLanguage('en-US');
    });
    await waitFor(() => expect(screen.getAllByText('Server (physical)')).toHaveLength(2));
    expect(screen.queryByText('Server (physisch)')).not.toBeInTheDocument();
    expect(screen.getByText('CI Type').closest('div')).toHaveTextContent('Server (physical)');
    expect(document.body.textContent ?? '').not.toContain(SERVER_TYPE_ID);
  });

  it('shows a CI in status maintenance without the raw status value (WP-523, GLO-10)', async () => {
    // What the API answers for a CI created with status maintenance (as in
    // e2e-real/i18n.spec.ts): the provenance lists every typed field of the
    // record (BuildProvenance, REC-10) including status, the history holds
    // the create change and the ci.created audit entry with the record.
    const maintenanceCI = { ...serverCI, status: 'maintenance', health: 'unknown' };
    const recorded = { source: 'manual', rank: 100, reason: 'record', diverged: false };
    stubBackend([serverType], {
      ci: maintenanceCI,
      routes: {
        [`/cis/${CI_ID}/provenance`]: {
          data: [
            { field: 'name', value: maintenanceCI.name, ...recorded },
            { field: 'status', value: 'maintenance', ...recorded },
          ],
          total: 2,
          limit: 2,
          offset: 0,
          has_more: false,
        },
        [`/history/ci/${CI_ID}`]: {
          data: [
            {
              id: 'a-1',
              entity_type: 'ci',
              entity_id: CI_ID,
              change_type: 'ci.created',
              new_value: { after: maintenanceCI },
              created_at: '2026-01-01T00:00:01Z',
              source: 'audit',
            },
            {
              id: 'c-1',
              entity_type: 'ci',
              entity_id: CI_ID,
              change_type: 'create',
              new_value: maintenanceCI,
              change_kind: 'created',
              change_set_id: 'set-1',
              created_at: '2026-01-01T00:00:00Z',
              source: 'ci_change',
            },
          ],
          total: 2,
          limit: 50,
          offset: 0,
          has_more: false,
          audit_included: true,
        },
      },
    });
    const { container } = renderServer();

    expect(await screen.findByRole('heading', { name: 'srv-label-01' })).toBeInTheDocument();
    // The provenance row of the status shows the catalog label.
    expect(
      await screen.findByRole('button', { name: 'Herkunft von status' }, { timeout: 2000 }),
    ).toBeInTheDocument();
    await waitFor(() => expect(screen.getAllByText('ci.created').length).toBeGreaterThan(0));
    expect(container.textContent ?? '').toContain('Wartung');
    expect(container.textContent ?? '').not.toContain('maintenance');

    await act(async () => {
      await i18n.changeLanguage('en-US');
    });
    await waitFor(() => expect(container.textContent ?? '').toContain('Maintenance'));
    expect(container.textContent ?? '').not.toContain('maintenance');
  });

  it('falls back to the type name without a display name', async () => {
    stubBackend([{ ...serverType, display_name: {} }]);
    renderServer();

    await waitFor(() => expect(screen.getAllByText('server')).toHaveLength(2));
    expect(document.body.textContent ?? '').not.toContain(SERVER_TYPE_ID);
  });

  it('shows the type id only while the type is unknown', async () => {
    stubBackend([]);
    renderServer();

    expect(await screen.findByRole('heading', { name: 'srv-label-01' })).toBeInTheDocument();
    await waitFor(() => expect(screen.getAllByText(SERVER_TYPE_ID)).toHaveLength(2));
  });
});

describe('CIDetailPage under a chosen scope (E-87, F-169)', () => {
  const CLIENT_A = 'aaaaaaaa-0000-4000-8000-000000000001';
  const CLIENT_B = 'aaaaaaaa-0000-4000-8000-000000000002';
  const CI_B = 'cccccccc-0000-4000-8000-00000000000b';
  const OTHER_B = 'cccccccc-0000-4000-8000-0000000000b2';
  const OTHER_A = 'cccccccc-0000-4000-8000-0000000000a1';

  function ciOf(id: string, clientId: string) {
    return {
      id,
      organization_id: 'org-1',
      client_id: clientId,
      ci_type_id: 'switch',
      name: `ci-${id.slice(-3)}`,
      status: 'active',
      attributes: {},
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-02T00:00:00Z',
    };
  }

  function edge(id: string, target: string) {
    return {
      id,
      organization_id: 'org-1',
      source_ci_id: CI_B,
      target_ci_id: target,
      rel_type: 'connected_to',
      attributes: {},
      source: 'manual',
      verification_state: 'verified',
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-01T00:00:00Z',
    };
  }

  function stubBackend() {
    const cis: Record<string, unknown> = {
      [CI_B]: ciOf(CI_B, CLIENT_B),
      [OTHER_B]: ciOf(OTHER_B, CLIENT_B),
      [OTHER_A]: ciOf(OTHER_A, CLIENT_A),
    };
    const page = (data: unknown[]) => ({
      data,
      total: data.length,
      limit: 200,
      offset: 0,
      has_more: false,
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const path = new URL(String(input), 'http://localhost').pathname.replace(/^\/api\/v1/, '');
      let body: unknown = page([]);
      if (path === '/clients')
        body = page([
          { id: CLIENT_A, name: 'Kunde A' },
          { id: CLIENT_B, name: 'Kunde B' },
        ]);
      else if (path === `/cis/${CI_B}/relationships`)
        body = page([edge('rel-b', OTHER_B), edge('rel-a', OTHER_A)]);
      else if (path === `/topology/cis/${CI_B}/neighbors`) body = { nodes: [], edges: [] };
      else if (path.startsWith('/cis/') && cis[path.slice(5)]) body = cis[path.slice(5)];
      return new Response(JSON.stringify(body), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    });
    vi.stubGlobal('fetch', fetchMock);
    return fetchMock;
  }

  function renderB() {
    return renderWithProviders(
      <Routes>
        <Route path="/cmdb/:id" element={<CIDetailPage />} />
      </Routes>,
      { route: `/cmdb/${CI_B}` },
    );
  }

  beforeEach(() => {
    window.localStorage.clear();
    useScopeStore.getState().reset();
  });
  afterEach(() => useScopeStore.getState().reset());

  it('shows the notice and scoped relationships for a CI of another client', async () => {
    stubBackend();
    useScopeStore.setState({ clientId: CLIENT_A, siteId: '' });
    renderB();

    // The CI loads (deep link): access is the server's decision.
    expect(await screen.findByRole('heading', { name: 'ci-00b' })).toBeInTheDocument();
    const banner = screen.getByRole('status', { name: 'Arbeitsbereich' });
    await waitFor(() =>
      expect(banner).toHaveTextContent(
        'Dieses CI liegt außerhalb des gewählten Bereichs (Kunde A).',
      ),
    );
    // The relationship to the client-B CI is hidden and counted.
    expect(await screen.findByText('1 außerhalb des Bereichs ausgeblendet')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: OTHER_A })).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: OTHER_B })).toBeNull();

    fireEvent.click(within(banner).getByRole('button', { name: 'Bereich wechseln' }));

    expect(useScopeStore.getState().clientId).toBe(CLIENT_B);
    await waitFor(() =>
      expect(screen.queryByRole('status', { name: 'Arbeitsbereich' })).toBeNull(),
    );
    // Now the client-A CI lies outside and the client-B CI is listed.
    expect(await screen.findByRole('link', { name: OTHER_B })).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: OTHER_A })).toBeNull();
  });

  it('shows no notice for a CI inside the scope', async () => {
    stubBackend();
    useScopeStore.setState({ clientId: CLIENT_B, siteId: '' });
    renderB();

    expect(await screen.findByRole('heading', { name: 'ci-00b' })).toBeInTheDocument();
    expect(await screen.findByText('1 außerhalb des Bereichs ausgeblendet')).toBeInTheDocument();
    expect(screen.queryByRole('status', { name: 'Arbeitsbereich' })).toBeNull();
    expect(screen.queryByText(/Dieses CI liegt außerhalb/)).toBeNull();
  });

  it('shows no notice and every relationship without a selection', async () => {
    stubBackend();
    renderB();

    expect(await screen.findByRole('heading', { name: 'ci-00b' })).toBeInTheDocument();
    expect(await screen.findByRole('link', { name: OTHER_A })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: OTHER_B })).toBeInTheDocument();
    expect(screen.queryByText(/Dieses CI liegt außerhalb/)).toBeNull();
    expect(screen.queryByText(/ausgeblendet/)).toBeNull();
  });
});

/**
 * WP-817 (CI-08, UI-14): the tags of a CI as colored chips from the
 * catalog, changed with a selection from the catalog through PUT
 * /cis/{id}/tags with If-Match; 409 and 412 appear like those of a PATCH.
 */
describe('CIDetailPage tags (WP-817)', () => {
  const taggedCI = { ...ci, version: 4, tags: ['backup'] };
  const catalog = [
    {
      id: 'tag-1',
      organization_id: 'org-1',
      key: 'backup',
      display_name: { de: 'Sicherung', en: 'Backup' },
      color: '#1f6feb',
      is_system: false,
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-01T00:00:00Z',
    },
    {
      id: 'tag-2',
      organization_id: 'org-1',
      key: 'prod',
      display_name: { de: 'Produktion', en: 'Production' },
      color: null,
      is_system: false,
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-01T00:00:00Z',
    },
  ];

  function mockTagApi(put: (init: RequestInit) => Response, permissions = ['ci:read', 'ci:write']) {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const { pathname } = new URL(String(input), 'http://localhost');
      const method = init?.method ?? 'GET';
      const ok = (body: unknown) =>
        new Response(JSON.stringify(body), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        });
      if (pathname.endsWith('/me/permissions')) return ok({ user_id: 'user-1', permissions });
      if (pathname.endsWith('/cis/ci-1/tags') && method === 'PUT') return put(init!);
      if (pathname.endsWith('/api/v1/tags')) {
        return ok({ data: catalog, total: 2, limit: 200, offset: 0, has_more: false });
      }
      if (pathname.endsWith('/cis/ci-1')) return ok(taggedCI);
      return new Response(JSON.stringify({ detail: 'unexpected' }), { status: 404 });
    });
    vi.stubGlobal('fetch', fetchMock);
    return fetchMock;
  }

  it('shows the tags of the CI as chips named from the catalog', async () => {
    mockTagApi(() => new Response(null, { status: 500 }));
    renderDetail();

    const chips = await screen.findByRole('list', { name: 'Tags' });
    expect(await within(chips).findByText('Sicherung')).toBeInTheDocument();
    expect(within(chips).queryByText('backup')).toBeNull();
  });

  it('replaces the tags with a selection from the catalog through PUT with If-Match', async () => {
    const fetchMock = mockTagApi(
      (init) =>
        new Response(
          JSON.stringify({
            ...taggedCI,
            version: 5,
            tags: (JSON.parse(String(init.body)) as { tags: string[] }).tags,
          }),
          { status: 200, headers: { 'Content-Type': 'application/json', ETag: '"5"' } },
        ),
    );
    renderDetail();

    fireEvent.click(await screen.findByRole('button', { name: 'Tags bearbeiten' }));
    const selection = await screen.findByRole('group', { name: 'Tags aus dem Katalog' });
    expect(within(selection).getByRole('checkbox', { name: 'Sicherung' })).toBeChecked();
    fireEvent.click(within(selection).getByRole('checkbox', { name: 'Sicherung' }));
    fireEvent.click(within(selection).getByRole('checkbox', { name: 'Produktion' }));
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));

    await waitFor(() =>
      expect(fetchMock.mock.calls.filter((call) => call[1]?.method === 'PUT')).toHaveLength(1),
    );
    const put = fetchMock.mock.calls.find((call) => call[1]?.method === 'PUT')!;
    expect(String(put[0])).toContain('/cis/ci-1/tags');
    expect(JSON.parse(String(put[1]!.body))).toEqual({ tags: ['prod'] });
    expect(new Headers(put[1]!.headers).get('If-Match')).toBe('"4"');
    await waitFor(() =>
      expect(screen.queryByRole('group', { name: 'Tags aus dem Katalog' })).toBeNull(),
    );
  });

  it('shows a 409 of the tags as a message', async () => {
    mockTagApi(
      () =>
        new Response(
          JSON.stringify({
            type: 'urn:reticora:problem:version-conflict',
            title: 'Conflict',
            status: 409,
            detail: 'the CI changed since version 4',
            fields: ['tags'],
          }),
          { status: 409, headers: { 'Content-Type': 'application/problem+json' } },
        ),
    );
    renderDetail();

    fireEvent.click(await screen.findByRole('button', { name: 'Tags bearbeiten' }));
    const selection = await screen.findByRole('group', { name: 'Tags aus dem Katalog' });
    fireEvent.click(within(selection).getByRole('checkbox', { name: 'Produktion' }));
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));

    expect(
      await screen.findByText(/Die Tags wurden zwischenzeitlich geändert \(tags\)/),
    ).toHaveAttribute('role', 'alert');
  });

  it('offers no change without ci:write', async () => {
    mockTagApi(() => new Response(null, { status: 500 }), ['ci:read']);
    renderDetail();

    const chips = await screen.findByRole('list', { name: 'Tags' });
    expect(await within(chips).findByText('Sicherung')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Tags bearbeiten' })).toBeNull();
  });
});

/**
 * CI-07 (WP-805): the overview shows 'Verantwortlich', the owner of the CI
 * (owner_user_id, owner_team_id), by name from GET /users/{id} and GET
 * /teams/{id}; without user:read only the kind. A CI with an asset has the
 * asset's owner (DB-05), marked 'vom Asset'. Not the contact role owner.
 */
describe('CIDetailPage owner (CI-07, WP-805)', () => {
  const USER = '0a000000-0000-4000-8000-0000000000a1';
  const TEAM = '0e000000-0000-4000-8000-0000000000a1';
  const ownedCI = { ...ci, version: 2, tags: [], owner_user_id: USER, owner_team_id: TEAM };

  function mockOwnerApi(shown: Record<string, unknown>, permissions: string[]) {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const { pathname } = new URL(String(input), 'http://localhost');
      const ok = (body: unknown) =>
        new Response(JSON.stringify(body), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        });
      if (pathname.endsWith('/me/permissions')) return ok({ user_id: 'user-1', permissions });
      if (pathname.endsWith(`/users/${USER}`)) {
        return ok({ id: USER, display_name: 'Erika Muster', email: 'erika@example.test' });
      }
      if (pathname.endsWith(`/teams/${TEAM}`)) {
        return ok({ id: TEAM, name: 'Netzbetrieb', member_count: 3 });
      }
      if (pathname.endsWith('/cis/ci-1')) return ok(shown);
      return new Response(JSON.stringify({ detail: 'unexpected' }), { status: 404 });
    });
    vi.stubGlobal('fetch', fetchMock);
    return fetchMock;
  }

  const ownerReads = (fetchMock: ReturnType<typeof mockOwnerApi>) =>
    fetchMock.mock.calls
      .map(([input]) => new URL(String(input), 'http://localhost').pathname)
      .filter((path) => /\/(users|teams)\//.test(path));

  it('shows the user and the team by name with user:read', async () => {
    mockOwnerApi(ownedCI, ['ci:read', 'user:read']);
    renderDetail();

    await screen.findByText('Verantwortlich', { selector: 'dt' });
    await waitFor(() =>
      expect(detailValue('Verantwortlich')).toHaveTextContent(
        'Nutzer: Erika Muster · Team: Netzbetrieb',
      ),
    );
    expect(detailValue('Verantwortlich')).not.toHaveTextContent('vom Asset');
  });

  it('shows only the kind without user:read and reads no names', async () => {
    const fetchMock = mockOwnerApi(ownedCI, ['ci:read']);
    renderDetail();

    await screen.findByText('Verantwortlich', { selector: 'dt' });
    await waitFor(() => expect(detailValue('Verantwortlich')).toHaveTextContent('Nutzer · Team'));
    expect(detailValue('Verantwortlich')).not.toHaveTextContent('Erika Muster');
    expect(ownerReads(fetchMock)).toEqual([]);
  });

  it('marks the owner of a CI with an asset as taken from the asset', async () => {
    mockOwnerApi(
      {
        ...ownedCI,
        owner_team_id: undefined,
        asset_id: ASSET,
        asset: { id: ASSET, asset_tag: 'INV-1', name: 'hw', status: 'assigned' },
      },
      ['ci:read', 'user:read'],
    );
    renderDetail();

    await screen.findByText('Verantwortlich', { selector: 'dt' });
    await waitFor(() =>
      expect(detailValue('Verantwortlich')).toHaveTextContent('Nutzer: Erika Muster (vom Asset)'),
    );
  });

  it('shows a dash without an owner and reads nothing', async () => {
    const fetchMock = mockOwnerApi({ ...ci, version: 2, tags: [] }, ['ci:read', 'user:read']);
    renderDetail();

    await screen.findByText('Verantwortlich', { selector: 'dt' });
    expect(detailValue('Verantwortlich')).toHaveTextContent('—');
    expect(ownerReads(fetchMock)).toEqual([]);
  });

  it('labels the owner in English', async () => {
    await act(async () => {
      await i18n.changeLanguage('en-US');
    });
    try {
      mockOwnerApi({ ...ownedCI, asset_id: ASSET }, ['ci:read']);
      renderDetail();

      await screen.findByText('Responsible', { selector: 'dt' });
      await waitFor(() =>
        expect(detailValue('Responsible')).toHaveTextContent('User · Team (from the asset)'),
      );
    } finally {
      await act(async () => {
        await i18n.changeLanguage('de-DE');
      });
    }
  });
});

/**
 * IPM-13, UI-14 (WP-919): the section "Netzwerk" follows the neighbors and
 * appears only with ipam:read; without it the page sends no request to
 * /cis/{id}/interfaces or /cis/{id}/addresses.
 */
describe('CIDetailPage network section (WP-919)', () => {
  function mockNetworkApi(permissions: string[]) {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const { pathname } = new URL(String(input), 'http://localhost');
      const ok = (body: unknown) =>
        new Response(JSON.stringify(body), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        });
      const empty = { data: [], total: 0, limit: 100, offset: 0, has_more: false };
      if (pathname.endsWith('/me/permissions')) return ok({ user_id: 'user-1', permissions });
      if (pathname.endsWith('/cis/ci-1/interfaces')) return ok(empty);
      if (pathname.endsWith('/cis/ci-1/addresses')) return ok(empty);
      if (pathname.endsWith('/vrfs')) return ok(empty);
      if (pathname.endsWith('/cis/ci-1')) {
        return ok({ ...ci, version: 1, tags: [], management_ip: '10.0.0.1' });
      }
      return new Response(JSON.stringify({ detail: 'unexpected' }), { status: 404 });
    });
    vi.stubGlobal('fetch', fetchMock);
    return fetchMock;
  }

  const ipamReads = (fetchMock: ReturnType<typeof mockNetworkApi>) =>
    fetchMock.mock.calls
      .map(([input]) => new URL(String(input), 'http://localhost').pathname)
      .filter((path) => /\/cis\/ci-1\/(interfaces|addresses)$/.test(path));

  it('shows the network section with ipam:read', async () => {
    const fetchMock = mockNetworkApi(['ci:read', 'ipam:read']);
    renderDetail();

    expect(await screen.findByRole('heading', { name: 'Netzwerk' })).toBeInTheDocument();
    expect(await screen.findByText('Keine Interfaces')).toBeInTheDocument();
    expect(ipamReads(fetchMock).length).toBeGreaterThan(0);
  });

  it('shows no network section and reads no interfaces without ipam:read', async () => {
    const fetchMock = mockNetworkApi(['ci:read']);
    renderDetail();

    await screen.findByRole('heading', { level: 1, name: 'core-sw-01' });
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(([input]) => String(input).includes('/me/permissions')),
      ).toBe(true),
    );
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(screen.queryByRole('heading', { name: 'Netzwerk' })).not.toBeInTheDocument();
    expect(ipamReads(fetchMock)).toEqual([]);
  });
});
