import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import { Route, Routes } from 'react-router-dom';
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

  it('shows an error state with a retry action when the CI cannot be loaded', async () => {
    stubFetchRoutes({});

    renderDetail();

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('Das Configuration Item konnte nicht geladen werden');
    expect(screen.getByRole('button', { name: 'Erneut versuchen' })).toBeInTheDocument();
  });
});
