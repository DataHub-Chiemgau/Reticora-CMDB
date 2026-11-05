import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { InventoryPage, MOVEMENT_TYPES } from './InventoryPage';
import { renderWithProviders } from '../test/utils';

// WP-274 (STK-04, E-38): the page knows the eleven movement types of the
// ledger and none of the former names; the booking dialog offers per object
// kind only the types POST /stock-movements takes and sends quantity only
// with a quantity item.

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const ASSET = 'a5000000-0000-4000-8000-000000000001';
const ITEM = 'c1000000-0000-4000-8000-000000000001';
const CONSUMABLE = 'c2000000-0000-4000-8000-000000000001';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const empty = { data: [], total: 0, limit: 50, offset: 0, has_more: false };

/** stubInventoryApi answers the lists of the page and records every POST /stock-movements body. */
function stubInventoryApi() {
  const posts: Record<string, unknown>[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://test');
    const method = (init?.method ?? 'GET').toUpperCase();
    if (url.pathname === '/api/v1/stock-movements' && method === 'POST') {
      const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
      posts.push(body);
      return json({ id: 'm-1', item_kind: 'asset', ...body }, 201);
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
    if (method === 'GET') return json(empty);
    return json({ status: 404, detail: `unexpected request: ${method} ${url.pathname}` }, 404);
  });
  vi.stubGlobal('fetch', fetchMock);
  return posts;
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

  it('books an asset without quantity and a quantity item with quantity', async () => {
    const posts = stubInventoryApi();
    renderWithProviders(<InventoryPage />, { route: '/inventory' });

    fireEvent.click(await screen.findByRole('button', { name: 'Bewegung erfassen' }));
    const dialog = await screen.findByRole('dialog');
    expect(optionValues(within(dialog).getByLabelText('Objektart'))).toEqual([
      'asset',
      'quantity_item',
    ]);
    const type = within(dialog).getByLabelText('Bewegungsart');
    expect(optionValues(type)).toEqual([
      'receipt',
      'transfer',
      'deployment',
      'retrieval',
      'repair',
      'correction',
    ]);
    expect(within(dialog).queryByLabelText('Menge')).toBeNull();

    fireEvent.change(type, { target: { value: 'transfer' } });
    fireEvent.change(within(dialog).getByLabelText('Asset-ID (serialisiert)'), {
      target: { value: ASSET },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Bewegung erfassen' }));
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0]).toEqual({ asset_id: ASSET, movement_type: 'transfer' });

    fireEvent.click(await screen.findByRole('button', { name: 'Bewegung erfassen' }));
    const second = await screen.findByRole('dialog');
    fireEvent.change(within(second).getByLabelText('Objektart'), {
      target: { value: 'quantity_item' },
    });
    const quantityType = within(second).getByLabelText('Bewegungsart');
    expect(optionValues(quantityType)).toEqual(['receipt', 'retrieval', 'correction', 'transfer']);
    fireEvent.change(quantityType, { target: { value: 'retrieval' } });
    fireEvent.change(within(second).getByLabelText('Mengenartikel-ID'), {
      target: { value: ITEM },
    });
    fireEvent.change(within(second).getByLabelText('Menge'), { target: { value: '3' } });
    fireEvent.click(within(second).getByRole('button', { name: 'Bewegung erfassen' }));
    await waitFor(() => expect(posts).toHaveLength(2));
    expect(posts[1]).toEqual({ quantity_item_id: ITEM, quantity: 3, movement_type: 'retrieval' });
  });
});
