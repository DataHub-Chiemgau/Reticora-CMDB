import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import type { TopologyGraphData } from '../api/client';
import { TopologyPage, buildGraphModel, computeOutageImpact } from './TopologyPage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';

vi.mock('../components/graph/TopologyGraph', () => ({
  TopologyGraph: ({ nodes, edges }: { nodes: unknown[]; edges: unknown[] }) => (
    <div data-testid="topology-graph">{`${nodes.length}/${edges.length}`}</div>
  ),
}));

const graph: TopologyGraphData = {
  nodes: [
    { id: 'ci-1', name: 'core-sw-01', ci_type: 'switch', status: 'active' },
    { id: 'ci-2', name: 'srv-01', ci_type: 'server', status: 'maintenance' },
  ],
  edges: [
    { id: 'e-1', source_ci_id: 'ci-1', target_ci_id: 'ci-2', rel_type: 'connected_to' },
    { id: 'e-2', source_ci_id: 'ci-1', target_ci_id: 'ci-missing', rel_type: 'depends_on' },
  ],
};

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('buildGraphModel', () => {
  it('maps nodes and drops edges with unknown endpoints', () => {
    const model = buildGraphModel(graph);

    expect(model.nodes).toEqual([
      { id: 'ci-1', label: 'core-sw-01', color: '#16a34a' },
      { id: 'ci-2', label: 'srv-01', color: '#d97706' },
    ]);
    expect(model.edges).toEqual([{ source: 'ci-1', target: 'ci-2', label: 'connected_to' }]);
  });

  it('collapses parallel edges between the same pair into one labelled edge', () => {
    const model = buildGraphModel({
      nodes: [
        { id: 'ci-1', name: 'core-sw-01', ci_type: 'switch', status: 'active' },
        { id: 'ci-2', name: 'srv-01', ci_type: 'server', status: 'active' },
      ],
      edges: [
        { id: 'e-1', source_ci_id: 'ci-1', target_ci_id: 'ci-2', rel_type: 'connected_to' },
        { id: 'e-2', source_ci_id: 'ci-1', target_ci_id: 'ci-2', rel_type: 'powers' },
        { id: 'e-3', source_ci_id: 'ci-1', target_ci_id: 'ci-2', rel_type: 'powers' },
        { id: 'e-4', source_ci_id: 'ci-2', target_ci_id: 'ci-1', rel_type: 'depends_on' },
      ],
    });

    expect(model.edges).toEqual([
      { source: 'ci-1', target: 'ci-2', label: 'connected_to, powers' },
      { source: 'ci-2', target: 'ci-1', label: 'depends_on' },
    ]);
  });

  it('falls back to the default color for unknown status values', () => {
    const model = buildGraphModel({
      nodes: [{ id: 'ci-3', name: 'unknown', ci_type: 'other', status: 'planned' }],
      edges: [],
    });

    expect(model.nodes[0]?.color).toBe('#4f46e5');
  });
});

describe('computeOutageImpact', () => {
  const edges = [
    { source: 'ups-1', target: 'sw-1' },
    { source: 'sw-1', target: 'srv-1' },
    { source: 'sw-1', target: 'srv-2' },
    { source: 'srv-1', target: 'vm-1' },
  ];

  it('marks all transitively dependent nodes as impacted', () => {
    expect(computeOutageImpact('ups-1', edges)).toEqual(
      new Set(['sw-1', 'srv-1', 'srv-2', 'vm-1']),
    );
    expect(computeOutageImpact('sw-1', edges)).toEqual(new Set(['srv-1', 'srv-2', 'vm-1']));
  });

  it('returns an empty set for leaf nodes', () => {
    expect(computeOutageImpact('vm-1', edges)).toEqual(new Set());
  });

  it('terminates on cyclic graphs', () => {
    const cyclic = [
      { source: 'a', target: 'b' },
      { source: 'b', target: 'a' },
    ];
    expect(computeOutageImpact('a', cyclic)).toEqual(new Set(['b']));
  });
});

describe('TopologyPage', () => {
  it('renders the graph and an accessible node list', async () => {
    stubFetchRoutes({ '/topology': graph });

    renderWithProviders(<TopologyPage />, { route: '/topology' });

    expect(await screen.findByTestId('topology-graph')).toHaveTextContent('2/1');
    expect(screen.getByRole('button', { name: 'core-sw-01' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'srv-01' })).toBeInTheDocument();
  });

  it('shows an empty state when the tenant has no topology yet', async () => {
    stubFetchRoutes({ '/topology': { nodes: [], edges: [] } });

    renderWithProviders(<TopologyPage />, { route: '/topology' });

    expect(await screen.findByText('Keine Topologiedaten')).toBeInTheDocument();
    expect(screen.queryByTestId('topology-graph')).not.toBeInTheDocument();
  });

  it('passes the focused CI from the query string to the API', async () => {
    const fetchMock = stubFetchRoutes({ '/topology': graph });

    renderWithProviders(<TopologyPage />, { route: '/topology?root=ci-1&depth=3' });

    await screen.findByTestId('topology-graph');

    const url = String(fetchMock.mock.calls[0]?.[0]);
    expect(url).toContain('root_ci_id=ci-1');
    expect(url).toContain('depth=3');
  });

  it('shows the outage impact panel when a failure is simulated', async () => {
    stubFetchRoutes({ '/topology': graph });

    renderWithProviders(<TopologyPage />, { route: '/topology?simulate=ci-1' });

    expect(await screen.findByText('Ausfallsimulation')).toBeInTheDocument();
    expect(
      screen.getByText(/Fällt „core-sw-01“ aus, sind 1 weitere CIs betroffen/),
    ).toBeInTheDocument();
  });
});
