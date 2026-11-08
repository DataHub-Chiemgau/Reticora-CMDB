import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Ban, FolderPlus, GitBranch, Plus, Trash2, Undo2 } from 'lucide-react';
import { Button, IconButton } from '../ui/Button';
import { Input } from '../ui/Input';
import { Select } from '../ui/Select';
import { cn } from '../ui/utils';
import { AttributePicker, fieldSpecLabel } from './AttributePicker';
import { ValueEditor } from './ValueEditor';
import { evaluateCondition, evaluateRule, type Condition, type Rule } from '../../rules/rules';
import { ASSET_PREFIX, computedTypeFit, evalExpr } from '../../rules/expr';
import { localizedText } from '../../api/cmdb';
import {
  CONDITION_JUNCTIONS,
  CONDITION_LIST_OPS,
  CONDITION_NO_VALUE_OPS,
  CONDITION_NUMBER_OPS,
  RANGE_OF,
  VALIDATION_NODE_KINDS,
  checkCondition,
  checkValidation,
  conditionChildren,
  conditionFieldSpec,
  conditionFields,
  conditionIssues,
  conditionNodeAtPath,
  conditionOpsFor,
  conditionStats,
  convertConditionValue,
  emptyConditionRoot,
  emptyValidationRoot,
  fromCondition,
  fromValidation,
  mapConditionNode,
  mapValidationNode,
  newConditionComparison,
  newConditionGroup,
  newValidationNode,
  previewValue,
  validationChildren,
  validationIssues,
  validationNodeAtPath,
  validationStats,
  type CompareOp,
  type ConditionCheck,
  type ConditionJunction,
  type ConditionMode,
  type ConditionNode,
  type RangeOf,
  type ValidationCheck,
  type ValidationMode,
  type ValidationNode,
  type ValidationNodeKind,
  FILTER_MODE,
  GROUP_JUNCTIONS,
  RELATED_DIRECTIONS,
  checkTree,
  childrenOf,
  convertValue,
  fieldSpecOf,
  findNode,
  fromFilter,
  mapNode,
  newComparison,
  newGroup,
  newRelated,
  nodeAtPath,
  nodeIssues,
  opsFor,
  treeStats,
  withinLimits,
  type BuilderMode,
  type BuilderNode,
  type FieldSpec,
  type FilterCheck,
  type FilterEntityKind,
  type FilterExpr,
  type FilterOp,
  type GroupJunction,
  type RelatedDirection,
  changeExprKind,
  checkExpr,
  emptyExprRoot,
  exprCanAddOperand,
  exprCanRemoveOperand,
  exprChildren,
  exprFieldSpec,
  exprFieldsOf,
  exprIssues,
  exprNodeAtPath,
  exprSampleValue,
  exprStats,
  fromExpr,
  mapExprNode,
  newExprOperand,
  type ExprCheck,
  type ExprFieldSpec,
  type ExprNode,
  type ExprOp,
  type ExprResultType,
  type ExpressionMode,
  type LiteralType,
} from '../../rules/builderModes';

/** A relationship type a related node may name (rel_types). */
export interface RuleBuilderRelType {
  key: string;
  label: string;
}

export interface FilterBuilderProps {
  /** mode fixes the output format: the filter mode (FilterExpr) by default. */
  mode?: BuilderMode;
  /** entityKind is the kind the filter queries; related nodes compare CIs. */
  entityKind: FilterEntityKind;
  /** initialValue is the AST loaded once; key the component to load another. */
  initialValue?: FilterExpr;
  /** fields are the fields per entity kind (fieldCatalog), ci also for related nodes. */
  fields: Readonly<Partial<Record<FilterEntityKind, readonly FieldSpec[]>>>;
  relationshipTypes?: readonly RuleBuilderRelType[];
  /** onChange reports every edit: the AST to send or why it cannot go out yet. */
  onChange?: (check: FilterCheck) => void;
  /** serverError is a 422 of the server naming the path of a node (where.and[1] …). */
  serverError?: { path: string; message: string };
  /** forbiddenFields are fields a 403 named (field permissions): marked, no longer offered. */
  forbiddenFields?: readonly string[];
  /** showIssues shows what is missing at each node (after a refused preview or save). */
  showIssues?: boolean;
  label?: string;
}

/** The ids of the node and its ancestors, the root first. */
function ancestry(node: BuilderNode, id: string): BuilderNode[] | undefined {
  if (node.id === id) return [node];
  for (const child of childrenOf(node)) {
    const trail = ancestry(child, id);
    if (trail) return [node, ...trail];
  }
  return undefined;
}

/**
 * neighbourAfterDelete is the node that takes the focus when id is deleted:
 * the next sibling, else the previous one, else the parent. A negation
 * goes with its node.
 */
function neighbourAfterDelete(root: BuilderNode, id: string): string | undefined {
  const trail = ancestry(root, id);
  if (!trail) return undefined;
  let index = trail.length - 1;
  while (index > 0 && trail[index - 1]!.kind === 'not') index--;
  const removed = trail[index]!;
  const parent = trail[index - 1];
  if (!parent) return undefined;
  if (parent.kind === 'group') {
    const at = parent.children.findIndex((c) => c.id === removed.id);
    return (parent.children[at + 1] ?? parent.children[at - 1] ?? parent).id;
  }
  return parent.id;
}

/**
 * FilterBuilder is the filter mode of the RuleBuilder (CH18, E-54,
 * WP-315): groups (and/or) that can be nested, switched, negated and
 * deleted, comparisons with the operators of their field kind, and for CIs
 * related nodes up to depth 5. It keeps the limits of the server (10
 * levels, 100 nodes) and emits only FilterExpr (filter mode). It is
 * operable by keyboard: every action is a button or a select, and after a
 * delete the focus moves to the neighbouring node.
 */
function FilterBuilder({
  mode = FILTER_MODE,
  entityKind,
  initialValue,
  fields,
  relationshipTypes = [],
  onChange,
  serverError,
  forbiddenFields = [],
  showIssues = false,
  label,
}: FilterBuilderProps) {
  const { t } = useTranslation();
  const [loadError] = useState<string | undefined>(() => {
    try {
      if (initialValue) fromFilter(initialValue);
      return undefined;
    } catch (error) {
      return error instanceof Error ? error.message : String(error);
    }
  });
  const [root, setRoot] = useState<BuilderNode>(() => {
    try {
      return initialValue ? fromFilter(initialValue) : newGroup('and');
    } catch {
      return newGroup('and');
    }
  });
  const [focusId, setFocusId] = useState<string | undefined>(undefined);
  const container = useRef<HTMLDivElement>(null);
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;

  const check = useMemo<FilterCheck>(
    () => (loadError ? { ok: false, message: loadError } : checkTree(root, entityKind, fields)),
    [root, entityKind, fields, loadError],
  );
  // Reports a changed result only: a reloaded field list that leaves the
  // AST as it was is no edit (and keeps the marks of the server).
  const reported = useRef<string | undefined>(undefined);
  useEffect(() => {
    const text = JSON.stringify(check);
    if (text === reported.current) return;
    reported.current = text;
    onChangeRef.current?.(check);
  }, [check]);

  // Moves the focus to a node after an add or a delete: its first control.
  useEffect(() => {
    if (!focusId) return;
    const node = container.current?.querySelector<HTMLElement>(`[data-node-id="${focusId}"]`);
    const control = node?.querySelector<HTMLElement>(
      'select:not([disabled]), input:not([disabled]), button:not([disabled])',
    );
    (control ?? node)?.focus();
    setFocusId(undefined);
  }, [focusId]);

  const issues = useMemo(
    () => (showIssues ? nodeIssues(root, entityKind, fields) : new Map()),
    [showIssues, root, entityKind, fields],
  );
  const serverNode = serverError ? nodeAtPath(root, serverError.path) : undefined;
  const forbidden = useMemo(() => new Set(forbiddenFields), [forbiddenFields]);
  const stats = treeStats(root);

  const replace = (id: string, fn: (n: BuilderNode) => BuilderNode | null) =>
    mapNode(root, id, fn) ?? newGroup('and');
  const apply = (next: BuilderNode, focus?: string) => {
    if (!withinLimits(next, mode)) return;
    setRoot(next);
    if (focus) setFocusId(focus);
  };
  const can = (next: BuilderNode) => withinLimits(next, mode);

  /** The tree with a node appended to the group id (or set as filter of the related node id). */
  const withChild = (id: string, child: BuilderNode) =>
    replace(id, (n) => {
      if (n.kind === 'group') return { ...n, children: [...n.children, child] };
      if (n.kind === 'related') return { ...n, child };
      return n;
    });
  /** The tree with a node next to the root (the root wrapped into an and group). */
  const besideRoot = (child: BuilderNode): BuilderNode =>
    root.kind === 'group'
      ? { ...root, children: [...root.children, child] }
      : { id: newGroup('and').id, kind: 'group', junction: 'and', children: [root, child] };
  const negated = (id: string) =>
    replace(id, (n) => ({ id: `${n.id}-not`, kind: 'not', child: n }));

  const remove = (id: string) => {
    const focus = neighbourAfterDelete(root, id);
    const next = id === root.id ? newGroup('and') : replace(id, () => null);
    setRoot(next);
    setFocusId(focus && findNode(next, focus, entityKind) ? focus : next.id);
  };

  const junctionLabel = (j: GroupJunction) => t(`rules.junctions.${j}`);

  function addButtons(kind: FilterEntityKind, target: (c: BuilderNode) => BuilderNode) {
    const comparison = newComparison();
    const group = newGroup('and');
    const related = newRelated();
    return (
      <div className="flex flex-wrap gap-2">
        <Button
          size="sm"
          variant="secondary"
          icon={<Plus />}
          disabled={!can(target(comparison))}
          onClick={() => apply(target(comparison), comparison.id)}
        >
          {t('rules.builder.addCondition')}
        </Button>
        <Button
          size="sm"
          variant="secondary"
          icon={<FolderPlus />}
          disabled={!can(target(group))}
          onClick={() => apply(target(group), group.id)}
        >
          {t('rules.builder.addGroup')}
        </Button>
        {kind === 'ci' ? (
          <Button
            size="sm"
            variant="secondary"
            icon={<GitBranch />}
            disabled={!can(target(related))}
            onClick={() => apply(target(related), related.id)}
          >
            {t('rules.builder.addRelated')}
          </Button>
        ) : null}
      </div>
    );
  }

  function nodeActions(node: BuilderNode, parent: BuilderNode | null, name: string) {
    const isRoot = node.id === root.id;
    return (
      <div className="flex shrink-0 items-center gap-1">
        {node.kind === 'not' ? (
          <IconButton
            size="sm"
            icon={<Undo2 />}
            label={t('rules.builder.removeNegation', { name })}
            onClick={() => apply(replace(node.id, (n) => (n.kind === 'not' ? n.child : n)))}
          />
        ) : parent?.kind !== 'not' ? (
          <IconButton
            size="sm"
            icon={<Ban />}
            label={t('rules.builder.negate', { name })}
            disabled={!can(negated(node.id))}
            onClick={() => apply(negated(node.id))}
          />
        ) : null}
        {!isRoot || node.kind !== 'group' || node.children.length > 0 ? (
          <IconButton
            size="sm"
            icon={<Trash2 />}
            label={t('rules.builder.delete', { name })}
            onClick={() => remove(node.id)}
            className="hover:text-danger"
          />
        ) : null}
      </div>
    );
  }

  function problem(node: BuilderNode): string | undefined {
    if (serverNode === node.id && serverError) {
      return t('rules.builder.serverError', { message: serverError.message });
    }
    if (node.kind === 'compare' && forbidden.has(node.field)) {
      return t('rules.builder.forbiddenField', { field: node.field });
    }
    const issue = issues.get(node.id);
    return issue ? t(`rules.issues.${issue}`) : undefined;
  }

  /** frame renders the box of a node: its role, label, focus target and problem. */
  function frame(node: BuilderNode, labelText: string, children: ReactNode, className?: string) {
    const error = problem(node);
    const errorId = `${node.id}-error`;
    return (
      <div
        key={node.id}
        data-node-id={node.id}
        data-invalid={error ? 'true' : undefined}
        role="group"
        aria-label={labelText}
        aria-describedby={error ? errorId : undefined}
        tabIndex={-1}
        className={cn(
          'min-w-0 space-y-3 rounded-lg border p-3 outline-none focus-visible:ring-2 focus-visible:ring-accent',
          error ? 'border-red-500' : 'border-line',
          className,
        )}
      >
        {children}
        {error ? (
          <p id={errorId} className="text-sm text-danger">
            {error}
          </p>
        ) : null}
      </div>
    );
  }

  function renderNode(
    node: BuilderNode,
    parent: BuilderNode | null,
    kind: FilterEntityKind,
    position: number,
  ): ReactNode {
    switch (node.kind) {
      case 'group': {
        const name = t('rules.builder.groupName', { junction: junctionLabel(node.junction) });
        return frame(
          node,
          name,
          <>
            <div className="flex flex-wrap items-end justify-between gap-2">
              <div className="w-56 max-w-full">
                <Select
                  label={t('rules.builder.junction')}
                  value={node.junction}
                  onChange={(e) =>
                    apply(
                      replace(node.id, (n) =>
                        n.kind === 'group'
                          ? { ...n, junction: e.target.value as GroupJunction }
                          : n,
                      ),
                    )
                  }
                  options={GROUP_JUNCTIONS.map((j) => ({
                    value: j,
                    label: t(`rules.junctionHints.${j}`),
                  }))}
                />
              </div>
              {nodeActions(node, parent, name)}
            </div>
            {node.children.length === 0 && !parent ? (
              <p className="text-sm text-fg-muted">{t('rules.builder.empty')}</p>
            ) : null}
            <div className="space-y-3">
              {node.children.map((c, i) => (
                <div key={c.id} className="space-y-1">
                  {i > 0 ? (
                    <p aria-hidden="true" className="text-xs font-semibold text-fg-muted">
                      {junctionLabel(node.junction)}
                    </p>
                  ) : null}
                  {renderNode(c, node, kind, i + 1)}
                </div>
              ))}
            </div>
            {addButtons(kind, (c) => withChild(node.id, c))}
          </>,
          parent ? 'bg-fg/[0.02]' : undefined,
        );
      }
      case 'not': {
        const name = t('rules.builder.notName');
        return frame(
          node,
          name,
          <>
            <div className="flex items-center justify-between gap-2">
              <p className="text-sm font-semibold text-fg">{t('rules.nodeKinds.not')}</p>
              {nodeActions(node, parent, name)}
            </div>
            {renderNode(node.child, node, kind, 1)}
          </>,
          'border-dashed',
        );
      }
      case 'related': {
        const name = t('rules.builder.relatedName');
        const update = (patch: Partial<Extract<BuilderNode, { kind: 'related' }>>) =>
          apply(replace(node.id, (n) => (n.kind === 'related' ? { ...n, ...patch } : n)));
        const chosen = node.relTypes ?? [];
        return frame(
          node,
          name,
          <>
            <div className="flex items-center justify-between gap-2">
              <p className="text-sm font-semibold text-fg">{t('rules.nodeKinds.related')}</p>
              {nodeActions(node, parent, name)}
            </div>
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <Select
                label={t('rules.builder.direction')}
                value={node.direction}
                onChange={(e) => update({ direction: e.target.value as RelatedDirection })}
                options={RELATED_DIRECTIONS.map((d) => ({
                  value: d,
                  label: t(`rules.directions.${d}`),
                }))}
              />
              <Input
                label={t('rules.builder.maxDepth', { max: mode.maxRelatedDepth })}
                type="number"
                min={1}
                max={mode.maxRelatedDepth}
                value={String(node.maxDepth ?? 1)}
                onChange={(e) => {
                  const n = Math.round(Number(e.target.value));
                  if (Number.isFinite(n)) {
                    update({ maxDepth: Math.min(mode.maxRelatedDepth, Math.max(1, n)) });
                  }
                }}
              />
            </div>
            {relationshipTypes.length > 0 ? (
              <fieldset className="min-w-0">
                <legend className="text-sm font-medium text-fg">
                  {t('rules.builder.relTypes')}
                </legend>
                <div className="mt-2 grid grid-cols-2 gap-2 xl:grid-cols-3">
                  {relationshipTypes.map((r) => (
                    <label key={r.key} className="flex items-center gap-2 text-sm text-fg">
                      <input
                        type="checkbox"
                        checked={chosen.includes(r.key)}
                        onChange={() =>
                          update({
                            relTypes: chosen.includes(r.key)
                              ? chosen.filter((k) => k !== r.key)
                              : [...chosen, r.key],
                          })
                        }
                      />
                      {r.label}
                    </label>
                  ))}
                </div>
              </fieldset>
            ) : null}
            <p className="text-sm text-fg-muted">{t('rules.builder.relatedWhere')}</p>
            {node.child
              ? renderNode(node.child, node, 'ci', 1)
              : addButtons('ci', (c) => withChild(node.id, c))}
          </>,
        );
      }
      case 'compare':
        return renderComparison(node, parent, kind, position);
    }
  }

  function renderComparison(
    node: Extract<BuilderNode, { kind: 'compare' }>,
    parent: BuilderNode | null,
    kind: FilterEntityKind,
    position: number,
  ) {
    const catalog = fields[kind] ?? [];
    const offered = catalog.filter((f) => !forbidden.has(f.field));
    const spec = node.field ? fieldSpecOf(node.field, kind, catalog, node.value) : undefined;
    const ops = opsFor(spec);
    const name = t('rules.builder.comparisonName', { position });
    const set = (patch: Partial<typeof node>) =>
      apply(replace(node.id, (n) => (n.kind === 'compare' ? { ...n, ...patch } : n)));
    const valueLabel = t('rules.builder.value');
    return frame(
      node,
      name,
      <div className="flex flex-wrap items-start gap-3">
        <div className="grid min-w-0 flex-1 grid-cols-1 gap-3 md:grid-cols-3">
          <AttributePicker
            label={t('rules.builder.field')}
            fields={offered}
            value={node.field}
            current={spec}
            onChange={(field, picked) => {
              const nextOps = opsFor(picked);
              const op: FilterOp = nextOps.includes(node.op) ? node.op : (nextOps[0] ?? 'eq');
              set({ field, op, value: op === 'exists' ? true : undefined });
            }}
          />
          <Select
            label={t('rules.builder.operator')}
            value={node.op}
            disabled={!spec}
            onChange={(e) => {
              const op = e.target.value as FilterOp;
              set({ op, value: convertValue(node.value, node.op, op) });
            }}
            options={(ops.includes(node.op) ? ops : [node.op, ...ops]).map((op) => ({
              value: op,
              label: t(`rules.ops.${op}`),
            }))}
          />
          {spec ? (
            <ValueEditor
              key={`${node.field}:${node.op}`}
              label={valueLabel}
              spec={spec}
              op={node.op}
              value={node.value}
              onChange={(value) => set({ value })}
            />
          ) : null}
        </div>
        {nodeActions(node, parent, name)}
      </div>,
    );
  }

  const legendId = useId();
  return (
    <div ref={container} className="space-y-3">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <p id={legendId} className="text-sm font-medium text-fg">
          {label ?? t('rules.builder.label')}
        </p>
        <p className="text-xs text-fg-muted" aria-live="polite">
          {t('rules.builder.limits', {
            nodes: stats.nodes,
            maxNodes: mode.maxNodes,
            depth: stats.depth,
            maxDepth: mode.maxDepth,
          })}
        </p>
      </div>
      {loadError ? (
        <p role="alert" className="text-sm text-danger">
          {t('rules.builder.loadError', { message: loadError })}
        </p>
      ) : null}
      <div aria-labelledby={legendId}>{renderNode(root, null, entityKind, 1)}</div>
      {root.kind !== 'group' ? addButtons(entityKind, (c) => besideRoot(c)) : null}
    </div>
  );
}

// ─── Shared parts of the rule catalog modes (WP-801) ─────────────────────────

/** useFocusAfterEdit moves the focus to the first control of a node after an add or a delete. */
function useFocusAfterEdit() {
  const container = useRef<HTMLDivElement>(null);
  const [focusId, setFocusId] = useState<string | undefined>(undefined);
  useEffect(() => {
    if (!focusId) return;
    const node = container.current?.querySelector<HTMLElement>(`[data-node-id="${focusId}"]`);
    const control = node?.querySelector<HTMLElement>(
      'select:not([disabled]), input:not([disabled]), button:not([disabled])',
    );
    (control ?? node)?.focus();
    setFocusId(undefined);
  }, [focusId]);
  return { container, setFocusId };
}

/** useReport reports a changed check only (a reloaded field list is no edit). */
function useReport<T>(check: T, onChange?: (check: T) => void) {
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;
  const reported = useRef<string | undefined>(undefined);
  useEffect(() => {
    const text = JSON.stringify(check);
    if (text === reported.current) return;
    reported.current = text;
    onChangeRef.current?.(check);
  }, [check]);
}

/** NodeFrame renders the box of a node: its role, label, focus target and problem. */
function NodeFrame({
  id,
  label,
  error,
  className,
  children,
}: {
  id: string;
  label: string;
  error?: string;
  className?: string;
  children: ReactNode;
}) {
  const errorId = `${id}-error`;
  return (
    <div
      data-node-id={id}
      data-invalid={error ? 'true' : undefined}
      role="group"
      aria-label={label}
      aria-describedby={error ? errorId : undefined}
      tabIndex={-1}
      className={cn(
        'min-w-0 space-y-3 rounded-lg border p-3 outline-none focus-visible:ring-2 focus-visible:ring-accent',
        error ? 'border-red-500' : 'border-line',
        className,
      )}
    >
      {children}
      {error ? (
        <p id={errorId} className="text-sm text-danger">
          {error}
        </p>
      ) : null}
    </div>
  );
}

/** BuilderHead shows the label of a builder and its limits. */
function BuilderHead({
  labelId,
  label,
  stats,
  mode,
}: {
  labelId: string;
  label: string;
  stats: { nodes: number; depth: number };
  mode: { maxNodes: number; maxDepth: number };
}) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-wrap items-baseline justify-between gap-2">
      <p id={labelId} className="text-sm font-medium text-fg">
        {label}
      </p>
      <p className="text-xs text-fg-muted" aria-live="polite">
        {t('rules.builder.limits', {
          nodes: stats.nodes,
          maxNodes: mode.maxNodes,
          depth: stats.depth,
          maxDepth: mode.maxDepth,
        })}
      </p>
    </div>
  );
}

/** trail returns the nodes from the root to the node of id. */
function trail<N extends { id: string }>(
  node: N,
  id: string,
  children: (n: N) => N[],
): N[] | undefined {
  if (node.id === id) return [node];
  for (const child of children(node)) {
    const path = trail(child, id, children);
    if (path) return [node, ...path];
  }
  return undefined;
}

/** neighbourOf is the node that takes the focus when id is deleted (next, previous, parent). */
function neighbourOf<N extends { id: string; kind: string }>(
  root: N,
  id: string,
  children: (n: N) => N[],
): string | undefined {
  const path = trail(root, id, children);
  if (!path) return undefined;
  let index = path.length - 1;
  while (index > 0 && path[index - 1]!.kind === 'not') index--;
  const removed = path[index]!;
  const parent = path[index - 1];
  if (!parent) return undefined;
  const siblings = children(parent);
  const at = siblings.findIndex((c) => c.id === removed.id);
  return (siblings[at + 1] ?? siblings[at - 1] ?? parent).id;
}

// ─── Condition mode (WP-801) ─────────────────────────────────────────────────

export interface ConditionBuilderProps {
  /** The condition mode: emits only the condition grammar of api/rules.schema.json. */
  mode: ConditionMode;
  /** initialValue is the condition loaded once (null: applies always); key the component to load another. */
  initialValue?: Condition | null;
  /** fields are the typed CI columns and the attributes of the target (conditionFieldCatalog). */
  fields: readonly FieldSpec[];
  /** onChange reports every edit: the condition to send (null: none) or why it cannot go out. */
  onChange?: (check: ConditionCheck) => void;
  /** serverError is a 422 of the server naming the path of a node (condition.rules[1].field …). */
  serverError?: { path: string; message: string };
  showIssues?: boolean;
  label?: string;
}

function ConditionBuilder({
  mode,
  initialValue,
  fields,
  onChange,
  serverError,
  showIssues = false,
  label,
}: ConditionBuilderProps) {
  const { t, i18n } = useTranslation();
  const [loadError] = useState<string | undefined>(() => {
    try {
      fromCondition(initialValue);
      return undefined;
    } catch (error) {
      return error instanceof Error ? error.message : String(error);
    }
  });
  const [root, setRoot] = useState<ConditionNode>(() => {
    try {
      return fromCondition(initialValue);
    } catch {
      return emptyConditionRoot();
    }
  });
  const [sample, setSample] = useState<Record<string, string>>({});
  const { container, setFocusId } = useFocusAfterEdit();
  const check = useMemo<ConditionCheck>(
    () => (loadError ? { ok: false, message: loadError } : checkCondition(root)),
    [root, loadError],
  );
  useReport(check, onChange);
  const issues = useMemo(
    () => (showIssues ? conditionIssues(root) : new Map<string, string>()),
    [showIssues, root],
  );
  const serverNode = serverError ? conditionNodeAtPath(root, serverError.path) : undefined;
  const stats = conditionStats(root);

  const can = (next: ConditionNode) => {
    const s = conditionStats(next);
    return s.nodes <= mode.maxNodes && s.depth <= mode.maxDepth;
  };
  const replace = (id: string, fn: (n: ConditionNode) => ConditionNode | null) =>
    mapConditionNode(root, id, fn) ?? emptyConditionRoot();
  const apply = (next: ConditionNode, focus?: string) => {
    if (!can(next)) return;
    setRoot(next);
    if (focus) setFocusId(focus);
  };
  const withChild = (id: string, child: ConditionNode) =>
    replace(id, (n) => (n.kind === 'group' ? { ...n, children: [...n.children, child] } : n));
  const negated = (id: string) =>
    replace(id, (n) => ({ id: `${n.id}-not`, kind: 'not', child: n }));
  const remove = (id: string) => {
    const focus = neighbourOf(root, id, conditionChildren);
    const next = id === root.id ? emptyConditionRoot() : replace(id, () => null);
    setRoot(next);
    setFocusId(focus && trail(next, focus, conditionChildren) ? focus : next.id);
  };

  const problem = (node: ConditionNode): string | undefined => {
    if (serverNode === node.id && serverError) {
      return t('rules.builder.serverError', { message: serverError.message });
    }
    const issue = issues.get(node.id);
    return issue ? t(`rules.issues.${issue}`) : undefined;
  };

  function actions(node: ConditionNode, parent: ConditionNode | null, name: string) {
    const isRoot = node.id === root.id;
    return (
      <div className="flex shrink-0 items-center gap-1">
        {node.kind === 'not' ? (
          <IconButton
            size="sm"
            icon={<Undo2 />}
            label={t('rules.builder.removeNegation', { name })}
            onClick={() => apply(replace(node.id, (n) => (n.kind === 'not' ? n.child : n)))}
          />
        ) : parent?.kind !== 'not' && !isRoot ? (
          <IconButton
            size="sm"
            icon={<Ban />}
            label={t('rules.builder.negate', { name })}
            disabled={!can(negated(node.id))}
            onClick={() => apply(negated(node.id))}
          />
        ) : null}
        {!isRoot || (node.kind === 'group' && node.children.length > 0) ? (
          <IconButton
            size="sm"
            icon={<Trash2 />}
            label={t('rules.builder.delete', { name })}
            onClick={() => remove(node.id)}
            className="hover:text-danger"
          />
        ) : null}
      </div>
    );
  }

  function addButtons(groupId: string) {
    const comparison = newConditionComparison();
    const group = newConditionGroup('all');
    return (
      <div className="flex flex-wrap gap-2">
        <Button
          size="sm"
          variant="secondary"
          icon={<Plus />}
          disabled={!can(withChild(groupId, comparison))}
          onClick={() => apply(withChild(groupId, comparison), comparison.id)}
        >
          {t('rules.builder.addCondition')}
        </Button>
        <Button
          size="sm"
          variant="secondary"
          icon={<FolderPlus />}
          disabled={!can(withChild(groupId, group))}
          onClick={() => apply(withChild(groupId, group), group.id)}
        >
          {t('rules.builder.addGroup')}
        </Button>
      </div>
    );
  }

  function renderNode(node: ConditionNode, parent: ConditionNode | null, position: number) {
    switch (node.kind) {
      case 'group': {
        const junction = (j: ConditionJunction) => t(`rules.conditionJunctions.${j}`);
        const name = t('rules.builder.groupName', { junction: junction(node.junction) });
        return (
          <NodeFrame
            key={node.id}
            id={node.id}
            label={name}
            error={problem(node)}
            className={parent ? 'bg-fg/[0.02]' : undefined}
          >
            <div className="flex flex-wrap items-end justify-between gap-2">
              <div className="w-56 max-w-full">
                <Select
                  label={t('rules.builder.junction')}
                  value={node.junction}
                  onChange={(e) =>
                    apply(
                      replace(node.id, (n) =>
                        n.kind === 'group'
                          ? { ...n, junction: e.target.value as ConditionJunction }
                          : n,
                      ),
                    )
                  }
                  options={CONDITION_JUNCTIONS.map((j) => ({
                    value: j,
                    label: t(`rules.conditionJunctionHints.${j}`),
                  }))}
                />
              </div>
              {actions(node, parent, name)}
            </div>
            {node.children.length === 0 && !parent ? (
              <p className="text-sm text-fg-muted">{t('rules.builder.conditionEmpty')}</p>
            ) : null}
            <div className="space-y-3">
              {node.children.map((c, i) => (
                <div key={c.id} className="space-y-1">
                  {i > 0 ? (
                    <p aria-hidden="true" className="text-xs font-semibold text-fg-muted">
                      {junction(node.junction)}
                    </p>
                  ) : null}
                  {renderNode(c, node, i + 1)}
                </div>
              ))}
            </div>
            {addButtons(node.id)}
          </NodeFrame>
        );
      }
      case 'not': {
        const name = t('rules.builder.notName');
        return (
          <NodeFrame
            key={node.id}
            id={node.id}
            label={name}
            error={problem(node)}
            className="border-dashed"
          >
            <div className="flex items-center justify-between gap-2">
              <p className="text-sm font-semibold text-fg">{t('rules.nodeKinds.not')}</p>
              {actions(node, parent, name)}
            </div>
            {renderNode(node.child, node, 1)}
          </NodeFrame>
        );
      }
      case 'compare': {
        const spec = conditionFieldSpec(node.field, fields);
        const ops = conditionOpsFor(spec);
        const name = t('rules.builder.comparisonName', { position });
        const set = (patch: Partial<Extract<ConditionNode, { kind: 'compare' }>>) =>
          apply(replace(node.id, (n) => (n.kind === 'compare' ? { ...n, ...patch } : n)));
        const numeric = CONDITION_NUMBER_OPS.includes(node.op);
        const editorSpec: FieldSpec | undefined =
          spec && numeric && spec.editor !== 'number'
            ? { ...spec, kind: 'number', editor: 'number' }
            : spec;
        return (
          <NodeFrame key={node.id} id={node.id} label={name} error={problem(node)}>
            <div className="flex flex-wrap items-start gap-3">
              <div className="grid min-w-0 flex-1 grid-cols-1 gap-3 md:grid-cols-3">
                <AttributePicker
                  label={t('rules.builder.field')}
                  fields={fields}
                  value={node.field}
                  current={spec}
                  onChange={(field, picked) => {
                    const next = conditionOpsFor(picked ?? conditionFieldSpec(field, fields));
                    const op: CompareOp = next.includes(node.op) ? node.op : (next[0] ?? 'eq');
                    set({ field, op, value: undefined });
                  }}
                />
                <Select
                  label={t('rules.builder.operator')}
                  value={node.op}
                  disabled={!spec}
                  onChange={(e) => {
                    const op = e.target.value as CompareOp;
                    set({ op, value: convertConditionValue(node.value, op) });
                  }}
                  options={(ops.includes(node.op) ? ops : [node.op, ...ops]).map((op) => ({
                    value: op,
                    label: t(`rules.conditionOps.${op}`),
                  }))}
                />
                {editorSpec && !CONDITION_NO_VALUE_OPS.includes(node.op) ? (
                  <ValueEditor
                    key={`${node.field}:${node.op}`}
                    label={t('rules.builder.value')}
                    spec={editorSpec}
                    op={CONDITION_LIST_OPS.includes(node.op) ? 'in' : 'eq'}
                    value={node.value}
                    onChange={(value) => set({ value })}
                  />
                ) : null}
              </div>
              {actions(node, parent, name)}
            </div>
          </NodeFrame>
        );
      }
    }
  }

  // The preview: evaluateCondition against an editable sample object.
  const used = conditionFields(root);
  const values: Record<string, unknown> = {};
  for (const field of used) {
    const v = previewValue(conditionFieldSpec(field, fields)?.kind, sample[field] ?? '');
    if (v !== undefined) values[field] = v;
  }
  let outcome: string;
  if (!check.ok) outcome = t('rules.preview.incomplete');
  else if (check.condition === null) outcome = t('rules.preview.always');
  else {
    outcome = evaluateCondition(check.condition, values)
      ? t('rules.preview.holds')
      : t('rules.preview.notHolds');
  }

  const labelId = useId();
  return (
    <div ref={container} className="space-y-3">
      <BuilderHead
        labelId={labelId}
        label={label ?? t('rules.builder.conditionLabel')}
        stats={stats}
        mode={mode}
      />
      {loadError ? (
        <p role="alert" className="text-sm text-danger">
          {t('rules.builder.loadError', { message: loadError })}
        </p>
      ) : null}
      <div aria-labelledby={labelId}>{renderNode(root, null, 1)}</div>
      <section
        aria-label={t('rules.preview.conditionTitle')}
        className="space-y-2 rounded-lg border border-dashed border-line p-3"
      >
        <p className="text-sm font-medium text-fg">{t('rules.preview.conditionTitle')}</p>
        {used.length === 0 ? (
          <p className="text-sm text-fg-muted">{t('rules.preview.noFields')}</p>
        ) : (
          <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
            {used.map((field) => {
              const spec = conditionFieldSpec(field, fields)!;
              const fieldLabel = t('rules.preview.sampleOf', {
                field: fieldSpecLabel(t, i18n.language, spec),
              });
              const setValue = (v: string) => setSample((s) => ({ ...s, [field]: v }));
              return spec.kind === 'bool' ? (
                <Select
                  key={field}
                  label={fieldLabel}
                  value={sample[field] ?? ''}
                  onChange={(e) => setValue(e.target.value)}
                  options={[
                    { value: '', label: t('rules.preview.absent') },
                    { value: 'true', label: t('rules.bool.true') },
                    { value: 'false', label: t('rules.bool.false') },
                  ]}
                />
              ) : (
                <Input
                  key={field}
                  label={fieldLabel}
                  type={spec.kind === 'number' ? 'number' : 'text'}
                  value={sample[field] ?? ''}
                  onChange={(e) => setValue(e.target.value)}
                />
              );
            })}
          </div>
        )}
        <p className="text-sm text-fg" aria-live="polite" data-testid="condition-preview">
          {outcome}
        </p>
      </section>
    </div>
  );
}

// ─── Validation mode (WP-801) ────────────────────────────────────────────────

export interface ValidationBuilderProps {
  /** The validation mode: emits only the value rule AST (all, required, pattern, range, enum). */
  mode: ValidationMode;
  /** initialValue is the value rule loaded once (null: none); key the component to load another. */
  initialValue?: Rule | null;
  /** attribute is the target attribute: its kind and enum values shape the editors and the preview. */
  attribute?: FieldSpec;
  /** onChange reports every edit: the rule to send (null: none) or why it cannot go out. */
  onChange?: (check: ValidationCheck) => void;
  /** serverError is a 422 of the server naming the path of a node (ast.rules[0] …). */
  serverError?: { path: string; message: string };
  showIssues?: boolean;
  label?: string;
}

/** The text spec an enum of a field without a definition edits. */
const TEXT_SPEC: FieldSpec = { field: 'value', source: 'attribute', kind: 'text', editor: 'text' };

/** parseBound reads a bound of a range: a number or none. */
function parseBound(value: string): number | undefined {
  if (value.trim() === '') return undefined;
  const n = Number(value);
  return Number.isFinite(n) ? n : undefined;
}

function ValidationBuilder({
  mode,
  initialValue,
  attribute,
  onChange,
  serverError,
  showIssues = false,
  label,
}: ValidationBuilderProps) {
  const { t } = useTranslation();
  const [loadError] = useState<string | undefined>(() => {
    try {
      fromValidation(initialValue);
      return undefined;
    } catch (error) {
      return error instanceof Error ? error.message : String(error);
    }
  });
  const [root, setRoot] = useState<ValidationNode>(() => {
    try {
      return fromValidation(initialValue);
    } catch {
      return emptyValidationRoot();
    }
  });
  const [sample, setSample] = useState('');
  const { container, setFocusId } = useFocusAfterEdit();
  const check = useMemo<ValidationCheck>(
    () => (loadError ? { ok: false, message: loadError } : checkValidation(root)),
    [root, loadError],
  );
  useReport(check, onChange);
  const issues = useMemo(
    () => (showIssues ? validationIssues(root) : new Map<string, string>()),
    [showIssues, root],
  );
  const serverNode = serverError ? validationNodeAtPath(root, serverError.path) : undefined;
  const stats = validationStats(root);
  const fieldKind = attribute?.kind ?? 'text';
  const valueSpec: FieldSpec = attribute
    ? { ...attribute, editor: attribute.editor === 'datetime' ? 'text' : attribute.editor }
    : TEXT_SPEC;

  const can = (next: ValidationNode) => {
    const s = validationStats(next);
    return s.nodes <= mode.maxNodes && s.depth <= mode.maxDepth;
  };
  const replace = (id: string, fn: (n: ValidationNode) => ValidationNode | null) =>
    mapValidationNode(root, id, fn) ?? emptyValidationRoot();
  const apply = (next: ValidationNode, focus?: string) => {
    if (!can(next)) return;
    setRoot(next);
    if (focus) setFocusId(focus);
  };
  const remove = (id: string) => {
    const focus = neighbourOf(root, id, validationChildren);
    const next = id === root.id ? emptyValidationRoot() : replace(id, () => null);
    setRoot(next);
    setFocusId(focus && trail(next, focus, validationChildren) ? focus : next.id);
  };
  const update = (id: string, patch: Partial<ValidationNode>) =>
    apply(replace(id, (n) => ({ ...n, ...patch }) as ValidationNode));

  const problem = (node: ValidationNode): string | undefined => {
    if (serverNode === node.id && serverError) {
      return t('rules.builder.serverError', { message: serverError.message });
    }
    const issue = issues.get(node.id);
    return issue ? t(`rules.issues.${issue}`) : undefined;
  };

  function deleteButton(node: ValidationNode, name: string) {
    const isRoot = node.id === root.id;
    if (isRoot && validationChildren(node).length === 0) return null;
    return (
      <IconButton
        size="sm"
        icon={<Trash2 />}
        label={t('rules.builder.delete', { name })}
        onClick={() => remove(node.id)}
        className="shrink-0 hover:text-danger"
      />
    );
  }

  function addButtons(groupId: string) {
    return (
      <div className="flex flex-wrap gap-2">
        {VALIDATION_NODE_KINDS.map((kind: ValidationNodeKind) => {
          const node = newValidationNode(kind, fieldKind);
          const next = replace(groupId, (n) =>
            n.kind === 'all' ? { ...n, children: [...n.children, node] } : n,
          );
          return (
            <Button
              key={kind}
              size="sm"
              variant="secondary"
              icon={kind === 'all' ? <FolderPlus /> : <Plus />}
              disabled={!can(next)}
              onClick={() => apply(next, node.id)}
            >
              {t(`rules.validation.add.${kind}`)}
            </Button>
          );
        })}
      </div>
    );
  }

  function renderNode(node: ValidationNode, parent: ValidationNode | null, position: number) {
    const kindLabel = t(`rules.validationKinds.${node.kind}`);
    const name = t('rules.validation.nodeName', { kind: kindLabel, position });
    const head = (
      <div className="flex items-center justify-between gap-2">
        <p className="text-sm font-semibold text-fg">{kindLabel}</p>
        {deleteButton(node, name)}
      </div>
    );
    switch (node.kind) {
      case 'all':
        return (
          <NodeFrame
            key={node.id}
            id={node.id}
            label={name}
            error={problem(node)}
            className={parent ? 'bg-fg/[0.02]' : undefined}
          >
            {head}
            {node.children.length === 0 && !parent ? (
              <p className="text-sm text-fg-muted">{t('rules.validation.empty')}</p>
            ) : null}
            <div className="space-y-3">
              {node.children.map((c, i) => renderNode(c, node, i + 1))}
            </div>
            {addButtons(node.id)}
          </NodeFrame>
        );
      case 'required':
        return (
          <NodeFrame key={node.id} id={node.id} label={name} error={problem(node)}>
            {head}
            <p className="text-sm text-fg-muted">{t('rules.validation.requiredHint')}</p>
          </NodeFrame>
        );
      case 'pattern':
        return (
          <NodeFrame key={node.id} id={node.id} label={name} error={problem(node)}>
            {head}
            <Input
              label={t('rules.validation.pattern')}
              value={node.pattern}
              className="font-mono"
              onChange={(e) => update(node.id, { pattern: e.target.value })}
            />
          </NodeFrame>
        );
      case 'range':
        return (
          <NodeFrame key={node.id} id={node.id} label={name} error={problem(node)}>
            {head}
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
              <Select
                label={t('rules.validation.of')}
                value={node.of}
                onChange={(e) => update(node.id, { of: e.target.value as RangeOf })}
                options={RANGE_OF.map((of) => ({ value: of, label: t(`rules.rangeOf.${of}`) }))}
              />
              <Input
                label={t('rules.validation.min')}
                type="number"
                step="any"
                value={node.min === undefined ? '' : String(node.min)}
                onChange={(e) => update(node.id, { min: parseBound(e.target.value) })}
              />
              <Input
                label={t('rules.validation.max')}
                type="number"
                step="any"
                value={node.max === undefined ? '' : String(node.max)}
                onChange={(e) => update(node.id, { max: parseBound(e.target.value) })}
              />
            </div>
          </NodeFrame>
        );
      case 'enum':
        return (
          <NodeFrame key={node.id} id={node.id} label={name} error={problem(node)}>
            {head}
            <ValueEditor
              label={t('rules.validation.values')}
              spec={valueSpec}
              op="in"
              value={node.values}
              onChange={(value) =>
                update(node.id, {
                  values: (Array.isArray(value) ? value : []) as (string | number | boolean)[],
                })
              }
            />
          </NodeFrame>
        );
    }
  }

  // The preview: evaluateRule against a sample value with its violation codes.
  const value = previewValue(fieldKind, sample);
  let outcome: ReactNode;
  if (!check.ok) outcome = <p className="text-sm text-fg">{t('rules.preview.incomplete')}</p>;
  else if (check.ast === null)
    outcome = <p className="text-sm text-fg">{t('rules.preview.noRule')}</p>;
  else {
    const violations = evaluateRule(check.ast, value);
    outcome =
      violations.length === 0 ? (
        <p className="text-sm text-fg">{t('rules.preview.valid')}</p>
      ) : (
        <ul aria-label={t('rules.preview.violations')} className="space-y-1 text-sm text-danger">
          {violations.map((v, i) => (
            <li key={`${v.code}-${i}`}>
              <code className="font-mono">{v.code}</code> {v.message}
            </li>
          ))}
        </ul>
      );
  }

  const labelId = useId();
  return (
    <div ref={container} className="space-y-3">
      <BuilderHead
        labelId={labelId}
        label={label ?? t('rules.builder.validationLabel')}
        stats={stats}
        mode={mode}
      />
      {loadError ? (
        <p role="alert" className="text-sm text-danger">
          {t('rules.builder.loadError', { message: loadError })}
        </p>
      ) : null}
      {!check.ok && check.message && !check.nodeId && !loadError ? (
        <p className="text-sm text-danger">
          {t('rules.builder.serverError', { message: check.message })}
        </p>
      ) : null}
      <div aria-labelledby={labelId}>{renderNode(root, null, 1)}</div>
      <section
        aria-label={t('rules.preview.validationTitle')}
        className="space-y-2 rounded-lg border border-dashed border-line p-3"
      >
        <p className="text-sm font-medium text-fg">{t('rules.preview.validationTitle')}</p>
        <div className="max-w-sm">
          {fieldKind === 'bool' ? (
            <Select
              label={t('rules.preview.sampleValue')}
              value={sample}
              onChange={(e) => setSample(e.target.value)}
              options={[
                { value: '', label: t('rules.preview.absent') },
                { value: 'true', label: t('rules.bool.true') },
                { value: 'false', label: t('rules.bool.false') },
              ]}
            />
          ) : (
            <Input
              label={t('rules.preview.sampleValue')}
              type={fieldKind === 'number' ? 'number' : 'text'}
              value={sample}
              onChange={(e) => setSample(e.target.value)}
            />
          )}
        </div>
        <div aria-live="polite" data-testid="validation-preview">
          {outcome}
        </div>
      </section>
    </div>
  );
}

// ─── Expression mode (WP-802) ────────────────────────────────────────────────

export interface ExpressionBuilderProps {
  /** The expression mode: emits only $defs/expr of api/rules.schema.json. */
  mode: ExpressionMode;
  /** initialValue is the expression loaded once (null: a new one); key the component to load another. */
  initialValue?: unknown;
  /** fields are what the expression can read: entered attributes of the type and asset fields. */
  fields: readonly ExprFieldSpec[];
  /** restrictFields allows only the references of fields (default); false leaves them to the server. */
  restrictFields?: boolean;
  /** dataType is the data type of the computed attribute: a result type that does not fit shows a hint. */
  dataType?: string;
  /** timeZone is the IANA zone of today in the preview (the zone of the organization). */
  timeZone: string;
  /** now is the instant of the preview (default: the time the builder opened). */
  now?: Date;
  /** onChange reports every edit: the expression to send or why it cannot go out. */
  onChange?: (check: ExprCheck) => void;
  /** serverError is a 422 of the server naming the path of a node (computed, computed.args[1] …). */
  serverError?: { path: string; message: string };
  showIssues?: boolean;
  label?: string;
}

/** The input type of a sample value of the preview. */
function sampleInputType(type: ExprResultType | undefined): string {
  if (type === 'number') return 'number';
  if (type === 'date') return 'date';
  return 'text';
}

function ExpressionBuilder({
  mode,
  initialValue,
  fields,
  restrictFields = true,
  dataType,
  timeZone,
  now: nowProp,
  onChange,
  serverError,
  showIssues = false,
  label,
}: ExpressionBuilderProps) {
  const { t, i18n } = useTranslation();
  const [loadError] = useState<string | undefined>(() => {
    try {
      fromExpr(initialValue);
      return undefined;
    } catch (error) {
      return error instanceof Error ? error.message : String(error);
    }
  });
  const [root, setRoot] = useState<ExprNode>(() => {
    try {
      return fromExpr(initialValue);
    } catch {
      return emptyExprRoot();
    }
  });
  const [openedAt] = useState(() => new Date());
  const now = nowProp ?? openedAt;
  const [sample, setSample] = useState<Record<string, string>>({});
  const { container, setFocusId } = useFocusAfterEdit();
  // Only the fields of the catalog may be read: no computed attribute, not
  // the attribute itself (the server refuses both with 422 on computed).
  const allowed = useMemo(() => {
    if (!restrictFields) return undefined;
    const keys = new Set(fields.map((f) => f.field));
    return (field: string) => keys.has(field);
  }, [fields, restrictFields]);
  const check = useMemo<ExprCheck>(
    () => (loadError ? { ok: false, message: loadError } : checkExpr(root, allowed)),
    [root, loadError, allowed],
  );
  useReport(check, onChange);
  const issues = useMemo(
    () => (showIssues ? exprIssues(root, allowed) : new Map<string, string>()),
    [showIssues, root, allowed],
  );
  const serverNode = serverError ? exprNodeAtPath(root, serverError.path) : undefined;
  const typeNode = !check.ok && !check.issue && check.message ? check.nodeId : undefined;
  const stats = exprStats(root);

  const can = (next: ExprNode) => {
    const s = exprStats(next);
    return s.nodes <= mode.maxNodes && s.depth <= mode.maxDepth;
  };
  const replace = (id: string, fn: (n: ExprNode) => ExprNode | null) =>
    mapExprNode(root, id, fn) ?? emptyExprRoot();
  const apply = (next: ExprNode, focus?: string) => {
    if (!can(next)) return;
    setRoot(next);
    if (focus) setFocusId(focus);
  };
  const removeOperand = (id: string) => {
    const focus = neighbourOf(root, id, exprChildren);
    const next = replace(id, () => null);
    setRoot(next);
    setFocusId(focus && trail(next, focus, exprChildren) ? focus : next.id);
  };

  const fieldLabel = (spec: ExprFieldSpec): string => {
    if (spec.source === 'asset') {
      const column = spec.field.slice(ASSET_PREFIX.length);
      return t('rules.fieldLabels.asset', { name: t(`savedviews.fields.${column}`, column) });
    }
    const name =
      (spec.definition && localizedText(spec.definition.display_name, i18n.language)) || spec.field;
    return t('rules.fieldLabels.attribute', { name });
  };

  const problem = (node: ExprNode): string | undefined => {
    if (serverNode === node.id && serverError) {
      return t('rules.builder.serverError', { message: serverError.message });
    }
    const issue = issues.get(node.id);
    if (issue) return t(`rules.issues.${issue}`);
    if (showIssues && typeNode === node.id && !check.ok && check.message) {
      return t('rules.expression.typeError', { message: check.message });
    }
    return undefined;
  };

  function renderNode(node: ExprNode, parent: ExprNode | null, position: number) {
    const kindLabel = t(`rules.exprOps.${node.kind}`);
    const name = parent
      ? t('rules.expression.operandName', { kind: kindLabel, position })
      : t('rules.expression.rootName', { kind: kindLabel });
    const head = (
      <div className="flex flex-wrap items-end justify-between gap-2">
        <div className="min-w-48 max-w-xs flex-1">
          <Select
            label={
              parent ? t('rules.expression.operandKind', { position }) : t('rules.expression.kind')
            }
            value={node.kind}
            onChange={(e) =>
              apply(
                replace(node.id, (n) => changeExprKind(n, e.target.value as ExprOp)),
                node.id,
              )
            }
            options={mode.nodeKinds.map((kind) => ({
              value: kind,
              label: t(`rules.exprOps.${kind}`),
            }))}
          />
        </div>
        {parent && exprCanRemoveOperand(parent) ? (
          <IconButton
            size="sm"
            icon={<Trash2 />}
            label={t('rules.builder.delete', { name })}
            onClick={() => removeOperand(node.id)}
            className="shrink-0 hover:text-danger"
          />
        ) : null}
      </div>
    );
    let body: ReactNode = null;
    switch (node.kind) {
      case 'field': {
        const known = fields.some((f) => f.field === node.field);
        const current = !known && node.field !== '' ? exprFieldSpec(fields, node.field) : undefined;
        const options = [
          { value: '', label: t('rules.builder.chooseField') },
          ...fields.map((f) => ({ value: f.field, label: fieldLabel(f) })),
          ...(!known && node.field !== ''
            ? [
                {
                  value: node.field,
                  label: t('rules.expression.unavailableField', {
                    name: current ? fieldLabel(current) : node.field,
                  }),
                },
              ]
            : []),
        ];
        body = (
          <Select
            label={t('rules.expression.field')}
            value={node.field}
            onChange={(e) => apply(replace(node.id, (n) => ({ ...n, field: e.target.value })))}
            options={options}
          />
        );
        break;
      }
      case 'literal':
        body = (
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <Select
              label={t('rules.expression.literalType')}
              value={node.type}
              onChange={(e) => {
                const type = e.target.value as LiteralType;
                apply(
                  replace(node.id, (n) => ({
                    ...n,
                    type,
                    text:
                      type === 'boolean'
                        ? 'true'
                        : n.kind === 'literal' && n.type !== 'boolean'
                          ? n.text
                          : '',
                  })),
                );
              }}
              options={mode.literalTypes.map((type) => ({
                value: type,
                label: t(`rules.literalTypes.${type}`),
              }))}
            />
            {node.type === 'boolean' ? (
              <Select
                label={t('rules.expression.literalValue')}
                value={node.text}
                onChange={(e) => apply(replace(node.id, (n) => ({ ...n, text: e.target.value })))}
                options={[
                  { value: 'true', label: t('rules.bool.true') },
                  { value: 'false', label: t('rules.bool.false') },
                ]}
              />
            ) : (
              <Input
                label={t('rules.expression.literalValue')}
                type={node.type === 'number' ? 'number' : 'text'}
                step={node.type === 'number' ? 'any' : undefined}
                value={node.text}
                onChange={(e) => apply(replace(node.id, (n) => ({ ...n, text: e.target.value })))}
              />
            )}
          </div>
        );
        break;
      case 'today':
        body = (
          <p className="text-sm text-fg-muted">
            {t('rules.expression.todayHint', { zone: timeZone })}
          </p>
        );
        break;
      default:
        body = (
          <>
            <div className="space-y-3 border-l-2 border-line pl-3">
              {node.children.map((c, i) => renderNode(c, node, i + 1))}
            </div>
            {exprCanAddOperand(node)
              ? (() => {
                  const operand = newExprOperand();
                  const next = replace(node.id, (n) =>
                    'children' in n ? { ...n, children: [...n.children, operand] } : n,
                  );
                  return (
                    <Button
                      size="sm"
                      variant="secondary"
                      icon={<Plus />}
                      disabled={!can(next)}
                      onClick={() => apply(next, operand.id)}
                    >
                      {t('rules.expression.addOperand')}
                    </Button>
                  );
                })()
              : null}
          </>
        );
    }
    return (
      <NodeFrame
        key={node.id}
        id={node.id}
        label={name}
        error={problem(node)}
        className={parent ? 'bg-fg/[0.02]' : undefined}
      >
        {head}
        {body}
      </NodeFrame>
    );
  }

  // The preview: evalExpr against editable sample values, with now and the
  // time zone of the organization.
  const referenced = exprFieldsOf(root);
  const specOf = (field: string) => exprFieldSpec(fields, field);
  let outcome: ReactNode;
  if (!check.ok) outcome = <p className="text-sm text-fg">{t('rules.preview.incomplete')}</p>;
  else {
    const inputs: Record<string, unknown> = {};
    for (const field of referenced) {
      const value = exprSampleValue(specOf(field)?.type, sample[field] ?? '');
      if (value !== undefined) inputs[field] = value;
    }
    const result = evalExpr(check.expr, inputs, now, timeZone);
    outcome =
      result === null ? (
        <p className="text-sm text-fg">{t('rules.preview.resultEmpty')}</p>
      ) : (
        <p className="text-sm text-fg">
          {t('rules.preview.result')}{' '}
          <code className="font-mono" data-testid="expression-result">
            {String(result)}
          </code>
        </p>
      );
  }
  const fit = check.ok && dataType ? computedTypeFit(check.expr, dataType) : 'fits';

  const labelId = useId();
  return (
    <div ref={container} className="space-y-3">
      <BuilderHead
        labelId={labelId}
        label={label ?? t('rules.builder.expressionLabel')}
        stats={stats}
        mode={mode}
      />
      {loadError ? (
        <p role="alert" className="text-sm text-danger">
          {t('rules.expression.loadError', { message: loadError })}
        </p>
      ) : null}
      {serverError && !serverNode ? (
        <p className="text-sm text-danger">
          {t('rules.builder.serverError', { message: serverError.message })}
        </p>
      ) : null}
      <div aria-labelledby={labelId}>{renderNode(root, null, 1)}</div>
      {check.ok && dataType && fit !== 'fits' ? (
        <p role="status" className="text-sm text-warning" data-testid="expression-type-hint">
          {fit === 'mismatch'
            ? t('rules.expression.typeMismatch', {
                result: t(`rules.resultTypes.${check.resultType}`),
                dataType: t(`citypes.dataTypes.${dataType}`, dataType),
              })
            : t('rules.expression.notComputable', {
                dataType: t(`citypes.dataTypes.${dataType}`, dataType),
              })}
        </p>
      ) : null}
      <section
        aria-label={t('rules.preview.expressionTitle')}
        className="space-y-2 rounded-lg border border-dashed border-line p-3"
      >
        <p className="text-sm font-medium text-fg">{t('rules.preview.expressionTitle')}</p>
        <p className="text-xs text-fg-muted">
          {t('rules.preview.today', {
            date: evalExpr({ op: 'today' }, {}, now, timeZone),
            zone: timeZone,
          })}
        </p>
        {referenced.length === 0 ? (
          <p className="text-sm text-fg-muted">{t('rules.preview.noInputs')}</p>
        ) : (
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            {referenced.map((field) => {
              const spec = specOf(field);
              const sampleLabel = t('rules.preview.sampleOf', {
                field: spec ? fieldLabel(spec) : field,
              });
              const value = sample[field] ?? '';
              const set = (v: string) => setSample((s) => ({ ...s, [field]: v }));
              return spec?.type === 'boolean' ? (
                <Select
                  key={field}
                  label={sampleLabel}
                  value={value}
                  onChange={(e) => set(e.target.value)}
                  options={[
                    { value: '', label: t('rules.preview.absent') },
                    { value: 'true', label: t('rules.bool.true') },
                    { value: 'false', label: t('rules.bool.false') },
                  ]}
                />
              ) : (
                <Input
                  key={field}
                  label={sampleLabel}
                  type={sampleInputType(spec?.type)}
                  step={spec?.type === 'number' ? 'any' : undefined}
                  value={value}
                  onChange={(e) => set(e.target.value)}
                />
              );
            })}
          </div>
        )}
        <div aria-live="polite" data-testid="expression-preview">
          {outcome}
        </div>
      </section>
    </div>
  );
}

// ─── The builder ─────────────────────────────────────────────────────────────

export type RuleBuilderProps =
  FilterBuilderProps | ConditionBuilderProps | ValidationBuilderProps | ExpressionBuilderProps;

function isConditionProps(props: RuleBuilderProps): props is ConditionBuilderProps {
  return props.mode?.id === 'condition';
}

function isValidationProps(props: RuleBuilderProps): props is ValidationBuilderProps {
  return props.mode?.id === 'validation';
}

function isExpressionProps(props: RuleBuilderProps): props is ExpressionBuilderProps {
  return props.mode?.id === 'expression';
}

/**
 * RuleBuilder edits a rule by clicks, without a text DSL (CH18, E-54). Its
 * mode fixes the output format and no mode emits nodes of another: the
 * filter mode (WP-315, default) emits FilterExpr, the condition mode
 * (WP-801) the condition grammar of api/rules.schema.json (all, any, not,
 * comparisons) with a preview by evaluateCondition, the validation mode
 * (WP-801) the value rule AST (all, required, pattern, range, enum) with a
 * preview by evaluateRule, the expression mode (WP-802) $defs/expr for
 * computed attributes with a preview by evalExpr in the zone of the
 * organization. Every output passes parseFilter, parseCondition, parseRule
 * or parseExpr before it is reported as ready.
 */
export function RuleBuilder(props: RuleBuilderProps) {
  if (isConditionProps(props)) return <ConditionBuilder {...props} />;
  if (isValidationProps(props)) return <ValidationBuilder {...props} />;
  if (isExpressionProps(props)) return <ExpressionBuilder {...props} />;
  return <FilterBuilder {...props} />;
}
