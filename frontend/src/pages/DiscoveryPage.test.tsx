import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { DiscoveryPage } from './DiscoveryPage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';

function paginated<T>(data: T[]) {
  return { data, total: data.length, limit: 100, offset: 0, has_more: false };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('DiscoveryPage scopes (DIS-09)', () => {
  it('lists the scopes of a collector and creates a new one', async () => {
    const fetchMock = stubFetchRoutes({
      '/discovery-scopes?collector_id=c1': paginated([
        {
          id: 's1',
          organization_id: 'org-1',
          collector_id: 'c1',
          name: '10.0.0.0/24',
          kind: 'cidr',
          target: '10.0.0.0/24',
          schedule: '24h',
          credential_ids: [],
          plugins: ['sweep', 'snmp'],
          enabled: true,
          created_at: '2026-10-01T00:00:00Z',
          updated_at: '2026-10-01T00:00:00Z',
        },
      ]),
      '/discovery-scopes': { id: 's2' },
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

    renderWithProviders(<DiscoveryPage />, { route: '/discovery' });

    expect(await screen.findByText('10.0.0.0/24')).toBeInTheDocument();
    expect(screen.getByText(/sweep, snmp/)).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText('Ziel'), { target: { value: '192.168.1.0/24' } });
    fireEvent.click(screen.getByRole('button', { name: 'Scope hinzufügen' }));

    await waitFor(() => {
      const calls = fetchMock.mock.calls as unknown as [
        RequestInfo | URL,
        RequestInit | undefined,
      ][];
      const post = calls.find(
        ([url, init]) => String(url).endsWith('/discovery-scopes') && init?.method === 'POST',
      );
      expect(post).toBeDefined();
      expect(JSON.parse(String(post?.[1]?.body))).toEqual({
        collector_id: 'c1',
        kind: 'cidr',
        target: '192.168.1.0/24',
        schedule: '24h',
      });
    });
  });
});
