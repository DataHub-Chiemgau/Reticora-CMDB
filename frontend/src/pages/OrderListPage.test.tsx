// UI-14 (WP-812): the orders with the state machine of WP-286/WP-760. Each
// of the nine states offers its actions; approve and reject follow the
// four-eyes rule (order:approve, a known requester who is someone else).
// Creating sends supplier_id and target_location_id instead of the
// deprecated supplier, a position sends consumable_id, the goods receipt
// sends items[{item_id, quantity, location_id}] with an Idempotency-Key and
// refuses an over-delivery in the dialog; a 409 shows the problem detail.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../test/utils';
import { useScopeStore } from '../stores/scope';
import { useAuthStore } from '../auth/authStore';
import { ORDER_STATUSES, OrderListPage, orderActions } from './OrderListPage';
import type { Order } from '../api/client';

const ME = 'b1000000-0000-4000-8000-000000000001';
const OTHER = 'b1000000-0000-4000-8000-000000000002';
const SUPPLIER = 'c5000000-0000-4000-8000-000000000001';
const FOREIGN_SUPPLIER = 'c5000000-0000-4000-8000-000000000002';
const CLIENT = 'c1000000-0000-4000-8000-000000000001';
const BIN = 'aa000000-0000-4000-8000-000000000002';
const CONSUMABLE = 'c2000000-0000-4000-8000-000000000001';
const ITEM = 'e3000000-0000-4000-8000-000000000001';
const AT = '2026-11-01T10:00:00Z';

const ALL = ['order:read', 'order:write', 'order:approve', 'stock:write'];

interface Call {
  method: string;
  path: string;
  params: URLSearchParams;
  body: unknown;
  headers: Headers;
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

const page = (data: unknown[]) => ({ data, total: data.length, limit: 100, offset: 0 });

function order(extra: Partial<Order> = {}): Order {
  return {
    id: 'o-1',
    organization_id: 'org',
    order_number: 'PO-1',
    title: 'Toner Nord',
    status: 'draft',
    requested_by: OTHER,
    supplier_id: SUPPLIER,
    supplier_name: 'Bechtle',
    target_location_id: BIN,
    created_at: AT,
    updated_at: AT,
    items: [],
    ...extra,
  };
}

interface Stub {
  orders?: Order[];
  detail?: Order;
  permissions?: string[];
  features?: string[];
  write?: (method: string, path: string, body: unknown) => Response | undefined;
}

function stubApi({
  orders = [order()],
  detail,
  permissions = ALL,
  features = ['internal_orders', 'stock', 'assets_basic'],
  write,
}: Stub = {}) {
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://test');
      const method = (init?.method ?? 'GET').toUpperCase();
      const body = typeof init?.body === 'string' ? JSON.parse(init.body) : undefined;
      const path = url.pathname.replace(/^\/api\/v1/, '');
      calls.push({
        method,
        path,
        params: url.searchParams,
        body,
        headers: new Headers(init?.headers),
      });
      if (method !== 'GET') {
        const custom = write?.(method, path, body);
        if (custom) return custom;
        if (method === 'DELETE') return new Response(null, { status: 204 });
        return json({ ...order(), ...(body as object) }, method === 'POST' ? 201 : 200);
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
      if (path === '/me/permissions') return json({ user_id: ME, permissions });
      if (path === '/locations/tree') {
        return json({ data: [{ id: BIN, name: 'Fach A1', kind: 'bin', children: [] }] });
      }
      if (path === `/locations/${BIN}`) return json({ id: BIN, name: 'Fach A1', kind: 'bin' });
      if (path === '/clients') return json(page([{ id: CLIENT, name: 'Mandant Nord' }]));
      if (path === '/suppliers') {
        return json(
          page([
            { id: SUPPLIER, name: 'Bechtle', client_id: null },
            { id: FOREIGN_SUPPLIER, name: 'Fremd GmbH', client_id: 'other-client' },
          ]),
        );
      }
      if (path === '/consumables') {
        return json(page([{ id: CONSUMABLE, name: 'Toner', sku: 'TN-1', unit: 'pcs' }]));
      }
      if (path === '/orders') return json(page(orders));
      if (path.startsWith('/orders/')) return json(detail ?? orders[0]);
      return json({ detail: `unexpected ${path}` }, 404);
    }),
  );
  return calls;
}

function typeText(input: HTMLElement, text: string) {
  for (let i = 1; i <= text.length; i += 1) {
    fireEvent.change(input, { target: { value: text.slice(0, i) } });
  }
}

async function rowOf(number: string) {
  const cell = await screen.findByText(number);
  return cell.closest('tr') as HTMLElement;
}

function actionsOf(row: HTMLElement) {
  return within(row)
    .queryAllByRole('button')
    .map((button) => button.textContent);
}

beforeEach(() => {
  useAuthStore.setState({
    token: 'header.payload.signature',
    expiresAt: new Date(Date.now() + 3600_000).toISOString(),
    user: { sub: ME },
  });
});

afterEach(() => {
  vi.unstubAllGlobals();
  useScopeStore.setState({ clientId: '', siteId: '' });
});

describe('OrderListPage', () => {
  it('offers the actions of each of the nine states', async () => {
    const orders = ORDER_STATUSES.map((status, index) =>
      order({ id: `o-${index}`, order_number: `PO-${index}`, status }),
    );
    stubApi({ orders });
    renderWithProviders(<OrderListPage />);

    const expected: Record<string, string[]> = {
      draft: ['Einreichen', 'Positionen', 'Bearbeiten', 'Löschen', 'Stornieren'],
      submitted: ['Genehmigen', 'Ablehnen', 'Stornieren'],
      approved: ['Bestellen', 'Stornieren'],
      rejected: [],
      ordered: ['Wareneingang', 'Stornieren'],
      partially_received: ['Wareneingang', 'Stornieren'],
      received: ['Abschließen'],
      closed: [],
      cancelled: [],
    };
    // The rights load after the list: wait until the draft row has its actions.
    await waitFor(async () => expect(actionsOf(await rowOf('PO-0'))).toHaveLength(5));
    for (const [index, status] of ORDER_STATUSES.entries()) {
      const row = await rowOf(`PO-${index}`);
      expect(actionsOf(row), status).toEqual(expected[status]);
      expect(within(row).getByText(new RegExp(`^${statusLabel(status)}$`))).toBeInTheDocument();
    }
  });

  it('shows approve to neither the requester, nor without requester, nor without order:approve', () => {
    const submitted = order({ status: 'submitted' });
    const rights = { permissions: ALL, userId: ME };
    expect(orderActions(submitted, rights)).toContain('approve');
    expect(orderActions({ ...submitted, requested_by: ME }, rights)).not.toContain('approve');
    expect(orderActions({ ...submitted, requested_by: undefined }, rights)).not.toContain(
      'approve',
    );
    expect(
      orderActions(submitted, { permissions: ['order:read', 'order:write'], userId: ME }),
    ).toEqual(['cancel']);
    expect(orderActions(submitted, { permissions: ALL, userId: undefined })).not.toContain(
      'reject',
    );
    // Without stock:write no goods receipt, without order:write no other action.
    expect(orderActions(order({ status: 'ordered' }), { permissions: ['order:write'] })).toEqual([
      'cancel',
    ]);
    expect(orderActions(order(), { permissions: ['order:read'] })).toEqual([]);
  });

  it('hides approve on the page for the requester', async () => {
    stubApi({ orders: [order({ status: 'submitted', requested_by: ME })] });
    renderWithProviders(<OrderListPage />);
    const row = await rowOf('PO-1');
    await waitFor(() => expect(actionsOf(row)).toEqual(['Stornieren']));
  });

  it('filters by status', async () => {
    const calls = stubApi();
    renderWithProviders(<OrderListPage />);
    await rowOf('PO-1');
    fireEvent.change(screen.getByLabelText('Status filtern'), { target: { value: 'ordered' } });
    await waitFor(() =>
      expect(calls.some((c) => c.path === '/orders' && c.params.get('status') === 'ordered')).toBe(
        true,
      ),
    );
  });

  it('creates an order with supplier_id, target bin and the scope client, without supplier', async () => {
    useScopeStore.setState({ clientId: CLIENT, siteId: '' });
    const calls = stubApi({ orders: [] });
    renderWithProviders(<OrderListPage />);

    fireEvent.click(await screen.findByRole('button', { name: 'Bestellung anlegen' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Titel'), { target: { value: 'Toner' } });
    const supplier = within(dialog).getByLabelText('Lieferant');
    await within(supplier).findByRole('option', { name: 'Bechtle' });
    // A supplier of another client is not offered (422 otherwise).
    expect(within(supplier).queryByRole('option', { name: 'Fremd GmbH' })).toBeNull();
    fireEvent.change(supplier, { target: { value: SUPPLIER } });
    fireEvent.click(await within(dialog).findByRole('treeitem', { name: /Fach A1/ }));
    fireEvent.click(within(dialog).getByRole('button', { name: 'Anlegen' }));

    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true));
    const post = calls.find((c) => c.method === 'POST' && c.path === '/orders');
    expect(post?.body).toEqual({
      title: 'Toner',
      supplier_id: SUPPLIER,
      target_location_id: BIN,
      client_id: CLIENT,
    });
    expect(post?.body).not.toHaveProperty('supplier');
  });

  it('adds a consumable position with consumable_id', async () => {
    const calls = stubApi();
    renderWithProviders(<OrderListPage />);

    fireEvent.click(await screen.findByRole('button', { name: 'Positionen: PO-1' }));
    const dialog = await screen.findByRole('dialog');
    typeText(await within(dialog).findByRole('combobox', { name: 'Mengenartikel' }), 'Ton');
    fireEvent.click(await within(dialog).findByRole('option', { name: /Toner/ }));
    fireEvent.change(within(dialog).getByLabelText('Menge'), { target: { value: '5' } });
    fireEvent.change(within(dialog).getByLabelText('Einzelpreis'), { target: { value: '12.5' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Position hinzufügen' }));

    await waitFor(() =>
      expect(calls.some((c) => c.method === 'POST' && c.path === '/orders/o-1/items')).toBe(true),
    );
    const post = calls.find((c) => c.method === 'POST' && c.path === '/orders/o-1/items');
    expect(post?.body).toEqual({
      description: 'Toner',
      quantity: 5,
      unit_price: 12.5,
      consumable_id: CONSUMABLE,
    });
  });

  it('books the goods receipt with an Idempotency-Key and refuses an over-delivery', async () => {
    const ordered = order({
      status: 'partially_received',
      items: [
        {
          id: ITEM,
          organization_id: 'org',
          order_id: 'o-1',
          description: 'Toner',
          quantity: 10,
          received_quantity: 4,
          consumable_id: CONSUMABLE,
          created_at: AT,
        },
      ],
    });
    const calls = stubApi({ orders: [ordered], detail: ordered });
    renderWithProviders(<OrderListPage />);

    fireEvent.click(await screen.findByRole('button', { name: 'Wareneingang: PO-1' }));
    const dialog = await screen.findByRole('dialog');
    expect(await within(dialog).findByText(/offen: 6/)).toBeInTheDocument();
    const quantity = within(dialog).getByLabelText('Gelieferte Menge (Toner)');
    const submit = within(dialog).getByRole('button', { name: 'Wareneingang buchen' });

    fireEvent.change(quantity, { target: { value: '7' } });
    expect(within(dialog).getByText(/Überlieferung ist nicht möglich/)).toBeInTheDocument();
    expect(submit).toBeDisabled();

    fireEvent.change(quantity, { target: { value: '3' } });
    expect(submit).toBeEnabled();
    fireEvent.click(submit);

    await waitFor(() =>
      expect(calls.some((c) => c.method === 'POST' && c.path === '/orders/o-1/receive')).toBe(true),
    );
    const post = calls.find((c) => c.method === 'POST' && c.path === '/orders/o-1/receive')!;
    expect(post.body).toEqual({ items: [{ item_id: ITEM, quantity: 3, location_id: BIN }] });
    expect(post.headers.get('Idempotency-Key')).toMatch(/\S{8,}/);
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
  });

  it('shows the detail of a 409 and reloads the list', async () => {
    const calls = stubApi({
      orders: [order({ status: 'approved' })],
      write: (_method, path) =>
        path === '/orders/o-1/place'
          ? json(
              { type: 'about:blank', title: 'Conflict', status: 409, detail: 'invalid transition' },
              409,
            )
          : undefined,
    });
    renderWithProviders(<OrderListPage />);

    fireEvent.click(await screen.findByRole('button', { name: 'Bestellen: PO-1' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('invalid transition');
    await waitFor(() => expect(calls.filter((c) => c.path === '/orders')).toHaveLength(2));
  });

  it('cancels only after the confirmation', async () => {
    const calls = stubApi({ orders: [order({ status: 'ordered' })] });
    renderWithProviders(<OrderListPage />);

    fireEvent.click(await screen.findByRole('button', { name: 'Stornieren: PO-1' }));
    const dialog = await screen.findByRole('dialog');
    expect(calls.some((c) => c.path === '/orders/o-1/cancel')).toBe(false);
    fireEvent.click(within(dialog).getByRole('button', { name: 'Stornieren' }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === 'POST' && c.path === '/orders/o-1/cancel')).toBe(true),
    );
  });

  it('is not shown without the entitlement internal_orders', async () => {
    const calls = stubApi({ features: ['stock'] });
    renderWithProviders(<OrderListPage />);
    expect(await screen.findByText('Ihr Paket enthält diese Funktion nicht.')).toBeInTheDocument();
    expect(calls.some((c) => c.path === '/orders')).toBe(false);
  });
});

function statusLabel(status: string): string {
  const labels: Record<string, string> = {
    draft: 'Entwurf',
    submitted: 'Eingereicht',
    approved: 'Genehmigt',
    rejected: 'Abgelehnt',
    ordered: 'Bestellt',
    partially_received: 'Teilweise geliefert',
    received: 'Geliefert',
    closed: 'Abgeschlossen',
    cancelled: 'Storniert',
  };
  return labels[status]!;
}
