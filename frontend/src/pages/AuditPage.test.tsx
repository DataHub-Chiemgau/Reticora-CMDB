import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import type { components } from '../api/generated/schema';
import { AuditPage } from './AuditPage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';

// The fixtures are typed with the generated OpenAPI schema, so they have the
// shape the backend sends (timestamp, resource_type, resource_id; F-078).
type AuditEntry = components['schemas']['AuditEntry'];

const TIMESTAMP = '2026-08-12T06:00:00Z';
const RESOURCE_ID = '0a0a0a0a-0000-4000-8000-000000000133';
const CI_ID = '0a0a0a0a-0000-4000-8000-000000000469';

const vrfEntry: AuditEntry = {
  id: 'entry-133',
  organization_id: 'org-1',
  seq: 7,
  timestamp: TIMESTAMP,
  actor_id: 'user-1',
  actor_type: 'user',
  action: 'vrfs.create',
  resource_type: 'vrfs',
  resource_id: RESOURCE_ID,
  source: 'api',
  hash: 'h',
};

const ciEntry: AuditEntry = {
  id: 'entry-1',
  organization_id: 'org-1',
  seq: 8,
  timestamp: TIMESTAMP,
  actor_id: 'user-1',
  actor_type: 'user',
  action: 'ci.updated',
  resource_type: 'ci',
  resource_id: CI_ID,
  source: 'ui',
  previous_hash: 'h',
  hash: 'h2',
};

const systemEntry: AuditEntry = {
  id: 'entry-2',
  organization_id: 'org-1',
  seq: 9,
  timestamp: TIMESTAMP,
  actor_type: 'system',
  action: 'retention.purge',
  resource_type: 'organization',
  source: 'system',
  previous_hash: 'h2',
  hash: 'h3',
};

function auditPage(data: AuditEntry[]) {
  return { data, total: data.length, limit: 50, offset: 0, has_more: false };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('AuditPage', () => {
  it('shows the entry time and the affected resource', async () => {
    stubFetchRoutes({ '/audit-log': auditPage([vrfEntry]) });

    renderWithProviders(<AuditPage />, { route: '/audit' });

    expect(await screen.findByText('vrfs.create')).toBeInTheDocument();
    expect(screen.queryByText('Invalid Date')).not.toBeInTheDocument();
    // The time is localized in the UI language (de-DE in the tests).
    expect(screen.getByText(new Date(TIMESTAMP).toLocaleString('de-DE'))).toBeInTheDocument();
    expect(screen.getByText(`vrfs/${RESOURCE_ID}`)).toBeInTheDocument();
    expect(screen.getByText('user-1')).toBeInTheDocument();
  });

  it('lists audit entries with actor, action and a link to the changed CI', async () => {
    stubFetchRoutes({ '/audit-log': auditPage([ciEntry, systemEntry]) });

    renderWithProviders(<AuditPage />, { route: '/audit' });

    expect(await screen.findByText('ci.updated')).toBeInTheDocument();
    const rows = screen.getAllByRole('row').slice(1);
    expect(rows).toHaveLength(2);

    const ciCells = rows[0]!.querySelectorAll('td');
    expect(ciCells[0]).toHaveTextContent(/^\d{1,2}\.\d{1,2}\.\d{4}/);
    expect(ciCells[1]).toHaveTextContent('user-1');
    expect(ciCells[2]).toHaveTextContent('ci.updated');
    const link = screen.getByRole('link', { name: `ci/${CI_ID}` });
    expect(link).toHaveAttribute('href', `/cmdb/${CI_ID}`);

    // An entry without actor id names the actor type; one without resource id
    // names the resource type and links nowhere.
    const systemCells = rows[1]!.querySelectorAll('td');
    expect(systemCells[1]).toHaveTextContent('system');
    expect(systemCells[3]).toHaveTextContent(/^organization$/);
    expect(systemCells[3]?.querySelector('a')).toBeNull();
    expect(screen.queryByText('Invalid Date')).not.toBeInTheDocument();
  });

  it('shows an intact result after verification', async () => {
    stubFetchRoutes({
      '/audit-log/verify': { intact: true, checked: 42 },
      '/audit-log': { data: [], total: 0, limit: 50, offset: 0, has_more: false },
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
      '/audit-log/verify': {
        intact: false,
        checked: 17,
        broken_id: 'entry-99',
        broken_at: 18,
        broken_reason: 'hash mismatch',
      },
      '/audit-log': { data: [], total: 0, limit: 50, offset: 0, has_more: false },
    });

    renderWithProviders(<AuditPage />, { route: '/audit' });
    fireEvent.click(await screen.findByRole('button', { name: /verifizieren/i }));

    await waitFor(() => {
      expect(screen.getByText('Unterbrochen')).toBeInTheDocument();
    });
    expect(screen.getByText(/entry-99/)).toBeInTheDocument();
  });
});
