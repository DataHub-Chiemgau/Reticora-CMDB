import { afterEach, describe, expect, it, vi } from 'vitest';

const originalCryptoDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'crypto');

afterEach(() => {
  window.sessionStorage.clear();
  vi.restoreAllMocks();
  vi.resetModules();
  if (originalCryptoDescriptor) {
    Object.defineProperty(globalThis, 'crypto', originalCryptoDescriptor);
  }
});

describe('OIDC callback handling', () => {
  it('rejects a mismatched state without exchanging the code', async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);
    window.sessionStorage.setItem(
      'reticora-auth-transaction',
      JSON.stringify({ state: 'expected-state', codeVerifier: 'verifier', returnTo: '/dashboard' }),
    );
    const { completeAuthorizationCodeFlow } = await import('./oidc');

    await expect(completeAuthorizationCodeFlow('?code=abc&state=wrong-state')).rejects.toThrow(
      'Invalid authentication state.',
    );
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe('OIDC login on insecure origins', () => {
  it('fails with a readable error when the Web Crypto API is unavailable', async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);
    // Browsers only expose Web Crypto in secure contexts (HTTPS or localhost).
    // When the app is served over plain HTTP, window.crypto is missing.
    Object.defineProperty(globalThis, 'crypto', { value: undefined, configurable: true });
    const { startAuthorizationCodeFlow, InsecureContextError } = await import('./oidc');

    await expect(startAuthorizationCodeFlow('/dashboard')).rejects.toThrow(InsecureContextError);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe('OIDC configuration validation', () => {
  it('reports an incomplete server configuration instead of redirecting with undefined parameters', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({
        issuer: 'https://idp.example.com/realms/reticora',
        client_id: 'reticora-app',
        scopes: ['openid'],
        authorization_endpoint:
          'https://idp.example.com/realms/reticora/protocol/openid-connect/auth',
        pkce_required: true,
      }),
    });
    vi.stubGlobal('fetch', fetchMock);
    const { startAuthorizationCodeFlow } = await import('./oidc');

    await expect(startAuthorizationCodeFlow('/dashboard')).rejects.toThrow(/redirect_uri/);
  });
});

describe('returnTo after the login (SR-62, WP-874)', () => {
  it('keeps a local path and replaces every other target with /dashboard', async () => {
    const { safeReturnTo } = await import('./oidc');

    for (const local of ['/cmdb?x=1', '/dashboard', '/cmdb/ci/1#history', '/']) {
      expect(safeReturnTo(local)).toBe(local);
    }
    for (const foreign of [
      '//evil.example',
      '/\\evil.example',
      '/%5Cevil',
      '/%2F%2Fevil.example',
      '/\t/evil.example',
      'https://evil.example',
      'javascript:alert(1)',
      'cmdb',
      '',
      '/%E0%A4%A',
      undefined,
      42,
    ]) {
      expect(safeReturnTo(foreign)).toBe('/dashboard');
    }
  });

  it('checks the stored target again in the callback', async () => {
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValue(
          new Response(
            JSON.stringify({ token: 't', expires_at: '2099-01-01T00:00:00Z', user: { id: 'u' } }),
            { status: 200, headers: { 'Content-Type': 'application/json' } },
          ),
        ),
    );
    window.sessionStorage.setItem(
      'reticora-auth-transaction',
      JSON.stringify({ state: 's', codeVerifier: 'v', returnTo: '//evil.example/path' }),
    );
    const { completeAuthorizationCodeFlow } = await import('./oidc');

    const result = await completeAuthorizationCodeFlow('?code=abc&state=s');
    expect(result.returnTo).toBe('/dashboard');
  });
});

describe('operator login (WP-887)', () => {
  it('starts with acr_values=2 and an operator transaction', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation(
        async () =>
          new Response(
            JSON.stringify({
              client_id: 'reticora-ui',
              redirect_uri: 'http://localhost/auth/callback',
              authorization_endpoint: 'http://idp.example/auth',
              scopes: ['openid'],
            }),
            { status: 200, headers: { 'Content-Type': 'application/json' } },
          ),
      ),
    );
    const assign = vi.fn();
    vi.spyOn(window, 'location', 'get').mockReturnValue({
      ...window.location,
      assign,
      origin: 'http://localhost',
    } as Location);
    const { startAuthorizationCodeFlow } = await import('./oidc');

    const url = new URL(await startAuthorizationCodeFlow('/dashboard', { kind: 'operator' }));
    expect(url.searchParams.get('acr_values')).toBe('2');
    const transaction = JSON.parse(window.sessionStorage.getItem('reticora-auth-transaction')!);
    expect(transaction.kind).toBe('operator');
    expect(transaction.returnTo).toBe('/operator');

    const tenant = new URL(await startAuthorizationCodeFlow('/dashboard'));
    expect(tenant.searchParams.has('acr_values')).toBe(false);
    expect(JSON.parse(window.sessionStorage.getItem('reticora-auth-transaction')!).kind).toBe(
      'tenant',
    );
  });

  it('exchanges the code at the operator callback and leaves the tenant session alone', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          token: 'op-token',
          expires_at: '2099-01-01T00:00:00Z',
          operator: { id: 'op-1', name: 'Olga', kind: 'oidc' },
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      ),
    );
    vi.stubGlobal('fetch', fetchMock);
    window.sessionStorage.setItem(
      'reticora-auth-transaction',
      JSON.stringify({ state: 's', codeVerifier: 'v', returnTo: '/operator', kind: 'operator' }),
    );
    const { completeAuthorizationCodeFlow } = await import('./oidc');
    const { useAuthStore } = await import('./authStore');
    const { useOperatorAuth } = await import('../operator/operatorAuth');

    const result = await completeAuthorizationCodeFlow('?code=abc&state=s');
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/v1/admin/auth/callback');
    expect(result.returnTo).toBe('/operator');
    expect(useOperatorAuth.getState().session?.token).toBe('op-token');
    expect(useAuthStore.getState().token).toBeNull();
    expect(window.sessionStorage.getItem('reticora-auth-session')).toBeNull();
  });
});
