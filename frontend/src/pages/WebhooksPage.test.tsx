import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import { WebhooksPage } from './WebhooksPage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';

function paginated<T>(data: T[]) {
  return { data, total: data.length, limit: 50, offset: 0, has_more: false };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('WebhooksPage', () => {
  it('renders subscriptions and an empty dead-letter queue', async () => {
    stubFetchRoutes({
      '/webhooks/dead-letters': paginated([]),
      '/webhooks': paginated([
        {
          id: 'wh-1',
          organization_id: 'org-1',
          name: 'Ticket hook',
          url: 'https://example.com/hook',
          events: ['ci.created'],
          is_active: true,
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:00:00Z',
        },
      ]),
    });

    renderWithProviders(<WebhooksPage />, { route: '/webhooks' });

    expect(await screen.findByText('Ticket hook')).toBeInTheDocument();
    expect(screen.getByText('Keine Dead Letters')).toBeInTheDocument();
  });

  it('shows the empty state when no subscriptions exist', async () => {
    stubFetchRoutes({
      '/webhooks/dead-letters': paginated([]),
      '/webhooks': paginated([]),
    });

    renderWithProviders(<WebhooksPage />, { route: '/webhooks' });

    expect(await screen.findByText('Keine Webhooks vorhanden')).toBeInTheDocument();
  });
});
