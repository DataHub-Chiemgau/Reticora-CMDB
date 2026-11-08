import { useId, useMemo, useState } from 'react';
import type { KeyboardEvent as ReactKeyboardEvent } from 'react';
import { useTranslation } from 'react-i18next';
import type { LocationKind, LocationTreeNode } from '../../api/cmdb';
import { useLocationTree } from '../../api/cmdbHooks';
import { ChevronDown, ChevronRight, Search } from 'lucide-react';
import { Input } from '../ui/Input';
import { cn } from '../ui/utils';

/** PickedLocation is a chosen node with the names of its path, root first. */
export interface PickedLocation {
  id: string;
  name: string;
  kind: LocationKind;
  path: string[];
}

export interface LocationPickerProps {
  label: string;
  /** value is the id of the chosen node. */
  value: string | null;
  onChange: (value: PickedLocation | null) => void;
  /**
   * kinds limit the nodes that can be chosen, e.g. ['bin'] for stock
   * bookings; their ancestors stay visible as the path. Without kinds
   * every node can be chosen.
   */
  kinds?: readonly LocationKind[];
}

interface VisibleNode {
  node: LocationTreeNode;
  children: VisibleNode[];
  selectable: boolean;
}

interface FlatNode {
  id: string;
  node: LocationTreeNode;
  level: number;
  parentId?: string;
  hasChildren: boolean;
  selectable: boolean;
  path: string[];
}

/**
 * prune keeps the nodes to show: those that can be chosen and match the
 * filter, and the ancestors of such nodes. Below a node whose name matches
 * the whole subtree stays (the bins of a matched warehouse).
 */
function prune(
  nodes: LocationTreeNode[],
  filter: string,
  kinds: readonly LocationKind[] | undefined,
): VisibleNode[] {
  const out: VisibleNode[] = [];
  for (const node of nodes) {
    const matches = filter === '' || node.name.toLowerCase().includes(filter);
    const children = prune(node.children ?? [], matches ? '' : filter, kinds);
    const selectable = !kinds || kinds.includes(node.kind);
    if ((selectable && matches) || children.length > 0) {
      out.push({ node, children, selectable });
    }
  }
  return out;
}

/** findPath returns the names from the root to the node id, or undefined. */
function findPath(
  nodes: LocationTreeNode[],
  id: string,
  trail: LocationTreeNode[] = [],
): LocationTreeNode[] | undefined {
  for (const node of nodes) {
    const next = [...trail, node];
    if (node.id === id) return next;
    const found = findPath(node.children ?? [], id, next);
    if (found) return found;
  }
  return undefined;
}

/**
 * LocationPicker chooses a node of the location tree (GET /locations/tree,
 * UI-15, WP-318). The tree opens and closes, works with the keyboard (tree
 * pattern: arrow keys, Home/End, Enter or Space chooses) and filters by
 * name in the client. Warehouse and operations branch follow from the kind
 * of a node; there is no ?branch= parameter (WP-282).
 */
export function LocationPicker({ label, value, onChange, kinds }: LocationPickerProps) {
  const { t } = useTranslation();
  const treeId = useId();
  const tree = useLocationTree();
  const roots = useMemo(() => tree.data?.data ?? [], [tree.data]);
  const [filter, setFilter] = useState('');
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set());
  const [focused, setFocused] = useState<string | undefined>(undefined);

  const term = filter.trim().toLowerCase();
  const visible = useMemo(() => prune(roots, term, kinds), [roots, term, kinds]);
  const selectedPath = useMemo(() => (value ? findPath(roots, value) : undefined), [roots, value]);

  // While filtering every shown node is open; otherwise the opened ones and
  // the ancestors of the chosen node.
  const isOpen = (id: string) =>
    term !== '' ||
    expanded.has(id) ||
    !!selectedPath?.slice(0, -1).some((ancestor) => ancestor.id === id);

  const flat: FlatNode[] = [];
  const walk = (nodes: VisibleNode[], level: number, path: string[], parentId?: string) => {
    for (const entry of nodes) {
      const names = [...path, entry.node.name];
      flat.push({
        id: entry.node.id,
        node: entry.node,
        level,
        parentId,
        hasChildren: entry.children.length > 0,
        selectable: entry.selectable,
        path: names,
      });
      if (entry.children.length > 0 && isOpen(entry.node.id)) {
        walk(entry.children, level + 1, names, entry.node.id);
      }
    }
  };
  walk(visible, 1, []);

  const focusIndex = Math.max(
    flat.findIndex((item) => item.id === (focused ?? value)),
    0,
  );
  const itemId = (id: string) => `${treeId}-node-${id}`;

  function moveTo(index: number) {
    const item = flat[index];
    if (!item) return;
    setFocused(item.id);
    document.getElementById(itemId(item.id))?.focus();
  }

  function toggle(id: string, open?: boolean) {
    setExpanded((current) => {
      const next = new Set(current);
      if (open ?? !next.has(id)) next.add(id);
      else next.delete(id);
      return next;
    });
  }

  function choose(item: FlatNode) {
    if (!item.selectable) {
      if (item.hasChildren) toggle(item.id);
      return;
    }
    onChange({ id: item.id, name: item.node.name, kind: item.node.kind, path: item.path });
  }

  function handleKeyDown(event: ReactKeyboardEvent<HTMLUListElement>) {
    const item = flat[focusIndex];
    if (!item) return;
    switch (event.key) {
      case 'ArrowDown':
        moveTo(Math.min(focusIndex + 1, flat.length - 1));
        break;
      case 'ArrowUp':
        moveTo(Math.max(focusIndex - 1, 0));
        break;
      case 'Home':
        moveTo(0);
        break;
      case 'End':
        moveTo(flat.length - 1);
        break;
      case 'ArrowRight':
        if (item.hasChildren && !isOpen(item.id)) toggle(item.id, true);
        else if (item.hasChildren) moveTo(focusIndex + 1);
        break;
      case 'ArrowLeft':
        if (item.hasChildren && isOpen(item.id) && term === '') toggle(item.id, false);
        else if (item.parentId) moveTo(flat.findIndex((other) => other.id === item.parentId));
        break;
      case 'Enter':
      case ' ':
        choose(item);
        break;
      default:
        return;
    }
    event.preventDefault();
  }

  const kindLabel = (kind: LocationKind) => t(`locations.kinds.${kind}`, kind);

  function renderLevel(parentId: string | undefined) {
    return flat
      .filter((item) => item.parentId === parentId)
      .map((item) => {
        const open = item.hasChildren && isOpen(item.id);
        const index = flat.indexOf(item);
        return (
          <li
            key={item.id}
            id={itemId(item.id)}
            role="treeitem"
            aria-level={item.level}
            // Named by its own name and kind, not by the text of its subtree.
            aria-labelledby={`${itemId(item.id)}-name ${itemId(item.id)}-kind`}
            aria-expanded={item.hasChildren ? open : undefined}
            // A node that cannot be chosen carries no aria-selected. It is no
            // aria-disabled: that would disable the nodes of its subtree too
            // (WAI-ARIA), the bins below a warehouse included (WP-324).
            aria-selected={item.selectable ? item.id === value : undefined}
            tabIndex={index === focusIndex ? 0 : -1}
            onFocus={(event) => {
              if (event.target === event.currentTarget) setFocused(item.id);
            }}
            onClick={(event) => {
              event.stopPropagation();
              setFocused(item.id);
              choose(item);
            }}
            className="outline-none focus-visible:[&>div]:ring-2 focus-visible:[&>div]:ring-accent"
          >
            <div
              className={cn(
                'flex items-center gap-2 rounded-md px-2 py-1 text-sm',
                item.id === value
                  ? 'bg-accent-soft font-medium text-accent-strong'
                  : item.selectable
                    ? 'cursor-pointer text-fg hover:bg-fg/[0.05]'
                    : 'text-fg-muted',
              )}
              style={{ paddingLeft: `${(item.level - 1) * 1.25 + 0.5}rem` }}
            >
              <span
                aria-hidden="true"
                className="inline-flex h-4 w-4 cursor-pointer items-center justify-center text-fg-subtle"
                onClick={(event) => {
                  event.stopPropagation();
                  if (item.hasChildren) toggle(item.id);
                }}
              >
                {item.hasChildren ? (
                  open ? (
                    <ChevronDown className="h-4 w-4" />
                  ) : (
                    <ChevronRight className="h-4 w-4" />
                  )
                ) : null}
              </span>
              <span id={`${itemId(item.id)}-name`}>{item.node.name}</span>
              <span id={`${itemId(item.id)}-kind`} className="text-xs text-fg-muted">
                {kindLabel(item.node.kind)}
              </span>
            </div>
            {open ? <ul role="group">{renderLevel(item.id)}</ul> : null}
          </li>
        );
      });
  }

  return (
    <div className="space-y-2">
      <Input
        label={t('pickers.locationFilter', { label })}
        icon={<Search />}
        value={filter}
        onChange={(event) => setFilter(event.target.value)}
      />
      {tree.isLoading ? (
        <p className="text-sm text-fg-muted">{t('app.loading')}</p>
      ) : flat.length === 0 ? (
        <p className="text-sm text-fg-muted">{t('pickers.noHits')}</p>
      ) : (
        <ul
          role="tree"
          aria-label={label}
          onKeyDown={handleKeyDown}
          className="max-h-72 overflow-y-auto rounded-lg border border-line p-1"
        >
          {renderLevel(undefined)}
        </ul>
      )}
      {selectedPath ? (
        <p className="text-sm text-fg">
          {t('pickers.chosenLocation', {
            path: selectedPath.map((node) => node.name).join(' › '),
          })}
        </p>
      ) : null}
    </div>
  );
}
