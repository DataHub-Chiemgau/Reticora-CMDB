import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import { WorkflowPage } from './WorkflowPage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';
function paginated<T>(data: T[]) {
  return { data, total: data.length, limit: 50, offset: 0, has_more: false };
}
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});
describe('WorkflowPage', () => {
  it('renders workflow definitions and approval runs', async () => {
    stubFetchRoutes({
      '/workflows': paginated([
        {
          id: 'wf-1',
          organization_id: 'org-1',
          name: 'Onboarding',
          trigger: { type: 'manual' },
          conditions: [],
          actions: [],
          active: true,
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:00:00Z',
        },
      ]),
      '/workflow-runs': paginated([
        {
          id: 'run-1',
          organization_id: 'org-1',
          workflow_id: 'wf-1',
          status: 'waiting_approval',
          trigger: 'manual',
          context: {},
          started_at: '2026-01-01T00:00:00Z',
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:00:00Z',
          steps: [],
        },
      ]),
    });
    renderWithProviders(<WorkflowPage />, { route: '/workflows' });
    expect(await screen.findByText('Onboarding')).toBeInTheDocument();
    expect(screen.getByText('Freigeben')).toBeInTheDocument();
  });
});
