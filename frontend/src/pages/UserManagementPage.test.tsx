/**
 * UserManagementPage invitations (TLC-03, API-05; F-142, WP-510): user:manage
 * holders list, issue, renew and revoke invitations; refusals of the API
 * (403, 409, 422) appear at the field in UI language; without user:manage
 * the page offers no invitation action.
 *
 * Retest:
 *   cd frontend && npx vitest run src/pages/UserManagementPage.test.tsx
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';
import { UserManagementPage } from './UserManagementPage';

const emptyList = { data: [], total: 0, limit: 50, offset: 0, has_more: false };

function paginated<T>(data: T[]) {
  return { data, total: data.length, limit: 50, offset: 0, has_more: false };
}

const managePermissions = { permissions: ['user:read', 'user:manage'] };

const users = paginated([
  {
    id: 'u-1',
    email: 'admin@example.invalid',
    display_name: 'Admin',
    status: 'active',
    is_active: true,
  },
]);

const ROLE_VIEWER = '11111111-1111-4111-8111-111111111111';
const ROLE_TECH = '22222222-2222-4222-8222-222222222222';
const ROLE_CUSTOM = '33333333-3333-4333-8333-333333333333';
const CLIENT_ID = '44444444-4444-4444-8444-444444444444';

const roles = paginated([
  // The seeded standard roles are system roles (F-201), as GET /roles answers.
  { id: ROLE_VIEWER, name: 'viewer', is_system: true, is_builtin: true, permissions: [] },
  {
    id: ROLE_TECH,
    name: 'client_technician',
    is_system: true,
    is_builtin: true,
    permissions: [],
  },
  { id: ROLE_CUSTOM, name: 'custom-auditor', is_system: false, is_builtin: false, permissions: [] },
]);

const clients = paginated([{ id: CLIENT_ID, name: 'Kunde Nord' }]);

const pending = {
  id: 'inv-1',
  organization_id: 'org-1',
  email: 'new@example.invalid',
  role_id: ROLE_TECH,
  scope_type: 'client',
  scope_id: CLIENT_ID,
  invited_by: 'u-1',
  status: 'pending',
  expires_at: '2026-11-02T10:00:00Z',
  created_at: '2026-10-26T10:00:00Z',
};
const accepted = {
  ...pending,
  id: 'inv-2',
  email: 'done@example.invalid',
  role_id: ROLE_VIEWER,
  scope_type: 'org',
  scope_id: undefined,
  status: 'accepted',
  accepted_at: '2026-10-27T10:00:00Z',
};

function problem(status: number, body: Record<string, unknown>) {
  return new Response(JSON.stringify({ status, ...body }), {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  });
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

/**
 * stubUserApi answers the reads of the page and hands every write to
 * `write`, so a test decides what the server answers.
 */
function stubUserApi(
  permissions: string[],
  write: (url: string, method: string, body: unknown) => Response = () => json({}, 201),
) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input.toString();
    const method = (init?.method ?? 'GET').toUpperCase();
    if (method !== 'GET') {
      return write(url, method, init?.body ? JSON.parse(String(init.body)) : undefined);
    }
    if (url.includes('/me/permissions')) return json({ permissions });
    if (url.includes('/invitations')) return json(paginated([pending, accepted]));
    if (url.includes('/users')) return json(users);
    if (url.includes('/teams')) return json(emptyList);
    if (url.includes('/roles')) return json(roles);
    if (url.includes('/clients')) return json(clients);
    if (url.includes('/sites')) return json(emptyList);
    return problem(404, { detail: `unexpected request: ${url}` });
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

function requests(fetchMock: ReturnType<typeof vi.fn>, method: string, path: string) {
  return fetchMock.mock.calls.filter(([input, init]) => {
    const url = typeof input === 'string' ? input : String(input);
    return (
      ((init as RequestInit | undefined)?.method ?? 'GET').toUpperCase() === method &&
      new URL(url, 'http://localhost').pathname.endsWith(path)
    );
  });
}

async function openInvitations() {
  fireEvent.click(await screen.findByRole('button', { name: 'Einladungen' }));
  return screen.findByRole('heading', { name: /^Einladungen \(/ });
}

async function fillInvite(email: string, roleId: string) {
  fireEvent.change(await screen.findByLabelText('E-Mail'), { target: { value: email } });
  const role = screen.getByLabelText('Rolle');
  await within(role).findByRole('option', { name: 'viewer' });
  fireEvent.change(role, { target: { value: roleId } });
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('UserManagementPage roles', () => {
  it('marks the standard roles as System and custom roles as Custom (F-201)', async () => {
    stubUserApi(managePermissions.permissions);
    renderWithProviders(<UserManagementPage />, { route: '/users' });
    fireEvent.click(await screen.findByRole('button', { name: /^(Rollen|Roles)$/ }));

    const viewer = (await screen.findByText('viewer')).closest('tr')!;
    expect(within(viewer).getByText('System')).toBeInTheDocument();
    const technician = screen.getByText('client_technician').closest('tr')!;
    expect(within(technician).getByText('System')).toBeInTheDocument();
    const custom = screen.getByText('custom-auditor').closest('tr')!;
    expect(within(custom).getByText('Custom')).toBeInTheDocument();
  });
});

describe('UserManagementPage invitations', () => {
  // Moved from UserManagementPage.auditdefect.tsx (F-142); the server's
  // answer to /me/permissions now grants user:manage, without which the
  // page offers no invitation action (see the next case).
  it('offers an action to invite a user', async () => {
    stubFetchRoutes({
      '/me/permissions': managePermissions,
      '/invitations': paginated([]),
      '/users': {
        data: [
          {
            id: 'u-1',
            email: 'admin@example.invalid',
            display_name: 'Admin',
            status: 'active',
            is_active: true,
          },
        ],
        total: 1,
        limit: 50,
        offset: 0,
        has_more: false,
      },
      '/teams': emptyList,
      '/roles': emptyList,
    });

    renderWithProviders(<UserManagementPage />, { route: '/users' });

    expect(await screen.findByText('admin@example.invalid')).toBeInTheDocument();
    await waitFor(() => {
      const invite = [...screen.queryAllByRole('button'), ...screen.queryAllByRole('link')].filter(
        (el) =>
          /einlad|invit/i.test(el.textContent ?? '') ||
          /einlad|invit/i.test(el.getAttribute('aria-label') ?? ''),
      );
      expect(invite.length).toBeGreaterThan(0);
    });
  });

  it('offers no invitation tab or action without user:manage and never lists invitations', async () => {
    const fetchMock = stubUserApi(['user:read']);
    renderWithProviders(<UserManagementPage />, { route: '/users' });

    expect(await screen.findByText('admin@example.invalid')).toBeInTheDocument();
    await waitFor(() => expect(requests(fetchMock, 'GET', '/me/permissions')).toHaveLength(1));
    expect(screen.queryByRole('button', { name: /einlad|invit/i })).toBeNull();
    expect(requests(fetchMock, 'GET', '/invitations')).toHaveLength(0);
  });

  it('lists email, role, scope, expiry and status; an accepted invitation has no action', async () => {
    stubUserApi(managePermissions.permissions);
    renderWithProviders(<UserManagementPage />, { route: '/users' });
    await openInvitations();

    const open = (await screen.findByText('new@example.invalid')).closest('tr')!;
    expect(within(open).getByText('client_technician')).toBeInTheDocument();
    expect(await within(open).findByText('Kunde: Kunde Nord')).toBeInTheDocument();
    expect(within(open).getByText('Offen')).toBeInTheDocument();
    expect(
      within(open).getByText(new Date(pending.expires_at).toLocaleString('de-DE')),
    ).toBeInTheDocument();
    expect(within(open).getByRole('button', { name: 'Erneuern' })).toBeInTheDocument();
    expect(within(open).getByRole('button', { name: 'Widerrufen' })).toBeInTheDocument();

    const done = screen.getByText('done@example.invalid').closest('tr')!;
    expect(within(done).getByText('viewer')).toBeInTheDocument();
    expect(within(done).getByText('Organisation')).toBeInTheDocument();
    expect(within(done).getByText('Angenommen')).toBeInTheDocument();
    expect(within(done).queryByRole('button')).toBeNull();
  });

  it('offers only the standard roles and issues a client-scoped invitation', async () => {
    const fetchMock = stubUserApi(managePermissions.permissions, (url, method, body) => {
      expect(method).toBe('POST');
      expect(url).toMatch(/\/invitations$/);
      return json(
        {
          ...pending,
          id: 'inv-3',
          email: (body as { email: string }).email,
          token: 'org.secret-token',
        },
        201,
      );
    });
    renderWithProviders(<UserManagementPage />, { route: '/users' });
    await openInvitations();

    await fillInvite('colleague@example.invalid', ROLE_TECH);
    expect(
      within(screen.getByLabelText('Rolle')).queryByRole('option', { name: 'custom-auditor' }),
    ).toBeNull();
    fireEvent.change(screen.getByLabelText('Scope-Typ'), { target: { value: 'client' } });
    const target = screen.getByLabelText('Scope-Ziel');
    await within(target).findByRole('option', { name: 'Kunde Nord' });
    fireEvent.change(target, { target: { value: CLIENT_ID } });
    fireEvent.click(screen.getByRole('button', { name: 'Einladung senden' }));

    expect(await screen.findByRole('status')).toHaveTextContent(
      'Einladung an colleague@example.invalid gesendet',
    );
    const posts = requests(fetchMock, 'POST', '/invitations');
    expect(posts).toHaveLength(1);
    expect(JSON.parse(String((posts[0]![1] as RequestInit).body))).toEqual({
      email: 'colleague@example.invalid',
      role_id: ROLE_TECH,
      scope_type: 'client',
      scope_id: CLIENT_ID,
    });
    expect(screen.getByLabelText('Einladungslink (wird nur jetzt angezeigt)')).toHaveValue(
      `${window.location.origin}/invitations/accept?token=org.secret-token`,
    );
  });

  it('shows a 409 for a pending address at the e-mail field and keeps the input', async () => {
    stubUserApi(managePermissions.permissions, () =>
      problem(409, {
        type: 'urn:reticora:problem:conflict',
        title: 'Conflict',
        detail: 'email: a pending invitation for this address exists',
        field: 'email',
      }),
    );
    renderWithProviders(<UserManagementPage />, { route: '/users' });
    await openInvitations();
    await fillInvite('new@example.invalid', ROLE_VIEWER);
    fireEvent.click(screen.getByRole('button', { name: 'Einladung senden' }));

    expect(
      await screen.findByText(
        'Für diese Adresse gibt es bereits eine offene Einladung. Erneuern Sie diese in der Liste.',
      ),
    ).toBeInTheDocument();
    expect(screen.getByLabelText('E-Mail')).toHaveAttribute('aria-invalid', 'true');
    expect(screen.getByLabelText('E-Mail')).toHaveValue('new@example.invalid');
    expect(screen.getByRole('alert')).toHaveTextContent('Die Einladung wurde nicht gesendet');
  });

  it('shows a 422 of a role that does not fit the scope type at the scope type', async () => {
    stubUserApi(managePermissions.permissions, () =>
      problem(422, {
        type: 'urn:reticora:problem:invalid-field',
        title: 'Unprocessable Entity',
        detail: 'scope_type: the role is a client role and is granted with scope_type client only',
        field: 'scope_type',
      }),
    );
    renderWithProviders(<UserManagementPage />, { route: '/users' });
    await openInvitations();
    await fillInvite('tech@example.invalid', ROLE_TECH);
    fireEvent.click(screen.getByRole('button', { name: 'Einladung senden' }));

    expect(await screen.findByText(/Die Rolle passt nicht zum Scope-Typ/)).toBeInTheDocument();
    expect(screen.getByLabelText('Scope-Typ')).toHaveAttribute('aria-invalid', 'true');
    expect(screen.getByLabelText('E-Mail')).not.toHaveAttribute('aria-invalid');
  });

  it('shows the 403 grant ceiling at the role', async () => {
    stubUserApi(managePermissions.permissions, () =>
      problem(403, { title: 'Forbidden', detail: 'grant ceiling: missing ci:write' }),
    );
    renderWithProviders(<UserManagementPage />, { route: '/users' });
    await openInvitations();
    await fillInvite('boss@example.invalid', ROLE_VIEWER);
    fireEvent.click(screen.getByRole('button', { name: 'Einladung senden' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Keine Berechtigung für diese Einladung.',
    );
    expect(screen.getByLabelText('Rolle')).toHaveAttribute('aria-invalid', 'true');
    expect(
      screen.getByText(
        'Sie können nur Rollen vergeben, deren Rechte Sie im gewählten Scope selbst besitzen.',
      ),
    ).toBeInTheDocument();
  });

  it('renews an invitation and shows the new link', async () => {
    const fetchMock = stubUserApi(managePermissions.permissions, (url, method) => {
      expect(method).toBe('POST');
      expect(url).toMatch(/\/invitations\/inv-1\/renew$/);
      return json({ ...pending, token: 'org.renewed' });
    });
    renderWithProviders(<UserManagementPage />, { route: '/users' });
    await openInvitations();
    const row = (await screen.findByText('new@example.invalid')).closest('tr')!;
    fireEvent.click(within(row).getByRole('button', { name: 'Erneuern' }));

    expect(await screen.findByRole('status')).toHaveTextContent(
      'Einladung an new@example.invalid gesendet',
    );
    expect(requests(fetchMock, 'POST', '/invitations/inv-1/renew')).toHaveLength(1);
    expect(screen.getByLabelText('Einladungslink (wird nur jetzt angezeigt)')).toHaveValue(
      `${window.location.origin}/invitations/accept?token=org.renewed`,
    );
  });

  it('revokes an invitation and reports a 409 for an accepted one in UI language', async () => {
    const fetchMock = stubUserApi(managePermissions.permissions, (url, method) => {
      expect(method).toBe('DELETE');
      expect(url).toMatch(/\/invitations\/inv-1$/);
      return problem(409, { title: 'Conflict', detail: 'invitation already accepted' });
    });
    renderWithProviders(<UserManagementPage />, { route: '/users' });
    await openInvitations();
    const row = (await screen.findByText('new@example.invalid')).closest('tr')!;
    fireEvent.click(within(row).getByRole('button', { name: 'Widerrufen' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'new@example.invalid: Die Einladung wurde bereits angenommen und kann nicht mehr geändert werden.',
    );
    expect(requests(fetchMock, 'DELETE', '/invitations/inv-1')).toHaveLength(1);
  });
});
