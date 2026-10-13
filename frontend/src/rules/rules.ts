/**
 * rules — preview evaluation of the JSON rule language of the metamodel
 * (MET-45, api/rules.schema.json). The binding evaluation is the Go package
 * backend/internal/rules; both pass the cases of api/rules.conformance.json.
 * Phase 1: required, pattern, range (value or length) and enum, combined
 * with all.
 */

export type Rule =
  | { op: 'all'; rules: Rule[] }
  | { op: 'required' }
  | { op: 'pattern'; pattern: string }
  | { op: 'range'; of?: 'value' | 'length'; min?: number; max?: number }
  | { op: 'enum'; values: (string | number | boolean)[] };

export type ViolationCode =
  'required' | 'pattern' | 'pattern.type' | 'range.min' | 'range.max' | 'range.type' | 'enum';

export interface Violation {
  code: ViolationCode;
  message: string;
}

const allowedKeys: Record<string, string[]> = {
  all: ['op', 'rules'],
  required: ['op'],
  pattern: ['op', 'pattern'],
  range: ['op', 'of', 'min', 'max'],
  enum: ['op', 'values'],
};

// Lookaround and backreferences are outside the common subset of RE2 and
// ECMAScript.
const unsupported = /\(\?<?[=!]|\\[1-9]/;

function compile(pattern: string): RegExp {
  return new RegExp(pattern, 'u');
}

/** Checks a rule strictly; throws on an invalid rule. */
export function parseRule(input: unknown): Rule {
  if (typeof input !== 'object' || input === null || Array.isArray(input)) {
    throw new Error('a rule is an object');
  }
  const r = input as Record<string, unknown>;
  const op = r.op;
  if (typeof op !== 'string' || !(op in allowedKeys)) {
    throw new Error(`unknown operator ${String(op)}`);
  }
  for (const key of Object.keys(r)) {
    if (!allowedKeys[op]!.includes(key)) throw new Error(`${op}: unknown property ${key}`);
  }
  switch (op) {
    case 'all':
      if (!Array.isArray(r.rules)) throw new Error('all: rules are required');
      return { op, rules: r.rules.map((child) => parseRule(child)) };
    case 'required':
      return { op };
    case 'pattern': {
      if (typeof r.pattern !== 'string' || r.pattern === '')
        throw new Error('pattern: the pattern is required');
      if (unsupported.test(r.pattern))
        throw new Error('pattern: lookaround and backreferences are not supported');
      compile(r.pattern);
      return { op, pattern: r.pattern };
    }
    case 'range': {
      if (r.of !== undefined && r.of !== 'value' && r.of !== 'length')
        throw new Error('range: of must be value or length');
      for (const bound of ['min', 'max'] as const) {
        if (r[bound] !== undefined && typeof r[bound] !== 'number')
          throw new Error(`range: ${bound} is a number`);
      }
      const min = r.min as number | undefined;
      const max = r.max as number | undefined;
      if (min === undefined && max === undefined) throw new Error('range: min or max is required');
      if (min !== undefined && max !== undefined && min > max)
        throw new Error('range: min is above max');
      return { op, of: r.of as 'value' | 'length' | undefined, min, max };
    }
    default: {
      const values = r.values;
      if (!Array.isArray(values) || values.length === 0)
        throw new Error('enum: values are required');
      for (const v of values) {
        if (!['string', 'number', 'boolean'].includes(typeof v))
          throw new Error('enum: values are strings, numbers or booleans');
      }
      return { op: 'enum', values: values as (string | number | boolean)[] };
    }
  }
}

/** Absent, null, whitespace-only strings and empty arrays are empty. */
export function isEmptyValue(value: unknown): boolean {
  if (value === undefined || value === null) return true;
  if (typeof value === 'string') return value.trim() === '';
  if (Array.isArray(value)) return value.length === 0;
  return false;
}

function check(rule: Rule, value: unknown): Violation | null {
  switch (rule.op) {
    case 'pattern':
      if (typeof value !== 'string')
        return { code: 'pattern.type', message: 'a text value is expected' };
      return compile(rule.pattern).test(value)
        ? null
        : { code: 'pattern', message: `value does not match pattern ${rule.pattern}` };
    case 'range': {
      let n: number;
      if (rule.of === 'length') {
        if (typeof value !== 'string')
          return { code: 'range.type', message: 'a text value is expected' };
        n = [...value].length; // code points, like Go's rune count
      } else {
        if (typeof value !== 'number' || Number.isNaN(value))
          return { code: 'range.type', message: 'a number is expected' };
        n = value;
      }
      const unit = rule.of === 'length' ? ' characters' : '';
      if (rule.min !== undefined && n < rule.min)
        return { code: 'range.min', message: `must be at least ${rule.min}${unit}` };
      if (rule.max !== undefined && n > rule.max)
        return { code: 'range.max', message: `must be at most ${rule.max}${unit}` };
      return null;
    }
    case 'enum':
      return rule.values.some((allowed) => allowed === value)
        ? null
        : { code: 'enum', message: `value ${String(value)} is not one of the allowed values` };
    default:
      return null;
  }
}

/**
 * The violations of a value; pass undefined for an absent value. The rule is
 * assumed valid (parseRule).
 */
export function evaluateRule(rule: Rule, value: unknown): Violation[] {
  if (rule.op === 'all') return rule.rules.flatMap((child) => evaluateRule(child, value));
  if (rule.op === 'required')
    return isEmptyValue(value) ? [{ code: 'required', message: 'a value is required' }] : [];
  if (isEmptyValue(value)) return [];
  for (const item of Array.isArray(value) ? value : [value]) {
    const violation = check(rule, item);
    if (violation) return [violation];
  }
  return [];
}
