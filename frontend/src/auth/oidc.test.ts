import { afterEach, describe, expect, it, vi } from 'vitest';

afterEach(() => {
  window.sessionStorage.clear();
  vi.restoreAllMocks();
  vi.resetModules();
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
