import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { Permission } from '../api/client';
import { PermissionsPage } from '../pages/PermissionsPage';
import { UserManagementPage } from '../pages/UserManagementPage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';
import { ScrollRegion } from './ScrollRegion';

// WP-631 (F-357): a table that scrolls sideways on a phone lies in a
// focusable, labelled region, so the keyboard reaches its right columns
// (axe scrollable-region-focusable).

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

function list<T>(data: T[]) {
  return { data, total: data.length, limit: 50, offset: 0, has_more: false };
}

describe('ScrollRegion', () => {
  it('is a labelled region that takes the focus with Tab', async () => {
    render(
      <>
        <button type="button">before</button>
        <ScrollRegion label="Users">
          <table>
            <tbody>
              <tr>
                <td>cell</td>
              </tr>
            </tbody>
          </table>
        </ScrollRegion>
      </>,
    );

    const region = screen.getByRole('region', { name: 'Users' });
    expect(region).toHaveAttribute('tabindex', '0');
    expect(region).toHaveClass('overflow-x-auto');
    expect(within(region).getByRole('table')).toBeInTheDocument();

    const user = userEvent.setup();
    screen.getByRole('button', { name: 'before' }).focus();
    await user.tab();
    expect(region).toHaveFocus();
  });

  it('keeps extra classes beside the scroll behaviour', () => {
    render(
      <ScrollRegion label="Roles" className="mt-2">
        <p>content</p>
      </ScrollRegion>,
    );
    const region = screen.getByRole('region', { name: 'Roles' });
    expect(region).toHaveClass('overflow-x-auto');
    expect(region).toHaveClass('mt-2');
  });

  it('wraps the user table of the user management page', async () => {
    stubFetchRoutes({
      '/users': list([
        {
          id: 'u-1',
          organization_id: 'org-1',
          email: 'ada@example.test',
          display_name: 'Ada Admin',
          status: 'active',
          created_at: '2026-10-01T00:00:00Z',
          updated_at: '2026-10-01T00:00:00Z',
        },
      ]),
      '/me/permissions': { permissions: [] },
    });

    renderWithProviders(<UserManagementPage />, { route: '/users' });

    const region = await screen.findByRole('region', { name: 'Benutzer' });
    expect(region).toHaveAttribute('tabindex', '0');
    expect(await within(region).findByText('Ada Admin')).toBeInTheDocument();
  });

  it('wraps the permission catalogue', async () => {
    const permission: Permission = {
      key: 'ci.read',
      resource: 'ci',
      action: 'read',
      description: 'Read configuration items',
    };
    stubFetchRoutes({
      '/me/permissions': { permissions: ['ci.read'] },
      '/permissions': list([permission]),
    });

    renderWithProviders(<PermissionsPage />, { route: '/permissions' });

    const region = await screen.findByRole('region', { name: 'Berechtigungskatalog' });
    expect(region).toHaveAttribute('tabindex', '0');
    expect(within(region).getByText('ci.read')).toBeInTheDocument();
  });
});
