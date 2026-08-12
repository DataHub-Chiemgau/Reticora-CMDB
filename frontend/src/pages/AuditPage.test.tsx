import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { AuditPage } from './AuditPage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('AuditPage', () => {
  it('lists audit entries', async () => {
    stubFetchRoutes({
      '/audit': {
        data: [
          {
            id: 'entry-1',
            organization_id: 'org-1',
            actor_email: 'admin@reticora.local',
            action: 'ci.update',
            entity_type: 'ci',
            entity_id: 'ci-1',
            created_at: '2026-08-12T06:00:00Z',
          },
        ],
        total: 1,
        limit: 50,
        offset: 0,
        has_more: false,
      },
    });

    renderWithProviders(<AuditPage />, { route: '/audit' });

    expect(await screen.findByText('admin@reticora.local')).toBeInTheDocument();
    expect(screen.getByText('ci.update')).toBeInTheDocument();
  });

  it('shows an intact result after verification', async () => {
    stubFetchRoutes({
      '/audit/verify': { intact: true, checked: 42 },
      '/audit': { data: [], total: 0, limit: 50, offset: 0, has_more: false },
    });

    renderWithProviders(<AuditPage />, { route: '/audit' });
    fireEvent.click(await screen.findByRole('button', { name: /verifizieren/i }));

    await waitFor(() => {
      expect(screen.getByText('Intakt')).toBeInTheDocument();
    });
    expect(screen.getByText(/42/)).toBeInTheDocument();
  });

  it('surfaces a broken chain with its position', async () => {
    stubFetchRoutes({
      '/audit/verify': {
        intact: false,
        checked: 17,
        broken_id: 'entry-99',
        broken_at: 18,
        broken_reason: 'hash mismatch',
      },
      '/audit': { data: [], total: 0, limit: 50, offset: 0, has_more: false },
    });

    renderWithProviders(<AuditPage />, { route: '/audit' });
    fireEvent.click(await screen.findByRole('button', { name: /verifizieren/i }));

    await waitFor(() => {
      expect(screen.getByText('Unterbrochen')).toBeInTheDocument();
    });
    expect(screen.getByText(/entry-99/)).toBeInTheDocument();
  });
});
