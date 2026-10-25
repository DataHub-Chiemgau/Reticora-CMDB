import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import type { TopologyGraphData } from '../api/client';
import { TopologyPage, buildGraphModel, computeOutageImpact, parseSimulated } from './TopologyPage';
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

const ciTypes = {
  data: [
    {
      id: 'type-switch',
      key: 'switch',
      name: 'switch',
      display_name: { de: 'Switch', en: 'Switch' },
    },
    {
      id: 'type-server',
      key: 'server',
      name: 'server',
      display_name: { de: 'Server', en: 'Server' },
    },
  ],
  total: 2,
  limit: 50,
  offset: 0,
  has_more: false,
};

describe('TopologyPage', () => {
  it('renders the graph and an accessible node list', async () => {
    stubFetchRoutes({ '/topology': graph, '/ci-types': ciTypes });

    renderWithProviders(<TopologyPage />, { route: '/topology' });

    expect(await screen.findByTestId('topology-graph')).toHaveTextContent('2/1');
    expect(screen.getByRole('button', { name: 'core-sw-01' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'srv-01' })).toBeInTheDocument();
  });

  it('shows an empty state when the tenant has no topology yet', async () => {
    stubFetchRoutes({ '/topology': { nodes: [], edges: [] }, '/ci-types': ciTypes });

    renderWithProviders(<TopologyPage />, { route: '/topology' });

    expect(await screen.findByText('Keine Topologiedaten')).toBeInTheDocument();
    expect(screen.queryByTestId('topology-graph')).not.toBeInTheDocument();
  });

  it('passes the focused CI from the query string to the API', async () => {
    const fetchMock = stubFetchRoutes({ '/topology': graph, '/ci-types': ciTypes });

    renderWithProviders(<TopologyPage />, { route: '/topology?root=ci-1&depth=3' });

    await screen.findByTestId('topology-graph');

    const url = fetchMock.mock.calls
      .map((call) => String(call[0]))
      .find((entry) => entry.includes('/topology'));
    expect(url).toContain('root_ci_id=ci-1');
    expect(url).toContain('depth=3');
  });

  it('shows the outage impact panel when a failure is simulated', async () => {
    stubFetchRoutes({ '/topology': graph, '/ci-types': ciTypes });

    renderWithProviders(<TopologyPage />, { route: '/topology?simulate=ci-1' });

    expect(await screen.findByText('Ausfallsimulation')).toBeInTheDocument();
    expect(
      screen.getByText(/Fällt „core-sw-01“ aus, sind 1 weitere CIs betroffen/),
    ).toBeInTheDocument();
  });

  it('filters by CI type id', async () => {
    const fetchMock = stubFetchRoutes({ '/topology': graph, '/ci-types': ciTypes });

    renderWithProviders(<TopologyPage />, { route: '/topology?type=type-server' });

    await screen.findByTestId('topology-graph');
    expect(await screen.findByRole('option', { name: 'Server' })).toHaveValue('type-server');
    const topologyUrl = fetchMock.mock.calls
      .map((call) => String(call[0]))
      .find((url) => url.includes('/topology'));
    expect(topologyUrl).toContain('ci_type=type-server');
  });

  it('asks for a filter when the topology is too large', async () => {
    stubFetchRoutes({
      '/topology': new Response(
        JSON.stringify({ title: 'Unprocessable Entity', status: 422, detail: 'too many' }),
        { status: 422, headers: { 'Content-Type': 'application/problem+json' } },
      ),
      '/ci-types': ciTypes,
    });

    renderWithProviders(<TopologyPage />, { route: '/topology' });

    expect(await screen.findByText(/mehr als 2.000 CIs/)).toBeInTheDocument();
  });

  it('says that a start CI is not found or not visible and leads back to the full view (F-161)', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      const respond = (body: unknown, status = 200) =>
        new Response(JSON.stringify(body), {
          status,
          headers: {
            'Content-Type': status === 200 ? 'application/json' : 'application/problem+json',
          },
        });
      if (url.includes('/topology')) {
        return url.includes('root_ci_id=')
          ? respond({ title: 'Not Found', status: 404, detail: 'ci not found' }, 404)
          : respond(graph);
      }
      if (url.includes('/ci-types')) return respond(ciTypes);
      return respond({ detail: 'unexpected' }, 404);
    });
    vi.stubGlobal('fetch', fetchMock);

    renderWithProviders(<TopologyPage />, { route: '/topology?root=ci-gone&depth=3' });

    expect(await screen.findByText('CI nicht gefunden oder nicht sichtbar')).toBeInTheDocument();
    expect(
      screen.getByText(
        'Das Start-CI dieser Ansicht existiert nicht mehr oder liegt außerhalb Ihres Sichtbereichs.',
      ),
    ).toBeInTheDocument();
    // Not the generic load error, and not the English detail of the server.
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    expect(screen.queryByText('Die Topologie konnte nicht geladen werden')).not.toBeInTheDocument();
    expect(screen.queryByText('ci not found')).not.toBeInTheDocument();
    expect(screen.queryByTestId('topology-graph')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Zur Gesamtansicht' }));

    expect(await screen.findByTestId('topology-graph')).toHaveTextContent('2/1');
    expect(screen.queryByText('CI nicht gefunden oder nicht sichtbar')).not.toBeInTheDocument();
    const topologyUrls = fetchMock.mock.calls
      .map((call) => String(call[0]))
      .filter((url) => url.includes('/topology'));
    expect(topologyUrls[0]).toContain('root_ci_id=ci-gone');
    expect(topologyUrls[topologyUrls.length - 1]).not.toContain('root_ci_id');
  });

  it('reports a failed topology request with a start CI as a load error, not as not found', async () => {
    stubFetchRoutes({
      '/topology': new Response(
        JSON.stringify({
          title: 'Internal Error',
          status: 500,
          detail: 'the request could not be completed',
        }),
        { status: 500, headers: { 'Content-Type': 'application/problem+json' } },
      ),
      '/ci-types': ciTypes,
    });

    renderWithProviders(<TopologyPage />, { route: '/topology?root=ci-1' });

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Die Topologie konnte nicht geladen werden',
    );
    expect(screen.getByRole('button', { name: 'Erneut versuchen' })).toBeInTheDocument();
    expect(screen.queryByText('CI nicht gefunden oder nicht sichtbar')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Zur Gesamtansicht' })).not.toBeInTheDocument();
  });

  it('opens a start dialog with site and type above 2,000 CIs (UI-06)', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      const respond = (body: unknown, status = 200) =>
        new Response(JSON.stringify(body), {
          status,
          headers: { 'Content-Type': 'application/json' },
        });
      if (url.includes('/topology')) {
        return url.includes('site_id=site-1')
          ? respond(graph)
          : respond({ title: 'Unprocessable Entity', status: 422, detail: 'too many' }, 422);
      }
      if (url.includes('/sites')) {
        return respond({
          data: [{ id: 'site-1', name: 'RZ Nord' }],
          total: 1,
          limit: 200,
          offset: 0,
          has_more: false,
        });
      }
      if (url.includes('/ci-types')) return respond(ciTypes);
      return respond({ detail: 'unexpected' }, 404);
    });
    vi.stubGlobal('fetch', fetchMock);

    renderWithProviders(<TopologyPage />, { route: '/topology' });

    const dialog = await screen.findByRole('form', { name: 'Ausschnitt wählen' });
    expect(dialog).toBeInTheDocument();
    expect(screen.queryByTestId('topology-graph')).not.toBeInTheDocument();
    const show = screen.getByRole('button', { name: 'Topologie anzeigen' });
    expect(show).toBeDisabled();

    const siteSelect = screen.getByLabelText('Standort', { selector: 'select' });
    await screen.findAllByRole('option', { name: 'RZ Nord' });
    fireEvent.change(siteSelect, { target: { value: 'site-1' } });
    fireEvent.click(show);

    expect(await screen.findByTestId('topology-graph')).toBeInTheDocument();
    expect(fetchMock.mock.calls.some((call) => String(call[0]).includes('site_id=site-1'))).toBe(
      true,
    );
  });

  it('simulates several failing CIs jointly (IMP-05)', async () => {
    const fetchMock = stubFetchRoutes({
      '/impact?ci_ids=': {
        failed_cis: graph.nodes,
        impacted: [{ id: 'ci-3', name: 'app-01', ci_type: 'application', status: 'active' }],
        count: 1,
      },
      '/topology': graph,
      '/ci-types': ciTypes,
    });

    renderWithProviders(<TopologyPage />, { route: '/topology?simulate=ci-1,ci-2' });

    expect(
      await screen.findByText(/Fallen 2 CIs \(core-sw-01, srv-01\) gemeinsam aus, sind 1 weitere/),
    ).toBeInTheDocument();
    const impactUrl = fetchMock.mock.calls
      .map((call) => String(call[0]))
      .find((url) => url.includes('/impact'));
    expect(impactUrl).toContain('ci_ids=ci-1%2Cci-2');
    // The page needs the ids only: it asks for the compact answer (NFR-02).
    expect(impactUrl).toContain('fields=name%2Cci_type%2Cstatus%2Cheuristic%2Chops%2Cvia%2Crelay');
    // Both failed CIs can be taken out of the simulation one by one.
    expect(screen.getAllByRole('button', { name: 'Aus Simulation entfernen' })).toHaveLength(2);
  });

  it('adds a further failure to a running simulation', async () => {
    stubFetchRoutes({
      '/impact?ci_ids=': { failed_cis: [], impacted: [], count: 0 },
      '/topology': graph,
      '/ci-types': ciTypes,
    });

    renderWithProviders(<TopologyPage />, { route: '/topology?simulate=ci-1' });

    fireEvent.click(await screen.findByRole('button', { name: 'Ausfall hinzufügen' }));
    await waitFor(() =>
      expect(screen.getAllByRole('button', { name: 'Aus Simulation entfernen' })).toHaveLength(2),
    );
  });
});

describe('multiple failures', () => {
  it('parses the failed CIs of the URL', () => {
    expect(parseSimulated('ci-1, ci-2,,ci-1')).toEqual(['ci-1', 'ci-2']);
    expect(parseSimulated(null)).toEqual([]);
  });

  it('unites the impact of several failures without the failed CIs', () => {
    const edges = [
      { source: 'a', target: 'b' },
      { source: 'b', target: 'c' },
      { source: 'd', target: 'e' },
    ];
    expect([...computeOutageImpact(['a', 'd'], edges)].sort()).toEqual(['b', 'c', 'e']);
    expect([...computeOutageImpact(['a', 'b'], edges)]).toEqual(['c']);
  });
});
