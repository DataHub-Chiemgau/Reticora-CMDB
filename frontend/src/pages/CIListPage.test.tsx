import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen } from '@testing-library/react';
import { CIListPage } from './CIListPage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';

const ci = {
  id: 'ci-1',
  organization_id: 'org-1',
  ci_type_id: 'switch',
  name: 'core-sw-01',
  status: 'active',
  manufacturer: 'Acme',
  attributes: {},
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
};

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('CIListPage', () => {
  it('links every row to the deep-linkable detail page', async () => {
    stubFetchRoutes({
      '/cis': { data: [ci], total: 1, limit: 25, offset: 0, has_more: false },
    });

    renderWithProviders(<CIListPage onCreateCI={() => {}} />, { route: '/cmdb' });

    expect(await screen.findByRole('link', { name: 'core-sw-01' })).toHaveAttribute(
      'href',
      '/cmdb/ci-1',
    );
  });

  it('offers the create action from the empty state', async () => {
    stubFetchRoutes({
      '/cis': { data: [], total: 0, limit: 25, offset: 0, has_more: false },
    });
    const onCreateCI = vi.fn();

    renderWithProviders(<CIListPage onCreateCI={onCreateCI} />, { route: '/cmdb' });

    expect(await screen.findByText('Noch keine Configuration Items')).toBeInTheDocument();
    const createButtons = screen.getAllByRole('button', { name: 'CI erstellen' });
    fireEvent.click(createButtons[createButtons.length - 1]!);
    expect(onCreateCI).toHaveBeenCalledTimes(1);
  });
});
