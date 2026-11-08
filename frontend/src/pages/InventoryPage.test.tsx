import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { InventoryPage, MOVEMENT_TYPES } from './InventoryPage';
import { renderWithProviders } from '../test/utils';

// WP-274 (STK-04, E-38): the page knows the eleven movement types of the
// ledger and none of the former names. WP-806 (UI-15): the free-text booking
// dialog gave way to the flows of StockFlowDialogs (flows.test.tsx); a click
// on the object of a movement opens its history. WP-320 (UI-14): the tabs
// of the quantity items and of the deprecated availability are gone, a link
// leads to /consumables; retrieval and correction book a consumable with
// consumable_id, never with quantity_item_id or asset_id.

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const CONSUMABLE = 'c2000000-0000-4000-8000-000000000001';
const SITE = 'aa000000-0000-4000-8000-000000000001';
const BIN_A = 'aa000000-0000-4000-8000-000000000002';
const BIN_B = 'aa000000-0000-4000-8000-000000000003';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const empty = { data: [], total: 0, limit: 50, offset: 0, has_more: false };

const tree = {
  data: [
    {
      id: SITE,
      name: 'Werk Nord',
      kind: 'site',
      children: [
        { id: BIN_A, name: 'Fach A1', kind: 'bin', parent_id: SITE, children: [] },
        { id: BIN_B, name: 'Fach B2', kind: 'bin', parent_id: SITE, children: [] },
      ],
    },
  ],
};

/** stubInventoryApi answers the lists of the page and its booking dialogs. */
function stubInventoryApi({ permissions = [] as string[] } = {}) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://test');
    const method = (init?.method ?? 'GET').toUpperCase();
    if (method === 'POST') {
      const body = typeof init?.body === 'string' ? JSON.parse(init.body) : {};
      return json({ id: 'new', ...body }, 201);
    }
    if (url.pathname === '/api/v1/me/permissions') return json({ user_id: 'u', permissions });
    if (url.pathname === '/api/v1/locations/tree') return json(tree);
    if (url.pathname === '/api/v1/consumables') {
      return json({ ...empty, data: [{ id: CONSUMABLE, name: 'Toner', sku: 'TN-1' }], total: 1 });
    }
    if (url.pathname === `/api/v1/consumables/${CONSUMABLE}/stock`) {
      return json({
        ...empty,
        data: [
          {
            consumable_id: CONSUMABLE,
            location_id: BIN_A,
            on_hand: 5,
            reserved: 0,
            updated_at: '2026-11-01T10:00:00Z',
          },
        ],
        total: 1,
      });
    }
    if (url.pathname === '/api/v1/stock-movements') {
      return json({
        data: [
          {
            id: 'dd000000-0000-4000-8000-000000000001',
            organization_id: 'org-1',
            item_kind: 'consumable',
            consumable_id: CONSUMABLE,
            movement_type: 'retrieval',
            quantity: 2,
            occurred_at: '2026-10-14T08:00:00Z',
            created_at: '2026-10-14T09:00:00Z',
          },
        ],
        total: 1,
        limit: 50,
        offset: 0,
        has_more: false,
      });
    }
    return json(empty);
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

/** requested lists the paths and bodies of the requests the page sent. */
function requested(fetchMock: ReturnType<typeof stubInventoryApi>) {
  return fetchMock.mock.calls.map(([input, init]) => ({
    url: String(input),
    method: (init?.method ?? 'GET').toUpperCase(),
    body: typeof init?.body === 'string' ? JSON.parse(init.body) : undefined,
  }));
}

/** pickToner chooses the consumable Toner in the object picker of the dialog. */
async function pickToner(dialog: HTMLElement) {
  fireEvent.change(within(dialog).getByRole('combobox', { name: 'Mengenartikel' }), {
    target: { value: 'Ton' },
  });
  fireEvent.click(await within(dialog).findByRole('option', { name: /Toner/ }));
}

function optionValues(select: HTMLElement) {
  return Array.from((select as HTMLSelectElement).options).map((o) => o.value);
}

describe('InventoryPage movements', () => {
  it('filters by the eleven STK-04 types without former names', async () => {
    stubInventoryApi();
    renderWithProviders(<InventoryPage />, { route: '/inventory' });

    const filter = await screen.findByLabelText('type');
    expect(optionValues(filter)).toEqual(['', ...MOVEMENT_TYPES]);
    expect(MOVEMENT_TYPES).toHaveLength(11);
    for (const former of [
      'warehouse_transfer',
      'bin_transfer',
      'reservation_release',
      'repair_transfer',
    ]) {
      expect(optionValues(filter)).not.toContain(former);
    }
    // A consumable movement shows its consumable and when it happened.
    expect(await screen.findByText(CONSUMABLE)).toBeTruthy();
    const time = document.querySelector('time');
    expect(time?.getAttribute('dateTime')).toBe('2026-10-14T08:00:00Z');
  });

  it('offers the flows instead of the free-text dialog and opens the history of an object (WP-806)', async () => {
    stubInventoryApi();
    const fetchMock = vi.mocked(fetch);
    renderWithProviders(<InventoryPage />, { route: '/inventory' });

    expect(await screen.findByRole('button', { name: 'Empfangen' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Verschieben' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Reservieren' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Bewegung erfassen' })).toBeNull();

    fireEvent.click(await screen.findByRole('button', { name: `Historie öffnen: ${CONSUMABLE}` }));
    const dialog = await screen.findByRole('dialog', { name: `Historie: ${CONSUMABLE}` });
    expect(await within(dialog).findByText('Entnahme')).toBeTruthy();
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(([input]) =>
          String(input).includes(`/stock-movements?consumable_id=${CONSUMABLE}`),
        ),
      ).toBe(true),
    );
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes('/history/'))).toBe(false);
  });
});

describe('InventoryPage without quantity items and availability (WP-320)', () => {
  it('links to /consumables and reads neither inventory/items nor inventory/availability', async () => {
    const fetchMock = stubInventoryApi();
    renderWithProviders(<InventoryPage />, { route: '/inventory' });

    const tabs = await screen.findAllByRole('tab');
    expect(tabs.map((tab) => tab.textContent)).toEqual(['Bewegungen', 'Reservierungen']);
    expect(screen.queryByRole('tab', { name: 'Mengenartikel' })).toBeNull();
    expect(screen.queryByRole('tab', { name: 'Verfügbarkeit' })).toBeNull();
    expect(screen.getByRole('link', { name: 'Mengenartikel' })).toHaveAttribute(
      'href',
      '/consumables',
    );

    // The movement type shows its catalog label, not the raw value.
    const row = (await screen.findByText(CONSUMABLE)).closest('tr')!;
    expect(within(row).getByText('Entnahme')).toBeTruthy();
    expect(within(row).queryByText('retrieval')).toBeNull();

    fireEvent.click(screen.getByRole('tab', { name: 'Reservierungen' }));
    await waitFor(() =>
      expect(requested(fetchMock).some((r) => r.url.includes('/reservations'))).toBe(true),
    );
    for (const { url } of requested(fetchMock)) {
      expect(url).not.toContain('/inventory/items');
      expect(url).not.toContain('/inventory/availability');
    }
  });

  it('offers retrieval and correction only with stock:write', async () => {
    stubInventoryApi();
    renderWithProviders(<InventoryPage />, { route: '/inventory' });
    expect(await screen.findByRole('button', { name: 'Empfangen' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Entnahme' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Korrektur' })).toBeNull();
  });

  it('books retrieval and correction of a consumable with consumable_id', async () => {
    const fetchMock = stubInventoryApi({ permissions: ['stock:write'] });
    renderWithProviders(<InventoryPage />, { route: '/inventory' });

    let dialog = await (async () => {
      fireEvent.click(await screen.findByRole('button', { name: 'Entnahme' }));
      return screen.findByRole('dialog');
    })();
    await pickToner(dialog);
    const source = await within(dialog).findByLabelText('Quell-Fach');
    fireEvent.change(source, { target: { value: BIN_A } });
    fireEvent.change(within(dialog).getByLabelText('Menge'), { target: { value: '2' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Buchen' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());

    fireEvent.click(screen.getByRole('button', { name: 'Korrektur' }));
    dialog = await screen.findByRole('dialog');
    await pickToner(dialog);
    fireEvent.change(await within(dialog).findByLabelText('Fach'), { target: { value: BIN_A } });
    fireEvent.change(within(dialog).getByLabelText(/Grund \(Pflicht\)/), {
      target: { value: 'Zählfehler' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Buchen' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());

    const posts = requested(fetchMock).filter((r) => r.method === 'POST');
    expect(posts.map((p) => [new URL(p.url, 'http://test').pathname, p.body])).toEqual([
      [
        '/api/v1/stock-movements',
        {
          consumable_id: CONSUMABLE,
          movement_type: 'retrieval',
          from_location_id: BIN_A,
          quantity: 2,
        },
      ],
      [
        '/api/v1/stock-movements',
        {
          consumable_id: CONSUMABLE,
          movement_type: 'correction',
          from_location_id: BIN_A,
          quantity: 1,
          reason: 'Zählfehler',
        },
      ],
    ]);
    for (const { body } of posts) {
      expect(body).not.toHaveProperty('quantity_item_id');
      expect(body).not.toHaveProperty('asset_id');
    }
  });
});
