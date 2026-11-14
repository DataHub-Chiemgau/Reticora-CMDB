import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MonitoringPage } from './MonitoringPage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';

// A cursor page of the list contract (API-02): {data, next_cursor}.
function page(data: unknown[]) {
  return { data, total: data.length, limit: 200, offset: 0, has_more: false };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

// A rule with a critical and a warning stage of single comparisons.
const stagedRule = {
  id: 'rule-2',
  org_id: 'org-1',
  name: 'Load',
  metric_name: 'cpu.load',
  duration: '5m',
  enabled: true,
  critical_condition: { op: 'gt', field: 'value', value: 90 },
  warning_condition: { op: 'gte', field: 'avg', value: 70 },
  info_condition: null,
  clear_condition: { op: 'lt', field: 'value', value: 60 },
  eval_window: '5m',
  scope: { client_id: 'client-1' },
  group_by: [],
  actions: { notify: { enabled: true }, webhook: { enabled: false } },
};

describe('MonitoringPage', () => {
  it('renders alert rules', async () => {
    stubFetchRoutes({
      '/monitoring/alerts': page([
        {
          id: 'rule-1',
          org_id: 'org-1',
          name: 'High CPU',
          metric_name: 'cpu_usage',
          condition: 'gt',
          threshold: 90,
          duration: '5m',
          severity: 'critical',
          enabled: true,
        },
      ]),
    });

    renderWithProviders(<MonitoringPage />, { route: '/monitoring' });

    expect(await screen.findByText('High CPU')).toBeInTheDocument();
    expect(screen.getByText('cpu_usage')).toBeInTheDocument();
  });

  it('shows the empty state when no alert rules exist', async () => {
    stubFetchRoutes({ '/monitoring/alerts': page([]) });

    renderWithProviders(<MonitoringPage />, { route: '/monitoring' });

    expect(await screen.findByText('Keine Alarmregeln')).toBeInTheDocument();
  });

  it('lists the stages, scope and actions of a rule (WP-990 AK4)', async () => {
    stubFetchRoutes({
      '/monitoring/alerts': page([
        {
          ...stagedRule,
          scope: { client_id: 'client-1', ci_ids: ['ci-1', 'ci-2'], labels: { env: 'prod' } },
        },
      ]),
      '/clients': page([{ id: 'client-1', name: 'Client One' }]),
    });
    renderWithProviders(<MonitoringPage />, { route: '/monitoring' });
    const row = (await screen.findByText('Load')).closest('tr')!;
    expect(within(row).getByText('critical: value > 90')).toBeInTheDocument();
    expect(within(row).getByText('warning: avg ≥ 70')).toBeInTheDocument();
    expect(within(row).getByText('clear: value < 60')).toBeInTheDocument();
    expect(await within(row).findByText('Mandant: Client One')).toBeInTheDocument();
    expect(within(row).getByText('CIs: 2')).toBeInTheDocument();
    expect(within(row).getByText('Labels: env=prod')).toBeInTheDocument();
    expect(within(row).getByText('Benachrichtigung')).toBeInTheDocument();
  });

  it('Bearbeiten sendet PATCH', async () => {
    const fetchMock = stubFetchRoutes({
      '/monitoring/alerts/rule-2': { ...stagedRule, name: 'Load renamed' },
      '/monitoring/alerts': page([stagedRule]),
      '/ci-types': page([]),
      '/clients': page([]),
      '/sites': page([]),
    });
    renderWithProviders(<MonitoringPage />, { route: '/monitoring' });
    fireEvent.click(await screen.findByRole('button', { name: 'Bearbeiten: Load' }));
    const dialog = await screen.findByRole('dialog', { name: 'Alarmregel bearbeiten' });
    fireEvent.change(within(dialog).getByLabelText('Regelname'), {
      target: { value: 'Load renamed' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));
    await waitFor(() => {
      const calls = fetchMock.mock.calls as unknown as [
        RequestInfo | URL,
        RequestInit | undefined,
      ][];
      const patch = calls.find(([, init]) => init?.method === 'PATCH');
      expect(patch).toBeDefined();
      expect(String(patch![0])).toContain('/monitoring/alerts/rule-2');
      const body = JSON.parse(String(patch![1]!.body));
      expect(body).toMatchObject({
        name: 'Load renamed',
        critical_condition: { op: 'gt', field: 'value', value: 90 },
        warning_condition: { op: 'gte', field: 'avg', value: 70 },
        clear_condition: { op: 'lt', field: 'value', value: 60 },
      });
      expect(body).not.toHaveProperty('metric_name');
    });
  });

  it('draws the threshold lines of warning and critical (WP-990 AK5)', async () => {
    const other = {
      ...stagedRule,
      id: 'rule-3',
      name: 'Rate',
      critical_condition: { op: 'gt', field: 'rate', value: 5 },
      warning_condition: null,
    };
    stubFetchRoutes({
      '/monitoring/alerts': page([stagedRule, other]),
      '/monitoring/metrics': page([
        { timestamp: '2026-11-14T10:00:00Z', value: 40 },
        { timestamp: '2026-11-14T10:01:00Z', value: 80 },
      ]),
    });
    renderWithProviders(<MonitoringPage />, { route: '/monitoring' });
    await screen.findByText('Load');
    fireEvent.change(screen.getAllByLabelText('Metrikname')[0]!, { target: { value: 'cpu.load' } });
    fireEvent.click(screen.getByRole('button', { name: 'Abfragen' }));
    expect(await screen.findByTestId('threshold-critical')).toHaveClass('border-danger');
    expect(screen.getByTestId('threshold-warning')).toHaveClass('border-warning');
    // The values are in the accessible description of the chart.
    const chart = screen.getByRole('img');
    expect(chart.getAttribute('aria-label')).toContain('value > 90');
    expect(chart.getAttribute('aria-label')).toContain('avg ≥ 70');
    // The rule is restricted to a client: its lines say the scope is not checked.
    expect(chart.getAttribute('aria-label')).toContain('Geltungsbereich für diese Serie ungeprüft');
    // A rate stage has no line and says so.
    expect(screen.getByText(/Keine Schwellwertlinie für Rate/)).toBeInTheDocument();
  });

  it('opens the rule dialog by keyboard, closes it with Esc and returns the focus (WP-990 AK6)', async () => {
    const user = userEvent.setup();
    stubFetchRoutes({
      '/monitoring/alerts': page([]),
      '/ci-types': page([]),
      '/clients': page([]),
      '/sites': page([]),
    });
    renderWithProviders(<MonitoringPage />, { route: '/monitoring' });
    const opener = await screen.findByRole('button', { name: 'Alarmregel anlegen' });
    opener.focus();
    await user.keyboard('{Enter}');
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByRole('button', { name: 'Speichern' })).toBeDisabled();
    await user.tab();
    expect(dialog.contains(document.activeElement)).toBe(true);
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(document.activeElement).toBe(opener);
  });
});
