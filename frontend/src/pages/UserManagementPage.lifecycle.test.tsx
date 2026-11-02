/**
 * UserManagementPage user lifecycle (TLC-04, RBA-02; F-196, WP-571): user:manage
 * holders deactivate, reactivate and delete users after a confirmation; a 409
 * of the deletion names the handover of open objects in UI language; the own
 * account offers no action; without user:manage no row offers one. The roles
 * tab marks the seeded standard roles as System (F-201, PR #103).
 *
 * Retest:
 *   cd frontend && npx vitest run src/pages/UserManagementPage.lifecycle.test.tsx
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../test/utils';
import { UserManagementPage } from './UserManagementPage';

const ADMIN_ID = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa';
const BERT_ID = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb';
const CARLA_ID = 'cccccccc-cccc-4ccc-8ccc-cccccccccccc';

interface StubUser {
  id: string;
  organization_id: string;
  email: string;
  display_name: string;
  status: string;
  created_at: string;
  updated_at: string;
}

function user(id: string, name: string, status: string): StubUser {
  return {
    id,
    organization_id: 'org-1',
    email: `${name.toLowerCase()}@example.invalid`,
    display_name: name,
    status,
    created_at: '2026-10-01T10:00:00Z',
    updated_at: '2026-10-01T10:00:00Z',
  };
}

function paginated<T>(data: T[]) {
  return { data, total: data.length, limit: 50, offset: 0, has_more: false };
}

const roles = paginated([
  { id: 'r-1', name: 'org_admin', is_system: true, is_builtin: true, permissions: ['user:manage'] },
  { id: 'r-2', name: 'viewer', is_system: true, is_builtin: true, permissions: ['ci:read'] },
  { id: 'r-3', name: 'auditor', is_system: false, is_builtin: false, permissions: [] },
]);

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function problem(status: number, body: Record<string, unknown>) {
  return new Response(JSON.stringify({ status, ...body }), {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  });
}

type Write = (method: string, id: string, body: unknown) => Response | undefined;

/**
 * stubServer answers the reads of the page from a user table that PATCH and
 * DELETE change like the server does; `write` may answer a write itself
 * (e.g. a 409). It returns the fetch mock.
 */
function stubServer(permissions: string[], write: Write = () => undefined) {
  let table = [
    user(ADMIN_ID, 'Admin', 'active'),
    user(BERT_ID, 'Bert', 'active'),
    user(CARLA_ID, 'Carla', 'inactive'),
  ];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://localhost');
    const method = (init?.method ?? 'GET').toUpperCase();
    if (url.pathname.endsWith('/me/permissions')) {
      return json({ user_id: ADMIN_ID, permissions });
    }
    const match = url.pathname.match(/\/users\/([^/]+)$/);
    if (match && method !== 'GET') {
      const id = match[1]!;
      const body = init?.body ? JSON.parse(String(init.body)) : undefined;
      const answer = write(method, id, body);
      if (answer) return answer;
      const current = table.find((u) => u.id === id);
      if (!current) return problem(404, { title: 'Not Found', detail: 'user not found' });
      if (method === 'DELETE') {
        table = table.filter((u) => u.id !== id);
        return new Response(null, { status: 204 });
      }
      const changed = { ...current, ...(body as Partial<StubUser>) };
      table = table.map((u) => (u.id === id ? changed : u));
      return json(changed);
    }
    if (method !== 'GET') return problem(405, { detail: `unexpected ${method} ${url.pathname}` });
    if (url.pathname.endsWith('/users')) return json(paginated(table));
    if (url.pathname.endsWith('/teams')) return json(paginated([]));
    if (url.pathname.endsWith('/roles')) return json(roles);
    if (url.pathname.endsWith('/invitations')) return json(paginated([]));
    return problem(404, { detail: `unexpected request: ${url.pathname}` });
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

function writes(fetchMock: ReturnType<typeof vi.fn>) {
  return fetchMock.mock.calls
    .map(([input, init]) => ({
      method: ((init as RequestInit | undefined)?.method ?? 'GET').toUpperCase(),
      path: new URL(String(input), 'http://localhost').pathname,
      body: (init as RequestInit | undefined)?.body
        ? JSON.parse(String((init as RequestInit).body))
        : undefined,
    }))
    .filter((call) => call.method !== 'GET');
}

async function row(name: string) {
  return (await screen.findByText(`${name.toLowerCase()}@example.invalid`)).closest('tr')!;
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('UserManagementPage user lifecycle', () => {
  it('deactivates a user after the confirmation with PATCH status and updates the badge', async () => {
    const fetchMock = stubServer(['user:read', 'user:manage']);
    renderWithProviders(<UserManagementPage />, { route: '/users' });

    const bert = await row('Bert');
    expect(within(bert).getByText('Aktiv')).toBeInTheDocument();
    fireEvent.click(await within(bert).findByRole('button', { name: 'Deaktivieren' }));

    const dialog = await screen.findByRole('dialog', { name: 'Benutzer deaktivieren?' });
    expect(dialog).toHaveTextContent('Bert (bert@example.invalid) kann sich danach nicht mehr');
    expect(writes(fetchMock), 'nothing is sent before the confirmation').toEqual([]);
    fireEvent.click(within(dialog).getByRole('button', { name: 'Deaktivieren' }));

    await waitFor(() => expect(within(bert).getByText('Inaktiv')).toBeInTheDocument());
    expect(writes(fetchMock)).toEqual([
      { method: 'PATCH', path: `/api/v1/users/${BERT_ID}`, body: { status: 'inactive' } },
    ]);
    expect(within(bert).getByRole('button', { name: 'Reaktivieren' })).toBeInTheDocument();
    expect(within(bert).queryByRole('button', { name: 'Deaktivieren' })).toBeNull();
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });

  it('reactivates an inactive user with PATCH status active', async () => {
    const fetchMock = stubServer(['user:read', 'user:manage']);
    renderWithProviders(<UserManagementPage />, { route: '/users' });

    const carla = await row('Carla');
    expect(within(carla).getByText('Inaktiv')).toBeInTheDocument();
    fireEvent.click(await within(carla).findByRole('button', { name: 'Reaktivieren' }));
    const dialog = await screen.findByRole('dialog', { name: 'Benutzer reaktivieren?' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Reaktivieren' }));

    await waitFor(() => expect(within(carla).getByText('Aktiv')).toBeInTheDocument());
    expect(writes(fetchMock)).toEqual([
      { method: 'PATCH', path: `/api/v1/users/${CARLA_ID}`, body: { status: 'active' } },
    ]);
  });

  it('sends nothing when the confirmation is cancelled', async () => {
    const fetchMock = stubServer(['user:read', 'user:manage']);
    renderWithProviders(<UserManagementPage />, { route: '/users' });

    const bert = await row('Bert');
    fireEvent.click(await within(bert).findByRole('button', { name: 'Löschen' }));
    const dialog = await screen.findByRole('dialog', { name: 'Benutzer löschen?' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Abbrechen' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(writes(fetchMock)).toEqual([]);
    expect(within(bert).getByText('Aktiv')).toBeInTheDocument();
  });

  it('deletes a user after the confirmation and removes the row', async () => {
    const fetchMock = stubServer(['user:read', 'user:manage']);
    renderWithProviders(<UserManagementPage />, { route: '/users' });

    fireEvent.click(await within(await row('Carla')).findByRole('button', { name: 'Löschen' }));
    const dialog = await screen.findByRole('dialog', { name: 'Benutzer löschen?' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Endgültig löschen' }));

    await waitFor(() => expect(screen.queryByText('carla@example.invalid')).toBeNull());
    expect(writes(fetchMock)).toEqual([
      { method: 'DELETE', path: `/api/v1/users/${CARLA_ID}`, body: undefined },
    ]);
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('shows the handover duty in UI language when the deletion answers 409 (open objects)', async () => {
    const fetchMock = stubServer(['user:read', 'user:manage'], (method) =>
      method === 'DELETE'
        ? problem(409, {
            type: 'urn:reticora:problem:conflict',
            title: 'Conflict',
            detail: 'user owns open objects; hand them over first: 2 open objects',
          })
        : undefined,
    );
    renderWithProviders(<UserManagementPage />, { route: '/users' });

    const bert = await row('Bert');
    fireEvent.click(await within(bert).findByRole('button', { name: 'Löschen' }));
    const dialog = await screen.findByRole('dialog', { name: 'Benutzer löschen?' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Endgültig löschen' }));

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent(
      'Bert kann nicht gelöscht werden: Der Benutzer ist Owner offener Objekte ' +
        '(offene Tickets, aktive Zuweisungen, nicht zurückgegebene Schlüssel). ' +
        'Übergeben Sie diese zuerst an einen anderen Benutzer oder deaktivieren Sie den Benutzer stattdessen.',
    );
    expect(alert).not.toHaveTextContent(/hand them over|open objects/);
    expect(writes(fetchMock)).toHaveLength(1);
    expect(screen.getByText('bert@example.invalid')).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });

  it('names deactivation or anonymization when closed records refer to the user (409)', async () => {
    stubServer(['user:read', 'user:manage'], (method) =>
      method === 'DELETE'
        ? problem(409, {
            title: 'Conflict',
            detail: 'user is referenced by existing records; deactivate or anonymize instead',
          })
        : undefined,
    );
    renderWithProviders(<UserManagementPage />, { route: '/users' });

    fireEvent.click(await within(await row('Bert')).findByRole('button', { name: 'Löschen' }));
    const dialog = await screen.findByRole('dialog', { name: 'Benutzer löschen?' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Endgültig löschen' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Bert kann nicht gelöscht werden: Bestehende Datensätze verweisen auf den Benutzer. ' +
        'Deaktivieren oder anonymisieren Sie den Benutzer stattdessen.',
    );
  });

  it('keeps the badge and reports a refused deactivation (503) in UI language', async () => {
    stubServer(['user:read', 'user:manage'], (method) =>
      method === 'PATCH'
        ? problem(503, { title: 'Service Unavailable', detail: 'sessions could not be revoked' })
        : undefined,
    );
    renderWithProviders(<UserManagementPage />, { route: '/users' });

    const bert = await row('Bert');
    fireEvent.click(await within(bert).findByRole('button', { name: 'Deaktivieren' }));
    const dialog = await screen.findByRole('dialog', { name: 'Benutzer deaktivieren?' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Deaktivieren' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Bert wurde nicht geändert: Die Sitzungen des Benutzers konnten nicht widerrufen werden.',
    );
    expect(within(bert).getByText('Aktiv')).toBeInTheDocument();
  });

  it('offers no action for the own account', async () => {
    stubServer(['user:read', 'user:manage']);
    renderWithProviders(<UserManagementPage />, { route: '/users' });

    // The other rows carry their actions once the rights are known.
    await within(await row('Bert')).findByRole('button', { name: 'Deaktivieren' });
    const admin = await row('Admin');
    expect(within(admin).getByText('Ihr Konto')).toBeInTheDocument();
    expect(within(admin).queryByRole('button')).toBeNull();
  });

  it('offers no deactivate, reactivate or delete action without user:manage', async () => {
    const fetchMock = stubServer(['user:read']);
    const { queryClient } = renderWithProviders(<UserManagementPage />, { route: '/users' });

    await row('Bert');
    // Once the rights arrived, the table still offers nothing.
    await waitFor(() => expect(queryClient.getQueryData(['me-permissions'])).toBeDefined());
    const table = screen.getByRole('table');
    expect(
      within(table).queryByRole('button', { name: /Deaktivieren|Reaktivieren|Löschen/ }),
    ).toBeNull();
    expect(within(table).queryByRole('columnheader', { name: 'Aktionen' })).toBeNull();
    expect(writes(fetchMock)).toEqual([]);
  });

  it('marks the standard roles as System in the roles tab (F-201)', async () => {
    stubServer(['user:read', 'user:manage']);
    renderWithProviders(<UserManagementPage />, { route: '/users' });
    fireEvent.click(await screen.findByRole('button', { name: 'Rollen' }));

    const admin = (await screen.findByText('org_admin')).closest('tr')!;
    expect(within(admin).getByText('System')).toBeInTheDocument();
    const viewer = screen.getByText('viewer').closest('tr')!;
    expect(within(viewer).getByText('System')).toBeInTheDocument();
    const custom = screen.getByText('auditor').closest('tr')!;
    expect(within(custom).getByText('Custom')).toBeInTheDocument();
  });
});
