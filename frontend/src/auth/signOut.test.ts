// Sign-out with OpenID Connect RP-initiated logout (F-351): the Reticora
// session ends first, then the browser leaves for the IdP's end-session
// endpoint with client_id and post_logout_redirect_uri, so the IdP ends its
// single sign-on session and returns to the sign-in page. Without an
// end-session endpoint only the Reticora session ends.
//
// Retest: cd frontend && npx vitest run src/auth/signOut.test.ts
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { AuthConfig } from './oidc';

const CONFIG: AuthConfig = {
  issuer: 'https://id.example/realms/reticora',
  client_id: 'reticora-app',
  redirect_uri: 'https://cmdb.example/auth/callback',
  scopes: ['openid'],
  authorization_endpoint: 'https://id.example/realms/reticora/protocol/openid-connect/auth',
  end_session_endpoint: 'https://id.example/realms/reticora/protocol/openid-connect/logout',
  pkce_required: true,
};

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

/**
 * signedIn loads fresh modules with a session, a server that answers the
 * logout and GET /auth/config with `config` (an Error rejects the fetch),
 * and a window.location whose assign is recorded.
 */
async function signedIn(config: AuthConfig | Response | Error = CONFIG) {
  vi.resetModules();
  const { useAuthStore } = await import('./authStore');
  useAuthStore.getState().setSession({
    token: 'reticora-access-token',
    expiresAt: '2999-01-01T00:00:00Z',
    user: { sub: 'admin-1', email: 'admin@reticora.example' },
  });
  const calls: string[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(`${init?.method ?? 'GET'} ${url}`);
    if (url === '/api/v1/auth/logout') return new Response(null, { status: 204 });
    if (url === '/api/v1/auth/config') {
      if (config instanceof Error) throw config;
      return config instanceof Response ? config : json(config);
    }
    return json({ detail: 'unexpected' }, 404);
  });
  vi.stubGlobal('fetch', fetchMock);
  const assign = vi.fn((url: string) => calls.push(`assign ${url}`));
  vi.stubGlobal('location', { ...window.location, assign });
  const module = await import('./signOut');
  return { ...module, useAuthStore, calls, assign };
}

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
});

describe('endSessionUrl', () => {
  it('names the client and returns to the sign-in page of the app', async () => {
    const { endSessionUrl } = await import('./signOut');

    const url = new URL(endSessionUrl(CONFIG) ?? '');
    expect(`${url.origin}${url.pathname}`).toBe(CONFIG.end_session_endpoint);
    expect(url.searchParams.get('client_id')).toBe('reticora-app');
    expect(url.searchParams.get('post_logout_redirect_uri')).toBe('https://cmdb.example/login');
    // The SPA keeps no ID token: Keycloak accepts the redirect for the
    // client_id and asks to confirm the logout instead.
    expect(url.searchParams.has('id_token_hint')).toBe(false);
  });

  it('keeps the query of the end-session endpoint', async () => {
    const { endSessionUrl } = await import('./signOut');

    const url = new URL(
      endSessionUrl({ ...CONFIG, end_session_endpoint: `${CONFIG.end_session_endpoint}?a=1` }) ??
        '',
    );
    expect(url.searchParams.get('a')).toBe('1');
    expect(url.searchParams.get('client_id')).toBe('reticora-app');
  });

  it('is null without an end-session endpoint', async () => {
    const { endSessionUrl } = await import('./signOut');

    expect(endSessionUrl({ ...CONFIG, end_session_endpoint: undefined })).toBeNull();
    expect(endSessionUrl({ ...CONFIG, end_session_endpoint: 'not a url' })).toBeNull();
  });
});

describe('signOut', () => {
  it('ends the Reticora session, then the IdP session with a way back', async () => {
    const { signOut, useAuthStore, calls, assign } = await signedIn();

    await expect(signOut()).resolves.toBe('idp');

    expect(useAuthStore.getState().isAuthenticated()).toBe(false);
    expect(calls).toEqual([
      'POST /api/v1/auth/logout',
      'GET /api/v1/auth/config',
      `assign ${CONFIG.end_session_endpoint}?client_id=reticora-app&post_logout_redirect_uri=${encodeURIComponent('https://cmdb.example/login')}`,
    ]);
    // The Reticora access token never travels to the IdP.
    expect(String(assign.mock.calls[0]?.[0])).not.toContain('reticora-access-token');
  });

  it('uses the stored configuration of the sign-in', async () => {
    const { signOut, calls } = await signedIn(new Error('not asked'));
    window.sessionStorage.setItem('reticora-auth-config', JSON.stringify(CONFIG));

    await expect(signOut()).resolves.toBe('idp');

    expect(calls[0]).toBe('POST /api/v1/auth/logout');
    expect(calls).not.toContain('GET /api/v1/auth/config');
    expect(calls[1]).toMatch(/^assign https:\/\/id\.example\/.*\/logout\?client_id=/);
  });

  it('ends only the Reticora session without an end-session endpoint', async () => {
    const { signOut, useAuthStore, assign } = await signedIn({
      ...CONFIG,
      end_session_endpoint: undefined,
    });

    await expect(signOut()).resolves.toBe('local');

    expect(useAuthStore.getState().isAuthenticated()).toBe(false);
    expect(assign).not.toHaveBeenCalled();
  });

  it('ends only the Reticora session when the configuration is unreachable', async () => {
    for (const failure of [json({ detail: 'down' }, 503), new TypeError('Failed to fetch')]) {
      const { signOut, useAuthStore, assign } = await signedIn(failure);

      await expect(signOut()).resolves.toBe('local');

      expect(useAuthStore.getState().isAuthenticated()).toBe(false);
      expect(assign).not.toHaveBeenCalled();
      vi.unstubAllGlobals();
    }
  });
});
