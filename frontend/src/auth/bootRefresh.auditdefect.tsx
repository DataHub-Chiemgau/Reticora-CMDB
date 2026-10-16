// Phase-1 acceptance audit, group T2-auth-authz: defect test of the session
// continuation on reload/navigation (fails until the finding is fixed).
//
// WP-066 (AUT-02), finding P2 "Expired stored session redirects to /login
// without trying the refresh cookie (reload, navigation, new tab)"; gap
// (high) "Frontend single-flight and boot refresh are not tested". AUT-02:
// the session continues with the rotating HttpOnly refresh cookie. App.tsx
// RequireAuth decides on the locally stored expiresAt only and redirects to
// /login although the refresh cookie is still valid.
//
// Retest: cd frontend && npx vitest run -c vitest.auditdefect.config.ts src/auth/bootRefresh.auditdefect.tsx
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';

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

describe('reload after the access token expired (AUT-02)', () => {
  it('renews the session with the refresh cookie before sending the user to /login', async () => {
    vi.resetModules();
    const { AUTH_STORAGE_KEY } = await import('./authStore');
    // A session of this tab whose 15-minute access token has expired.
    window.sessionStorage.setItem(
      AUTH_STORAGE_KEY,
      JSON.stringify({
        token: 'expired',
        expiresAt: '2000-01-01T00:00:00Z',
        user: { sub: 'u', permissions: ['ci:read'] },
      }),
    );
    let refreshes = 0;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        if (urlOf(input).endsWith('/api/v1/auth/refresh')) {
          refreshes += 1;
          return json({ token: 'renewed', expires_at: future });
        }
        return json({ data: [] });
      }),
    );
    // jsdom has no matchMedia (theme store at App import).
    vi.stubGlobal(
      'matchMedia',
      vi.fn((query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addEventListener: () => undefined,
        removeEventListener: () => undefined,
        addListener: () => undefined,
        removeListener: () => undefined,
        dispatchEvent: () => false,
      })),
    );
    const { useAuthStore } = await import('./authStore');
    const { default: App } = await import('../App');
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });

    render(
      <MemoryRouter initialEntries={['/dashboard']}>
        <QueryClientProvider client={client}>
          <App />
        </QueryClientProvider>
      </MemoryRouter>,
    );

    await waitFor(
      () => {
        expect(refreshes, 'POST /api/v1/auth/refresh on reload with an expired token').toBe(1);
      },
      { timeout: 3000 },
    );
    await waitFor(() => {
      expect(useAuthStore.getState().token).toBe('renewed');
    });
  });
});
