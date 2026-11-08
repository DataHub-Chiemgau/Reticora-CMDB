import { describe, expect, it } from 'vitest';
import {
  compareOps,
  evaluateCondition,
  evaluateRule,
  parseCondition,
  parseRule,
  type Condition,
} from './rules';
import { EXPR_OPS, evalExpr, exprFields, parseExpr, type Expr } from './expr';

// Loaded at test time: the frontend image is built from frontend/ alone, so
// the type check must not depend on the file outside it.
const conformance = Object.values(
  import.meta.glob<{
    cases: unknown[];
    invalid_rules: unknown[];
    condition_cases: unknown[];
    invalid_conditions: unknown[];
    expressions: unknown[];
    invalid_expressions: unknown[];
  }>('../../../api/rules.conformance.json', { eager: true, import: 'default' }),
)[0]!;

interface Case {
  name: string;
  rule: unknown;
  value?: unknown;
  absent?: boolean;
  violations: string[];
}

// The shared cases of api/rules.conformance.json (MET-45); the Go package
// backend/internal/rules runs the same file.
describe('rule AST conformance', () => {
  const cases = conformance.cases as Case[];
  const invalid = conformance.invalid_rules as { name: string; rule: unknown }[];

  it('has cases', () => {
    expect(cases.length).toBeGreaterThan(0);
    expect(invalid.length).toBeGreaterThan(0);
  });

  it.each(cases.map((c) => [c.name, c] as const))('%s', (_name, c) => {
    const rule = parseRule(c.rule);
    const value = c.absent ? undefined : c.value === undefined ? null : c.value;
    expect(evaluateRule(rule, value).map((v) => v.code)).toEqual(c.violations);
  });

  it.each(invalid.map((c) => [c.name, c] as const))('%s', (_name, c) => {
    expect(() => parseRule(c.rule)).toThrow();
  });
});

interface ConditionCase {
  name: string;
  condition: unknown;
  values: Record<string, unknown>;
  result: boolean;
}

// condition_cases and invalid_conditions (WP-715), the same cases as
// TestConformance in backend/internal/rules.
describe('condition conformance', () => {
  const cases = conformance.condition_cases as ConditionCase[];
  const invalid = conformance.invalid_conditions as {
    name: string;
    condition: unknown;
    path: string;
  }[];

  it('has a case for every operator', () => {
    const seen = new Set<string>();
    const walk = (c: Condition): void => {
      seen.add(c.op);
      if ('rules' in c) c.rules.forEach(walk);
    };
    cases.forEach((c) => walk(parseCondition(c.condition)));
    expect([...seen].sort()).toEqual(['all', 'any', 'not', ...compareOps].sort());
    expect(invalid.length).toBeGreaterThan(0);
  });

  it.each(cases.map((c) => [c.name, c] as const))('%s', (_name, c) => {
    expect(evaluateCondition(parseCondition(c.condition), c.values)).toBe(c.result);
  });

  it.each(invalid.map((c) => [c.name, c] as const))('%s', (_name, c) => {
    expect(() => parseCondition(c.condition)).toThrow(
      new RegExp(`^${c.path.replace(/[.[\]]/g, '\\$&')}: `),
    );
  });

  it('keeps value rules and conditions apart', () => {
    expect(() => parseRule({ op: 'eq', field: 'env', value: 'prod' })).toThrow();
    expect(() => parseRule({ op: 'any', rules: [] })).toThrow();
    for (const op of ['required', 'pattern', 'range', 'enum']) {
      expect(() => parseCondition({ op })).toThrow(/^op: /);
    }
  });
});

interface ExpressionCase {
  name: string;
  expr: unknown;
  inputs: Record<string, unknown>;
  now: string;
  timezone: string;
  result: unknown;
}

// expressions and invalid_expressions (WP-258, WP-802): the same cases as
// TestExpressionConformance in backend/internal/rules (MET-45), with now and
// the time zone of each case (today across midnight, days_between).
describe('expression conformance', () => {
  const cases = conformance.expressions as ExpressionCase[];
  const invalid = conformance.invalid_expressions as {
    name: string;
    expr: unknown;
    path: string;
  }[];

  it('has a case for every operator', () => {
    const seen = new Set<string>();
    const walk = (e: Expr): void => {
      seen.add(e.op);
      if ('args' in e) e.args.forEach(walk);
    };
    cases.forEach((c) => walk(parseExpr(c.expr)));
    expect([...seen].sort()).toEqual([...EXPR_OPS].sort());
    expect(invalid.length).toBeGreaterThan(0);
  });

  it.each(cases.map((c) => [c.name, c] as const))('%s', (_name, c) => {
    const e = parseExpr(c.expr);
    expect(evalExpr(e, c.inputs, new Date(c.now), c.timezone)).toEqual(c.result);
    // A valid expression survives the round trip unchanged.
    expect(JSON.parse(JSON.stringify(e))).toEqual(c.expr);
  });

  it.each(invalid.map((c) => [c.name, c] as const))('%s', (_name, c) => {
    expect(() => parseExpr(c.expr)).toThrow(new RegExp(`^${c.path.replace(/[.[\]]/g, '\\$&')}: `));
  });

  it('keeps expressions apart from rules and conditions', () => {
    expect(() => parseRule({ op: 'add', args: [] })).toThrow();
    expect(() => parseCondition({ op: 'today' })).toThrow(/^op: /);
    for (const op of ['required', 'all', 'eq', 'like']) {
      expect(() => parseExpr({ op })).toThrow(/^op: /);
    }
  });

  it('lists the references and bounds the depth', () => {
    const e = parseExpr({
      op: 'add',
      args: [
        { op: 'field', field: 'b' },
        {
          op: 'days_between',
          args: [{ op: 'today' }, { op: 'field', field: 'asset.warranty_end' }],
        },
        { op: 'field', field: 'b' },
      ],
    });
    expect(exprFields(e)).toEqual(['asset.warranty_end', 'b']);
    let deep: unknown = { op: 'literal', value: 1 };
    for (let i = 0; i <= 32; i++) deep = { op: 'lower', args: [deep] };
    expect(() => parseExpr(deep)).toThrow(/nested deeper than 32/);
    // An unknown zone counts as UTC, a missing input of today's date stays empty.
    const today = parseExpr({ op: 'today' });
    expect(evalExpr(today, {}, new Date('2026-03-01T23:30:00Z'), 'Nowhere/City')).toBe(
      '2026-03-01',
    );
    expect(
      evalExpr(
        parseExpr({ op: 'days_between', args: [{ op: 'today' }, { op: 'field', field: 'd' }] }),
        { d: '2026-02-30' },
        new Date('2026-03-01T12:00:00Z'),
        'UTC',
      ),
    ).toBeNull();
  });
});
