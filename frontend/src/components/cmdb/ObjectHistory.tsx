/**
 * ObjectHistory — the history of one object (CI-06, UI-15, WP-806): the
 * changes of a CI or an asset (GET /history/{ci|asset}/{id}) and the
 * movements of an asset or a consumable (GET /stock-movements?asset_id= or
 * ?consumable_id=, WP-274, E-38), newest first in one list. A CI has no
 * movements, a consumable no change history in the backend; for them only
 * the one source is read. The deprecated aliases /assets/{id}/movements and
 * /consumables/{id}/movements are not used.
 */
import {
  ArrowDownToLine,
  ArrowLeftRight,
  ArrowUpFromLine,
  CircleDot,
  Lock,
  LockOpen,
  Pencil,
  Recycle,
  Rocket,
  Undo2,
  UserCheck,
  Wrench,
  type LucideIcon,
} from 'lucide-react';
import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { useEntityHistory, useLocationTree, useMovements } from '../../api/cmdbHooks';
import type { EntityChange, LocationTreeNode, StockMovement } from '../../api/cmdb';
import { fieldValueText } from './fieldValue';
import { Badge } from '../ui/Badge';
import { SkeletonList } from '../ui/Skeleton';

// ─── Times and location names (CI-06, DB-05) ────────────────────────────────

/** formatDateTime shows a timestamp in the UI language; no value is undefined. */
export function formatDateTime(value: string | undefined, language: string): string | undefined {
  if (!value) return undefined;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString(language, { dateStyle: 'medium', timeStyle: 'short' });
}

export interface LocationNames {
  /** The name of a node of the location tree (site, room, …). */
  name: (id?: string) => string | undefined;
  /** The path of a node from its site: Site › Gebäude › Raum › Rack. */
  path: (id?: string) => string | undefined;
}

/**
 * useLocationNames names the nodes of the location tree (GET /locations/tree,
 * the nodes visible to the user). The tree is read only when enabled, i.e.
 * when there is a location to name. A node the tree does not hold (not
 * loaded, outside the user's scope or deleted) keeps its id.
 */
export function useLocationNames(enabled: boolean): LocationNames {
  const tree = useLocationTree({ enabled });
  return useMemo(() => {
    const nodes = new Map<string, LocationTreeNode>();
    const walk = (list: LocationTreeNode[] | undefined) => {
      for (const node of list ?? []) {
        nodes.set(node.id, node);
        walk(node.children);
      }
    };
    walk(tree.data?.data);
    return {
      name: (id) => (id ? (nodes.get(id)?.name ?? id) : undefined),
      path: (id) => {
        if (!id) return undefined;
        const names: string[] = [];
        // A node whose parent is outside the scope is a root of the tree.
        for (
          let node = nodes.get(id);
          node;
          node = node.parent_id ? nodes.get(node.parent_id) : undefined
        ) {
          names.unshift(node.name);
          if (names.length > nodes.size) break;
        }
        return names.length > 0 ? names.join(' › ') : id;
      },
    };
  }, [tree.data]);
}

/** The location fields of a CI or asset whose values are nodes of the location tree. */
export const LOCATION_FIELDS = new Set(['location_id', 'site_id', 'room_id']);

/** historyValue renders an old or new value of a field change; empty is "—". */
function historyValue(value: unknown): string {
  if (value === null || value === undefined || value === '') return '—';
  return typeof value === 'string' ? value : JSON.stringify(value);
}

// ─── Movements (STK-04, WP-274) ─────────────────────────────────────────────

const MOVEMENT_ICONS: Record<string, LucideIcon> = {
  receipt: ArrowDownToLine,
  transfer: ArrowLeftRight,
  assignment: UserCheck,
  return: Undo2,
  deployment: Rocket,
  retrieval: ArrowUpFromLine,
  reservation: Lock,
  release: LockOpen,
  repair: Wrench,
  disposal: Recycle,
  correction: Pencil,
};

/** MovementIcon shows the kind of a movement as a small glyph before its label. */
export function MovementIcon({ type }: { type: string }) {
  const Icon = MOVEMENT_ICONS[type] ?? CircleDot;
  return <Icon aria-hidden="true" className="h-4 w-4 shrink-0 text-fg-subtle" />;
}

/** movementLabel is the catalog label of a movement type (movement.types.*). */
export function movementLabel(t: TFunction, type: string): string {
  return t(`movement.types.${type}`, type);
}

// ─── The history ─────────────────────────────────────────────────────────────

export type HistoryEntityType = 'ci' | 'asset' | 'consumable';

type HistoryItem =
  | { kind: 'change'; at: string; change: EntityChange }
  | { kind: 'movement'; at: string; movement: StockMovement };

/**
 * mergeNewestFirst merges the changes and the movements, each already
 * newest first from the server, into one list newest first. Entries of one
 * source keep their server order.
 */
function mergeNewestFirst(changes: EntityChange[], movements: StockMovement[]): HistoryItem[] {
  const out: HistoryItem[] = [];
  let c = 0;
  let m = 0;
  const time = (value: string) => {
    const ms = new Date(value).getTime();
    return Number.isNaN(ms) ? 0 : ms;
  };
  while (c < changes.length || m < movements.length) {
    const change = changes[c];
    const movement = movements[m];
    if (change && (!movement || time(change.created_at) >= time(movement.occurred_at))) {
      out.push({ kind: 'change', at: change.created_at, change });
      c += 1;
    } else if (movement) {
      out.push({ kind: 'movement', at: movement.occurred_at, movement });
      m += 1;
    }
  }
  return out;
}

export interface ObjectHistoryProps {
  entityType: HistoryEntityType;
  id: string;
  /** label names the list; the history title by default. */
  label?: string;
}

/**
 * ObjectHistory lists the changes and movements of a CI, an asset or a
 * consumable, newest first. A change shows its time, change_type and
 * field_name, its trail as badge, the site at that time and the field lines
 * of a change set (AUD-03) with catalog labels (WP-523) and location names.
 * A movement shows its time, the catalog label of its type, the quantity,
 * the source and target location by name and the reason. Without
 * audit:read the server leaves the audit entries out (E-78); the list then
 * says so.
 */
export function ObjectHistory({ entityType, id, label }: ObjectHistoryProps) {
  const { t, i18n } = useTranslation();
  const withChanges = entityType !== 'consumable';
  const withMovements = entityType !== 'ci';
  const history = useEntityHistory(entityType, withChanges ? id : undefined);
  const movements = useMovements(
    entityType === 'asset' ? { asset_id: id } : { consumable_id: id },
    { enabled: withMovements && !!id },
  );
  const changes = useMemo(
    () => (withChanges ? (history.data?.data ?? []) : []),
    [withChanges, history.data],
  );
  const moves = useMemo(
    () => (withMovements ? (movements.data?.data ?? []) : []),
    [withMovements, movements.data],
  );
  const items = useMemo(() => mergeNewestFirst(changes, moves), [changes, moves]);

  // Sites, location changes and the locations of movements are shown with
  // the names of the location tree (CI-06), a location field as its path.
  const locations = useLocationNames(
    changes.some(
      (entry) =>
        !!entry.site_id ||
        !!entry.field_changes?.some((change) => LOCATION_FIELDS.has(change.field)),
    ) || moves.some((move) => !!move.from_location_id || !!move.to_location_id),
  );
  const changeText =
    (field: string) =>
    (value: unknown): string =>
      LOCATION_FIELDS.has(field) && typeof value === 'string' && value !== ''
        ? ((field === 'location_id' ? locations.path(value) : locations.name(value)) ?? value)
        : historyValue(value);

  const loading = (withChanges && history.isLoading) || (withMovements && movements.isLoading);
  const changesFailed = withChanges && !!history.error;
  const movementsFailed = withMovements && !!movements.error;
  const loadError = entityType === 'ci' ? t('ci.historyLoadError') : t('objectHistory.loadError');

  if (loading) return <SkeletonList rows={3} label={t('app.loading')} />;
  if (changesFailed && (!withMovements || movementsFailed)) {
    return <p className="text-sm text-danger">{loadError}</p>;
  }
  if (movementsFailed && !withChanges) {
    return <p className="text-sm text-danger">{t('objectHistory.movementsLoadError')}</p>;
  }

  return (
    <div className="space-y-2">
      {changesFailed ? <p className="text-sm text-danger">{loadError}</p> : null}
      {movementsFailed ? (
        <p className="text-sm text-danger">{t('objectHistory.movementsLoadError')}</p>
      ) : null}
      {items.length === 0 ? (
        <p className="text-sm text-fg-muted">
          {entityType === 'ci' ? t('ci.historyEmpty') : t('objectHistory.empty')}
        </p>
      ) : (
        <ol className="divide-y divide-line text-sm" aria-label={label ?? t('objectHistory.title')}>
          {items.map((item) =>
            item.kind === 'change' ? (
              <li
                key={`${item.change.source ?? 'change'}-${item.change.id}`}
                className="flex flex-wrap items-baseline gap-x-3 gap-y-1 py-2.5 first:pt-0 last:pb-0"
              >
                <time dateTime={item.change.created_at} className="text-fg-muted">
                  {formatDateTime(item.change.created_at, i18n.language)}
                </time>
                <span className="font-medium text-fg">
                  {item.change.change_type}
                  {item.change.field_name ? ` · ${item.change.field_name}` : ''}
                </span>
                {item.change.source ? (
                  <Badge variant="neutral">{t(`ci.historySource.${item.change.source}`)}</Badge>
                ) : null}
                {item.change.site_id ? (
                  <span className="text-fg-muted">
                    {t('ci.historySite', { site: locations.name(item.change.site_id) })}
                  </span>
                ) : null}
                {item.change.change_set_id && item.change.field_changes?.length ? (
                  <ul className="basis-full space-y-0.5 pl-4 text-fg">
                    {item.change.field_changes.map((change) => (
                      <li key={change.field} className="break-all">
                        {`${change.field}: ${fieldValueText(t, change.field, change.old, changeText(change.field))} → ${fieldValueText(t, change.field, change.new, changeText(change.field))}`}
                      </li>
                    ))}
                  </ul>
                ) : null}
              </li>
            ) : (
              <li
                key={`movement-${item.movement.id}`}
                className="flex flex-wrap items-baseline gap-x-3 gap-y-1 py-2.5 first:pt-0 last:pb-0"
              >
                <time dateTime={item.movement.occurred_at} className="text-fg-muted">
                  {formatDateTime(item.movement.occurred_at, i18n.language)}
                </time>
                <span className="inline-flex items-center gap-2 self-center font-medium text-fg">
                  <MovementIcon type={item.movement.movement_type} />
                  {movementLabel(t, item.movement.movement_type)}
                </span>
                {item.movement.quantity ? (
                  <span className="tabular-nums text-fg">
                    {t('objectHistory.quantity', { quantity: item.movement.quantity })}
                  </span>
                ) : null}
                {item.movement.from_location_id ? (
                  <span className="text-fg-muted">
                    {t('objectHistory.from', {
                      location: locations.name(item.movement.from_location_id),
                    })}
                  </span>
                ) : null}
                {item.movement.to_location_id ? (
                  <span className="text-fg-muted">
                    {t('objectHistory.to', {
                      location: locations.name(item.movement.to_location_id),
                    })}
                  </span>
                ) : null}
                {item.movement.reason ? (
                  <span className="basis-full text-fg-muted">
                    {t('objectHistory.reason', { reason: item.movement.reason })}
                  </span>
                ) : null}
              </li>
            ),
          )}
        </ol>
      )}
      {withChanges && history.data?.audit_included === false ? (
        <p className="text-xs text-fg-muted">{t('ci.historyAuditHint')}</p>
      ) : null}
    </div>
  );
}
