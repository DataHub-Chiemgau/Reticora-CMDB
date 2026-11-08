// UI-14 (WP-320): the warehouse pages of a quantity item. The detail page
// shows the stock and the availability per bin from GET
// /consumables/{id}/stock and /availability with the bin names of the
// location tree, books retrieval, correction and transfer through POST
// /stock-movements with consumable_id (retrieval only from_location_id, a
// correction exactly one bin, a transfer two different bins), shows the
// detail of a 409 insufficient-stock and reserves a quantity in a bin via
// POST /reservations. Bookings need stock:write, the reservation
// reservation:write. The list links to the detail page, books nothing and
// marks an item only when its total stock is below min_level.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { Route, Routes } from 'react-router-dom';
import i18n from '../i18n';
import { renderWithProviders } from '../test/utils';
import { ConsumableDetailPage } from './ConsumableDetailPage';
import { ConsumableListPage } from './ConsumableListPage';
import { ToastViewport } from '../components/ui/Toast';
import { useToastStore } from '../stores/toast';
import { UNDO_WINDOW_MS } from '../hooks/useUndoableDelete';

const CONSUMABLE = 'c2000000-0000-4000-8000-000000000001';
const SITE = 'aa000000-0000-4000-8000-000000000001';
const BIN_A = 'aa000000-0000-4000-8000-000000000002';
const BIN_B = 'aa000000-0000-4000-8000-000000000003';
const BIN_C = 'aa000000-0000-4000-8000-000000000004';
const BOOKING_RIGHTS = ['stock:read', 'stock:write', 'reservation:write'];

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

const page = (data: unknown[], extra: Record<string, unknown> = {}) => ({
  data,
  total: data.length,
  limit: 200,
  offset: 0,
  has_more: false,
  ...extra,
});

const tree = {
  data: [
    {
      id: SITE,
      name: 'Werk Nord',
      kind: 'site',
      children: [
        { id: BIN_A, name: 'Fach A1', kind: 'bin', parent_id: SITE, children: [] },
        { id: BIN_B, name: 'Fach B2', kind: 'bin', parent_id: SITE, children: [] },
        { id: BIN_C, name: 'Fach C3', kind: 'bin', parent_id: SITE, children: [] },
      ],
    },
  ],
};

const consumable = {
  id: CONSUMABLE,
  organization_id: 'org-1',
  name: 'Toner',
  sku: 'TN-1',
  barcode: '4006381333931',
  category: 'printer',
  unit: 'pcs',
  stock_level: 7,
  min_level: 10,
  created_at: '2026-11-01T10:00:00Z',
  updated_at: '2026-11-01T10:00:00Z',
};

const stockRow = (location_id: string, on_hand: number, reserved: number) => ({
  consumable_id: CONSUMABLE,
  location_id,
  on_hand,
  reserved,
  updated_at: '2026-11-02T09:30:00Z',
});

interface Stub {
  permissions?: string[];
  write?: (path: string, body: unknown) => Response;
  consumables?: unknown[];
}

/** stubApi answers the reads of the pages and records every request. */
function stubApi({ permissions = BOOKING_RIGHTS, write, consumables = [] }: Stub = {}) {
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://test');
      const method = (init?.method ?? 'GET').toUpperCase();
      const body = typeof init?.body === 'string' ? JSON.parse(init.body) : undefined;
      const path = url.pathname.replace(/^\/api\/v1/, '');
      calls.push({ method, path, search: url.search, body });
      if (method !== 'GET') return write ? write(path, body) : json({ id: 'new', ...body }, 201);
      if (path === '/me/permissions') return json({ user_id: 'user-1', permissions });
      if (path === '/locations/tree') return json(tree);
      if (path === '/consumables') return json(page(consumables, { limit: 100 }));
      if (path === `/consumables/${CONSUMABLE}`) return json(consumable);
      if (path === `/consumables/${CONSUMABLE}/stock`) {
        return json(page([stockRow(BIN_A, 5, 1), stockRow(BIN_B, 2, 0)]));
      }
      if (path === `/consumables/${CONSUMABLE}/availability`) {
        // Page by page over next_cursor: the second page holds Fach B2.
        if (url.searchParams.get('cursor') === 'next-1') {
          return json(page([{ location_id: BIN_B, on_hand: 2, reserved: 0, available: 2 }]));
        }
        return json(
          page([{ location_id: BIN_A, on_hand: 5, reserved: 1, available: 4 }], {
            has_more: true,
            next_cursor: 'next-1',
          }),
        );
      }
      if (path === '/stock-movements') {
        return json(
          page([
            {
              id: 'dd000000-0000-4000-8000-000000000001',
              item_kind: 'consumable',
              consumable_id: CONSUMABLE,
              movement_type: 'receipt',
              to_location_id: BIN_A,
              quantity: 5,
              occurred_at: '2026-11-02T09:30:00Z',
              created_at: '2026-11-02T09:30:00Z',
            },
          ]),
        );
      }
      return json(page([]));
    }),
  );
  return calls;
}

const writes = (calls: Call[]) => calls.filter((c) => c.method !== 'GET');

function renderDetail() {
  return renderWithProviders(
    <>
      <Routes>
        <Route path="/consumables/:id" element={<ConsumableDetailPage />} />
      </Routes>
      <ToastViewport />
    </>,
    { route: `/consumables/${CONSUMABLE}` },
  );
}

async function openFlow(name: string) {
  fireEvent.click(await screen.findByRole('button', { name }));
  return screen.findByRole('dialog');
}

/** chooseSource takes a bin from the stock of the dialog. */
async function chooseSource(dialog: HTMLElement, label: string, bin: string) {
  const source = await within(dialog).findByLabelText(label);
  await waitFor(() => expect(within(source).getAllByRole('option').length).toBeGreaterThan(1));
  fireEvent.change(source, { target: { value: bin } });
}

/** chooseBin opens the site in the location picker of the dialog and takes the bin. */
async function chooseBin(dialog: HTMLElement, label: string, bin: RegExp) {
  const picker = await within(dialog).findByRole('tree', { name: label });
  const site = within(picker).getByRole('treeitem', { name: /Werk Nord/ });
  if (site.getAttribute('aria-expanded') === 'false') fireEvent.click(site);
  fireEvent.click(within(picker).getByRole('treeitem', { name: bin }));
}

beforeEach(async () => {
  await i18n.changeLanguage('de-DE');
  useToastStore.setState({ toasts: [] });
  vi.useFakeTimers({ shouldAdvanceTime: true });
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('ConsumableDetailPage', () => {
  it('shows the stock and the availability per bin and loads further bins over next_cursor', async () => {
    const calls = stubApi();
    renderDetail();

    expect(await screen.findByRole('heading', { name: 'Toner', level: 1 })).toBeInTheDocument();
    expect(screen.getByText('4006381333931')).toBeInTheDocument();
    expect(
      screen.getByText('Gesamtbestand 7 liegt unter dem Mindestbestand 10.'),
    ).toBeInTheDocument();

    const table = await screen.findByRole('region', { name: 'Bestand je Fach' });
    const row = (await within(table).findByText('Werk Nord › Fach A1')).closest('tr')!;
    expect(
      within(row)
        .getAllByRole('cell')
        .map((cell) => cell.textContent),
    ).toEqual(['Werk Nord › Fach A1', '5 pcs', '1', '4', expect.stringMatching(/2026/)]);
    expect(within(table).queryByText('Werk Nord › Fach B2')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Weitere Fächer laden' }));
    expect(await within(table).findByText('Werk Nord › Fach B2')).toBeInTheDocument();
    expect(calls.some((c) => c.path === `/consumables/${CONSUMABLE}/stock`)).toBe(true);
    expect(
      calls.some(
        (c) => c.path === `/consumables/${CONSUMABLE}/availability` && c.search.includes('next-1'),
      ),
    ).toBe(true);
    // The movements of the item come from the ledger (ObjectHistory, WP-806).
    await waitFor(() =>
      expect(
        calls.some((c) => c.path === '/stock-movements' && c.search.includes(CONSUMABLE)),
      ).toBe(true),
    );
  });

  it('books a retrieval with consumable_id, quantity and only the source bin', async () => {
    const calls = stubApi();
    renderDetail();

    const dialog = await openFlow('Entnahme');
    await chooseSource(dialog, 'Quell-Fach', BIN_A);
    fireEvent.change(within(dialog).getByLabelText('Menge'), { target: { value: '2' } });
    fireEvent.change(within(dialog).getByLabelText('Grund'), { target: { value: 'Drucker 3' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Buchen' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    const [call] = writes(calls);
    expect(call!.path).toBe('/stock-movements');
    expect(call!.body).toEqual({
      consumable_id: CONSUMABLE,
      movement_type: 'retrieval',
      from_location_id: BIN_A,
      quantity: 2,
      reason: 'Drucker 3',
    });
    expect(call!.body).not.toHaveProperty('to_location_id');
    expect(call!.body).not.toHaveProperty('asset_id');
  });

  it('books a correction with exactly one bin and a required reason', async () => {
    const calls = stubApi();
    renderDetail();

    // Decrease: from a bin of the stock.
    let dialog = await openFlow('Korrektur');
    await chooseSource(dialog, 'Fach', BIN_B);
    fireEvent.change(within(dialog).getByLabelText('Menge'), { target: { value: '1' } });
    const submit = within(dialog).getByRole('button', { name: 'Buchen' });
    expect(submit).toBeDisabled();
    fireEvent.change(within(dialog).getByLabelText(/Grund \(Pflicht\)/), {
      target: { value: 'Zählfehler' },
    });
    fireEvent.click(submit);
    await waitFor(() => expect(writes(calls)).toHaveLength(1));
    expect(writes(calls)[0]!.body).toEqual({
      consumable_id: CONSUMABLE,
      movement_type: 'correction',
      from_location_id: BIN_B,
      quantity: 1,
      reason: 'Zählfehler',
    });
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    // Increase: into any bin of the location tree.
    dialog = await openFlow('Korrektur');
    fireEvent.change(within(dialog).getByLabelText('Richtung'), { target: { value: 'increase' } });
    await chooseBin(dialog, 'Fach', /Fach C3/);
    fireEvent.change(within(dialog).getByLabelText('Menge'), { target: { value: '3' } });
    fireEvent.change(within(dialog).getByLabelText(/Grund \(Pflicht\)/), {
      target: { value: 'Fund' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Buchen' }));
    await waitFor(() => expect(writes(calls)).toHaveLength(2));
    expect(writes(calls)[1]!.body).toEqual({
      consumable_id: CONSUMABLE,
      movement_type: 'correction',
      to_location_id: BIN_C,
      quantity: 3,
      reason: 'Fund',
    });
  });

  it('books a transfer between two different bins after the undo window', async () => {
    const calls = stubApi();
    renderDetail();

    const dialog = await openFlow('Umlagerung');
    // The item is fixed on its page: no object picker.
    expect(within(dialog).queryByRole('combobox', { name: 'Mengenartikel' })).toBeNull();
    await chooseSource(dialog, 'Quell-Fach', BIN_A);
    await chooseBin(dialog, 'Ziel-Fach', /Fach A1/);
    expect(within(dialog).getByRole('alert')).toHaveTextContent(
      'Ziel-Fach und Quell-Fach müssen sich unterscheiden.',
    );
    await chooseBin(dialog, 'Ziel-Fach', /Fach B2/);
    fireEvent.change(within(dialog).getByLabelText('Menge'), { target: { value: '2' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Verschieben' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    act(() => {
      vi.advanceTimersByTime(UNDO_WINDOW_MS);
    });
    await waitFor(() => expect(writes(calls)).toHaveLength(1));
    expect(writes(calls)[0]!.body).toEqual({
      consumable_id: CONSUMABLE,
      movement_type: 'transfer',
      from_location_id: BIN_A,
      to_location_id: BIN_B,
      quantity: 2,
    });
  });

  it('shows the detail of a 409 insufficient-stock in the dialog', async () => {
    stubApi({
      write: () =>
        json(
          {
            type: 'https://reticora.io/problems/insufficient-stock',
            title: 'Insufficient stock',
            status: 409,
            detail: 'bin holds 5, requested 9',
          },
          409,
        ),
    });
    renderDetail();

    const dialog = await openFlow('Entnahme');
    await chooseSource(dialog, 'Quell-Fach', BIN_A);
    fireEvent.change(within(dialog).getByLabelText('Menge'), { target: { value: '9' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Buchen' }));

    expect(await within(dialog).findByRole('alert')).toHaveTextContent('bin holds 5, requested 9');
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });

  it('shows field errors of violations at their input', async () => {
    stubApi({
      write: () =>
        json(
          {
            title: 'Unprocessable',
            status: 422,
            detail: 'invalid booking',
            violations: [{ field: 'quantity', detail: 'exceeds the stock of the bin' }],
          },
          422,
        ),
    });
    renderDetail();

    const dialog = await openFlow('Entnahme');
    await chooseSource(dialog, 'Quell-Fach', BIN_A);
    fireEvent.click(within(dialog).getByRole('button', { name: 'Buchen' }));
    const quantity = within(dialog).getByLabelText('Menge');
    await waitFor(() => expect(quantity).toHaveAttribute('aria-invalid', 'true'));
    expect(within(dialog).getByText('exceeds the stock of the bin')).toBeInTheDocument();
  });

  it('reserves a quantity in a bin through POST /reservations', async () => {
    const calls = stubApi();
    renderDetail();

    const dialog = await openFlow('Reservieren');
    await chooseSource(dialog, 'Fach', BIN_A);
    fireEvent.change(within(dialog).getByLabelText('Menge'), { target: { value: '5' } });
    // Fach A1 holds 5 with 1 reserved: at most 4.
    expect(within(dialog).getByRole('button', { name: 'Reservieren' })).toBeDisabled();
    fireEvent.change(within(dialog).getByLabelText('Menge'), { target: { value: '3' } });
    fireEvent.change(within(dialog).getByLabelText('Verfällt am (optional)'), {
      target: { value: '2026-11-30' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Reservieren' }));

    await waitFor(() => expect(writes(calls)).toHaveLength(1));
    const [call] = writes(calls);
    expect(call!.path).toBe('/reservations');
    expect(call!.body).toEqual({
      item_kind: 'consumable',
      consumable_id: CONSUMABLE,
      location_id: BIN_A,
      quantity: 3,
      expires_at: new Date('2026-11-30T23:59:59').toISOString(),
    });
  });

  it('offers no bookings without stock:write and no reservation without reservation:write', async () => {
    stubApi({ permissions: ['stock:read', 'reservation:write'] });
    const first = renderDetail();
    expect(await screen.findByRole('button', { name: 'Reservieren' })).toBeInTheDocument();
    for (const name of ['Wareneingang', 'Entnahme', 'Korrektur', 'Umlagerung']) {
      expect(screen.queryByRole('button', { name })).toBeNull();
    }
    first.unmount();

    stubApi({ permissions: ['stock:read', 'stock:write'] });
    renderDetail();
    expect(await screen.findByRole('button', { name: 'Entnahme' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Reservieren' })).toBeNull();
  });
});

describe('ConsumableListPage', () => {
  const item = (id: string, name: string, stock_level: number, min_level: number) => ({
    ...consumable,
    id,
    name,
    stock_level,
    min_level,
  });

  it('links to the detail page, books nothing and marks only a total below min_level', async () => {
    const calls = stubApi({
      consumables: [
        item(CONSUMABLE, 'Toner', 7, 10),
        item('c2000000-0000-4000-8000-000000000002', 'Kabel', 0, 0),
        item('c2000000-0000-4000-8000-000000000003', 'Papier', 10, 10),
      ],
    });
    renderWithProviders(<ConsumableListPage />, { route: '/consumables' });

    const link = await screen.findByRole('link', { name: 'Toner' });
    expect(link).toHaveAttribute('href', `/consumables/${CONSUMABLE}`);
    expect(screen.queryByRole('button', { name: /^[+−]1/ })).toBeNull();
    expect(screen.queryByText('+1')).toBeNull();

    const status = (name: string) =>
      within(screen.getByRole('link', { name }).closest('tr')!).getAllByRole('cell')[4]!
        .textContent;
    expect(status('Toner')).toBe('niedrig');
    expect(status('Kabel')).toBe('OK');
    expect(status('Papier')).toBe('OK');
    expect(screen.getAllByText('7 pcs')).toHaveLength(1);

    fireEvent.click(screen.getByLabelText('Nur Mindestbestand unterschritten'));
    await waitFor(() =>
      expect(
        calls.some((c) => c.path === '/consumables' && c.search.includes('low_stock=true')),
      ).toBe(true),
    );
    expect(calls.some((c) => c.path.endsWith('/movements'))).toBe(false);
  });

  it('creates an item without initial stock and shows the detail of a duplicate', async () => {
    const calls = stubApi({
      write: () => json({ title: 'Conflict', status: 409, detail: 'sku TN-1 already exists' }, 409),
    });
    renderWithProviders(<ConsumableListPage />, { route: '/consumables' });

    fireEvent.click(await screen.findByRole('button', { name: 'Artikel anlegen' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText(/^Name/), { target: { value: 'Toner' } });
    fireEvent.change(within(dialog).getByLabelText('SKU'), { target: { value: 'TN-1' } });
    fireEvent.change(within(dialog).getByLabelText('Einheit'), { target: { value: 'pcs' } });
    fireEvent.change(within(dialog).getByLabelText('Kategorie'), { target: { value: 'printer' } });
    fireEvent.change(within(dialog).getByLabelText('Mindestbestand'), { target: { value: '4' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Anlegen' }));

    expect(await within(dialog).findByRole('alert')).toHaveTextContent('sku TN-1 already exists');
    const [call] = writes(calls);
    expect(call!.path).toBe('/consumables');
    expect(call!.body).toEqual({
      name: 'Toner',
      sku: 'TN-1',
      unit: 'pcs',
      category: 'printer',
      min_level: 4,
    });
  });
});
