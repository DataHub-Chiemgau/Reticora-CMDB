import { describe, expect, it } from 'vitest';
import type { AlertRule } from '../../api/client';
import { singleComparison, summarizeCondition, thresholdLines } from './thresholds';

function rule(partial: Partial<AlertRule>): AlertRule {
  return {
    id: 'r1',
    org_id: 'o1',
    name: 'cpu',
    metric_name: 'cpu.load',
    duration: '5m',
    enabled: true,
    critical_condition: null,
    warning_condition: null,
    info_condition: null,
    clear_condition: null,
    eval_window: '5m',
    ...partial,
  } as AlertRule;
}

describe('threshold lines (WP-990 AK5)', () => {
  it('a single comparison gives a line', () => {
    expect(singleComparison({ op: 'gt', field: 'avg', value: 90 })).toEqual({
      field: 'avg',
      op: 'gt',
      value: 90,
    });
    // The group of one comparison the builder emits.
    expect(singleComparison({ op: 'all', rules: [{ op: 'lte', field: 'min', value: 5 }] })).toEqual(
      {
        field: 'min',
        op: 'lte',
        value: 5,
      },
    );
    const { lines, skipped } = thresholdLines(
      [
        rule({
          critical_condition: { op: 'gt', field: 'value', value: 95 },
          warning_condition: { op: 'all', rules: [{ op: 'gte', field: 'avg', value: 80 }] },
        }),
      ],
      'cpu.load',
    );
    expect(lines.map((l) => [l.stage, l.field, l.op, l.value])).toEqual([
      ['critical', 'value', 'gt', 95],
      ['warning', 'avg', 'gte', 80],
    ]);
    expect(skipped).toEqual([]);
  });

  it('any, not and rate give no line', () => {
    expect(
      singleComparison({
        op: 'any',
        rules: [
          { op: 'gt', field: 'value', value: 90 },
          { op: 'gt', field: 'max', value: 99 },
        ],
      }),
    ).toBeNull();
    expect(
      singleComparison({ op: 'not', rules: [{ op: 'gt', field: 'value', value: 90 }] }),
    ).toBeNull();
    expect(singleComparison({ op: 'gt', field: 'rate', value: 5 })).toBeNull();
    expect(singleComparison({ op: 'eq', field: 'value', value: 1 })).toBeNull();
    const { lines, skipped } = thresholdLines(
      [
        rule({ critical_condition: { op: 'gt', field: 'rate', value: 5 } }),
        rule({
          id: 'r2',
          metric_name: 'other',
          critical_condition: { op: 'gt', field: 'value', value: 1 },
        }),
        rule({
          id: 'r3',
          enabled: false,
          warning_condition: { op: 'gt', field: 'value', value: 1 },
        }),
      ],
      'cpu.load',
    );
    expect(lines).toEqual([]);
    expect(skipped).toEqual([{ ruleId: 'r1', ruleName: 'cpu', stage: 'critical' }]);
  });

  it('draws no line for a rule whose CIs do not include the queried CI', () => {
    const stages = { critical_condition: { op: 'gt' as const, field: 'value', value: 90 } };
    const rules = [
      rule({ id: 'a', name: 'only-a', scope: { ci_ids: ['ci-a'] }, ...stages }),
      rule({ id: 't', name: 'by-type', scope: { ci_type_ids: ['type-1'] }, ...stages }),
      rule({ id: 'all', name: 'all', ...stages }),
    ];
    const forB = thresholdLines(rules, 'cpu.load', 'ci-b').lines;
    expect(forB.map((l) => [l.ruleId, l.scopeUnchecked])).toEqual([
      ['t', true],
      ['all', false],
    ]);
    const forA = thresholdLines(rules, 'cpu.load', 'ci-a').lines;
    expect(forA.map((l) => [l.ruleId, l.scopeUnchecked])).toEqual([
      ['a', false],
      ['t', true],
      ['all', false],
    ]);
  });

  it('summarizes a stage in short form', () => {
    expect(summarizeCondition({ op: 'gt', field: 'avg', value: 90 })).toBe('avg > 90');
    expect(
      summarizeCondition({
        op: 'any',
        rules: [
          { op: 'gt', field: 'value', value: 90 },
          { op: 'lte', field: 'rate', value: 5 },
        ],
      }),
    ).toBe('any(value > 90, rate ≤ 5)');
    expect(summarizeCondition(null)).toBe('');
  });
});
