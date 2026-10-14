import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
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

  it('retries a dead delivery (WHK-01)', async () => {
    const fetchMock = stubFetchRoutes({
      '/webhooks/dead-letters': paginated([]),
      '/webhooks/wh-1/deliveries/dl-1/retry': { id: 'dl-1', status: 'pending' },
      '/webhooks/wh-1/deliveries': paginated([
        {
          id: 'dl-1',
          organization_id: 'org-1',
          subscription_id: 'wh-1',
          event: 'ci.created',
          status: 'dead',
          attempt: 6,
          max_attempts: 6,
          error: 'unexpected status 502',
          created_at: '2026-01-01T00:00:00Z',
        },
      ]),
      '/webhooks': paginated([
        {
          id: 'wh-1',
          organization_id: 'org-1',
          name: 'Ticket hook',
          url: 'https://example.com/hook',
          events: ['ci.created'],
          is_active: false,
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:00:00Z',
        },
      ]),
    });

    renderWithProviders(<WebhooksPage />, { route: '/webhooks' });
    fireEvent.click(await screen.findByRole('button', { name: 'Zustellverlauf' }));
    expect(await screen.findByText('aufgegeben')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Erneut senden' }));
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(([url]) =>
          String(url).includes('/webhooks/wh-1/deliveries/dl-1/retry'),
        ),
      ).toBe(true),
    );
  });
});
