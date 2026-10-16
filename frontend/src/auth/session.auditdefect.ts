// Phase-1 acceptance audit, group T2-auth-authz: defect test of the SPA
// token renewal (fails until the finding is fixed).
//
// WP-066 (AUT-02), finding P2 "Concurrent 401 responses trigger parallel
// refreshes that revoke the refresh family"; gap (high) "Frontend
// single-flight and boot refresh are not tested". AUT-02: the rotating
// HttpOnly refresh cookie renews the 15-minute access token; the server
// treats a second use of a consumed cookie as reuse and revokes the family
// (identity TestRefreshRotatesAndDetectsReuse). fetchAPI calls
// refreshSession() once per 401 (api/client.ts), and refreshSession has no
// shared in-flight promise, so parallel queries at token expiry send the
// same cookie several times and the user is signed out.
//
// Retest: cd frontend && npx vitest run -c vitest.auditdefect.config.ts src/auth/session.auditdefect.ts
import { afterEach, describe, expect, it, vi } from 'vitest';

const future = '2999-01-01T00:00:00Z';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function urlOf(input: RequestInfo | URL) {
  if (typeof input === 'string') return input;
  if (input instanceof URL) return input.pathname;
  return input.url;
}

afterEach(() => {
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
});
