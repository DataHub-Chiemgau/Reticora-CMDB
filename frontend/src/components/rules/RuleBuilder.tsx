import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Ban, FolderPlus, GitBranch, Plus, Trash2, Undo2 } from 'lucide-react';
import { Button, IconButton } from '../ui/Button';
import { Input } from '../ui/Input';
import { Select } from '../ui/Select';
import { cn } from '../ui/utils';
import { AttributePicker } from './AttributePicker';
import { ValueEditor } from './ValueEditor';
import {
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
} from '../../rules/builderModes';

/** A relationship type a related node may name (rel_types). */
export interface RuleBuilderRelType {
  key: string;
  label: string;
}

export interface RuleBuilderProps {
  /** mode fixes the output format; this WP delivers the filter mode (FilterExpr). */
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
 * RuleBuilder edits a rule by clicks, without a text DSL (CH18, E-54,
 * WP-315): groups (and/or) that can be nested, switched, negated and
 * deleted, comparisons with the operators of their field kind, and for CIs
 * related nodes up to depth 5. It keeps the limits of the server (10
 * levels, 100 nodes) and emits only FilterExpr (filter mode). It is
 * operable by keyboard: every action is a button or a select, and after a
 * delete the focus moves to the neighbouring node.
 */
export function RuleBuilder({
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
}: RuleBuilderProps) {
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
