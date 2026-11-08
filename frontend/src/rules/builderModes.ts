/**
 * builderModes — the modes of the click builder RuleBuilder (CH18, E-54,
 * WP-315): node kinds, operators per field kind and limits of each mode,
 * and the fixed output format. This WP delivers the filter mode: it emits
 * FilterExpr of api/openapi.yaml (after WP-309 and WP-789), stored in
 * filter_spec.where of a saved view and sent as where of POST
 * /api/v1/search/query. WP-801 adds the condition mode (the condition
 * grammar of api/rules.schema.json: all, any, not and comparisons) and the
 * validation mode (the value rule AST of MET-45: all, required, pattern,
 * range and enum) for the rule catalog. WP-802 adds the expression mode
 * ($defs/expr of api/rules.schema.json: field, literal, add, sub, mul, div,
 * today, days_between, concat, lower, upper, coalesce) for computed
 * attributes. No mode emits nodes of another: no like in conditions, no
 * comparison in value rules, no required in a filter, no rule or
 * comparison in an expression.
 *
 * The binding check is the server's (savedview.Expr.Validate,
 * backend/internal/savedview/filter.go); the field whitelist and the
 * operator matrix below mirror astFields and validateComparison there. A
 * mirror that drifts from the server shows the 422 of the server at the
 * node (RuleBuilder).
 */

import type { components } from '../api/generated/schema';
import {
  compareOps,
  parseCondition,
  parseRule,
  type CompareOp,
  type Condition,
  type Rule,
  type Scalar,
} from './rules';
import {
  MAX_FILTER_DEPTH,
  MAX_FILTER_NODES,
  MAX_RELATED_DEPTH,
  TAGS_FIELD,
  parseFilter,
  type FilterEntityKind,
  type FilterOp,
  type RelatedDirection,
} from './filter';
import {
  ASSET_PREFIX,
  DEPRECATED_EXPR_ASSET_FIELDS,
  EXPR_ARITY,
  EXPR_ASSET_FIELDS,
  EXPR_OPS,
  MAX_EXPR_DEPTH,
  exprResultType,
  parseExpr,
  type Expr,
  type ExprArgsOp,
  type ExprOp,
  type ExprResultType,
} from './expr';

/** The output format of the filter mode: FilterExpr of the OpenAPI document. */
export type FilterExpr = components['schemas']['FilterExpr'];
export type FilterRelated = components['schemas']['FilterRelated'];
type AttributeDefinition = components['schemas']['AttributeDefinition'];

export type { FilterEntityKind, FilterOp, RelatedDirection };

/** The entity kinds a filter queries (SavedView.entity_kind). */
export const FILTER_ENTITY_KINDS: readonly FilterEntityKind[] = ['ci', 'asset'];

/**
 * The kinds of a field, as validateComparison distinguishes them: text,
 * number, time, uuid, bool and the tag field.
 */
export const FIELD_KINDS = ['text', 'number', 'time', 'uuid', 'bool', 'tag'] as const;
export type FieldKind = (typeof FIELD_KINDS)[number];

/**
 * The operators of each field kind (validateComparison): lt, lte, gt and
 * gte not for uuid and bool, like for text only, between for number and
 * time only, has_any and has_all for the tag field only, which takes no
 * other operator.
 */
export const OPS_BY_KIND: Readonly<Record<FieldKind, readonly FilterOp[]>> = {
  text: ['eq', 'ne', 'lt', 'lte', 'gt', 'gte', 'like', 'in', 'exists'],
  number: ['eq', 'ne', 'lt', 'lte', 'gt', 'gte', 'between', 'in', 'exists'],
  time: ['eq', 'ne', 'lt', 'lte', 'gt', 'gte', 'between', 'in', 'exists'],
  uuid: ['eq', 'ne', 'in', 'exists'],
  bool: ['eq', 'ne', 'in', 'exists'],
  tag: ['has_any', 'has_all'],
};

/** The operators whose value is a list (in at least one, between exactly two). */
export const LIST_OPS: readonly FilterOp[] = ['in', 'between', 'has_any', 'has_all'];

/**
 * astFields of backend/internal/savedview/filter.go: the columns of each
 * entity kind with their field kind. For CIs asset.<field> adds the
 * columns of the linked asset.
 */
export const FILTER_FIELDS: Readonly<
  Record<FilterEntityKind, Readonly<Record<string, FieldKind>>>
> = {
  ci: {
    name: 'text',
    status: 'text',
    hostname: 'text',
    fqdn: 'text',
    serial_number: 'text',
    manufacturer: 'text',
    model: 'text',
    os_name: 'text',
    os_version: 'text',
    management_ip: 'text',
    lifecycle_state: 'text',
    discovery_source: 'text',
    health: 'text',
    ci_type: 'text',
    client_id: 'uuid',
    site_id: 'uuid',
    location_id: 'uuid',
    owner_user_id: 'uuid',
    owner_team_id: 'uuid',
    is_manual: 'bool',
    created_at: 'time',
    updated_at: 'time',
    last_seen_at: 'time',
  },
  asset: {
    name: 'text',
    inventory_no: 'text',
    asset_tag: 'text',
    category: 'text',
    status: 'text',
    serial_number: 'text',
    supplier: 'text',
    lifecycle_state: 'text',
    barcode: 'text',
    rfid_tag: 'text',
    purchase_price: 'number',
    purchase_cost: 'number',
    purchase_date: 'time',
    warranty_until: 'time',
    warranty_end: 'time',
    client_id: 'uuid',
    location_id: 'uuid',
    created_at: 'time',
    manufacturer: 'text',
    model: 'text',
    cost_center: 'text',
    currency: 'text',
    supplier_id: 'uuid',
    contract_id: 'uuid',
    owner_user_id: 'uuid',
    owner_team_id: 'uuid',
  },
};

/**
 * The deprecated asset aliases (AST-01, WP-738): they read the new columns
 * inventory_no, purchase_price and warranty_until. The picker offers the
 * new names only; a loaded filter keeps the alias.
 */
const DEPRECATED_ASSET_COLUMNS = new Set(['asset_tag', 'purchase_cost', 'warranty_end']);

/** Time columns holding a date (purchase_date, warranty_until); the others are times. */
const DATE_COLUMNS = new Set(['purchase_date', 'warranty_until', 'warranty_end']);

/**
 * The editors of a value (ValueEditor): text, number, date, date and time,
 * boolean, the enum values of a definition, the lists of CI types, clients
 * and sites, the location tree, the pickers of WP-318 for references and
 * the tag keys. none is a field without an editor (an id without a picker,
 * a reference to a contract): it takes exists only, there is no free id
 * input.
 */
export const VALUE_EDITORS = [
  'text',
  'number',
  'date',
  'datetime',
  'bool',
  'enum',
  'ci_type',
  'client',
  'site',
  'location',
  'ci',
  'asset',
  'user',
  'team',
  'tags',
  'none',
] as const;
export type ValueEditorKind = (typeof VALUE_EDITORS)[number];

/** Where a field comes from; the picker groups and labels by it. */
export type FieldSource = 'column' | 'asset' | 'attribute' | 'asset_attribute' | 'tags';

/** A field the builder offers: its path in the AST, its kind and its editor. */
export interface FieldSpec {
  field: string;
  source: FieldSource;
  kind: FieldKind;
  editor: ValueEditorKind;
  /** The column of a column or asset field (asset.<column>). */
  column?: string;
  /** The attribute definition of an attribute field. */
  definition?: AttributeDefinition;
}

/** The editor of a column. */
function columnEditor(column: string, kind: FieldKind): ValueEditorKind {
  switch (column) {
    case 'ci_type':
      return 'ci_type';
    case 'client_id':
      return 'client';
    case 'site_id':
      return 'site';
    case 'location_id':
      return 'location';
    case 'owner_user_id':
      return 'user';
    case 'owner_team_id':
      return 'team';
  }
  switch (kind) {
    case 'number':
      return 'number';
    case 'time':
      return DATE_COLUMNS.has(column) ? 'date' : 'datetime';
    case 'bool':
      return 'bool';
    case 'uuid':
      // supplier_id, contract_id: no picker, so no free id input.
      return 'none';
    case 'tag':
      return 'tags';
  }
  return 'text';
}

/** The data types of an attribute definition by their filter kind (fieldmeta.FilterKind). */
export function attributeKind(dataType: string): FieldKind {
  switch (dataType) {
    case 'integer':
    case 'decimal':
    case 'number':
      return 'number';
    case 'date':
    case 'datetime':
      return 'time';
    case 'boolean':
      return 'bool';
    case 'uuid':
      return 'uuid';
  }
  return 'text';
}

/** The reference targets with a picker of WP-318; contract has none (WP-315, not part). */
const REFERENCE_EDITORS: Readonly<Record<string, ValueEditorKind>> = {
  ci: 'ci',
  asset: 'asset',
  user: 'user',
  team: 'team',
  location: 'location',
};

/** The former reference data types and their target (fieldmeta aliases). */
const LEGACY_REFERENCE_TYPES: Readonly<Record<string, string>> = {
  user: 'user',
  team: 'team',
  location: 'location',
  ci_ref: 'ci',
  asset_ref: 'asset',
  contract_ref: 'contract',
};

/** The editor of an attribute definition by data type, enum values and reference target. */
export function attributeEditor(def: AttributeDefinition): ValueEditorKind {
  const target = def.reference_target || LEGACY_REFERENCE_TYPES[def.data_type];
  if (def.data_type === 'reference' || def.data_type === 'contract_ref' || target) {
    return (target && REFERENCE_EDITORS[target]) || 'none';
  }
  if ((def.enum_values?.length ?? 0) > 0) return 'enum';
  switch (def.data_type) {
    case 'integer':
    case 'decimal':
    case 'number':
      return 'number';
    case 'date':
      return 'date';
    case 'datetime':
      return 'datetime';
    case 'boolean':
      return 'bool';
    case 'uuid':
      return 'none';
  }
  return 'text';
}

/** The tag field (has_any, has_all). */
export const TAG_FIELD_SPEC: FieldSpec = {
  field: TAGS_FIELD,
  source: 'tags',
  kind: 'tag',
  editor: 'tags',
};

export interface FieldCatalogOptions {
  /** The attribute definitions; only searchable ones are offered. */
  definitions?: readonly AttributeDefinition[];
  /** Whether the page assets is enabled: asset.* of a CI is offered only then. */
  assets?: boolean;
}

/**
 * fieldCatalog lists the fields of an entity kind: the columns of
 * astFields, tags, for CIs (with assets enabled) the columns of the linked
 * asset, and attributes.<key> of the searchable definitions (for CIs also
 * asset.attributes.<key>).
 */
export function fieldCatalog(
  entityKind: FilterEntityKind,
  { definitions = [], assets = true }: FieldCatalogOptions = {},
): FieldSpec[] {
  const columns = (kind: FilterEntityKind, prefix: string, source: FieldSource) =>
    Object.entries(FILTER_FIELDS[kind])
      .filter(([column]) => kind !== 'asset' || !DEPRECATED_ASSET_COLUMNS.has(column))
      .map(([column, k]): FieldSpec => ({
        field: prefix + column,
        source,
        kind: k,
        editor: columnEditor(column, k),
        column,
      }));
  const searchable = definitions.filter((d) => d.searchable && /^[a-z0-9_]{1,64}$/.test(d.key));
  const seen = new Set<string>();
  const attributes = (prefix: string, source: FieldSource) =>
    searchable.flatMap((d): FieldSpec[] => {
      const field = prefix + d.key;
      if (seen.has(field)) return [];
      seen.add(field);
      return [
        {
          field,
          source,
          kind: attributeKind(d.data_type),
          editor: attributeEditor(d),
          definition: d,
        },
      ];
    });
  const fields = [...columns(entityKind, '', 'column'), TAG_FIELD_SPEC];
  fields.push(...attributes('attributes.', 'attribute'));
  if (entityKind === 'ci' && assets) {
    fields.push(...columns('asset', 'asset.', 'asset'));
    fields.push(...attributes('asset.attributes.', 'asset_attribute'));
  }
  return fields;
}

/**
 * The spec of a field that is not in the catalog (a view saved through the
 * API, a definition no longer searchable): its kind from the path and the
 * value, as the server derives the kind of an unresolved attribute.
 */
export function fieldSpecOf(
  field: string,
  entityKind: FilterEntityKind,
  catalog: readonly FieldSpec[],
  value?: unknown,
): FieldSpec | undefined {
  const known = catalog.find((f) => f.field === field);
  if (known) return known;
  if (field === TAGS_FIELD) return TAG_FIELD_SPEC;
  const asset = entityKind === 'ci' ? /^asset\.(?!attributes\.)(.+)$/.exec(field) : null;
  if (asset?.[1] && FILTER_FIELDS.asset[asset[1]]) {
    const k = FILTER_FIELDS.asset[asset[1]]!;
    return { field, source: 'asset', kind: k, editor: columnEditor(asset[1], k), column: asset[1] };
  }
  if (FILTER_FIELDS[entityKind][field]) {
    const k = FILTER_FIELDS[entityKind][field]!;
    return { field, source: 'column', kind: k, editor: columnEditor(field, k), column: field };
  }
  if (/^(asset\.)?attributes\.[a-z0-9_]{1,64}$/.test(field)) {
    const first = Array.isArray(value) ? value[0] : value;
    const kind: FieldKind =
      typeof first === 'number' ? 'number' : typeof first === 'boolean' ? 'bool' : 'text';
    const editor: ValueEditorKind =
      kind === 'number' ? 'number' : kind === 'bool' ? 'bool' : 'text';
    return {
      field,
      source: field.startsWith('asset.') ? 'asset_attribute' : 'attribute',
      kind,
      editor,
    };
  }
  return undefined;
}

/**
 * The operators a field offers: those of its kind; a field without an
 * editor (no picker, no free id input) takes exists only.
 */
export function opsFor(spec: FieldSpec | undefined): readonly FilterOp[] {
  if (!spec) return [];
  if (spec.editor === 'none') return ['exists'];
  // A value chosen from a list or a picker is compared for equality only.
  if (CHOICE_EDITORS.includes(spec.editor)) {
    return OPS_BY_KIND[spec.kind].filter((op) => CHOICE_OPS.includes(op));
  }
  return OPS_BY_KIND[spec.kind];
}

/** The editors that choose a value from a list or a picker. */
const CHOICE_EDITORS: readonly ValueEditorKind[] = [
  'enum',
  'ci_type',
  'client',
  'site',
  'location',
  'ci',
  'asset',
  'user',
  'team',
];
const CHOICE_OPS: readonly FilterOp[] = ['eq', 'ne', 'in', 'exists'];

/** The group junctions of the filter mode. */
export const GROUP_JUNCTIONS = ['and', 'or'] as const;
export type GroupJunction = (typeof GROUP_JUNCTIONS)[number];

/** The node kinds of the filter mode: groups, negation, relationship and comparison. */
export const FILTER_NODE_KINDS = ['and', 'or', 'not', 'related', 'compare'] as const;
export type FilterNodeKind = (typeof FILTER_NODE_KINDS)[number];

/** The directions of a related node. */
export const RELATED_DIRECTIONS: readonly RelatedDirection[] = ['out', 'in', 'both'];

/** A mode of the builder: what it emits and the limits it keeps. */
export interface BuilderMode {
  id: 'filter';
  nodeKinds: readonly FilterNodeKind[];
  junctions: readonly GroupJunction[];
  ops: readonly FilterOp[];
  maxDepth: number;
  maxNodes: number;
  maxRelatedDepth: number;
}

/** The filter mode (WP-315): FilterExpr with the limits of the server (MaxExprDepth, MaxExprNodes). */
export const FILTER_MODE: BuilderMode = {
  id: 'filter',
  nodeKinds: FILTER_NODE_KINDS,
  junctions: GROUP_JUNCTIONS,
  ops: [...new Set(Object.values(OPS_BY_KIND).flat())],
  maxDepth: MAX_FILTER_DEPTH,
  maxNodes: MAX_FILTER_NODES,
  maxRelatedDepth: MAX_RELATED_DEPTH,
};

// ─── The tree of the builder ─────────────────────────────────────────────────

/**
 * A node of the builder: one node of the AST with an id for focus and keys.
 * A comparison may be incomplete while it is edited (no field, no value);
 * a group may be empty. present records which optional members the AST had
 * (value, max_depth, rel_types), so a loaded AST is emitted unchanged.
 */
export type BuilderNode =
  | { id: string; kind: 'group'; junction: GroupJunction; children: BuilderNode[] }
  | { id: string; kind: 'not'; child: BuilderNode }
  | {
      id: string;
      kind: 'related';
      direction: RelatedDirection;
      relTypes?: string[];
      maxDepth?: number;
      child: BuilderNode | null;
    }
  | { id: string; kind: 'compare'; field: string; op: FilterOp; value?: unknown };

let nextId = 0;

/** newId returns an id unique in this page. */
export function newId(): string {
  nextId += 1;
  return `rb${nextId}`;
}

export function newComparison(): BuilderNode {
  return { id: newId(), kind: 'compare', field: '', op: 'eq' };
}

export function newGroup(junction: GroupJunction = 'and'): BuilderNode {
  return { id: newId(), kind: 'group', junction, children: [] };
}

export function newRelated(): BuilderNode {
  return { id: newId(), kind: 'related', direction: 'out', maxDepth: 1, child: null };
}

/** fromFilter turns an AST into the tree of the builder (throws on a node of no known kind). */
export function fromFilter(expr: FilterExpr): BuilderNode {
  const e = expr as Record<string, unknown>;
  if (Array.isArray(e.and)) {
    return {
      id: newId(),
      kind: 'group',
      junction: 'and',
      children: (e.and as FilterExpr[]).map(fromFilter),
    };
  }
  if (Array.isArray(e.or)) {
    return {
      id: newId(),
      kind: 'group',
      junction: 'or',
      children: (e.or as FilterExpr[]).map(fromFilter),
    };
  }
  if (e.not) return { id: newId(), kind: 'not', child: fromFilter(e.not as FilterExpr) };
  if (e.related) {
    const r = e.related as FilterRelated;
    const node: BuilderNode = {
      id: newId(),
      kind: 'related',
      direction: r.direction,
      child: r.where ? fromFilter(r.where) : null,
    };
    if (r.rel_types !== undefined) node.relTypes = [...r.rel_types];
    if (r.max_depth !== undefined) node.maxDepth = r.max_depth;
    return node;
  }
  if (typeof e.field === 'string') {
    const node: BuilderNode = {
      id: newId(),
      kind: 'compare',
      field: e.field,
      op: e.op as FilterOp,
    };
    if ('value' in e) node.value = e.value;
    return node;
  }
  throw new Error('where: a node is exactly one of and, or, not, related or a comparison');
}

/** toFilter emits the AST of a tree; an empty root group is no filter (undefined). */
export function toFilter(node: BuilderNode | null, root = true): FilterExpr | undefined {
  if (!node) return undefined;
  switch (node.kind) {
    case 'group': {
      if (root && node.children.length === 0) return undefined;
      const children = node.children.map((c) => toFilter(c, false)!);
      return node.junction === 'and' ? { and: children } : { or: children };
    }
    case 'not':
      return { not: toFilter(node.child, false)! };
    case 'related': {
      const related: Record<string, unknown> = { direction: node.direction };
      if (node.relTypes !== undefined) related.rel_types = node.relTypes;
      if (node.maxDepth !== undefined) related.max_depth = node.maxDepth;
      related.where = node.child ? toFilter(node.child, false) : undefined;
      return { related: related as unknown as FilterRelated };
    }
    case 'compare': {
      const out: FilterExpr = { field: node.field, op: node.op };
      if (node.value !== undefined) out.value = node.value;
      return out;
    }
  }
}

/** children returns the nodes directly below a node. */
export function childrenOf(node: BuilderNode): BuilderNode[] {
  switch (node.kind) {
    case 'group':
      return node.children;
    case 'not':
      return [node.child];
    case 'related':
      return node.child ? [node.child] : [];
    default:
      return [];
  }
}

/** treeStats counts the nodes and the depth of a tree as the server does (root depth 1). */
export function treeStats(node: BuilderNode | null): { nodes: number; depth: number } {
  if (!node) return { nodes: 0, depth: 0 };
  let nodes = 1;
  let depth = 0;
  for (const c of childrenOf(node)) {
    const s = treeStats(c);
    nodes += s.nodes;
    depth = Math.max(depth, s.depth);
  }
  return { nodes, depth: depth + 1 };
}

/** withinLimits reports whether a tree keeps the limits of the mode. */
export function withinLimits(node: BuilderNode | null, mode: BuilderMode = FILTER_MODE): boolean {
  const s = treeStats(node);
  return s.nodes <= mode.maxNodes && s.depth <= mode.maxDepth;
}

/** mapNode replaces the node of id by fn(node) (null removes it from a group or related). */
export function mapNode(
  node: BuilderNode,
  id: string,
  fn: (n: BuilderNode) => BuilderNode | null,
): BuilderNode | null {
  if (node.id === id) return fn(node);
  switch (node.kind) {
    case 'group': {
      const children = node.children.flatMap((c) => {
        const next = mapNode(c, id, fn);
        return next ? [next] : [];
      });
      return { ...node, children };
    }
    case 'not': {
      const child = mapNode(node.child, id, fn);
      // A negation without its node is gone with it.
      return child ? { ...node, child } : null;
    }
    case 'related':
      return { ...node, child: node.child ? mapNode(node.child, id, fn) : null };
    default:
      return node;
  }
}

/** findNode returns the node of id with its parent and the entity kind it compares. */
export function findNode(
  node: BuilderNode | null,
  id: string,
  entityKind: FilterEntityKind,
  parent: BuilderNode | null = null,
): { node: BuilderNode; parent: BuilderNode | null; entityKind: FilterEntityKind } | undefined {
  if (!node) return undefined;
  if (node.id === id) return { node, parent, entityKind };
  const kind = node.kind === 'related' ? 'ci' : entityKind;
  for (const c of childrenOf(node)) {
    const hit = findNode(c, id, kind, node);
    if (hit) return hit;
  }
  return undefined;
}

/**
 * nodePaths maps the path of each node, as the server names it in a 422
 * (where, where.and[1], where.not, where.related.where …), to its id.
 */
export function nodePaths(node: BuilderNode | null, path = 'where'): Map<string, string> {
  const paths = new Map<string, string>();
  if (!node) return paths;
  paths.set(path, node.id);
  const add = (child: BuilderNode | null, sub: string) => {
    if (child) for (const [p, id] of nodePaths(child, sub)) paths.set(p, id);
  };
  switch (node.kind) {
    case 'group':
      node.children.forEach((c, i) => add(c, `${path}.${node.junction}[${i}]`));
      break;
    case 'not':
      add(node.child, `${path}.not`);
      break;
    case 'related':
      add(node.child, `${path}.related.where`);
      break;
  }
  return paths;
}

/**
 * nodeAtPath returns the id of the deepest node a server path names
 * (where.and[1].value, filter_spec.where.or[0].related.max_depth …).
 */
export function nodeAtPath(node: BuilderNode | null, path: string): string | undefined {
  return deepestPath(nodePaths(node), path.replace(/^filter_spec\./, ''));
}

/** deepestPath returns the id of the longest path of paths that p starts with. */
function deepestPath(paths: Map<string, string>, p: string): string | undefined {
  let best: string | undefined;
  let bestLength = -1;
  for (const [candidate, id] of paths) {
    const boundary = p.length === candidate.length || p[candidate.length] === '.';
    if (p.startsWith(candidate) && boundary && candidate.length > bestLength) {
      best = id;
      bestLength = candidate.length;
    }
  }
  return best;
}

/** The problems the builder reports at a node before anything is sent. */
export const NODE_ISSUES = [
  'empty_group',
  'no_field',
  'no_value',
  'list_empty',
  'two_values',
  'not_number',
  'not_time',
  'no_related_filter',
] as const;
export type NodeIssue = (typeof NODE_ISSUES)[number];

/** RFC 3339 time or date, as validTime of the server accepts it. */
const TIME_SHAPE = /^\d{4}-\d{2}-\d{2}(T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:[0-5]\d))?$/;

function valueIssue(kind: FieldKind, value: unknown): NodeIssue | undefined {
  if (value === undefined || value === null || value === '') return 'no_value';
  if (kind === 'number' && typeof value !== 'number') return 'not_number';
  if (kind === 'time' && (typeof value !== 'string' || !TIME_SHAPE.test(value))) return 'not_time';
  return undefined;
}

/**
 * nodeIssues checks each node of a tree for what the builder must complete
 * before the AST goes out: a field, a value of the kind, the list sizes of
 * in, between, has_any and has_all, a filter in a related node and no empty
 * group below the root.
 */
export function nodeIssues(
  node: BuilderNode | null,
  entityKind: FilterEntityKind,
  catalogs: Readonly<Partial<Record<FilterEntityKind, readonly FieldSpec[]>>>,
  root = true,
): Map<string, NodeIssue> {
  const issues = new Map<string, NodeIssue>();
  if (!node) return issues;
  const merge = (child: BuilderNode, kind: FilterEntityKind) => {
    for (const [id, issue] of nodeIssues(child, kind, catalogs, false)) issues.set(id, issue);
  };
  switch (node.kind) {
    case 'group':
      if (!root && node.children.length === 0) issues.set(node.id, 'empty_group');
      node.children.forEach((c) => merge(c, entityKind));
      break;
    case 'not':
      merge(node.child, entityKind);
      break;
    case 'related':
      if (!node.child) issues.set(node.id, 'no_related_filter');
      else merge(node.child, 'ci');
      break;
    case 'compare': {
      if (!node.field) {
        issues.set(node.id, 'no_field');
        break;
      }
      const spec = fieldSpecOf(node.field, entityKind, catalogs[entityKind] ?? [], node.value);
      const kind = spec?.kind ?? 'text';
      let issue: NodeIssue | undefined;
      if (node.op === 'exists') issue = undefined;
      else if (LIST_OPS.includes(node.op)) {
        const list = Array.isArray(node.value) ? node.value : [];
        if (node.op === 'between' && list.length !== 2) issue = 'two_values';
        else if (list.length === 0) issue = 'list_empty';
        else if (kind !== 'tag') issue = list.map((v) => valueIssue(kind, v)).find(Boolean);
      } else {
        issue = valueIssue(kind, node.value);
      }
      if (issue) issues.set(node.id, issue);
      break;
    }
  }
  return issues;
}

/** The result of checking a tree: the AST to send or the node and its problem. */
export type FilterCheck =
  | { ok: true; where: FilterExpr | undefined }
  | { ok: false; nodeId?: string; issue?: NodeIssue; message?: string };

/**
 * checkTree completes nodeIssues with parseFilter (structure, operators,
 * value forms and limits of WP-789), so only a valid AST goes out.
 */
export function checkTree(
  node: BuilderNode | null,
  entityKind: FilterEntityKind,
  catalogs: Readonly<Partial<Record<FilterEntityKind, readonly FieldSpec[]>>>,
): FilterCheck {
  const issues = nodeIssues(node, entityKind, catalogs);
  const first = issues.entries().next();
  if (!first.done) return { ok: false, nodeId: first.value[0], issue: first.value[1] };
  const where = toFilter(node);
  if (where === undefined) return { ok: true, where };
  try {
    parseFilter(where, entityKind);
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    const path = message.split(':', 1)[0] ?? '';
    return { ok: false, nodeId: nodeAtPath(node, path), message };
  }
  return { ok: true, where };
}

/**
 * convertValue fits a value to another operator: a scalar becomes the first
 * element of a list and back; exists takes true.
 */
export function convertValue(value: unknown, from: FilterOp, to: FilterOp): unknown {
  if (to === 'exists') return typeof value === 'boolean' && from === 'exists' ? value : true;
  if (from === 'exists') return LIST_OPS.includes(to) ? [] : undefined;
  const wasList = LIST_OPS.includes(from);
  const isList = LIST_OPS.includes(to);
  if (wasList && !isList) return Array.isArray(value) ? value[0] : undefined;
  if (!wasList && isList) {
    const list = value === undefined || value === '' ? [] : [value];
    return to === 'between' ? list.slice(0, 2) : list;
  }
  if (to === 'between' && Array.isArray(value)) return value.slice(0, 2);
  return value;
}

/** isFilterExpr reports whether a value of filter_spec.where can be loaded into the builder. */
export function isFilterExpr(value: unknown): value is FilterExpr {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

// ─── Condition mode (WP-801) ─────────────────────────────────────────────────

/**
 * The typed CI columns a condition may name (typedColumns of
 * backend/internal/ci/validation.go): the write path evaluates a catalog
 * rule against the attributes with these columns taking precedence (CI-01).
 */
export const CONDITION_COLUMNS: readonly string[] = [
  'name',
  'hostname',
  'fqdn',
  'serial_number',
  'manufacturer',
  'model',
  'os_name',
  'os_version',
  'firmware_version',
  'management_ip',
  'primary_mac',
  'hardware_uuid',
  'sys_object_id',
];

/** The junctions of a condition group: all (AND) and any (OR). */
export const CONDITION_JUNCTIONS = ['all', 'any'] as const;
export type ConditionJunction = (typeof CONDITION_JUNCTIONS)[number];

/** The node kinds of the condition mode: groups, negation and comparison. */
export const CONDITION_NODE_KINDS = ['all', 'any', 'not', 'compare'] as const;

/** The comparison operators of the condition grammar (api/rules.schema.json). */
export const CONDITION_OPS: readonly CompareOp[] = compareOps;
export type { CompareOp };

/** The operators whose value is a non-empty list. */
export const CONDITION_LIST_OPS: readonly CompareOp[] = ['in', 'not_in'];
/** The operators without a value. */
export const CONDITION_NO_VALUE_OPS: readonly CompareOp[] = ['empty', 'not_empty'];
/** The operators that compare numbers (a number value). */
export const CONDITION_NUMBER_OPS: readonly CompareOp[] = ['gt', 'gte', 'lt', 'lte'];

/**
 * The operators of each field kind: gt, gte, lt and lte compare numbers
 * only (evaluateCondition reads both sides as numbers), contains reads a
 * substring of a text or an element of a list.
 */
const CONDITION_OPS_BY_KIND: Readonly<Record<FieldKind, readonly CompareOp[]>> = {
  text: ['eq', 'ne', 'in', 'not_in', 'contains', 'empty', 'not_empty'],
  number: ['eq', 'ne', 'in', 'not_in', 'gt', 'gte', 'lt', 'lte', 'empty', 'not_empty'],
  time: ['eq', 'ne', 'in', 'not_in', 'empty', 'not_empty'],
  uuid: ['eq', 'ne', 'in', 'not_in', 'empty', 'not_empty'],
  bool: ['eq', 'ne', 'empty', 'not_empty'],
  tag: ['contains', 'empty', 'not_empty'],
};

/** The editor of an attribute in a condition: its values are scalars, no picker. */
function conditionEditor(def: AttributeDefinition): ValueEditorKind {
  if ((def.enum_values?.length ?? 0) > 0) return 'enum';
  switch (def.data_type) {
    case 'integer':
    case 'decimal':
    case 'number':
      return 'number';
    case 'date':
      return 'date';
    case 'datetime':
      return 'datetime';
    case 'boolean':
      return 'bool';
  }
  return 'text';
}

/** The field kind of a condition editor. */
function editorKind(editor: ValueEditorKind): FieldKind {
  switch (editor) {
    case 'number':
      return 'number';
    case 'bool':
      return 'bool';
    case 'date':
    case 'datetime':
      return 'time';
  }
  return 'text';
}

/**
 * conditionFieldCatalog lists the fields of a condition: the typed CI
 * columns and the attributes of the target (by key, as the write path
 * names them). An attribute named like a column is the column.
 */
export function conditionFieldCatalog(
  definitions: readonly AttributeDefinition[] = [],
): FieldSpec[] {
  const columns = CONDITION_COLUMNS.map((column): FieldSpec => ({
    field: column,
    source: 'column',
    kind: 'text',
    editor: 'text',
    column,
  }));
  const seen = new Set<string>(CONDITION_COLUMNS);
  const attributes = definitions.flatMap((d): FieldSpec[] => {
    if (seen.has(d.key)) return [];
    seen.add(d.key);
    const editor = conditionEditor(d);
    return [{ field: d.key, source: 'attribute', kind: editorKind(editor), editor, definition: d }];
  });
  return [...columns, ...attributes];
}

/** conditionFieldSpec returns the spec of a field, a text attribute if the catalog lacks it. */
export function conditionFieldSpec(
  field: string,
  catalog: readonly FieldSpec[],
): FieldSpec | undefined {
  if (!field) return undefined;
  return (
    catalog.find((f) => f.field === field) ?? {
      field,
      source: 'attribute',
      kind: 'text',
      editor: 'text',
    }
  );
}

/** The operators a field of a condition offers; a choice compares for equality only. */
export function conditionOpsFor(spec: FieldSpec | undefined): readonly CompareOp[] {
  if (!spec) return [];
  if (spec.editor === 'enum') return ['eq', 'ne', 'in', 'not_in', 'empty', 'not_empty'];
  return CONDITION_OPS_BY_KIND[spec.kind];
}

/**
 * convertConditionValue fits a value to another operator: a scalar becomes
 * the first element of a list and back, empty and not_empty take none, a
 * number operator keeps a number only.
 */
export function convertConditionValue(value: unknown, to: CompareOp): unknown {
  if (CONDITION_NO_VALUE_OPS.includes(to)) return undefined;
  const scalar: unknown = Array.isArray(value) ? value[0] : value;
  if (CONDITION_LIST_OPS.includes(to)) {
    if (Array.isArray(value)) return value;
    return scalar === undefined || scalar === '' ? [] : [scalar];
  }
  if (CONDITION_NUMBER_OPS.includes(to)) return typeof scalar === 'number' ? scalar : undefined;
  return scalar;
}

/**
 * A node of the condition builder. A group is all or any; implicit marks
 * the root group the builder adds itself, which emits its only child alone
 * (a loaded comparison stays a comparison).
 */
export type ConditionNode =
  | {
      id: string;
      kind: 'group';
      junction: ConditionJunction;
      children: ConditionNode[];
      implicit?: boolean;
    }
  | { id: string; kind: 'not'; child: ConditionNode }
  | { id: string; kind: 'compare'; field: string; op: CompareOp; value?: unknown };

export function newConditionComparison(): ConditionNode {
  return { id: newId(), kind: 'compare', field: '', op: 'eq' };
}

export function newConditionGroup(junction: ConditionJunction = 'all'): ConditionNode {
  return { id: newId(), kind: 'group', junction, children: [] };
}

/** The root of an empty condition: an implicit all group (null = applies always). */
export function emptyConditionRoot(): ConditionNode {
  return { id: newId(), kind: 'group', junction: 'all', children: [], implicit: true };
}

function conditionNodeOf(cond: Condition): ConditionNode {
  if ('rules' in cond) {
    if (cond.op === 'not') {
      return { id: newId(), kind: 'not', child: conditionNodeOf(cond.rules[0]!) };
    }
    return {
      id: newId(),
      kind: 'group',
      junction: cond.op,
      children: cond.rules.map(conditionNodeOf),
    };
  }
  const node: ConditionNode = { id: newId(), kind: 'compare', field: cond.field, op: cond.op };
  if ('value' in cond) node.value = cond.value;
  return node;
}

/**
 * fromCondition turns a condition into the tree of the builder (throws on
 * an invalid one, parseCondition): a group stays the root, any other node
 * goes into an implicit all group; null is the empty root.
 */
export function fromCondition(input: unknown): ConditionNode {
  if (input === null || input === undefined) return emptyConditionRoot();
  const node = conditionNodeOf(parseCondition(input));
  if (node.kind === 'group') return node;
  return { id: newId(), kind: 'group', junction: 'all', children: [node], implicit: true };
}

/**
 * emittedConditionRoot is the node the root emits: the only child of an
 * implicit group, else the root itself; null for an empty implicit group.
 */
function emittedConditionRoot(root: ConditionNode): ConditionNode | null {
  if (root.kind === 'group' && root.implicit) {
    if (root.children.length === 0) return null;
    if (root.children.length === 1) return root.children[0]!;
  }
  return root;
}

function conditionOf(node: ConditionNode): Condition {
  switch (node.kind) {
    case 'group':
      return { op: node.junction, rules: node.children.map(conditionOf) };
    case 'not':
      return { op: 'not', rules: [conditionOf(node.child)] };
    case 'compare': {
      const out: Record<string, unknown> = { op: node.op, field: node.field };
      if (!CONDITION_NO_VALUE_OPS.includes(node.op) && node.value !== undefined) {
        out.value = node.value;
      }
      return out as Condition;
    }
  }
}

/** toCondition emits the condition of a tree; an empty root is none (null, applies always). */
export function toCondition(root: ConditionNode): Condition | null {
  const node = emittedConditionRoot(root);
  if (!node) return null;
  if (node === root && node.kind === 'group' && node.children.length === 0) return null;
  return conditionOf(node);
}

/** conditionChildren returns the nodes directly below a node. */
export function conditionChildren(node: ConditionNode): ConditionNode[] {
  if (node.kind === 'group') return node.children;
  if (node.kind === 'not') return [node.child];
  return [];
}

/** conditionStats counts the nodes and the depth of what a tree emits. */
export function conditionStats(root: ConditionNode): { nodes: number; depth: number } {
  const count = (node: ConditionNode): { nodes: number; depth: number } => {
    let nodes = 1;
    let depth = 0;
    for (const c of conditionChildren(node)) {
      const s = count(c);
      nodes += s.nodes;
      depth = Math.max(depth, s.depth);
    }
    return { nodes, depth: depth + 1 };
  };
  const node = emittedConditionRoot(root);
  return node ? count(node) : { nodes: 0, depth: 0 };
}

/**
 * conditionPaths maps the path of each node, as the server names it in a
 * 422 (condition, condition.rules[1], condition.rules[0].rules[0] …), to
 * its id.
 */
export function conditionPaths(root: ConditionNode, base = 'condition'): Map<string, string> {
  const paths = new Map<string, string>();
  const walk = (node: ConditionNode, path: string) => {
    paths.set(path, node.id);
    conditionChildren(node).forEach((c, i) => walk(c, `${path}.rules[${i}]`));
  };
  const node = emittedConditionRoot(root);
  if (node) walk(node, base);
  if (!paths.has(base)) paths.set(base, root.id);
  return paths;
}

/** conditionNodeAtPath returns the id of the deepest node a server path names. */
export function conditionNodeAtPath(root: ConditionNode, path: string): string | undefined {
  return deepestPath(conditionPaths(root), path);
}

/** conditionFields lists the fields the comparisons of a tree name, in order. */
export function conditionFields(root: ConditionNode): string[] {
  const out: string[] = [];
  const walk = (node: ConditionNode) => {
    if (node.kind === 'compare' && node.field && !out.includes(node.field)) out.push(node.field);
    conditionChildren(node).forEach(walk);
  };
  walk(root);
  return out;
}

/** conditionIssues checks each node for what the builder must complete before it goes out. */
export function conditionIssues(root: ConditionNode): Map<string, NodeIssue> {
  const issues = new Map<string, NodeIssue>();
  const walk = (node: ConditionNode, isRoot: boolean) => {
    switch (node.kind) {
      case 'group':
        if (!isRoot && node.children.length === 0) issues.set(node.id, 'empty_group');
        node.children.forEach((c) => walk(c, false));
        return;
      case 'not':
        walk(node.child, false);
        return;
      case 'compare': {
        let issue: NodeIssue | undefined;
        const v = node.value;
        if (!node.field) issue = 'no_field';
        else if (CONDITION_NO_VALUE_OPS.includes(node.op)) issue = undefined;
        else if (CONDITION_LIST_OPS.includes(node.op)) {
          if (!Array.isArray(v) || v.length === 0) issue = 'list_empty';
        } else if (v === undefined || v === null || v === '') issue = 'no_value';
        else if (CONDITION_NUMBER_OPS.includes(node.op) && typeof v !== 'number') {
          issue = 'not_number';
        }
        if (issue) issues.set(node.id, issue);
      }
    }
  };
  walk(root, true);
  return issues;
}

/** The result of checking a condition tree: the condition to send or the node and its problem. */
export type ConditionCheck =
  | { ok: true; condition: Condition | null }
  | { ok: false; nodeId?: string; issue?: NodeIssue; message?: string };

/**
 * checkCondition completes conditionIssues with parseCondition, so only a
 * condition of the grammar goes out (null: none, the rule applies always).
 */
export function checkCondition(root: ConditionNode): ConditionCheck {
  const first = conditionIssues(root).entries().next();
  if (!first.done) return { ok: false, nodeId: first.value[0], issue: first.value[1] };
  const condition = toCondition(root);
  if (condition === null) return { ok: true, condition };
  try {
    return { ok: true, condition: parseCondition(condition) };
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    const local = message.split(': ', 1)[0] ?? '';
    const path = /^(rules\[|op$|field$|value)/.test(local) ? `condition.${local}` : 'condition';
    return { ok: false, nodeId: conditionNodeAtPath(root, path), message };
  }
}

/** mapConditionNode replaces the node of id by fn(node) (null removes it). */
export function mapConditionNode(
  node: ConditionNode,
  id: string,
  fn: (n: ConditionNode) => ConditionNode | null,
): ConditionNode | null {
  if (node.id === id) return fn(node);
  if (node.kind === 'group') {
    const children = node.children.flatMap((c) => {
      const next = mapConditionNode(c, id, fn);
      return next ? [next] : [];
    });
    return { ...node, children };
  }
  if (node.kind === 'not') {
    const child = mapConditionNode(node.child, id, fn);
    return child ? { ...node, child } : null;
  }
  return node;
}

// ─── Validation mode (WP-801) ────────────────────────────────────────────────

/** The node kinds of the validation mode: the value rule AST of MET-45. */
export const VALIDATION_NODE_KINDS = ['all', 'required', 'pattern', 'range', 'enum'] as const;
export type ValidationNodeKind = (typeof VALIDATION_NODE_KINDS)[number];

/** What a range measures: the value (a number) or the length of a text. */
export const RANGE_OF = ['value', 'length'] as const;
export type RangeOf = (typeof RANGE_OF)[number];

/** The problems the validation mode reports at a node. */
export const VALIDATION_ISSUES = [
  'empty_group',
  'no_pattern',
  'bad_pattern',
  'no_bound',
  'min_above_max',
  'list_empty',
] as const;
export type ValidationIssue = (typeof VALIDATION_ISSUES)[number];

/**
 * A node of the validation builder. implicit marks the root all the
 * builder adds itself, which emits its only child alone.
 */
export type ValidationNode =
  | { id: string; kind: 'all'; children: ValidationNode[]; implicit?: boolean }
  | { id: string; kind: 'required' }
  | { id: string; kind: 'pattern'; pattern: string }
  | { id: string; kind: 'range'; of: RangeOf; min?: number; max?: number }
  | { id: string; kind: 'enum'; values: Scalar[] };

/** newValidationNode returns an empty node of a kind; a range measures by the field kind. */
export function newValidationNode(
  kind: ValidationNodeKind,
  fieldKind: FieldKind = 'text',
): ValidationNode {
  switch (kind) {
    case 'all':
      return { id: newId(), kind, children: [] };
    case 'required':
      return { id: newId(), kind };
    case 'pattern':
      return { id: newId(), kind, pattern: '' };
    case 'range':
      return { id: newId(), kind, of: fieldKind === 'number' ? 'value' : 'length' };
    case 'enum':
      return { id: newId(), kind, values: [] };
  }
}

/** The root of an empty validation: an implicit all. */
export function emptyValidationRoot(): ValidationNode {
  return { id: newId(), kind: 'all', children: [], implicit: true };
}

function validationNodeOf(rule: Rule): ValidationNode {
  switch (rule.op) {
    case 'all':
      return { id: newId(), kind: 'all', children: rule.rules.map(validationNodeOf) };
    case 'required':
      return { id: newId(), kind: 'required' };
    case 'pattern':
      return { id: newId(), kind: 'pattern', pattern: rule.pattern };
    case 'range': {
      const node: ValidationNode = { id: newId(), kind: 'range', of: rule.of ?? 'value' };
      if (rule.min !== undefined) node.min = rule.min;
      if (rule.max !== undefined) node.max = rule.max;
      return node;
    }
    case 'enum':
      return { id: newId(), kind: 'enum', values: [...rule.values] };
  }
}

/** fromValidation turns a value rule into the tree of the builder (throws on an invalid one). */
export function fromValidation(input: unknown): ValidationNode {
  if (input === null || input === undefined) return emptyValidationRoot();
  const node = validationNodeOf(parseRule(input));
  if (node.kind === 'all') return node;
  return { id: newId(), kind: 'all', children: [node], implicit: true };
}

function emittedValidationRoot(root: ValidationNode): ValidationNode | null {
  if (root.kind === 'all' && root.implicit) {
    if (root.children.length === 0) return null;
    if (root.children.length === 1) return root.children[0]!;
  }
  return root;
}

function ruleOf(node: ValidationNode): Rule {
  switch (node.kind) {
    case 'all':
      return { op: 'all', rules: node.children.map(ruleOf) };
    case 'required':
      return { op: 'required' };
    case 'pattern':
      return { op: 'pattern', pattern: node.pattern };
    case 'range': {
      const out: Extract<Rule, { op: 'range' }> = { op: 'range', of: node.of };
      if (node.min !== undefined) out.min = node.min;
      if (node.max !== undefined) out.max = node.max;
      return out;
    }
    case 'enum':
      return { op: 'enum', values: node.values };
  }
}

/** toValidation emits the value rule of a tree; an empty root is none (null). */
export function toValidation(root: ValidationNode): Rule | null {
  const node = emittedValidationRoot(root);
  if (!node) return null;
  if (node === root && node.kind === 'all' && node.children.length === 0) return null;
  return ruleOf(node);
}

/** validationChildren returns the nodes directly below a node. */
export function validationChildren(node: ValidationNode): ValidationNode[] {
  return node.kind === 'all' ? node.children : [];
}

/** validationStats counts the nodes and the depth of what a tree emits. */
export function validationStats(root: ValidationNode): { nodes: number; depth: number } {
  const count = (node: ValidationNode): { nodes: number; depth: number } => {
    let nodes = 1;
    let depth = 0;
    for (const c of validationChildren(node)) {
      const s = count(c);
      nodes += s.nodes;
      depth = Math.max(depth, s.depth);
    }
    return { nodes, depth: depth + 1 };
  };
  const node = emittedValidationRoot(root);
  return node ? count(node) : { nodes: 0, depth: 0 };
}

/** validationPaths maps the server path of each node (ast, ast.rules[1] …) to its id. */
export function validationPaths(root: ValidationNode, base = 'ast'): Map<string, string> {
  const paths = new Map<string, string>();
  const walk = (node: ValidationNode, path: string) => {
    paths.set(path, node.id);
    validationChildren(node).forEach((c, i) => walk(c, `${path}.rules[${i}]`));
  };
  const node = emittedValidationRoot(root);
  if (node) walk(node, base);
  if (!paths.has(base)) paths.set(base, root.id);
  return paths;
}

/** validationNodeAtPath returns the id of the deepest node a server path names. */
export function validationNodeAtPath(root: ValidationNode, path: string): string | undefined {
  return deepestPath(validationPaths(root), path);
}

/** patternIssue checks a pattern as parseRule does (common subset of RE2 and ECMAScript). */
function patternIssue(pattern: string): ValidationIssue | undefined {
  if (pattern === '') return 'no_pattern';
  if (/\(\?<?[=!]|\\[1-9]/.test(pattern)) return 'bad_pattern';
  try {
    new RegExp(pattern, 'u');
  } catch {
    return 'bad_pattern';
  }
  return undefined;
}

/** validationIssues checks each node for what the builder must complete before it goes out. */
export function validationIssues(root: ValidationNode): Map<string, ValidationIssue> {
  const issues = new Map<string, ValidationIssue>();
  const walk = (node: ValidationNode, isRoot: boolean) => {
    let issue: ValidationIssue | undefined;
    switch (node.kind) {
      case 'all':
        if (!isRoot && node.children.length === 0) issue = 'empty_group';
        node.children.forEach((c) => walk(c, false));
        break;
      case 'pattern':
        issue = patternIssue(node.pattern);
        break;
      case 'range':
        if (node.min === undefined && node.max === undefined) issue = 'no_bound';
        else if (node.min !== undefined && node.max !== undefined && node.min > node.max) {
          issue = 'min_above_max';
        }
        break;
      case 'enum':
        if (node.values.length === 0) issue = 'list_empty';
        break;
    }
    if (issue) issues.set(node.id, issue);
  };
  walk(root, true);
  return issues;
}

/** The result of checking a validation tree: the rule to send or the node and its problem. */
export type ValidationCheck =
  | { ok: true; ast: Rule | null }
  | { ok: false; nodeId?: string; issue?: ValidationIssue; message?: string };

/** checkValidation completes validationIssues with parseRule, so only a value rule goes out. */
export function checkValidation(root: ValidationNode): ValidationCheck {
  const first = validationIssues(root).entries().next();
  if (!first.done) return { ok: false, nodeId: first.value[0], issue: first.value[1] };
  const ast = toValidation(root);
  if (ast === null) return { ok: true, ast };
  try {
    return { ok: true, ast: parseRule(ast) };
  } catch (error) {
    return { ok: false, message: error instanceof Error ? error.message : String(error) };
  }
}

/** mapValidationNode replaces the node of id by fn(node) (null removes it). */
export function mapValidationNode(
  node: ValidationNode,
  id: string,
  fn: (n: ValidationNode) => ValidationNode | null,
): ValidationNode | null {
  if (node.id === id) return fn(node);
  if (node.kind !== 'all') return node;
  const children = node.children.flatMap((c) => {
    const next = mapValidationNode(c, id, fn);
    return next ? [next] : [];
  });
  return { ...node, children };
}

// ─── The modes of the rule catalog ───────────────────────────────────────────

/** The condition mode (WP-801): the condition grammar of api/rules.schema.json. */
export interface ConditionMode {
  id: 'condition';
  nodeKinds: readonly (typeof CONDITION_NODE_KINDS)[number][];
  junctions: readonly ConditionJunction[];
  ops: readonly CompareOp[];
  maxDepth: number;
  maxNodes: number;
}

/** The validation mode (WP-801): the value rule AST (all, required, pattern, range, enum). */
export interface ValidationMode {
  id: 'validation';
  nodeKinds: readonly ValidationNodeKind[];
  maxDepth: number;
  maxNodes: number;
}

/**
 * The limits of the two modes: the server sets none for rules; the builder
 * keeps those of the filter mode so a rule stays readable.
 */
export const CONDITION_MODE: ConditionMode = {
  id: 'condition',
  nodeKinds: CONDITION_NODE_KINDS,
  junctions: CONDITION_JUNCTIONS,
  ops: CONDITION_OPS,
  maxDepth: MAX_FILTER_DEPTH,
  maxNodes: MAX_FILTER_NODES,
};

export const VALIDATION_MODE: ValidationMode = {
  id: 'validation',
  nodeKinds: VALIDATION_NODE_KINDS,
  maxDepth: MAX_FILTER_DEPTH,
  maxNodes: MAX_FILTER_NODES,
};

/** The kinds of a catalog rule (MetamodelRuleKind). */
export const RULE_KINDS = ['validation', 'required', 'readonly', 'visible'] as const;
export type RuleKind = (typeof RULE_KINDS)[number];

/**
 * previewValue reads an input of the preview as a value of the field kind:
 * a number for number fields, a boolean for bool fields; empty is absent.
 */
export function previewValue(kind: FieldKind | undefined, input: string): unknown {
  if (input === '') return undefined;
  if (kind === 'number') {
    const n = Number(input);
    return input.trim() !== '' && Number.isFinite(n) ? n : input;
  }
  if (kind === 'bool') return input === 'true';
  return input;
}

// ─── Expression mode (WP-802) ────────────────────────────────────────────────

export type { Expr, ExprOp, ExprResultType };

/** The node kinds of the expression mode: the operators of $defs/expr. */
export const EXPR_NODE_KINDS: readonly ExprOp[] = EXPR_OPS;

/** The kinds of a literal: text, number or boolean. */
export const LITERAL_TYPES = ['string', 'number', 'boolean'] as const;
export type LiteralType = (typeof LITERAL_TYPES)[number];

/** The problems the expression mode reports at a node. */
export const EXPR_ISSUES = ['no_field', 'not_number', 'field_not_allowed'] as const;
export type ExprIssue = (typeof EXPR_ISSUES)[number];

/**
 * A node of the expression builder. A literal keeps the text of its input
 * (a number is read on emit); an operator with operands keeps them as
 * children, never fewer than its arity.
 */
export type ExprNode =
  | { id: string; kind: 'field'; field: string }
  | { id: string; kind: 'literal'; type: LiteralType; text: string }
  | { id: string; kind: 'today' }
  | { id: string; kind: ExprArgsOp; children: ExprNode[] };

/**
 * A field an expression can read: an entered (not computed) attribute of
 * the type or asset.<field> of the whitelist. type is the kind of sample
 * value of the preview; an attribute counts as any for the server's type
 * check.
 */
export interface ExprFieldSpec {
  field: string;
  source: 'attribute' | 'asset';
  type: ExprResultType;
  /** The attribute's definition (attributes only). */
  definition?: AttributeDefinition;
}

/** The sample kind of an attribute by its data type. */
function attributeResultType(dataType: string): ExprResultType {
  switch (dataType) {
    case 'integer':
    case 'decimal':
    case 'number':
      return 'number';
    case 'date':
      return 'date';
    case 'boolean':
      return 'boolean';
    default:
      return 'string';
  }
}

/**
 * exprFieldCatalog lists what an expression of the attribute selfKey can
 * read: the entered attributes of the definitions (no computed one, not
 * the attribute itself, each key once) and the asset fields of the
 * whitelist without the deprecated names.
 */
export function exprFieldCatalog(
  definitions: readonly AttributeDefinition[],
  selfKey?: string,
): ExprFieldSpec[] {
  const seen = new Set<string>();
  const attributes: ExprFieldSpec[] = [];
  for (const d of definitions) {
    if (d.computed || d.key === selfKey || seen.has(d.key)) continue;
    seen.add(d.key);
    attributes.push({
      field: d.key,
      source: 'attribute',
      type: attributeResultType(d.data_type),
      definition: d,
    });
  }
  attributes.sort((a, b) => a.field.localeCompare(b.field));
  const assets: ExprFieldSpec[] = Object.entries(EXPR_ASSET_FIELDS)
    .filter(([name]) => !DEPRECATED_EXPR_ASSET_FIELDS.includes(name))
    .map(([name, type]) => ({ field: `${ASSET_PREFIX}${name}`, source: 'asset', type }));
  return [...attributes, ...assets];
}

/** exprFieldSpec finds the spec of a reference; a whitelisted asset field outside the catalog too. */
export function exprFieldSpec(
  catalog: readonly ExprFieldSpec[],
  field: string,
): ExprFieldSpec | undefined {
  const spec = catalog.find((f) => f.field === field);
  if (spec) return spec;
  const name = field.startsWith(ASSET_PREFIX) ? field.slice(ASSET_PREFIX.length) : undefined;
  const type = name === undefined ? undefined : EXPR_ASSET_FIELDS[name];
  return type ? { field, source: 'asset', type } : undefined;
}

function isArgsKind(kind: ExprOp): kind is ExprArgsOp {
  return kind !== 'field' && kind !== 'literal' && kind !== 'today';
}

const ARITHMETIC: readonly ExprOp[] = ['add', 'sub', 'mul', 'div'];

/** newExprOperand returns the operand an operator adds: an empty field reference. */
export function newExprOperand(): ExprNode {
  return { id: newId(), kind: 'field', field: '' };
}

/**
 * newExprNode returns an empty node of a kind; an operator gets as many
 * operands as it needs (empty field references; a number literal for the
 * further operands of arithmetic).
 */
export function newExprNode(kind: ExprOp): ExprNode {
  switch (kind) {
    case 'field':
      return newExprOperand();
    case 'literal':
      return { id: newId(), kind, type: 'string', text: '' };
    case 'today':
      return { id: newId(), kind };
  }
  const [min] = EXPR_ARITY[kind];
  const children = Array.from({ length: min }, (_, i): ExprNode =>
    i > 0 && ARITHMETIC.includes(kind)
      ? { id: newId(), kind: 'literal', type: 'number', text: '' }
      : newExprOperand(),
  );
  return { id: newId(), kind, children };
}

/**
 * changeExprKind turns a node into another kind; an operator taking
 * operands keeps those of the node it replaces as far as its arity allows.
 */
export function changeExprKind(node: ExprNode, kind: ExprOp): ExprNode {
  if (node.kind === kind) return node;
  const next = newExprNode(kind);
  if (!isArgsKind(kind) || !('children' in next)) return { ...next, id: node.id };
  // A node that takes no operands becomes the first operand (with an id of
  // its own: the operator keeps the id of the node it replaces).
  const before = 'children' in node ? node.children : [{ ...node, id: newId() }];
  const [min, max] = EXPR_ARITY[kind];
  const kept = max === -1 ? before : before.slice(0, max);
  const children = [...kept, ...next.children.slice(kept.length)].slice(
    0,
    Math.max(min, kept.length),
  );
  return { id: node.id, kind, children };
}

/** exprCanAddOperand: the operator takes another operand. */
export function exprCanAddOperand(node: ExprNode): boolean {
  return 'children' in node && EXPR_ARITY[node.kind][1] === -1;
}

/** exprCanRemoveOperand: the operator keeps its arity without one operand. */
export function exprCanRemoveOperand(node: ExprNode): boolean {
  return 'children' in node && node.children.length > EXPR_ARITY[node.kind][0];
}

function exprNodeOf(e: Expr): ExprNode {
  switch (e.op) {
    case 'field':
      return { id: newId(), kind: 'field', field: e.field };
    case 'literal':
      return {
        id: newId(),
        kind: 'literal',
        type: typeof e.value as LiteralType,
        text: String(e.value),
      };
    case 'today':
      return { id: newId(), kind: 'today' };
    default:
      return { id: newId(), kind: e.op, children: e.args.map(exprNodeOf) };
  }
}

/** The root of a new expression: an empty field reference. */
export function emptyExprRoot(): ExprNode {
  return newExprOperand();
}

/** fromExpr turns an expression into the tree of the builder (throws on an invalid one). */
export function fromExpr(input: unknown): ExprNode {
  if (input === null || input === undefined) return emptyExprRoot();
  return exprNodeOf(parseExpr(input));
}

/** literalValue reads the input of a literal; NaN: no number. */
function literalValue(node: Extract<ExprNode, { kind: 'literal' }>): string | number | boolean {
  if (node.type === 'boolean') return node.text === 'true';
  if (node.type === 'number') {
    const n = node.text.trim() === '' ? Number.NaN : Number(node.text);
    return Number.isFinite(n) ? n : Number.NaN;
  }
  return node.text;
}

/** toExpr emits the expression of a tree ($defs/expr only). */
export function toExpr(node: ExprNode): Expr {
  switch (node.kind) {
    case 'field':
      return { op: 'field', field: node.field };
    case 'literal':
      return { op: 'literal', value: literalValue(node) };
    case 'today':
      return { op: 'today' };
    default:
      return { op: node.kind, args: node.children.map(toExpr) };
  }
}

/** exprChildren returns the operands of a node. */
export function exprChildren(node: ExprNode): ExprNode[] {
  return 'children' in node ? node.children : [];
}

/** exprStats counts the nodes and the depth of a tree. */
export function exprStats(root: ExprNode): { nodes: number; depth: number } {
  let nodes = 1;
  let depth = 0;
  for (const c of exprChildren(root)) {
    const s = exprStats(c);
    nodes += s.nodes;
    depth = Math.max(depth, s.depth);
  }
  return { nodes, depth: depth + 1 };
}

/** exprPaths maps the server path of each node (computed, computed.args[1] …) to its id. */
export function exprPaths(root: ExprNode, base = 'computed'): Map<string, string> {
  const paths = new Map<string, string>();
  const walk = (node: ExprNode, path: string) => {
    paths.set(path, node.id);
    exprChildren(node).forEach((c, i) => walk(c, `${path}.args[${i}]`));
  };
  walk(root, base);
  return paths;
}

/** exprNodeAtPath returns the id of the deepest node a server path names. */
export function exprNodeAtPath(root: ExprNode, path: string): string | undefined {
  return deepestPath(exprPaths(root), path);
}

/**
 * exprErrorPath reads the path an expression error names (args[1].op: …)
 * as a path below computed; a message without one names the expression.
 */
export function exprErrorPath(message: string): string {
  const path = /^((?:args\[\d+\]\.?)*(?:op|field|value|args)?|expression): /.exec(message)?.[1];
  if (!path || path === 'expression') return 'computed';
  return `computed.${path.replace(/\.$/, '')}`;
}

/** exprFieldsOf lists the references of a tree, each once in the order they appear. */
export function exprFieldsOf(root: ExprNode): string[] {
  const out: string[] = [];
  const walk = (node: ExprNode) => {
    if (node.kind === 'field' && node.field !== '' && !out.includes(node.field)) {
      out.push(node.field);
    }
    exprChildren(node).forEach(walk);
  };
  walk(root);
  return out;
}

/**
 * exprIssues checks each node for what the builder must complete before
 * it goes out: a reference to choose, a number to read, a reference that
 * the catalog does not offer (a computed attribute, the attribute itself).
 * allowed tells whether a reference may be read; without it every
 * reference may.
 */
export function exprIssues(
  root: ExprNode,
  allowed?: (field: string) => boolean,
): Map<string, ExprIssue> {
  const issues = new Map<string, ExprIssue>();
  const walk = (node: ExprNode) => {
    if (node.kind === 'field') {
      if (node.field === '') issues.set(node.id, 'no_field');
      else if (allowed && !allowed(node.field)) issues.set(node.id, 'field_not_allowed');
    }
    if (node.kind === 'literal' && node.type === 'number') {
      if (Number.isNaN(literalValue(node))) issues.set(node.id, 'not_number');
    }
    exprChildren(node).forEach(walk);
  };
  walk(root);
  return issues;
}

/** The result of checking an expression tree: the expression to send or the node and its problem. */
export type ExprCheck =
  | { ok: true; expr: Expr; resultType: ExprResultType }
  | { ok: false; nodeId?: string; issue?: ExprIssue; message?: string };

/**
 * checkExpr completes exprIssues with parseExpr, so only an expression of
 * $defs/expr goes out; a type error of parseExpr names its node.
 */
export function checkExpr(root: ExprNode, allowed?: (field: string) => boolean): ExprCheck {
  const first = exprIssues(root, allowed).entries().next();
  if (!first.done) return { ok: false, nodeId: first.value[0], issue: first.value[1] };
  try {
    const expr = parseExpr(toExpr(root));
    return { ok: true, expr, resultType: exprResultType(expr) };
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    return { ok: false, nodeId: exprNodeAtPath(root, exprErrorPath(message)), message };
  }
}

/** mapExprNode replaces the node of id by fn(node) (null removes an operand). */
export function mapExprNode(
  node: ExprNode,
  id: string,
  fn: (n: ExprNode) => ExprNode | null,
): ExprNode | null {
  if (node.id === id) return fn(node);
  if (!('children' in node)) return node;
  const children = node.children.flatMap((c) => {
    const next = mapExprNode(c, id, fn);
    return next ? [next] : [];
  });
  return { ...node, children };
}

/** The expression mode (WP-802): $defs/expr of api/rules.schema.json for computed attributes. */
export interface ExpressionMode {
  id: 'expression';
  nodeKinds: readonly ExprOp[];
  literalTypes: readonly LiteralType[];
  maxDepth: number;
  maxNodes: number;
}

/**
 * The limits of the expression mode: the nesting of the Go package
 * (maxExprDepth) and the node count of the other modes.
 */
export const EXPRESSION_MODE: ExpressionMode = {
  id: 'expression',
  nodeKinds: EXPR_NODE_KINDS,
  literalTypes: LITERAL_TYPES,
  maxDepth: MAX_EXPR_DEPTH,
  maxNodes: MAX_FILTER_NODES,
};

/**
 * exprSampleValue reads an input of the preview as a value of the sample
 * kind: a number for number fields, a boolean for boolean fields, text
 * otherwise; empty is absent.
 */
export function exprSampleValue(type: ExprResultType | undefined, input: string): unknown {
  if (input === '') return undefined;
  if (type === 'number') {
    const n = Number(input);
    return input.trim() !== '' && Number.isFinite(n) ? n : input;
  }
  if (type === 'boolean') return input === 'true';
  return input;
}
