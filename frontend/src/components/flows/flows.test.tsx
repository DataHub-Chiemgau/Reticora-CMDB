// UI-15 (WP-806): the everyday flows of the inventory page. Receiving,
// moving and reserving choose objects and locations with the pickers and
// send only after the undo window (useUndoableSubmit): receive books a
// receipt of a consumable into a bin, move takes the source bin from the
// stock per bin and refuses the same bin as target, reserve goes to POST
// /assets/{id}/reserve. Undo sends nothing, a failure after the window is an
// error toast. Moving a serial asset books a transfer with asset_id and is
// offered only with stock:write and asset:move. The reservation list
// converts and releases with the message of a failure at the row. Without
// module or feature the flows are hidden. No other movement type is booked
// with asset_id, assets are never reserved through POST /reservations, and
// /assets/{id}/movements is not called.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import i18n from '../../i18n';
import { renderWithProviders } from '../../test/utils';
import { InventoryPage } from '../../pages/InventoryPage';
import { ToastViewport } from '../ui/Toast';
import { useToastStore } from '../../stores/toast';
import { useAuthStore } from '../../auth/authStore';
import { UNDO_WINDOW_MS } from '../../hooks/useUndoableDelete';
import { PICKER_DEBOUNCE_MS } from '../pickers/ObjectPicker';

const CONSUMABLE = 'c2000000-0000-4000-8000-000000000001';
const ASSET = 'a5000000-0000-4000-8000-000000000001';
const USER = 'b1000000-0000-4000-8000-000000000001';
const TEAM = 'b2000000-0000-4000-8000-000000000001';
const SITE = 'aa000000-0000-4000-8000-000000000001';
const BIN_A = 'aa000000-0000-4000-8000-000000000002';
const BIN_B = 'aa000000-0000-4000-8000-000000000003';
const USER_RESERVATION = 'dd000000-0000-4000-8000-000000000001';
const PROJECT_RESERVATION = 'dd000000-0000-4000-8000-000000000002';

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

const page = (data: unknown[]) => ({
  data,
  total: data.length,
  limit: 50,
  offset: 0,
  has_more: false,
});

const problem = (status: number, type: string, detail: string) =>
  json({ type, title: 'Problem', status, detail }, status);

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

const reservations = [
  {
    id: USER_RESERVATION,
    item_kind: 'asset',
    asset_id: ASSET,
    quantity: 1,
    state: 'active',
    reserved_from: '2026-11-01T10:00:00Z',
    reserved_until: '2026-11-20T22:59:59Z',
    assignee_id: USER,
  },
  {
    id: PROJECT_RESERVATION,
    item_kind: 'asset',
    asset_id: 'a5000000-0000-4000-8000-000000000002',
    quantity: 1,
    state: 'active',
    reserved_from: '2026-11-01T10:00:00Z',
    project_ref: 'Rollout-7',
  },
];

interface Stub {
  /** write answers every write; 201 with the body by default. */
  write?: (path: string, body: unknown) => Response;
  modules?: { key: string; enabled: boolean }[];
  features?: Record<string, boolean>;
  permissions?: string[];
}

/** stubApi answers the reads of the page and its pickers and records every request. */
function stubApi({ write, modules, features, permissions = [] }: Stub = {}) {
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
      if (path === '/modules') {
        return json({
          data: (modules ?? []).map((m) => ({ ...m, phase: 'P2', nav: [] })),
        });
      }
      if (path === '/entitlements') {
        return json(
          page(
            Object.entries(features ?? {}).map(([feature_key, enabled]) => ({
              feature_key,
              enabled,
            })),
          ),
        );
      }
      if (path === '/me/permissions') return json({ user_id: USER, permissions });
      if (path === '/locations/tree') return json(tree);
      if (path === '/consumables') {
        return json(page([{ id: CONSUMABLE, name: 'Toner', sku: 'TN-1', unit: 'pcs' }]));
      }
      if (path === '/assets') {
        return json(page([{ id: ASSET, name: 'Laptop 7', inventory_no: 'INV-7' }]));
      }
      if (path === `/consumables/${CONSUMABLE}/stock`) {
        return json(
          page([
            {
              consumable_id: CONSUMABLE,
              location_id: BIN_A,
              on_hand: 5,
              reserved: 1,
              updated_at: '2026-11-01T10:00:00Z',
            },
            {
              consumable_id: CONSUMABLE,
              location_id: BIN_B,
              on_hand: 2,
              reserved: 0,
              updated_at: '2026-11-01T10:00:00Z',
            },
          ]),
        );
      }
      if (path === '/users') return json(page([{ id: USER, display_name: 'Anna Berg' }]));
      if (path === '/teams') return json(page([{ id: TEAM, name: 'Service Desk' }]));
      if (path === '/reservations') return json(page(reservations));
      return json(page([]));
    }),
  );
  return calls;
}

const writes = (calls: Call[]) => calls.filter((c) => c.method !== 'GET');

function signIn() {
  useAuthStore.setState({
    token: 'header.payload.signature',
    expiresAt: new Date(Date.now() + 3600_000).toISOString(),
    user: { sub: 'user-1' },
  });
}

function renderPage() {
  return renderWithProviders(
    <>
      <InventoryPage />
      <ToastViewport />
    </>,
    { route: '/inventory' },
  );
}

/**
 * choose types into a picker of the scope and takes the option. The picker
 * searches once the input rested for PICKER_DEBOUNCE_MS; the fake clock
 * passes that pause at once instead of waiting it out in steps of real
 * time, which a loaded machine stretches.
 */
async function choose(scope: HTMLElement, label: string, text: string, option: RegExp) {
  fireEvent.change(within(scope).getByRole('combobox', { name: label }), {
    target: { value: text },
  });
  elapse(PICKER_DEBOUNCE_MS);
  fireEvent.click(await within(scope).findByRole('option', { name: option }));
}

/** chooseBin opens the site in the location picker of the scope and takes the bin. */
async function chooseBin(scope: HTMLElement, tree: string, bin: RegExp) {
  const picker = await within(scope).findByRole('tree', { name: tree });
  const site = within(picker).getByRole('treeitem', { name: /Werk Nord/ });
  if (site.getAttribute('aria-expanded') === 'false') fireEvent.click(site);
  fireEvent.click(within(picker).getByRole('treeitem', { name: bin }));
}

async function openFlow(name: string) {
  fireEvent.click(await screen.findByRole('button', { name }));
  return screen.findByRole('dialog');
}

function elapse(ms = UNDO_WINDOW_MS) {
  act(() => {
    vi.advanceTimersByTime(ms);
  });
}

/** receiveToner fills the receive dialog: 5 × Toner into Fach A1. */
async function receiveToner() {
  const dialog = await openFlow('Empfangen');
  await choose(dialog, 'Mengenartikel', 'Ton', /Toner/);
  await chooseBin(dialog, 'Ziel-Fach', /Fach A1/);
  fireEvent.change(within(dialog).getByLabelText('Menge'), { target: { value: '5' } });
  fireEvent.change(within(dialog).getByLabelText('Grund'), { target: { value: 'Lieferung' } });
  fireEvent.click(within(dialog).getByRole('button', { name: 'Empfangen' }));
  await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
}

beforeEach(async () => {
  await i18n.changeLanguage('de-DE');
  useToastStore.setState({ toasts: [] });
  vi.useFakeTimers({ shouldAdvanceTime: true });
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  useAuthStore.setState({ token: null, expiresAt: null, user: null });
});

describe('stock flows', () => {
  it('receives a consumable into a bin after the undo window', async () => {
    const calls = stubApi();
    renderPage();

    const dialog = await openFlow('Empfangen');
    // Only bins can be chosen; the site is the path.
    const picker = await within(dialog).findByRole('tree', { name: 'Ziel-Fach' });
    expect(within(picker).getByRole('treeitem', { name: /Werk Nord/ })).not.toHaveAttribute(
      'aria-selected',
    );
    fireEvent.click(within(dialog).getByRole('button', { name: 'Abbrechen' }));
    await receiveToner();

    expect(screen.getByText('5 × „Toner (TN-1)“ wird in Fach A1 gebucht')).toBeInTheDocument();
    expect(writes(calls)).toHaveLength(0);
    elapse();
    await waitFor(() => expect(writes(calls)).toHaveLength(1));
    const [call] = writes(calls);
    expect(call!.method).toBe('POST');
    expect(call!.path).toBe('/stock-movements');
    expect(call!.body).toEqual({
      consumable_id: CONSUMABLE,
      movement_type: 'receipt',
      to_location_id: BIN_A,
      quantity: 5,
      reason: 'Lieferung',
    });
  });

  it('sends nothing after Undo', async () => {
    const calls = stubApi();
    renderPage();
    await receiveToner();

    fireEvent.click(screen.getByRole('button', { name: 'Rückgängig' }));
    elapse(UNDO_WINDOW_MS * 2);
    expect(writes(calls)).toHaveLength(0);
  });

  it('moves stock from a bin of the stock list, refuses the same bin and reports 409 as toast', async () => {
    const calls = stubApi({
      write: () =>
        problem(409, 'https://reticora.io/problems/insufficient-stock', 'insufficient stock'),
    });
    renderPage();

    const dialog = await openFlow('Verschieben');
    await choose(dialog, 'Mengenartikel', 'Ton', /Toner/);
    const source = await within(dialog).findByLabelText('Quell-Fach');
    await waitFor(() =>
      expect(
        within(source)
          .getAllByRole('option')
          .map((o) => o.textContent),
      ).toEqual([
        'Fach wählen',
        'Fach A1 – Bestand 5, reserviert 1',
        'Fach B2 – Bestand 2, reserviert 0',
      ]),
    );
    expect(calls.some((c) => c.path === `/consumables/${CONSUMABLE}/stock`)).toBe(true);
    fireEvent.change(source, { target: { value: BIN_A } });

    await chooseBin(dialog, 'Ziel-Fach', /Fach A1/);
    expect(
      within(dialog).getByText('Ziel-Fach und Quell-Fach müssen sich unterscheiden.'),
    ).toBeInTheDocument();
    const submit = within(dialog).getByRole('button', { name: 'Verschieben' });
    expect(submit).toBeDisabled();

    await chooseBin(dialog, 'Ziel-Fach', /Fach B2/);
    fireEvent.change(within(dialog).getByLabelText('Menge'), { target: { value: '5' } });
    expect(within(dialog).getByText(/Höchstens 4 verfügbar/)).toBeInTheDocument();
    expect(submit).toBeDisabled();
    fireEvent.change(within(dialog).getByLabelText('Menge'), { target: { value: '3' } });
    expect(submit).toBeEnabled();
    fireEvent.click(submit);
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    expect(writes(calls)).toHaveLength(0);
    elapse();
    expect(
      await screen.findByText(
        'Verschieben von „Toner (TN-1)“ fehlgeschlagen: Der Bestand im Fach reicht nicht aus.',
      ),
    ).toBeInTheDocument();
    expect(writes(calls)[0]!.body).toEqual({
      consumable_id: CONSUMABLE,
      movement_type: 'transfer',
      from_location_id: BIN_A,
      to_location_id: BIN_B,
      quantity: 3,
    });
  });

  it('reserves an asset for a person and for a project through POST /assets/{id}/reserve', async () => {
    const calls = stubApi();
    renderPage();

    let dialog = await openFlow('Reservieren');
    await choose(dialog, 'Asset', 'Lap', /Laptop 7/);
    fireEvent.change(within(dialog).getByLabelText('Reserviert bis'), {
      target: { value: '2026-12-24' },
    });
    await choose(dialog, 'Person', 'Ann', /Anna Berg/);
    fireEvent.click(within(dialog).getByRole('button', { name: 'Reservieren' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(screen.getByText('„Laptop 7 (INV-7)“ wird reserviert')).toBeInTheDocument();
    elapse();
    await waitFor(() => expect(writes(calls)).toHaveLength(1));
    expect(writes(calls)[0]!.path).toBe(`/assets/${ASSET}/reserve`);
    const forUser = writes(calls)[0]!.body as Record<string, unknown>;
    expect(forUser.assignee_id).toBe(USER);
    expect(forUser).not.toHaveProperty('project_ref');
    expect(new Date(forUser.reserved_until as string).getTime()).toBe(
      new Date('2026-12-24T23:59:59').getTime(),
    );

    dialog = await openFlow('Reservieren');
    await choose(dialog, 'Asset', 'Lap', /Laptop 7/);
    fireEvent.change(within(dialog).getByLabelText('Reserviert bis'), {
      target: { value: '2026-12-24' },
    });
    fireEvent.change(within(dialog).getByLabelText('Reserviert für'), {
      target: { value: 'project' },
    });
    fireEvent.change(within(dialog).getByLabelText('Projekt'), { target: { value: 'Rollout-7' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Reservieren' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    elapse();
    await waitFor(() => expect(writes(calls)).toHaveLength(2));
    const forProject = writes(calls)[1]!.body as Record<string, unknown>;
    expect(writes(calls)[1]!.path).toBe(`/assets/${ASSET}/reserve`);
    expect(forProject.project_ref).toBe('Rollout-7');
    expect(forProject).not.toHaveProperty('assignee_id');
    expect(calls.some((c) => c.method === 'POST' && c.path === '/reservations')).toBe(false);
  });

  it('reports an asset reserved already after the window as error toast', async () => {
    stubApi({
      write: () => problem(409, 'https://reticora.io/problems/asset-reserved', 'reserved'),
    });
    renderPage();

    const dialog = await openFlow('Reservieren');
    await choose(dialog, 'Asset', 'Lap', /Laptop 7/);
    fireEvent.change(within(dialog).getByLabelText('Reserviert bis'), {
      target: { value: '2026-12-24' },
    });
    await choose(dialog, 'Person', 'Ann', /Anna Berg/);
    fireEvent.click(within(dialog).getByRole('button', { name: 'Reservieren' }));
    elapse();
    expect(
      await screen.findByText(
        'Reservieren von „Laptop 7 (INV-7)“ fehlgeschlagen: Das Asset ist bereits reserviert.',
      ),
    ).toBeInTheDocument();
  });

  it('converts and releases asset reservations with the failure at the row', async () => {
    const calls = stubApi({
      write: (path) =>
        path.endsWith('/release')
          ? problem(409, 'about:blank', 'reservation is not active')
          : json({ reservation: reservations[0] }),
    });
    renderPage();
    fireEvent.click(await screen.findByRole('tab', { name: 'Reservierungen' }));

    // A reservation for a person converts at once, to that person.
    fireEvent.click(
      await screen.findByRole('button', { name: `In Zuweisung umwandeln: ${ASSET}` }),
    );
    await waitFor(() => expect(writes(calls)).toHaveLength(1));
    expect(writes(calls)[0]!.path).toBe(`/reservations/${USER_RESERVATION}/convert`);
    expect(writes(calls)[0]!.body).toEqual({});

    // A project reservation asks for the recipient.
    fireEvent.click(
      screen.getByRole('button', {
        name: 'In Zuweisung umwandeln: a5000000-0000-4000-8000-000000000002',
      }),
    );
    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent('Rollout-7');
    await choose(dialog, 'Empfänger', 'Serv', /Service Desk/);
    fireEvent.click(within(dialog).getByRole('button', { name: 'Umwandeln' }));
    await waitFor(() => expect(writes(calls)).toHaveLength(2));
    expect(writes(calls)[1]!.path).toBe(`/reservations/${PROJECT_RESERVATION}/convert`);
    expect(writes(calls)[1]!.body).toEqual({ team_id: TEAM });

    fireEvent.click(screen.getByRole('button', { name: `Freigeben: ${ASSET}` }));
    await waitFor(() => expect(writes(calls)).toHaveLength(3));
    expect(writes(calls)[2]!.path).toBe(`/reservations/${USER_RESERVATION}/release`);
    const row = screen.getByRole('button', { name: `Freigeben: ${ASSET}` }).closest('tr')!;
    expect(
      await within(row).findByText('Die Reservierung ist nicht mehr aktiv.'),
    ).toBeInTheDocument();
    expect(row).toHaveTextContent('bis');
  });

  it('moves a serial asset with asset_id and transfer only with stock:write and asset:move', async () => {
    const calls = stubApi({ permissions: ['stock:write', 'asset:move'] });
    renderPage();

    const dialog = await openFlow('Asset verschieben');
    await choose(dialog, 'Asset', 'Lap', /Laptop 7/);
    const picker = await within(dialog).findByRole('tree', { name: 'Ziel-Standort' });
    fireEvent.click(within(picker).getByRole('treeitem', { name: /Werk Nord/ }));
    fireEvent.change(within(dialog).getByLabelText('Grund'), { target: { value: 'Umzug' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Asset verschieben' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    elapse();
    await waitFor(() => expect(writes(calls)).toHaveLength(1));
    expect(writes(calls)[0]!.path).toBe('/stock-movements');
    expect(writes(calls)[0]!.body).toEqual({
      asset_id: ASSET,
      movement_type: 'transfer',
      to_location_id: SITE,
      reason: 'Umzug',
    });
    expect(calls.some((c) => c.path.endsWith('/movements'))).toBe(false);
    expect(calls.some((c) => c.method === 'PATCH')).toBe(false);
    expect(calls.some((c) => c.path === `/assets/${ASSET}/transfer`)).toBe(false);
  });

  it.each([[['stock:write']], [['asset:move']]])(
    'hides the move of an asset with only %j',
    async (permissions) => {
      const calls = stubApi({ permissions });
      renderPage();

      expect(await screen.findByRole('button', { name: 'Verschieben' })).toBeInTheDocument();
      await waitFor(() => expect(calls.some((c) => c.path === '/me/permissions')).toBe(true));
      await act(async () => {
        await Promise.resolve();
      });
      expect(screen.queryByRole('button', { name: 'Asset verschieben' })).not.toBeInTheDocument();
    },
  );

  it('hides receive and move without the feature stock or the module stocktake', async () => {
    signIn();
    const calls = stubApi({
      modules: [
        { key: 'inventory', enabled: true },
        { key: 'stocktake', enabled: true },
      ],
      features: { stock: false, assets_basic: true },
      permissions: ['stock:write', 'asset:move'],
    });
    renderPage();

    await waitFor(() => expect(calls.some((c) => c.path === '/entitlements')).toBe(true));
    await waitFor(() =>
      expect(screen.queryByRole('button', { name: 'Empfangen' })).not.toBeInTheDocument(),
    );
    expect(screen.queryByRole('button', { name: 'Verschieben' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Asset verschieben' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Reservieren' })).toBeInTheDocument();
  });

  it('hides receive and move without the module stocktake and reserve without assets_basic', async () => {
    signIn();
    const calls = stubApi({
      modules: [
        { key: 'inventory', enabled: true },
        { key: 'stocktake', enabled: false },
      ],
      features: { stock: true, assets_basic: false },
    });
    renderPage();

    await waitFor(() => expect(calls.some((c) => c.path === '/modules')).toBe(true));
    await waitFor(() => expect(calls.some((c) => c.path === '/entitlements')).toBe(true));
    await waitFor(() =>
      expect(screen.queryByRole('button', { name: 'Empfangen' })).not.toBeInTheDocument(),
    );
    await waitFor(() =>
      expect(screen.queryByRole('button', { name: 'Reservieren' })).not.toBeInTheDocument(),
    );
    expect(screen.queryByRole('button', { name: 'Verschieben' })).not.toBeInTheDocument();
  });

  it('hides every flow without the module inventory', async () => {
    signIn();
    const calls = stubApi({
      modules: [
        { key: 'inventory', enabled: false },
        { key: 'stocktake', enabled: true },
      ],
      features: { stock: true, assets_basic: true },
      permissions: ['stock:write', 'asset:move'],
    });
    renderPage();

    await waitFor(() => expect(calls.some((c) => c.path === '/modules')).toBe(true));
    for (const name of ['Empfangen', 'Verschieben', 'Asset verschieben', 'Reservieren']) {
      await waitFor(() => expect(screen.queryByRole('button', { name })).not.toBeInTheDocument());
    }
  });
});
