import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import { IGAPage } from './IGAPage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';

function paginated<T>(data: T[]) {
  return { data, total: data.length, limit: 50, offset: 0, has_more: false };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('IGAPage', () => {
  it('renders connectors, tasks and empty review states', async () => {
    stubFetchRoutes({
      '/iga/connectors': paginated([
        {
          id: 'c1',
          organization_id: 'org',
          name: 'HR SCIM',
          type: 'scim',
          capabilities: {},
          status: 'active',
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:00:00Z',
        },
      ]),
      '/iga/tasks': paginated([
        {
          id: 't1',
          connector_id: 'c1',
          action: 'create_account',
          status: 'pending',
          attempts: 0,
          max_attempts: 3,
          next_run_at: '2026-01-01T00:00:00Z',
          created_at: '2026-01-01T00:00:00Z',
        },
      ]),
      '/iga/access-requests': paginated([]),
      '/iga/access-reviews': paginated([]),
      '/iga/drift': paginated([]),
    });

    renderWithProviders(<IGAPage />, { route: '/iga' });

    expect(await screen.findByText('HR SCIM')).toBeInTheDocument();
    expect(screen.getByText('create_account')).toBeInTheDocument();
    expect(screen.getByText('Keine Review-Kampagnen')).toBeInTheDocument();
  });
});
