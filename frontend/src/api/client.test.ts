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
