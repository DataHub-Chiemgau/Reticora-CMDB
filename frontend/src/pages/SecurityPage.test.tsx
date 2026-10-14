import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import { SecurityPage } from './SecurityPage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';
import { useAuthStore } from '../auth/authStore';

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  useAuthStore.setState({ token: null, expiresAt: null, user: null });
});

const reportFixture = {
  generated_at: '2026-08-12T10:00:00Z',
  standard: 'iso27001',
  audit_integrity: { intact: true, checked: 42 },
  compliance_score: { passed: 8, failed: 2, not_applicable: 0, score: 80 },
  failures_by_severity: { high: 2 },
  findings: [
    {
      rule_id: 'rule-1',
      rule_name: 'encryption at rest',
      severity: 'high',
      category: 'ISO27001',
      remediation_hint: 'Enable encryption',
      affected_cis: ['ci-1', 'ci-2'],
    },
  ],
  capabilities: [{ feature_key: 'compliance', enabled: true }],
};

describe('SecurityPage', () => {
  it('renders the security report with findings', async () => {
    stubFetchRoutes({
      '/compliance/report?standard=iso27001': reportFixture,
      '/privacy/retention': { status: 404 },
    });

    renderWithProviders(<SecurityPage />, { route: '/security' });

    expect(await screen.findByText('encryption at rest')).toBeInTheDocument();
    expect(screen.getByText('80%')).toBeInTheDocument();
    expect(screen.getByText('Intakt')).toBeInTheDocument();
  });

  it('keeps the DSGVO controls without the compliance module (WP-214)', async () => {
    useAuthStore.setState({
      token: 'header.payload.signature',
      expiresAt: new Date(Date.now() + 3600_000).toISOString(),
      user: { sub: 'user-1' },
    });
    const fetchMock = stubFetchRoutes({
      '/modules': {
        data: [{ key: 'compliance', phase: 'P3', nav: ['compliance'], enabled: false }],
      },
      '/privacy/retention': { status: 404 },
    });

    renderWithProviders(<SecurityPage />, { route: '/security' });

    await waitFor(() =>
      expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/modules'))).toBe(true),
    );
    expect(await screen.findByText('Aufbewahrung (DSGVO)')).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByText('Integrität der Audit-Kette')).toBeNull());
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/compliance/report'))).toBe(
      false,
    );
  });
});
