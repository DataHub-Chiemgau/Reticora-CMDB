import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { DashboardPage } from './DashboardPage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';
import { useAuthStore } from '../auth/authStore';
import { useDashboardStore } from '../stores/dashboard';

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

  describe('escalated reviews (REC-08, F-247)', () => {
    const DISCOVERY_READER = { user_id: 'user-1', permissions: ['ci:read', 'discovery:read'] };
    const BASE_ROUTES = {
      '/cis?limit=1&offset=0&status=active': { ...paginated([]), total: 3 },
      '/cis?limit=1&offset=0&status=maintenance': { ...paginated([]), total: 2 },
      '/cis?limit=1&offset=0': { ...paginated([]), total: 5 },
      '/collectors': paginated([]),
    };

    // The widget belongs to the technician set (the default role); an
    // earlier test may have switched the role.
    beforeEach(() => useDashboardStore.setState({ role: 'technician', hidden: [] }));

    function widget() {
      return screen.findByRole('heading', { name: 'Eskalierte Reviews' }).then((heading) => {
        const card = heading.closest('section');
        expect(card).not.toBeNull();
        return within(card as HTMLElement);
      });
    }

    it('shows the number of escalated open items and links to the filtered inbox', async () => {
      const fetchMock = stubFetchRoutes({
        '/me/permissions': DISCOVERY_READER,
        '/review-items': { ...paginated([]), total: 7 },
        ...BASE_ROUTES,
      });

      renderWithProviders(<DashboardPage />, { route: '/dashboard' });

      const card = await widget();
      expect(await card.findByText('7')).toBeInTheDocument();
      expect(card.getByRole('link', { name: 'Eskalierte Reviews anzeigen' })).toHaveAttribute(
        'href',
        '/reviews?escalated=true',
      );
      // GET /review-items?escalated=true&status=open: the total is the count.
      const reviewCalls = fetchMock.mock.calls
        .map(([url]) => new URL(String(url), 'http://localhost'))
        .filter((url) => url.pathname === '/api/v1/review-items');
      expect(reviewCalls.length).toBeGreaterThan(0);
      for (const url of reviewCalls) {
        expect(url.searchParams.get('escalated')).toBe('true');
        expect(url.searchParams.get('status')).toBe('open');
      }
    });

    it('shows a neutral state without escalated items', async () => {
      stubFetchRoutes({
        '/me/permissions': DISCOVERY_READER,
        '/review-items': { ...paginated([]), total: 0 },
        ...BASE_ROUTES,
      });

      renderWithProviders(<DashboardPage />, { route: '/dashboard' });

      const card = await widget();
      expect(await card.findByText('Keine offenen Reviews älter als 14 Tage.')).toBeInTheDocument();
      expect(card.getByText('0')).toBeInTheDocument();
      expect(card.queryByRole('link')).toBeNull();
      expect(card.queryByRole('alert')).toBeNull();
    });

    it('shows a failed load instead of a count', async () => {
      stubFetchRoutes({
        '/me/permissions': DISCOVERY_READER,
        '/review-items': new Response(
          JSON.stringify({ title: 'Internal Server Error', status: 500, detail: 'boom' }),
          { status: 500, headers: { 'Content-Type': 'application/problem+json' } },
        ),
        ...BASE_ROUTES,
      });

      renderWithProviders(<DashboardPage />, { route: '/dashboard' });

      const card = await widget();
      expect(await card.findByRole('alert')).toHaveTextContent(
        'Die eskalierten Reviews konnten nicht geladen werden',
      );
      expect(card.queryByText('0')).toBeNull();
      expect(card.getByRole('button', { name: 'Erneut versuchen' })).toBeInTheDocument();
    });

    it('is not shown and asks nothing without discovery:read', async () => {
      const fetchMock = stubFetchRoutes({
        '/me/permissions': { user_id: 'user-1', permissions: ['ci:read'] },
        ...BASE_ROUTES,
      });

      renderWithProviders(<DashboardPage />, { route: '/dashboard' });

      expect(await screen.findByText('Gesamte CIs')).toBeInTheDocument();
      await waitFor(() =>
        expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/me/permissions'))).toBe(
          true,
        ),
      );
      expect(screen.queryByRole('heading', { name: 'Eskalierte Reviews' })).toBeNull();
      expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/review-items'))).toBe(
        false,
      );
    });
  });

  it('counts new and open tickets as open, also the critical ones (WP-814)', async () => {
    useDashboardStore.setState({ role: 'management', hidden: [] });
    const counts: Record<string, number> = {
      'new|': 2,
      'open|': 3,
      'new|critical': 1,
      'open|critical': 3,
    };
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://localhost');
      const json = (body: unknown) =>
        new Response(JSON.stringify(body), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        });
      if (url.pathname === '/api/v1/tickets') {
        const key = `${url.searchParams.get('status')}|${url.searchParams.get('priority') ?? ''}`;
        return json({ ...paginated([]), total: counts[key] ?? 99 });
      }
      if (url.pathname === '/api/v1/modules') {
        return json({
          data: [{ key: 'ticketing', phase: 'P2', nav: ['tickets', 'slas'], enabled: true }],
        });
      }
      if (url.pathname === '/api/v1/cis' || url.pathname === '/api/v1/collectors') {
        return json(paginated([]));
      }
      return new Response(JSON.stringify({ detail: 'unexpected' }), { status: 404 });
    });
    vi.stubGlobal('fetch', fetchMock);

    renderWithProviders(<DashboardPage />, { route: '/dashboard' });

    const open = (await screen.findByRole('link', { name: 'Offene Tickets' })).closest('section')!;
    await waitFor(() => expect(within(open).getByText('5')).toBeInTheDocument());
    const critical = screen.getByRole('link', { name: 'Kritische Tickets' }).closest('section')!;
    await waitFor(() => expect(within(critical).getByText('4')).toBeInTheDocument());
    const statuses = fetchMock.mock.calls
      .map(([input]) => new URL(String(input), 'http://localhost'))
      .filter((url) => url.pathname === '/api/v1/tickets')
      .map((url) => url.searchParams.get('status'));
    expect(new Set(statuses)).toEqual(new Set(['new', 'open']));
    useDashboardStore.setState({ role: 'technician', hidden: [] });
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

  it('hides the widgets without their entitlement instead of showing a 403 (WP-705)', async () => {
    useAuthStore.setState({
      token: 'header.payload.signature',
      expiresAt: new Date(Date.now() + 3600_000).toISOString(),
      user: { sub: 'user-1' },
    });
    const fetchMock = stubFetchRoutes({
      '/modules': {
        data: [{ key: 'ticketing', phase: 'P2', nav: ['tickets', 'slas'], enabled: true }],
      },
      '/entitlements': paginated([
        { organization_id: 'org-1', feature_key: 'ticketing_essential', enabled: true },
        { organization_id: 'org-1', feature_key: 'ticketing_standard', enabled: false },
      ]),
      '/cis': paginated([]),
      '/collectors': paginated([]),
      '/tickets': paginated([]),
    });

    renderWithProviders(<DashboardPage />, { route: '/dashboard' });

    await waitFor(() =>
      expect(screen.queryByRole('button', { name: 'SLA-Verletzungen' })).not.toBeInTheDocument(),
    );
    // Essential ticketing keeps its widget.
    expect(screen.getByRole('button', { name: 'Tickets' })).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/entitlements'))).toBe(true);
  });

  it('shows the compliance widget with compliance_findings, not with the former key compliance (WP-862)', async () => {
    useAuthStore.setState({
      token: 'header.payload.signature',
      expiresAt: new Date(Date.now() + 3600_000).toISOString(),
      user: { sub: 'user-1' },
    });
    const routes = (featureKey: string) => ({
      '/modules': { data: [{ key: 'compliance', phase: 'P3', nav: ['compliance'], enabled: true }] },
      '/entitlements': paginated([{ organization_id: 'org-1', feature_key: featureKey, enabled: true }]),
      '/compliance/score': { overall: { score: 80, passed: 4, failed: 1, total: 5 }, rules: [] },
      '/cis': paginated([]),
      '/collectors': paginated([]),
    });

    stubFetchRoutes(routes('compliance_findings'));
    const { unmount } = renderWithProviders(<DashboardPage />, { route: '/dashboard' });
    expect(await screen.findByText('80 %')).toBeInTheDocument();
    unmount();

    stubFetchRoutes(routes('compliance'));
    renderWithProviders(<DashboardPage />, { route: '/dashboard' });
    await waitFor(() => expect(screen.queryByText('Compliance-Score')).not.toBeInTheDocument());
    expect(screen.queryByText('80 %')).not.toBeInTheDocument();
  });

  describe('quick access and setup checklist (WP-857)', () => {
    const SETUP_READER = {
      user_id: 'user-1',
      permissions: ['ci:read', 'discovery:read', 'credential:read', 'location:read'],
    };
    const client = (id: string, slug: string) => ({
      id,
      organization_id: 'org-1',
      name: slug,
      slug,
      external_ref: null,
      timezone: null,
      settings: {},
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-01T00:00:00Z',
    });

    function signInAs(sub: string) {
      useAuthStore.setState({
        token: 'header.payload.signature',
        expiresAt: new Date(Date.now() + 3600_000).toISOString(),
        user: { sub },
      });
    }

    beforeEach(() => {
      window.localStorage.clear();
      useDashboardStore.setState({ role: 'technician', hidden: [], setupHiddenFor: [] });
    });

    function checklist() {
      return screen.findByRole('heading', { name: 'Einrichtung' }).then((heading) => {
        const section = heading.closest('section');
        expect(section).not.toBeNull();
        return within(section as HTMLElement);
      });
    }

    it('offers the reachable pages and leaves out the locked ones', async () => {
      signInAs('user-1');
      stubFetchRoutes({
        '/me/permissions': { user_id: 'user-1', permissions: ['ci:read'] },
        '/modules': { data: [{ key: 'ticketing', phase: 'P2', nav: ['tickets'], enabled: false }] },
        '/entitlements': paginated([
          { organization_id: 'org-1', feature_key: 'cmdb_core', plan: 'essential', enabled: true },
          { organization_id: 'org-1', feature_key: 'topology', plan: 'essential', enabled: true },
          {
            organization_id: 'org-1',
            feature_key: 'assets_basic',
            plan: 'essential',
            enabled: false,
          },
        ]),
        '/cis': paginated([]),
        '/collectors': paginated([]),
      });

      renderWithProviders(<DashboardPage />, { route: '/dashboard' });

      const region = await screen.findByRole('heading', { name: 'Schnellzugriff' });
      const quick = within(region.closest('section') as HTMLElement);
      expect(quick.getByRole('link', { name: 'CMDB' })).toHaveAttribute('href', '/cmdb');
      await waitFor(() => expect(quick.getByRole('link', { name: 'Topologie' })).toBeVisible());
      // Locked by the plan, switched off in the release, without the right.
      await waitFor(() => expect(quick.queryByRole('link', { name: 'Inventar' })).toBeNull());
      expect(quick.queryByRole('link', { name: 'Tickets' })).toBeNull();
      expect(quick.queryByRole('link', { name: 'Kunden' })).toBeNull();
      // The dashboard does not lead to itself.
      expect(quick.queryByRole('link', { name: 'Dashboard' })).toBeNull();
    });

    it('ticks off the steps from the existing lists and links where they are done', async () => {
      const fetchMock = stubFetchRoutes({
        '/me/permissions': SETUP_READER,
        '/clients': paginated([client('cl-1', 'default')]),
        '/sites': { ...paginated([]), total: 1 },
        '/credentials': { ...paginated([]), total: 0 },
        '/cis?limit=1&offset=0&status=active': { ...paginated([]), total: 3 },
        '/cis?limit=1&offset=0&status=maintenance': { ...paginated([]), total: 2 },
        '/cis?limit=1&offset=0': { ...paginated([]), total: 5 },
        '/collectors': paginated([]),
      });

      renderWithProviders(<DashboardPage />, { route: '/dashboard' });

      const list = await checklist();
      expect(await list.findByText('2 von 5 Schritten erledigt')).toBeInTheDocument();
      const steps = list.getAllByRole('listitem');
      expect(steps.map((step) => within(step).getByRole('link').textContent)).toEqual([
        'Kunde anlegen',
        'Standort anlegen',
        'Zugangsdaten anlegen',
        'Collector registrieren',
        'Erste CIs erfassen',
      ]);
      expect(list.getByRole('link', { name: 'Kunde anlegen' })).toHaveAttribute('href', '/clients');
      expect(list.getByRole('link', { name: 'Standort anlegen' })).toHaveAttribute(
        'href',
        '/locations',
      );
      expect(list.getByRole('link', { name: 'Zugangsdaten anlegen' })).toHaveAttribute(
        'href',
        '/discovery',
      );
      expect(list.getByRole('link', { name: 'Collector registrieren' })).toHaveAttribute(
        'href',
        '/onboarding',
      );
      expect(list.getByRole('link', { name: 'Erste CIs erfassen' })).toHaveAttribute(
        'href',
        '/cmdb',
      );
      // Only the default client exists: the step is open; a site and CIs are there.
      expect(within(steps[0]!).getByText('Offen')).toBeInTheDocument();
      expect(within(steps[1]!).getByText('Erledigt')).toBeInTheDocument();
      expect(within(steps[2]!).getByText('Offen')).toBeInTheDocument();
      expect(within(steps[3]!).getByText('Offen')).toBeInTheDocument();
      expect(within(steps[4]!).getByText('Erledigt')).toBeInTheDocument();
      // Counts are lists with limit=1, no endpoint of their own.
      const urls = fetchMock.mock.calls.map(([url]) => String(url));
      expect(urls).toContain('/api/v1/sites?limit=1');
      expect(urls).toContain('/api/v1/credentials?limit=1');
    });

    it('counts a client of its own besides the default client', async () => {
      stubFetchRoutes({
        '/me/permissions': { user_id: 'user-1', permissions: ['location:read'] },
        '/clients': paginated([client('cl-1', 'default'), client('cl-2', 'acme')]),
        '/sites': { ...paginated([]), total: 0 },
        '/cis': paginated([]),
        '/collectors': paginated([]),
      });

      renderWithProviders(<DashboardPage />, { route: '/dashboard' });

      const list = await checklist();
      expect(await list.findByText('1 von 2 Schritten erledigt')).toBeInTheDocument();
    });

    it('leaves out the steps without the right', async () => {
      const fetchMock = stubFetchRoutes({
        '/me/permissions': { user_id: 'user-1', permissions: ['ci:read'] },
        '/cis': { ...paginated([]), total: 0 },
        '/collectors': paginated([]),
      });

      renderWithProviders(<DashboardPage />, { route: '/dashboard' });

      const list = await checklist();
      expect(list.getAllByRole('listitem')).toHaveLength(1);
      expect(list.getByRole('link', { name: 'Erste CIs erfassen' })).toBeInTheDocument();
      expect(list.queryByRole('link', { name: 'Kunde anlegen' })).toBeNull();
      const urls = fetchMock.mock.calls.map(([url]) => String(url));
      expect(urls.some((url) => url.includes('/credentials'))).toBe(false);
      expect(urls.some((url) => url.includes('/sites'))).toBe(false);
    });

    it('is hidden per user and can be shown again', async () => {
      signInAs('user-1');
      stubFetchRoutes({
        '/me/permissions': { user_id: 'user-1', permissions: ['ci:read'] },
        '/entitlements': paginated([]),
        '/modules': { data: [] },
        '/cis': { ...paginated([]), total: 0 },
        '/collectors': paginated([]),
      });

      const { unmount } = renderWithProviders(<DashboardPage />, { route: '/dashboard' });

      const list = await checklist();
      fireEvent.click(list.getByRole('button', { name: 'Liste ausblenden' }));
      expect(screen.queryByRole('heading', { name: 'Einrichtung' })).toBeNull();
      expect(
        JSON.parse(window.localStorage.getItem('reticora-dashboard-setup-hidden') ?? '[]'),
      ).toEqual(['user-1']);
      const toggle = screen.getByRole('button', { name: 'Einrichtungsliste' });
      expect(toggle).toHaveAttribute('aria-pressed', 'false');
      unmount();

      // Another user of the browser still sees the list.
      signInAs('user-2');
      renderWithProviders(<DashboardPage />, { route: '/dashboard' });
      expect(await screen.findByRole('heading', { name: 'Einrichtung' })).toBeInTheDocument();
      fireEvent.click(screen.getByRole('button', { name: 'Liste ausblenden' }));
      fireEvent.click(screen.getByRole('button', { name: 'Einrichtungsliste' }));
      expect(await screen.findByRole('heading', { name: 'Einrichtung' })).toBeInTheDocument();
    });
  });
});
