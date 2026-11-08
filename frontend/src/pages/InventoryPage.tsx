import { ArrowDownToLine, ArrowLeftRight, Boxes, Lock, MapPin, Warehouse } from 'lucide-react';
import { useRef, useState, type KeyboardEvent } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import {
  useMovements,
  useQuantityItems,
  useReservations,
  useAvailability,
  useReleaseReservation,
  useConvertReservation,
} from '../api/cmdbHooks';
import type { Reservation } from '../api/cmdb';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { Select } from '../components/ui/Select';
import { Modal } from '../components/ui/Modal';
import { SkeletonList } from '../components/ui/Skeleton';
import { ErrorState } from '../components/ui/ErrorState';
import { PageHeader } from '../components/ui/PageHeader';
import { Toolbar } from '../components/ui/Toolbar';
import {
  tableClasses,
  tdClasses,
  tdMonoClasses,
  tdNumericClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';
import { cn } from '../components/ui/utils';
import { ScrollRegion } from '../components/ScrollRegion';
import {
  MovementIcon,
  ObjectHistory,
  movementLabel,
  type HistoryEntityType,
} from '../components/cmdb/ObjectHistory';
import {
  ConvertReservationDialog,
  MoveAssetDialog,
  MoveStockDialog,
  ReceiveDialog,
  ReserveDialog,
  stockFlowErrorMessage,
  useStockFlowGates,
} from '../components/flows/StockFlowDialogs';
import type { PickedHolder } from '../components/pickers/UserTeamPicker';
import { TableEmptyRow } from './ListParts';

/** The eleven movement types of the ledger (STK-04, WP-274), labelled by movement.types.*. */
export const MOVEMENT_TYPES = [
  'receipt',
  'transfer',
  'assignment',
  'return',
  'deployment',
  'retrieval',
  'reservation',
  'release',
  'repair',
  'disposal',
  'correction',
] as const;

type InventoryTab = 'movements' | 'items' | 'reservations' | 'availability';

/** The flows of the page (UI-15, WP-806); each is a dialog of StockFlowDialogs. */
type StockFlow = 'receive' | 'move' | 'moveAsset' | 'reserve';

/** HistoryTarget is the object whose history the page shows in a dialog. */
interface HistoryTarget {
  entityType: HistoryEntityType;
  id: string;
}

/** objectTarget is the object of a movement or a reservation: an asset or a consumable. */
function objectTarget(row: {
  asset_id?: string;
  consumable_id?: string;
  quantity_item_id?: string;
}): HistoryTarget | undefined {
  if (row.asset_id) return { entityType: 'asset', id: row.asset_id };
  const consumable = row.consumable_id ?? row.quantity_item_id;
  return consumable ? { entityType: 'consumable', id: consumable } : undefined;
}

/**
 * ObjectLink opens the history of an object (ObjectHistory, WP-806). It
 * shows the id: the lists of movements and reservations carry no names.
 */
function ObjectLink({
  target,
  onOpen,
  t,
}: {
  target: HistoryTarget | undefined;
  onOpen: (target: HistoryTarget) => void;
  t: TFunction;
}) {
  if (!target) return <span>—</span>;
  return (
    <button
      type="button"
      onClick={() => onOpen(target)}
      aria-label={t('inventory.openHistory', { object: target.id })}
      className="rounded text-left font-medium text-accent-strong underline-offset-2 hover:underline focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent"
    >
      {target.id}
    </button>
  );
}

export function InventoryPage() {
  const { t } = useTranslation();
  const [tab, setTab] = useState<InventoryTab>('movements');
  const tabRefs = useRef<Record<string, HTMLButtonElement | null>>({});
  const [typeFilter, setTypeFilter] = useState('');
  const [flow, setFlow] = useState<StockFlow | null>(null);
  const [historyOf, setHistoryOf] = useState<HistoryTarget | null>(null);
  const [converting, setConverting] = useState<Reservation | null>(null);
  const [rowErrors, setRowErrors] = useState<Record<string, string>>({});
  const gates = useStockFlowGates();

  const movements = useMovements({ movement_type: typeFilter });
  const items = useQuantityItems();
  const reservations = useReservations('active');
  const availability = useAvailability();
  const releaseReservation = useReleaseReservation();
  const convertReservation = useConvertReservation();

  // Release and convert answer at the row: a 409 of a reservation that is
  // no longer active, a holder of the asset, a missing right or feature.
  const setRowError = (id: string, message?: string) =>
    setRowErrors((current) => {
      const next = { ...current };
      if (message) next[id] = message;
      else delete next[id];
      return next;
    });
  const rowCallbacks = (id: string) => ({
    onSuccess: () => setRowError(id),
    onError: (error: unknown) =>
      setRowError(id, stockFlowErrorMessage(error, t, t('stockFlow.errors.notActive'))),
  });
  const release = (r: Reservation) => {
    setRowError(r.id);
    releaseReservation.mutate(r.id, rowCallbacks(r.id));
  };
  const convert = (r: Reservation, holder?: PickedHolder) => {
    setRowError(r.id);
    let data = {};
    if (holder) data = holder.kind === 'team' ? { team_id: holder.id } : { assignee_id: holder.id };
    convertReservation.mutate({ id: r.id, data }, rowCallbacks(r.id));
  };

  const tabs = [
    { id: 'movements', label: t('inventory.movements') },
    { id: 'items', label: t('inventory.items') },
    { id: 'reservations', label: t('inventory.reservations') },
    { id: 'availability', label: t('inventory.availability') },
  ] as const;

  /** Arrow keys, Home and End move between the tabs (WAI-ARIA tabs pattern). */
  function onTabKeyDown(event: KeyboardEvent<HTMLButtonElement>, index: number) {
    const last = tabs.length - 1;
    const next =
      event.key === 'ArrowRight'
        ? (index + 1) % tabs.length
        : event.key === 'ArrowLeft'
          ? (index + last) % tabs.length
          : event.key === 'Home'
            ? 0
            : event.key === 'End'
              ? last
              : null;
    if (next === null) {
      return;
    }
    event.preventDefault();
    const target = tabs[next]!;
    setTab(target.id);
    tabRefs.current[target.id]?.focus();
  }

  const dialog = (name: StockFlow) => ({
    open: flow === name,
    onOpenChange: (open: boolean) => setFlow(open ? name : null),
  });

  return (
    <div className="space-y-4">
      <PageHeader
        title={t('inventory.title')}
        description={t('inventory.subtitle')}
        actions={
          <div className="flex flex-wrap gap-2">
            {gates.receive ? (
              <Button icon={<ArrowDownToLine />} onClick={() => setFlow('receive')}>
                {t('stockFlow.receive')}
              </Button>
            ) : null}
            {gates.move ? (
              <Button variant="secondary" icon={<ArrowLeftRight />} onClick={() => setFlow('move')}>
                {t('stockFlow.move')}
              </Button>
            ) : null}
            {gates.moveAsset ? (
              <Button variant="secondary" icon={<MapPin />} onClick={() => setFlow('moveAsset')}>
                {t('stockFlow.moveAsset')}
              </Button>
            ) : null}
            {gates.reserve ? (
              <Button variant="secondary" icon={<Lock />} onClick={() => setFlow('reserve')}>
                {t('stockFlow.reserve')}
              </Button>
            ) : null}
          </div>
        }
      />

      <div role="tablist" className="flex gap-1 overflow-x-auto border-b border-line">
        {tabs.map((tb, index) => {
          const selected = tab === tb.id;
          return (
            <button
              key={tb.id}
              ref={(node) => {
                tabRefs.current[tb.id] = node;
              }}
              type="button"
              role="tab"
              id={`inventory-tab-${tb.id}`}
              aria-selected={selected}
              aria-controls="inventory-panel"
              tabIndex={selected ? 0 : -1}
              onClick={() => setTab(tb.id)}
              onKeyDown={(event) => onTabKeyDown(event, index)}
              className={cn(
                '-mb-px whitespace-nowrap border-b-2 px-3 py-2 text-sm font-medium transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent',
                selected
                  ? 'border-accent text-fg'
                  : 'border-transparent text-fg-muted hover:text-fg',
              )}
            >
              {tb.label}
            </button>
          );
        })}
      </div>

      <div role="tabpanel" id="inventory-panel" aria-labelledby={`inventory-tab-${tab}`}>
        {tab === 'movements' ? (
          <MovementsTab
            movements={movements}
            typeFilter={typeFilter}
            setTypeFilter={setTypeFilter}
            onOpenHistory={setHistoryOf}
            t={t}
          />
        ) : null}
        {tab === 'items' ? <ItemsTab items={items} t={t} /> : null}
        {tab === 'reservations' ? (
          <ReservationsTab
            reservations={reservations}
            errors={rowErrors}
            onRelease={release}
            onConvert={(r) => (r.project_ref && !r.assignee_id ? setConverting(r) : convert(r))}
            onOpenHistory={setHistoryOf}
            t={t}
          />
        ) : null}
        {tab === 'availability' ? <AvailabilityTab availability={availability} t={t} /> : null}
      </div>

      {/* The dialogs stay mounted: a request waiting in its undo window is
          sent when the page is left (useUndoableSubmit). */}
      <ReceiveDialog {...dialog('receive')} />
      <MoveStockDialog {...dialog('move')} />
      <MoveAssetDialog {...dialog('moveAsset')} />
      <ReserveDialog {...dialog('reserve')} />
      <ConvertReservationDialog
        open={!!converting}
        onOpenChange={(open) => {
          if (!open) setConverting(null);
        }}
        project={converting?.project_ref}
        onConvert={(holder) => {
          if (converting) convert(converting, holder);
        }}
      />
      <Modal
        open={!!historyOf}
        onOpenChange={(open) => {
          if (!open) setHistoryOf(null);
        }}
        title={t('inventory.historyOf', { object: historyOf?.id ?? '' })}
        size="lg"
      >
        {historyOf ? <ObjectHistory entityType={historyOf.entityType} id={historyOf.id} /> : null}
      </Modal>
    </div>
  );
}

function MovementsTab({
  movements,
  typeFilter,
  setTypeFilter,
  onOpenHistory,
  t,
}: {
  movements: ReturnType<typeof useMovements>;
  typeFilter: string;
  setTypeFilter: (v: string) => void;
  onOpenHistory: (target: HistoryTarget) => void;
  t: TFunction;
}) {
  const rows = movements.data?.data ?? [];
  return (
    <div className="space-y-4">
      <Toolbar>
        <Select
          aria-label="type"
          options={[
            { value: '', label: t('inventory.allTypes') },
            ...MOVEMENT_TYPES.map((mt) => ({ value: mt, label: movementLabel(t, mt) })),
          ]}
          value={typeFilter}
          onChange={(e) => setTypeFilter(e.target.value)}
          className="w-48"
        />
      </Toolbar>
      {movements.isLoading ? <SkeletonList rows={5} label={t('app.loading')} /> : null}
      {movements.error ? (
        <ErrorState
          title={t('app.error')}
          description={movements.error instanceof Error ? movements.error.message : undefined}
          retryLabel={t('common.retry')}
          onRetry={() => void movements.refetch()}
        />
      ) : null}
      {movements.data ? (
        <Card className="p-0 overflow-hidden">
          <ScrollRegion label={t('inventory.movements')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('inventory.movementType')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('inventory.item')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('inventory.quantity')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('inventory.reason')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('inventory.occurredAt')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map((m) => (
                  <tr key={m.id} className={trClasses}>
                    <td className={tdPrimaryClasses}>
                      <span className="inline-flex items-center gap-2">
                        <MovementIcon type={m.movement_type} />
                        {movementLabel(t, m.movement_type)}
                      </span>
                    </td>
                    <td className={tdMonoClasses}>
                      <span className="inline-flex flex-wrap items-center gap-2">
                        <ObjectLink target={objectTarget(m)} onOpen={onOpenHistory} t={t} />
                        {m.consumable_id ? (
                          <Badge variant="neutral">{t('pickers.kinds.consumable')}</Badge>
                        ) : null}
                      </span>
                    </td>
                    <td className={tdNumericClasses}>{m.quantity ? m.quantity : '—'}</td>
                    <td className={tdClasses}>{m.reason || '—'}</td>
                    <td className={cn(tdClasses, 'whitespace-nowrap text-right tabular-nums')}>
                      <time dateTime={m.occurred_at}>
                        {new Date(m.occurred_at).toLocaleString()}
                      </time>
                    </td>
                  </tr>
                ))}
                {rows.length === 0 ? (
                  <TableEmptyRow
                    colSpan={5}
                    icon={<ArrowLeftRight />}
                    title={typeFilter ? t('common.noResults') : t('inventory.emptyMovementsTitle')}
                    description={
                      typeFilter ? t('common.noResultsHint') : t('inventory.emptyMovementsHint')
                    }
                  />
                ) : null}
              </tbody>
            </table>
          </ScrollRegion>
        </Card>
      ) : null}
    </div>
  );
}

function ItemsTab({ items, t }: { items: ReturnType<typeof useQuantityItems>; t: TFunction }) {
  const rows = items.data?.data ?? [];
  return (
    <div className="space-y-4">
      {items.isLoading ? <SkeletonList rows={5} label={t('app.loading')} /> : null}
      {items.error ? (
        <ErrorState
          title={t('app.error')}
          description={items.error instanceof Error ? items.error.message : undefined}
          retryLabel={t('common.retry')}
          onRetry={() => void items.refetch()}
        />
      ) : null}
      {items.data ? (
        <Card className="p-0 overflow-hidden">
          <ScrollRegion label={t('inventory.items')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('common.name')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('inventory.sku')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('consumables.stock')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('consumables.minLevel')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ci.status')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map((it) => (
                  <tr key={it.id} className={trClasses}>
                    <td className={tdPrimaryClasses}>{it.name}</td>
                    <td className={tdMonoClasses}>{it.sku || '—'}</td>
                    <td className={cn(tdNumericClasses, 'whitespace-nowrap text-fg')}>
                      {it.stock_level} {it.unit}
                    </td>
                    <td className={tdNumericClasses}>{it.min_level}</td>
                    <td className={tdClasses}>
                      {it.stock_level <= it.min_level ? (
                        <Badge variant="danger">{t('consumables.low')}</Badge>
                      ) : (
                        <Badge variant="success">{t('consumables.ok')}</Badge>
                      )}
                    </td>
                  </tr>
                ))}
                {rows.length === 0 ? (
                  <TableEmptyRow
                    colSpan={5}
                    icon={<Boxes />}
                    title={t('inventory.emptyItemsTitle')}
                    description={t('inventory.emptyItemsHint')}
                  />
                ) : null}
              </tbody>
            </table>
          </ScrollRegion>
        </Card>
      ) : null}
    </div>
  );
}

function ReservationsTab({
  reservations,
  errors,
  onRelease,
  onConvert,
  onOpenHistory,
  t,
}: {
  reservations: ReturnType<typeof useReservations>;
  errors: Record<string, string>;
  onRelease: (reservation: Reservation) => void;
  onConvert: (reservation: Reservation) => void;
  onOpenHistory: (target: HistoryTarget) => void;
  t: TFunction;
}) {
  const rows = reservations.data?.data ?? [];
  const date = (value: string) => new Date(value).toLocaleDateString();
  return (
    <div className="space-y-4">
      {reservations.isLoading ? <SkeletonList rows={5} label={t('app.loading')} /> : null}
      {reservations.error ? (
        <ErrorState
          title={t('app.error')}
          description={reservations.error instanceof Error ? reservations.error.message : undefined}
          retryLabel={t('common.retry')}
          onRetry={() => void reservations.refetch()}
        />
      ) : null}
      {reservations.data ? (
        <Card className="p-0 overflow-hidden">
          <ScrollRegion label={t('inventory.reservations')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('inventory.item')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('inventory.quantity')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('inventory.project')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('inventory.expiresAt')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('common.actions')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => {
                  const target = objectTarget(r);
                  const name = target?.id ?? r.id;
                  return (
                    <tr key={r.id} className={trClasses}>
                      <td className={tdMonoClasses}>
                        <ObjectLink target={target} onOpen={onOpenHistory} t={t} />
                        {r.item_kind === 'consumable' && r.location_id ? (
                          <span className="block text-fg-muted">
                            {t('locations.kinds.bin')} {r.location_id.slice(0, 8)}…
                          </span>
                        ) : null}
                        {r.reserved_until ? (
                          <span className="block text-fg-muted">
                            {t('inventory.reservedUntil', { date: date(r.reserved_until) })}
                          </span>
                        ) : null}
                      </td>
                      <td className={tdNumericClasses}>{r.quantity}</td>
                      <td className={tdClasses}>
                        {r.project_ref ? <Badge variant="info">{r.project_ref}</Badge> : '—'}
                      </td>
                      <td className={cn(tdClasses, 'whitespace-nowrap tabular-nums')}>
                        {r.expires_at ? date(r.expires_at) : '—'}
                      </td>
                      <td className={cn(tdClasses, 'text-right')}>
                        <div className="flex flex-wrap justify-end gap-2">
                          {r.item_kind === 'asset' ? (
                            <Button
                              size="sm"
                              variant="secondary"
                              aria-label={t('stockFlow.convertOf', { item: name })}
                              onClick={() => onConvert(r)}
                            >
                              {t('stockFlow.convert')}
                            </Button>
                          ) : null}
                          <Button
                            size="sm"
                            variant="secondary"
                            aria-label={t('stockFlow.releaseOf', { item: name })}
                            onClick={() => onRelease(r)}
                          >
                            {t('inventory.release')}
                          </Button>
                        </div>
                        {errors[r.id] ? (
                          <p role="alert" className="mt-1 text-sm text-danger">
                            {errors[r.id]}
                          </p>
                        ) : null}
                      </td>
                    </tr>
                  );
                })}
                {rows.length === 0 ? (
                  <TableEmptyRow
                    colSpan={5}
                    icon={<Lock />}
                    title={t('inventory.emptyReservationsTitle')}
                    description={t('inventory.emptyReservationsHint')}
                  />
                ) : null}
              </tbody>
            </table>
          </ScrollRegion>
        </Card>
      ) : null}
    </div>
  );
}

function AvailabilityTab({
  availability,
  t,
}: {
  availability: ReturnType<typeof useAvailability>;
  t: TFunction;
}) {
  const rows = availability.data?.data ?? [];
  return (
    <div className="space-y-4">
      {availability.isLoading ? <SkeletonList rows={5} label={t('app.loading')} /> : null}
      {availability.error ? (
        <ErrorState
          title={t('app.error')}
          description={availability.error instanceof Error ? availability.error.message : undefined}
          retryLabel={t('common.retry')}
          onRetry={() => void availability.refetch()}
        />
      ) : null}
      {availability.data ? (
        <Card className="p-0 overflow-hidden">
          <ScrollRegion label={t('inventory.availability')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('inventory.item')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('inventory.total')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('inventory.available')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('inventory.reserved')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('inventory.assigned')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('inventory.repair')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map((a) => (
                  <tr key={`${a.item_kind}-${a.item_id}`} className={trClasses}>
                    <td className={tdMonoClasses}>
                      <span className="inline-flex flex-wrap items-center gap-2">
                        <span>{a.item_id.slice(0, 8)}…</span>
                        <Badge variant="neutral">{t(`pickers.kinds.${a.item_kind}`)}</Badge>
                      </span>
                    </td>
                    <td className={tdNumericClasses}>{a.total}</td>
                    <td className={cn(tdNumericClasses, 'font-medium text-fg')}>{a.available}</td>
                    <td className={tdNumericClasses}>{a.reserved}</td>
                    <td className={tdNumericClasses}>{a.assigned}</td>
                    <td className={tdNumericClasses}>{a.repair}</td>
                  </tr>
                ))}
                {rows.length === 0 ? (
                  <TableEmptyRow
                    colSpan={6}
                    icon={<Warehouse />}
                    title={t('inventory.emptyAvailabilityTitle')}
                    description={t('inventory.emptyAvailabilityHint')}
                  />
                ) : null}
              </tbody>
            </table>
          </ScrollRegion>
        </Card>
      ) : null}
    </div>
  );
}
