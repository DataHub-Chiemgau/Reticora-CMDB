// Token renewal of the SPA (AUT-02, AUT-10): the rotating HttpOnly refresh
// cookie renews the 15-minute access token. The server treats a second use of
// a consumed cookie as reuse and revokes the family (identity
// TestRefreshRotatesAndDetectsReuse), so concurrent callers share one refresh
// (F-065). A 429 of the pre-auth limit is waited out once and does not end the
// session; only 401 and 403 do (F-063). The store plans a renewal about a
// minute before the token expires.
//
// Retest: cd frontend && npx vitest run src/auth/session.test.ts
import { afterEach, describe, expect, it, vi } from 'vitest';

const future = '2999-01-01T00:00:00Z';

function json(body: unknown, status = 200, headers: Record<string, string> = {}) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  });
}

function urlOf(input: RequestInfo | URL) {
  if (typeof input === 'string') return input;
  if (input instanceof URL) return input.pathname;
  return input.url;
}

function isRefresh(input: RequestInfo | URL) {
  return urlOf(input).endsWith('/api/v1/auth/refresh');
}

// signedIn loads fresh modules with a session of user u.
async function signedIn(expiresAt = future) {
  vi.resetModules();
  const { useAuthStore } = await import('./authStore');
  const session = await import('./session');
  useAuthStore.getState().setSession({ token: 'old', expiresAt, user: { sub: 'u' } });
  return { useAuthStore, ...session };
}

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
});

describe('refresh at token expiry (AUT-02)', () => {
  it('three concurrent 401s renew the session once and retry with the new token', async () => {
    vi.resetModules();
    const { useAuthStore } = await import('./authStore');
    useAuthStore.getState().setSession({ token: 'old', expiresAt: future, user: { sub: 'u' } });
    const { fetchAPI } = await import('../api/client');

    let refreshes = 0;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (urlOf(input).endsWith('/api/v1/auth/refresh')) {
        refreshes += 1;
        // The server rotates the cookie on the first use; a second use of
        // the same cookie is reuse and revokes the family.
        return refreshes === 1
          ? json({ token: 'new', expires_at: future })
          : json({ detail: 'refresh token already used' }, 401);
      }
      const auth = new Headers(init?.headers).get('Authorization');
      return auth === 'Bearer new' ? json({ data: [] }) : json({ detail: 'token expired' }, 401);
    });
    vi.stubGlobal('fetch', fetchMock);

    const results = await Promise.allSettled([
      fetchAPI('/cis'),
      fetchAPI('/locations'),
      fetchAPI('/notifications'),
    ]);

    expect(refreshes, 'POST /auth/refresh calls for one expiry').toBe(1);
    expect(results.map((r) => r.status)).toEqual(['fulfilled', 'fulfilled', 'fulfilled']);
    expect(useAuthStore.getState().token).toBe('new');
  });

  it('concurrent callers share one in-flight refresh', async () => {
    const { refreshSession, useAuthStore } = await signedIn();
    let release: (response: Response) => void = () => undefined;
    const fetchMock = vi.fn(
      () =>
        new Promise<Response>((resolve) => {
          release = resolve;
        }),
    );
    vi.stubGlobal('fetch', fetchMock);

    const calls = [refreshSession(), refreshSession(), refreshSession()];
    await Promise.resolve();
    release(json({ token: 'new', expires_at: future }));

    expect(await Promise.all(calls)).toEqual([true, true, true]);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(useAuthStore.getState().token).toBe('new');

    // The next expiry gets its own refresh.
    fetchMock.mockImplementationOnce(async () => json({ token: 'newer', expires_at: future }));
    expect(await refreshSession()).toBe(true);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(useAuthStore.getState().token).toBe('newer');
  });

  it('a 401 that arrives after another request renewed the token retries without a second refresh', async () => {
    const { useAuthStore } = await signedIn();
    const { fetchAPI } = await import('../api/client');
    let refreshes = 0;
    let answerLate: () => void = () => undefined;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        if (isRefresh(input)) {
          refreshes += 1;
          return json({ token: 'new', expires_at: future });
        }
        const auth = new Headers(init?.headers).get('Authorization');
        if (auth === 'Bearer new') return json({ data: [] });
        if (urlOf(input).endsWith('/late')) {
          // Sent with the old token, answered after the refresh finished.
          return new Promise<Response>((resolve) => {
            answerLate = () => resolve(json({ detail: 'token expired' }, 401));
          });
        }
        return json({ detail: 'token expired' }, 401);
      }),
    );

    const late = fetchAPI('/late');
    await expect(fetchAPI('/cis')).resolves.toEqual({ data: [] });
    answerLate();

    await expect(late).resolves.toEqual({ data: [] });
    expect(refreshes).toBe(1);
    expect(useAuthStore.getState().token).toBe('new');
  });

  it('ends the session when the server refuses the refresh cookie (401, 403)', async () => {
    for (const status of [401, 403]) {
      const { refreshSession, useAuthStore } = await signedIn();
      vi.stubGlobal(
        'fetch',
        vi.fn(async () => json({ detail: 'refused' }, status)),
      );

      expect(await refreshSession(), `refresh → ${status}`).toBe(false);
      expect(useAuthStore.getState().user, `refresh → ${status}`).toBeNull();
      expect(useAuthStore.getState().token).toBeNull();
    }
  });

  it('keeps the session when the server cannot answer (network error, 5xx)', async () => {
    for (const answer of [
      () => Promise.reject(new TypeError('Failed to fetch')),
      async () => json({ detail: 'unavailable' }, 503),
    ]) {
      const { refreshSession, useAuthStore } = await signedIn();
      const fetchMock = vi.fn(answer);
      vi.stubGlobal('fetch', fetchMock);

      expect(await refreshSession()).toBe(false);
      expect(fetchMock).toHaveBeenCalledTimes(1);
      expect(useAuthStore.getState().user).toEqual({ sub: 'u' });
      expect(useAuthStore.getState().token).toBe('old');
    }
  });

  it('sends nothing without a signed-in user', async () => {
    vi.resetModules();
    const { refreshSession } = await import('./session');
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);

    expect(await refreshSession()).toBe(false);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('holds the shared Web Lock of the tabs while it uses the refresh cookie', async () => {
    const { refreshSession } = await signedIn();
    let held = false;
    const request = vi.fn(async (_name: string, callback: () => Promise<unknown>) => {
      held = true;
      try {
        return await callback();
      } finally {
        held = false;
      }
    });
    Object.defineProperty(window.navigator, 'locks', { value: { request }, configurable: true });
    const heldDuringRefresh: boolean[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        heldDuringRefresh.push(held);
        return json({ token: 'new', expires_at: future });
      }),
    );

    try {
      expect(await refreshSession()).toBe(true);
    } finally {
      Reflect.deleteProperty(window.navigator, 'locks');
    }
    expect(request).toHaveBeenCalledTimes(1);
    expect(request.mock.calls[0]?.[0]).toBe('reticora-auth-refresh');
    expect(heldDuringRefresh).toEqual([true]);
  });
});

describe('refresh refused by the pre-auth limit (AUT-10, 429)', () => {
  function limitedThen(second: () => Response, headers: Record<string, string>) {
    const sent: number[] = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      if (!isRefresh(input)) return json({ data: [] });
      sent.push(Date.now());
      return sent.length === 1
        ? json({ detail: 'too many authentication attempts' }, 429, headers)
        : second();
    });
    vi.stubGlobal('fetch', fetchMock);
    return sent;
  }

  it('waits Retry-After and tries once more without ending the session', async () => {
    vi.useFakeTimers();
    const { refreshSession, useAuthStore } = await signedIn();
    const sent = limitedThen(() => json({ token: 'new', expires_at: future }), {
      'Retry-After': '5',
    });

    const result = refreshSession();
    await vi.advanceTimersByTimeAsync(4_999);
    expect(sent).toHaveLength(1);
    expect(useAuthStore.getState().user, 'the 429 keeps the session').toEqual({ sub: 'u' });
    await vi.advanceTimersByTimeAsync(1);

    expect(await result).toBe(true);
    expect(sent).toHaveLength(2);
    expect(sent[1]! - sent[0]!).toBe(5_000);
    expect(useAuthStore.getState().token).toBe('new');
  });

  it('keeps the session when the second attempt is refused again', async () => {
    vi.useFakeTimers();
    const { refreshSession, useAuthStore } = await signedIn();
    const sent = limitedThen(() => json({ detail: 'too many' }, 429, { 'Retry-After': '1' }), {
      'Retry-After': '1',
    });

    const result = refreshSession();
    await vi.advanceTimersByTimeAsync(60_000);

    expect(await result).toBe(false);
    expect(sent, 'one retry only').toHaveLength(2);
    expect(useAuthStore.getState().user).toEqual({ sub: 'u' });
    expect(useAuthStore.getState().token).toBe('old');
  });

  it('waits at most 60 s, also without Retry-After', async () => {
    for (const headers of [
      { 'Retry-After': '3600' },
      { 'Retry-After': new Date(Date.now() + 3_600_000).toUTCString() },
      {} as Record<string, string>,
    ]) {
      vi.useFakeTimers();
      const { refreshSession } = await signedIn();
      const sent = limitedThen(() => json({ token: 'new', expires_at: future }), headers);

      const result = refreshSession();
      await vi.advanceTimersByTimeAsync(59_999);
      expect(sent, JSON.stringify(headers)).toHaveLength(1);
      await vi.advanceTimersByTimeAsync(1);

      expect(await result).toBe(true);
      expect(sent[1]! - sent[0]!, JSON.stringify(headers)).toBe(60_000);
      vi.useRealTimers();
    }
  });

  it('does not retry after the user signed out during the wait', async () => {
    vi.useFakeTimers();
    const { refreshSession, clearSession } = await signedIn();
    const sent = limitedThen(() => json({ token: 'new', expires_at: future }), {
      'Retry-After': '5',
    });

    const result = refreshSession();
    await vi.advanceTimersByTimeAsync(1_000);
    clearSession();
    await vi.advanceTimersByTimeAsync(5_000);

    expect(await result).toBe(false);
    expect(sent).toHaveLength(1);
  });
});

describe('planned renewal before expiry (AUT-02)', () => {
  it('renews about 60 s before expiresAt and plans the next renewal', async () => {
    vi.useFakeTimers();
    const expiresAt = new Date(Date.now() + 5 * 60_000).toISOString();
    const { useAuthStore } = await signedIn(expiresAt);
    const refreshes: number[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        refreshes.push(Date.now());
        return json({
          token: `t${refreshes.length}`,
          expires_at: new Date(Date.now() + 15 * 60_000).toISOString(),
        });
      }),
    );

    await vi.advanceTimersByTimeAsync(4 * 60_000 - 1);
    expect(refreshes).toHaveLength(0);
    await vi.advanceTimersByTimeAsync(1);
    expect(refreshes).toHaveLength(1);
    expect(useAuthStore.getState().token).toBe('t1');

    await vi.advanceTimersByTimeAsync(14 * 60_000);
    expect(refreshes).toHaveLength(2);
    expect(useAuthStore.getState().token).toBe('t2');
    useAuthStore.getState().clearSession();
  });

  it('plans nothing after the session ended', async () => {
    vi.useFakeTimers();
    const { useAuthStore } = await signedIn(new Date(Date.now() + 5 * 60_000).toISOString());
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);

    useAuthStore.getState().clearSession();
    await vi.advanceTimersByTimeAsync(10 * 60_000);

    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('plans the renewal of a session restored on reload', async () => {
    vi.useFakeTimers();
    const { AUTH_STORAGE_KEY } = await import('./authStore');
    window.sessionStorage.setItem(
      AUTH_STORAGE_KEY,
      JSON.stringify({
        token: 'stored',
        expiresAt: new Date(Date.now() + 2 * 60_000).toISOString(),
        user: { sub: 'u' },
      }),
    );
    vi.resetModules();
    const { useAuthStore } = await import('./authStore');
    await import('./session');
    const fetchMock = vi.fn(async () => json({ token: 'renewed', expires_at: future }));
    vi.stubGlobal('fetch', fetchMock);

    await vi.advanceTimersByTimeAsync(60_000);

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(useAuthStore.getState().token).toBe('renewed');
  });
});
