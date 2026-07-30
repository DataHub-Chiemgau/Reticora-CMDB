import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import { SLAPage } from './SLAPage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';

function paginated<T>(data: T[]) {
  return { data, total: data.length, limit: 50, offset: 0, has_more: false };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('SLAPage', () => {
  it('renders SLA policies and empty breach state', async () => {
    stubFetchRoutes({
      '/slas/breaches': paginated([]),
      '/slas': paginated([
        {
          id: 'sla-1',
          organization_id: 'org-1',
          name: 'High priority',
          priority: 'high',
          response_target_minutes: 30,
          resolution_target_minutes: 240,
          business_calendar: false,
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:00:00Z',
        },
      ]),
    });

    renderWithProviders(<SLAPage />, { route: '/slas' });

    expect(await screen.findByText('High priority')).toBeInTheDocument();
    expect(screen.getByText('Keine Verletzungen')).toBeInTheDocument();
  });
});
