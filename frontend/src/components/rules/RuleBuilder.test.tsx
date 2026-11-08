/**
 * WP-315 (SRC-03, MET-45): the click builder for the filter AST of saved
 * views. The filter mode emits only FilterExpr, offers the operators of
 * each field kind (validateComparison), keeps the limits of the server
 * (10 levels, 100 nodes), sends numbers as JSON numbers and gives a loaded
 * AST back unchanged; it is operable by keyboard with labelled controls.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithProviders } from '../../test/utils';
import { RuleBuilder } from './RuleBuilder';
import {
  FILTER_FIELDS,
  OPS_BY_KIND,
  checkTree,
  fieldCatalog,
  fromFilter,
  newGroup,
  nodeAtPath,
  opsFor,
  toFilter,
  type FieldSpec,
  type FilterCheck,
  type FilterEntityKind,
  type FilterExpr,
} from '../../rules/builderModes';
import { filterOps, parseFilter } from '../../rules/filter';
import type { components } from '../../api/generated/schema';

type AttributeDefinition = components['schemas']['AttributeDefinition'];

function definition(key: string, data_type: string, extra: Partial<AttributeDefinition> = {}) {
  return {
    id: `def-${key}`,
    scope: 'global',
    key,
    display_name: { de: `${key} (de)`, en: `${key} (en)` },
    data_type,
    required: false,
    multi_value: false,
    searchable: true,
    unique: false,
    unique_scope: 'org',
    inheritable: false,
    order: 0,
    is_system: false,
    applies_to: 'all',
    applies_to_categories: [],
    applies_to_type_ids: [],
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...extra,
  } as AttributeDefinition;
}

const definitions = [
  definition('ram_gb', 'integer'),
  definition('environment', 'enum', { enum_values: ['prod', 'test'] }),
  definition('hidden_note', 'text', { searchable: false }),
];

const fields: Record<FilterEntityKind, FieldSpec[]> = {
  ci: fieldCatalog('ci', { definitions }),
  asset: fieldCatalog('asset', { definitions }),
};

function setup(
  props: {
    entityKind?: FilterEntityKind;
    initialValue?: FilterExpr;
    forbiddenFields?: string[];
    serverError?: { path: string; message: string };
  } = {},
) {
  const checks: FilterCheck[] = [];
  const onChange = vi.fn((check: FilterCheck) => checks.push(check));
  const view = renderWithProviders(
    <RuleBuilder
      entityKind={props.entityKind ?? 'ci'}
      initialValue={props.initialValue}
      fields={fields}
      onChange={onChange}
      forbiddenFields={props.forbiddenFields}
      serverError={props.serverError}
    />,
  );
  return { ...view, last: () => checks[checks.length - 1]!, checks };
}

/** Each node is exactly one of and, or, not, related or a comparison. */
function expectOneKind(node: FilterExpr) {
  const n = node as Record<string, unknown>;
  const kinds = ['and', 'or', 'not', 'related', 'field'].filter((k) => n[k] !== undefined);
  expect(kinds).toHaveLength(1);
  for (const key of Object.keys(n)) {
    expect(['and', 'or', 'not', 'related', 'field', 'op', 'value']).toContain(key);
  }
  if (n.and) (n.and as FilterExpr[]).forEach(expectOneKind);
  if (n.or) (n.or as FilterExpr[]).forEach(expectOneKind);
  if (n.not) expectOneKind(n.not as FilterExpr);
  if (n.related) expectOneKind((n.related as { where: FilterExpr }).where);
  if (n.field !== undefined) expect(filterOps).toContain(n.op);
}

/** nested builds groups of the given depth, the innermost holding one comparison. */
function nested(depth: number): FilterExpr {
  let node: FilterExpr = { field: 'name', op: 'eq', value: 'x' };
  for (let i = 1; i < depth; i++) node = { and: [node] };
  return node;
}

function optionValues(select: HTMLElement): string[] {
  return within(select)
    .getAllByRole('option')
    .map((o) => (o as HTMLOptionElement).value);
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('RuleBuilder filter mode', () => {
  it('builds groups, negations and comparisons as FilterExpr only', async () => {
    const { last } = setup();
    // The empty root emits no filter.
    expect(last()).toEqual({ ok: true, where: undefined });

    fireEvent.click(screen.getByRole('button', { name: 'Bedingung hinzufügen' }));
    fireEvent.change(screen.getByLabelText('Feld'), { target: { value: 'name' } });
    fireEvent.change(screen.getByLabelText('Wert'), { target: { value: 'web-*' } });
    fireEvent.change(screen.getByLabelText('Operator'), { target: { value: 'like' } });

    // A nested group switched to or, holding a negated comparison.
    const root = screen.getByRole('group', { name: 'Gruppe (UND)' });
    fireEvent.click(
      within(root).getAllByRole('button', { name: 'Gruppe hinzufügen' }).slice(-1)[0]!,
    );
    const inner = screen.getAllByRole('group', { name: 'Gruppe (UND)' })[1]!;
    fireEvent.change(within(inner).getByLabelText('Verknüpfung'), { target: { value: 'or' } });
    const orGroup = screen.getByRole('group', { name: 'Gruppe (ODER)' });
    fireEvent.click(within(orGroup).getByRole('button', { name: 'Bedingung hinzufügen' }));
    const inOr = within(orGroup).getByRole('group', { name: 'Bedingung 1' });
    fireEvent.change(within(inOr).getByLabelText('Feld'), { target: { value: 'is_manual' } });
    fireEvent.change(within(inOr).getByLabelText('Wert'), { target: { value: 'true' } });
    fireEvent.click(within(inOr).getByRole('button', { name: 'Bedingung 1 negieren' }));

    const check = last();
    expect(check.ok).toBe(true);
    const where = (check as { where: FilterExpr }).where;
    expect(where).toEqual({
      and: [
        { field: 'name', op: 'like', value: 'web-*' },
        { or: [{ not: { field: 'is_manual', op: 'eq', value: true } }] },
      ],
    });
    expectOneKind(where);
    expect(parseFilter(where, 'ci')).toEqual(where);
    expect(screen.getByRole('group', { name: 'Negation' })).toBeInTheDocument();
  });

  it('offers related nodes for CIs only, up to depth 5', () => {
    setup({ entityKind: 'asset' });
    expect(screen.queryByRole('button', { name: 'Beziehung hinzufügen' })).toBeNull();
  });

  it('builds a related node with direction, depth and a filter of the related CI', () => {
    const { last } = setup();
    fireEvent.click(screen.getByRole('button', { name: 'Beziehung hinzufügen' }));
    const related = screen.getByRole('group', { name: 'Beziehung' });
    fireEvent.change(within(related).getByLabelText('Richtung'), { target: { value: 'in' } });
    const depth = within(related).getByLabelText('Tiefe (1–5)');
    fireEvent.change(depth, { target: { value: '9' } });
    expect(depth).toHaveValue(5);
    // Without a filter of the related CI the AST does not go out.
    expect(last().ok).toBe(false);
    fireEvent.click(within(related).getByRole('button', { name: 'Bedingung hinzufügen' }));
    fireEvent.change(within(related).getByLabelText('Feld'), { target: { value: 'ci_type' } });
    fireEvent.change(within(related).getByLabelText('Operator'), {
      target: { value: 'exists' },
    });
    expect(last()).toEqual({
      ok: true,
      where: {
        and: [
          {
            related: {
              direction: 'in',
              max_depth: 5,
              where: { field: 'ci_type', op: 'exists', value: true },
            },
          },
        ],
      },
    });
  });

  it('offers the operators of each field kind (validateComparison)', () => {
    const spec = (field: string, kind: FilterEntityKind = 'ci') =>
      fields[kind].find((f) => f.field === field)!;
    expect(opsFor(spec('client_id'))).not.toContain('lt');
    expect(opsFor(spec('is_manual'))).toEqual(['eq', 'ne', 'in', 'exists']);
    expect(opsFor(spec('name'))).toContain('like');
    expect(opsFor(spec('created_at'))).not.toContain('like');
    expect(opsFor(spec('created_at'))).toContain('between');
    expect(opsFor(spec('purchase_price', 'asset'))).toContain('between');
    expect(opsFor(spec('tags'))).toEqual(['has_any', 'has_all']);
    // Without a picker there is no free id input: exists only.
    expect(opsFor(spec('supplier_id', 'asset'))).toEqual(['exists']);
    for (const [kind, ops] of Object.entries(OPS_BY_KIND)) {
      if (kind === 'uuid' || kind === 'bool') {
        for (const op of ['lt', 'lte', 'gt', 'gte']) expect(ops).not.toContain(op);
      }
      if (kind !== 'text') expect(ops).not.toContain('like');
    }

    setup();
    fireEvent.click(screen.getByRole('button', { name: 'Bedingung hinzufügen' }));
    fireEvent.change(screen.getByLabelText('Feld'), { target: { value: 'site_id' } });
    expect(optionValues(screen.getByLabelText('Operator'))).toEqual(['eq', 'ne', 'in', 'exists']);
    fireEvent.change(screen.getByLabelText('Feld'), { target: { value: 'hostname' } });
    expect(optionValues(screen.getByLabelText('Operator'))).toContain('like');
  });

  it('needs at least one value for in and takes true or false for exists', () => {
    const { last } = setup();
    fireEvent.click(screen.getByRole('button', { name: 'Bedingung hinzufügen' }));
    fireEvent.change(screen.getByLabelText('Feld'), { target: { value: 'hostname' } });
    fireEvent.change(screen.getByLabelText('Operator'), { target: { value: 'in' } });
    expect(last()).toMatchObject({ ok: false, issue: 'list_empty' });
    const add = screen.getByLabelText('Wert hinzufügen');
    fireEvent.change(add, { target: { value: 'web-01' } });
    fireEvent.keyDown(add, { key: 'Enter' });
    fireEvent.change(screen.getByLabelText('Wert hinzufügen'), { target: { value: 'web-02' } });
    fireEvent.click(screen.getByRole('button', { name: 'Hinzufügen' }));
    expect(last()).toEqual({
      ok: true,
      where: { and: [{ field: 'hostname', op: 'in', value: ['web-01', 'web-02'] }] },
    });
    fireEvent.click(screen.getByRole('button', { name: 'web-01 entfernen' }));
    fireEvent.click(screen.getByRole('button', { name: 'web-02 entfernen' }));
    expect(last()).toMatchObject({ ok: false, issue: 'list_empty' });

    fireEvent.change(screen.getByLabelText('Operator'), { target: { value: 'exists' } });
    expect(optionValues(screen.getByLabelText('Wert'))).toEqual(['true', 'false']);
    fireEvent.change(screen.getByLabelText('Wert'), { target: { value: 'false' } });
    expect(last()).toEqual({
      ok: true,
      where: { and: [{ field: 'hostname', op: 'exists', value: false }] },
    });
  });

  it('cannot create the 11th level or the 101st node', () => {
    setup({ initialValue: nested(10) });
    // Nine groups and a comparison on level 10; a group on level 10 is possible …
    const groups = screen.getAllByRole('group', { name: 'Gruppe (UND)' });
    expect(groups).toHaveLength(9);
    const innermost = groups.slice(-1)[0]!;
    fireEvent.click(
      within(innermost).getAllByRole('button', { name: 'Gruppe hinzufügen' }).slice(-1)[0]!,
    );
    const level10 = screen.getAllByRole('group', { name: 'Gruppe (UND)' }).slice(-1)[0]!;
    // … but nothing below it: that would be level 11.
    for (const name of ['Bedingung hinzufügen', 'Gruppe hinzufügen', 'Beziehung hinzufügen']) {
      expect(within(level10).getByRole('button', { name })).toBeDisabled();
    }
    // Negating the comparison on level 10 would put it on level 11.
    expect(screen.getAllByRole('button', { name: 'Bedingung 1 negieren' })[0]).toBeDisabled();
    expect(screen.getByText('11 von 100 Knoten · Tiefe 10 von 10')).toBeInTheDocument();
  });

  // Role queries are slow on 100 nodes; the buttons are found by their text.
  it('stops at 100 nodes', { timeout: 20_000 }, () => {
    const hundred: FilterExpr = {
      and: Array.from({ length: 99 }, (_, i) => ({ field: 'name', op: 'eq', value: `n${i}` })),
    };
    const { last } = setup({ initialValue: hundred });
    expect(last()).toEqual({ ok: true, where: hundred });
    const button = (text: string) => screen.getByText(text).closest('button');
    expect(button('Bedingung hinzufügen')).toBeDisabled();
    expect(button('Gruppe hinzufügen')).toBeDisabled();
    expect(button('Beziehung hinzufügen')).toBeDisabled();
    expect(document.querySelector('[aria-label="Bedingung 1 negieren"]')).toBeDisabled();
    expect(screen.getByText('100 von 100 Knoten · Tiefe 2 von 10')).toBeInTheDocument();
  });

  it('sends numbers as JSON numbers and dates as ISO 8601', () => {
    const { last } = setup();
    fireEvent.click(screen.getByRole('button', { name: 'Bedingung hinzufügen' }));
    fireEvent.change(screen.getByLabelText('Feld'), { target: { value: 'attributes.ram_gb' } });
    fireEvent.change(screen.getByLabelText('Operator'), { target: { value: 'gte' } });
    fireEvent.change(screen.getByLabelText('Wert'), { target: { value: '16' } });
    expect(last()).toEqual({
      ok: true,
      where: { and: [{ field: 'attributes.ram_gb', op: 'gte', value: 16 }] },
    });

    fireEvent.change(screen.getByLabelText('Feld'), {
      target: { value: 'asset.warranty_until' },
    });
    fireEvent.change(screen.getByLabelText('Wert'), { target: { value: '2027-03-31' } });
    expect(last()).toEqual({
      ok: true,
      where: { and: [{ field: 'asset.warranty_until', op: 'gte', value: '2027-03-31' }] },
    });

    fireEvent.change(screen.getByLabelText('Feld'), { target: { value: 'created_at' } });
    fireEvent.change(screen.getByLabelText('Wert'), { target: { value: '2026-05-01T08:30' } });
    const value = (last() as { where: { and: Array<{ value: string }> } }).where.and[0]!.value;
    expect(value).toBe(new Date('2026-05-01T08:30').toISOString());
  });

  it('labels attributes with display_name in the UI language and offers searchable ones only', () => {
    setup();
    fireEvent.click(screen.getByRole('button', { name: 'Bedingung hinzufügen' }));
    const field = screen.getByLabelText('Feld');
    expect(within(field).getByRole('option', { name: 'Attribut: ram_gb (de)' })).toHaveValue(
      'attributes.ram_gb',
    );
    expect(optionValues(field)).toContain('asset.attributes.ram_gb');
    expect(optionValues(field)).toContain('asset.warranty_until');
    expect(optionValues(field)).not.toContain('attributes.hidden_note');
    fireEvent.change(field, { target: { value: 'attributes.environment' } });
    expect(optionValues(screen.getByLabelText('Wert'))).toEqual(['', 'prod', 'test']);
  });

  it.each<[string, FilterEntityKind, FilterExpr]>([
    [
      'groups',
      'ci',
      {
        or: [
          { and: [{ field: 'status', op: 'eq', value: 'active' }] },
          { field: 'hostname', op: 'like', value: 'db*' },
        ],
      },
    ],
    ['not', 'ci', { not: { field: 'is_manual', op: 'eq', value: true } }],
    ['in', 'ci', { field: 'ci_type', op: 'in', value: ['server', 'vm'] }],
    ['exists false', 'ci', { field: 'hostname', op: 'exists', value: false }],
    ['exists without value', 'ci', { field: 'hostname', op: 'exists' }],
    [
      'asset.warranty_end',
      'ci',
      { and: [{ field: 'asset.warranty_end', op: 'lt', value: '2026-12-31' }] },
    ],
    ['attribute with a number', 'ci', { field: 'attributes.ram_gb', op: 'gt', value: 16 }],
    [
      'between and tags',
      'asset',
      {
        and: [
          { field: 'purchase_price', op: 'between', value: [100, 2000.5] },
          { field: 'tags', op: 'has_any', value: ['pci', 'gdpr'] },
        ],
      },
    ],
    [
      'related',
      'ci',
      {
        related: {
          direction: 'out',
          rel_types: ['runs_on'],
          max_depth: 2,
          where: { field: 'location_id', op: 'exists', value: true },
        },
      },
    ],
  ])('loads %s and gives it back unchanged', (_name, entityKind, expr) => {
    const { last } = setup({ entityKind, initialValue: expr });
    expect(last()).toEqual({ ok: true, where: expr });
    expect(toFilter(fromFilter(expr))).toEqual(expr);
  });

  it('marks the node of a 422 path and the comparisons of refused fields', () => {
    const expr: FilterExpr = {
      and: [
        { field: 'name', op: 'eq', value: 'a' },
        { field: 'hostname', op: 'eq', value: 'b' },
      ],
    };
    setup({
      initialValue: expr,
      serverError: { path: 'where.and[1].value', message: 'must be a string' },
      forbiddenFields: ['name'],
    });
    const first = screen.getByRole('group', { name: 'Bedingung 1' });
    const second = screen.getByRole('group', { name: 'Bedingung 2' });
    expect(second).toHaveAttribute('data-invalid', 'true');
    expect(second).toHaveAccessibleDescription('Vom Server abgelehnt: must be a string');
    expect(first).toHaveAccessibleDescription('Keine Berechtigung für das Feld name.');
    // The refused field is no longer offered, except as the value it has.
    expect(optionValues(within(second).getByLabelText('Feld'))).not.toContain('name');
    expect(optionValues(within(first).getByLabelText('Feld'))).toContain('name');
  });

  it('is operable by keyboard and moves the focus to the neighbour after a delete', async () => {
    const user = userEvent.setup();
    setup({
      initialValue: {
        and: [
          { field: 'name', op: 'eq', value: 'a' },
          { field: 'hostname', op: 'eq', value: 'b' },
          { field: 'fqdn', op: 'eq', value: 'c' },
        ],
      },
    });
    // The group names its junction; every comparison has labelled inputs.
    const root = screen.getByRole('group', { name: 'Gruppe (UND)' });
    expect(within(root).getByLabelText('Verknüpfung')).toHaveValue('and');
    expect(screen.getAllByLabelText('Feld')).toHaveLength(3);
    expect(screen.getAllByLabelText('Operator')).toHaveLength(3);
    expect(screen.getAllByLabelText('Wert')).toHaveLength(3);

    // Delete the second comparison by keyboard: the focus goes to the next one.
    screen.getByRole('button', { name: 'Bedingung 2 löschen' }).focus();
    await user.keyboard('{Enter}');
    expect(screen.getAllByLabelText('Feld')).toHaveLength(2);
    const next = screen.getByRole('group', { name: 'Bedingung 2' });
    expect(within(next).getByLabelText('Feld')).toHaveValue('fqdn');
    expect(next.contains(document.activeElement)).toBe(true);

    // The last one: the focus goes to the previous one.
    screen.getByRole('button', { name: 'Bedingung 2 löschen' }).focus();
    await user.keyboard('{Enter}');
    const previous = screen.getByRole('group', { name: 'Bedingung 1' });
    expect(previous.contains(document.activeElement)).toBe(true);

    // Add by keyboard: the new comparison takes the focus.
    const add = within(root).getAllByRole('button', { name: 'Bedingung hinzufügen' }).slice(-1)[0]!;
    add.focus();
    await user.keyboard('{Enter}');
    const added = screen.getByRole('group', { name: 'Bedingung 2' });
    expect(added.contains(document.activeElement)).toBe(true);

    // Switch the junction by keyboard (arrow keys of the select).
    within(root).getByLabelText('Verknüpfung').focus();
    await user.keyboard('{ArrowDown}');
    expect(screen.getByRole('group', { name: 'Gruppe (ODER)' })).toBeInTheDocument();
  });
});

describe('builderModes', () => {
  it('mirrors the whitelist astFields of the server', () => {
    expect(Object.keys(FILTER_FIELDS.ci)).toContain('owner_team_id');
    expect(FILTER_FIELDS.ci.is_manual).toBe('bool');
    expect(FILTER_FIELDS.asset.purchase_price).toBe('number');
    expect(FILTER_FIELDS.asset.warranty_end).toBe('time');
  });

  it('offers asset.* only with the page assets', () => {
    const without = fieldCatalog('ci', { definitions, assets: false }).map((f) => f.field);
    expect(without.some((f) => f.startsWith('asset.'))).toBe(false);
    expect(fieldCatalog('ci', { definitions }).some((f) => f.field === 'asset.name')).toBe(true);
  });

  it('maps a server path to the deepest node', () => {
    const tree = fromFilter({
      and: [
        { field: 'name', op: 'eq', value: 'a' },
        { or: [{ not: { field: 'fqdn', op: 'exists' } }] },
      ],
    });
    const paths = toFilter(tree);
    expect(paths).toBeTruthy();
    const comparison =
      tree.kind === 'group' && tree.children[1]!.kind === 'group'
        ? tree.children[1]!.children[0]!
        : undefined;
    expect(nodeAtPath(tree, 'filter_spec.where.and[1].or[0].value')).toBe(comparison?.id);
    expect(nodeAtPath(tree, 'where.and[10]')).toBe(tree.id);
  });

  it('refuses an incomplete tree and an empty group below the root', () => {
    const empty = newGroup('and');
    expect(checkTree(empty, 'ci', fields)).toEqual({ ok: true, where: undefined });
    const tree = fromFilter({ and: [{ or: [] }] } as unknown as FilterExpr);
    expect(checkTree(tree, 'ci', fields)).toMatchObject({ ok: false, issue: 'empty_group' });
  });
});
