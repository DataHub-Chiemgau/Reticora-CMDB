import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { DashboardPage } from './DashboardPage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';
import { useAuthStore } from '../auth/authStore';

function paginated<T>(data: T[]) {
  return { data, total: data.length, limit: 50, offset: 0, has_more: false };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  useAuthStore.setState({ token: null, expiresAt: null, user: null });
});

describe('DashboardPage', () => {
  it('renders summary cards with counts', async () => {
    stubFetchRoutes({
      '/cis?limit=1&offset=0&status=active': { ...paginated([]), total: 3 },
      '/cis?limit=1&offset=0&status=maintenance': { ...paginated([]), total: 2 },
      '/cis?limit=1&offset=0': { ...paginated([]), total: 5 },
      '/collectors': paginated([
        {
          id: 'c1',
          organization_id: 'org-1',
          name: 'collector-a',
          status: 'active',
          config: {},
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:00:00Z',
        },
      ]),
    });

    renderWithProviders(<DashboardPage />, { route: '/dashboard' });

    expect(await screen.findByText('5')).toBeInTheDocument(); // total CIs
    expect(screen.getByText('3')).toBeInTheDocument(); // active CIs
    expect(screen.getByText('2')).toBeInTheDocument(); // maintenance CIs
    expect(screen.getByText('1')).toBeInTheDocument(); // online collectors
    // Collector and CIs are there: the onboarding is finished (F-351).
    expect(screen.queryByRole('link', { name: 'Einrichtung fortsetzen' })).toBeNull();
  });

  it('counts every active collector, also beyond the 200 of one page (F-362)', async () => {
    const collectors = Array.from({ length: 250 }, (_, i) => ({
      id: `c${i}`,
      organization_id: 'org-1',
      name: `collector-${i}`,
      status: i < 240 ? 'active' : 'offline',
      config: {},
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-01T00:00:00Z',
    }));
    const counts = (total: number) => ({ ...paginated([]), total });
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input), 'http://localhost');
        const status = url.searchParams.get('status');
        let body: unknown;
        if (url.pathname === '/api/v1/collectors') {
          // The server caps the limit at 200 (parameter Limit, API-02).
          const limit = Math.min(Number(url.searchParams.get('limit') ?? 50), 200);
          const offset = Number(url.searchParams.get('offset') ?? 0);
          body = {
            data: collectors.slice(offset, offset + limit),
            total: collectors.length,
            limit,
            offset,
            has_more: offset + limit < collectors.length,
          };
        } else if (url.pathname === '/api/v1/cis') {
          body = counts(status === 'active' ? 3 : status === 'maintenance' ? 2 : 5);
        } else {
          return new Response(JSON.stringify({ detail: 'unexpected' }), { status: 404 });
        }
        return new Response(JSON.stringify(body), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        });
      }),
    );

    renderWithProviders(<DashboardPage />, { route: '/dashboard' });

    expect(await screen.findByText('240')).toBeInTheDocument(); // online collectors
  });

  it.each([
    ['without a collector', 0, 5],
    ['without CIs', 1, 0],
  ])('leads back to the onboarding %s (UI-17, F-351)', async (_case, collectors, cis) => {
    stubFetchRoutes({
      '/cis?limit=1&offset=0&status=active': { ...paginated([]), total: 0 },
      '/cis?limit=1&offset=0&status=maintenance': { ...paginated([]), total: 0 },
      '/cis?limit=1&offset=0': { ...paginated([]), total: cis },
      '/collectors': paginated(
        Array.from({ length: collectors }, (_, i) => ({
          id: `c${i}`,
          organization_id: 'org-1',
          name: `collector-${i}`,
          status: 'active',
          config: {},
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:00:00Z',
        })),
      ),
    });

    renderWithProviders(<DashboardPage />, { route: '/dashboard' });

    expect(await screen.findByRole('link', { name: 'Einrichtung fortsetzen' })).toHaveAttribute(
      'href',
      '/onboarding',
    );
    expect(screen.getByText('Einrichtung noch nicht abgeschlossen')).toBeInTheDocument();
  });

  it('does not crash when a list endpoint returns data:null (legacy/empty backend)', async () => {
    const fetchMock = stubFetchRoutes({
      '/cis': { data: null, total: 0, limit: 1, offset: 0, has_more: false },
      '/collectors': { data: null, total: 0, limit: 1000, offset: 0, has_more: false },
    });

    renderWithProviders(<DashboardPage />, { route: '/dashboard' });

    // Wait until the collectors request has been issued, so the render path
    // that previously threw on `.filter(null)` is actually exercised.
    await waitFor(() =>
      expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/collectors'))).toBe(true),
    );
    // After the null payload resolves, the dashboard must render with 0 counts
    // instead of crashing to a blank page.
    expect(
      await screen.findByText(/Schneller Überblick über Ihre Konfigurationsdaten\./),
    ).toBeInTheDocument();
    expect(screen.getByText('Collector-Status')).toBeInTheDocument();
    expect(screen.getAllByText('0').length).toBeGreaterThan(0);
  });

  it('switches the widget set when the role changes', async () => {
    stubFetchRoutes({
      '/cis?limit=1&offset=0&status=active': { ...paginated([]), total: 3 },
      '/cis?limit=1&offset=0&status=maintenance': { ...paginated([]), total: 2 },
      '/cis?limit=1&offset=0': { ...paginated([]), total: 5 },
      '/collectors': paginated([]),
      '/assets?limit=1&offset=0&status=assigned': { ...paginated([]), total: 7 },
      '/assets?limit=1&offset=0': { ...paginated([]), total: 12 },
      '/stocktakes?status=in_progress': { ...paginated([]), total: 1 },
    });

    renderWithProviders(<DashboardPage />, { route: '/dashboard' });

    // Default role "Technik" shows CI overview widgets.
    expect(await screen.findByText('Gesamte CIs')).toBeInTheDocument();

    const roleSelect = screen.getByRole('combobox', { name: 'Dashboard-Rolle' });
    fireEvent.change(roleSelect, { target: { value: 'procurement' } });

    // Procurement role replaces the CI overview with inventory widgets.
    expect(await screen.findByText('Assets gesamt')).toBeInTheDocument();
    expect(screen.getByText('Laufende Inventuren')).toBeInTheDocument();
    expect(screen.queryByText('Gesamte CIs')).not.toBeInTheDocument();
  });

  it('hides the widgets of switched-off modules (WP-214)', async () => {
    useAuthStore.setState({
      token: 'header.payload.signature',
      expiresAt: new Date(Date.now() + 3600_000).toISOString(),
      user: { sub: 'user-1' },
    });
    stubFetchRoutes({
      '/modules': {
        data: [
          { key: 'ticketing', phase: 'P2', nav: ['tickets', 'slas'], enabled: false },
          { key: 'inventory', phase: 'P2', nav: ['assets'], enabled: true },
        ],
      },
      '/cis': paginated([]),
      '/collectors': paginated([]),
      '/assets': paginated([]),
    });

    renderWithProviders(<DashboardPage />, { route: '/dashboard' });

    await waitFor(() =>
      expect(screen.queryByRole('button', { name: 'Tickets' })).not.toBeInTheDocument(),
    );
    expect(screen.queryByText('Offene Tickets')).not.toBeInTheDocument();
  });
});
