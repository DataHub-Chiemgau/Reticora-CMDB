import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { InventoryPage, MOVEMENT_TYPES } from './InventoryPage';
import { renderWithProviders } from '../test/utils';

// WP-274 (STK-04, E-38): the page knows the eleven movement types of the
// ledger and none of the former names. WP-806 (UI-15): the free-text booking
// dialog gave way to the flows of StockFlowDialogs (flows.test.tsx); a click
// on the object of a movement opens its history.

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const CONSUMABLE = 'c2000000-0000-4000-8000-000000000001';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const empty = { data: [], total: 0, limit: 50, offset: 0, has_more: false };

/** stubInventoryApi answers the lists of the page. */
function stubInventoryApi() {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://test');
    const method = (init?.method ?? 'GET').toUpperCase();
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
    if (method === 'GET') return json(empty);
    return json({ status: 404, detail: `unexpected request: ${method} ${url.pathname}` }, 404);
  });
  vi.stubGlobal('fetch', fetchMock);
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
