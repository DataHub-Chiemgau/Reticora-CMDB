// UI-15 (WP-806): ObjectHistory shows the changes and movements of one
// object, newest first. An asset mixes its changes (GET /history/asset/{id})
// with its movements (GET /stock-movements?asset_id=); a consumable shows
// only its movements and never asks /history/consumable/…; a CI shows only
// its changes with source badge and site at the time. A movement shows the
// catalog label of its type, quantity, source and target by name and the
// reason. The deprecated aliases /assets/{id}/movements and
// /consumables/{id}/movements are not called.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import i18n from '../../i18n';
import { renderWithProviders } from '../../test/utils';
import { ObjectHistory } from './ObjectHistory';

const ASSET = 'a5000000-0000-4000-8000-000000000001';
const CONSUMABLE = 'c2000000-0000-4000-8000-000000000001';
const CI = 'c1000000-0000-4000-8000-000000000009';
const SITE = 'aa000000-0000-4000-8000-000000000001';
const BIN_A = 'aa000000-0000-4000-8000-000000000002';
const BIN_B = 'aa000000-0000-4000-8000-000000000003';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const page = (data: unknown[], extra: Record<string, unknown> = {}) => ({
  data,
  total: data.length,
  limit: 50,
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
      ],
    },
  ],
};

const change = (id: string, entityType: string, entityId: string, at: string, extra = {}) => ({
  id,
  entity_type: entityType,
  entity_id: entityId,
  change_type: 'update',
  field_name: 'name',
  created_at: at,
  source: 'entity_change',
  ...extra,
});

const movement = (id: string, at: string, extra: Record<string, unknown>) => ({
  id,
  item_kind: 'asset',
  occurred_at: at,
  created_at: at,
  ...extra,
});

/** stubApi answers by path (without /api/v1) and records every requested URL. */
function stubApi(routes: Record<string, unknown>) {
  const urls: string[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://test');
      urls.push(`${url.pathname}${url.search}`);
      const path = url.pathname.replace(/^\/api\/v1/, '');
      return path in routes ? json(routes[path]) : json(page([]));
    }),
  );
  return urls;
}

/** entries are the top-level entries of the history list. */
async function entries() {
  const list = await screen.findByRole('list', { name: 'Historie' });
  return within(list)
    .getAllByRole('listitem')
    .filter((li) => li.parentElement === list);
}

beforeEach(async () => {
  await i18n.changeLanguage('de-DE');
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('ObjectHistory', () => {
  it('mixes the changes and the movements of an asset newest first', async () => {
    const urls = stubApi({
      [`/history/asset/${ASSET}`]: page(
        [
          change('h2', 'asset', ASSET, '2026-10-22T10:00:00Z'),
          change('h1', 'asset', ASSET, '2026-10-20T10:00:00Z', { field_name: 'status' }),
        ],
        { audit_included: true },
      ),
      '/stock-movements': page([
        movement('m1', '2026-10-21T10:00:00Z', {
          asset_id: ASSET,
          movement_type: 'transfer',
          from_location_id: BIN_A,
          to_location_id: BIN_B,
          reason: 'Umzug Lager',
        }),
      ]),
      '/locations/tree': tree,
    });

    renderWithProviders(<ObjectHistory entityType="asset" id={ASSET} />);

    await waitFor(async () => expect(await entries()).toHaveLength(3));
    const [first, second, third] = await entries();
    expect(first).toHaveTextContent('update · name');
    expect(second).toHaveTextContent('Umlagerung');
    await waitFor(() => expect(second).toHaveTextContent('von Fach A1'));
    expect(second).toHaveTextContent('nach Fach B2');
    expect(second).toHaveTextContent('Grund: Umzug Lager');
    expect(second!.textContent).not.toContain('transfer');
    expect(third).toHaveTextContent('update · status');

    expect(urls).toContain(`/api/v1/history/asset/${ASSET}`);
    expect(urls).toContain(`/api/v1/stock-movements?asset_id=${ASSET}`);
    expect(urls.some((u) => u.includes('/movements') && !u.includes('/stock-movements'))).toBe(
      false,
    );
  });

  it('shows only the movements of a consumable and does not ask its change history', async () => {
    const urls = stubApi({
      '/stock-movements': page([
        movement('m2', '2026-10-23T10:00:00Z', {
          item_kind: 'consumable',
          consumable_id: CONSUMABLE,
          movement_type: 'receipt',
          to_location_id: BIN_A,
          quantity: 3,
        }),
        movement('m1', '2026-10-21T10:00:00Z', {
          item_kind: 'consumable',
          consumable_id: CONSUMABLE,
          movement_type: 'retrieval',
          from_location_id: BIN_A,
          quantity: 1,
        }),
      ]),
      '/locations/tree': tree,
    });

    renderWithProviders(<ObjectHistory entityType="consumable" id={CONSUMABLE} />);

    const [first, second] = await entries();
    expect(first).toHaveTextContent('Wareneingang');
    expect(first).toHaveTextContent('Menge 3');
    await waitFor(() => expect(first).toHaveTextContent('nach Fach A1'));
    expect(second).toHaveTextContent('Entnahme');
    expect(second).toHaveTextContent('von Fach A1');

    expect(urls).toContain(`/api/v1/stock-movements?consumable_id=${CONSUMABLE}`);
    expect(urls.some((u) => u.includes('/history/'))).toBe(false);
    expect(urls.some((u) => u.includes(`/consumables/${CONSUMABLE}/movements`))).toBe(false);
  });

  it('shows only the changes of a CI with source badge and site at the time', async () => {
    const urls = stubApi({
      [`/history/ci/${CI}`]: page(
        [change('h1', 'ci', CI, '2026-10-20T10:00:00Z', { source: 'ci_change', site_id: SITE })],
        { audit_included: false },
      ),
      '/locations/tree': tree,
    });

    renderWithProviders(<ObjectHistory entityType="ci" id={CI} />);

    const [only, ...rest] = await entries();
    expect(rest).toHaveLength(0);
    expect(only).toHaveTextContent('update · name');
    expect(within(only!).getByText('Änderung')).toBeTruthy();
    await waitFor(() => expect(only).toHaveTextContent('Site Werk Nord'));
    expect(screen.getByText(/audit:read/)).toBeTruthy();
    expect(urls.some((u) => u.includes('/stock-movements'))).toBe(false);
  });
});
