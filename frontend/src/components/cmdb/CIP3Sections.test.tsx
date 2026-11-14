import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../../test/utils';
import { useAuthStore } from '../../auth/authStore';
import { CIFindingsSection } from './CIFindingsSection';
import { CIComplianceSection } from './CIComplianceSection';
import { CIRunsSection } from './CIRunsSection';

// The sections Findings, Compliance and Automationen of the CI detail
// (SEC-02, SEC-03, WP-1137) against a BFF mocked over fetch: one query on
// the first expand, a section per module, a refused field hides only its
// section, empty and error states.

const CI = 'ci-1137';
const AT = '2026-11-01T10:00:00Z';

const fullCI = {
  id: CI,
  findings: [
    {
      id: 'f-1',
      severity: 'critical',
      title: 'OpenSSL RCE',
      status: 'open',
      cveIds: ['CVE-2026-0001'],
      kev: true,
      fixedVersion: '3.0.15',
    },
    {
      id: 'f-2',
      severity: 'high',
      title: 'Weak cipher',
      status: 'acknowledged',
      cveIds: [],
      kev: false,
      fixedVersion: null,
    },
  ],
  complianceResults: [
    {
      ruleId: 'r-1',
      ruleName: 'Alpha passes',
      status: 'pass',
      severity: 'low',
      evaluatedAt: AT,
      statusChangedAt: AT,
    },
    {
      ruleId: 'r-2',
      ruleName: 'Zulu fails',
      status: 'fail',
      severity: 'high',
      evaluatedAt: AT,
      statusChangedAt: AT,
    },
  ],
  complianceScore: { score: 66.6, passed: 2, failed: 1, notApplicable: 0 },
  workflowRuns: [
    {
      id: 'run-1',
      workflowId: 'w-1',
      workflowName: 'Rückbau',
      version: 3,
      status: 'succeeded',
      trigger: 'manual',
      startedAt: AT,
      finishedAt: AT,
      createdAt: AT,
    },
  ],
};

interface Setup {
  modules?: { key: string; enabled: boolean }[];
  bff?: () => Response;
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function stub({ modules = [], bff }: Setup = {}) {
  const queries: string[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input.toString();
      if (url.includes('/modules'))
        return json({ data: modules.map((m) => ({ ...m, phase: 'P3', nav: [] })) });
      if (url.includes('/bff/graphql')) {
        queries.push(typeof init?.body === 'string' ? init.body : '');
        return bff ? bff() : json({ data: { ci: fullCI } });
      }
      return json({ detail: 'not found' }, 404);
    }),
  );
  return queries;
}

function renderSections() {
  return renderWithProviders(
    <div>
      <CIFindingsSection ciId={CI} />
      <CIComplianceSection ciId={CI} />
      <CIRunsSection ciId={CI} />
    </div>,
    { route: `/cmdb/${CI}` },
  );
}

function expand(field: string) {
  const section = screen.getByTestId(`ci-section-${field}`) as HTMLDetailsElement;
  section.open = true;
  fireEvent(section, new Event('toggle'));
  return section;
}

beforeEach(() => {
  useAuthStore.setState({
    token: 'header.payload.signature',
    expiresAt: new Date(Date.now() + 3600_000).toISOString(),
    user: { sub: 'user-1' },
  });
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  useAuthStore.setState({ token: null, expiresAt: null, user: null });
});

describe('CI P3 sections', () => {
  it('reads the three sections with one BFF query on first expand', async () => {
    const queries = stub();
    renderSections();
    await waitFor(() => expect(screen.getByTestId('ci-section-findings')).toBeInTheDocument());
    expect(queries).toHaveLength(0);
    const findings = expand('findings');
    expect(await within(findings).findByText('OpenSSL RCE')).toBeInTheDocument();
    expect(within(findings).getByText('CVE-2026-0001')).toBeInTheDocument();
    expect(within(findings).getByText('KEV')).toBeInTheDocument();
    expect(
      within(screen.getByTestId('finding-counts')).getByText('Kritisch: 1'),
    ).toBeInTheDocument();
    expect(within(findings).getByRole('link', { name: 'Alle Findings des CIs' })).toHaveAttribute(
      'href',
      `/findings?ci_id=${CI}`,
    );
    expand('complianceResults');
    expand('workflowRuns');
    expect(await screen.findByText('Zulu fails')).toBeInTheDocument();
    expect(screen.getByText('Rückbau (Version 3)')).toBeInTheDocument();
    expect(queries).toHaveLength(1);
    expect(queries[0]).toContain('findings(first: 50)');
    expect(queries[0]).toContain('complianceScore');
    expect(queries[0]).toContain('workflowRuns(first: 20)');
  });

  it('hides a section when its module is off', async () => {
    stub({ modules: [{ key: 'compliance', enabled: false }] });
    renderSections();
    await waitFor(() => expect(screen.queryByTestId('ci-section-complianceResults')).toBeNull());
    expect(screen.getByTestId('ci-section-findings')).toBeInTheDocument();
    expect(screen.getByTestId('ci-section-workflowRuns')).toBeInTheDocument();
  });

  it('hides only the section whose field returns UNAVAILABLE or FORBIDDEN', async () => {
    stub({
      bff: () =>
        json({
          data: { ci: { ...fullCI, findings: [], workflowRuns: [] } },
          errors: [
            {
              message: 'module off',
              path: ['ci', 'findings'],
              extensions: { code: 'UNAVAILABLE' },
            },
            {
              message: 'forbidden',
              path: ['ci', 'workflowRuns'],
              extensions: { code: 'FORBIDDEN' },
            },
          ],
        }),
    });
    renderSections();
    expand('complianceResults');
    expect(await screen.findByText('Zulu fails')).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByTestId('ci-section-findings')).toBeNull());
    expect(screen.queryByTestId('ci-section-workflowRuns')).toBeNull();
  });

  it('shows the empty state per section', async () => {
    stub({
      bff: () =>
        json({
          data: {
            ci: {
              id: CI,
              findings: [],
              complianceResults: [],
              complianceScore: null,
              workflowRuns: [],
            },
          },
        }),
    });
    renderSections();
    expand('findings');
    expand('complianceResults');
    expand('workflowRuns');
    expect(await screen.findByText('Keine offenen Findings')).toBeInTheDocument();
    expect(screen.getByText('Keine Compliance-Ergebnisse')).toBeInTheDocument();
    expect(screen.getByText('Keine Workflow-Läufe')).toBeInTheDocument();
  });

  it('shows an error with retry and keeps the other sections', async () => {
    let fail = true;
    const queries = stub({
      bff: () => (fail ? json({ detail: 'down' }, 503) : json({ data: { ci: fullCI } })),
    });
    renderSections();
    const findings = expand('findings');
    expect(await within(findings).findByRole('alert')).toHaveTextContent(
      'Die Daten konnten nicht geladen werden.',
    );
    // The other sections stay and can be opened.
    expect(screen.getByTestId('ci-section-workflowRuns')).toBeInTheDocument();
    fail = false;
    fireEvent.click(within(findings).getByRole('button', { name: 'Erneut versuchen' }));
    expect(await within(findings).findByText('OpenSSL RCE')).toBeInTheDocument();
    expand('workflowRuns');
    expect(await screen.findByText('Rückbau (Version 3)')).toBeInTheDocument();
    expect(queries.length).toBe(2);
  });

  it('lists failed compliance results first', async () => {
    stub();
    renderSections();
    expand('complianceResults');
    const list = await screen.findByTestId('compliance-results');
    const items = within(list).getAllByRole('listitem');
    expect(items[0]).toHaveTextContent('Zulu fails');
    expect(items[1]).toHaveTextContent('Alpha passes');
    expect(screen.getByText('67%')).toBeInTheDocument();
  });

  it('links a run to the run view', async () => {
    stub();
    renderSections();
    expand('workflowRuns');
    expect(await screen.findByRole('link', { name: 'Rückbau (Version 3)' })).toHaveAttribute(
      'href',
      '/workflows/runs/run-1',
    );
  });

  it('shows no start for a run that has not started', async () => {
    stub({
      bff: () =>
        json({
          data: {
            ci: {
              ...fullCI,
              workflowRuns: [
                {
                  ...fullCI.workflowRuns[0],
                  status: 'pending',
                  startedAt: null,
                  finishedAt: null,
                },
              ],
            },
          },
        }),
    });
    renderSections();
    const runs = expand('workflowRuns');
    expect(await within(runs).findByText(/Start –, Ende –/)).toBeInTheDocument();
    expect(within(runs).getByText(/erstellt /)).toBeInTheDocument();
  });

  it('marks the counts as a lower bound when the findings page is full', async () => {
    const many = Array.from({ length: 50 }, (_, i) => ({
      ...fullCI.findings[1],
      id: `f-${i}`,
      title: `Finding ${i}`,
    }));
    stub({ bff: () => json({ data: { ci: { ...fullCI, findings: many } } }) });
    renderSections();
    const findings = expand('findings');
    expect(
      await within(screen.getByTestId('ci-section-findings')).findByText('Hoch: 50+'),
    ).toBeInTheDocument();
    expect(within(findings).getByText(/Die ersten 50 Findings/)).toBeInTheDocument();
  });
});
