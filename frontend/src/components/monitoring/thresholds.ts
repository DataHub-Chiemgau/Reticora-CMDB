import type { AlertRule, AlertRuleCondition } from '../../api/client';
import type { Condition } from '../../rules/rules';

/**
 * Threshold lines of the metric chart (MON-03, WP-990 AK5): for each rule
 * of the queried metric one line for its warning and one for its critical
 * stage, when the stage is a single comparison (gt, gte, lt, lte) of value,
 * avg, max or min with a number. Any other stage (any/all of several, not,
 * rate, delta …) has no line; the chart names it instead.
 */

/** The stages that get a line, highest first. */
export const LINE_STAGES = ['critical', 'warning'] as const;
export type LineStage = (typeof LINE_STAGES)[number];

/** The fields of the metric context a line can stand for. */
export const LINE_FIELDS = ['value', 'avg', 'max', 'min'] as const;

const LINE_OPS = ['gt', 'gte', 'lt', 'lte'] as const;
type LineOp = (typeof LINE_OPS)[number];

/** A threshold line: the rule, its stage and the compared value. */
export interface ThresholdLine {
  ruleId: string;
  ruleName: string;
  stage: LineStage;
  field: (typeof LINE_FIELDS)[number];
  op: LineOp;
  value: number;
  /**
   * scopeUnchecked: the rule is restricted by CI type, client, site or
   * labels, which the chart cannot check for the queried series; the line
   * is drawn and named as such.
   */
  scopeUnchecked: boolean;
}

/** A stage of a rule that has no line (its condition is no single comparison). */
export interface SkippedStage {
  ruleId: string;
  ruleName: string;
  stage: LineStage;
}

/**
 * singleComparison returns the comparison of a stage made of one comparison
 * of a line field, also wrapped in an all/any group of that one comparison
 * (the form the builder emits); null for every other condition.
 */
export function singleComparison(
  condition: AlertRuleCondition | undefined,
): { field: ThresholdLine['field']; op: LineOp; value: number } | null {
  let c: Condition | null | undefined = condition;
  while (c && (c.op === 'all' || c.op === 'any') && 'rules' in c && c.rules.length === 1) {
    c = c.rules[0];
  }
  if (!c || !('field' in c)) return null;
  if (!(LINE_OPS as readonly string[]).includes(c.op)) return null;
  if (!(LINE_FIELDS as readonly string[]).includes(c.field)) return null;
  const value = (c as { value?: unknown }).value;
  if (typeof value !== 'number' || !Number.isFinite(value)) return null;
  return { field: c.field as ThresholdLine['field'], op: c.op as LineOp, value };
}

/**
 * scopeMatch tells whether a rule applies to the series of ciId: a rule
 * whose scope names CIs applies only to those ("no"); a restriction by CI
 * type, client, site or labels cannot be checked here ("unchecked").
 */
export function scopeMatch(rule: AlertRule, ciId: string | undefined): 'yes' | 'no' | 'unchecked' {
  const s = rule.scope ?? {};
  if (s.ci_ids?.length) {
    if (!ciId) return 'unchecked';
    if (!s.ci_ids.includes(ciId)) return 'no';
  }
  if (s.ci_type_ids?.length || s.client_id || s.site_id || Object.keys(s.labels ?? {}).length) {
    return 'unchecked';
  }
  return 'yes';
}

/**
 * thresholdLines derives the lines of the rules of metric for the series
 * of ciId: a rule of another metric, a disabled one or one whose CIs do not
 * include ciId contributes nothing; a warning or critical stage without a
 * single comparison is reported in skipped.
 */
export function thresholdLines(
  rules: readonly AlertRule[],
  metric: string,
  ciId?: string,
): { lines: ThresholdLine[]; skipped: SkippedStage[] } {
  const lines: ThresholdLine[] = [];
  const skipped: SkippedStage[] = [];
  for (const rule of rules) {
    if (rule.metric_name !== metric || rule.enabled === false) continue;
    const match = scopeMatch(rule, ciId);
    if (match === 'no') continue;
    for (const stage of LINE_STAGES) {
      const condition = stage === 'critical' ? rule.critical_condition : rule.warning_condition;
      if (!condition) continue;
      const cmp = singleComparison(condition);
      if (cmp) {
        lines.push({
          ruleId: rule.id,
          ruleName: rule.name,
          stage,
          ...cmp,
          scopeUnchecked: match === 'unchecked',
        });
      } else {
        skipped.push({ ruleId: rule.id, ruleName: rule.name, stage });
      }
    }
  }
  return { lines, skipped };
}

/** opSymbol is the short form of a comparison operator. */
export function opSymbol(op: string): string {
  switch (op) {
    case 'gt':
      return '>';
    case 'gte':
      return '≥';
    case 'lt':
      return '<';
    case 'lte':
      return '≤';
    case 'eq':
      return '=';
    case 'ne':
      return '≠';
    default:
      return op;
  }
}

/**
 * summarizeCondition renders a stage in short form for the rule list
 * (e.g. "avg > 90", "any(value > 90, rate > 5)"); null is "".
 */
export function summarizeCondition(condition: AlertRuleCondition | undefined): string {
  const walk = (c: Condition): string => {
    if ('rules' in c) {
      const inner = c.rules.map(walk).join(', ');
      if (c.op === 'not') return `not(${inner})`;
      return c.rules.length === 1 ? inner : `${c.op}(${inner})`;
    }
    if (c.op === 'empty' || c.op === 'not_empty') return `${c.field} ${c.op}`;
    const raw = (c as { value: unknown }).value;
    const value = Array.isArray(raw) ? `[${raw.join(', ')}]` : String(raw);
    return `${c.field} ${opSymbol(c.op)} ${value}`;
  };
  return condition ? walk(condition) : '';
}
