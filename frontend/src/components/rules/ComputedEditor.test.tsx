/**
 * WP-802 (UI-11 [P2], MET-45): the expression mode of the click builder and
 * the ComputedEditor in the attribute dialog of CITypeAdminPage.
 *
 *   Mode     the expression mode offers exactly the operators of $defs/expr
 *            and emits only $defs/expr (every output passes parseExpr); no
 *            reference to a computed attribute or to the attribute itself;
 *            a result type that does not fit the data type is a hint
 *   Preview  evalExpr against editable sample values, with now and the time
 *            zone of the organization (GET /api/v1/org)
 *   Dialog   a computed attribute is created by POST with ci_type_id and
 *            computed, its expression changed by PATCH; no switch between
 *            computed and entered after creation, no required, unique or
 *            default; a 422 on computed shows at the expression (at the
 *            node its path names)
 *
 * Retest:
 *   cd frontend && npx vitest run src/components/rules/ComputedEditor.test.tsx
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../../test/utils';
import { RuleBuilder } from './RuleBuilder';
import { AttributeEditor } from '../metamodel/AttributeEditor';
import {
  EXPRESSION_MODE,
  exprFieldCatalog,
  type ExprCheck,
  type ExprFieldSpec,
} from '../../rules/builderModes';
import { EXPR_OPS, parseExpr, type Expr } from '../../rules/expr';
import type { components } from '../../api/generated/schema';

type AttributeDefinition = components['schemas']['AttributeDefinition'];
type Json = Record<string, unknown>;

function definition(id: string, key: string, extra: Json = {}): AttributeDefinition {
  return {
    id,
    key,
    scope: 'type',
    ci_type_id: 't-srv',
    display_name: { de: key, en: key },
    data_type: 'text',
    required: false,
    multi_value: false,
    searchable: false,
    unique: false,
    unique_scope: 'org',
    inheritable: true,
    order: 0,
    is_system: false,
    applies_to: 'all',
    applies_to_categories: [],
    applies_to_type_ids: [],
    index_status: { state: 'none', index_name: '', valid: false, attempts: 0, max_attempts: 3 },
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...extra,
  } as AttributeDefinition;
}

const HOSTNAME = definition('ad-host', 'hostname');
const RAM = definition('ad-ram', 'ram_gb', { data_type: 'integer' });
const LEFT = definition('ad-left', 'warranty_left', {
  data_type: 'integer',
  computed: {
    op: 'days_between',
    args: [{ op: 'today' }, { op: 'field', field: 'asset.warranty_until' }],
  },
});

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

interface Call {
  method: string;
  url: string;
  body: Json | undefined;
}

function stubBackend(
  opts: { timezone?: string; definitions?: AttributeDefinition[]; patch?: () => Response } = {},
) {
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input.toString();
      const method = (init?.method ?? 'GET').toUpperCase();
      const body = init?.body ? (JSON.parse(String(init.body)) as Json) : undefined;
      calls.push({ method, url, body });
      const path = new URL(url, 'http://localhost').pathname.replace(/^\/api\/v1/, '');
      if (path === '/org') {
        return json({ id: 'o1', name: 'Org', timezone: opts.timezone ?? 'Europe/Berlin' });
      }
      if (path === '/attribute-definitions' && method === 'POST') {
        return json({ ...definition('ad-new', String(body?.key)), ...body }, 201);
      }
      if (path === '/attribute-definitions') {
        return json({ data: opts.definitions ?? [HOSTNAME, RAM, LEFT] });
      }
      if (path.startsWith('/attribute-definitions/') && method === 'PATCH') {
        return opts.patch ? opts.patch() : json({ ...LEFT, ...body });
      }
      return json({ detail: `unexpected ${method} ${url}` }, 404);
    }),
  );
  return calls;
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

/** walk returns the operators of an expression. */
function opsOf(e: Expr): string[] {
  return [e.op, ...('args' in e ? e.args.flatMap(opsOf) : [])];
}

/** The node group of the builder named label. */
const node = (label: string) => screen.getByRole('group', { name: label });

function renderBuilder(
  props: { initialValue?: unknown; fields?: ExprFieldSpec[]; dataType?: string; now?: Date } = {},
  timeZone = 'UTC',
) {
  const onChange = vi.fn<(check: ExprCheck) => void>();
  renderWithProviders(
    <RuleBuilder
      mode={EXPRESSION_MODE}
      initialValue={props.initialValue ?? null}
      fields={props.fields ?? exprFieldCatalog([HOSTNAME, RAM, LEFT], 'days_left')}
      dataType={props.dataType}
      timeZone={timeZone}
      now={props.now ?? new Date('2026-03-01T12:00:00Z')}
      onChange={onChange}
    />,
  );
  const last = () => onChange.mock.calls[onChange.mock.calls.length - 1]?.[0];
  return { onChange, last };
}

describe('expression mode of the RuleBuilder', () => {
  it('offers exactly the operators of $defs/expr and emits only expression nodes', () => {
    const { onChange, last } = renderBuilder({ dataType: 'text' });
    const kind = within(node('Ausdruck: Feld')).getByLabelText('Art des Ausdrucks');
    const options = within(kind).getAllByRole('option') as HTMLOptionElement[];
    expect(options.map((o) => o.value)).toEqual([...EXPR_OPS]);
    expect(options.map((o) => o.value)).not.toContain('required');
    expect(options.map((o) => o.value)).not.toContain('eq');
    expect(options.map((o) => o.value)).not.toContain('like');

    // Every kind as root: what goes out passes parseExpr and holds only
    // operators of $defs/expr.
    for (const op of EXPR_OPS) {
      // From an empty field reference each time, so no operand carries over.
      const root = () => screen.getAllByLabelText('Art des Ausdrucks')[0]!;
      fireEvent.change(root(), { target: { value: 'field' } });
      fireEvent.change(root(), { target: { value: op } });
      // The operands are empty field references: fill each with hostname.
      for (const select of screen.queryAllByLabelText('Feld')) {
        fireEvent.change(select, { target: { value: 'hostname' } });
      }
      for (const select of screen.queryAllByLabelText('Art des Festwerts')) {
        fireEvent.change(select, { target: { value: 'number' } });
      }
      for (const input of screen.queryAllByLabelText('Festwert')) {
        fireEvent.change(input, { target: { value: '2' } });
      }
      const check = last()!;
      expect(check.ok, `${op}: ${JSON.stringify(check)}`).toBe(true);
      if (!check.ok) continue;
      expect(check.expr.op).toBe(op);
      expect(parseExpr(check.expr)).toEqual(check.expr);
      for (const used of opsOf(check.expr)) expect(EXPR_OPS).toContain(used);
    }
    expect(onChange).toHaveBeenCalled();
  });

  it('reads no computed attribute and not the attribute itself', () => {
    renderBuilder();
    const field = within(node('Ausdruck: Feld')).getByLabelText('Feld');
    const values = (within(field).getAllByRole('option') as HTMLOptionElement[]).map(
      (o) => o.value,
    );
    expect(values).toEqual(
      expect.arrayContaining(['hostname', 'ram_gb', 'asset.warranty_until', 'asset.serial_number']),
    );
    expect(values).not.toContain('warranty_left');
    expect(values).not.toContain('days_left');
    // The deprecated asset names are not offered.
    expect(values).not.toContain('asset.warranty_end');
    expect(exprFieldCatalog([HOSTNAME, LEFT], 'hostname').map((f) => f.field)).not.toContain(
      'hostname',
    );
  });

  it('marks a stored reference to a computed attribute and does not send it', () => {
    const { last } = renderBuilder({
      initialValue: { op: 'field', field: 'warranty_left' },
    });
    expect(last()).toMatchObject({ ok: false, issue: 'field_not_allowed' });
    expect(
      within(node('Ausdruck: Feld')).getByRole('option', { name: /nicht verfügbar/ }),
    ).toBeInTheDocument();
  });

  it('builds an expression by clicks and shows the type hint', () => {
    const { last } = renderBuilder({ dataType: 'integer' });
    fireEvent.change(screen.getByLabelText('Art des Ausdrucks'), { target: { value: 'concat' } });
    const operand = node('Operand 1: Feld');
    fireEvent.change(within(operand).getByLabelText('Feld'), { target: { value: 'hostname' } });
    fireEvent.click(screen.getByRole('button', { name: 'Operand hinzufügen' }));
    fireEvent.change(within(node('Operand 2: Feld')).getByLabelText('Operand 2'), {
      target: { value: 'literal' },
    });
    fireEvent.change(within(node('Operand 2: Festwert')).getByLabelText('Festwert'), {
      target: { value: '.lan' },
    });
    expect(last()).toEqual({
      ok: true,
      resultType: 'string',
      expr: {
        op: 'concat',
        args: [
          { op: 'field', field: 'hostname' },
          { op: 'literal', value: '.lan' },
        ],
      },
    });
    // concat yields text: it does not fit integer (the server decides).
    expect(screen.getByTestId('expression-type-hint')).toHaveTextContent(
      'Der Ausdruck ergibt einen Text, das passt nicht zum Datentyp Ganzzahl',
    );
    // The preview with a sample value.
    fireEvent.change(screen.getByLabelText('Beispiel: Attribut: hostname'), {
      target: { value: 'srv' },
    });
    expect(screen.getByTestId('expression-result')).toHaveTextContent('srv.lan');
    // An operand of concat can go again; the last one cannot.
    fireEvent.click(screen.getByRole('button', { name: 'Operand 2: Festwert löschen' }));
    expect(
      screen.queryByRole('button', { name: 'Operand 1: Feld löschen' }),
    ).not.toBeInTheDocument();
  });

  it('evaluates today in the time zone of the organization', () => {
    renderBuilder(
      {
        initialValue: {
          op: 'days_between',
          args: [{ op: 'today' }, { op: 'field', field: 'asset.warranty_until' }],
        },
        now: new Date('2026-03-01T23:30:00Z'),
      },
      'Europe/Berlin',
    );
    expect(screen.getByText('Heute: 2026-03-02 (Zeitzone Europe/Berlin)')).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Beispiel: Asset: Garantie bis'), {
      target: { value: '2026-03-12' },
    });
    expect(screen.getByTestId('expression-result')).toHaveTextContent('10');
    // A missing input makes the expression empty.
    fireEvent.change(screen.getByLabelText('Beispiel: Asset: Garantie bis'), {
      target: { value: '' },
    });
    expect(screen.getByTestId('expression-preview')).toHaveTextContent('Ergebnis: leer');
  });
});

describe('ComputedEditor in the attribute dialog', () => {
  it('creates a computed attribute by POST with ci_type_id and computed', async () => {
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(new Date('2026-03-01T23:30:00Z'));
    const calls = stubBackend();
    const onClose = vi.fn();
    renderWithProviders(
      <AttributeEditor
        target={{ mode: 'create', scope: 'type', ciTypeId: 't-srv' }}
        onClose={onClose}
      />,
    );
    const dialog = screen.getByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Feldname'), { target: { value: 'days_left' } });
    expect(within(dialog).getByLabelText('Pflichtfeld')).toBeInTheDocument();
    fireEvent.click(within(dialog).getByLabelText(/^Berechnetes Attribut/));
    // A computed attribute offers neither required nor unique nor a default.
    expect(within(dialog).queryByLabelText('Pflichtfeld')).not.toBeInTheDocument();
    expect(within(dialog).queryByLabelText('Eindeutig')).not.toBeInTheDocument();
    expect(within(dialog).queryByLabelText(/Standardwert|Default/)).not.toBeInTheDocument();
    const types = (
      within(within(dialog).getByLabelText('Typ')).getAllByRole('option') as HTMLOptionElement[]
    ).map((o) => o.value);
    expect(types).toEqual(['integer', 'decimal', 'text', 'multiline', 'date', 'boolean']);
    fireEvent.change(within(dialog).getByLabelText('Typ'), { target: { value: 'integer' } });

    fireEvent.change(within(dialog).getByLabelText('Art des Ausdrucks'), {
      target: { value: 'days_between' },
    });
    fireEvent.change(within(node('Operand 1: Feld')).getByLabelText('Operand 1'), {
      target: { value: 'today' },
    });
    // The catalog of the type has loaded: no computed attribute, not itself.
    const field = within(node('Operand 2: Feld')).getByLabelText('Feld');
    await waitFor(() =>
      expect(within(field).getByRole('option', { name: 'Attribut: hostname' })).toBeInTheDocument(),
    );
    expect(within(field).queryByRole('option', { name: /warranty_left/ })).toBeNull();
    fireEvent.change(field, { target: { value: 'asset.warranty_until' } });

    // The preview in the zone of the organization.
    expect(
      await screen.findByText('Heute: 2026-03-02 (Zeitzone Europe/Berlin)'),
    ).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Beispiel: Asset: Garantie bis'), {
      target: { value: '2026-03-12' },
    });
    expect(screen.getByTestId('expression-result')).toHaveTextContent('10');

    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    const post = calls.find((c) => c.method === 'POST')!;
    expect(post.url).toContain('/attribute-definitions');
    expect(post.body).toMatchObject({
      key: 'days_left',
      data_type: 'integer',
      ci_type_id: 't-srv',
      unique: false,
      computed: {
        op: 'days_between',
        args: [{ op: 'today' }, { op: 'field', field: 'asset.warranty_until' }],
      },
    });
    expect(post.body).not.toHaveProperty('required');
    expect(post.body).not.toHaveProperty('default_value');
    expect(parseExpr(post.body!.computed)).toEqual(post.body!.computed);
  });

  it('does not send an incomplete expression', async () => {
    const calls = stubBackend();
    renderWithProviders(
      <AttributeEditor
        target={{ mode: 'create', scope: 'type', ciTypeId: 't-srv' }}
        onClose={() => {}}
      />,
    );
    const dialog = screen.getByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Feldname'), { target: { value: 'x' } });
    fireEvent.click(within(dialog).getByLabelText(/^Berechnetes Attribut/));
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));
    expect(await within(node('Ausdruck: Feld')).findByText('Wählen Sie ein Feld.')).toBeVisible();
    expect(calls.some((c) => c.method === 'POST')).toBe(false);
  });

  it('changes the expression by PATCH and offers no switch to entered', async () => {
    const calls = stubBackend();
    const onClose = vi.fn();
    renderWithProviders(
      <AttributeEditor target={{ mode: 'edit', definition: LEFT }} onClose={onClose} />,
    );
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).queryByLabelText(/^Berechnetes Attribut/)).not.toBeInTheDocument();
    expect(within(dialog).queryByLabelText('Pflichtfeld')).not.toBeInTheDocument();
    expect(within(dialog).queryByLabelText('Eindeutig')).not.toBeInTheDocument();
    // The stored expression is loaded: days_between(today, asset.warranty_until).
    expect(node('Ausdruck: Tage zwischen')).toBeInTheDocument();
    fireEvent.change(within(node('Operand 2: Feld')).getByLabelText('Feld'), {
      target: { value: 'asset.purchase_date' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    const patch = calls.find((c) => c.method === 'PATCH')!;
    expect(patch.url).toContain('/attribute-definitions/ad-left');
    expect(patch.body).toEqual({
      computed: {
        op: 'days_between',
        args: [{ op: 'today' }, { op: 'field', field: 'asset.purchase_date' }],
      },
    });
    expect(calls.some((c) => c.method === 'POST')).toBe(false);
  });

  it('shows a 422 on computed at the expression node its path names', async () => {
    stubBackend({
      patch: () =>
        new Response(
          JSON.stringify({
            status: 422,
            title: 'Unprocessable',
            field: 'computed',
            detail: 'computed: args[1]: days_between takes no number',
          }),
          { status: 422, headers: { 'Content-Type': 'application/problem+json' } },
        ),
    });
    renderWithProviders(
      <AttributeEditor target={{ mode: 'edit', definition: LEFT }} onClose={() => {}} />,
    );
    const dialog = screen.getByRole('dialog');
    await within(node('Operand 2: Feld')).findByRole('option', { name: 'Attribut: ram_gb' });
    fireEvent.change(within(node('Operand 2: Feld')).getByLabelText('Feld'), {
      target: { value: 'ram_gb' },
    });
    await act(async () => {
      fireEvent.click(within(dialog).getByRole('button', { name: 'Speichern' }));
    });
    expect(
      await within(node('Operand 2: Feld')).findByText(
        'Vom Server abgelehnt: args[1]: days_between takes no number',
      ),
    ).toBeInTheDocument();
    expect(within(node('Operand 1: Heutiges Datum')).queryByText(/Vom Server/)).toBeNull();
  });

  it('shows a 422 on computed without a path at the expression', async () => {
    stubBackend({
      patch: () =>
        new Response(
          JSON.stringify({
            status: 422,
            title: 'Unprocessable',
            field: 'computed',
            detail: 'computed: the expression refers to the computed attribute warranty_left',
          }),
          { status: 422, headers: { 'Content-Type': 'application/problem+json' } },
        ),
    });
    renderWithProviders(
      <AttributeEditor
        target={{
          mode: 'edit',
          definition: definition('ad-up', 'name_upper', {
            computed: { op: 'upper', args: [{ op: 'field', field: 'hostname' }] },
          }),
        }}
        onClose={() => {}}
      />,
    );
    await within(node('Operand 1: Feld')).findByRole('option', { name: 'Attribut: ram_gb' });
    fireEvent.change(within(node('Operand 1: Feld')).getByLabelText('Feld'), {
      target: { value: 'ram_gb' },
    });
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Speichern' }));
    });
    expect(
      await within(node('Ausdruck: Großbuchstaben')).findAllByText(
        /the expression refers to the computed attribute warranty_left/,
      ),
    ).not.toHaveLength(0);
  });
});
