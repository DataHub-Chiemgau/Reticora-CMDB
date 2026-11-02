/**
 * rules — preview evaluation of the JSON rule language of the metamodel
 * (MET-45, api/rules.schema.json). The binding evaluation is the Go package
 * backend/internal/rules; both pass the cases of api/rules.conformance.json.
 * A value rule checks one value: required, pattern, range (value or length)
 * and enum, combined with all. A condition (WP-715) is true or false for a
 * record: comparisons of a field (the semantics of fieldmeta.Predicate)
 * combined with all, any and not; it is the common form of metamodel
 * conditions, guards and filters (CH18).
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

// ─── Conditions (WP-715) ─────────────────────────────────────────────────────

export type Scalar = string | number | boolean;

export type CompareOp =
  'eq' | 'ne' | 'in' | 'not_in' | 'gt' | 'gte' | 'lt' | 'lte' | 'contains' | 'empty' | 'not_empty';

export type Comparison =
  | { op: 'empty' | 'not_empty'; field: string }
  | { op: 'in' | 'not_in'; field: string; value: Scalar[] }
  | { op: 'gt' | 'gte' | 'lt' | 'lte'; field: string; value: number }
  | { op: 'eq' | 'ne' | 'contains'; field: string; value: Scalar };

export type Condition = { op: 'all' | 'any' | 'not'; rules: Condition[] } | Comparison;

const logicOps = ['all', 'any', 'not'];
export const compareOps: readonly CompareOp[] = [
  'eq',
  'ne',
  'in',
  'not_in',
  'gt',
  'gte',
  'lt',
  'lte',
  'contains',
  'empty',
  'not_empty',
];
const valueOps = ['required', 'pattern', 'range', 'enum'];

function at(path: string, prop: string): string {
  return path === '' ? prop : `${path}.${prop}`;
}

function isScalar(v: unknown): v is Scalar {
  return (
    typeof v === 'string' || typeof v === 'boolean' || (typeof v === 'number' && !Number.isNaN(v))
  );
}

function parseComparison(node: Record<string, unknown>, op: CompareOp, path: string): Comparison {
  const field = node.field;
  if (typeof field !== 'string' || field.trim() === '')
    throw new Error(`${at(path, 'field')}: ${op} requires a field`);
  const has = 'value' in node;
  const value = node.value;
  switch (op) {
    case 'empty':
    case 'not_empty':
      if (has) throw new Error(`${at(path, 'value')}: ${op} takes no value`);
      return { op, field };
    case 'in':
    case 'not_in':
      if (!Array.isArray(value) || value.length === 0)
        throw new Error(`${at(path, 'value')}: ${op} requires a non-empty array`);
      value.forEach((item, i) => {
        if (!isScalar(item))
          throw new Error(`${at(path, 'value')}[${i}]: a string, number or boolean is expected`);
      });
      return { op, field, value: value as Scalar[] };
    case 'gt':
    case 'gte':
    case 'lt':
    case 'lte':
      if (typeof value !== 'number' || Number.isNaN(value))
        throw new Error(`${at(path, 'value')}: ${op} requires a number`);
      return { op, field, value };
    default:
      if (!has || !isScalar(value))
        throw new Error(`${at(path, 'value')}: ${op} requires a string, number or boolean`);
      return { op, field, value };
  }
}

function parseNode(input: unknown, path: string): Condition {
  if (typeof input !== 'object' || input === null || Array.isArray(input)) {
    throw new Error(`${path === '' ? 'condition' : path}: a condition is an object`);
  }
  const node = input as Record<string, unknown>;
  const op = node.op;
  const isLogic = typeof op === 'string' && logicOps.includes(op);
  const isCompare = typeof op === 'string' && (compareOps as readonly string[]).includes(op);
  if (!isLogic && !isCompare) {
    if (typeof op === 'string' && valueOps.includes(op))
      throw new Error(`${at(path, 'op')}: ${op} is a value rule, not a condition`);
    throw new Error(`${at(path, 'op')}: unknown operator ${String(op)}`);
  }
  const keys = isLogic ? ['op', 'rules'] : ['op', 'field', 'value'];
  for (const key of Object.keys(node).sort()) {
    if (!keys.includes(key)) throw new Error(`${at(path, key)}: unknown property of ${String(op)}`);
  }
  if (!isLogic) return parseComparison(node, op as CompareOp, path);
  const list = node.rules;
  if (!Array.isArray(list))
    throw new Error(`${at(path, 'rules')}: ${String(op)} requires an array of rules`);
  if (op === 'not' && list.length !== 1)
    throw new Error(`${at(path, 'rules')}: not takes exactly one rule`);
  return {
    op: op as 'all' | 'any' | 'not',
    rules: list.map((child, i) => parseNode(child, at(path, `rules[${i}]`))),
  };
}

/**
 * Checks a condition strictly; throws on an invalid condition, the message
 * starting with the path of the failing property (e.g. rules[1].field).
 * Value operators (required, pattern, range, enum) are no conditions.
 */
export function parseCondition(input: unknown): Condition {
  return parseNode(input, '');
}

/** isEmptyValue plus the empty object, as in fieldmeta. */
function blank(v: unknown): boolean {
  if (typeof v === 'object' && v !== null && !Array.isArray(v))
    return Object.keys(v as object).length === 0;
  return isEmptyValue(v);
}

// A decimal number as text; other notations (hex, Infinity) are text in Go
// and TypeScript alike.
const numeric = /^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$/;

function looseNumber(v: unknown): number | undefined {
  if (typeof v === 'number') return Number.isNaN(v) ? undefined : v;
  if (typeof v !== 'string') return undefined;
  const s = v.trim();
  if (!numeric.test(s)) return undefined;
  const n = Number(s);
  return Number.isFinite(n) ? n : undefined;
}

function text(v: unknown): string | undefined {
  return isScalar(v) ? String(v) : undefined;
}

// Two empty values are equal, numbers compare by value (also as text),
// strings and booleans by their text; a non-empty array or object equals
// nothing.
function looseEqual(a: unknown, b: unknown): boolean {
  if (blank(a) && blank(b)) return true;
  const an = looseNumber(a);
  if (an !== undefined) {
    const bn = looseNumber(b);
    return bn !== undefined && an === bn;
  }
  const ta = text(a);
  const tb = text(b);
  return ta !== undefined && tb !== undefined && ta === tb;
}

function inList(actual: unknown, list: unknown): boolean {
  return Array.isArray(list) && list.some((item) => looseEqual(actual, item));
}

function compare(cond: Comparison, actual: unknown): boolean {
  switch (cond.op) {
    case 'empty':
      return blank(actual);
    case 'not_empty':
      return !blank(actual);
    case 'eq':
      return looseEqual(actual, cond.value);
    case 'ne':
      return !looseEqual(actual, cond.value);
    case 'in':
      return inList(actual, cond.value);
    case 'not_in':
      return !inList(actual, cond.value);
    case 'contains': {
      if (typeof actual === 'string') {
        const needle = text(cond.value);
        return needle !== undefined && actual.includes(needle);
      }
      return Array.isArray(actual) && actual.some((item) => looseEqual(item, cond.value));
    }
    default: {
      const a = looseNumber(actual);
      const b = looseNumber(cond.value);
      if (a === undefined || b === undefined) return false;
      if (cond.op === 'gt') return a > b;
      if (cond.op === 'gte') return a >= b;
      if (cond.op === 'lt') return a < b;
      return a <= b;
    }
  }
}

/**
 * Whether the condition holds for the values of a record; a missing field
 * is absent. all of no rules holds, any of no rules does not. The condition
 * is assumed valid (parseCondition).
 */
export function evaluateCondition(cond: Condition, values: Record<string, unknown>): boolean {
  if ('rules' in cond) {
    if (cond.op === 'all') return cond.rules.every((child) => evaluateCondition(child, values));
    if (cond.op === 'any') return cond.rules.some((child) => evaluateCondition(child, values));
    return cond.rules.length === 1 && !evaluateCondition(cond.rules[0]!, values);
  }
  return compare(
    cond,
    Object.prototype.hasOwnProperty.call(values, cond.field) ? values[cond.field] : undefined,
  );
}
