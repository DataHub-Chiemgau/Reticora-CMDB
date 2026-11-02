import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import type { TopologyGraphData } from '../api/client';
import type { RelationshipType } from '../api/cmdb';
import {
  TopologyPage,
  buildGraphModel,
  computeOutageImpact,
  impactDirections,
  parseSimulated,
} from './TopologyPage';
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

// The impact directions of the built-in catalogue (CH27, E-18) the tests use.
const directions = impactDirections([
  relType('connected_to', 'undirected'),
  relType('powered_by', 'source_depends_on_target'),
  relType('powers', 'target_depends_on_source'),
  relType('hosted_on', 'source_depends_on_target'),
  relType('depends_on', 'source_depends_on_target'),
  relType('contains', 'target_depends_on_source'),
  relType('monitors', 'none'),
]);

function relType(key: string, impact: RelationshipType['impact_direction']): RelationshipType {
  return {
    id: `rt-${key}`,
    key,
    display_name: key,
    reverse_name: key,
    directed: impact !== 'undirected',
    category: 'logical',
    impact_direction: impact,
    redundant: false,
    allowed_source_types: [],
    allowed_target_types: [],
    cardinality: 'many_to_many',
    is_system: true,
  };
}

// F-254 retest: the edges as they are stored (server powered_by pdu,
// vm hosted_on server), source -> target. The failure runs against the
// stored direction: a PDU failure reaches the server and the VM on it, a
// VM failure reaches nothing. The old walk along source -> target gave
// pdu -> {} and vm -> {pdu, server}.
const stored = [
  { source: 'server', target: 'pdu', label: 'powered_by' },
  { source: 'vm', target: 'server', label: 'hosted_on' },
];

describe('computeOutageImpact', () => {
  it('follows the impact direction of the relationship types (F-254)', () => {
    expect(computeOutageImpact('pdu', stored, directions)).toEqual(new Set(['server', 'vm']));
    expect(computeOutageImpact('vm', stored, directions)).toEqual(new Set());
    expect(computeOutageImpact('server', stored, directions)).toEqual(new Set(['vm']));
  });

  it('follows a target_depends_on_source type along the edge', () => {
    const edges = [
      { source: 'pdu', target: 'server', label: 'powers' },
      { source: 'rack', target: 'pdu', label: 'contains' },
    ];
    expect(computeOutageImpact('rack', edges, directions)).toEqual(new Set(['pdu', 'server']));
    expect(computeOutageImpact('server', edges, directions)).toEqual(new Set());
  });

  it('carries a failure over connected_to both ways and never over a type without impact', () => {
    const edges = [
      { source: 'sw-1', target: 'srv-1', label: 'connected_to' },
      { source: 'mon-1', target: 'srv-2', label: 'monitors' },
    ];
    expect(computeOutageImpact('sw-1', edges, directions)).toEqual(new Set(['srv-1']));
    expect(computeOutageImpact('srv-1', edges, directions)).toEqual(new Set(['sw-1']));
    expect(computeOutageImpact('srv-2', edges, directions)).toEqual(new Set());
    expect(computeOutageImpact('mon-1', edges, directions)).toEqual(new Set());
  });

  it('reads every type of a collapsed edge and narrows the walk to the facet', () => {
    const edges = [
      { source: 'server', target: 'pdu', label: 'connected_to, powered_by' },
      { source: 'vm', target: 'server', label: 'hosted_on' },
    ];
    expect(computeOutageImpact('pdu', edges, directions)).toEqual(new Set(['server', 'vm']));
    expect(computeOutageImpact('pdu', edges, directions, 'powered_by')).toEqual(
      new Set(['server']),
    );
    expect(computeOutageImpact('pdu', edges, directions, 'depends_on')).toEqual(new Set());
  });

  it('reads a type without a known direction as source_depends_on_target, as the server', () => {
    const edges = [{ source: 'app', target: 'db', label: 'custom_link' }];
    expect(computeOutageImpact('db', edges, directions)).toEqual(new Set(['app']));
    expect(computeOutageImpact('app', edges, directions)).toEqual(new Set());
  });

  it('terminates on cyclic graphs', () => {
    const cyclic = [
      { source: 'a', target: 'b', label: 'depends_on' },
      { source: 'b', target: 'a', label: 'depends_on' },
    ];
    expect(computeOutageImpact('a', cyclic, directions)).toEqual(new Set(['b']));
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

// A PDU feeds a server that hosts a VM, stored as server powered_by pdu and
// vm hosted_on server (source -> target).
const powerGraph: TopologyGraphData = {
  nodes: [
    { id: 'ci-pdu', name: 'pdu-01', ci_type: 'pdu', status: 'active' },
    { id: 'ci-server', name: 'srv-01', ci_type: 'server', status: 'active' },
    { id: 'ci-vm', name: 'vm-01', ci_type: 'vm', status: 'active' },
  ],
  edges: [
    { id: 'e-1', source_ci_id: 'ci-server', target_ci_id: 'ci-pdu', rel_type: 'powered_by' },
    { id: 'e-2', source_ci_id: 'ci-vm', target_ci_id: 'ci-server', rel_type: 'hosted_on' },
  ],
};

const relationshipTypes = {
  data: [
    relType('connected_to', 'undirected'),
    relType('powered_by', 'source_depends_on_target'),
    relType('hosted_on', 'source_depends_on_target'),
  ],
  total: 3,
  limit: 100,
  offset: 0,
  has_more: false,
};

function problem(status: number) {
  return new Response(JSON.stringify({ title: 'Error', status, detail: 'failed' }), {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  });
}

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

  it('shows the server analysis of a simulated failure without a provisional note', async () => {
    stubFetchRoutes({
      '/impact?ci_ids=': {
        failed_cis: [powerGraph.nodes[0]],
        impacted: [powerGraph.nodes[1], powerGraph.nodes[2]],
        count: 2,
      },
      '/topology': powerGraph,
      '/ci-types': ciTypes,
      '/relationship-types': relationshipTypes,
    });

    renderWithProviders(<TopologyPage />, { route: '/topology?simulate=ci-pdu' });

    expect(
      await screen.findByText(/Fällt „pdu-01“ aus, sind 2 weitere CIs betroffen/),
    ).toBeInTheDocument();
    expect(screen.queryByText(/Vorläufiges Ergebnis/)).not.toBeInTheDocument();
    expect(screen.queryByText('Vorschau')).not.toBeInTheDocument();
  });

  // F-326: a failed server analysis is an error, never a client-side result.
  // The relationship types load, so a client walk could compute a set; the
  // page shows none, neither as badges nor as a count.
  it('shows an error instead of a client-side impact when /impact fails', async () => {
    const cases = [
      {
        status: 422,
        response: new Response(
          JSON.stringify({
            type: 'urn:reticora:problem:invalid-field',
            title: 'Unprocessable Entity',
            status: 422,
            detail: 'ci_ids: at most 100 CIs per analysis',
            field: 'ci_ids',
          }),
          { status: 422, headers: { 'Content-Type': 'application/problem+json' } },
        ),
        message: /höchstens 100 CIs je Analyse/,
      },
      {
        status: 500,
        response: problem(500),
        message: /Die Analyse des Servers ist nicht verfügbar/,
      },
    ];
    for (const { status, response, message } of cases) {
      const fetchMock = stubFetchRoutes({
        '/impact?ci_ids=': response,
        '/topology': powerGraph,
        '/ci-types': ciTypes,
        '/relationship-types': relationshipTypes,
      });

      const { unmount } = renderWithProviders(<TopologyPage />, {
        route: '/topology?simulate=ci-pdu',
      });

      const alert = await screen.findByRole('alert');
      expect(alert, `/impact ${status}`).toHaveTextContent(
        'Die Auswirkung konnte nicht berechnet werden.',
      );
      expect(alert, `/impact ${status}`).toHaveTextContent(message);
      // The English detail of the server stays out of the page.
      expect(alert, `/impact ${status}`).not.toHaveTextContent(/per analysis|failed/);
      // The relationship types are there: a client walk could have run.
      await waitFor(() =>
        expect(
          fetchMock.mock.calls.some((call) => String(call[0]).includes('/relationship-types')),
        ).toBe(true),
      );
      const panel = screen
        .getByRole('heading', { name: 'Ausfallsimulation' })
        .closest('section') as HTMLElement;
      expect(within(panel).queryAllByRole('listitem'), `/impact ${status}`).toEqual([]);
      expect(within(panel).queryByText('srv-01'), `/impact ${status}`).not.toBeInTheDocument();
      expect(within(panel).queryByText('vm-01'), `/impact ${status}`).not.toBeInTheDocument();
      expect(
        screen.queryByText(/weitere CIs betroffen/),
        `/impact ${status}`,
      ).not.toBeInTheDocument();
      expect(screen.queryByText(/Vorschau|Vorläufig/), `/impact ${status}`).not.toBeInTheDocument();
      expect(within(alert).getByRole('button', { name: 'Erneut versuchen' })).toBeInTheDocument();
      unmount();
      vi.unstubAllGlobals();
    }
  });

  it('marks the client-side result as a preview while the server analysis loads (F-254)', async () => {
    const pending = new Promise<Response>(() => {});
    const respond = (body: unknown) =>
      new Response(JSON.stringify(body), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes('/impact?ci_ids=')) return pending;
        if (url.includes('/topology')) return respond(powerGraph);
        if (url.includes('/relationship-types')) return respond(relationshipTypes);
        if (url.includes('/ci-types')) return respond(ciTypes);
        return new Response('{}', { status: 404 });
      }),
    );

    const first = renderWithProviders(<TopologyPage />, {
      route: '/topology?simulate=ci-server',
    });

    expect(
      await screen.findByText(/Fällt „srv-01“ aus, sind 1 weitere CIs betroffen/),
    ).toBeInTheDocument();
    expect(screen.getByText(/die Analyse des Servers läuft noch/)).toBeInTheDocument();
    expect(screen.getByText('Vorschau')).toBeInTheDocument();
    first.unmount();

    // The preview follows the impact direction: a PDU failure reaches the
    // server and the VM on it.
    const second = renderWithProviders(<TopologyPage />, { route: '/topology?simulate=ci-pdu' });
    expect(
      await screen.findByText(/Fällt „pdu-01“ aus, sind 2 weitere CIs betroffen/),
    ).toBeInTheDocument();
    expect(screen.getByText('Vorschau')).toBeInTheDocument();
    const panel = screen
      .getByRole('heading', { name: 'Ausfallsimulation' })
      .closest('section') as HTMLElement;
    const impacted = within(panel)
      .getAllByRole('listitem')
      .map((item) => item.textContent);
    expect(impacted.sort()).toEqual(['srv-01', 'vm-01']);
    second.unmount();

    // The VM has no dependents: its failure impacts nothing, not its host.
    renderWithProviders(<TopologyPage />, { route: '/topology?simulate=ci-vm' });
    expect(
      await screen.findByText(/Fällt „vm-01“ aus, sind 0 weitere CIs betroffen/),
    ).toBeInTheDocument();
    expect(screen.getByText(/Dieses CI hat keine abhängigen CIs/)).toBeInTheDocument();
  });

  it('renders the type label of a node', async () => {
    stubFetchRoutes({
      '/topology': {
        nodes: [
          { id: 'ci-1', name: 'core-sw-01', ci_type: 'type-switch', status: 'active' },
          { id: 'ci-2', name: 'srv-01', ci_type: 'type-server', status: 'active' },
          { id: 'ci-3', name: 'odd-01', ci_type: 'type-unknown', status: 'active' },
        ],
        edges: [],
      },
      '/ci-types': ciTypes,
    });

    renderWithProviders(<TopologyPage />, { route: '/topology' });

    const list = (await screen.findByRole('heading', { name: 'Knotenliste' })).closest(
      'section',
    ) as HTMLElement;
    const row = (name: string) =>
      within(list).getByRole('button', { name }).closest('li') as HTMLElement;
    await waitFor(() => expect(within(row('srv-01')).getByText('Server')).toBeInTheDocument());
    expect(within(row('core-sw-01')).getByText('Switch')).toBeInTheDocument();
    expect(within(row('srv-01')).queryByText('type-server')).not.toBeInTheDocument();
    // A type the catalogue does not list keeps its id.
    expect(within(row('odd-01')).getByText('type-unknown')).toBeInTheDocument();
  });

  it('says that the impact cannot be computed without /impact and relationship types', async () => {
    stubFetchRoutes({
      '/impact?ci_ids=': problem(500),
      '/relationship-types': problem(403),
      '/topology': powerGraph,
      '/ci-types': ciTypes,
    });

    renderWithProviders(<TopologyPage />, { route: '/topology?simulate=ci-pdu' });

    expect(
      await screen.findByText('Die Auswirkung konnte nicht berechnet werden.'),
    ).toBeInTheDocument();
    expect(screen.getByText('Ausfallsimulation')).toBeInTheDocument();
    expect(screen.queryByText(/weitere CIs betroffen/)).not.toBeInTheDocument();
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
    // b depends on a, c on b, e on d.
    const edges = [
      { source: 'b', target: 'a', label: 'depends_on' },
      { source: 'c', target: 'b', label: 'depends_on' },
      { source: 'e', target: 'd', label: 'depends_on' },
    ];
    expect([...computeOutageImpact(['a', 'd'], edges, directions)].sort()).toEqual(['b', 'c', 'e']);
    expect([...computeOutageImpact(['a', 'b'], edges, directions)]).toEqual(['c']);
  });
});
