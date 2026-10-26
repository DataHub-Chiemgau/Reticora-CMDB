import { afterEach, describe, expect, it, vi } from 'vitest';

const futureExpiry = '2999-01-01T00:00:00Z';

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

async function importClientWithSession(token = 'old-token') {
  vi.resetModules();
  const { useAuthStore } = await import('../auth/authStore');
  useAuthStore.getState().setSession({
    token,
    expiresAt: futureExpiry,
    user: { sub: 'user-1', email: 'user@example.com' },
  });
  const { fetchAPI } = await import('./client');
  return { fetchAPI, useAuthStore };
}

afterEach(() => {
  vi.useRealTimers();
  window.sessionStorage.clear();
  vi.restoreAllMocks();
  vi.resetModules();
});

describe('fetchAPI authentication', () => {
  it('attaches the bearer token when a session exists', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ ok: true }));
    vi.stubGlobal('fetch', fetchMock);
    const { fetchAPI } = await importClientWithSession('attached-token');

    await expect(fetchAPI('/cis')).resolves.toEqual({ ok: true });

    const headers = fetchMock.mock.calls[0]?.[1]?.headers as Headers;
    expect(headers.get('Authorization')).toBe(['Bearer', 'attached-token'].join(' '));
  });

  it('refreshes once on 401 and retries the original request', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse({ detail: 'expired' }, 401))
      .mockResolvedValueOnce(jsonResponse({ token: 'new-token', expires_at: futureExpiry }))
      .mockResolvedValueOnce(jsonResponse({ data: ['ok'] }));
    vi.stubGlobal('fetch', fetchMock);
    const { fetchAPI, useAuthStore } = await importClientWithSession('old-token');

    await expect(fetchAPI('/cis')).resolves.toEqual({ data: ['ok'] });

    expect(fetchMock).toHaveBeenCalledTimes(3);
    expect(fetchMock.mock.calls[1]?.[0]).toBe('/api/v1/auth/refresh');
    // The refresh token travels only in the HttpOnly cookie (AUT-02): no
    // token in the body, cookies sent for the same origin.
    expect(fetchMock.mock.calls[1]?.[1]?.body).toBeUndefined();
    expect(fetchMock.mock.calls[1]?.[1]?.credentials).toBe('same-origin');
    const retryHeaders = fetchMock.mock.calls[2]?.[1]?.headers as Headers;
    expect(retryHeaders.get('Authorization')).toBe(['Bearer', 'new-token'].join(' '));
    expect(useAuthStore.getState().token).toBe('new-token');
  });

  it('reports the 401 but keeps the session when the refresh is rate limited twice (429)', async () => {
    vi.useFakeTimers();
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse({ detail: 'expired' }, 401))
      .mockResolvedValueOnce(jsonResponse({ detail: 'too many' }, 429))
      .mockResolvedValueOnce(jsonResponse({ detail: 'too many' }, 429));
    vi.stubGlobal('fetch', fetchMock);
    const { fetchAPI, useAuthStore } = await importClientWithSession('old-token');

    const result = fetchAPI('/cis').catch((error: unknown) => error);
    await vi.advanceTimersByTimeAsync(60_000);
    const error = await result;

    expect(error).toMatchObject({ name: 'ApiError', status: 401 });
    expect(fetchMock).toHaveBeenCalledTimes(3);
    expect(fetchMock.mock.calls[2]?.[0]).toBe('/api/v1/auth/refresh');
    expect(useAuthStore.getState().token).toBe('old-token');
    expect(useAuthStore.getState().user?.sub).toBe('user-1');
  });

  it('ends the session when the server refuses the refresh cookie', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse({ detail: 'expired' }, 401))
      .mockResolvedValueOnce(jsonResponse({ detail: 'refresh token already used' }, 401));
    vi.stubGlobal('fetch', fetchMock);
    const { fetchAPI, useAuthStore } = await importClientWithSession('old-token');

    await expect(fetchAPI('/cis')).rejects.toMatchObject({ status: 401 });

    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(useAuthStore.getState().token).toBeNull();
    expect(useAuthStore.getState().user).toBeNull();
  });
});

describe('logout', () => {
  it('ends the server session with the access token and the cookie, then clears the local session', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal('fetch', fetchMock);
    const { useAuthStore } = await importClientWithSession('live-token');
    const { logout } = await import('../auth/session');

    await logout();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/v1/auth/logout');
    const init = fetchMock.mock.calls[0]?.[1] as RequestInit;
    expect(init.method).toBe('POST');
    expect(init.credentials).toBe('same-origin');
    expect((init.headers as Record<string, string>).Authorization).toBe(
      ['Bearer', 'live-token'].join(' '),
    );
    expect(useAuthStore.getState().token).toBeNull();
  });

  it('clears the local session when the server is unreachable', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('offline')));
    const { useAuthStore } = await importClientWithSession('live-token');
    const { logout } = await import('../auth/session');

    await logout();

    expect(useAuthStore.getState().token).toBeNull();
  });
});

describe('ciApi.update optimistic concurrency', () => {
  it('sends the CI version as If-Match and surfaces a 409 conflict', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse({ id: 'ci-1', version: 4 }))
      .mockResolvedValueOnce(
        jsonResponse(
          {
            title: 'Conflict',
            detail: 'fields changed since the If-Match version: name',
            fields: ['name'],
          },
          409,
        ),
      )
      .mockResolvedValueOnce(jsonResponse({ id: 'ci-1', version: 5 }));
    vi.stubGlobal('fetch', fetchMock);
    await importClientWithSession();
    const { ciApi, ApiError } = await import('./client');

    await expect(ciApi.update('ci-1', { name: 'a' }, 3)).resolves.toEqual({
      id: 'ci-1',
      version: 4,
    });
    expect((fetchMock.mock.calls[0]?.[1]?.headers as Headers).get('If-Match')).toBe('"3"');

    const conflict = await ciApi.update('ci-1', { name: 'b' }, 3).catch((err: unknown) => err);
    expect(conflict).toBeInstanceOf(ApiError);
    expect((conflict as InstanceType<typeof ApiError>).status).toBe(409);

    // Without a version no If-Match is sent.
    await ciApi.update('ci-1', { name: 'c' });
    expect((fetchMock.mock.calls[2]?.[1]?.headers as Headers).has('If-Match')).toBe(false);
  });
});

describe('bffQuery', () => {
  it('posts the query to /bff/graphql with the session and returns data', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(jsonResponse({ data: { currentUser: { id: 'user-1' } } }));
    vi.stubGlobal('fetch', fetchMock);
    await importClientWithSession('bff-token');
    const { bffQuery } = await import('./client');

    await expect(bffQuery('{ currentUser { id } }')).resolves.toEqual({
      currentUser: { id: 'user-1' },
    });

    expect(fetchMock.mock.calls[0]?.[0]).toBe('/bff/graphql');
    expect(fetchMock.mock.calls[0]?.[1]?.method).toBe('POST');
    const headers = fetchMock.mock.calls[0]?.[1]?.headers as Headers;
    expect(headers.get('Authorization')).toBe(['Bearer', 'bff-token'].join(' '));
  });

  it('throws the GraphQL errors', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        jsonResponse({
          data: null,
          errors: [{ message: 'missing required permission: ci:read' }],
        }),
      ),
    );
    await importClientWithSession();
    const { bffQuery } = await import('./client');

    await expect(bffQuery('{ cis { totalCount } }')).rejects.toThrow('ci:read');
  });
});

describe('impact clients', () => {
  it('ask for the fields they read, never for the full answer with paths (NFR-02)', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(jsonResponse({ failed_ci_id: 'ci-1', impacted: [], count: 0 }));
    vi.stubGlobal('fetch', fetchMock);
    await importClientWithSession();
    const { impactApi } = await import('./cmdb');

    await impactApi.impact('ci-1', { depth: 3 });

    const url = new URL(String(fetchMock.mock.calls[0]?.[0]), 'http://localhost');
    expect(url.pathname).toBe('/api/v1/impact/ci-1');
    expect(url.searchParams.get('depth')).toBe('3');
    expect(url.searchParams.get('fields')).toBe('name,ci_type,status');
  });
});

describe('complete lists over several pages (API-02)', () => {
  function permissions(from: number, count: number) {
    return Array.from({ length: count }, (_, i) => ({
      key: `res${from + i}:read`,
      resource: `res${from + i}`,
      action: 'read',
      description: '',
    }));
  }

  it('follows next_cursor with limit=200 until the end: two pages with 103 entries', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        jsonResponse({
          data: permissions(0, 100),
          total: 103,
          limit: 200,
          offset: 0,
          has_more: true,
          next_cursor: 'cursor-2',
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          data: permissions(100, 3),
          total: 103,
          limit: 200,
          offset: 100,
          has_more: false,
        }),
      );
    vi.stubGlobal('fetch', fetchMock);
    await importClientWithSession();
    const { permissionApi } = await import('./client');

    const catalogue = await permissionApi.list();

    expect(catalogue).toHaveLength(103);
    expect(new Set(catalogue.map((p) => p.key)).size).toBe(103);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    const first = new URL(String(fetchMock.mock.calls[0]?.[0]), 'http://localhost');
    expect(first.pathname).toBe('/api/v1/permissions');
    expect(first.searchParams.get('limit')).toBe('200');
    expect(first.searchParams.has('cursor')).toBe(false);
    const second = new URL(String(fetchMock.mock.calls[1]?.[0]), 'http://localhost');
    expect(second.searchParams.get('limit')).toBe('200');
    expect(second.searchParams.get('cursor')).toBe('cursor-2');
  });

  it('reads data of the role grants and team members', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://localhost');
      if (url.pathname === '/api/v1/roles/role-1/permissions') {
        return Promise.resolve(
          jsonResponse({
            data: [{ role_id: 'role-1', permission_key: 'ci:read' }],
            total: 1,
            limit: 200,
            offset: 0,
            has_more: false,
          }),
        );
      }
      return Promise.resolve(
        jsonResponse({ data: [], total: 0, limit: 200, offset: 0, has_more: false }),
      );
    });
    vi.stubGlobal('fetch', fetchMock);
    await importClientWithSession();
    const { permissionApi, teamApi } = await import('./client');

    await expect(permissionApi.listRole('role-1')).resolves.toEqual([
      { role_id: 'role-1', permission_key: 'ci:read' },
    ]);
    await expect(teamApi.listMembers('team-1')).resolves.toEqual([]);
    const members = new URL(String(fetchMock.mock.calls[1]?.[0]), 'http://localhost');
    expect(members.pathname).toBe('/api/v1/teams/team-1/members');
    expect(members.searchParams.get('limit')).toBe('200');
  });
});
