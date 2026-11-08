/**
 * builderModes — the modes of the click builder RuleBuilder (CH18, E-54,
 * WP-315): node kinds, operators per field kind and limits of each mode,
 * and the fixed output format. This WP delivers the filter mode: it emits
 * FilterExpr of api/openapi.yaml (after WP-309 and WP-789), stored in
 * filter_spec.where of a saved view and sent as where of POST
 * /api/v1/search/query. The condition and validation modes (WP-801) and
 * the expression mode (WP-802) follow; no mode emits nodes of another.
 *
 * The binding check is the server's (savedview.Expr.Validate,
 * backend/internal/savedview/filter.go); the field whitelist and the
 * operator matrix below mirror astFields and validateComparison there. A
 * mirror that drifts from the server shows the 422 of the server at the
 * node (RuleBuilder).
 */

import type { components } from '../api/generated/schema';
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
  const p = path.replace(/^filter_spec\./, '');
  let best: string | undefined;
  let bestLength = -1;
  for (const [candidate, id] of nodePaths(node)) {
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
