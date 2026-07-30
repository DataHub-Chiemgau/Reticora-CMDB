import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import { CompliancePage } from './CompliancePage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';
function paginated<T>(data: T[]) {
  return { data, total: data.length, limit: 50, offset: 0, has_more: false };
}
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});
describe('CompliancePage', () => {
  it('renders score and failing results', async () => {
    stubFetchRoutes({
      '/compliance/rules': paginated([
        {
          id: 'rule-1',
          organization_id: 'org-1',
          name: 'Encrypted',
          severity: 'high',
          category: 'ISO27001',
          expression: {},
          active: true,
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:00:00Z',
        },
      ]),
      '/compliance/results': paginated([
        {
          id: 'result-1',
          organization_id: 'org-1',
          rule_id: 'rule-1',
          ci_id: 'ci-1',
          ci_type_id: 'server',
          status: 'fail',
          details: 'Enable encryption',
          evaluated_at: '2026-01-01T00:00:00Z',
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:00:00Z',
        },
      ]),
      '/compliance/score': {
        overall: { passed: 1, failed: 1, not_applicable: 0, score: 50 },
        by_ci_type: [],
        results: [],
      },
    });
    renderWithProviders(<CompliancePage />, { route: '/compliance' });
    expect(await screen.findByText('50%')).toBeInTheDocument();
    expect(screen.getByText('Enable encryption')).toBeInTheDocument();
  });
});
