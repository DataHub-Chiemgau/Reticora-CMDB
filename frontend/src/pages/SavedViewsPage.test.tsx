/**
 * Regression tests for creating and deleting personal saved views.
 *
 * The backend has always exposed POST and DELETE /saved-views, and a
 * `useSaveView` hook existed, but no component ever called it. There was no
 * create and no delete affordance anywhere in the UI, so "Meine Ansichten" was
 * permanently empty and its empty-state string was unconditionally true.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../test/utils';
import { SavedViewsPage } from './SavedViewsPage';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const preset = {
  id: '',
  organization_id: '',
  name: 'Assets with warranty expiring within 90 days',
  entity_kind: 'asset',
  filter_spec: { warranty_within_days: 90 },
  shared: true,
};

const me = 'user-1';

const ownView = {
  id: 'view-1',
  organization_id: 'org-1',
  owner_id: me,
  name: 'My servers',
  entity_kind: 'ci',
  filter_spec: { status: 'active' },
  shared: false,
};

/**
 * Routes each request by URL/method so a single mock serves the whole page.
 * search answers the n-th POST /search/query (0-based); by default it is an
 * empty result.
 */
const roles = [
  { id: 'role-1', organization_id: 'org-1', name: 'Auditoren', is_system: false, permissions: [] },
  { id: 'role-2', organization_id: 'org-1', name: 'Netzwerk', is_system: false, permissions: [] },
];

function mockApi(
  overrides: {
    views?: unknown[];
    search?: (n: number) => Response;
    me?: string | null;
    permissions?: string[];
    patch?: () => Response;
  } = {},
) {
  let searches = 0;
  return vi.fn().mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? 'GET';
    if (url.includes('/me/permissions'))
      return overrides.me === null
        ? json({ title: 'Internal Server Error', status: 500 }, 500)
        : json({
            user_id: overrides.me ?? me,
            permissions: overrides.permissions ?? ['saved_view:write', 'role:read'],
          });
    if (url.includes('/roles')) return json({ data: roles, total: roles.length });
    if (url.includes('/attribute-definitions')) return json({ data: [] });
    if (url.includes('/relationship-types')) return json({ data: [], total: 0 });
    if (url.includes('/saved-views/presets')) return json({ data: [preset] });
    if (url.includes('/search/query'))
      return overrides.search ? overrides.search(searches++) : json({ data: [], total: 0 });
    if (url.includes('/saved-views') && method === 'POST') return json({ id: 'new' }, 201);
    if (url.includes('/saved-views/') && method === 'PATCH')
      return overrides.patch
        ? overrides.patch()
        : json({ ...ownView, ...JSON.parse(String(init?.body)) });
    if (url.includes('/saved-views') && method === 'DELETE')
      return new Response(null, { status: 204 });
    return json({ data: overrides.views ?? [], total: 0, limit: 50, offset: 0, has_more: false });
  });
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('SavedViewsPage', () => {
  it('saves the currently running filter as a personal view', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    vi.spyOn(window, 'prompt').mockReturnValue('Warranty watch');

    renderWithProviders(<SavedViewsPage />);

    // Nothing is running yet, so there is nothing to save.
    expect(screen.queryByRole('button', { name: /speichern|save as view/i })).toBeNull();

    fireEvent.click(await screen.findByRole('button', { name: new RegExp(preset.name, 'i') }));

    // entity_kind is a sibling column of filter_spec, so it must be folded
    // into the query; otherwise an asset view runs against the ci table and
    // its asset-only predicates are silently dropped.
    await waitFor(() => {
      const q = fetchMock.mock.calls.find((call) => String(call[0]).includes('/search/query'));
      expect(q).toBeTruthy();
      expect(JSON.parse(String(q![1]!.body)).entity_kind).toBe('asset');
    });

    const saveButton = await screen.findByRole('button', { name: /speichern|save as view/i });
    fireEvent.click(saveButton);

    await waitFor(() => {
      const post = fetchMock.mock.calls.find(
        (call) => call[1]?.method === 'POST' && String(call[0]).includes('/saved-views'),
      );
      expect(post).toBeTruthy();
      const body = JSON.parse(String(post![1]!.body));
      expect(body.name).toBe('Warranty watch');
      // entity_kind is lifted out of the spec into its own column.
      expect(body.entity_kind).toBe('asset');
      expect(body.filter_spec).toEqual({ warranty_within_days: 90 });
      expect(body.filter_spec.entity_kind).toBeUndefined();
    });
  });

  it('does not save when the name prompt is cancelled', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    vi.spyOn(window, 'prompt').mockReturnValue(null);

    renderWithProviders(<SavedViewsPage />);
    fireEvent.click(await screen.findByRole('button', { name: new RegExp(preset.name, 'i') }));
    fireEvent.click(await screen.findByRole('button', { name: /speichern|save as view/i }));

    await waitFor(() => expect(window.prompt).toHaveBeenCalled());
    expect(
      fetchMock.mock.calls.some(
        (call) => call[1]?.method === 'POST' && String(call[0]).includes('/saved-views'),
      ),
    ).toBe(false);
  });

  it('deletes a personal view', async () => {
    const fetchMock = mockApi({ views: [ownView] });
    vi.stubGlobal('fetch', fetchMock);

    renderWithProviders(<SavedViewsPage />);

    // The delete control carries an explicit aria-label ("Löschen: My
    // servers") so it is distinguishable from the run button.
    const del = await screen.findByRole('button', {
      name: new RegExp(`(löschen|delete): ${ownView.name}`, 'i'),
    });
    fireEvent.click(del);

    await waitFor(() => {
      const call = fetchMock.mock.calls.find((c) => c[1]?.method === 'DELETE');
      expect(call).toBeTruthy();
      expect(String(call![0])).toContain(`/saved-views/${ownView.id}`);
    });
  });

  // E-86 (WP-518): only the owner changes or deletes a view; the server
  // answers 404 to anybody else, so the page offers no delete for views
  // shared with the caller by somebody else.
  it('offers delete only for own views, not for views shared by others', async () => {
    const foreign = {
      ...ownView,
      id: 'view-2',
      owner_id: 'user-2',
      name: 'Team servers',
      shared: true,
    };
    const roleShared = {
      ...ownView,
      id: 'view-3',
      owner_id: 'user-3',
      name: 'Role servers',
      shared_with_roles: ['r1'],
    };
    vi.stubGlobal('fetch', mockApi({ views: [ownView, foreign, roleShared] }));

    renderWithProviders(<SavedViewsPage />);

    expect(
      await screen.findByRole('button', {
        name: new RegExp(`(löschen|delete): ${ownView.name}`, 'i'),
      }),
    ).toBeInTheDocument();
    // The foreign views are listed and can be run, but not deleted.
    expect(screen.getByRole('button', { name: /^Team servers/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /^Role servers/ })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /(löschen|delete): Team servers/i })).toBeNull();
    expect(screen.queryByRole('button', { name: /(löschen|delete): Role servers/i })).toBeNull();
  });

  it('offers no delete while the caller is unknown', async () => {
    vi.stubGlobal('fetch', mockApi({ views: [ownView], me: null }));

    renderWithProviders(<SavedViewsPage />);

    expect(await screen.findByRole('button', { name: /^My servers/ })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /(löschen|delete): My servers/i })).toBeNull();
  });

  // F-055 (WP-460): the retry of a failed search was a no-op, and a missing
  // read right looked like a generic error.
  it('retries a failed search with a second request', async () => {
    const fetchMock = mockApi({
      search: (n) =>
        n === 0
          ? json({ title: 'Internal Server Error', status: 500 }, 500)
          : json({ data: [{ id: 'a1', name: 'Asset one', entity_kind: 'asset' }], total: 1 }),
    });
    vi.stubGlobal('fetch', fetchMock);
    const searches = () =>
      fetchMock.mock.calls.filter((call) => String(call[0]).includes('/search/query')).length;

    renderWithProviders(<SavedViewsPage />);
    fireEvent.click(await screen.findByRole('button', { name: new RegExp(preset.name, 'i') }));

    fireEvent.click(await screen.findByRole('button', { name: /erneut versuchen|retry/i }));

    expect(await screen.findByRole('link', { name: 'Asset one' })).toBeInTheDocument();
    expect(searches()).toBe(2);
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('shows a permission hint when the search answers 403', async () => {
    const fetchMock = mockApi({
      search: () =>
        json({ title: 'Forbidden', status: 403, detail: 'missing permission asset:read' }, 403),
    });
    vi.stubGlobal('fetch', fetchMock);

    renderWithProviders(<SavedViewsPage />);
    fireEvent.click(await screen.findByRole('button', { name: new RegExp(preset.name, 'i') }));

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('Keine Berechtigung für diese Ansicht');
    expect(alert).toHaveTextContent(/Leserecht/);
    // A missing right is not repeated: no retry, no automatic second request.
    expect(screen.queryByRole('button', { name: /erneut versuchen|retry/i })).toBeNull();
    expect(
      fetchMock.mock.calls.filter((call) => String(call[0]).includes('/search/query')),
    ).toHaveLength(1);
  });

  it('runs the same view again on a second click', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    const searches = () =>
      fetchMock.mock.calls.filter((call) => String(call[0]).includes('/search/query')).length;

    renderWithProviders(<SavedViewsPage />);
    const button = await screen.findByRole('button', { name: new RegExp(preset.name, 'i') });
    fireEvent.click(button);
    await waitFor(() => expect(searches()).toBe(1));
    await screen.findByText('Keine Treffer.');
    fireEvent.click(button);
    await waitFor(() => expect(searches()).toBe(2));
  });

  it('marks a view shared with roles', async () => {
    vi.stubGlobal(
      'fetch',
      mockApi({
        views: [
          {
            ...ownView,
            sort: [{ field: 'name' }],
            columns: ['name'],
            shared_with_roles: ['r1', 'r2'],
          },
        ],
      }),
    );

    renderWithProviders(<SavedViewsPage />);

    expect(await screen.findByText('für 2 Rollen')).toBeInTheDocument();
  });
});

/**
 * Retest F-336 (WP-555, VIE-01): sort and columns of a view were stored but
 * never used, the page saved only name, entity kind and filter, and sharing
 * was possible through the API only.
 */
describe('SavedViewsPage sort, columns and sharing (F-336)', () => {
  const searchBodies = (fetchMock: ReturnType<typeof mockApi>) =>
    fetchMock.mock.calls
      .filter((call) => String(call[0]).includes('/search/query'))
      .map((call) => JSON.parse(String(call[1]!.body)) as Record<string, unknown>);

  const sortedView = {
    ...ownView,
    name: 'Server absteigend',
    sort: [{ field: 'name', dir: 'desc' }],
    columns: ['name', 'status'],
    shared: false,
    shared_with_roles: ['role-1'],
  };

  // The server answers in the order of the saved sort; the rows carry the
  // saved columns only.
  const sortedRows = {
    data: [
      {
        id: 'ci-c',
        entity_kind: 'ci',
        name: 'srv-c',
        summary: 'summary-c',
        columns: { name: 'srv-c', status: 'maintenance' },
      },
      {
        id: 'ci-b',
        entity_kind: 'ci',
        name: 'srv-b',
        summary: 'summary-b',
        columns: { name: 'srv-b', status: 'active' },
      },
      {
        id: 'ci-a',
        entity_kind: 'ci',
        name: 'srv-a',
        summary: 'summary-a',
        columns: { name: 'srv-a', status: 'active' },
      },
    ],
    total: 3,
  };

  it('runs a saved view with its sort and columns and renders only those columns in that order', async () => {
    const fetchMock = mockApi({ views: [sortedView], search: () => json(sortedRows) });
    vi.stubGlobal('fetch', fetchMock);

    renderWithProviders(<SavedViewsPage />);
    fireEvent.click(await screen.findByRole('button', { name: /^Server absteigend/ }));

    await waitFor(() =>
      expect(searchBodies(fetchMock)).toEqual([
        {
          status: 'active',
          entity_kind: 'ci',
          sort: [{ field: 'name', dir: 'desc' }],
          columns: ['name', 'status'],
        },
      ]),
    );
    const table = await screen.findByRole('table');
    expect(
      within(table)
        .getAllByRole('columnheader')
        .map((h) => h.textContent),
    ).toEqual(['Name', 'Status']);
    const rows = within(table).getAllByRole('row').slice(1);
    expect(
      rows.map((r) =>
        within(r)
          .getAllByRole('cell')
          .map((c) => c.textContent),
      ),
    ).toEqual([
      ['srv-c', 'Wartung'],
      ['srv-b', 'Aktiv'],
      ['srv-a', 'Aktiv'],
    ]);
    // The first column links to the CI; the summary is no saved column.
    expect(within(rows[0]!).getByRole('link', { name: 'srv-c' })).toHaveAttribute(
      'href',
      '/cmdb/ci-c',
    );
    expect(screen.queryByText('summary-c')).toBeNull();
    // The presentation of the view is loaded into the form.
    expect(screen.getByLabelText('Sortieren nach')).toHaveValue('name');
    expect(screen.getByLabelText('Richtung')).toHaveValue('desc');
    expect(screen.getByRole('checkbox', { name: 'Auditoren' })).toBeChecked();
    expect(screen.getByRole('checkbox', { name: 'Netzwerk' })).not.toBeChecked();
  });

  it('sends a view without sort and columns as its filter alone and lists name and summary', async () => {
    const fetchMock = mockApi({
      views: [{ ...ownView, sort: [], columns: [] }],
      search: () =>
        json({ data: [{ id: 'ci-a', entity_kind: 'ci', name: 'srv-a', summary: 'sum-a' }] }),
    });
    vi.stubGlobal('fetch', fetchMock);

    renderWithProviders(<SavedViewsPage />);
    fireEvent.click(await screen.findByRole('button', { name: /^My servers/ }));

    await waitFor(() =>
      expect(searchBodies(fetchMock)).toEqual([{ status: 'active', entity_kind: 'ci' }]),
    );
    expect(await screen.findByRole('link', { name: 'srv-a' })).toBeInTheDocument();
    expect(screen.getByText('sum-a')).toBeInTheDocument();
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('saves sort, columns and sharing with a new view', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    vi.spyOn(window, 'prompt').mockReturnValue('Garantie absteigend');

    renderWithProviders(<SavedViewsPage />);
    fireEvent.click(await screen.findByRole('button', { name: new RegExp(preset.name, 'i') }));

    fireEvent.change(await screen.findByLabelText('Sortieren nach'), {
      target: { value: 'warranty_end' },
    });
    fireEvent.change(screen.getByLabelText('Richtung'), { target: { value: 'desc' } });
    fireEvent.click(screen.getByRole('checkbox', { name: 'Inventarnummer' }));
    fireEvent.click(screen.getByRole('checkbox', { name: 'Garantieende' }));
    fireEvent.click(screen.getByRole('checkbox', { name: 'Für die ganze Organisation freigeben' }));
    fireEvent.click(await screen.findByRole('checkbox', { name: 'Netzwerk' }));

    // The changed presentation runs at once.
    await waitFor(() =>
      expect(searchBodies(fetchMock).slice(-1)[0]).toEqual({
        warranty_within_days: 90,
        entity_kind: 'asset',
        sort: [{ field: 'warranty_end', dir: 'desc' }],
        columns: ['asset_tag', 'warranty_end'],
      }),
    );

    fireEvent.click(screen.getByRole('button', { name: 'Als Ansicht speichern' }));
    await waitFor(() => {
      const post = fetchMock.mock.calls.find(
        (call) => call[1]?.method === 'POST' && String(call[0]).includes('/saved-views'),
      );
      expect(post).toBeTruthy();
      expect(JSON.parse(String(post![1]!.body))).toEqual({
        name: 'Garantie absteigend',
        entity_kind: 'asset',
        filter_spec: { warranty_within_days: 90 },
        sort: [{ field: 'warranty_end', dir: 'desc' }],
        columns: ['asset_tag', 'warranty_end'],
        shared: true,
        shared_with_roles: ['role-2'],
      });
    });
  });

  it('changes sort, columns and sharing of an own view through savedViewApi.update', async () => {
    const fetchMock = mockApi({ views: [sortedView], search: () => json(sortedRows) });
    vi.stubGlobal('fetch', fetchMock);

    renderWithProviders(<SavedViewsPage />);
    fireEvent.click(await screen.findByRole('button', { name: /^Server absteigend/ }));

    fireEvent.change(await screen.findByLabelText('Richtung'), { target: { value: 'asc' } });
    fireEvent.click(screen.getByRole('checkbox', { name: 'Status' }));
    fireEvent.click(screen.getByRole('checkbox', { name: 'Hostname' }));
    fireEvent.click(await screen.findByRole('checkbox', { name: 'Auditoren' }));
    fireEvent.click(screen.getByRole('checkbox', { name: 'Netzwerk' }));
    fireEvent.click(screen.getByRole('button', { name: 'Änderungen speichern' }));

    await waitFor(() => {
      const patch = fetchMock.mock.calls.find((call) => call[1]?.method === 'PATCH');
      expect(patch).toBeTruthy();
      expect(String(patch![0])).toContain(`/saved-views/${sortedView.id}`);
      expect(JSON.parse(String(patch![1]!.body))).toEqual({
        sort: [{ field: 'name', dir: 'asc' }],
        columns: ['name', 'hostname'],
        shared: false,
        shared_with_roles: ['role-2'],
      });
    });
    // Nothing else is created.
    expect(
      fetchMock.mock.calls.some(
        (call) => call[1]?.method === 'POST' && String(call[0]).includes('/saved-views'),
      ),
    ).toBe(false);
  });

  it('offers no update for a view shared by somebody else', async () => {
    vi.stubGlobal(
      'fetch',
      mockApi({
        views: [{ ...sortedView, owner_id: 'user-2' }],
        search: () => json(sortedRows),
      }),
    );

    renderWithProviders(<SavedViewsPage />);
    fireEvent.click(await screen.findByRole('button', { name: /^Server absteigend/ }));

    expect(await screen.findByRole('table')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Änderungen speichern' })).toBeNull();
    expect(screen.getByText(/wurde mit Ihnen geteilt/)).toBeInTheDocument();
  });

  it('does not ask for the roles without role:read and says why roles are missing', async () => {
    const fetchMock = mockApi({ permissions: ['saved_view:write'] });
    vi.stubGlobal('fetch', fetchMock);

    renderWithProviders(<SavedViewsPage />);
    fireEvent.click(await screen.findByRole('button', { name: new RegExp(preset.name, 'i') }));

    expect(await screen.findByText('Die Rollen können nicht geladen werden.')).toBeInTheDocument();
    expect(fetchMock.mock.calls.some((call) => String(call[0]).includes('/roles'))).toBe(false);
  });
});

/**
 * WP-315 (SRC-03, CH18): the filter of a view is edited with the click
 * builder; saving sends the complete state, the preview runs on the server
 * with limit 10, and the answers 422, 403 and 404 appear at the node or as
 * a hint.
 */
describe('SavedViewsPage filter builder (WP-315)', () => {
  const calls = (fetchMock: ReturnType<typeof mockApi>, method: string, path: string) =>
    fetchMock.mock.calls.filter(
      (call) => (call[1]?.method ?? 'GET') === method && String(call[0]).includes(path),
    );
  const bodyOf = (call: unknown[] | undefined) =>
    JSON.parse(String((call?.[1] as RequestInit | undefined)?.body)) as Record<string, unknown>;

  const sharedView = {
    ...ownView,
    id: 'view-9',
    name: 'Org servers',
    shared: true,
    shared_with_roles: ['role-1'],
    sort: [{ field: 'name', dir: 'desc' }],
    columns: ['name', 'status'],
    filter_spec: {
      lacks_relationship: 'backup',
      where: {
        and: [
          { field: 'status', op: 'eq', value: 'active' },
          { field: 'hostname', op: 'like', value: 'srv-*' },
        ],
      },
    },
  };

  async function addNameCondition(value: string) {
    const root = await screen.findByRole('group', { name: 'Gruppe (UND)' });
    fireEvent.click(
      within(root).getAllByRole('button', { name: 'Bedingung hinzufügen' }).slice(-1)[0]!,
    );
    const added = screen.getAllByRole('group', { name: /^Bedingung \d+$/ }).slice(-1)[0]!;
    fireEvent.change(within(added).getByLabelText('Feld'), { target: { value: 'name' } });
    fireEvent.change(within(added).getByLabelText('Wert'), { target: { value } });
  }

  async function editView(name: RegExp) {
    fireEvent.click(await screen.findByRole('button', { name }));
    fireEvent.click(await screen.findByRole('button', { name: 'Filter bearbeiten' }));
  }

  it('creates a new view by POST', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<SavedViewsPage />);

    fireEvent.click(await screen.findByRole('button', { name: 'Neue Ansicht' }));
    fireEvent.change(screen.getByLabelText('Name der Ansicht'), {
      target: { value: 'Web servers' },
    });
    expect(screen.getByLabelText('Objektart')).toHaveValue('ci');
    await addNameCondition('web-01');
    fireEvent.click(screen.getByRole('button', { name: 'Als neue Ansicht speichern' }));

    await waitFor(() => expect(calls(fetchMock, 'POST', '/saved-views')).toHaveLength(1));
    expect(bodyOf(calls(fetchMock, 'POST', '/saved-views')[0])).toEqual({
      name: 'Web servers',
      entity_kind: 'ci',
      filter_spec: { where: { and: [{ field: 'name', op: 'eq', value: 'web-01' }] } },
      sort: [],
      columns: [],
      shared: false,
      shared_with_roles: [],
    });
    expect(calls(fetchMock, 'PATCH', '/saved-views')).toHaveLength(0);
    // The saved view runs at once.
    await waitFor(() =>
      expect(bodyOf(calls(fetchMock, 'POST', '/search/query').slice(-1)[0])).toEqual({
        where: { and: [{ field: 'name', op: 'eq', value: 'web-01' }] },
        entity_kind: 'ci',
      }),
    );
  });

  it('saves an own org-shared view by PATCH with its complete state and keeps the flat keys', async () => {
    const fetchMock = mockApi({ views: [sharedView] });
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<SavedViewsPage />);
    await editView(/^Org servers/);

    // The flat key is shown next to the builder, not editable.
    expect(screen.getByRole('list', { name: 'Weitere Kriterien' })).toHaveTextContent(
      'lacks_relationship: "backup"',
    );
    expect(screen.getByLabelText('Name der Ansicht')).toHaveValue('Org servers');
    const second = screen.getByRole('group', { name: 'Bedingung 2' });
    fireEvent.change(within(second).getByLabelText('Wert'), { target: { value: 'db-*' } });
    fireEvent.click(screen.getByRole('button', { name: 'Filter speichern' }));

    await waitFor(() => expect(calls(fetchMock, 'PATCH', '/saved-views/view-9')).toHaveLength(1));
    expect(bodyOf(calls(fetchMock, 'PATCH', '/saved-views/view-9')[0])).toEqual({
      name: 'Org servers',
      entity_kind: 'ci',
      filter_spec: {
        lacks_relationship: 'backup',
        where: {
          and: [
            { field: 'status', op: 'eq', value: 'active' },
            { field: 'hostname', op: 'like', value: 'db-*' },
          ],
        },
      },
      sort: [{ field: 'name', dir: 'desc' }],
      columns: ['name', 'status'],
      shared: true,
      shared_with_roles: ['role-1'],
    });
    expect(calls(fetchMock, 'POST', '/saved-views')).toHaveLength(0);
  });

  it('saves a preset as a new view by POST without from_preset and keeps its flat keys', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<SavedViewsPage />);
    await editView(new RegExp(preset.name, 'i'));

    expect(screen.getByLabelText('Objektart')).toHaveValue('asset');
    expect(screen.getByRole('list', { name: 'Weitere Kriterien' })).toHaveTextContent(
      'warranty_within_days: 90',
    );
    expect(screen.queryByRole('button', { name: 'Filter speichern' })).toBeNull();
    await addNameCondition('laptop-*');
    fireEvent.click(screen.getByRole('button', { name: 'Als neue Ansicht speichern' }));

    await waitFor(() => expect(calls(fetchMock, 'POST', '/saved-views')).toHaveLength(1));
    const body = bodyOf(calls(fetchMock, 'POST', '/saved-views')[0]);
    expect(body).toEqual({
      name: preset.name,
      entity_kind: 'asset',
      filter_spec: {
        warranty_within_days: 90,
        where: { and: [{ field: 'name', op: 'eq', value: 'laptop-*' }] },
      },
      sort: [],
      columns: [],
      shared: false,
      shared_with_roles: [],
    });
    expect('from_preset' in body).toBe(false);
  });

  it('previews on the server with limit 10 and shows total', async () => {
    const fetchMock = mockApi({
      search: () =>
        json({
          data: [
            { id: 'ci-1', entity_kind: 'ci', name: 'web-01' },
            { id: 'ci-2', entity_kind: 'ci', name: 'web-02' },
          ],
          total: 37,
        }),
    });
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<SavedViewsPage />);

    fireEvent.click(await screen.findByRole('button', { name: 'Neue Ansicht' }));
    await addNameCondition('web-*');
    fireEvent.click(screen.getByRole('button', { name: 'Vorschau' }));

    const previewSection = await screen.findByRole('region', { name: 'Vorschau' });
    expect(previewSection).toHaveTextContent('37 Treffer');
    expect(within(previewSection).getByText('web-02')).toBeInTheDocument();
    const call = calls(fetchMock, 'POST', '/search/query')[0];
    expect(String(call?.[0])).toContain('/search/query?limit=10');
    expect(bodyOf(call)).toEqual({
      where: { and: [{ field: 'name', op: 'eq', value: 'web-*' }] },
      entity_kind: 'ci',
    });
  });

  it('marks the second node on a 422 with the path where.and[1]', async () => {
    const fetchMock = mockApi({
      views: [sharedView],
      search: (n) =>
        n === 0
          ? json({ data: [], total: 0 })
          : json(
              {
                type: 'urn:reticora:problem:invalid-field',
                title: 'Unprocessable Entity',
                status: 422,
                detail: 'where.and[1]: unknown field "hostnme"',
                field: 'where.and[1]',
              },
              422,
            ),
    });
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<SavedViewsPage />);
    await editView(/^Org servers/);
    fireEvent.click(screen.getByRole('button', { name: 'Vorschau' }));

    const second = screen.getByRole('group', { name: 'Bedingung 2' });
    await waitFor(() => expect(second).toHaveAttribute('data-invalid', 'true'));
    expect(second).toHaveAccessibleDescription('Vom Server abgelehnt: unknown field "hostnme"');
    expect(screen.getByRole('group', { name: 'Bedingung 1' })).not.toHaveAttribute('data-invalid');
  });

  it('marks the comparisons of a 403 with fields and no longer offers those fields', async () => {
    const fetchMock = mockApi({
      views: [sharedView],
      search: (n) =>
        n === 0
          ? json({ data: [], total: 0 })
          : json(
              {
                type: 'urn:reticora:problem:forbidden',
                title: 'Forbidden',
                status: 403,
                detail: 'field permissions do not allow this request on: hostname',
                fields: ['hostname'],
              },
              403,
            ),
    });
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<SavedViewsPage />);
    await editView(/^Org servers/);
    fireEvent.click(screen.getByRole('button', { name: 'Vorschau' }));

    expect(
      await screen.findByText(/Keine Berechtigung für die Felder: hostname/),
    ).toBeInTheDocument();
    const second = screen.getByRole('group', { name: 'Bedingung 2' });
    expect(second).toHaveAccessibleDescription('Keine Berechtigung für das Feld hostname.');
    const first = screen.getByRole('group', { name: 'Bedingung 1' });
    const offered = within(within(first).getByLabelText('Feld'))
      .getAllByRole('option')
      .map((o) => (o as HTMLOptionElement).value);
    expect(offered).not.toContain('hostname');
    expect(offered).toContain('fqdn');
  });

  it('says so when saving answers 404 (no write permission)', async () => {
    const fetchMock = mockApi({
      views: [sharedView],
      patch: () => json({ title: 'Not Found', status: 404, detail: 'saved view not found' }, 404),
    });
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<SavedViewsPage />);
    await editView(/^Org servers/);
    fireEvent.click(screen.getByRole('button', { name: 'Filter speichern' }));

    expect(
      await screen.findByText('Keine Schreibberechtigung für diese Ansicht.'),
    ).toBeInTheDocument();
  });

  it('saves a view shared by somebody else as a new view', async () => {
    const fetchMock = mockApi({ views: [{ ...sharedView, owner_id: 'user-2' }] });
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<SavedViewsPage />);
    await editView(/^Org servers/);

    expect(screen.queryByRole('button', { name: 'Filter speichern' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Als neue Ansicht speichern' }));
    await waitFor(() => expect(calls(fetchMock, 'POST', '/saved-views')).toHaveLength(1));
    expect(calls(fetchMock, 'PATCH', '/saved-views')).toHaveLength(0);
  });

  it('lets saved_view:manage save a foreign view by PATCH (WP-307)', async () => {
    const fetchMock = mockApi({
      views: [{ ...sharedView, owner_id: 'user-2' }],
      permissions: ['saved_view:write', 'saved_view:manage', 'role:read'],
    });
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<SavedViewsPage />);
    await editView(/^Org servers/);
    fireEvent.click(await screen.findByRole('button', { name: 'Filter speichern' }));
    await waitFor(() => expect(calls(fetchMock, 'PATCH', '/saved-views/view-9')).toHaveLength(1));
  });

  it('sends no incomplete filter and marks the node', async () => {
    const fetchMock = mockApi();
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<SavedViewsPage />);
    fireEvent.click(await screen.findByRole('button', { name: 'Neue Ansicht' }));
    const root = await screen.findByRole('group', { name: 'Gruppe (UND)' });
    fireEvent.click(within(root).getByRole('button', { name: 'Bedingung hinzufügen' }));
    fireEvent.click(screen.getByRole('button', { name: 'Vorschau' }));

    expect(await screen.findByText(/Der Filter ist unvollständig/)).toBeInTheDocument();
    expect(screen.getByRole('group', { name: 'Bedingung 1' })).toHaveAccessibleDescription(
      'Wählen Sie ein Feld.',
    );
    expect(calls(fetchMock, 'POST', '/search/query')).toHaveLength(0);
  });
});
