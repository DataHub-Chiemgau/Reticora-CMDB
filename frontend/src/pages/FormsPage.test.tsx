import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import { FormsPage } from './FormsPage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';
function paginated<T>(data: T[]) {
  return { data, total: data.length, limit: 50, offset: 0, has_more: false };
}
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});
describe('FormsPage', () => {
  it('renders a form definition and fields', async () => {
    stubFetchRoutes({
      '/forms': paginated([
        {
          id: 'form-1',
          organization_id: 'org-1',
          name: 'Access request',
          schema: {
            properties: { email: { type: 'string', title: 'E-Mail' } },
            required: ['email'],
          },
          ui_hints: {},
          active: true,
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:00:00Z',
        },
      ]),
    });
    renderWithProviders(<FormsPage />, { route: '/forms' });
    expect(await screen.findByText('Access request')).toBeInTheDocument();
    expect(screen.getByLabelText('E-Mail')).toBeInTheDocument();
  });
});
