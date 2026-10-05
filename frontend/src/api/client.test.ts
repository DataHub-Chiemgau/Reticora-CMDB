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
    expect(JSON.parse(fetchMock.mock.calls[1]?.[1]?.body as string)).toEqual({
      token: 'old-token',
    });
    const retryHeaders = fetchMock.mock.calls[2]?.[1]?.headers as Headers;
    expect(retryHeaders.get('Authorization')).toBe(['Bearer', 'new-token'].join(' '));
    expect(useAuthStore.getState().token).toBe('new-token');
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
