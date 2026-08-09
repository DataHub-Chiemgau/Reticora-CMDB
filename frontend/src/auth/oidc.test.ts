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
