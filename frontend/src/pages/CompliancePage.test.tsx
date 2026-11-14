import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { CompliancePage } from './CompliancePage';
import { renderWithProviders } from '../test/utils';
import type { Condition } from '../rules/rules';

// The click builder has its own tests (RuleBuilder.test.tsx); a stub stands
// for each builder: its button reports the condition the test set for its
// label, and it shows the server path it was handed.
const nextCondition: Record<string, Condition | null> = {};
vi.mock('../components/rules/RuleBuilder', () => ({
  RuleBuilder: (props: {
    label?: string;
    onChange?: (check: { ok: true; condition: Condition | null }) => void;
    serverError?: { path: string; message: string };
    fields?: { field: string }[];
  }) => (
    <div
      data-testid={`builder-stub-${props.label}`}
      data-server-path={props.serverError?.path ?? ''}
      data-fields={(props.fields ?? []).map((f) => f.field).join(' ')}
    >
      <button
        type="button"
        onClick={() =>
          props.onChange?.({ ok: true, condition: nextCondition[props.label ?? ''] ?? null })
        }
      >
        {`set ${props.label}`}
      </button>
    </div>
  ),
}));

const AT = '2026-11-01T00:00:00Z';

function paginated<T>(data: T[]) {
  return { data, total: data.length, limit: 50, offset: 0, has_more: false };
}

const rule = {
  id: 'rule-1',
  organization_id: 'org-1',
  name: 'Encrypted',
  severity: 'high',
  category: 'security',
  expression: { op: 'eq', field: 'status', value: 'active' },
  applies_when: null,
  active: true,
  created_at: AT,
  updated_at: AT,
};

const control = {
  id: 'c0000000-0000-4000-8000-000000000001',
  standard: 'iso27001',
  control_id: '5.15',
  title_de: 'Zugangssteuerung',
  title_en: 'Access control',
  sort_order: 1,
  rule_count: 0,
  active_rule_count: 0,
};

interface Call {
  method: string;
  url: string;
  body?: unknown;
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

/**
 * stubAPI answers the reads of the page; write answers the writes (null:
 * the default). Every request is recorded.
 */
function stubAPI(
  options: {
    rule?: Record<string, unknown>;
    permissions?: string[];
    write?: (call: Call) => Response | null;
    job?: () => Record<string, unknown>;
  } = {},
) {
  const calls: Call[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input.toString();
    const method = (init?.method ?? 'GET').toUpperCase();
    const body = typeof init?.body === 'string' ? JSON.parse(init.body) : undefined;
    const call = { method, url, body };
    calls.push(call);
    if (method !== 'GET') {
      const answer = options.write?.(call);
      if (answer) return answer;
      if (url.includes('/compliance/evaluations'))
        return json(
          { id: 'job-1', kind: 'compliance.evaluate', status: 'queued', progress: 0 },
          202,
        );
      if (url.includes('/instantiate'))
        return json(
          { ...rule, id: 'rule-2', name: 'Firmware', active: false, control_ids: [] },
          201,
        );
      if (url.includes('/controls'))
        return json({ rule_id: 'rule-1', control_ids: body?.control_ids });
      if (method === 'POST') return json({ ...rule, ...body, id: 'rule-new' }, 201);
      return json({ ...rule, ...body });
    }
    if (url.includes('/me/permissions'))
      return json({
        user_id: 'u-1',
        permissions: options.permissions ?? ['compliance:read', 'compliance:write'],
      });
    if (url.includes('/jobs/')) return json(options.job?.() ?? { id: 'job-1', status: 'running' });
    const current = { ...rule, ...options.rule };
    if (url.includes('/compliance/rules/')) return json({ ...current, control_ids: [] });
    if (url.includes('/compliance/rules')) return json(paginated([current]));
    if (url.includes('/attribute-definitions'))
      return json(
        paginated([
          {
            id: 'def-1',
            key: 'disk_encryption',
            name: 'Disk encryption',
            data_type: 'boolean',
            searchable: true,
          },
        ]),
      );
    if (url.includes('/compliance/results'))
      return json(
        paginated([
          {
            id: 'result-1',
            organization_id: 'org-1',
            rule_id: 'rule-1',
            ci_id: 'ci-1',
            ci_type_id: 'server',
            status: 'fail',
            details: 'Enable encryption',
            evaluated_at: AT,
            created_at: AT,
            updated_at: AT,
          },
        ]),
      );
    if (url.includes('/compliance/score/history'))
      return json({
        group_by: 'overall',
        from: '2025-10-01',
        to: '2026-11-01',
        data: [
          {
            day: '2026-10-31',
            key: '',
            label: '',
            passed: 1,
            failed: 1,
            passed_weight: 5,
            failed_weight: 5,
            score: 50,
          },
          {
            day: '2026-11-01',
            key: '',
            label: '',
            passed: 3,
            failed: 1,
            passed_weight: 15,
            failed_weight: 5,
            score: 75,
          },
        ],
      });
    if (url.includes('/compliance/score')) {
      const groupBy = new URL(url, 'http://x').searchParams.get('group_by');
      return json({
        overall: { passed: 1, failed: 1, not_applicable: 0, score: 50 },
        by_ci_type: [],
        results: [],
        ...(groupBy
          ? {
              group_by: groupBy,
              groups: [
                {
                  key: 'cl-1',
                  label: `Gruppe ${groupBy}`,
                  passed: 4,
                  failed: 1,
                  not_applicable: 0,
                  score: 80,
                },
              ],
            }
          : {}),
      });
    }
    if (url.includes('/compliance/controls/coverage')) return json(paginated([control]));
    if (url.includes('/compliance/controls')) return json(paginated([control]));
    if (url.includes('/compliance/rule-templates'))
      return json({
        data: [
          {
            key: 'firmware_maintained',
            version: 1,
            name: 'Firmware gepflegt',
            description: 'Firmware ist gesetzt',
            severity: 'medium',
            category: 'security',
            expression: { op: 'not_empty', field: 'firmware_version' },
            remediation_hint: 'x',
            controls: [],
          },
        ],
      });
    if (url.includes('/ci-types'))
      return json(
        paginated([{ id: 'server', name: 'server', display_name: { 'de-DE': 'Server' } }]),
      );
    return json({ detail: `unexpected request: ${url}` }, 404);
  });
  vi.stubGlobal('fetch', fetchMock);
  return calls;
}

beforeEach(() => {
  for (const key of Object.keys(nextCondition)) delete nextCondition[key];
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('CompliancePage', () => {
  it('renders score and failing results', async () => {
    stubAPI();
    renderWithProviders(<CompliancePage />, { route: '/compliance' });
    expect(await screen.findByText('50%')).toBeInTheDocument();
    expect(screen.getByText('Enable encryption')).toBeInTheDocument();
  });

  it('creates a rule with a MET-45 condition and controls', async () => {
    const calls = stubAPI();
    renderWithProviders(<CompliancePage />, { route: '/compliance' });
    fireEvent.click(await screen.findByRole('button', { name: 'Neue Regel' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText(/^Name/), {
      target: { value: 'Firmware gesetzt' },
    });
    nextCondition['Bedingung'] = { op: 'not_empty', field: 'firmware_version' };
    fireEvent.click(within(dialog).getByRole('button', { name: 'set Bedingung' }));
    fireEvent.click(
      (
        await within(dialog).findAllByRole('checkbox', { name: /5\.15/, hidden: true })
      )[0] as HTMLElement,
    );
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === 'PUT' && c.url.includes('/controls'))).toBe(true),
    );
    const post = calls.find((c) => c.method === 'POST' && c.url.endsWith('/compliance/rules'));
    expect(post?.body).toMatchObject({
      name: 'Firmware gesetzt',
      expression: { op: 'not_empty', field: 'firmware_version' },
    });
    const put = calls.find((c) => c.method === 'PUT');
    expect(put?.url).toContain('/compliance/rules/rule-new/controls');
    expect(put?.body).toEqual({ control_ids: [control.id] });
  });

  it('offers the attributes as attributes.<key> in the builder', async () => {
    stubAPI();
    renderWithProviders(<CompliancePage />, { route: '/compliance' });
    fireEvent.click(await screen.findByRole('button', { name: 'Neue Regel' }));
    await screen.findByRole('dialog');
    await waitFor(() =>
      expect(screen.getByTestId('builder-stub-Bedingung').getAttribute('data-fields')).toContain(
        'attributes.disk_encryption',
      ),
    );
    expect(screen.getByTestId('builder-stub-Bedingung').getAttribute('data-fields')).toContain(
      'finding.open_count.vulnerability',
    );
  });

  it('clears the CI type of a rule on edit', async () => {
    const calls = stubAPI({ rule: { ci_type_id: 'server' } });
    renderWithProviders(<CompliancePage />, { route: '/compliance' });
    fireEvent.click(await screen.findByRole('button', { name: 'Regel Encrypted bearbeiten' }));
    const dialog = await screen.findByRole('dialog');
    const select = within(dialog).getByLabelText('CI-Typ');
    await waitFor(() => expect((select as HTMLSelectElement).value).toBe('server'));
    fireEvent.change(select, { target: { value: '' } });
    nextCondition['Bedingung'] = { op: 'eq', field: 'status', value: 'active' };
    fireEvent.click(within(dialog).getByRole('button', { name: 'set Bedingung' }));
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
    const patch = calls.find((c) => c.method === 'PATCH');
    expect(patch?.body).toMatchObject({ ci_type_id: '' });
  });

  it('shows a 422 at the AST node', async () => {
    stubAPI({
      write: (c) =>
        c.method === 'POST' && c.url.endsWith('/compliance/rules')
          ? json(
              {
                title: 'Unprocessable Entity',
                status: 422,
                detail: 'unknown field',
                field: 'expression.rules[1].field',
              },
              422,
            )
          : null,
    });
    renderWithProviders(<CompliancePage />, { route: '/compliance' });
    fireEvent.click(await screen.findByRole('button', { name: 'Neue Regel' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText(/^Name/), { target: { value: 'r' } });
    nextCondition['Bedingung'] = {
      op: 'all',
      rules: [
        { op: 'eq', field: 'status', value: 'active' },
        { op: 'eq', field: 'nope', value: 'x' },
      ],
    };
    fireEvent.click(within(dialog).getByRole('button', { name: 'set Bedingung' }));
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));
    await waitFor(() =>
      expect(screen.getByTestId('builder-stub-Bedingung').getAttribute('data-server-path')).toBe(
        'condition.rules[1].field',
      ),
    );
    expect(
      screen.getByTestId('builder-stub-Gilt wenn (applies_when)').getAttribute('data-server-path'),
    ).toBe('');
  });

  it('instantiates a template as an inactive rule', async () => {
    const calls = stubAPI();
    renderWithProviders(<CompliancePage />, { route: '/compliance' });
    fireEvent.click(await screen.findByRole('button', { name: 'Aus Vorlage anlegen' }));
    fireEvent.click(
      await screen.findByRole('button', { name: 'Vorlage Firmware gepflegt anlegen' }),
    );
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === 'POST' &&
            c.url.includes('/compliance/rule-templates/firmware_maintained/instantiate'),
        ),
      ).toBe(true),
    );
    // The editor opens on the created (inactive) rule.
    expect(
      await screen.findByRole('dialog', { name: 'Compliance-Regel bearbeiten' }),
    ).toBeInTheDocument();
    expect(screen.getByRole('checkbox', { name: 'Aktiv' })).not.toBeChecked();
  });

  it('groups the score by client and renders the 400-day trend with a table alternative', async () => {
    const calls = stubAPI();
    renderWithProviders(<CompliancePage />, { route: '/compliance' });
    const groupBy = await screen.findByLabelText('Gruppieren nach');
    fireEvent.change(groupBy, { target: { value: 'client' } });
    expect(await screen.findByText('Gruppe client')).toBeInTheDocument();
    expect(within(screen.getByTestId('score-groups')).getByText('80%')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '400 Tage' }));
    await waitFor(() =>
      expect(
        calls.some((c) => {
          if (!c.url.includes('/compliance/score/history')) return false;
          const q = new URL(c.url, 'http://x').searchParams;
          const days =
            (Date.parse(q.get('to') ?? '') - Date.parse(q.get('from') ?? '')) / 86_400_000 + 1;
          return days === 400;
        }),
      ).toBe(true),
    );
    const table = await screen.findByRole('table', { name: 'Score der letzten 400 Tage' });
    expect(within(table).getByText('2026-11-01')).toBeInTheDocument();
    expect(within(table).getByText('75 %')).toBeInTheDocument();
    expect(screen.getByTestId('score-trend-line').getAttribute('aria-hidden')).toBe('true');
  });

  it('starts the evaluation as a job and reloads on completion', async () => {
    let status = 'running';
    const calls = stubAPI({
      job: () => ({
        id: 'job-1',
        kind: 'compliance.evaluate',
        status,
        progress: status === 'running' ? 40 : 100,
      }),
    });
    renderWithProviders(<CompliancePage />, { route: '/compliance' });
    const button = await screen.findByRole('button', { name: 'Bewerten' });
    await waitFor(() => expect(button).toBeEnabled());
    fireEvent.click(button);
    expect(await screen.findByText(/Bewertung: .*\(40 %\)/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Bewerten' })).toBeDisabled();
    const scoreReads = () => calls.filter((c) => c.url.endsWith('/compliance/score')).length;
    const before = scoreReads();
    status = 'succeeded';
    await waitFor(() => expect(scoreReads()).toBeGreaterThan(before), { timeout: 5000 });
    await waitFor(() => expect(screen.getByRole('button', { name: 'Bewerten' })).toBeEnabled());
  });

  it('is read-only without compliance:write', async () => {
    const calls = stubAPI({ permissions: ['compliance:read'] });
    renderWithProviders(<CompliancePage />, { route: '/compliance' });
    fireEvent.click(await screen.findByRole('button', { name: 'Regel Encrypted ansehen' }));
    const dialog = await screen.findByRole('dialog', { name: 'Compliance-Regel ansehen' });
    expect(within(dialog).queryByRole('button', { name: 'Speichern' })).toBeNull();
    expect(within(dialog).getByLabelText(/^Name/)).toBeDisabled();
    expect(screen.queryByRole('button', { name: 'Neue Regel' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Aus Vorlage anlegen' })).toBeNull();
    expect(calls.some((c) => c.method !== 'GET')).toBe(false);
  });
});
