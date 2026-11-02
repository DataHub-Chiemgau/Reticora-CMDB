/**
 * Tests for the entitlement expiry banner of the layout (ENT-07, WP-464,
 * F-071): a feature whose valid_until lies at most 14 days ahead gets a
 * localized notice with feature and date; 15 days ahead, without valid_until,
 * after the end or disabled it gets none.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, screen, waitFor, within } from '@testing-library/react';
import i18n from '../i18n';
import { useAuthStore } from '../auth/authStore';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';
import { EntitlementExpiryBanner } from './EntitlementExpiryBanner';

const NOW = new Date('2026-10-26T10:00:00Z');
const DAY = 24 * 60 * 60 * 1000;

function inDays(days: number): string {
  return new Date(NOW.getTime() + days * DAY).toISOString();
}

function entitlement(feature: string, validUntil?: string, enabled = true) {
  return {
    organization_id: 'org-1',
    feature_key: feature,
    plan: 'standard',
    enabled,
    limits: {},
    ...(validUntil ? { valid_until: validUntil } : {}),
    source: 'selfsignup',
    updated_at: NOW.toISOString(),
  };
}

function dateIn(lang: string, days: number): string {
  return new Intl.DateTimeFormat(lang, { dateStyle: 'long' }).format(
    new Date(NOW.getTime() + days * DAY),
  );
}

/** render serves GET /entitlements and waits until the banner has its data. */
async function render(entitlements: unknown[]) {
  const fetchMock = stubFetchRoutes({
    '/entitlements': { data: entitlements, total: entitlements.length, limit: 200 },
  });
  const { queryClient } = renderWithProviders(<EntitlementExpiryBanner />);
  await waitFor(() =>
    expect(queryClient.getQueryState(['entitlements', 'expiry'])?.status).toBe('success'),
  );
  expect(fetchMock.mock.calls.some((call) => String(call[0]).includes('/entitlements'))).toBe(true);
}

beforeEach(() => {
  // Only Date is faked: react-query and waitFor keep their real timers.
  vi.useFakeTimers({ toFake: ['Date'] });
  vi.setSystemTime(NOW);
  useAuthStore.setState({
    token: 'header.payload.signature',
    expiresAt: new Date(NOW.getTime() + 3600_000).toISOString(),
    user: { sub: 'user-1' },
  });
});

afterEach(async () => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  useAuthStore.setState({ token: null, expiresAt: null, user: null });
  await i18n.changeLanguage('de-DE');
});

describe('EntitlementExpiryBanner', () => {
  it('shows nothing 15 days before valid_until', async () => {
    await render([entitlement('discovery', inDays(15))]);
    expect(screen.queryByRole('region', { name: 'Ablaufende Funktionen' })).toBeNull();
  });

  it('warns 14 days before valid_until with feature and date', async () => {
    await render([entitlement('discovery', inDays(14))]);
    const banner = await screen.findByRole('region', { name: 'Ablaufende Funktionen' });
    expect(within(banner).getAllByRole('listitem')).toHaveLength(1);
    expect(banner).toHaveTextContent(
      `Die Funktion „Discovery“ läuft am ${dateIn('de-DE', 14)} ab (in 14 Tagen).`,
    );
  });

  it('warns 1 day before valid_until', async () => {
    await render([entitlement('webhooks', inDays(1))]);
    const banner = await screen.findByRole('region', { name: 'Ablaufende Funktionen' });
    expect(banner).toHaveTextContent(
      `Die Funktion „Webhooks“ läuft am ${dateIn('de-DE', 1)} ab (in 1 Tag).`,
    );
  });

  it('shows nothing without valid_until', async () => {
    await render([entitlement('cmdb_core'), entitlement('discovery')]);
    expect(screen.queryByRole('region', { name: 'Ablaufende Funktionen' })).toBeNull();
  });

  it('leaves out expired and disabled features and lists the others soonest first', async () => {
    await render([
      entitlement('topology', inDays(-1)),
      entitlement('export_csv', inDays(3), false),
      entitlement('discovery', inDays(7)),
      entitlement('webhooks', inDays(1)),
      entitlement('rack_view', inDays(30)),
    ]);
    const banner = await screen.findByRole('region', { name: 'Ablaufende Funktionen' });
    const items = within(banner).getAllByRole('listitem');
    expect(items.map((item) => item.getAttribute('data-feature'))).toEqual([
      'webhooks',
      'discovery',
    ]);
    expect(items[1]).toHaveTextContent(`läuft am ${dateIn('de-DE', 7)} ab (in 7 Tagen)`);
  });

  it('localizes the notice in English', async () => {
    await i18n.changeLanguage('en-US');
    await render([entitlement('rack_view', inDays(7))]);
    const banner = await screen.findByRole('region', { name: 'Expiring features' });
    expect(banner).toHaveTextContent(
      `The feature “Rack view” expires on ${dateIn('en-US', 7)} (in 7 days).`,
    );
  });
});
