/**
 * WP-886 (PKG-06, ENT-05, UI-14): the page "Paket und Add-ons". It marks the
 * organization's plan in the comparison, shows a locked feature, a booked
 * add-on with its term, usage bars against the limit ("unbegrenzt" without
 * a limit, no number when the count is hidden) and the origin of a limit;
 * 403 shows an error without asking again. FeatureLockedPage links the page
 * for a user with entitlement:read and the dashboard otherwise.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, screen, waitFor, within } from '@testing-library/react';
import { useAuthStore } from '../auth/authStore';
import deDE from '../i18n/de-DE.json';
import enUS from '../i18n/en-US.json';
import { renderWithProviders } from '../test/utils';
import { FeatureLockedPage } from './FeatureLockedPage';
import { PlanPage } from './PlanPage';

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

const PLANS = {
  data: [
    { plan: 'essential', features: ['cmdb_core', 'documents'], quota_defaults: { max_cis: 500 } },
    {
      plan: 'standard',
      features: ['cmdb_core', 'documents', 'monitoring'],
      quota_defaults: { max_cis: 2500 },
    },
    {
      plan: 'pro',
      features: ['cmdb_core', 'documents', 'monitoring', 'workflows'],
      quota_defaults: { max_cis: 10000 },
    },
    {
      plan: 'enterprise',
      features: ['cmdb_core', 'documents', 'monitoring', 'workflows', 'monitoring_analytics'],
      quota_defaults: { max_cis: 50000 },
    },
  ],
  addons: [
    { key: 'iga', phase: 'A', description_key: 'addons.iga.description' },
    { key: 'ai', phase: 'A', description_key: 'addons.ai.description' },
  ],
};

const ROWS = {
  data: [
    {
      organization_id: 'org-1',
      feature_key: 'cmdb_core',
      plan: 'standard',
      enabled: true,
      limits: { max_cis: 500 },
      source: 'manual',
      origin: 'plan',
      updated_at: '2026-11-01T00:00:00Z',
    },
    {
      organization_id: 'org-1',
      feature_key: 'iga',
      plan: 'standard',
      enabled: true,
      limits: {},
      valid_until: '2027-03-31T00:00:00Z',
      source: 'billing',
      origin: 'addon',
      updated_at: '2026-11-01T00:00:00Z',
    },
    {
      organization_id: 'org-1',
      feature_key: 'documents',
      plan: 'standard',
      enabled: true,
      limits: {},
      valid_until: '2026-12-15T00:00:00Z',
      source: 'manual',
      origin: 'plan',
      updated_at: '2026-11-01T00:00:00Z',
    },
  ],
  total: 3,
  limit: 200,
  offset: 0,
  has_more: false,
  plan: 'standard',
  plan_changed_at: '2026-11-09T10:00:00Z',
};

function usage(hidden: boolean) {
  return {
    plan: 'standard',
    data: [
      { quota: 'max_cis', used: hidden ? null : 3, limit: 500, origin: 'override' },
      { quota: 'max_api_keys', used: hidden ? null : 2, limit: null, origin: 'plan' },
      { quota: 'max_storage_mb', used: hidden ? null : 5, limit: 10000, origin: 'plan' },
    ],
  };
}

function mockApi({
  status = 200,
  hidden = false,
  permissions = ['entitlement:read'],
}: { status?: number; hidden?: boolean; permissions?: string[] } = {}) {
  return vi.fn().mockImplementation(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), 'http://localhost');
    if (url.pathname.endsWith('/me/permissions')) {
      return json({ user_id: 'user-1', permissions });
    }
    if (status !== 200 && url.pathname.includes('/entitlements')) {
      return json({ title: 'Forbidden', status }, status);
    }
    if (url.pathname.endsWith('/entitlements/usage')) return json(usage(hidden));
    if (url.pathname.endsWith('/entitlements/plans')) return json(PLANS);
    if (url.pathname.endsWith('/entitlements')) return json(ROWS);
    return json({ title: 'Not Found', status: 404 }, 404);
  });
}

beforeEach(() => {
  useAuthStore.setState({
    token: 'header.payload.signature',
    expiresAt: new Date(Date.now() + 3600_000).toISOString(),
    user: { sub: 'user-1' },
  });
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  useAuthStore.setState({ token: null, expiresAt: null, user: null });
});

describe('PlanPage', () => {
  it('shows the plan, the comparison, add-ons, usage and expiry', async () => {
    vi.stubGlobal('fetch', mockApi());
    renderWithProviders(<PlanPage />);

    expect(await screen.findByText('Zuletzt geändert am 9. November 2026.')).toBeInTheDocument();

    // The own plan is marked in the comparison; a locked feature is shown.
    const header = screen.getByRole('columnheader', { name: /Standard/ });
    expect(within(header).getByText('Ihr Paket')).toBeInTheDocument();
    const analytics = screen.getByRole('rowheader', { name: 'Monitoring-Analysen' }).closest('tr');
    expect(analytics).not.toBeNull();
    expect(within(analytics as HTMLElement).getAllByText('nicht enthalten')).toHaveLength(3);
    expect(within(analytics as HTMLElement).getAllByText('enthalten')).toHaveLength(1);

    // Add-ons: iga booked with its term, ai not booked.
    expect(screen.getByText('Gebucht')).toBeInTheDocument();
    expect(screen.getByText('Laufzeit bis 31. März 2027')).toBeInTheDocument();
    expect(screen.getByText('Nicht gebucht')).toBeInTheDocument();

    // Usage: a bar 3 of 500 with origin override, unlimited API keys.
    const bar = screen.getByRole('progressbar', { name: 'CIs' });
    expect(bar).toHaveAttribute('aria-valuenow', '3');
    expect(bar).toHaveAttribute('aria-valuemax', '500');
    expect(screen.getAllByText('3 von 500')).not.toHaveLength(0);
    expect(screen.getByText('2 von unbegrenzt')).toBeInTheDocument();
    expect(screen.getByText('Abweichung')).toBeInTheDocument();
    expect(screen.getAllByText('5 MiB von 10.000 MiB')).not.toHaveLength(0);

    // Expiry of a plan feature.
    expect(screen.getByText('Dokumente läuft am 15. Dezember 2026 ab.')).toBeInTheDocument();
  });

  it('shows the limits without a number when the counts are hidden', async () => {
    vi.stubGlobal('fetch', mockApi({ hidden: true }));
    renderWithProviders(<PlanPage />);
    expect(await screen.findByText('Grenze: 500')).toBeInTheDocument();
    expect(screen.getByText('Grenze: unbegrenzt')).toBeInTheDocument();
    expect(screen.queryByRole('progressbar')).toBeNull();
    expect(screen.queryByText(/von 500/)).toBeNull();
  });

  it('shows an error on 403 and does not ask again', async () => {
    const fetchMock = mockApi({ status: 403 });
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<PlanPage />);
    expect(
      await screen.findByText('Sie dürfen Paket und Kontingente nicht einsehen'),
    ).toBeInTheDocument();
    await new Promise((resolve) => setTimeout(resolve, 50));
    const usageCalls = fetchMock.mock.calls.filter((call) =>
      String(call[0]).includes('/entitlements/usage'),
    );
    expect(usageCalls).toHaveLength(1);
  });
});

describe('FeatureLockedPage', () => {
  it('links the plan page with entitlement:read', async () => {
    vi.stubGlobal('fetch', mockApi());
    renderWithProviders(<FeatureLockedPage page="workflows" plan="standard" />);
    await waitFor(() =>
      expect(screen.getByRole('link', { name: 'Paket und Add-ons ansehen' })).toHaveAttribute(
        'href',
        '/plan',
      ),
    );
  });

  it('links the dashboard without entitlement:read', async () => {
    vi.stubGlobal('fetch', mockApi({ permissions: [] }));
    renderWithProviders(<FeatureLockedPage page="workflows" plan="standard" />);
    await waitFor(() =>
      expect(screen.getByRole('link', { name: 'Zum Dashboard' })).toHaveAttribute(
        'href',
        '/dashboard',
      ),
    );
  });
});

describe('plan page texts', () => {
  it('names every feature of the comparison in both languages', () => {
    for (const catalog of [deDE, enUS]) {
      for (const p of PLANS.data) {
        for (const f of p.features) {
          expect(catalog.planPage.features).toHaveProperty(f);
        }
      }
      expect(catalog.nav).toHaveProperty('plan');
      expect(catalog.commandPalette.commands).toHaveProperty('plan');
    }
  });
});
