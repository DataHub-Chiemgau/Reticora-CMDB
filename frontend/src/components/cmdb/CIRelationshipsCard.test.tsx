/**
 * Regression tests for the relationship management surface.
 *
 * The backend has long exposed POST/DELETE and now PATCH for relationship
 * verification metadata, but the CI detail page rendered a read-only list, so
 * a discovered edge could never be confirmed or disputed and manual edges
 * could not be created. These tests pin the verify/delete wiring and the
 * create dialog: a double click creates one edge and closes the dialog
 * (F-314), an invisible or unknown endpoint is reported at the CI ID field in
 * the UI language (F-145). With a chosen client or site the card lists only
 * edges to CIs in that scope and names the hidden ones (E-87, F-169).
 * "Entfernen und unterdrücken" deletes nothing without a confirmation with
 * a reason; the suppressions of the CI are listed with reason and actor and
 * can be lifted (F-273). The create dialog offers no structural type,
 * which the server refuses with 422 (F-242).
 *
 * WP-323 (UI-15): the types come from /relationship-types, filtered by
 * direction and the allowed CI types (subtypes included) and labelled with
 * display_name/reverse_name; the other end is chosen with the ObjectPicker;
 * the attribute schema of the type is a form whose values go into
 * attributes of POST and PATCH, with 422 field errors at the attribute; the
 * edit dialog shows the edge history.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../../test/utils';
import {
  CIRelationshipsCard,
  STRUCTURAL_REL_TYPES,
  verificationBadgeVariant,
} from './CIRelationshipsCard';
import { useScopeStore } from '../../stores/scope';

const CI_ID = '11111111-1111-1111-1111-111111111111';
const OTHER_ID = '22222222-2222-2222-2222-222222222222';

function relationshipList(verificationState: string) {
  return {
    data: [
      {
        id: 'rel-1',
        organization_id: 'org-1',
        source_ci_id: CI_ID,
        target_ci_id: OTHER_ID,
        rel_type: 'depends_on',
        attributes: {},
        source: 'discovery',
        source_system: 'nmap',
        verification_state: verificationState,
        created_at: '2026-01-01T00:00:00Z',
        updated_at: '2026-01-01T00:00:00Z',
      },
    ],
    total: 1,
    limit: 50,
    offset: 0,
    has_more: false,
  };
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('verificationBadgeVariant', () => {
  it('maps the verification vocabulary onto distinct badge variants', () => {
    expect(verificationBadgeVariant('verified')).toBe('success');
    expect(verificationBadgeVariant('disputed')).toBe('danger');
    expect(verificationBadgeVariant('stale')).toBe('warning');
    expect(verificationBadgeVariant('unverified')).toBe('neutral');
    // An unknown or absent state must not be shown as confirmed.
    expect(verificationBadgeVariant(undefined)).toBe('neutral');
  });
});

describe('CIRelationshipsCard', () => {
  it('verifies an unverified relationship with a PATCH', async () => {
    const fetchMock = vi
      .fn()
      .mockImplementation(async (_input: RequestInfo | URL, init?: RequestInit) => {
        if (init?.method === 'PATCH') return json({ id: 'rel-1' });
        return json(relationshipList('unverified'));
      });
    vi.stubGlobal('fetch', fetchMock);

    renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);

    const verifyButton = await screen.findByRole('button', { name: /bestätigen|verify/i });
    fireEvent.click(verifyButton);

    await waitFor(() => {
      const patch = fetchMock.mock.calls.find((call) => call[1]?.method === 'PATCH');
      expect(patch).toBeDefined();
      expect(patch?.[0]).toBe('/api/v1/relationships/rel-1');
      expect(JSON.parse(patch?.[1]?.body as string)).toEqual({ verification_state: 'verified' });
    });
  });

  it('does not offer a verify action for an already verified relationship', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation(async () => json(relationshipList('verified'))),
    );

    renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);

    // Wait for the list to settle before asserting on absence.
    await screen.findByText('depends_on');
    expect(screen.queryByRole('button', { name: /bestätigen|verify/i })).toBeNull();
  });

  it('removes a discovery edge only after confirmation with a reason, with force and the reason (REL-04, F-273)', async () => {
    const fetchMock = vi
      .fn()
      .mockImplementation(async (_input: RequestInfo | URL, init?: RequestInit) => {
        if (init?.method === 'DELETE') return new Response(null, { status: 204 });
        return json(relationshipList('unverified'));
      });
    vi.stubGlobal('fetch', fetchMock);
    const deletes = () => fetchMock.mock.calls.filter((call) => call[1]?.method === 'DELETE');

    renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);

    // Without confirmation nothing is deleted: the button opens the dialog,
    // whose confirmation needs a reason, and cancelling deletes nothing.
    fireEvent.click(await screen.findByRole('button', { name: 'Entfernen und unterdrücken' }));
    let dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent(
      `Die Discovery-Kante depends_on zu ${OTHER_ID} wird entfernt.`,
    );
    const confirm = within(dialog).getByRole('button', { name: 'Entfernen und unterdrücken' });
    expect(confirm).toBeDisabled();
    fireEvent.change(within(dialog).getByLabelText('Grund'), { target: { value: '   ' } });
    expect(confirm).toBeDisabled();
    fireEvent.submit(confirm.closest('form')!);
    fireEvent.click(within(dialog).getByRole('button', { name: 'Abbrechen' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(deletes()).toHaveLength(0);

    // With confirmation and a reason the edge is deleted with force and
    // the trimmed reason; the dialog closes.
    fireEvent.click(screen.getByRole('button', { name: 'Entfernen und unterdrücken' }));
    dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByLabelText('Grund')).toHaveValue('');
    fireEvent.change(within(dialog).getByLabelText('Grund'), {
      target: { value: '  Falscher Nachbar & Altlast  ' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Entfernen und unterdrücken' }));

    await waitFor(() => expect(deletes()).toHaveLength(1));
    const url = new URL(String(deletes()[0]?.[0]), 'http://localhost');
    expect(url.pathname).toBe('/api/v1/relationships/rel-1');
    expect(Object.fromEntries(url.searchParams)).toEqual({
      force: 'true',
      reason: 'Falscher Nachbar & Altlast',
    });
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });

  it('names a refused reason in the dialog and keeps it open (F-273)', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation(async (_input: RequestInfo | URL, init?: RequestInit) => {
        if (init?.method === 'DELETE') {
          return json(
            {
              type: 'https://reticora.io/problems/422',
              title: 'Unprocessable Entity',
              status: 422,
              detail: 'reason: too long',
              violations: [{ field: 'reason', detail: 'too long' }],
            },
            422,
          );
        }
        return json(relationshipList('unverified'));
      }),
    );

    renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Entfernen und unterdrücken' }));
    const dialog = await screen.findByRole('dialog');
    const input = within(dialog).getByLabelText('Grund');
    fireEvent.change(input, { target: { value: 'zu lang' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Entfernen und unterdrücken' }));

    await waitFor(() => expect(input).toHaveAttribute('aria-invalid', 'true'));
    expect(input).toHaveAccessibleDescription('Der Grund hat höchstens 1000 Zeichen.');
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });

  it('deletes a manual edge without force and without a dialog', async () => {
    const list = relationshipList('verified');
    list.data[0]!.source = 'manual';
    const fetchMock = vi
      .fn()
      .mockImplementation(async (_input: RequestInfo | URL, init?: RequestInit) => {
        if (init?.method === 'DELETE') return new Response(null, { status: 204 });
        return json(list);
      });
    vi.stubGlobal('fetch', fetchMock);

    renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Löschen' }));

    await waitFor(() => {
      const del = fetchMock.mock.calls.find((call) => call[1]?.method === 'DELETE');
      expect(del?.[0]).toBe('/api/v1/relationships/rel-1');
    });
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('lists the suppressions of the CI with reason and actor and lifts one (F-273)', async () => {
    const ACTOR = '44444444-4444-4444-4444-444444444444';
    let lifted = false;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      if (url.pathname === '/api/v1/relationship-suppressions/sup-1' && init?.method === 'DELETE') {
        lifted = true;
        return new Response(null, { status: 204 });
      }
      if (url.pathname === '/api/v1/relationship-suppressions') {
        const data = lifted
          ? []
          : [
              {
                id: 'sup-1',
                organization_id: 'org-1',
                source_ci_id: CI_ID,
                target_ci_id: OTHER_ID,
                rel_type: 'connected_to',
                reason: 'Falscher Nachbar',
                created_by: ACTOR,
                created_at: '2026-01-01T00:00:00Z',
              },
            ];
        return json({ data, total: data.length, limit: 100, offset: 0, has_more: false });
      }
      return json(relationshipList('verified'));
    });
    vi.stubGlobal('fetch', fetchMock);

    renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);
    await screen.findByText('depends_on');
    // The list is read only when shown.
    expect(
      fetchMock.mock.calls.some((call) => String(call[0]).includes('relationship-suppressions')),
    ).toBe(false);

    fireEvent.click(screen.getByRole('button', { name: 'Unterdrückte Beziehungen anzeigen' }));
    const list = await screen.findByRole('list', { name: 'Unterdrückte Beziehungen' });
    const listCall = fetchMock.mock.calls.find((call) =>
      String(call[0]).startsWith('/api/v1/relationship-suppressions'),
    );
    expect(new URL(String(listCall?.[0]), 'http://localhost').searchParams.get('ci_id')).toBe(
      CI_ID,
    );
    const item = within(list).getByRole('listitem');
    expect(item).toHaveTextContent('connected_to');
    expect(item).toHaveTextContent(OTHER_ID);
    expect(item).toHaveTextContent('Grund: Falscher Nachbar');
    expect(item).toHaveTextContent(`Unterdrückt von ${ACTOR}`);

    fireEvent.click(within(item).getByRole('button', { name: 'Aufheben' }));
    await waitFor(() => expect(lifted).toBe(true));
    expect(await screen.findByText('Keine unterdrückten Beziehungen.')).toBeInTheDocument();
  });

  it('surfaces a rejected mutation instead of failing silently', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation(async (_input: RequestInfo | URL, init?: RequestInit) => {
        if (init?.method === 'PATCH') {
          return json({ status: 422, detail: 'invalid verification_state' }, 422);
        }
        return json(relationshipList('unverified'));
      }),
    );

    renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);

    fireEvent.click(await screen.findByRole('button', { name: /bestätigen|verify/i }));

    await waitFor(() => {
      expect(screen.getByRole('alert')).toHaveTextContent('invalid verification_state');
    });
  });
});

describe('CIRelationshipsCard location edges (F-237)', () => {
  it('names a located_in target by its path in the location tree, not by its id', async () => {
    const node = (id: string, name: string, parent: string | null, children: unknown[] = []) => ({
      id,
      organization_id: 'org-1',
      kind: 'site',
      name,
      parent_id: parent,
      children,
    });
    const tree = [
      node('loc-site', 'HQ', null, [
        node('loc-room', 'Raum 1', 'loc-site', [node('loc-rack', 'Rack A', 'loc-room')]),
      ]),
    ];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input), 'http://localhost');
        if (url.pathname === '/api/v1/locations/tree') return json({ data: tree });
        return json({
          data: [
            {
              id: 'rel-loc',
              organization_id: 'org-1',
              source_ci_id: CI_ID,
              target_ci_id: '',
              target_location_id: 'loc-rack',
              rel_type: 'located_in',
              attributes: {},
              source: 'projection',
              projected: true,
              verification_state: 'verified',
              created_at: '2026-01-01T00:00:00Z',
              updated_at: '2026-01-01T00:00:00Z',
            },
          ],
          total: 1,
          limit: 50,
          offset: 0,
          has_more: false,
        });
      }),
    );

    renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);

    expect(await screen.findByText('HQ › Raum 1 › Rack A')).toBeInTheDocument();
    expect(screen.queryByText('loc-rack')).not.toBeInTheDocument();
  });
});

describe('CIRelationshipsCard typed edge dialog (WP-323)', () => {
  const T_SERVER = 'aaaa0000-0000-4000-8000-000000000001';
  const T_BLADE = 'aaaa0000-0000-4000-8000-000000000002';
  const T_APP = 'aaaa0000-0000-4000-8000-000000000003';
  const T_PDU = 'aaaa0000-0000-4000-8000-000000000004';
  const AT = '2026-01-01T00:00:00Z';

  function ciType(id: string, key: string, parent?: string) {
    return {
      id,
      organization_id: 'org-1',
      key,
      name: key,
      display_name: { de: key, en: key },
      is_builtin: false,
      is_system: false,
      is_active: true,
      is_logical: false,
      is_observable: false,
      schema_version: 1,
      version: 1,
      parent_type_id: parent,
      capabilities: {},
      allowed_relationship_types: [],
      ui_schema: {},
      created_at: AT,
      updated_at: AT,
    };
  }
  // blade_server is a subtype of server: a type allowing server allows it.
  const ciTypes = [
    ciType(T_SERVER, 'server'),
    ciType(T_BLADE, 'blade_server', T_SERVER),
    ciType(T_APP, 'application'),
    ciType(T_PDU, 'pdu'),
  ];

  function relType(key: string, extra: Record<string, unknown> = {}) {
    return {
      id: `rt-${key}`,
      key,
      display_name: key,
      reverse_name: key,
      directed: true,
      category: 'logical',
      impact_direction: 'source_depends_on_target',
      redundant: false,
      allowed_source_types: [],
      allowed_target_types: [],
      cardinality: 'many_to_many',
      is_system: true,
      metadata_schema: [],
      created_at: AT,
      updated_at: AT,
      ...extra,
    };
  }
  const relTypes = [
    relType('depends_on', { display_name: 'hängt ab von', reverse_name: 'wird benötigt von' }),
    // Structural types are projected; the dialog never offers them.
    relType('contains', { display_name: 'enthält', reverse_name: 'ist enthalten in' }),
    relType('mounted_in', { display_name: 'montiert in', reverse_name: 'montiert' }),
    relType('powered_by', {
      display_name: 'wird versorgt von',
      reverse_name: 'versorgt',
      allowed_source_types: ['server'],
      allowed_target_types: ['pdu'],
    }),
    relType('monitored_by', {
      organization_id: 'org-1',
      is_system: false,
      display_name: 'überwacht von',
      reverse_name: 'überwacht',
      allowed_source_types: ['application'],
    }),
    relType('backs_up', {
      organization_id: 'org-1',
      is_system: false,
      display_name: 'sichert',
      reverse_name: 'wird gesichert von',
      allowed_source_types: ['server'],
      allowed_target_types: ['application'],
      metadata_schema: [
        {
          key: 'schedule',
          display_name: { de: 'Zeitplan', en: 'Schedule' },
          data_type: 'text',
          required: true,
        },
        {
          key: 'retention_days',
          display_name: { de: 'Aufbewahrung (Tage)', en: 'Retention (days)' },
          data_type: 'integer',
          default: 30,
        },
      ],
    }),
  ];

  const page = <T,>(data: T[]) => ({
    data,
    total: data.length,
    limit: 50,
    offset: 0,
    has_more: false,
  });

  function edge(id: string, source: string, target: string, extra: object = {}) {
    return {
      id,
      organization_id: 'org-1',
      source_ci_id: source,
      target_ci_id: target,
      rel_type: 'depends_on',
      attributes: {},
      source: 'manual',
      verification_state: 'verified',
      created_at: AT,
      updated_at: AT,
      ...extra,
    };
  }

  interface Server {
    edges?: object[];
    create?: () => Response;
    update?: () => Response;
    changes?: object[];
  }

  /** stubServer answers the card, the dialog, the picker and the writes. */
  function stubServer({ edges = [], create, update, changes = [] }: Server = {}) {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      const path = url.pathname;
      if (path === '/api/v1/relationships' && init?.method === 'POST') {
        return create ? create() : json({ id: 'rel-new' }, 201);
      }
      if (/^\/api\/v1\/relationships\/[^/]+$/.test(path) && init?.method === 'PATCH') {
        return update ? update() : json({ id: 'rel-1' });
      }
      if (/^\/api\/v1\/relationships\/[^/]+\/changes$/.test(path)) return json(page(changes));
      if (path === `/api/v1/cis/${CI_ID}/relationships`) return json(page(edges));
      if (path === `/api/v1/cis/${CI_ID}`) {
        return json({
          id: CI_ID,
          organization_id: 'org-1',
          ci_type_id: T_BLADE,
          name: 'blade-01',
          status: 'active',
          attributes: {},
          created_at: AT,
          updated_at: AT,
        });
      }
      if (path === '/api/v1/cis') {
        return json(
          page([
            {
              id: OTHER_ID,
              organization_id: 'org-1',
              ci_type_id: T_APP,
              name: 'web-app',
              status: 'active',
              attributes: {},
              created_at: AT,
              updated_at: AT,
            },
          ]),
        );
      }
      if (path === '/api/v1/ci-types') return json(page(ciTypes));
      if (path === '/api/v1/relationship-types') return json(page(relTypes));
      return json(page([]));
    });
    vi.stubGlobal('fetch', fetchMock);
    return fetchMock;
  }

  const calls = (fetchMock: ReturnType<typeof vi.fn>, method: string) =>
    fetchMock.mock.calls.filter((call) => (call[1] as RequestInit | undefined)?.method === method);

  /** openCreateDialog opens the dialog once the catalog and this CI are read. */
  async function openCreateDialog(direction?: 'outgoing' | 'incoming') {
    fireEvent.click(await screen.findByRole('button', { name: /Beziehung hinzufügen/ }));
    const dialog = await screen.findByRole('dialog');
    if (direction) {
      fireEvent.change(within(dialog).getByLabelText('Richtung'), {
        target: { value: direction },
      });
    }
    // The catalog, the CI types and the type of this CI are read: the types
    // the CI type does not allow drop out.
    await waitFor(() => {
      const offered = offeredTypes(dialog).map(([value]) => value);
      expect(offered).toContain('depends_on');
      expect(offered).not.toContain(direction === 'incoming' ? 'powered_by' : 'monitored_by');
    });
    return dialog;
  }

  function offeredTypes(dialog: HTMLElement) {
    const select = within(dialog).getByLabelText('Beziehungstyp') as HTMLSelectElement;
    return Array.from(select.options).map((option) => [option.value, option.textContent]);
  }

  /** pickOther finds the other end by name in the ObjectPicker. */
  async function pickOther(dialog: HTMLElement, name = 'web') {
    fireEvent.change(within(dialog).getByRole('combobox', { name: 'Gegenstelle' }), {
      target: { value: name },
    });
    fireEvent.click(await within(dialog).findByRole('option', { name: /web-app/ }));
  }

  it('offers the types of /relationship-types by direction and allowed CI types, subtypes included, without structural ones (F-242)', async () => {
    stubServer();
    renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);

    let dialog = await openCreateDialog();
    // Outgoing: this blade_server is the source. powered_by allows server
    // (the parent type), backs_up too; monitored_by only application. The
    // label is display_name.
    expect(offeredTypes(dialog)).toEqual([
      ['depends_on', 'hängt ab von'],
      ['powered_by', 'wird versorgt von'],
      ['backs_up', 'sichert'],
    ]);
    const structural = STRUCTURAL_REL_TYPES;
    expect(offeredTypes(dialog).filter(([value]) => structural.includes(value!))).toEqual([]);

    // Incoming: this CI is the target; the label is reverse_name. The
    // organization type monitored_by allows every target.
    fireEvent.change(within(dialog).getByLabelText('Richtung'), {
      target: { value: 'incoming' },
    });
    expect(offeredTypes(dialog)).toEqual([
      ['depends_on', 'wird benötigt von'],
      ['monitored_by', 'überwacht'],
    ]);
    fireEvent.click(within(dialog).getByRole('button', { name: 'Abbrechen' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());

    dialog = await openCreateDialog();
    expect(within(dialog).getByLabelText('Beziehungstyp')).toHaveValue('depends_on');
  });

  it('labels the edges of the list with display_name or reverse_name', async () => {
    stubServer({
      edges: [
        edge('rel-out', CI_ID, OTHER_ID),
        edge('rel-in', OTHER_ID, CI_ID, { rel_type: 'backs_up' }),
      ],
    });
    renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);

    const list = await screen.findByRole('list', { name: 'Beziehungen' });
    await waitFor(() => expect(list).toHaveTextContent('hängt ab von'));
    expect(list).toHaveTextContent('wird gesichert von');
    expect(list).not.toHaveTextContent('depends_on');
    expect(list).not.toHaveTextContent('backs_up');
  });

  it('chooses the other end with the ObjectPicker among the allowed CI types and sends the attributes of the schema', async () => {
    const fetchMock = stubServer();
    renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);
    const dialog = await openCreateDialog();
    expect(within(dialog).queryByLabelText('CI-ID der Gegenstelle')).toBeNull();
    // A type without schema shows no attribute form.
    expect(within(dialog).queryByRole('group', { name: 'Attribute' })).toBeNull();

    fireEvent.change(within(dialog).getByLabelText('Beziehungstyp'), {
      target: { value: 'backs_up' },
    });
    // The schema of backs_up: labels from display_name, the default prefilled.
    const attributes = within(dialog).getByRole('group', { name: 'Attribute' });
    expect(within(attributes).getByLabelText('Zeitplan *')).toHaveValue('');
    expect(within(attributes).getByLabelText('Aufbewahrung (Tage)')).toHaveValue(30);
    fireEvent.change(within(attributes).getByLabelText('Zeitplan *'), {
      target: { value: 'nightly' },
    });

    await pickOther(dialog);
    // The picker searched among the allowed target types of backs_up
    // (application) only.
    const search = fetchMock.mock.calls
      .map((call) => new URL(String(call[0]), 'http://localhost'))
      .find((url) => url.pathname === '/api/v1/cis');
    expect(search?.searchParams.get('search')).toBe('web');
    expect(search?.searchParams.get('filter[ci_type_id][in]')).toBe(T_APP);

    fireEvent.click(within(dialog).getByRole('button', { name: 'Anlegen' }));
    await waitFor(() => expect(calls(fetchMock, 'POST')).toHaveLength(1));
    expect(JSON.parse(calls(fetchMock, 'POST')[0]?.[1]?.body as string)).toEqual({
      source_ci_id: CI_ID,
      target_ci_id: OTHER_ID,
      rel_type: 'backs_up',
      source: 'manual',
      verification_state: 'verified',
      attributes: { schedule: 'nightly', retention_days: 30 },
    });
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });

  it('shows a 422 field error of the schema at the attribute', async () => {
    stubServer({
      create: () =>
        json(
          {
            type: 'https://reticora.io/problems/422',
            title: 'Unprocessable Entity',
            status: 422,
            detail: 'schedule: does not match the pattern',
            violations: [{ field: 'schedule', detail: 'Wert passt nicht zum Muster' }],
          },
          422,
        ),
    });
    renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);
    const dialog = await openCreateDialog();
    fireEvent.change(within(dialog).getByLabelText('Beziehungstyp'), {
      target: { value: 'backs_up' },
    });
    fireEvent.change(within(dialog).getByLabelText('Zeitplan *'), {
      target: { value: 'sometimes' },
    });
    await pickOther(dialog);
    fireEvent.click(within(dialog).getByRole('button', { name: 'Anlegen' }));

    const input = within(dialog).getByLabelText('Zeitplan *');
    await waitFor(() => expect(input).toHaveAttribute('aria-invalid', 'true'));
    expect(input).toHaveAccessibleDescription('Wert passt nicht zum Muster');
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });

  it.each([
    [
      'the type pair',
      { field: 'target_ci_id', detail: 'the type of the target CI is not in allowed_target_types' },
      'Der Beziehungstyp erlaubt diese Kombination von CI-Typen nicht.',
    ],
    [
      'the cardinality',
      {
        field: 'rel_type',
        detail: 'the edge would exceed the cardinality of the relationship type',
      },
      'Die Kante würde die Kardinalität des Beziehungstyps überschreiten.',
    ],
  ])('shows the 422 of %s in the dialog (WP-294)', async (_case, violation, message) => {
    stubServer({
      create: () =>
        json(
          {
            type: 'https://reticora.io/problems/422',
            title: 'Unprocessable Entity',
            status: 422,
            detail: `${violation.field}: ${violation.detail}`,
            violations: [violation],
          },
          422,
        ),
    });
    renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);
    const dialog = await openCreateDialog();
    await pickOther(dialog);
    fireEvent.click(within(dialog).getByRole('button', { name: 'Anlegen' }));

    expect(await within(dialog).findByText(message)).toBeInTheDocument();
    expect(document.body).not.toHaveTextContent(violation.detail);
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });

  it.each([
    ['outgoing', 'target_ci_id', { source_ci_id: CI_ID, target_ci_id: OTHER_ID }],
    ['incoming', 'source_ci_id', { source_ci_id: OTHER_ID, target_ci_id: CI_ID }],
  ] as const)('names a 422 on %s %s in the UI language (F-145)', async (direction, field, ends) => {
    const end = field === 'target_ci_id' ? 'target' : 'source';
    const fetchMock = stubServer({
      create: () =>
        json(
          {
            type: 'https://reticora.io/problems/422',
            title: 'Unprocessable Entity',
            status: 422,
            detail: `${field}: ${end} CI not found`,
            violations: [{ field, detail: `${end} CI not found` }],
          },
          422,
        ),
    });
    renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);
    const dialog = await openCreateDialog(direction);
    await pickOther(dialog);
    fireEvent.click(within(dialog).getByRole('button', { name: 'Anlegen' }));

    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      'Die Gegenstelle wurde nicht gefunden oder ist nicht sichtbar.',
    );
    // The server's English detail is not shown.
    expect(document.body).not.toHaveTextContent(/CI not found/);
    expect(calls(fetchMock, 'POST')).toHaveLength(1);
    expect(JSON.parse(calls(fetchMock, 'POST')[0]?.[1]?.body as string)).toMatchObject(ends);
  });

  it('creates the edge once on a double click and closes the dialog (F-314)', async () => {
    // The server stores the first create; a repeat is refused as a
    // conflict, as the unique edge key does on the real stack.
    let created = 0;
    const fetchMock = stubServer({
      create: () => {
        created++;
        if (created > 1) {
          return json(
            {
              type: 'https://reticora.io/problems/409',
              title: 'Conflict',
              status: 409,
              detail: 'a record with the same unique value already exists',
            },
            409,
          );
        }
        return json({ id: 'rel-new', source_ci_id: CI_ID, target_ci_id: OTHER_ID }, 201);
      },
    });
    renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);
    const dialog = await openCreateDialog();
    await pickOther(dialog);
    const submit = within(dialog).getByRole('button', { name: 'Anlegen' });
    // Both clicks land before React renders the pending state that disables
    // the button: the worst case of a real double click.
    act(() => {
      submit.click();
      submit.click();
    });

    await waitFor(() => {
      const open = screen.queryByRole('dialog');
      expect(open === null || within(open).queryByRole('alert') !== null).toBe(true);
    });
    const open = screen.queryByRole('dialog');
    expect({
      posts: calls(fetchMock, 'POST').length,
      dialogOpen: open !== null,
      error: open ? (within(open).queryByRole('alert')?.textContent ?? null) : null,
    }).toEqual({ posts: 1, dialogOpen: false, error: null });
    expect(JSON.parse(calls(fetchMock, 'POST')[0]?.[1]?.body as string)).toMatchObject({
      source_ci_id: CI_ID,
      target_ci_id: OTHER_ID,
      rel_type: 'depends_on',
      source: 'manual',
    });
  });

  it('edits the attributes of the schema with a PATCH and shows the edge history, newest first', async () => {
    const ACTOR = '44444444-4444-4444-4444-444444444444';
    const change = (id: string, kind: string, at: string, extra: object = {}) => ({
      id,
      relationship_id: 'rel-1',
      source_ci_id: CI_ID,
      target_ci_id: OTHER_ID,
      rel_type: 'backs_up',
      change_kind: kind,
      changed_fields: [],
      before: null,
      after: null,
      actor_type: 'user',
      source: 'ui',
      observed_at: at,
      ...extra,
    });
    const fetchMock = stubServer({
      edges: [
        edge('rel-1', CI_ID, OTHER_ID, {
          rel_type: 'backs_up',
          attributes: { schedule: 'nightly', provenance: { actor: ACTOR } },
        }),
      ],
      changes: [
        change('ch-1', 'created', '2026-01-01T08:00:00Z', { actor_id: ACTOR }),
        change('ch-2', 'updated', '2026-01-02T08:00:00Z', {
          changed_fields: ['attributes', 'notes'],
          actor_type: 'api_key',
          source: 'api',
        }),
      ],
    });
    renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Bearbeiten' }));
    const dialog = await screen.findByRole('dialog');

    const history = await within(dialog).findByRole('list', { name: 'Kantenhistorie' });
    expect(
      fetchMock.mock.calls.some(
        (call) =>
          new URL(String(call[0]), 'http://localhost').pathname ===
          '/api/v1/relationships/rel-1/changes',
      ),
    ).toBe(true);
    const rows = within(history).getAllByRole('listitem');
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveTextContent('Geändert');
    expect(rows[0]).toHaveTextContent('Geändert: Attribute, Notiz');
    expect(rows[0]).toHaveTextContent('Von API-Schlüssel über API');
    expect(rows[1]).toHaveTextContent('Angelegt');
    expect(rows[1]).toHaveTextContent(`Von ${ACTOR} über Oberfläche`);

    const schedule = await within(dialog).findByLabelText('Zeitplan *');
    expect(schedule).toHaveValue('nightly');
    fireEvent.change(schedule, { target: { value: 'weekly' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(calls(fetchMock, 'PATCH')).toHaveLength(1));
    const patch = calls(fetchMock, 'PATCH')[0];
    expect(patch?.[0]).toBe('/api/v1/relationships/rel-1');
    // provenance is the server's (REL-08) and not sent back.
    expect(JSON.parse(patch?.[1]?.body as string)).toEqual({
      verification_state: 'verified',
      notes: '',
      attributes: { schedule: 'weekly' },
    });
    // The mutation invalidates the history: it is read again.
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.filter((call) => String(call[0]).includes('/rel-1/changes')).length,
      ).toBeGreaterThan(1),
    );
  });

  it('shows a 422 field error of the schema at the attribute of the edit dialog', async () => {
    stubServer({
      edges: [edge('rel-1', CI_ID, OTHER_ID, { rel_type: 'backs_up', attributes: {} })],
      update: () =>
        json(
          {
            type: 'https://reticora.io/problems/422',
            title: 'Unprocessable Entity',
            status: 422,
            detail: 'retention_days: must be an integer',
            violations: [{ field: 'retention_days', detail: 'keine ganze Zahl' }],
          },
          422,
        ),
    });
    renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Bearbeiten' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(await within(dialog).findByLabelText('Zeitplan *'), {
      target: { value: 'daily' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    const input = within(dialog).getByLabelText('Aufbewahrung (Tage)');
    await waitFor(() => expect(input).toHaveAttribute('aria-invalid', 'true'));
    expect(input).toHaveAccessibleDescription('keine ganze Zahl');
  });
});

describe('CIRelationshipsCard under a chosen scope (E-87)', () => {
  const CLIENT_A = 'aaaaaaaa-0000-4000-8000-000000000001';
  const CLIENT_B = 'aaaaaaaa-0000-4000-8000-000000000002';
  const SITE_A1 = 'bbbbbbbb-0000-4000-8000-000000000001';
  const SITE_A2 = 'bbbbbbbb-0000-4000-8000-000000000002';
  const IN_A = '33333333-3333-3333-3333-333333333331';
  const IN_A2 = '33333333-3333-3333-3333-333333333332';
  const IN_B = '33333333-3333-3333-3333-333333333333';
  const IN_B2 = '33333333-3333-3333-3333-333333333334';
  const UNREADABLE = '33333333-3333-3333-3333-333333333335';

  const others: Record<string, { client_id?: string; site_id?: string }> = {
    [IN_A]: { client_id: CLIENT_A, site_id: SITE_A1 },
    [IN_A2]: { client_id: CLIENT_A, site_id: SITE_A2 },
    [IN_B]: { client_id: CLIENT_B },
    [IN_B2]: { client_id: CLIENT_B },
  };

  function edge(id: string, source: string, target: string, extra: object = {}) {
    return {
      id,
      organization_id: 'org-1',
      source_ci_id: source,
      target_ci_id: target,
      rel_type: 'depends_on',
      attributes: {},
      source: 'manual',
      verification_state: 'verified',
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-01T00:00:00Z',
      ...extra,
    };
  }

  const edges = [
    edge('rel-a', CI_ID, IN_A),
    edge('rel-a2', IN_A2, CI_ID),
    edge('rel-b', CI_ID, IN_B),
    edge('rel-b2', IN_B2, CI_ID),
    edge('rel-x', CI_ID, UNREADABLE),
    edge('rel-loc', CI_ID, '', { target_location_id: 'loc-1', projected: true }),
  ];

  function stubServer() {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://localhost');
      if (url.pathname === `/api/v1/cis/${CI_ID}/relationships`)
        return json({ data: edges, total: edges.length, limit: 50, offset: 0, has_more: false });
      const id = url.pathname.replace('/api/v1/cis/', '');
      const other = others[id];
      if (other)
        return json({
          id,
          organization_id: 'org-1',
          ci_type_id: 'server',
          name: id,
          status: 'active',
          attributes: {},
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:00:00Z',
          ...other,
        });
      return json({ detail: 'not found' }, 404);
    });
    vi.stubGlobal('fetch', fetchMock);
    return fetchMock;
  }

  const listedIds = () =>
    screen.queryAllByRole('link').map((link) => link.getAttribute('href')?.replace('/cmdb/', ''));

  beforeEach(() => {
    window.localStorage.clear();
    useScopeStore.getState().reset();
  });
  afterEach(() => useScopeStore.getState().reset());

  it('lists only edges to CIs of the chosen client and names the hidden ones', async () => {
    stubServer();
    useScopeStore.setState({ clientId: CLIENT_A, siteId: '' });
    renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);

    expect(await screen.findByText('2 außerhalb des Bereichs ausgeblendet')).toBeInTheDocument();
    // Both directions count; an edge to a location and one whose other CI
    // cannot be read are not known to lie outside and stay listed.
    expect(listedIds().sort()).toEqual([IN_A, IN_A2, UNREADABLE].sort());
    expect(screen.getByText('loc-1')).toBeInTheDocument();
    expect(screen.queryByText('Außerhalb des Bereichs')).toBeNull();

    // The toggle shows all edges and marks the outside ones.
    fireEvent.click(screen.getByRole('button', { name: 'Alle anzeigen' }));
    expect(listedIds().sort()).toEqual([IN_A, IN_A2, IN_B, IN_B2, UNREADABLE].sort());
    expect(screen.getAllByText('Außerhalb des Bereichs')).toHaveLength(2);
    expect(screen.getByText('2 außerhalb des Bereichs ausgeblendet')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Nur Bereich anzeigen' }));
    expect(listedIds()).not.toContain(IN_B);
  });

  it('narrows to the chosen site as well', async () => {
    stubServer();
    useScopeStore.setState({ clientId: CLIENT_A, siteId: SITE_A1 });
    renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);

    expect(await screen.findByText('3 außerhalb des Bereichs ausgeblendet')).toBeInTheDocument();
    expect(listedIds().sort()).toEqual([IN_A, UNREADABLE].sort());
  });

  it('lists every edge and reads no CI without a selection', async () => {
    const fetchMock = stubServer();
    renderWithProviders(<CIRelationshipsCard ciId={CI_ID} />);

    await waitFor(() => expect(listedIds()).toHaveLength(5));
    expect(screen.queryByText(/außerhalb des Bereichs/)).toBeNull();
    expect(screen.queryByRole('button', { name: 'Alle anzeigen' })).toBeNull();
    // The location edge reads the location tree for its name (F-237), no CI.
    expect(
      fetchMock.mock.calls
        .map((call) => String(call[0]))
        .filter((u) => u.startsWith('/api/v1/cis')),
    ).toEqual([`/api/v1/cis/${CI_ID}/relationships`]);
  });
});
