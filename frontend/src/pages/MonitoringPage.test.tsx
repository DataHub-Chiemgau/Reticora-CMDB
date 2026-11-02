import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import { MonitoringPage } from './MonitoringPage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';

// A cursor page of the list contract (API-02): {data, next_cursor}.
function page(data: unknown[]) {
  return { data, total: data.length, limit: 200, offset: 0, has_more: false };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('MonitoringPage', () => {
  it('renders alert rules', async () => {
    stubFetchRoutes({
      '/monitoring/alerts': page([
        {
          id: 'rule-1',
          org_id: 'org-1',
          name: 'High CPU',
          metric_name: 'cpu_usage',
          condition: 'gt',
          threshold: 90,
          duration: '5m',
          severity: 'critical',
          enabled: true,
        },
      ]),
    });

    renderWithProviders(<MonitoringPage />, { route: '/monitoring' });

    expect(await screen.findByText('High CPU')).toBeInTheDocument();
    expect(screen.getByText('cpu_usage')).toBeInTheDocument();
  });

  it('shows the empty state when no alert rules exist', async () => {
    stubFetchRoutes({ '/monitoring/alerts': page([]) });

    renderWithProviders(<MonitoringPage />, { route: '/monitoring' });

    expect(await screen.findByText('Keine Alarmregeln')).toBeInTheDocument();
  });
});
