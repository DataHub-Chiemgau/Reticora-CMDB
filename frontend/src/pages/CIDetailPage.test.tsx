import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, screen, waitFor } from '@testing-library/react';
import { Route, Routes } from 'react-router-dom';
import i18n from '../i18n';
import { CIDetailPage, buildNeighborList } from './CIDetailPage';
import type { TopologyGraphData } from '../api/client';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';

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

  it('shows time and location and the unified history (CI-06)', async () => {
    stubFetchRoutes({
      '/history/ci/ci-1': {
        data: [
          {
            id: 'a-1',
            entity_type: 'ci',
            entity_id: 'ci-1',
            change_type: 'ci.updated',
            created_at: '2026-01-03T10:00:00Z',
            source: 'audit',
          },
          {
            id: 'c-1',
            entity_type: 'ci',
            entity_id: 'ci-1',
            change_type: 'update',
            field_name: 'name',
            created_at: '2026-01-02T10:00:00Z',
            source: 'ci_change',
            site_id: 'site-1',
          },
        ],
        total: 2,
        limit: 50,
        offset: 0,
        has_more: false,
      },
      '/cis/ci-1/relationships': { data: [], total: 0, limit: 25, offset: 0, has_more: false },
      '/topology/cis/ci-1/neighbors': { nodes: [], edges: [] },
      '/cis/ci-1': {
        ...ci,
        location_id: 'loc-1',
        site_id: 'site-1',
        first_seen_at: '2026-01-01T08:00:00Z',
      },
    });

    renderDetail();

    expect(await screen.findByRole('heading', { name: 'core-sw-01' })).toBeInTheDocument();
    expect(screen.getByText('Site')).toBeInTheDocument();
    expect(screen.getAllByText('site-1').length).toBeGreaterThan(0);
    expect(screen.getByText('2026-01-01T08:00:00Z')).toBeInTheDocument();
    const history = await screen.findByRole('list', { name: 'Historie' });
    expect(history).toHaveTextContent('update · name');
    expect(history).toHaveTextContent('Audit');
    expect(history).toHaveTextContent('Site site-1');
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

  /** Answers by exact API path; types are what GET /ci-types lists. */
  function stubBackend(types: unknown[] = [serverType]) {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const raw = typeof input === 'string' ? input : input.toString();
      const path = new URL(raw, 'http://localhost').pathname.replace(/^\/api\/v1/, '');
      const method = (init?.method ?? 'GET').toUpperCase();
      if (path === '/ci-types') {
        return json({ data: types, total: types.length, limit: 200, offset: 0, has_more: false });
      }
      if (path === `/cis/${CI_ID}`) {
        return json(serverCI);
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
