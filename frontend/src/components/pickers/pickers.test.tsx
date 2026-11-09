// UI-15 (WP-318): pickers instead of id input. ObjectPicker searches the
// scoped type lists after a typing pause (one request per pause, scope from
// useScopeStore, CI type filter), offers assets and consumables only with
// module and feature, follows the combobox pattern and takes a CI created
// in CIFormModal. LocationPicker shows the location tree, filters by name
// and lets only the given kinds be chosen. UserTeamPicker returns users and
// teams as { kind, id } and says so when people may not be searched.
import { useState } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import i18n from '../../i18n';
import { renderWithProviders } from '../../test/utils';
import { useScopeStore } from '../../stores/scope';
import { useAuthStore } from '../../auth/authStore';
import { Modal } from '../ui/Modal';
import { ObjectPicker, type ObjectKind, type PickedObject } from './ObjectPicker';
import { LocationPicker, type PickedLocation } from './LocationPicker';
import { UserTeamPicker, type PickedHolder } from './UserTeamPicker';

vi.mock('../../pages/CIFormModal', () => ({
  CIFormModal: ({
    open,
    onSuccess,
    onOpenChange,
  }: {
    open: boolean;
    onSuccess?: (ci: { id: string; name: string }) => void;
    onOpenChange: (open: boolean) => void;
  }) =>
    open ? (
      <button
        type="button"
        onClick={() => {
          onSuccess?.({ id: 'ci-new', name: 'Neuer Server' });
          onOpenChange(false);
        }}
      >
        stub-create
      </button>
    ) : null,
}));

const CI_ID = 'c1000000-0000-4000-8000-000000000001';

interface Call {
  path: string;
  params: URLSearchParams;
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

const page = (data: unknown[]) => ({
  data,
  total: data.length,
  limit: 20,
  offset: 0,
  has_more: false,
});

/** stubApi answers by path (without /api/v1) and records the requests. */
function stubApi(routes: Record<string, (params: URLSearchParams) => Response>) {
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://test');
      const path = url.pathname.replace(/^\/api\/v1/, '');
      calls.push({ path, params: url.searchParams });
      const route = routes[path];
      return route ? route(url.searchParams) : json(page([]));
    }),
  );
  return calls;
}

beforeEach(async () => {
  await i18n.changeLanguage('de-DE');
  useScopeStore.setState({ clientId: '', siteId: '' });
});

afterEach(() => {
  vi.unstubAllGlobals();
  useAuthStore.setState({ token: null, expiresAt: null, user: null });
});

function ObjectHost({
  kinds,
  ciTypeIds,
  allowCreate,
  onChange,
}: {
  kinds: ObjectKind[];
  ciTypeIds?: string[];
  allowCreate?: boolean;
  onChange?: (value: PickedObject[]) => void;
}) {
  const [value, setValue] = useState<PickedObject[]>([]);
  return (
    <ObjectPicker
      label="Objekt"
      kinds={kinds}
      ciTypeIds={ciTypeIds}
      allowCreate={allowCreate}
      value={value}
      onChange={(next) => {
        setValue(next);
        onChange?.(next);
      }}
    />
  );
}

/** type enters the text letter by letter without a pause. */
function type(input: HTMLElement, text: string) {
  for (let i = 1; i <= text.length; i += 1) {
    fireEvent.change(input, { target: { value: text.slice(0, i) } });
  }
}

const ciHits = page([
  { id: CI_ID, name: 'Server 1', serial_number: 'SN-1', ci_type_id: 't1', status: 'active' },
  {
    id: 'c1000000-0000-4000-8000-000000000002',
    name: 'Server 2',
    ci_type_id: 't2',
    status: 'active',
  },
]);

describe('ObjectPicker', () => {
  it('searches CIs once per typing pause with scope and CI type filter', async () => {
    useScopeStore.setState({ clientId: 'client-1', siteId: 'site-1' });
    const calls = stubApi({ '/cis': () => json(ciHits) });
    renderWithProviders(<ObjectHost kinds={['ci']} ciTypeIds={['t1', 't2']} />);

    type(screen.getByRole('combobox', { name: 'Objekt' }), 'Server');
    const listbox = await screen.findByRole('listbox', { name: 'Objekt' });

    const searches = calls.filter((c) => c.path === '/cis');
    expect(searches).toHaveLength(1);
    const params = searches[0]!.params;
    expect(params.get('search')).toBe('Server');
    expect(params.get('limit')).toBe('20');
    expect(params.get('client_id')).toBe('client-1');
    expect(params.get('site_id')).toBe('site-1');
    expect(params.get('filter[ci_type_id][in]')).toBe('t1,t2');
    // Kind, name and code, no UUID.
    const options = within(listbox).getAllByRole('option');
    expect(options[0]).toHaveTextContent('CI');
    expect(options[0]).toHaveTextContent('Server 1');
    expect(options[0]).toHaveTextContent('SN-1');
    expect(listbox.textContent).not.toContain(CI_ID);
  });

  // WP-859: search= of GET /cis also matches host name and management IP;
  // the option shows both, so targets of the same name can be told apart.
  it('shows host name and management IP of CI hits of the same name', async () => {
    const calls = stubApi({
      '/cis': () =>
        json(
          page([
            {
              id: CI_ID,
              name: 'Core-Switch',
              hostname: 'sw-core-01',
              management_ip: '10.85.9.17',
              ci_type_id: 't1',
              status: 'active',
            },
            {
              id: 'c1000000-0000-4000-8000-000000000002',
              name: 'Core-Switch',
              management_ip: '10.85.9.18',
              ci_type_id: 't1',
              status: 'active',
            },
            {
              id: 'c1000000-0000-4000-8000-000000000003',
              name: 'Core-Switch',
              ci_type_id: 't1',
              status: 'active',
            },
          ]),
        ),
    });
    const changes: PickedObject[][] = [];
    renderWithProviders(<ObjectHost kinds={['ci']} onChange={(next) => changes.push(next)} />);

    const input = screen.getByRole('combobox', { name: 'Objekt' });
    type(input, '10.85.9');
    const listbox = await screen.findByRole('listbox', { name: 'Objekt' });
    expect(calls.filter((c) => c.path === '/cis')[0]!.params.get('search')).toBe('10.85.9');

    const options = within(listbox).getAllByRole('option');
    expect(options).toHaveLength(3);
    expect(options[0]).toHaveTextContent('Core-Switch');
    expect(options[0]).toHaveTextContent('sw-core-01 · 10.85.9.17');
    expect(options[1]).toHaveTextContent('10.85.9.18');
    expect(options[1]).not.toHaveTextContent('·');
    expect(options[2]!.textContent).toBe('CICore-Switch');

    fireEvent.click(options[1]!);
    expect(changes[changes.length - 1]).toEqual([
      { kind: 'ci', id: 'c1000000-0000-4000-8000-000000000002', name: 'Core-Switch' },
    ]);
  });

  it('searches assets and consumables in the client scope', async () => {
    useScopeStore.setState({ clientId: 'client-1', siteId: 'site-1' });
    const calls = stubApi({
      '/assets': () =>
        json(page([{ id: 'a1', name: 'Laptop 7', inventory_no: 'INV-7', custom_fields: {} }])),
      '/consumables': () =>
        json(page([{ id: 'k1', name: 'Toner', sku: 'SKU-9', unit: 'pcs', stock_level: 3 }])),
    });
    renderWithProviders(<ObjectHost kinds={['asset', 'consumable']} />);

    type(screen.getByRole('combobox', { name: 'Objekt' }), 'Lap');
    expect(await screen.findByRole('option', { name: /Laptop 7/ })).toHaveTextContent('INV-7');
    const assetSearch = calls.filter((c) => c.path === '/assets');
    expect(assetSearch).toHaveLength(1);
    expect(assetSearch[0]!.params.get('search')).toBe('Lap');
    expect(assetSearch[0]!.params.get('limit')).toBe('20');
    expect(assetSearch[0]!.params.get('client_id')).toBe('client-1');

    fireEvent.change(screen.getByLabelText('Art'), { target: { value: 'consumable' } });
    type(screen.getByRole('combobox', { name: 'Objekt' }), 'Ton');
    expect(await screen.findByRole('option', { name: /Toner/ })).toHaveTextContent('SKU-9');
    const consumableSearch = calls.filter((c) => c.path === '/consumables');
    expect(consumableSearch).toHaveLength(1);
    expect(consumableSearch[0]!.params.get('search')).toBe('Ton');
    expect(consumableSearch[0]!.params.get('client_id')).toBe('client-1');
  });

  it('offers assets and consumables only with module and feature', async () => {
    useAuthStore.setState({
      token: 'header.payload.signature',
      expiresAt: new Date(Date.now() + 3600_000).toISOString(),
      user: { sub: 'user-1' },
    });
    stubApi({
      '/modules': () =>
        json({
          data: [
            { key: 'inventory', phase: 'P2', nav: ['assets'], enabled: true },
            { key: 'stocktake', phase: 'P2', nav: ['stocktake'], enabled: true },
          ],
        }),
      '/entitlements': () =>
        json(
          page([
            { feature_key: 'assets_basic', enabled: true },
            { feature_key: 'stock', enabled: false },
          ]),
        ),
    });
    renderWithProviders(<ObjectHost kinds={['ci', 'asset', 'consumable']} />);

    await waitFor(() =>
      expect(
        within(screen.getByLabelText('Art'))
          .getAllByRole('option')
          .map((o) => o.textContent),
      ).toEqual(['CI', 'Asset']),
    );
  });

  it('offers only CIs when the module inventory is off', async () => {
    useAuthStore.setState({
      token: 'header.payload.signature',
      expiresAt: new Date(Date.now() + 3600_000).toISOString(),
      user: { sub: 'user-1' },
    });
    const calls = stubApi({
      '/modules': () =>
        json({ data: [{ key: 'inventory', phase: 'P2', nav: ['assets'], enabled: false }] }),
      '/entitlements': () => json(page([{ feature_key: 'assets_basic', enabled: true }])),
    });
    renderWithProviders(<ObjectHost kinds={['ci', 'asset']} />);

    await waitFor(() => expect(calls.some((c) => c.path === '/modules')).toBe(true));
    await waitFor(() => expect(screen.queryByLabelText('Art')).not.toBeInTheDocument());
    expect(screen.getByRole('combobox', { name: 'Objekt' })).toBeInTheDocument();
  });

  it('follows the combobox pattern with arrow keys, Enter and Escape', async () => {
    stubApi({ '/cis': () => json(ciHits) });
    const onChange = vi.fn();
    renderWithProviders(
      <Modal open onOpenChange={() => undefined} title="Dialog">
        <ObjectHost kinds={['ci']} onChange={onChange} />
      </Modal>,
    );
    const input = screen.getByRole('combobox', { name: 'Objekt' });
    type(input, 'Server');
    const options = await screen.findAllByRole('option');
    expect(input).toHaveAttribute('aria-expanded', 'true');
    expect(input).toHaveAttribute('aria-controls', screen.getByRole('listbox').id);
    expect(input).toHaveAttribute('aria-activedescendant', options[0]!.id);

    fireEvent.keyDown(input, { key: 'ArrowDown' });
    expect(input).toHaveAttribute('aria-activedescendant', options[1]!.id);
    fireEvent.keyDown(input, { key: 'ArrowUp' });
    expect(input).toHaveAttribute('aria-activedescendant', options[0]!.id);

    // Escape closes the list, not the dialog; the focus stays in the input.
    input.focus();
    fireEvent.keyDown(input, { key: 'Escape' });
    expect(input).toHaveAttribute('aria-expanded', 'false');
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    expect(document.activeElement).toBe(input);

    fireEvent.keyDown(input, { key: 'ArrowDown' });
    fireEvent.keyDown(input, { key: 'ArrowDown' });
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(onChange).toHaveBeenLastCalledWith([
      { kind: 'ci', id: 'c1000000-0000-4000-8000-000000000002', name: 'Server 2', code: undefined },
    ]);
    expect(screen.getByRole('list', { name: 'Ausgewählt' })).toHaveTextContent('Server 2');
  });

  it('creates a CI with CIFormModal and takes it from onSuccess', async () => {
    stubApi({});
    const onChange = vi.fn();
    renderWithProviders(<ObjectHost kinds={['ci']} allowCreate onChange={onChange} />);

    fireEvent.click(screen.getByRole('button', { name: 'Neues CI anlegen' }));
    fireEvent.click(await screen.findByRole('button', { name: 'stub-create' }));

    expect(onChange).toHaveBeenCalledWith([
      { kind: 'ci', id: 'ci-new', name: 'Neuer Server', code: undefined },
    ]);
    expect(screen.queryByRole('button', { name: 'stub-create' })).not.toBeInTheDocument();
  });
});

const tree = {
  data: [
    {
      id: 'site-1',
      kind: 'site',
      name: 'Werk Nord',
      children: [
        {
          id: 'wh-1',
          kind: 'warehouse',
          name: 'Lager A',
          children: [
            { id: 'bin-1', kind: 'bin', name: 'Fach 1', children: [] },
            { id: 'bin-2', kind: 'bin', name: 'Fach 2', children: [] },
          ],
        },
        {
          id: 'b-1',
          kind: 'building',
          name: 'Halle',
          children: [{ id: 'room-1', kind: 'room', name: 'Raum 1', children: [] }],
        },
      ],
    },
  ],
};

describe('LocationPicker', () => {
  function LocationHost({
    kinds,
    onChange,
  }: {
    kinds?: ('bin' | 'room')[];
    onChange: (value: PickedLocation | null) => void;
  }) {
    const [value, setValue] = useState<string | null>(null);
    return (
      <LocationPicker
        label="Lagerplatz"
        kinds={kinds}
        value={value}
        onChange={(next) => {
          setValue(next?.id ?? null);
          onChange(next);
        }}
      />
    );
  }

  it('shows the tree of /locations/tree and chooses only bins with the keyboard', async () => {
    const calls = stubApi({ '/locations/tree': () => json(tree) });
    const onChange = vi.fn();
    renderWithProviders(<LocationHost kinds={['bin']} onChange={onChange} />);

    const site = await screen.findByRole('treeitem', { name: /Werk Nord/ });
    expect(calls.some((c) => c.path === '/locations/tree')).toBe(true);
    expect(site).not.toHaveAttribute('aria-selected');
    expect(site).not.toHaveAttribute('aria-disabled');
    expect(site).toHaveAttribute('aria-expanded', 'false');

    act(() => site.focus());
    fireEvent.keyDown(site, { key: 'ArrowRight' });
    expect(site).toHaveAttribute('aria-expanded', 'true');
    // The building branch holds no bin and is left out.
    expect(screen.queryByRole('treeitem', { name: /Halle/ })).not.toBeInTheDocument();

    fireEvent.keyDown(site, { key: 'ArrowDown' });
    const warehouse = screen.getByRole('treeitem', { name: /Lager A/ });
    expect(document.activeElement).toBe(warehouse);
    // Enter on an ancestor that cannot be chosen chooses nothing.
    fireEvent.keyDown(warehouse, { key: 'ArrowRight' });
    fireEvent.keyDown(warehouse, { key: 'ArrowDown' });
    const bin = screen.getByRole('treeitem', { name: /Fach 1/ });
    expect(document.activeElement).toBe(bin);
    fireEvent.keyDown(bin, { key: 'Enter' });

    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledWith({
      id: 'bin-1',
      name: 'Fach 1',
      kind: 'bin',
      path: ['Werk Nord', 'Lager A', 'Fach 1'],
    });
    expect(screen.getByRole('treeitem', { name: /Fach 1/ })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    expect(screen.getByText('Gewählt: Werk Nord › Lager A › Fach 1')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('treeitem', { name: /Lager A/ }));
    expect(onChange).toHaveBeenCalledTimes(1);
  });

  it('filters the tree by name and keeps the ancestors as path', async () => {
    stubApi({ '/locations/tree': () => json(tree) });
    const onChange = vi.fn();
    renderWithProviders(<LocationHost onChange={onChange} />);
    await screen.findByRole('treeitem', { name: /Werk Nord/ });

    fireEvent.change(screen.getByLabelText('Lagerplatz filtern'), { target: { value: 'raum' } });

    const names = screen.getAllByRole('treeitem').map((item) => item.firstChild?.textContent);
    expect(names).toEqual(['Werk NordStandort', 'HalleGebäude', 'Raum 1Raum']);
    fireEvent.click(screen.getByRole('treeitem', { name: /Raum 1/ }));
    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({ id: 'room-1', path: ['Werk Nord', 'Halle', 'Raum 1'] }),
    );
  });
});

describe('UserTeamPicker', () => {
  function HolderHost({ onChange }: { onChange: (value: PickedHolder | null) => void }) {
    const [value, setValue] = useState<PickedHolder | null>(null);
    return (
      <UserTeamPicker
        label="Empfänger"
        value={value}
        onChange={(next) => {
          setValue(next);
          onChange(next);
        }}
      />
    );
  }

  it('returns users and teams as kind and id', async () => {
    const calls = stubApi({
      '/users': () => json(page([{ id: 'u-1', display_name: 'Anna Berg', email: 'a@x' }])),
      '/teams': () => json(page([{ id: 't-1', name: 'Annahme', member_count: 2 }])),
    });
    const onChange = vi.fn();
    renderWithProviders(<HolderHost onChange={onChange} />);

    type(screen.getByRole('combobox', { name: 'Empfänger' }), 'Ann');
    const user = await screen.findByRole('option', { name: /Anna Berg/ });
    expect(user).toHaveTextContent('Person');
    expect(screen.getByRole('option', { name: /Annahme/ })).toHaveTextContent('Team');
    const users = calls.filter((c) => c.path === '/users');
    expect(users).toHaveLength(1);
    expect(users[0]!.params.get('search')).toBe('Ann');
    expect(calls.filter((c) => c.path === '/teams')).toHaveLength(1);

    fireEvent.click(screen.getByRole('option', { name: /Annahme/ }));
    expect(onChange).toHaveBeenLastCalledWith({ kind: 'team', id: 't-1', name: 'Annahme' });

    type(screen.getByRole('combobox', { name: 'Empfänger' }), 'Anna');
    fireEvent.click(await screen.findByRole('option', { name: /Anna Berg/ }));
    expect(onChange).toHaveBeenLastCalledWith({ kind: 'user', id: 'u-1', name: 'Anna Berg' });
  });

  it('says that people may not be searched on 403', async () => {
    stubApi({
      '/users': () => json({ status: 403, title: 'Forbidden', detail: 'user:read' }, 403),
      '/teams': () => json({ status: 403, title: 'Forbidden', detail: 'user:read' }, 403),
    });
    renderWithProviders(<HolderHost onChange={vi.fn()} />);

    type(screen.getByRole('combobox', { name: 'Empfänger' }), 'Ann');

    expect(await screen.findByText('Keine Berechtigung, Personen zu suchen')).toBeInTheDocument();
    expect(screen.queryByText('Keine Treffer')).not.toBeInTheDocument();
  });
});
