// UI-14 (WP-813): the stocktake with area picker, scan loop, quantity count,
// difference list and completion. The ScanDialog runs in continuous mode
// without camera (no secure context; BarcodeDetector and getUserMedia are
// absent or mocked), so codes come through its text field like a handheld
// scanner. Each code is resolved through GET /labels/lookup; a location
// label sets the place found, an asset or a consumable is recorded with
// POST /stocktakes/{id}/scans. The list page creates a stocktake with
// location_id instead of scope and deletes planned ones behind the undo
// window.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { Route, Routes } from 'react-router';
import i18n from '../i18n';
import { renderWithProviders } from '../test/utils';
import { useAuthStore } from '../auth/authStore';
import { useToastStore } from '../stores/toast';
import { UNDO_WINDOW_MS } from '../hooks/useUndoableDelete';
import { ToastViewport } from '../components/ui/Toast';
import { StocktakeDetailPage } from './StocktakeDetailPage';
import { StocktakeListPage } from './StocktakeListPage';

const ST = 'db000000-0000-4000-8000-000000000001';
const LEGACY = 'db000000-0000-4000-8000-000000000002';
const SITE = 'aa000000-0000-4000-8000-000000000001';
const ROOM = 'aa000000-0000-4000-8000-000000000003';
const BIN = 'aa000000-0000-4000-8000-000000000002';
const ASSET = 'a5000000-0000-4000-8000-000000000001';
const ASSET_2 = 'a5000000-0000-4000-8000-000000000002';
const CONSUMABLE = 'c2000000-0000-4000-8000-000000000001';
const USER = 'b1000000-0000-4000-8000-000000000001';
const AT = '2026-11-01T10:00:00Z';

interface Call {
  method: string;
  path: string;
  search: string;
  body: unknown;
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

const problem = (status: number, detail: string) =>
  json({ type: 'about:blank', title: 'Conflict', status, detail }, status);

const page = (data: unknown[]) => ({
  data,
  total: data.length,
  limit: 200,
  offset: 0,
  has_more: false,
});

const tree = {
  data: [
    {
      id: SITE,
      name: 'Werk Nord',
      kind: 'site',
      children: [
        { id: ROOM, name: 'Raum 1', kind: 'room', parent_id: SITE, children: [] },
        { id: BIN, name: 'Fach A1', kind: 'bin', parent_id: SITE, children: [] },
      ],
    },
  ],
};

function stocktake(extra: Record<string, unknown> = {}) {
  return {
    id: ST,
    organization_id: 'org',
    title: 'Inventur Nord',
    status: 'in_progress',
    scope: 'full',
    location_id: SITE,
    total_expected: 3,
    total_scanned: 2,
    total_missing: 2,
    total_surplus: 1,
    created_at: AT,
    updated_at: AT,
    ...extra,
  };
}

const items = [
  {
    id: 'i1',
    stocktake_id: ST,
    item_kind: 'asset',
    asset_id: ASSET,
    location_id: ROOM,
    expected_quantity: 1,
    created_at: AT,
  },
  {
    id: 'i2',
    stocktake_id: ST,
    item_kind: 'consumable',
    consumable_id: CONSUMABLE,
    location_id: BIN,
    expected_quantity: 5,
    created_at: AT,
  },
];

const differences = [
  {
    item_kind: 'asset',
    asset_id: ASSET,
    classification: 'found',
    expected_location_id: ROOM,
    found_location_id: BIN,
    expected_quantity: 1,
    counted_quantity: 1,
    asset: { id: ASSET, name: 'Laptop 1' },
  },
  {
    item_kind: 'asset',
    asset_id: ASSET_2,
    classification: 'unexpected',
    found_location_id: BIN,
    expected_quantity: 0,
    counted_quantity: 1,
    asset: { id: ASSET_2, name: 'Monitor 2' },
  },
  {
    item_kind: 'consumable',
    consumable_id: CONSUMABLE,
    classification: 'found',
    expected_location_id: BIN,
    found_location_id: BIN,
    expected_quantity: 5,
    counted_quantity: 2,
    consumable: { id: CONSUMABLE, name: 'Toner' },
  },
];

interface Stub {
  stocktake?: Record<string, unknown>;
  features?: string[];
  permissions?: string[];
  /** write answers a write; the default answers 201 or 200. */
  write?: (method: string, path: string, body: unknown) => Response | undefined;
  list?: unknown[];
}

function stubApi({
  stocktake: st = stocktake(),
  features = ['barcode', 'assets_basic', 'stock', 'stocktake_standard'],
  permissions = ['stocktake:read', 'stocktake:write', 'stocktake:complete'],
  write,
  list,
}: Stub = {}) {
  const calls: Call[] = [];
  const state = {
    stocktake: st,
    scans: [
      {
        id: 's1',
        organization_id: 'org',
        stocktake_id: ST,
        consumable_id: CONSUMABLE,
        quantity: 2,
        location_id: BIN,
        scanned_by: USER,
        scan_method: 'manual',
        scan_result: 'found',
        scanned_at: AT,
      },
    ] as Record<string, unknown>[],
  };
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://test');
      const method = (init?.method ?? 'GET').toUpperCase();
      const body = typeof init?.body === 'string' ? JSON.parse(init.body) : undefined;
      const path = url.pathname.replace(/^\/api\/v1/, '');
      calls.push({ method, path, search: url.search, body });
      if (method !== 'GET') {
        const custom = write?.(method, path, body);
        if (custom) return custom;
        if (path === `/stocktakes/${ST}/start`) {
          state.stocktake = { ...state.stocktake, status: 'in_progress' };
          return json(state.stocktake);
        }
        if (path === `/stocktakes/${ST}/cancel`) {
          state.stocktake = { ...state.stocktake, status: 'cancelled' };
          return json(state.stocktake);
        }
        if (path === `/stocktakes/${ST}/complete`) {
          state.stocktake = { ...state.stocktake, status: 'completed' };
          return json({
            stocktake: state.stocktake,
            corrections_applied: 1,
            corrections: [
              {
                movement_id: 'm1',
                movement_type: 'transfer',
                item_kind: 'asset',
                asset_id: ASSET,
                from_location_id: ROOM,
                to_location_id: BIN,
              },
            ],
          });
        }
        if (path === `/stocktakes/${ST}/scans`) {
          const scan = {
            id: `s${state.scans.length + 1}`,
            stocktake_id: ST,
            scanned_by: USER,
            scanned_at: AT,
            scan_result: 'found',
            ...(body as object),
          };
          state.scans = [scan, ...state.scans];
          return json(scan, 201);
        }
        return json({ id: 'new', ...(body as object) }, 201);
      }
      if (path === '/modules') {
        return json({
          data: ['inventory', 'stocktake'].map((key) => ({
            key,
            enabled: true,
            phase: 'P2',
            nav: [],
          })),
        });
      }
      if (path === '/entitlements') {
        return json(page(features.map((feature_key) => ({ feature_key, enabled: true }))));
      }
      if (path === '/me/permissions') return json({ user_id: USER, permissions });
      if (path === '/locations/tree') return json(tree);
      if (path === `/locations/${BIN}`) {
        return json({ id: BIN, name: 'Fach A1', kind: 'bin', parent_id: SITE, path: 'x' });
      }
      if (path === '/labels/lookup') {
        const code = url.searchParams.get('code');
        if (code === 'LOC-A1') return json({ kind: 'location', id: BIN, name: 'Fach A1' });
        if (code === 'INV-1') return json({ kind: 'asset', id: ASSET, name: 'Laptop 1' });
        if (code === 'TN-1') return json({ kind: 'consumable', id: CONSUMABLE, name: 'Toner' });
        return problem(404, 'unknown code');
      }
      if (path === '/stocktakes') return json(page(list ?? [state.stocktake]));
      if (path === `/stocktakes/${ST}` || path === `/stocktakes/${LEGACY}`) {
        return json(state.stocktake);
      }
      if (path === `/stocktakes/${ST}/items`) return json(page(items));
      if (path === `/stocktakes/${ST}/scans`) return json(page(state.scans));
      if (path === `/stocktakes/${ST}/differences`) return json(page(differences));
      if (path === `/assets/${ASSET}`) return json({ id: ASSET, name: 'Laptop 1' });
      if (path === `/consumables/${CONSUMABLE}`) return json({ id: CONSUMABLE, name: 'Toner' });
      if (path === '/assets')
        return json(page([{ id: ASSET, name: 'Laptop 1', inventory_no: 'INV-1' }]));
      if (path === '/consumables')
        return json(page([{ id: CONSUMABLE, name: 'Toner', sku: 'TN-1' }]));
      return json(page([]));
    }),
  );
  return calls;
}

const writes = (calls: Call[]) => calls.filter((call) => call.method !== 'GET');
const gets = (calls: Call[], path: string) =>
  calls.filter((call) => call.method === 'GET' && call.path === path);

function signIn() {
  useAuthStore.setState({
    token: 'header.payload.signature',
    expiresAt: new Date(Date.now() + 3600_000).toISOString(),
    user: { sub: 'user-1' },
  });
}

function renderDetail(id = ST) {
  return renderWithProviders(
    <Routes>
      <Route path="/stocktake/:id" element={<StocktakeDetailPage />} />
    </Routes>,
    { route: `/stocktake/${id}` },
  );
}

/** typeCode enters a code into the text field of the scan dialog like a handheld scanner. */
function typeCode(dialog: HTMLElement, code: string) {
  const input = within(dialog).getByLabelText('Code (Handscanner oder Eingabe)');
  fireEvent.change(input, { target: { value: code } });
  fireEvent.submit(input.closest('form')!);
}

/** chooseNode opens the site of the tree with the keyboard and takes the node. */
function chooseNode(tree: HTMLElement, name: RegExp) {
  fireEvent.keyDown(tree, { key: 'ArrowRight' });
  fireEvent.click(within(tree).getByRole('treeitem', { name }));
}

async function openScanner() {
  fireEvent.click(await screen.findByRole('button', { name: 'Scanner öffnen' }));
  return screen.findByRole('dialog', { name: 'Inventur scannen' });
}

beforeEach(async () => {
  await i18n.changeLanguage('de-DE');
  useToastStore.setState({ toasts: [] });
  Object.defineProperty(window, 'isSecureContext', { value: false, configurable: true });
  Object.defineProperty(navigator, 'mediaDevices', { value: undefined, configurable: true });
  signIn();
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  useAuthStore.setState({ token: null, expiresAt: null, user: null });
});

describe('StocktakeDetailPage', () => {
  it('starts a planned stocktake after confirmation and reads the frozen snapshot', async () => {
    const calls = stubApi({ stocktake: stocktake({ status: 'planned' }) });
    renderDetail();
    fireEvent.click(await screen.findByRole('button', { name: 'Starten' }));
    const dialog = await screen.findByRole('dialog', { name: 'Inventur starten?' });
    expect(gets(calls, `/stocktakes/${ST}/items`)).toHaveLength(0);
    fireEvent.click(within(dialog).getByRole('button', { name: 'Starten' }));
    await waitFor(() =>
      expect(writes(calls).map((call) => call.path)).toEqual([`/stocktakes/${ST}/start`]),
    );
    await waitFor(() => expect(gets(calls, `/stocktakes/${ST}/items`).length).toBeGreaterThan(0));
    expect((await screen.findAllByRole('cell', { name: 'Laptop 1' })).length).toBeGreaterThan(0);
  });

  it('sets the place found from a location label and records an asset scan', async () => {
    const calls = stubApi();
    renderDetail();
    const dialog = await openScanner();

    typeCode(dialog, 'INV-1');
    expect(
      await within(dialog).findByText(/Zuerst einen Fundort wählen/, { selector: '[role=status]' }),
    ).toBeInTheDocument();
    expect(writes(calls)).toHaveLength(0);

    typeCode(dialog, 'LOC-A1');
    expect(await within(dialog).findByText('Fundort gesetzt: Fach A1')).toBeInTheDocument();
    expect(new URLSearchParams(gets(calls, '/labels/lookup')[1]!.search).get('code')).toBe(
      'LOC-A1',
    );

    typeCode(dialog, 'INV-1');
    expect(await within(dialog).findByText('Laptop 1: Gefunden')).toBeInTheDocument();
    expect(writes(calls)).toEqual([
      expect.objectContaining({
        method: 'POST',
        path: `/stocktakes/${ST}/scans`,
        body: { asset_id: ASSET, location_id: BIN, scan_method: 'barcode', code: 'INV-1' },
      }),
    ]);
    expect((writes(calls)[0]!.body as Record<string, unknown>).scan_result).toBeUndefined();
  });

  it('counts a consumable in the bin with the sum counted so far and shows expected and counted', async () => {
    const calls = stubApi();
    renderDetail();
    const table = await screen.findByRole('region', {
      name: 'Soll-Bestand: Mengenartikel je Lagerplatz',
    });
    const row = (await within(table).findByRole('cell', { name: 'Toner' })).closest('tr')!;
    expect(
      within(row)
        .getAllByRole('cell')
        .map((cell) => cell.textContent),
    ).toEqual(expect.arrayContaining(['5', '2', '-3']));

    const dialog = await openScanner();
    typeCode(dialog, 'LOC-A1');
    await within(dialog).findByText('Fundort gesetzt: Fach A1');
    typeCode(dialog, 'TN-1');
    expect(await within(dialog).findByText('Bisher gezählt: 2')).toBeInTheDocument();
    fireEvent.change(within(dialog).getByLabelText('Zusätzliche Menge'), {
      target: { value: '3' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Zählen' }));
    await waitFor(() => expect(writes(calls)).toHaveLength(1));
    expect(writes(calls)[0]!.body).toEqual({
      consumable_id: CONSUMABLE,
      quantity: 3,
      location_id: BIN,
      scan_method: 'barcode',
      code: 'TN-1',
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Schließen' }));
    await waitFor(() =>
      expect(
        within(row)
          .getAllByRole('cell')
          .map((cell) => cell.textContent),
      ).toEqual(expect.arrayContaining(['5', '5', '0'])),
    );
  });

  it('counts a consumable in its row of the snapshot', async () => {
    const calls = stubApi();
    renderDetail();
    const input = await screen.findByLabelText('Zusätzliche Menge für Toner in Fach A1');
    fireEvent.change(input, { target: { value: '4' } });
    fireEvent.click(within(input.closest('form')!).getByRole('button', { name: 'Zählen' }));
    await waitFor(() => expect(writes(calls)).toHaveLength(1));
    expect(writes(calls)[0]!.body).toEqual({
      consumable_id: CONSUMABLE,
      quantity: 4,
      location_id: BIN,
      scan_method: 'manual',
    });
  });

  it('announces the live count of found, unexpected and open objects', async () => {
    stubApi();
    renderDetail();
    const counters = await screen.findByLabelText('Zählstand');
    expect(counters).toHaveAttribute('aria-live', 'polite');
    const value = (name: string) =>
      within(counters).getByText(name).parentElement!.querySelector('dd')!.textContent;
    expect(value('Soll')).toBe('3');
    expect(value('Gefunden')).toBe('1');
    expect(value('Unerwartet')).toBe('1');
    expect(value('Noch offen')).toBe('2');
  });

  it('splits the difference list into assets and quantities', async () => {
    stubApi();
    renderDetail();
    const assets = await screen.findByRole('region', { name: 'Assets' });
    expect(await within(assets).findByText('Abweichender Standort')).toBeInTheDocument();
    expect(within(assets).getByText('Monitor 2')).toBeInTheDocument();
    expect(within(assets).getByText('Unerwartet')).toBeInTheDocument();
    const quantities = screen.getByRole('region', { name: 'Mengen' });
    expect(within(quantities).getByText('Toner')).toBeInTheDocument();
    expect(within(quantities).getByText('-3')).toBeInTheDocument();
  });

  it('names the expected transfers and corrections and completes without apply_corrections', async () => {
    const calls = stubApi();
    renderDetail();
    await screen.findByRole('region', { name: 'Mengen' });
    fireEvent.click(await screen.findByRole('button', { name: 'Abschließen' }));
    const dialog = await screen.findByRole('dialog', { name: 'Inventur abschließen?' });
    const movements = within(dialog).getByRole('list', { name: 'Voraussichtliche Bewegungen' });
    const entries = within(movements)
      .getAllByRole('listitem')
      .map((item) => item.textContent);
    expect(entries).toEqual([
      'Umlagerung: Laptop 1 von Werk Nord › Raum 1 nach Werk Nord › Fach A1',
      'Umlagerung: Monitor 2 von — nach Werk Nord › Fach A1',
      'Korrektur: Toner in Werk Nord › Fach A1 (-3)',
    ]);
    fireEvent.click(within(dialog).getByRole('button', { name: 'Abschließen' }));
    await waitFor(() => expect(writes(calls)).toHaveLength(1));
    expect(writes(calls)[0]).toMatchObject({ method: 'POST', path: `/stocktakes/${ST}/complete` });
    expect(writes(calls)[0]!.body).toEqual({});

    expect(await screen.findByText('Gebuchte Korrekturen: 1')).toBeInTheDocument();
    expect(await screen.findByText(/nur lesbar/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Scanner öffnen' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Inventur abbrechen' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Bearbeiten' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Abschließen' })).not.toBeInTheDocument();
  });

  it('offers only cancelling for a stocktake without area', async () => {
    const calls = stubApi({
      stocktake: stocktake({
        id: LEGACY,
        status: 'planned',
        location_id: undefined,
        scope: 'room',
      }),
    });
    renderDetail(LEGACY);
    expect(await screen.findByText('Raum')).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: 'Inventur abbrechen' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Starten' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Bearbeiten' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Etiketten drucken' })).not.toBeInTheDocument();
    expect(gets(calls, `/stocktakes/${LEGACY}/items`)).toHaveLength(0);
  });

  it('offers neither start nor scan without stocktake:write and no completion without stocktake:complete', async () => {
    stubApi({ stocktake: stocktake({ status: 'planned' }), permissions: ['stocktake:read'] });
    const view = renderDetail();
    await screen.findByRole('heading', { name: 'Inventur Nord' });
    await screen.findByRole('button', { name: 'Etiketten drucken' });
    expect(screen.queryByRole('button', { name: 'Starten' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Inventur abbrechen' })).not.toBeInTheDocument();
    view.unmount();

    stubApi({ permissions: ['stocktake:read', 'stocktake:write'] });
    renderDetail();
    expect(await screen.findByRole('button', { name: 'Scanner öffnen' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Abschließen' })).not.toBeInTheDocument();
  });

  it('records a find with the ObjectPicker without barcode, offering neither scanner nor labels', async () => {
    const calls = stubApi({ features: ['assets_basic', 'stock', 'stocktake_standard'] });
    renderDetail();
    const panel = (
      await screen.findByRole('heading', { name: 'Fund ohne Scanner erfassen' })
    ).closest('form')!;
    expect(screen.queryByRole('button', { name: 'Scanner öffnen' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Etiketten drucken' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Etikett für/ })).not.toBeInTheDocument();

    const place = await screen.findByRole('tree', { name: 'Fundort' });
    chooseNode(place, /Fach A1/);

    fireEvent.change(within(panel).getByRole('combobox', { name: 'Gefundenes Objekt' }), {
      target: { value: 'Laptop' },
    });
    fireEvent.click(await within(panel).findByRole('option', { name: /Laptop 1/ }));
    fireEvent.click(within(panel).getByRole('button', { name: 'Fund erfassen' }));
    await waitFor(() => expect(writes(calls)).toHaveLength(1));
    expect(writes(calls)[0]!.body).toEqual({
      asset_id: ASSET,
      location_id: BIN,
      scan_method: 'manual',
    });
    expect(gets(calls, '/labels/lookup')).toHaveLength(0);
  });

  it('shows the detail of a refused action', async () => {
    stubApi({
      stocktake: stocktake({ status: 'planned' }),
      write: (_method, path) =>
        path.endsWith('/start') ? problem(409, 'stocktake is not planned') : undefined,
    });
    renderDetail();
    fireEvent.click(await screen.findByRole('button', { name: 'Starten' }));
    const dialog = await screen.findByRole('dialog', { name: 'Inventur starten?' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Starten' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('stocktake is not planned');
  });
});

describe('StocktakeListPage', () => {
  function renderList() {
    return renderWithProviders(
      <>
        <StocktakeListPage />
        <ToastViewport />
      </>,
      { route: '/stocktake' },
    );
  }

  it('creates a stocktake with the area location_id and without scope', async () => {
    const calls = stubApi({ list: [] });
    renderList();
    fireEvent.click(await screen.findByRole('button', { name: 'Inventur starten' }));
    const dialog = await screen.findByRole('dialog', { name: 'Inventur starten' });
    fireEvent.change(within(dialog).getByLabelText(/Bezeichnung/), { target: { value: 'Lager' } });
    const picker = await within(dialog).findByRole('tree', { name: 'Bereich' });
    chooseNode(picker, /Raum 1/);
    fireEvent.click(within(dialog).getByRole('button', { name: 'Inventur starten' }));
    await waitFor(() => expect(writes(calls)).toHaveLength(1));
    expect(writes(calls)[0]).toMatchObject({
      method: 'POST',
      path: '/stocktakes',
      body: { title: 'Lager', location_id: ROOM },
    });
    expect(writes(calls)[0]!.body).not.toHaveProperty('scope');
  });

  it('deletes a planned stocktake only, behind the undo window, and labels the scope of an old one', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const calls = stubApi({
      list: [
        stocktake({ status: 'planned' }),
        stocktake({
          id: LEGACY,
          title: 'Altinventur',
          status: 'completed',
          location_id: undefined,
          scope: 'rack',
        }),
      ],
    });
    renderList();
    const planned = (await screen.findByRole('link', { name: 'Inventur Nord' })).closest('tr')!;
    expect(screen.getByRole('link', { name: 'Inventur Nord' })).toHaveAttribute(
      'href',
      `/stocktake/${ST}`,
    );
    const old = screen.getByRole('link', { name: 'Altinventur' }).closest('tr')!;
    expect(within(old).getByText('Rack')).toBeInTheDocument();
    expect(within(old).queryByRole('button', { name: /löschen/ })).not.toBeInTheDocument();
    expect(await within(planned).findByText('Werk Nord')).toBeInTheDocument();

    fireEvent.click(within(planned).getByRole('button', { name: 'Inventur Nord löschen' }));
    expect(writes(calls)).toHaveLength(0);
    act(() => {
      vi.advanceTimersByTime(UNDO_WINDOW_MS);
    });
    await waitFor(() => expect(writes(calls)).toHaveLength(1));
    expect(writes(calls)[0]).toMatchObject({ method: 'DELETE', path: `/stocktakes/${ST}` });
  });
});
