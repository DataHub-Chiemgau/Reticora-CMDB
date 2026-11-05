// AST-02/API-06 (WP-746): the assignment page writes through the asset.
// Assigning calls POST /assets/{id}/assign with a user or a team, the
// return POST /assets/{id}/return with the holder of the row as
// expected_holder, and a 409 shows a message and loads the list again.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import i18n from '../i18n';
import { AssignmentListPage } from './AssignmentListPage';
import { renderWithProviders } from '../test/utils';

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
// A row of a CI only (before WP-275) offers no return.
const ciRow = {
  ...userRow,
  id: '55555555-5555-4555-8555-555555555555',
  asset_id: undefined,
  ci_id: '66666666-6666-4666-8666-666666666666',
};

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

/**
 * stubApi answers the list with rows and the writes with write(path); it
 * records every request.
 */
function stubApi(rows: unknown[], write: (path: string) => Response) {
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://test');
      const method = (init?.method ?? 'GET').toUpperCase();
      const body = typeof init?.body === 'string' ? JSON.parse(init.body) : undefined;
      calls.push({ method, path: url.pathname, body });
      if (method === 'GET' && url.pathname.endsWith('/assignments')) {
        return json({ data: rows, total: rows.length, limit: 50, offset: 0, has_more: false });
      }
      return write(url.pathname);
    }),
  );
  return calls;
}

const writes = (calls: Call[]) => calls.filter((c) => c.method === 'POST');
const lists = (calls: Call[]) =>
  calls.filter((c) => c.method === 'GET' && c.path.endsWith('/assignments'));

beforeEach(async () => {
  await i18n.changeLanguage('de-DE');
});

afterEach(() => {
  vi.unstubAllGlobals();
});

async function openAssign() {
  fireEvent.click(await screen.findByRole('button', { name: 'Zuweisen' }));
  const dialog = await screen.findByRole('dialog');
  fireEvent.change(within(dialog).getByLabelText(/Asset-ID/), { target: { value: ASSET } });
  return dialog;
}

describe('AssignmentListPage', () => {
  it('assigns the asset to a user with POST /assets/{id}/assign', async () => {
    const calls = stubApi([], () => json(userRow, 201));
    renderWithProviders(<AssignmentListPage />, { route: '/assignments' });
    const dialog = await openAssign();
    fireEvent.change(within(dialog).getByLabelText(/Nutzer-ID/), { target: { value: USER } });
    fireEvent.change(within(dialog).getByLabelText('Fällig am'), {
      target: { value: '2026-12-24' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(writes(calls)).toHaveLength(1));
    const [call] = writes(calls);
    expect(call!.path).toBe(`/api/v1/assets/${ASSET}/assign`);
    const body = call!.body as Record<string, unknown>;
    expect(body.assigned_user_id).toBe(USER);
    expect(body).not.toHaveProperty('assigned_team_id');
    expect(new Date(body.due_at as string).getTime()).toBe(
      new Date('2026-12-24T23:59:59').getTime(),
    );
  });

  it('assigns the asset to a team', async () => {
    const calls = stubApi([], () =>
      json({ ...userRow, assigned_to: undefined, assigned_team_id: TEAM }, 201),
    );
    renderWithProviders(<AssignmentListPage />, { route: '/assignments' });
    const dialog = await openAssign();
    fireEvent.change(within(dialog).getByLabelText('Halter'), { target: { value: 'team' } });
    fireEvent.change(within(dialog).getByLabelText(/Team-ID/), { target: { value: TEAM } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));

    await waitFor(() => expect(writes(calls)).toHaveLength(1));
    const [call] = writes(calls);
    expect(call!.path).toBe(`/api/v1/assets/${ASSET}/assign`);
    expect(call!.body).toEqual({ assigned_team_id: TEAM });
  });

  it('returns the asset of a row with its holder as expected_holder', async () => {
    const calls = stubApi([userRow, ciRow], () => json({ id: 'movement-1' }));
    renderWithProviders(<AssignmentListPage />, { route: '/assignments' });
    const buttons = await screen.findAllByRole('button', { name: 'Rückgabe' });
    // The row with only a CI has no return.
    expect(buttons).toHaveLength(1);
    fireEvent.click(buttons[0]!);

    await waitFor(() => expect(writes(calls)).toHaveLength(1));
    const [call] = writes(calls);
    expect(call!.path).toBe(`/api/v1/assets/${ASSET}/return`);
    expect(call!.body).toEqual({ expected_holder: USER });
  });

  it('shows a message on 409 and loads the list again', async () => {
    const calls = stubApi([userRow], () =>
      json(
        {
          type: 'https://reticora.io/problems/holder-mismatch',
          title: 'Conflict',
          status: 409,
          detail: 'the asset has another holder than expected_holder',
        },
        409,
      ),
    );
    renderWithProviders(<AssignmentListPage />, { route: '/assignments' });
    fireEvent.click(await screen.findByRole('button', { name: 'Rückgabe' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Der Halter des Assets hat sich zwischenzeitlich geändert.',
    );
    await waitFor(() => expect(lists(calls).length).toBeGreaterThanOrEqual(2));
  });
});
