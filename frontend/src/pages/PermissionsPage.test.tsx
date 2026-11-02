import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import { PermissionsPage } from './PermissionsPage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';

function permission(key: string) {
  const [resource, action] = key.split(':');
  return { key, resource, action, description: `${resource} ${action}` };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('PermissionsPage', () => {
  it('renders the catalogue and highlights effective permissions', async () => {
    stubFetchRoutes({
      '/me/permissions': { user_id: 'user-1', permissions: ['ci:read'] },
      '/permissions': {
        data: [permission('ci:read'), permission('ticket:write')],
        total: 2,
        limit: 200,
        offset: 0,
        has_more: false,
      },
    });

    renderWithProviders(<PermissionsPage />, { route: '/permissions' });

    expect(await screen.findByText('ci:read')).toBeInTheDocument();
    expect(screen.getByText('ticket:write')).toBeInTheDocument();
    expect(screen.getByText('Gewährt')).toBeInTheDocument();
  });
});
