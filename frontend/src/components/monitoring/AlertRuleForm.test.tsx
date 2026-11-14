import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { AlertRuleForm } from './AlertRuleForm';
import { renderWithProviders } from '../../test/utils';
import type { AlertRule } from '../../api/client';
import type { Condition } from '../../rules/rules';

// The click builder is covered by its own tests (RuleBuilder.test.tsx); here
// a stub stands for each builder: its button reports the condition the test
// set for its label, and it shows the server path it was handed.
const nextCondition: Record<string, Condition | null> = {};
vi.mock('../rules/RuleBuilder', () => ({
  RuleBuilder: (props: {
    label?: string;
    onChange?: (check: { ok: true; condition: Condition | null }) => void;
    serverError?: { path: string; message: string };
  }) => (
    <div
      data-testid={`builder-stub-${props.label}`}
      data-server-path={props.serverError?.path ?? ''}
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

interface Call {
  method: string;
  url: string;
  body?: unknown;
}

// stubAPI answers the lists of the form and records every write; reply
// answers the writes.
function stubAPI(reply: (call: Call) => Response) {
  const calls: Call[] = [];
  const json = (body: unknown, status = 200) =>
    new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input.toString();
    const method = (init?.method ?? 'GET').toUpperCase();
    if (method !== 'GET') {
      const call: Call = {
        method,
        url,
        body: init?.body ? JSON.parse(String(init.body)) : undefined,
      };
      calls.push(call);
      return reply(call);
    }
    if (url.includes('/ci-types')) {
      return json({
        data: [
          { id: 'type-server', key: 'server', name: 'Server', display_name: { en: 'Server' } },
        ],
        total: 1,
      });
    }
    if (url.includes('/clients'))
      return json({ data: [{ id: 'client-1', name: 'Client One' }], total: 1 });
    if (url.includes('/sites'))
      return json({ data: [{ id: 'site-1', name: 'Site One' }], total: 1 });
    return json({ data: [], total: 0 });
  });
  vi.stubGlobal('fetch', fetchMock);
  return calls;
}

const saved = (body: unknown) =>
  new Response(JSON.stringify({ id: 'rule-1', ...(body as object) }), {
    status: 201,
    headers: { 'Content-Type': 'application/json' },
  });

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  for (const k of Object.keys(nextCondition)) delete nextCondition[k];
});

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe('AlertRuleForm (WP-990)', () => {
  it('Anlegen mit Hysterese sendet AST', async () => {
    const calls = stubAPI((c) => saved(c.body));
    renderWithProviders(<AlertRuleForm open onOpenChange={() => {}} />);
    fill('Regelname', 'cpu high');
    fill('Metrikname', 'cpu.load');
    nextCondition['Kritisch'] = { op: 'gt', field: 'value', value: 90 };
    nextCondition['Clear-Bedingung (optional)'] = { op: 'lt', field: 'value', value: 85 };
    fireEvent.click(screen.getByRole('button', { name: 'set Kritisch' }));
    fireEvent.click(screen.getByRole('button', { name: 'set Clear-Bedingung (optional)' }));
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));
    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]!.method).toBe('POST');
    expect(calls[0]!.url).toContain('/monitoring/alerts');
    expect(calls[0]!.body).toMatchObject({
      name: 'cpu high',
      metric_name: 'cpu.load',
      critical_condition: { op: 'gt', field: 'value', value: 90 },
      clear_condition: { op: 'lt', field: 'value', value: 85 },
      warning_condition: null,
      info_condition: null,
      actions: { notify: { enabled: true }, webhook: { enabled: true } },
    });
  });

  it('Scope mit CI-Typ und Client wird gesendet', async () => {
    const calls = stubAPI((c) => saved(c.body));
    renderWithProviders(<AlertRuleForm open onOpenChange={() => {}} />);
    fill('Regelname', 'disk');
    fill('Metrikname', 'disk.used');
    nextCondition['Warnung'] = { op: 'gte', field: 'avg', value: 80 };
    fireEvent.click(screen.getByRole('button', { name: 'set Warnung' }));
    fireEvent.click(await screen.findByRole('checkbox', { name: 'Server' }));
    expect(screen.getByText('einschließlich Untertypen')).toBeInTheDocument();
    await screen.findByRole('option', { name: 'Client One' });
    fireEvent.change(screen.getByLabelText('Mandant'), { target: { value: 'client-1' } });
    await screen.findByRole('option', { name: 'Site One' });
    fireEvent.change(screen.getByLabelText('Standort'), { target: { value: 'site-1' } });
    fill('Gruppieren nach (Labels, durch Komma getrennt)', 'interface, vlan');
    fireEvent.click(screen.getByRole('checkbox', { name: 'Webhook' }));
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));
    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]!.body).toMatchObject({
      warning_condition: { op: 'gte', field: 'avg', value: 80 },
      scope: { ci_type_ids: ['type-server'], client_id: 'client-1', site_id: 'site-1' },
      group_by: ['interface', 'vlan'],
      actions: { notify: { enabled: true }, webhook: { enabled: false } },
    });
  });

  it('422 mit Pfad markiert den Builder-Knoten', async () => {
    stubAPI(
      () =>
        new Response(
          JSON.stringify({
            title: 'Unprocessable Entity',
            status: 422,
            detail: 'unknown field',
            field: 'warning_condition.rules[1].field',
          }),
          { status: 422, headers: { 'Content-Type': 'application/problem+json' } },
        ),
    );
    renderWithProviders(<AlertRuleForm open onOpenChange={() => {}} />);
    fill('Regelname', 'r');
    fill('Metrikname', 'm');
    nextCondition['Warnung'] = {
      op: 'all',
      rules: [
        { op: 'gt', field: 'value', value: 1 },
        { op: 'gt', field: 'value', value: 2 },
      ],
    };
    fireEvent.click(screen.getByRole('button', { name: 'set Warnung' }));
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));
    await waitFor(() =>
      expect(screen.getByTestId('builder-stub-Warnung').getAttribute('data-server-path')).toBe(
        'condition.rules[1].field',
      ),
    );
    expect(screen.getByTestId('builder-stub-Kritisch').getAttribute('data-server-path')).toBe('');
    expect(await screen.findByRole('alert')).toHaveTextContent('unknown field');
  });

  it('Speichern ohne Stufe gesperrt', async () => {
    const calls = stubAPI((c) => saved(c.body));
    renderWithProviders(<AlertRuleForm open onOpenChange={() => {}} />);
    fill('Regelname', 'no stage');
    fill('Metrikname', 'm');
    expect(screen.getByRole('button', { name: 'Speichern' })).toBeDisabled();
    expect(
      screen.getByText('Mindestens eine Stufe festlegen, um die Regel zu speichern.'),
    ).toBeInTheDocument();
    // A clear condition alone is no stage.
    nextCondition['Clear-Bedingung (optional)'] = { op: 'lt', field: 'value', value: 1 };
    fireEvent.click(screen.getByRole('button', { name: 'set Clear-Bedingung (optional)' }));
    expect(screen.getByRole('button', { name: 'Speichern' })).toBeDisabled();
    nextCondition['Info'] = { op: 'gt', field: 'value', value: 1 };
    fireEvent.click(screen.getByRole('button', { name: 'set Info' }));
    expect(screen.getByRole('button', { name: 'Speichern' })).toBeEnabled();
    expect(calls).toHaveLength(0);
  });

  it('edits with PATCH without the metric', async () => {
    const calls = stubAPI(
      (c) =>
        new Response(JSON.stringify({ id: 'rule-9', ...(c.body as object) }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
    );
    const rule = {
      id: 'rule-9',
      org_id: 'o',
      name: 'old',
      metric_name: 'cpu.load',
      duration: '1m',
      enabled: true,
      critical_condition: { op: 'gt', field: 'value', value: 90 },
      warning_condition: null,
      info_condition: null,
      clear_condition: null,
      eval_window: '5m',
      scope: {},
      group_by: [],
      actions: { notify: { enabled: false }, webhook: { enabled: true } },
    } as AlertRule;
    renderWithProviders(<AlertRuleForm open onOpenChange={() => {}} rule={rule} />);
    expect(screen.getByLabelText('Metrikname')).toBeDisabled();
    fill('Regelname', 'renamed');
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));
    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]!.method).toBe('PATCH');
    expect(calls[0]!.url).toContain('/monitoring/alerts/rule-9');
    expect(calls[0]!.body).toMatchObject({
      name: 'renamed',
      critical_condition: { op: 'gt', field: 'value', value: 90 },
      actions: { notify: { enabled: false }, webhook: { enabled: true } },
    });
    expect(calls[0]!.body).not.toHaveProperty('metric_name');
  });

  it('shows a 422 outside the builders at its field, the rest in the alert line', async () => {
    stubAPI(
      () =>
        new Response(
          JSON.stringify({
            title: 'Unprocessable Entity',
            status: 422,
            detail: 'invalid rule',
            violations: [
              { field: 'scope.client_id', detail: 'client not found' },
              { field: 'scope.ci_ids[0]', detail: 'ci not found' },
              { field: 'scope.labels.env', detail: 'label value too long' },
              { field: 'group_by[1]', detail: 'label key named twice' },
              { field: 'severity', detail: 'not allowed' },
            ],
          }),
          { status: 422, headers: { 'Content-Type': 'application/problem+json' } },
        ),
    );
    renderWithProviders(<AlertRuleForm open onOpenChange={() => {}} />);
    fill('Regelname', 'r');
    fill('Metrikname', 'm');
    nextCondition['Kritisch'] = { op: 'gt', field: 'value', value: 1 };
    fireEvent.click(screen.getByRole('button', { name: 'set Kritisch' }));
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));
    expect(await screen.findByText('client not found')).toBeInTheDocument();
    expect(screen.getByText('ci not found')).toBeInTheDocument();
    expect(screen.getByText('label value too long')).toBeInTheDocument();
    expect(screen.getByText('label key named twice')).toBeInTheDocument();
    const alerts = screen.getAllByRole('alert').map((a) => a.textContent ?? '');
    expect(alerts.some((a) => a.includes('severity: not allowed'))).toBe(true);
  });

  it('refuses group_by keys named twice before sending', async () => {
    const calls = stubAPI((c) => saved(c.body));
    renderWithProviders(<AlertRuleForm open onOpenChange={() => {}} />);
    fill('Regelname', 'dup');
    fill('Metrikname', 'm');
    nextCondition['Kritisch'] = { op: 'gt', field: 'value', value: 1 };
    fireEvent.click(screen.getByRole('button', { name: 'set Kritisch' }));
    fill('Gruppieren nach (Labels, durch Komma getrennt)', 'a, a');
    expect(screen.getByText('Das Label a steht doppelt.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Speichern' })).toBeDisabled();
    expect(calls).toHaveLength(0);
  });

  it('keeps a site without client when a rule is renamed', async () => {
    const calls = stubAPI(
      (c) =>
        new Response(JSON.stringify({ id: 'rule-7', ...(c.body as object) }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
    );
    const rule = {
      id: 'rule-7',
      org_id: 'o',
      name: 'site only',
      metric_name: 'cpu.load',
      duration: '1m',
      enabled: true,
      critical_condition: { op: 'gt', field: 'value', value: 90 },
      warning_condition: null,
      info_condition: null,
      clear_condition: null,
      eval_window: '5m',
      scope: { site_id: 'site-1' },
      group_by: [],
    } as AlertRule;
    renderWithProviders(<AlertRuleForm open onOpenChange={() => {}} rule={rule} />);
    expect(screen.getByLabelText('Standort')).toBeEnabled();
    fill('Regelname', 'site only renamed');
    fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));
    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]!.body).toMatchObject({ scope: { site_id: 'site-1' } });
    expect((calls[0]!.body as { scope: object }).scope).not.toHaveProperty('client_id');
  });
});
