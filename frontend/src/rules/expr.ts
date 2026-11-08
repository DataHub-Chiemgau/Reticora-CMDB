/**
 * expr — preview evaluation of the expressions of computed attributes
 * (WP-258, WP-802, MET-10 computed, MET-45): $defs/expr of
 * api/rules.schema.json. The binding evaluation is the Go package
 * backend/internal/rules (ParseExpr, EvalExpr); both pass the cases
 * expressions and invalid_expressions of api/rules.conformance.json.
 *
 * An expression yields a value from the values of a record: a field
 * reference (an attribute of the record or asset.<field> of the linked
 * asset), a literal, arithmetic (add, sub, mul, div), dates (today,
 * days_between), strings (concat, lower, upper) and coalesce.
 */

import { isEmptyValue } from './rules';

/** The operators of the expression AST, apart from those of rules and conditions. */
export const EXPR_OPS = [
  'field',
  'literal',
  'add',
  'sub',
  'mul',
  'div',
  'today',
  'days_between',
  'concat',
  'lower',
  'upper',
  'coalesce',
] as const;
export type ExprOp = (typeof EXPR_OPS)[number];

/** The operators that take operands (args). */
export type ExprArgsOp = Exclude<ExprOp, 'field' | 'literal' | 'today'>;

export type ExprScalar = string | number | boolean;

export type Expr =
  | { op: 'field'; field: string }
  | { op: 'literal'; value: ExprScalar }
  | { op: 'today' }
  | { op: ExprArgsOp; args: Expr[] };

/** Result types of an expression (rules.ResultType); any is known only at evaluation. */
export const EXPR_RESULT_TYPES = ['number', 'string', 'date', 'boolean', 'any'] as const;
export type ExprResultType = (typeof EXPR_RESULT_TYPES)[number];

/** A reference to a field of the asset linked through ci.asset_id starts with it. */
export const ASSET_PREFIX = 'asset.';

/**
 * rules.AssetFields: the asset fields an expression can read, as
 * asset.<field>, with their result types. warranty_end, purchase_cost and
 * asset_tag are the deprecated names of warranty_until, purchase_price and
 * inventory_no (AST-01) and read the same values.
 */
export const EXPR_ASSET_FIELDS: Readonly<Record<string, ExprResultType>> = {
  warranty_until: 'date',
  purchase_date: 'date',
  purchase_price: 'number',
  inventory_no: 'string',
  serial_number: 'string',
  status: 'string',
  warranty_end: 'date',
  purchase_cost: 'number',
  asset_tag: 'string',
};

/** The deprecated asset field names (still accepted by the server). */
export const DEPRECATED_EXPR_ASSET_FIELDS: readonly string[] = [
  'warranty_end',
  'purchase_cost',
  'asset_tag',
];

/** maxExprDepth of the Go package: the nesting an expression may have. */
export const MAX_EXPR_DEPTH = 32;

/**
 * The number of operands of an operator: exactly min when max equals min,
 * otherwise at least min (max -1: no upper bound).
 */
export const EXPR_ARITY: Readonly<Record<ExprArgsOp, readonly [number, number]>> = {
  add: [2, -1],
  mul: [2, -1],
  sub: [2, 2],
  div: [2, 2],
  days_between: [2, 2],
  concat: [1, -1],
  lower: [1, 1],
  upper: [1, 1],
  coalesce: [1, -1],
};

const keysOf = (op: string): readonly string[] => {
  if (op === 'field') return ['op', 'field'];
  if (op === 'literal') return ['op', 'value'];
  if (op === 'today') return ['op'];
  return ['op', 'args'];
};

function at(path: string, prop: string): string {
  return path === '' ? prop : `${path}.${prop}`;
}

function isScalar(v: unknown): v is ExprScalar {
  return (
    typeof v === 'string' || typeof v === 'boolean' || (typeof v === 'number' && !Number.isNaN(v))
  );
}

/** isExprOp reports whether op is an operator of the expression AST. */
export function isExprOp(op: unknown): op is ExprOp {
  return typeof op === 'string' && (EXPR_OPS as readonly string[]).includes(op);
}

function parseNode(input: unknown, path: string, depth: number): Expr {
  const where = path === '' ? 'expression' : path;
  if (depth > MAX_EXPR_DEPTH) {
    throw new Error(`${where}: the expression is nested deeper than ${MAX_EXPR_DEPTH}`);
  }
  if (typeof input !== 'object' || input === null || Array.isArray(input)) {
    throw new Error(`${where}: an expression is an object`);
  }
  const node = input as Record<string, unknown>;
  const op = node.op;
  if (!isExprOp(op)) {
    throw new Error(`${at(path, 'op')}: unknown operator ${JSON.stringify(String(op))}`);
  }
  const keys = keysOf(op);
  for (const key of Object.keys(node).sort()) {
    if (!keys.includes(key)) throw new Error(`${at(path, key)}: unknown property of ${op}`);
  }
  switch (op) {
    case 'field': {
      const field = node.field;
      if (typeof field !== 'string' || field.trim() === '' || field !== field.trim()) {
        throw new Error(
          `${at(path, 'field')}: field requires the key of an attribute or asset.<field>`,
        );
      }
      if (field.startsWith(ASSET_PREFIX)) {
        const name = field.slice(ASSET_PREFIX.length);
        if (!Object.prototype.hasOwnProperty.call(EXPR_ASSET_FIELDS, name)) {
          throw new Error(
            `${at(path, 'field')}: ${field} is no asset field of an expression (${Object.keys(
              EXPR_ASSET_FIELDS,
            ).join(', ')})`,
          );
        }
      }
      return { op, field };
    }
    case 'literal': {
      if (!('value' in node) || !isScalar(node.value)) {
        throw new Error(`${at(path, 'value')}: literal requires a string, number or boolean`);
      }
      return { op, value: node.value };
    }
    case 'today':
      return { op };
  }
  const list = node.args;
  if (!Array.isArray(list)) {
    throw new Error(`${at(path, 'args')}: ${op} requires an array of expressions`);
  }
  const [min, max] = EXPR_ARITY[op];
  if (max === min && list.length !== min) {
    throw new Error(`${at(path, 'args')}: ${op} takes exactly ${min} expressions`);
  }
  if (list.length < min) {
    throw new Error(`${at(path, 'args')}: ${op} takes at least ${min} expressions`);
  }
  return {
    op,
    args: list.map((child, i) => parseNode(child, at(path, `args[${i}]`), depth + 1)),
  };
}

function typeOf(e: Expr, path: string): ExprResultType {
  switch (e.op) {
    case 'field': {
      if (e.field.startsWith(ASSET_PREFIX)) {
        return EXPR_ASSET_FIELDS[e.field.slice(ASSET_PREFIX.length)] ?? 'any';
      }
      return 'any';
    }
    case 'literal':
      if (typeof e.value === 'string') return 'string';
      if (typeof e.value === 'boolean') return 'boolean';
      return 'number';
    case 'today':
      return 'date';
  }
  const types = e.args.map((a, i) => typeOf(a, at(path, `args[${i}]`)));
  const accept = (result: ExprResultType, ...allowed: ExprResultType[]): ExprResultType => {
    types.forEach((t, i) => {
      if (t !== 'any' && !allowed.includes(t)) {
        throw new Error(`${at(path, `args[${i}]`)}: ${e.op} takes no ${t}`);
      }
    });
    return result;
  };
  switch (e.op) {
    case 'add':
    case 'sub':
    case 'mul':
    case 'div':
      return accept('number', 'number');
    case 'days_between':
      return accept('number', 'date', 'string');
    case 'lower':
    case 'upper':
      return accept('string', 'string');
    case 'concat':
      return 'string';
  }
  // coalesce: the common type of its operands.
  let result: ExprResultType = 'any';
  types.forEach((t, i) => {
    if (t === 'any' || t === result) return;
    if (result === 'any') {
      result = t;
      return;
    }
    throw new Error(`${at(path, `args[${i}]`)}: coalesce mixes ${result} and ${t}`);
  });
  return result;
}

/**
 * parseExpr checks an expression strictly, as rules.ParseExpr does:
 * unknown operators and properties, a wrong number of operands, a literal
 * that is no string, number or boolean, an asset field outside the list
 * and operands of the wrong type (e.g. text in add) throw; the message
 * starts with the path of the failing property (e.g. args[1].op) and a
 * colon.
 */
export function parseExpr(input: unknown): Expr {
  const e = parseNode(input, '', 0);
  typeOf(e, '');
  return e;
}

/** exprResultType is the type an expression yields; the expression is assumed valid. */
export function exprResultType(e: Expr): ExprResultType {
  try {
    return typeOf(e, '');
  } catch {
    return 'any';
  }
}

/** exprFields returns the references of an expression, sorted and each once. */
export function exprFields(e: Expr): string[] {
  const out = new Set<string>();
  const walk = (n: Expr) => {
    if (n.op === 'field') out.add(n.field);
    if ('args' in n) n.args.forEach(walk);
  };
  walk(e);
  return [...out].sort();
}

/** exprUsesToday reports whether an expression depends on the current date. */
export function exprUsesToday(e: Expr): boolean {
  if (e.op === 'today') return true;
  return 'args' in e && e.args.some(exprUsesToday);
}

/**
 * The result types each data type of a computed attribute takes
 * (computedResults of backend/internal/citype): any, a reference to an
 * attribute, fits every one of them. Other data types cannot be computed.
 */
export const COMPUTED_RESULTS: Readonly<Record<string, readonly ExprResultType[]>> = {
  integer: ['number'],
  decimal: ['number'],
  text: ['string', 'date'],
  multiline: ['string', 'date'],
  date: ['date'],
  boolean: ['boolean'],
};

/** The former names of the data types (fieldmeta.CanonicalType). */
const TYPE_ALIASES: Readonly<Record<string, string>> = {
  textarea: 'multiline',
  string: 'text',
  number: 'decimal',
};

/** COMPUTED_DATA_TYPES are the data types a computed attribute can have. */
export const COMPUTED_DATA_TYPES: readonly string[] = Object.keys(COMPUTED_RESULTS);

/**
 * computedTypeFit tells whether an expression fits the data type of its
 * attribute: 'fits', 'mismatch' (the result type does not fit) or
 * 'not_computable' (the data type cannot be computed). The binding check
 * is the server's (validateComputed).
 */
export function computedTypeFit(e: Expr, dataType: string): 'fits' | 'mismatch' | 'not_computable' {
  const results = COMPUTED_RESULTS[TYPE_ALIASES[dataType] ?? dataType];
  if (!results) return 'not_computable';
  const result = exprResultType(e);
  return result === 'any' || results.includes(result) ? 'fits' : 'mismatch';
}

// ─── Evaluation ──────────────────────────────────────────────────────────────

/** isEmptyValue plus the empty object, as blank in the Go package. */
function blank(v: unknown): boolean {
  if (typeof v === 'object' && v !== null && !Array.isArray(v) && !(v instanceof Date)) {
    return Object.keys(v as object).length === 0;
  }
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
  return Number.isNaN(n) ? undefined : n;
}

function text(v: unknown): string | undefined {
  return isScalar(v) ? String(v) : undefined;
}

/** The calendar day of a date as its midnight UTC in milliseconds. */
type Day = number;

const DATE_ONLY = /^(\d{4})-(\d{2})-(\d{2})$/;
const TIMESTAMP = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/;

const formatters = new Map<string, Intl.DateTimeFormat>();

function formatterOf(timeZone: string): Intl.DateTimeFormat {
  let f = formatters.get(timeZone);
  if (!f) {
    try {
      f = new Intl.DateTimeFormat('en-US', {
        timeZone,
        year: 'numeric',
        month: '2-digit',
        day: '2-digit',
      });
    } catch {
      // An unknown zone counts as UTC (Go: a nil location).
      f = formatterOf('UTC');
    }
    formatters.set(timeZone, f);
  }
  return f;
}

/** dayIn is the calendar day of an instant in a time zone. */
function dayIn(instant: Date, timeZone: string): Day {
  const parts = formatterOf(timeZone).formatToParts(instant);
  const part = (type: string) => Number(parts.find((p) => p.type === type)?.value);
  return Date.UTC(part('year'), part('month') - 1, part('day'));
}

function isoDay(day: Day): string {
  return new Date(day).toISOString().slice(0, 10);
}

/** civilDay reads a date (YYYY-MM-DD) or an RFC 3339 timestamp with its date in the zone. */
function civilDay(v: unknown, timeZone: string): Day | undefined {
  if (v instanceof Date) return Number.isNaN(v.getTime()) ? undefined : dayIn(v, timeZone);
  if (typeof v !== 'string') return undefined;
  const s = v.trim();
  const m = DATE_ONLY.exec(s);
  if (m) {
    const [y, mo, d] = [Number(m[1]), Number(m[2]), Number(m[3])];
    const day = Date.UTC(y, mo - 1, d);
    const back = new Date(day);
    // 2026-02-30 is no date (Go's time.Parse refuses it).
    if (back.getUTCFullYear() !== y || back.getUTCMonth() !== mo - 1 || back.getUTCDate() !== d) {
      return undefined;
    }
    return day;
  }
  if (TIMESTAMP.test(s)) {
    const t = new Date(s);
    return Number.isNaN(t.getTime()) ? undefined : dayIn(t, timeZone);
  }
  return undefined;
}

function arithmetic(op: 'add' | 'sub' | 'mul' | 'div', args: unknown[]): number | null {
  const nums: number[] = [];
  for (const a of args) {
    const n = looseNumber(a);
    if (n === undefined) return null;
    nums.push(n);
  }
  let r = nums[0]!;
  for (const n of nums.slice(1)) {
    if (op === 'add') r += n;
    else if (op === 'sub') r -= n;
    else if (op === 'mul') r *= n;
    else {
      if (n === 0) return null;
      r /= n;
    }
  }
  return Number.isFinite(r) ? r : null;
}

function apply(op: ExprArgsOp, args: unknown[], timeZone: string): unknown {
  switch (op) {
    case 'add':
    case 'sub':
    case 'mul':
    case 'div':
      return arithmetic(op, args);
    case 'days_between': {
      const from = civilDay(args[0], timeZone);
      const to = civilDay(args[1], timeZone);
      if (from === undefined || to === undefined) return null;
      return Math.round((to - from) / 86_400_000);
    }
    case 'concat': {
      let out = '';
      for (const a of args) {
        const s = text(a);
        if (s === undefined) return null;
        out += s;
      }
      return out;
    }
    case 'lower':
    case 'upper': {
      const s = text(args[0]);
      if (s === undefined) return null;
      return op === 'lower' ? s.toLowerCase() : s.toUpperCase();
    }
    default:
      return null;
  }
}

function evalNode(
  e: Expr,
  inputs: Readonly<Record<string, unknown>>,
  now: Date,
  timeZone: string,
): unknown {
  if (e.op === 'coalesce') {
    for (const arg of e.args) {
      const v = evalNode(arg, inputs, now, timeZone);
      if (v !== null) return v;
    }
    return null;
  }
  let v: unknown;
  switch (e.op) {
    case 'field':
      v = Object.prototype.hasOwnProperty.call(inputs, e.field) ? inputs[e.field] : undefined;
      break;
    case 'literal':
      v = e.value;
      break;
    case 'today':
      v = isoDay(dayIn(now, timeZone));
      break;
    default: {
      const args: unknown[] = [];
      for (const arg of e.args) {
        const a = evalNode(arg, inputs, now, timeZone);
        if (a === null) return null;
        args.push(a);
      }
      v = apply(e.op, args, timeZone);
    }
  }
  return blank(v) ? null : v;
}

/**
 * evalExpr evaluates an expression for the values of a record with the
 * semantics of rules.EvalExpr: inputs holds the attributes by key and the
 * asset fields as asset.<field>; now and timeZone (IANA, e.g. the zone of
 * the organization) give today's date. It returns null for an empty value:
 * a missing input (absent, null or blank text), a value of the wrong kind,
 * a date that cannot be read or a division by 0 make the expression empty,
 * except within coalesce, which takes its first non-empty operand. Numbers
 * are numbers, dates text YYYY-MM-DD. The expression is assumed valid
 * (parseExpr).
 */
export function evalExpr(
  expr: Expr,
  inputs: Readonly<Record<string, unknown>>,
  now: Date,
  timeZone = 'UTC',
): unknown {
  return evalNode(expr, inputs, now, timeZone || 'UTC');
}
