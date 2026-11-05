/**
 * filter — structural check of the filter AST (WP-789, MET-45, SRC-03,
 * SRC-04): $defs/filter of api/rules.schema.json, FilterExpr of the API, the
 * where of POST /api/v1/search/query and of saved views. The binding check
 * is savedview.Expr.Validate in the backend; both pass the cases filter_cases
 * and invalid_filters of api/rules.conformance.json, refusing at the same
 * path. parseFilter checks structure, operators, value forms, the limits and
 * the related node; the field whitelist, the kinds of fields, attribute
 * definitions, tags and relationship types are known to the backend only.
 * The click builder (WP-315) builds on it.
 */

import type { Scalar } from './rules';

export type FilterEntityKind = 'ci' | 'asset';

/** The operators of a comparison, the enum of $defs/filter_compare. */
export const filterOps = [
  'eq',
  'ne',
  'lt',
  'lte',
  'gt',
  'gte',
  'like',
  'in',
  'exists',
  'between',
  'has_any',
  'has_all',
] as const;

export type FilterOp = (typeof filterOps)[number];

export type RelatedDirection = 'out' | 'in' | 'both';

export interface FilterComparison {
  field: string;
  op: FilterOp;
  value?: Scalar | Scalar[] | null;
}

export interface FilterRelated {
  direction: RelatedDirection;
  rel_types?: string[];
  max_depth?: number;
  where: FilterExpr;
}

export type FilterExpr =
  | { and: FilterExpr[] }
  | { or: FilterExpr[] }
  | { not: FilterExpr }
  | { related: FilterRelated }
  | FilterComparison;

/** Limits of one AST (MaxExprDepth, MaxExprNodes, MaxRelationshipDepth). */
export const MAX_FILTER_DEPTH = 10;
export const MAX_FILTER_NODES = 100;
export const MAX_RELATED_DEPTH = 5;

/** The field of the tag comparisons (has_any, has_all). */
export const TAGS_FIELD = 'tags';

const nodeKeys = ['and', 'or', 'not', 'related', 'field', 'op', 'value'];
const relatedKeys = ['direction', 'rel_types', 'max_depth', 'where'];
const directions: readonly string[] = ['out', 'in', 'both'];
const attributeKey = /^[a-z0-9_]{1,64}$/;

function fail(path: string, message: string): never {
  throw new Error(`${path}: ${message}`);
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function present(value: unknown): boolean {
  return value !== undefined && value !== null;
}

function isScalar(value: unknown): value is Scalar {
  return typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean';
}

/** scalarError reports why value is no string, number or boolean ('' if it is one). */
function scalarError(value: unknown): string {
  if (!isScalar(value)) return 'must be a string, number or boolean';
  if (typeof value === 'string' && value.includes('\u0000'))
    return 'must not contain NUL characters';
  return '';
}

function checkList(
  path: string,
  value: unknown,
  message: string,
  size: (n: number) => boolean,
  tag = false,
): void {
  if (!Array.isArray(value) || !size(value.length)) fail(`${path}.value`, message);
  value.forEach((item, i) => {
    let error = scalarError(item);
    if (!error && tag && typeof item !== 'string') error = 'must be a string';
    if (!error && tag && item === '') error = 'must be a tag key';
    if (error) fail(`${path}.value[${i}]`, error);
  });
}

/** The key of an attribute the field names (attributes.<key>, for CIs also asset.attributes.<key>). */
function attributeOf(field: string, entityKind: FilterEntityKind): string | undefined {
  if (field.startsWith('attributes.')) return field.slice('attributes.'.length);
  if (entityKind === 'ci' && field.startsWith('asset.attributes.'))
    return field.slice('asset.attributes.'.length);
  return undefined;
}

function checkComparison(
  node: Record<string, unknown>,
  entityKind: FilterEntityKind,
  path: string,
): void {
  const field = node.field as string;
  const key = attributeOf(field, entityKind);
  if (key !== undefined && !attributeKey.test(key))
    fail(path, `invalid attribute key ${JSON.stringify(key)}`);
  const op = node.op;
  if (field === TAGS_FIELD && op !== 'has_any' && op !== 'has_all')
    fail(path, 'tags take has_any or has_all');
  const value = node.value;
  switch (op) {
    case 'exists':
      if (present(value) && typeof value !== 'boolean')
        fail(`${path}.value`, 'exists takes true, false or no value');
      return;
    case 'eq':
    case 'ne':
    case 'lt':
    case 'lte':
    case 'gt':
    case 'gte':
    case 'like':
      break;
    case 'in':
      checkList(path, value, 'in needs a non-empty list', (n) => n > 0);
      return;
    case 'between':
      checkList(path, value, 'between needs a list of exactly two values', (n) => n === 2);
      return;
    case 'has_any':
    case 'has_all':
      if (field !== TAGS_FIELD) fail(path, `${op} applies to tags only`);
      checkList(path, value, `${op} needs a non-empty list of tag keys`, (n) => n > 0, true);
      return;
    default:
      fail(path, `unknown operator ${JSON.stringify(op)}`);
  }
  const error = scalarError(value);
  if (error) fail(`${path}.value`, error);
}

interface Counter {
  nodes: number;
}

function checkRelated(
  input: unknown,
  entityKind: FilterEntityKind,
  path: string,
  depth: number,
  counter: Counter,
): void {
  if (entityKind !== 'ci') fail(path, 'related applies to entity_kind ci only');
  if (!isObject(input)) fail(path, 'related is an object');
  for (const key of Object.keys(input)) {
    if (!relatedKeys.includes(key)) fail(`${path}.${key}`, 'unknown property of related');
  }
  if (typeof input.direction !== 'string' || !directions.includes(input.direction)) {
    fail(`${path}.direction`, 'must be out, in or both');
  }
  if (present(input.max_depth)) {
    const d = input.max_depth;
    if (typeof d !== 'number' || !Number.isInteger(d) || d < 1 || d > MAX_RELATED_DEPTH) {
      fail(`${path}.max_depth`, `must be between 1 and ${MAX_RELATED_DEPTH}`);
    }
  }
  if (present(input.rel_types)) {
    if (!Array.isArray(input.rel_types))
      fail(`${path}.rel_types`, 'must be a list of relationship type keys');
    input.rel_types.forEach((key, i) => {
      if (typeof key !== 'string' || key === '' || key.includes('\u0000')) {
        fail(`${path}.rel_types[${i}]`, 'must be a relationship type key');
      }
    });
  }
  if (!present(input.where)) fail(`${path}.where`, 'related needs a filter (where)');
  checkNode(input.where, 'ci', `${path}.where`, depth + 1, counter);
}

function checkGroup(value: unknown, path: string, key: string): value is unknown[] {
  if (!present(value)) return false;
  if (!Array.isArray(value)) fail(`${path}.${key}`, `${key} is a list of nodes`);
  return value.length > 0;
}

/** checkNode mirrors savedview.Expr.validate: the same checks in the same order. */
function checkNode(
  input: unknown,
  entityKind: FilterEntityKind,
  path: string,
  depth: number,
  counter: Counter,
): void {
  counter.nodes++;
  if (depth > MAX_FILTER_DEPTH) fail(path, `nested deeper than ${MAX_FILTER_DEPTH} levels`);
  if (counter.nodes > MAX_FILTER_NODES) fail(path, `more than ${MAX_FILTER_NODES} nodes`);
  if (!isObject(input)) fail(path, 'a node is an object');
  for (const key of Object.keys(input)) {
    if (!nodeKeys.includes(key)) fail(`${path}.${key}`, 'unknown property of a node');
  }
  if (present(input.field) && typeof input.field !== 'string')
    fail(`${path}.field`, 'field is a string');
  const and = checkGroup(input.and, path, 'and');
  const or = checkGroup(input.or, path, 'or');
  const parts = [
    and,
    or,
    present(input.not),
    present(input.related),
    typeof input.field === 'string' && input.field !== '',
  ];
  if (parts.filter(Boolean).length !== 1)
    fail(path, 'a node is exactly one of and, or, not, related or a comparison');
  if (and)
    (input.and as unknown[]).forEach((n, i) =>
      checkNode(n, entityKind, `${path}.and[${i}]`, depth + 1, counter),
    );
  else if (or)
    (input.or as unknown[]).forEach((n, i) =>
      checkNode(n, entityKind, `${path}.or[${i}]`, depth + 1, counter),
    );
  else if (present(input.not)) checkNode(input.not, entityKind, `${path}.not`, depth + 1, counter);
  else if (present(input.related))
    checkRelated(input.related, entityKind, `${path}.related`, depth, counter);
  else checkComparison(input, entityKind, path);
}

/**
 * Checks a filter AST strictly for the entity kind; throws on an invalid
 * one, the message starting with the path of the node (where, where.and[1],
 * where.related.max_depth …) and a colon, as the API names it in field.
 */
export function parseFilter(input: unknown, entityKind: FilterEntityKind = 'ci'): FilterExpr {
  checkNode(input, entityKind, 'where', 1, { nodes: 0 });
  return input as FilterExpr;
}
