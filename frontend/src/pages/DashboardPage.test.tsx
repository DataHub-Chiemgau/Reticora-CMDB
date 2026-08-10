import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import { DashboardPage } from './DashboardPage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';

function paginated<T>(data: T[]) {
  return { data, total: data.length, limit: 50, offset: 0, has_more: false };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
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
          status: 'online',
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
  });

  it('does not crash when a list endpoint returns data:null (legacy/empty backend)', async () => {
    stubFetchRoutes({
      '/cis': { data: null, total: 0, limit: 1, offset: 0, has_more: false },
      '/collectors': { data: null, total: 0, limit: 1000, offset: 0, has_more: false },
    });

    renderWithProviders(<DashboardPage />, { route: '/dashboard' });

    // The dashboard heading must render even though the payloads carried null data.
    expect(
      await screen.findByText('Schneller Überblick über Ihre Konfigurationsdaten.'),
    ).toBeInTheDocument();
    expect(screen.getByText('Collector online')).toBeInTheDocument();
  });
});
