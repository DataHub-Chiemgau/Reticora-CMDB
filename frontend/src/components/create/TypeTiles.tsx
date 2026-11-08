/**
 * TypeTiles — the type choice of the guided creation (MET-51, UI-12,
 * WP-317): one tile per type, grouped by category, with the subtypes of a
 * type (parent_type_id, WP-248) folded under it. Category and icon come
 * from the type itself (ci_type.category and ci_type.icon, E-117); a type
 * without a category stands under "other", one without a known icon gets a
 * generic device icon. The tiles are buttons: Tab reaches them, Enter and
 * Space choose one.
 */
import { useId, useMemo, useState } from 'react';
import type { LucideIcon } from 'lucide-react';
import {
  AppWindow,
  BatteryCharging,
  Box,
  Boxes,
  Briefcase,
  Cable,
  Camera,
  ChevronDown,
  ChevronRight,
  Cloud,
  Cpu,
  Database,
  Globe,
  HardDrive,
  Keyboard,
  Laptop,
  Layers,
  Lock,
  Monitor,
  Network,
  Package,
  Phone,
  PlugZap,
  Printer,
  Radio,
  Router,
  Server,
  Shield,
  Smartphone,
  Tablet,
  Thermometer,
  Tv,
  Wifi,
} from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { cn } from '../ui/utils';

/**
 * TYPE_CATEGORIES are the categories of the Go catalogue citype.Templates()
 * in the order of the tiles, and other for the types without one.
 */
export const TYPE_CATEGORIES = [
  'hardware',
  'end_user',
  'network',
  'power',
  'storage',
  'peripheral',
  'iot',
  'software',
  'infrastructure',
  'service',
  'cloud',
  'generic',
  'other',
] as const;

/** OTHER_CATEGORY holds the types without a category. */
export const OTHER_CATEGORY = 'other';

/**
 * TYPE_ICONS are the lucide-react icons a type may name in ci_type.icon:
 * those of the system types (migration 000197) and a few common ones. The
 * set is fixed so the bundle does not carry every icon.
 */
const TYPE_ICONS: Record<string, LucideIcon> = {
  'app-window': AppWindow,
  'battery-charging': BatteryCharging,
  box: Box,
  boxes: Boxes,
  briefcase: Briefcase,
  cable: Cable,
  camera: Camera,
  cloud: Cloud,
  cpu: Cpu,
  database: Database,
  globe: Globe,
  'hard-drive': HardDrive,
  keyboard: Keyboard,
  laptop: Laptop,
  layers: Layers,
  lock: Lock,
  monitor: Monitor,
  network: Network,
  package: Package,
  phone: Phone,
  'plug-zap': PlugZap,
  printer: Printer,
  radio: Radio,
  router: Router,
  server: Server,
  shield: Shield,
  smartphone: Smartphone,
  tablet: Tablet,
  thermometer: Thermometer,
  tv: Tv,
  wifi: Wifi,
};

/** GENERIC_ICON is the icon of a type without a known icon. */
export const GENERIC_ICON: LucideIcon = Cpu;

/** typeIcon is the icon a type names, the generic device icon otherwise. */
export function typeIcon(name: string | undefined): LucideIcon {
  return (name && TYPE_ICONS[name.trim().toLowerCase()]) || GENERIC_ICON;
}

/** TileType is one offered type: id, shown label and its classification. */
export interface TileType {
  id: string;
  label: string;
  category?: string;
  icon?: string;
  /** parentId folds the type under its parent when that is offered too. */
  parentId?: string;
}

export interface TypeTilesProps {
  types: readonly TileType[];
  selectedId?: string;
  onSelect: (id: string) => void;
  /**
   * categories are the known categories in their order (TYPE_CATEGORIES by
   * default); other categories follow, OTHER_CATEGORY comes last.
   */
  categories?: readonly string[];
  /** categoryPrefix is the i18n prefix of the category labels. */
  categoryPrefix?: string;
}

interface TileNode {
  type: TileType;
  children: TileNode[];
}

interface TileGroup {
  category: string;
  nodes: TileNode[];
}

/**
 * groupTypes folds the subtypes under their parents and groups the roots
 * by category: the known ones in order, then the others alphabetically,
 * other last. A subtype whose parent is not offered is a root.
 */
export function groupTypes(
  types: readonly TileType[],
  categories: readonly string[] = TYPE_CATEGORIES,
  language = 'de',
): TileGroup[] {
  const ids = new Set(types.map((type) => type.id));
  const byLabel = (a: TileNode, b: TileNode) => a.type.label.localeCompare(b.type.label, language);
  const nodes = new Map<string, TileNode>();
  for (const type of types) nodes.set(type.id, { type, children: [] });
  const roots: TileNode[] = [];
  for (const node of nodes.values()) {
    const parent = node.type.parentId;
    if (parent && parent !== node.type.id && ids.has(parent)) {
      nodes.get(parent)!.children.push(node);
    } else {
      roots.push(node);
    }
  }
  for (const node of nodes.values()) node.children.sort(byLabel);

  const groups = new Map<string, TileNode[]>();
  for (const node of roots) {
    const category = node.type.category?.trim() || OTHER_CATEGORY;
    if (!groups.has(category)) groups.set(category, []);
    groups.get(category)!.push(node);
  }
  const rank = (category: string) => {
    if (category === OTHER_CATEGORY) return Number.MAX_SAFE_INTEGER;
    const index = categories.indexOf(category);
    return index >= 0 ? index : categories.length;
  };
  return Array.from(groups.entries())
    .map(([category, list]) => ({ category, nodes: list.sort(byLabel) }))
    .sort((a, b) => rank(a.category) - rank(b.category) || a.category.localeCompare(b.category));
}

function containsSelected(node: TileNode, selectedId: string | undefined): boolean {
  return node.children.some(
    (child) => child.type.id === selectedId || containsSelected(child, selectedId),
  );
}

export function TypeTiles({
  types,
  selectedId,
  onSelect,
  categories = TYPE_CATEGORIES,
  categoryPrefix = 'create.categories',
}: TypeTilesProps) {
  const { t, i18n } = useTranslation();
  const groups = useMemo(
    () => groupTypes(types, categories, i18n.language),
    [types, categories, i18n.language],
  );

  return (
    <div className="space-y-5">
      {groups.map((group) => (
        <TileGroupSection
          key={group.category}
          label={t(`${categoryPrefix}.${group.category}`, group.category)}
          nodes={group.nodes}
          selectedId={selectedId}
          onSelect={onSelect}
        />
      ))}
    </div>
  );
}

function TileGroupSection({
  label,
  nodes,
  selectedId,
  onSelect,
}: {
  label: string;
  nodes: TileNode[];
  selectedId?: string;
  onSelect: (id: string) => void;
}) {
  const headingId = useId();
  return (
    <section aria-labelledby={headingId}>
      <h4 id={headingId} className="mb-2 text-xs font-semibold uppercase text-fg-muted">
        {label}
      </h4>
      <ul className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3">
        {nodes.map((node) => (
          <TileItem key={node.type.id} node={node} selectedId={selectedId} onSelect={onSelect} />
        ))}
      </ul>
    </section>
  );
}

function TileItem({
  node,
  selectedId,
  onSelect,
}: {
  node: TileNode;
  selectedId?: string;
  onSelect: (id: string) => void;
}) {
  const { t } = useTranslation();
  const childrenId = useId();
  const [open, setOpen] = useState(() => containsSelected(node, selectedId));
  const Icon = typeIcon(node.type.icon);
  const selected = node.type.id === selectedId;
  const hasChildren = node.children.length > 0;

  return (
    <li className={cn('min-w-0', hasChildren && open ? 'sm:col-span-2 lg:col-span-3' : '')}>
      <div className="flex items-stretch gap-1">
        <button
          type="button"
          aria-pressed={selected}
          onClick={() => onSelect(node.type.id)}
          className={cn(
            'flex min-h-12 min-w-0 flex-1 items-center gap-3 rounded-lg border px-3 py-2 text-left text-sm transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent',
            selected
              ? 'border-accent bg-accent/10 font-medium text-fg'
              : 'border-line bg-panel text-fg hover:bg-fg/[0.04]',
          )}
        >
          <Icon aria-hidden="true" className="h-5 w-5 shrink-0 text-fg-muted" />
          <span className="truncate">{node.type.label}</span>
        </button>
        {hasChildren ? (
          <button
            type="button"
            aria-expanded={open}
            aria-controls={open ? childrenId : undefined}
            aria-label={t('create.tiles.subtypes', { name: node.type.label })}
            onClick={() => setOpen((current) => !current)}
            className="flex w-9 shrink-0 items-center justify-center rounded-lg border border-line bg-panel text-fg-muted hover:bg-fg/[0.04] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
          >
            {open ? (
              <ChevronDown aria-hidden="true" className="h-4 w-4" />
            ) : (
              <ChevronRight aria-hidden="true" className="h-4 w-4" />
            )}
          </button>
        ) : null}
      </div>
      {hasChildren && open ? (
        <ul
          id={childrenId}
          aria-label={t('create.tiles.subtypesOf', { name: node.type.label })}
          className="mt-2 grid grid-cols-1 gap-2 border-l border-line pl-3 sm:grid-cols-2"
        >
          {node.children.map((child) => (
            <TileItem
              key={child.type.id}
              node={child}
              selectedId={selectedId}
              onSelect={onSelect}
            />
          ))}
        </ul>
      ) : null}
    </li>
  );
}
