// AST-02/API-06 (WP-746) and UI-15 (WP-318): the assignment page writes
// through the asset and chooses asset and recipient with pickers instead of
// ids. Assigning sends POST /assets/{id}/assign only after the undo window
// (Undo sends nothing, leaving the page sends at once, a failure is an
// error toast); taking back and transferring call /return and /transfer
// with the holder of the row as expected_holder, and 409 or 403 show in the
// dialog. Nothing is written below /assignments. The table names asset and
// holder, the status filter comes from the catalog.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import i18n from '../i18n';
import { AssignmentListPage } from './AssignmentListPage';
import { renderWithProviders } from '../test/utils';
import { ToastViewport } from '../components/ui/Toast';
import { useToastStore } from '../stores/toast';
import { UNDO_WINDOW_MS } from '../hooks/useUndoableDelete';

const ASSET = '11111111-1111-4111-8111-111111111111';
const USER = '22222222-2222-4222-8222-222222222222';
const TEAM = '33333333-3333-4333-8333-333333333333';
const AT = '2026-11-01T10:00:00Z';

const userRow = {
  id: '44444444-4444-4444-8444-444444444444',
  organization_id: 'org-1',
  asset_id: ASSET,
  assigned_to: USER,
  assigned_by: USER,
  assignment_type: 'assignment',
  status: 'active',
  assigned_at: AT,
  created_at: AT,
  updated_at: AT,
};
// A row of a CI only offers neither return nor transfer.
const ciRow = {
  ...userRow,
  id: '55555555-5555-4555-8555-555555555555',
  asset_id: undefined,
  ci_id: '66666666-6666-4666-8666-666666666666',
};

const asset = { id: ASSET, name: 'Laptop 7', inventory_no: 'INV-7', custom_fields: {} };
const user = { id: USER, display_name: 'Anna Berg', email: 'anna@example.com' };
const team = { id: TEAM, name: 'Service Desk', member_count: 3 };

interface Call {
  method: string;
  path: string;
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

/**
 * stubApi answers the reads (list, pickers, names) and the writes with
 * write(path); it records every request.
 */
function stubApi(rows: unknown[], write: (path: string) => Response) {
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://test');
      const method = (init?.method ?? 'GET').toUpperCase();
      const body = typeof init?.body === 'string' ? JSON.parse(init.body) : undefined;
      const path = url.pathname.replace(/^\/api\/v1/, '');
      calls.push({ method, path: url.pathname, body });
      if (method !== 'GET') return write(url.pathname);
      if (path === '/assignments') return json(page(rows));
      if (path === '/assets') return json(page([asset]));
      if (path === '/users') return json(page([user]));
      if (path === '/teams') return json(page([team]));
      if (path === `/assets/${ASSET}`) return json(asset);
      if (path === `/users/${USER}`) return json(user);
      if (path === `/teams/${TEAM}`) return json(team);
      return json(page([]));
    }),
  );
  return calls;
}

const writes = (calls: Call[]) => calls.filter((c) => c.method !== 'GET');
const lists = (calls: Call[]) =>
  calls.filter((c) => c.method === 'GET' && c.path.endsWith('/assignments'));

function renderPage() {
  return renderWithProviders(
    <>
      <AssignmentListPage />
      <ToastViewport />
    </>,
    { route: '/assignments' },
  );
}

/** search types into a picker and takes the option. */
async function choose(scope: HTMLElement, label: string, text: string, option: RegExp) {
  fireEvent.change(within(scope).getByRole('combobox', { name: label }), {
    target: { value: text },
  });
  fireEvent.click(await within(scope).findByRole('option', { name: option }));
}

/** assignLaptop fills the assign dialog: Laptop 7 to Anna Berg, due 2026-12-24. */
async function assignLaptop() {
  fireEvent.click(await screen.findByRole('button', { name: 'Zuweisen' }));
  const dialog = await screen.findByRole('dialog');
  await choose(dialog, 'Asset', 'Lap', /Laptop 7/);
  await choose(dialog, 'Empfänger', 'Ann', /Anna Berg/);
  fireEvent.change(within(dialog).getByLabelText('Fällig am'), {
    target: { value: '2026-12-24' },
  });
  fireEvent.click(within(dialog).getByRole('button', { name: 'Zuweisen' }));
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
});

describe('AssignmentListPage', () => {
  it('assigns with the pickers and sends POST /assets/{id}/assign after the undo window', async () => {
    const calls = stubApi([], () => json(userRow, 201));
    renderPage();
    await assignLaptop();

    expect(screen.getByText('„Laptop 7 (INV-7)“ wird Anna Berg zugewiesen')).toBeInTheDocument();
    expect(writes(calls)).toHaveLength(0);

    act(() => {
      vi.advanceTimersByTime(UNDO_WINDOW_MS);
    });
    await waitFor(() => expect(writes(calls)).toHaveLength(1));
    const [call] = writes(calls);
    expect(call!.method).toBe('POST');
    expect(call!.path).toBe(`/api/v1/assets/${ASSET}/assign`);
    const body = call!.body as Record<string, unknown>;
    expect(body.assigned_user_id).toBe(USER);
    expect(body).not.toHaveProperty('assigned_team_id');
    expect(new Date(body.due_at as string).getTime()).toBe(
      new Date('2026-12-24T23:59:59').getTime(),
    );
  });

  it('sends nothing after Undo', async () => {
    const calls = stubApi([], () => json(userRow, 201));
    renderPage();
    await assignLaptop();

    fireEvent.click(screen.getByRole('button', { name: 'Rückgängig' }));
    act(() => {
      vi.advanceTimersByTime(UNDO_WINDOW_MS * 2);
    });
    expect(writes(calls)).toHaveLength(0);
  });

  it('sends a waiting assignment at once when the page is left', async () => {
    const calls = stubApi([], () => json(userRow, 201));
    const { unmount } = renderPage();
    await assignLaptop();

    unmount();
    await waitFor(() => expect(writes(calls)).toHaveLength(1));
    expect(writes(calls)[0]!.path).toBe(`/api/v1/assets/${ASSET}/assign`);
  });

  it('reports a failed assignment after the window as error toast', async () => {
    stubApi([], () =>
      problem(
        403,
        'https://reticora.io/problems/feature-not-entitled',
        'feature assets_basic is not entitled',
      ),
    );
    renderPage();
    await assignLaptop();

    act(() => {
      vi.advanceTimersByTime(UNDO_WINDOW_MS);
    });
    expect(
      await screen.findByText(
        'Zuweisen von „Laptop 7 (INV-7)“ fehlgeschlagen: Ihr Paket enthält diese Funktion nicht.',
      ),
    ).toBeInTheDocument();
  });

  it('names asset and holder in the table and offers the actions only for asset rows', async () => {
    stubApi([userRow, ciRow], () => json({}));
    renderPage();

    expect(await screen.findByText('Laptop 7 (INV-7)')).toBeInTheDocument();
    expect(await screen.findAllByText('Anna Berg')).toHaveLength(2);
    expect(screen.getByRole('table').textContent).not.toContain(ASSET);
    expect(screen.getByRole('table').textContent).not.toContain(USER);
    expect(screen.getAllByRole('button', { name: /^Zurücknehmen/ })).toHaveLength(1);
    expect(screen.getAllByRole('button', { name: /^Übertragen/ })).toHaveLength(1);
  });

  it('takes the asset back with POST /assets/{id}/return', async () => {
    const calls = stubApi([userRow], () => json({ id: 'movement-1' }));
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Zurücknehmen: Laptop 7 (INV-7)' }));
    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent('„Laptop 7 (INV-7)“ von Anna Berg zurücknehmen.');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Zurücknehmen' }));

    await waitFor(() => expect(writes(calls)).toHaveLength(1));
    const [call] = writes(calls);
    expect(call!.path).toBe(`/api/v1/assets/${ASSET}/return`);
    expect(call!.body).toEqual({ expected_holder: USER });
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
  });

  it('transfers the asset to a team with POST /assets/{id}/transfer', async () => {
    const calls = stubApi([userRow], () => json({ ...userRow, assigned_team_id: TEAM }, 201));
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Übertragen: Laptop 7 (INV-7)' }));
    const dialog = await screen.findByRole('dialog');
    await choose(dialog, 'Neuer Empfänger', 'Serv', /Service Desk/);
    fireEvent.click(within(dialog).getByRole('button', { name: 'Übertragen' }));

    await waitFor(() => expect(writes(calls)).toHaveLength(1));
    const [call] = writes(calls);
    expect(call!.path).toBe(`/api/v1/assets/${ASSET}/transfer`);
    expect(call!.body).toEqual({ expected_holder: USER, assigned_team_id: TEAM });
  });

  it('shows a 409 of the return in the dialog and loads the list again', async () => {
    const calls = stubApi([userRow], () =>
      problem(
        409,
        'https://reticora.io/problems/holder-mismatch',
        'the asset has another holder than expected_holder',
      ),
    );
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Zurücknehmen: Laptop 7 (INV-7)' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Zurücknehmen' }));

    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      'Der Halter des Assets hat sich zwischenzeitlich geändert.',
    );
    await waitFor(() => expect(lists(calls).length).toBeGreaterThanOrEqual(2));
  });

  it('shows a 409 of a reserved asset and a 403 of the plan in the transfer dialog', async () => {
    let answer = problem(409, 'https://reticora.io/problems/conflict', 'the asset is reserved');
    stubApi([userRow], () => answer);
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Übertragen: Laptop 7 (INV-7)' }));
    const dialog = await screen.findByRole('dialog');
    await choose(dialog, 'Neuer Empfänger', 'Serv', /Service Desk/);
    fireEvent.click(within(dialog).getByRole('button', { name: 'Übertragen' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('the asset is reserved');

    answer = problem(403, 'https://reticora.io/problems/feature-not-entitled', 'no assets_basic');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Übertragen' }));
    await waitFor(() =>
      expect(within(dialog).getByRole('alert')).toHaveTextContent(
        'Ihr Paket enthält diese Funktion nicht.',
      ),
    );
  });

  it('writes nothing below /assignments', async () => {
    const calls = stubApi([userRow], () => json(userRow, 201));
    renderPage();
    await assignLaptop();
    act(() => {
      vi.advanceTimersByTime(UNDO_WINDOW_MS);
    });
    await waitFor(() => expect(writes(calls)).toHaveLength(1));
    expect(writes(calls).some((c) => c.path.includes('/assignments'))).toBe(false);
  });

  it('offers the statuses of the catalog as filter', async () => {
    stubApi([], () => json({}));
    renderPage();
    const select = await screen.findByLabelText('Status');
    expect(
      within(select)
        .getAllByRole('option')
        .map((o) => o.textContent),
    ).toEqual(['Alle', 'Aktiv', 'Zurückgegeben', 'Transferiert', 'Überfällig']);
  });
});
