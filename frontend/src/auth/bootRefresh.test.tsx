// Session continuation on reload and navigation (AUT-02, F-066): the session
// continues with the rotating HttpOnly refresh cookie. RequireAuth (App.tsx)
// renews an expired access token of a stored user before it sends the user to
// /login; only a refused refresh cookie ends at the login.
//
// Retest: cd frontend && npx vitest run src/auth/bootRefresh.test.tsx
import { afterEach, describe, expect, it, vi } from 'vitest';
import { useEffect } from 'react';
import { act, render, waitFor } from '@testing-library/react';
import { MemoryRouter, useLocation, useNavigate, type NavigateFunction } from 'react-router-dom';
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

// page answers the data requests of the pages behind the login.
function page(url: string) {
  if (url.endsWith('/compliance/score')) {
    return json({ overall: { score: 100, passed: 0, failed: 0 }, by_ci_type: [], results: [] });
  }
  return json({ data: [], total: 0 });
}

function stubMatchMedia() {
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
}

// RouterProbe records the paths the SPA shows and hands out its navigate.
function RouterProbe({
  onPath,
  onNavigate,
}: {
  onPath: (path: string) => void;
  onNavigate: (navigate: NavigateFunction) => void;
}) {
  const location = useLocation();
  const navigate = useNavigate();
  useEffect(() => {
    onPath(location.pathname);
  }, [location.pathname, onPath]);
  useEffect(() => {
    onNavigate(navigate);
  }, [navigate, onNavigate]);
  return null;
}

// renderApp renders the SPA at path and records the paths it shows.
async function renderApp(path: string) {
  const { default: App } = await import('../App');
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const paths: string[] = [];
  let navigate: NavigateFunction = () => undefined;
  render(
    <MemoryRouter initialEntries={[path]}>
      <QueryClientProvider client={client}>
        <App />
        <RouterProbe onPath={(p) => paths.push(p)} onNavigate={(n) => (navigate = n)} />
      </QueryClientProvider>
    </MemoryRouter>,
  );
  return { paths, navigate: (to: string) => navigate(to) };
}

afterEach(() => {
  vi.useRealTimers();
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
    // Reload: the store reads the stored session when it is loaded.
    vi.resetModules();
    let refreshes = 0;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        if (urlOf(input).endsWith('/api/v1/auth/refresh')) {
          refreshes += 1;
          return json({ token: 'renewed', expires_at: future });
        }
        return page(urlOf(input));
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

  it('stays on the target page, which loads with the renewed token only', async () => {
    vi.resetModules();
    stubMatchMedia();
    const { AUTH_STORAGE_KEY } = await import('./authStore');
    window.sessionStorage.setItem(
      AUTH_STORAGE_KEY,
      JSON.stringify({ token: 'expired', expiresAt: '2000-01-01T00:00:00Z', user: { sub: 'u' } }),
    );
    vi.resetModules();
    const sentWith: (string | null)[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        if (urlOf(input).endsWith('/api/v1/auth/refresh')) {
          return json({ token: 'renewed', expires_at: future });
        }
        sentWith.push(new Headers(init?.headers).get('Authorization'));
        return page(urlOf(input));
      }),
    );

    const { paths } = await renderApp('/cmdb');
    const { useAuthStore } = await import('./authStore');

    await waitFor(() => expect(useAuthStore.getState().token).toBe('renewed'));
    await waitFor(() => expect(sentWith.length).toBeGreaterThan(0));
    expect(paths).not.toContain('/login');
    expect(paths[paths.length - 1]).toBe('/cmdb');
    expect(new Set(sentWith), 'no request with the expired token').toEqual(
      new Set(['Bearer renewed']),
    );
  });

  it('sends the user to /login when the server refuses the refresh cookie', async () => {
    vi.resetModules();
    stubMatchMedia();
    const { AUTH_STORAGE_KEY } = await import('./authStore');
    window.sessionStorage.setItem(
      AUTH_STORAGE_KEY,
      JSON.stringify({ token: 'expired', expiresAt: '2000-01-01T00:00:00Z', user: { sub: 'u' } }),
    );
    vi.resetModules();
    const fetchMock = vi.fn(async () => json({ detail: 'session revoked' }, 401));
    vi.stubGlobal('fetch', fetchMock);

    const { paths } = await renderApp('/dashboard');
    const { useAuthStore } = await import('./authStore');

    await waitFor(() => expect(paths[paths.length - 1]).toBe('/login'));
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(useAuthStore.getState().user).toBeNull();
  });

  it('sends a visitor without stored user to /login without a refresh', async () => {
    vi.resetModules();
    stubMatchMedia();
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => page(urlOf(input)));
    vi.stubGlobal('fetch', fetchMock);

    const { paths } = await renderApp('/dashboard');

    await waitFor(() => expect(paths[paths.length - 1]).toBe('/login'));
    expect(fetchMock.mock.calls.map(([input]) => urlOf(input as RequestInfo))).not.toContain(
      '/api/v1/auth/refresh',
    );
  });
});

describe('navigation after the access token expired (AUT-02)', () => {
  it('renews the session before the next page instead of sending the user to /login', async () => {
    vi.resetModules();
    stubMatchMedia();
    vi.useFakeTimers({ toFake: ['Date'] });
    const { useAuthStore } = await import('./authStore');
    await import('./session');
    useAuthStore.getState().setSession({
      token: 'first',
      expiresAt: new Date(Date.now() + 15 * 60_000).toISOString(),
      user: { sub: 'u' },
    });
    let refreshes = 0;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        if (urlOf(input).endsWith('/api/v1/auth/refresh')) {
          refreshes += 1;
          return json({ token: 'second', expires_at: future });
        }
        return page(urlOf(input));
      }),
    );
    const { paths, navigate } = await renderApp('/dashboard');
    await waitFor(() => expect(paths[paths.length - 1]).toBe('/dashboard'));
    expect(refreshes).toBe(0);

    // The tab slept through the planned renewal; the 15 minutes are over.
    vi.setSystemTime(Date.now() + 16 * 60_000);
    act(() => navigate('/cmdb'));

    await waitFor(() => expect(useAuthStore.getState().token).toBe('second'));
    expect(refreshes).toBe(1);
    expect(paths).not.toContain('/login');
    expect(paths[paths.length - 1]).toBe('/cmdb');
    useAuthStore.getState().clearSession();
  });
});
