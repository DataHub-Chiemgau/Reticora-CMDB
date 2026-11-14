import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
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

  it('generates a report as a job, follows its status and downloads it (WP-1135)', async () => {
    let status = 'running';
    let scan = 'pending_scan';
    const open = vi.spyOn(window, 'open').mockImplementation(() => null);
    const calls: { method: string; url: string; body?: unknown }[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = typeof input === 'string' ? input : input.toString();
        const method = (init?.method ?? 'GET').toUpperCase();
        calls.push({
          method,
          url,
          body: typeof init?.body === 'string' ? JSON.parse(init.body) : undefined,
        });
        const reply = (body: unknown, code = 200) =>
          new Response(JSON.stringify(body), {
            status: code,
            headers: { 'Content-Type': 'application/json' },
          });
        if (method === 'POST' && url.includes('/compliance/reports'))
          return reply(
            { id: 'job-7', kind: 'compliance.report', status: 'queued', progress: 0 },
            202,
          );
        if (url.includes('/jobs/job-7'))
          return reply({
            id: 'job-7',
            kind: 'compliance.report',
            status,
            progress: status === 'running' ? 30 : 100,
            result: status === 'succeeded' ? { document_id: 'doc-7' } : undefined,
          });
        if (url.includes('/documents/doc-7/content'))
          return reply({ download_url: 'https://blob.example/report.pdf?sig=1' });
        if (url.includes('/compliance/reports')) {
          // The report enters the list when its job has ended.
          const data =
            status === 'succeeded'
              ? [
                  {
                    document_id: 'doc-7',
                    title: 'ISO 27001',
                    file_name: 'iso27001.csv',
                    standard: 'iso27001',
                    format: 'csv',
                    scope: 'organization',
                    scan_status: scan,
                    created_at: '2026-11-14T08:00:00Z',
                  },
                ]
              : [];
          return reply({ data, total: data.length, limit: 50, offset: 0, has_more: false });
        }
        if (url.includes('/compliance/report?')) return reply(reportFixture);
        if (url.includes('/clients'))
          return reply({
            data: [{ id: 'cl-1', name: 'Kunde A' }],
            total: 1,
            limit: 50,
            offset: 0,
            has_more: false,
          });
        return reply({ detail: 'not found' }, 404);
      }),
    );

    renderWithProviders(<SecurityPage />, { route: '/security' });

    fireEvent.change(await screen.findByLabelText('Format'), { target: { value: 'csv' } });
    fireEvent.click(screen.getByRole('button', { name: 'Report erzeugen' }));
    expect(await screen.findByText(/Report: .*\(30 %\)/)).toBeInTheDocument();
    expect(calls.find((c) => c.method === 'POST')?.body).toEqual({
      standard: 'iso27001',
      format: 'csv',
    });
    status = 'succeeded';
    // The job's end reloads the list; the download waits for a clean scan.
    expect(
      await screen.findByText('iso27001.csv', undefined, { timeout: 5000 }),
    ).toBeInTheDocument();
    expect(screen.getByText(/wird auf Schadsoftware geprüft/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Herunterladen' })).toBeNull();
    scan = 'clean';
    const download = await screen.findByRole(
      'button',
      { name: 'Herunterladen' },
      { timeout: 8000 },
    );
    fireEvent.click(download);
    await waitFor(() =>
      expect(open).toHaveBeenCalledWith(
        'https://blob.example/report.pdf?sig=1',
        '_blank',
        'noopener',
      ),
    );
    // The JSON preview stays as a collapsible section.
    expect(screen.getByTestId('report-preview').tagName).toBe('DETAILS');
  }, 20000);
});
