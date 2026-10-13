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

  it('shows the state and the causes of failed scope runs (DIS-08)', async () => {
    stubFetchRoutes({
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
          plugins: ['sweep', 'redfish'],
          enabled: true,
          last_run_at: '2026-10-13T08:00:00Z',
          health: {
            state: 'partial',
            errors: [
              {
                cause: 'auth_failed',
                protocol: 'redfish',
                target: '10.0.0.9',
                message: 'HTTP 401: Unauthorized',
              },
            ],
          },
          created_at: '2026-10-01T00:00:00Z',
          updated_at: '2026-10-01T00:00:00Z',
        },
      ]),
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

    expect(await screen.findByText('Teilweise fehlgeschlagen')).toBeInTheDocument();
    expect(screen.getByText('Anmeldung fehlgeschlagen')).toBeInTheDocument();
    expect(screen.getByText(/10\.0\.0\.9/)).toBeInTheDocument();
    expect(screen.getByText(/HTTP 401: Unauthorized/)).toBeInTheDocument();
  });

  it('confirms a suggested scope (INS-06)', async () => {
    const fetchMock = stubFetchRoutes({
      '/review-items?kind=scope_suggested': paginated([
        {
          id: 'r1',
          kind: 'scope_suggested',
          status: 'open',
          payload: { collector_id: 'c1', collector_name: 'collector-a', cidr: '192.168.50.0/24' },
          created_at: '2026-10-13T08:00:00Z',
        },
      ]),
      '/review-items/r1/resolve': { id: 'r1', status: 'resolved' },
      '/discovery-scopes': paginated([]),
      '/collectors': paginated([]),
    });

    renderWithProviders(<DiscoveryPage />, { route: '/discovery' });

    expect(await screen.findByText('192.168.50.0/24')).toBeInTheDocument();
    expect(screen.getByText('gemeldet von collector-a')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Als Scope übernehmen' }));

    await waitFor(() => {
      const calls = fetchMock.mock.calls as unknown as [
        RequestInfo | URL,
        RequestInit | undefined,
      ][];
      const resolve = calls.find(([url]) => String(url).includes('/review-items/r1/resolve'));
      expect(resolve?.[1]?.method).toBe('POST');
      expect(JSON.parse(String(resolve?.[1]?.body))).toEqual({ action: 'accept' });
    });
  });
});
