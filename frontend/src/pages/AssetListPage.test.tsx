// The asset list (UI-14, UI-12, WP-319): catalog labels of lifecycle_state
// and category (GLO-10/UI-18, WP-525), columns, sort, filters and pages of
// GET /assets, saved views of entity_kind asset and the guided creation.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import i18n from '../i18n';
import { AssetListPage, ASSET_COLUMNS, isExtendedView } from './AssetListPage';
import { renderWithProviders } from '../test/utils';

const asset = {
  id: 'asset-1',
  organization_id: 'org-1',
  inventory_no: 'INV-1',
  name: 'Laptop 1',
  category: 'license',
  lifecycle_state: 'in_stock',
  location_id: 'loc-1',
  custom_fields: {},
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
};

function page(data: unknown[], extra: Record<string, unknown> = {}) {
  return { data, total: data.length, limit: 50, offset: 0, has_more: false, ...extra };
}

interface Call {
  method: string;
  url: URL;
  body: unknown;
}

type Reply = unknown | ((call: Call) => Response | unknown);

/**
 * stubApi answers by method and path (exact path below /api/v1) and records
 * every call; an unknown request is 404.
 */
function stubApi(routes: Record<string, Reply>) {
  const calls: Call[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://localhost');
    const method = (init?.method ?? 'GET').toUpperCase();
    const body = typeof init?.body === 'string' ? JSON.parse(init.body) : undefined;
    const call = { method, url, body };
    calls.push(call);
    const key = `${method} ${url.pathname.replace('/api/v1', '')}`;
    if (key in routes) {
      const route = routes[key];
      const reply = typeof route === 'function' ? (route as (c: Call) => unknown)(call) : route;
      if (reply instanceof Response) return reply;
      return new Response(JSON.stringify(reply), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    }
    return new Response(JSON.stringify({ detail: `unexpected request: ${key}` }), {
      status: 404,
      headers: { 'Content-Type': 'application/json' },
    });
  });
  vi.stubGlobal('fetch', fetchMock);
  return calls;
}

const tree = { data: [{ id: 'loc-1', name: 'Lager Nord', children: [] }] };

function baseRoutes(assets: Reply = page([asset]), views: unknown[] = []) {
  return {
    'GET /assets': assets,
    'GET /saved-views': page(views),
    'GET /locations/tree': tree,
  } as Record<string, Reply>;
}

function assetLists(calls: Call[]) {
  return calls.filter((c) => c.method === 'GET' && c.url.pathname === '/api/v1/assets');
}

function lastList(calls: Call[]) {
  const lists = assetLists(calls);
  return lists[lists.length - 1]!.url.searchParams;
}

afterEach(async () => {
  vi.unstubAllGlobals();
  await i18n.changeLanguage('de-DE');
});

async function renderList(routes = baseRoutes()) {
  const calls = stubApi(routes);
  renderWithProviders(<AssetListPage />, { route: '/assets' });
  const row = await screen.findByRole('row', { name: /INV-1/ });
  return { calls, row };
}

describe('AssetListPage', () => {
  it('shows lifecycle state and category labels from the catalog in de', async () => {
    await i18n.changeLanguage('de-DE');
    const { row } = await renderList();
    expect(within(row).getByText('Auf Lager')).toBeInTheDocument();
    expect(within(row).getByText('Lizenz')).toBeInTheDocument();
    expect(within(row).queryByText('in_stock')).not.toBeInTheDocument();
    expect(within(row).queryByText('license')).not.toBeInTheDocument();
    expect(screen.getByRole('option', { name: 'Auf Lager' })).toBeInTheDocument();
    expect(screen.getByRole('option', { name: 'Im Einsatz' })).toBeInTheDocument();
    // The location appears with its name from the location tree.
    expect(await within(row).findByText('Lager Nord')).toBeInTheDocument();
  });

  it('shows the en labels after a language switch', async () => {
    await i18n.changeLanguage('en-US');
    const { row } = await renderList();
    expect(within(row).getByText('In stock')).toBeInTheDocument();
    expect(within(row).getByText('License')).toBeInTheDocument();
    expect(within(row).queryByText('in_stock')).not.toBeInTheDocument();
  });

  it('shows the label of the organization model for a state without catalog entry', async () => {
    await i18n.changeLanguage('de-DE');
    const routes = baseRoutes(page([{ ...asset, lifecycle_state: 'quarantined' }]));
    routes['GET /lifecycle-models'] = page([
      {
        id: 'm-1',
        key: 'org_assets',
        name: 'Org',
        applies_to: 'asset',
        is_system: false,
        version: 1,
        states: [
          {
            id: 's-1',
            key: 'quarantined',
            label: 'Quarantäne',
            is_initial: false,
            is_terminal: false,
            sort_order: 1,
            required_fields: [],
          },
        ],
      },
    ]);
    const { row } = await renderList(routes);
    expect(await within(row).findByText('Quarantäne')).toBeInTheDocument();
  });

  it('loads the default columns with fields= and shows a missing field as a dash', async () => {
    const { calls, row } = await renderList();
    const params = lastList(calls);
    expect(params.get('fields')).toBe('inventory_no,name,category,lifecycle_state,location_id');
    expect(params.has('status')).toBe(false);
    expect(screen.getAllByRole('columnheader').map((h) => h.textContent)).toEqual(
      expect.arrayContaining(['Inventarnummer', 'Name', 'Kategorie', 'Lebenszyklus', 'Standort']),
    );
    expect(within(row).queryByText('—')).not.toBeInTheDocument();

    // A field the field permissions leave out appears as a dash.
    fireEvent.click(screen.getByRole('button', { name: /Spalten/ }));
    fireEvent.click(screen.getByRole('checkbox', { name: 'Hersteller' }));
    await waitFor(() =>
      expect(lastList(calls).get('fields')).toBe(
        'inventory_no,name,category,manufacturer,lifecycle_state,location_id',
      ),
    );
    const updated = await screen.findByRole('row', { name: /INV-1/ });
    expect(within(updated).getByText('—')).toBeInTheDocument();
  });

  it('sorts by a column header and filters by lifecycle_state', async () => {
    const { calls } = await renderList();
    fireEvent.click(screen.getByRole('button', { name: 'Nach Name sortieren' }));
    await waitFor(() => expect(lastList(calls).get('sort')).toBe('name'));
    fireEvent.click(screen.getByRole('button', { name: 'Nach Name sortieren' }));
    await waitFor(() => expect(lastList(calls).get('sort')).toBe('-name'));
    // The location is no field of the sort whitelist.
    expect(screen.queryByRole('button', { name: 'Nach Standort sortieren' })).toBeNull();

    fireEvent.change(screen.getByRole('combobox', { name: 'Lebenszyklus' }), {
      target: { value: 'deployed' },
    });
    await waitFor(() => expect(lastList(calls).get('filter[lifecycle_state]')).toBe('deployed'));
    expect(lastList(calls).get('sort')).toBe('-name');
  });

  it('pages over next_cursor', async () => {
    const routes = baseRoutes((call: Call) =>
      call.url.searchParams.get('cursor') === 'c-2'
        ? page([{ ...asset, id: 'asset-2', inventory_no: 'INV-2', name: 'Laptop 2' }])
        : page([asset], { has_more: true, next_cursor: 'c-2', total: 2 }),
    );
    const { calls } = await renderList(routes);
    fireEvent.click(screen.getByRole('button', { name: 'Nächste Seite' }));
    expect(await screen.findByRole('row', { name: /INV-2/ })).toBeInTheDocument();
    expect(lastList(calls).get('cursor')).toBe('c-2');
  });

  it('has no status bulk bar and sends no PATCH with status', async () => {
    const { calls } = await renderList();
    expect(screen.queryByRole('toolbar', { name: 'Massenbearbeitung' })).toBeNull();
    expect(screen.queryAllByRole('checkbox')).toHaveLength(0);
    expect(calls.some((c) => c.method === 'PATCH')).toBe(false);
  });

  it('saves columns, sort and filters as a view of entity_kind asset', async () => {
    const routes = baseRoutes();
    routes['POST /saved-views'] = (call: Call) => ({
      id: 'view-new',
      shared: false,
      created_at: '2026-01-01T00:00:00Z',
      ...(call.body as object),
    });
    const { calls } = await renderList(routes);
    fireEvent.click(screen.getByRole('button', { name: 'Nach Name sortieren' }));
    fireEvent.change(screen.getByRole('combobox', { name: 'Lebenszyklus' }), {
      target: { value: 'in_stock' },
    });
    fireEvent.change(screen.getByRole('combobox', { name: 'Kategorie' }), {
      target: { value: 'hardware' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Als Sicht speichern' }));
    fireEvent.change(await screen.findByLabelText(/Name der Sicht/), {
      target: { value: 'Lager' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true));
    const post = calls.find((c) => c.method === 'POST')!;
    expect(post.url.pathname).toBe('/api/v1/saved-views');
    expect(post.body).toEqual({
      name: 'Lager',
      entity_kind: 'asset',
      columns: ['inventory_no', 'name', 'category', 'lifecycle_state', 'location_id'],
      sort: [{ field: 'name', dir: 'asc' }],
      filter_spec: { lifecycle: 'in_stock', asset_category: 'hardware' },
    });
  });

  it('applies the columns, sort and filters of an asset view', async () => {
    const view = {
      id: 'view-1',
      name: 'Garantie',
      entity_kind: 'asset',
      filter_spec: { lifecycle: 'deployed', asset_category: 'hardware', search: 'Dell' },
      columns: ['name', 'warranty_until'],
      sort: [{ field: 'warranty_until', dir: 'desc' }],
      shared: true,
      created_at: '2026-01-01T00:00:00Z',
    };
    const ciView = { ...view, id: 'view-ci', name: 'CI-Sicht', entity_kind: 'ci' };
    const { calls } = await renderList(baseRoutes(page([asset]), [view, ciView]));
    const picker = screen.getByRole('combobox', { name: 'Sicht' });
    await within(picker).findByRole('option', { name: 'Garantie' });
    expect(within(picker).queryByRole('option', { name: 'CI-Sicht' })).toBeNull();
    fireEvent.change(picker, { target: { value: 'view-1' } });

    await waitFor(() => expect(lastList(calls).get('fields')).toBe('name,warranty_until'));
    const params = lastList(calls);
    expect(params.get('sort')).toBe('-warranty_until');
    expect(params.get('filter[lifecycle_state]')).toBe('deployed');
    expect(params.get('category')).toBe('hardware');
    expect(params.get('search')).toBe('Dell');
    expect(screen.getByRole('combobox', { name: 'Lebenszyklus' })).toHaveValue('deployed');
    expect(screen.getByRole('textbox', { name: 'Suchen' })).toHaveValue('Dell');
    expect(screen.getAllByRole('columnheader').map((h) => h.textContent)).toContain('Garantie bis');
  });

  it('marks a view with further conditions and links to the saved views', async () => {
    const view = {
      id: 'view-2',
      name: 'Ohne Halter',
      entity_kind: 'asset',
      filter_spec: { has_owner: false, lifecycle: 'deployed' },
      shared: false,
      created_at: '2026-01-01T00:00:00Z',
    };
    const { calls } = await renderList(baseRoutes(page([asset]), [view]));
    const picker = screen.getByRole('combobox', { name: 'Sicht' });
    await within(picker).findByRole('option', { name: 'Ohne Halter (erweitert)' });
    const before = assetLists(calls).length;
    fireEvent.change(picker, { target: { value: 'view-2' } });
    expect(await screen.findByRole('link', { name: 'In Sichten öffnen' })).toHaveAttribute(
      'href',
      '/saved-views',
    );
    // The list does not pretend to show the view.
    expect(lastList(calls).has('filter[lifecycle_state]')).toBe(false);
    expect(assetLists(calls).length).toBeGreaterThanOrEqual(before);
  });

  it('offers only columns the saved view field table knows', () => {
    expect(ASSET_COLUMNS).not.toContain('asset_tag');
    expect(ASSET_COLUMNS).not.toContain('status');
    expect(isExtendedView({ filter_spec: { search: 'x', entity_kind: 'asset' } })).toBe(false);
    expect(isExtendedView({ filter_spec: { warranty_within_days: 30 } })).toBe(true);
  });

  it('creates an asset guided by category tiles and shows a 409 at the field', async () => {
    const routes = baseRoutes();
    let attempt = 0;
    routes['POST /assets'] = () => {
      attempt += 1;
      if (attempt === 1) {
        return new Response(
          JSON.stringify({
            type: 'urn:reticora:problem:conflict',
            title: 'Conflict',
            status: 409,
            detail: 'Inventarnummer ist vergeben',
            field: 'inventory_no',
          }),
          { status: 409, headers: { 'Content-Type': 'application/problem+json' } },
        );
      }
      return { ...asset, id: 'asset-new' };
    };
    const { calls } = await renderList(routes);
    fireEvent.click(screen.getByRole('button', { name: 'Asset anlegen' }));
    const dialog = await screen.findByRole('dialog');
    // No field before the category tile.
    expect(within(dialog).queryByLabelText(/Inventarnummer/)).toBeNull();
    fireEvent.click(within(dialog).getByRole('button', { name: 'Hardware' }));

    fireEvent.change(within(dialog).getByLabelText(/Inventarnummer/), {
      target: { value: 'INV-9' },
    });
    fireEvent.change(within(dialog).getByLabelText(/^Name/), { target: { value: 'Notebook' } });
    fireEvent.change(within(dialog).getByLabelText(/Hersteller/), { target: { value: 'Dell' } });
    // The details are folded until asked for.
    expect(within(dialog).queryByLabelText(/Seriennummer/)).toBeNull();
    fireEvent.click(within(dialog).getByRole('button', { name: 'Weitere Angaben' }));
    fireEvent.change(within(dialog).getByLabelText(/Seriennummer/), {
      target: { value: 'SN-1' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Anlegen' }));

    expect(await within(dialog).findByText('Inventarnummer ist vergeben')).toBeInTheDocument();
    expect(within(dialog).getByLabelText(/Inventarnummer/)).toHaveAttribute('aria-invalid', 'true');
    const post = calls.find((c) => c.method === 'POST' && c.url.pathname === '/api/v1/assets')!;
    expect(post.body).toEqual({
      inventory_no: 'INV-9',
      name: 'Notebook',
      category: 'hardware',
      manufacturer: 'Dell',
      serial_number: 'SN-1',
    });

    fireEvent.change(within(dialog).getByLabelText(/Inventarnummer/), {
      target: { value: 'INV-10' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Anlegen' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });
});
