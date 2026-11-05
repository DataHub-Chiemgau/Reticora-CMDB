/**
 * TypeTree shows the CI types as a tree of their type chain (parent_type_id,
 * MET-06, WP-248) after the WAI-ARIA tree pattern (UI-11, WP-313): role
 * tree/treeitem/group, aria-expanded and aria-selected, one tab stop (roving
 * tabindex), arrow keys (down/up move, right opens or enters, left closes or
 * goes to the parent), Home/End, Enter or Space selects. The search filters
 * the loaded tree and keeps the ancestors of the matches visible.
 */
import { ChevronDown, ChevronRight } from 'lucide-react';
import { useMemo, useRef, useState, type KeyboardEvent } from 'react';
import { useTranslation } from 'react-i18next';
import type { CIType } from '../../api/cmdb';
import { Badge } from '../ui/Badge';
import { cn } from '../ui/utils';

export interface TypeTreeProps {
  types: CIType[];
  selectedId?: string;
  onSelect: (id: string) => void;
  /** Filters by name, label and key (case-insensitive); empty: every type. */
  search?: string;
  /** The label of a type in the UI language. */
  label: (type: CIType) => string;
}

interface Node {
  type: CIType;
  children: Node[];
}

/** buildForest arranges the types by parent; a type whose parent is not loaded is a root. */
export function buildForest(types: CIType[], label: (type: CIType) => string): Node[] {
  const byId = new Map(types.map((ty) => [ty.id, ty]));
  const children = new Map<string, CIType[]>();
  const roots: CIType[] = [];
  for (const ty of types) {
    const parent = ty.parent_type_id;
    if (parent && parent !== ty.id && byId.has(parent)) {
      children.set(parent, [...(children.get(parent) ?? []), ty]);
    } else {
      roots.push(ty);
    }
  }
  const byLabel = (a: CIType, b: CIType) => label(a).localeCompare(label(b));
  // A cycle cannot be stored (409, WP-248); seen guards the rendering anyway.
  const seen = new Set<string>();
  const build = (ty: CIType): Node => {
    seen.add(ty.id);
    return {
      type: ty,
      children: (children.get(ty.id) ?? [])
        .filter((c) => !seen.has(c.id))
        .sort(byLabel)
        .map(build),
    };
  };
  return roots.sort(byLabel).map(build);
}

/** ancestorsOf returns the chain above a type, nearest parent first. */
export function ancestorsOf(id: string | undefined, types: CIType[]): CIType[] {
  const byId = new Map(types.map((ty) => [ty.id, ty]));
  const chain: CIType[] = [];
  let parent = id ? byId.get(id)?.parent_type_id : undefined;
  while (parent && byId.has(parent) && !chain.some((c) => c.id === parent)) {
    const ty = byId.get(parent)!;
    chain.push(ty);
    parent = ty.parent_type_id;
  }
  return chain;
}

interface Row {
  type: CIType;
  level: number;
  parentId?: string;
  hasChildren: boolean;
}

export function TypeTree({ types, selectedId, onSelect, search = '', label }: TypeTreeProps) {
  const { t } = useTranslation();
  const forest = useMemo(() => buildForest(types, label), [types, label]);
  const [collapsed, setCollapsed] = useState<Set<string>>(() => new Set());
  const [focusedId, setFocusedId] = useState<string | undefined>();
  const items = useRef(new Map<string, HTMLLIElement>());

  const query = search.trim().toLowerCase();
  // The matches and their ancestors; undefined: no search, every type.
  const visible = useMemo(() => {
    if (!query) return undefined;
    const keep = new Set<string>();
    const visit = (node: Node): boolean => {
      const ty = node.type;
      const own = [label(ty), ty.name, ty.key].some((s) => s?.toLowerCase().includes(query));
      const below = node.children.map(visit).some(Boolean);
      if (own || below) keep.add(ty.id);
      return own || below;
    };
    forest.forEach(visit);
    return keep;
  }, [forest, query, label]);

  // The rows in display order: a search opens the path to every match.
  const rows: Row[] = [];
  const walk = (nodes: Node[], level: number, parentId?: string) => {
    for (const node of nodes) {
      if (visible && !visible.has(node.type.id)) continue;
      const kids = visible ? node.children.filter((c) => visible.has(c.type.id)) : node.children;
      rows.push({ type: node.type, level, parentId, hasChildren: kids.length > 0 });
      if (kids.length > 0 && (visible || !collapsed.has(node.type.id))) {
        walk(kids, level + 1, node.type.id);
      }
    }
  };
  walk(forest, 1);

  const isOpen = (row: Row) => row.hasChildren && (!!visible || !collapsed.has(row.type.id));
  const tabStop =
    rows.find((r) => r.type.id === focusedId)?.type.id ??
    rows.find((r) => r.type.id === selectedId)?.type.id ??
    rows[0]?.type.id;

  const focus = (id: string | undefined) => {
    if (!id) return;
    setFocusedId(id);
    items.current.get(id)?.focus();
  };
  const setOpen = (id: string, open: boolean) =>
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (open) next.delete(id);
      else next.add(id);
      return next;
    });

  const onKeyDown = (event: KeyboardEvent<HTMLLIElement>, row: Row) => {
    // Keys of an inner element (none today) and of a nested item stay theirs.
    if (event.target !== event.currentTarget) return;
    const index = rows.findIndex((r) => r.type.id === row.type.id);
    switch (event.key) {
      case 'ArrowDown':
        focus(rows[index + 1]?.type.id);
        break;
      case 'ArrowUp':
        focus(rows[index - 1]?.type.id);
        break;
      case 'Home':
        focus(rows[0]?.type.id);
        break;
      case 'End':
        focus(rows[rows.length - 1]?.type.id);
        break;
      case 'ArrowRight':
        if (!row.hasChildren) return;
        if (isOpen(row)) focus(rows[index + 1]?.type.id);
        else setOpen(row.type.id, true);
        break;
      case 'ArrowLeft':
        if (isOpen(row) && !visible) setOpen(row.type.id, false);
        else focus(row.parentId);
        break;
      case 'Enter':
      case ' ':
        onSelect(row.type.id);
        break;
      default:
        return;
    }
    event.preventDefault();
    event.stopPropagation();
  };

  const renderLevel = (level: number, parentId?: string) =>
    rows
      .filter((r) => r.level === level && r.parentId === parentId)
      .map((row) => {
        const ty = row.type;
        const open = isOpen(row);
        const selected = ty.id === selectedId;
        const labelId = `type-tree-${ty.id}-label`;
        const metaId = `type-tree-${ty.id}-meta`;
        return (
          <li
            key={ty.id}
            ref={(el) => {
              if (el) items.current.set(ty.id, el);
              else items.current.delete(ty.id);
            }}
            role="treeitem"
            aria-level={row.level}
            aria-selected={selected}
            aria-expanded={row.hasChildren ? open : undefined}
            aria-labelledby={labelId}
            aria-describedby={metaId}
            tabIndex={ty.id === tabStop ? 0 : -1}
            onKeyDown={(e) => onKeyDown(e, row)}
            onFocus={(e) => {
              if (e.target === e.currentTarget) setFocusedId(ty.id);
            }}
            onClick={(e) => {
              e.stopPropagation();
              setFocusedId(ty.id);
              onSelect(ty.id);
            }}
            className="outline-none [&:focus-visible>div]:ring-2 [&:focus-visible>div]:ring-accent/40"
          >
            <div
              className={cn(
                'flex cursor-pointer items-start gap-2 rounded-lg border px-3 py-2 transition-colors',
                selected
                  ? 'border-accent bg-accent-soft'
                  : 'border-line bg-panel hover:border-line-strong hover:bg-fg/[0.025]',
              )}
              style={{ marginLeft: `${(row.level - 1) * 1.25}rem` }}
            >
              <span
                aria-hidden
                className="mt-0.5 inline-flex h-4 w-4 shrink-0 items-center justify-center text-fg-muted"
                onClick={(e) => {
                  if (!row.hasChildren || visible) return;
                  e.stopPropagation();
                  setOpen(ty.id, !open);
                }}
              >
                {row.hasChildren ? (
                  open ? (
                    <ChevronDown className="h-4 w-4" />
                  ) : (
                    <ChevronRight className="h-4 w-4" />
                  )
                ) : null}
              </span>
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center justify-between gap-1">
                  <span id={labelId} className="font-medium text-fg [overflow-wrap:anywhere]">
                    {label(ty)}
                  </span>
                  <span id={metaId} className="flex flex-wrap gap-1">
                    {ty.is_system ? <Badge variant="info">{t('citypes.system')}</Badge> : null}
                    {!ty.is_active ? (
                      <Badge variant="warning">{t('citypes.inactive')}</Badge>
                    ) : null}
                    {ty.is_logical ? <Badge variant="neutral">{t('citypes.logical')}</Badge> : null}
                    {ty.is_observable ? (
                      <Badge variant="neutral">{t('citypes.observable')}</Badge>
                    ) : null}
                  </span>
                </div>
                <p className="mt-0.5 text-xs text-fg-muted [overflow-wrap:anywhere]">
                  {ty.key} · v{ty.version} · {t('citypes.schemaVersion')} {ty.schema_version}
                  {ty.category ? ` · ${ty.category}` : ''}
                </p>
              </div>
            </div>
            {row.hasChildren && open ? (
              <ul role="group" className="mt-2 space-y-2">
                {renderLevel(level + 1, ty.id)}
              </ul>
            ) : null}
          </li>
        );
      });

  if (rows.length === 0) {
    return (
      <p className="text-sm text-fg-muted">
        {query ? t('citypes.tree.noMatch') : t('citypes.tree.empty')}
      </p>
    );
  }
  return (
    <ul role="tree" aria-label={t('citypes.tree.label')} className="space-y-2">
      {renderLevel(1)}
    </ul>
  );
}
