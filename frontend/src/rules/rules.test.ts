import { describe, expect, it } from 'vitest';
import conformance from '../../../api/rules.conformance.json';
import { evaluateRule, parseRule } from './rules';

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
