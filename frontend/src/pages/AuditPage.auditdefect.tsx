/**
 * Phase-1 acceptance audit, group T6-epic-b – DEFECT test (fails until fixed).
 *
 * WP-133 (AUD-05, AUD-07)
 * Finding P2 "Audit viewer shows 'Invalid Date' and no object for every entry
 * (frontend type does not match the API)": AuditPage renders created_at /
 * entity_type / entity_id / actor_email from a hand-written type, while the API
 * (OpenAPI AuditEntry) returns timestamp / resource_type / resource_id.
 * The fixture below is typed with the generated OpenAPI schema, so it has the
 * shape the real backend sends.
 *
 * Retest:
 *   cd frontend && npx vitest run -c vitest.auditdefect.config.ts src/pages/AuditPage.auditdefect.tsx
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import type { components } from '../api/generated/schema';
import { AuditPage } from './AuditPage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';

type AuditEntry = components['schemas']['AuditEntry'];

const TIMESTAMP = '2026-08-12T06:00:00Z';
const RESOURCE_ID = '0a0a0a0a-0000-4000-8000-000000000133';

const entry: AuditEntry = {
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

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('AuditPage renders the API audit entry (audit defect)', () => {
  it('shows the entry time and the affected resource', async () => {
    stubFetchRoutes({
      '/audit-log': { data: [entry], total: 1, limit: 50, offset: 0, has_more: false },
    });

    renderWithProviders(<AuditPage />, { route: '/audit' });

    expect(await screen.findByText('vrfs.create')).toBeInTheDocument();
    expect(screen.queryByText('Invalid Date')).not.toBeInTheDocument();
    expect(screen.getByText(new Date(TIMESTAMP).toLocaleString())).toBeInTheDocument();
    expect(screen.getByText(`vrfs/${RESOURCE_ID}`)).toBeInTheDocument();
  });
});
