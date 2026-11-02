import { describe, expect, it } from 'vitest';
import {
  compareOps,
  evaluateCondition,
  evaluateRule,
  parseCondition,
  parseRule,
  type Condition,
} from './rules';

// Loaded at test time: the frontend image is built from frontend/ alone, so
// the type check must not depend on the file outside it.
const conformance = Object.values(
  import.meta.glob<{
    cases: unknown[];
    invalid_rules: unknown[];
    condition_cases: unknown[];
    invalid_conditions: unknown[];
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
