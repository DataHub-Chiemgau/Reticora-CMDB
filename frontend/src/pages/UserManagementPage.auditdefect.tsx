/**
 * Phase-1 acceptance audit, group T6-epic-b – DEFECT test (fails until fixed).
 *
 * WP-114 (TLC-03 invitations: issue, accept, renew; AUT-01 first login with
 * an invitation)
 * Finding P2 "No UI for issuing, renewing or accepting invitations":
 * UserManagementPage only lists users, teams and roles; no code in
 * frontend/src mentions invitations, so an org admin can invite colleagues
 * only through raw REST (POST /api/v1/invitations).
 * Encoded requirement: user management offers an action to invite a user.
 * (The public accept page is not asserted here; see not_codified.)
 *
 * Retest:
 *   cd frontend && npx vitest run -c vitest.auditdefect.config.ts src/pages/UserManagementPage.auditdefect.tsx
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';
import { UserManagementPage } from './UserManagementPage';

const emptyList = { data: [], total: 0, limit: 50, offset: 0, has_more: false };

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('UserManagementPage invitations (audit defect)', () => {
  it('offers an action to invite a user', async () => {
    stubFetchRoutes({
      '/users': {
        data: [
          {
            id: 'u-1',
            email: 'admin@example.invalid',
            display_name: 'Admin',
            status: 'active',
            is_active: true,
          },
        ],
        total: 1,
        limit: 50,
        offset: 0,
        has_more: false,
      },
      '/teams': emptyList,
      '/roles': emptyList,
    });

    renderWithProviders(<UserManagementPage />, { route: '/users' });

    expect(await screen.findByText('admin@example.invalid')).toBeInTheDocument();
    const invite = [...screen.queryAllByRole('button'), ...screen.queryAllByRole('link')].filter(
      (el) =>
        /einlad|invit/i.test(el.textContent ?? '') ||
        /einlad|invit/i.test(el.getAttribute('aria-label') ?? ''),
    );
    expect(invite.length).toBeGreaterThan(0);
  });
});
