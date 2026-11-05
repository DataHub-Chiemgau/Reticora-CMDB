import {
  ArrowDownToLine,
  ArrowLeftRight,
  ArrowUpFromLine,
  Boxes,
  CircleDot,
  Lock,
  LockOpen,
  Pencil,
  Plus,
  Recycle,
  Rocket,
  Undo2,
  UserCheck,
  Warehouse,
  Wrench,
  type LucideIcon,
} from 'lucide-react';
import { useRef, useState, type KeyboardEvent } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import {
  useMovements,
  useQuantityItems,
  useReservations,
  useAvailability,
  useReleaseReservation,
} from '../api/cmdbHooks';
import { inventoryApi } from '../api/cmdb';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { Input } from '../components/ui/Input';
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
import { TableEmptyRow } from './ListParts';

/** The eleven movement types of the ledger (STK-04, WP-274). */
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

/**
 * The types POST /stock-movements books per object kind (E-38, WP-274):
 * physical movements of an asset without quantity, quantity bookings of a
 * quantity item with quantity. Assignment, return, reservation, release
 * and disposal arise only in their domain paths.
 */
export const BOOKABLE_TYPES = {
  asset: ['receipt', 'transfer', 'deployment', 'retrieval', 'repair', 'correction'],
  quantity_item: ['receipt', 'retrieval', 'correction', 'transfer'],
} as const;

type ObjectKind = keyof typeof BOOKABLE_TYPES;

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

type InventoryTab = 'movements' | 'items' | 'reservations' | 'availability';

export function InventoryPage() {
  const { t } = useTranslation();
  const [tab, setTab] = useState<InventoryTab>('movements');
  const tabRefs = useRef<Record<string, HTMLButtonElement | null>>({});
  const [typeFilter, setTypeFilter] = useState('');
  const [showMove, setShowMove] = useState(false);
  const [moveDraft, setMoveDraft] = useState<{
    kind: ObjectKind;
    object_id: string;
    movement_type: string;
    to_location_id: string;
    quantity: number;
    reason: string;
  }>({
    kind: 'asset',
    object_id: '',
    movement_type: 'receipt',
    to_location_id: '',
    quantity: 1,
    reason: '',
  });
  const bookable: readonly string[] = BOOKABLE_TYPES[moveDraft.kind];

  const queryClient = useQueryClient();
  const movements = useMovements({ movement_type: typeFilter });
  const items = useQuantityItems();
  const reservations = useReservations('active');
  const availability = useAvailability();
  const releaseReservation = useReleaseReservation();
  const record = useMutation({
    mutationFn: () =>
      inventoryApi.record({
        ...(moveDraft.kind === 'asset'
          ? { asset_id: moveDraft.object_id }
          : { quantity_item_id: moveDraft.object_id, quantity: moveDraft.quantity }),
        movement_type: moveDraft.movement_type,
        to_location_id: moveDraft.to_location_id || undefined,
        reason: moveDraft.reason || undefined,
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['stock-movements'] });
      queryClient.invalidateQueries({ queryKey: ['inventory-items'] });
      setShowMove(false);
    },
  });

  const tabs = [
    { id: 'movements', label: t('inventory.movements', 'Bewegungen') },
    { id: 'items', label: t('inventory.items', 'Mengenartikel') },
    { id: 'reservations', label: t('inventory.reservations', 'Reservierungen') },
    { id: 'availability', label: t('inventory.availability', 'Verfügbarkeit') },
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

  return (
    <div className="space-y-4">
      <PageHeader
        title={t('inventory.title', 'Inventar')}
        description={t(
          'inventory.subtitle',
          'Bewegungsjournal, Mengenartikel, Reservierungen und Verfügbarkeit.',
        )}
        actions={
          <Button icon={<Plus />} onClick={() => setShowMove(true)}>
            {t('inventory.record', 'Bewegung erfassen')}
          </Button>
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
            t={t}
          />
        ) : null}
        {tab === 'items' ? <ItemsTab items={items} t={t} /> : null}
        {tab === 'reservations' ? (
          <ReservationsTab
            reservations={reservations}
            onRelease={(id: string) => releaseReservation.mutate(id)}
            t={t}
          />
        ) : null}
        {tab === 'availability' ? <AvailabilityTab availability={availability} t={t} /> : null}
      </div>

      <Modal
        open={showMove}
        onOpenChange={setShowMove}
        title={t('inventory.record', 'Bewegung erfassen')}
      >
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            record.mutate();
          }}
        >
          <Select
            label={t('inventory.objectKind', 'Objektart')}
            options={[
              { value: 'asset', label: t('inventory.kindAsset', 'Asset (serialisiert)') },
              { value: 'quantity_item', label: t('inventory.items', 'Mengenartikel') },
            ]}
            value={moveDraft.kind}
            onChange={(e) => {
              const kind = e.target.value as ObjectKind;
              const allowed: readonly string[] = BOOKABLE_TYPES[kind];
              setMoveDraft({
                ...moveDraft,
                kind,
                movement_type: allowed.includes(moveDraft.movement_type)
                  ? moveDraft.movement_type
                  : (allowed[0] ?? 'receipt'),
              });
            }}
          />
          <Select
            label={t('inventory.movementType', 'Bewegungsart')}
            options={bookable.map((mt) => ({
              value: mt,
              label: t(`inventory.movementTypes.${mt}`, mt),
            }))}
            value={moveDraft.movement_type}
            onChange={(e) => setMoveDraft({ ...moveDraft, movement_type: e.target.value })}
          />
          <Input
            label={
              moveDraft.kind === 'asset'
                ? t('inventory.assetId', 'Asset-ID (serialisiert)')
                : t('inventory.itemId', 'Mengenartikel-ID')
            }
            value={moveDraft.object_id}
            onChange={(e) => setMoveDraft({ ...moveDraft, object_id: e.target.value })}
          />
          {moveDraft.kind === 'quantity_item' ? (
            <Input
              label={t('inventory.quantity', 'Menge')}
              type="number"
              value={moveDraft.quantity}
              onChange={(e) => setMoveDraft({ ...moveDraft, quantity: Number(e.target.value) })}
            />
          ) : null}
          <Input
            label={t('inventory.toLocation', 'Ziellocation-ID')}
            value={moveDraft.to_location_id}
            onChange={(e) => setMoveDraft({ ...moveDraft, to_location_id: e.target.value })}
          />
          <Input
            label={t('inventory.reason', 'Grund')}
            value={moveDraft.reason}
            onChange={(e) => setMoveDraft({ ...moveDraft, reason: e.target.value })}
          />
          <div className="flex justify-end gap-2">
            <Button type="button" variant="secondary" onClick={() => setShowMove(false)}>
              {t('common.cancel', 'Abbrechen')}
            </Button>
            <Button type="submit" disabled={record.isPending}>
              {t('inventory.record', 'Bewegung erfassen')}
            </Button>
          </div>
        </form>
      </Modal>
    </div>
  );
}

/** MovementIcon shows the kind of a movement as a small glyph before its label. */
function MovementIcon({ type }: { type: string }) {
  const Icon = MOVEMENT_ICONS[type] ?? CircleDot;
  return <Icon aria-hidden="true" className="h-4 w-4 shrink-0 text-fg-subtle" />;
}

function MovementsTab({
  movements,
  typeFilter,
  setTypeFilter,
  t,
}: {
  movements: ReturnType<typeof useMovements>;
  typeFilter: string;
  setTypeFilter: (v: string) => void;
  t: TFunction;
}) {
  const rows = movements.data?.data ?? [];
  return (
    <div className="space-y-4">
      <Toolbar>
        <Select
          aria-label="type"
          options={[
            { value: '', label: t('inventory.allTypes', 'Alle Arten') },
            ...MOVEMENT_TYPES.map((mt) => ({
              value: mt,
              label: t(`inventory.movementTypes.${mt}`, mt),
            })),
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
          <ScrollRegion label={t('inventory.movements', 'Bewegungen')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('inventory.movementType', 'Bewegungsart')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('inventory.item', 'Artikel')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('inventory.quantity', 'Menge')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('inventory.reason', 'Grund')}
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
                        {t(`inventory.movementTypes.${m.movement_type}`, m.movement_type)}
                      </span>
                    </td>
                    <td className={tdMonoClasses}>
                      <span className="inline-flex flex-wrap items-center gap-2">
                        <span>{m.asset_id ?? m.quantity_item_id ?? m.consumable_id}</span>
                        {m.consumable_id ? <Badge variant="neutral">{m.item_kind}</Badge> : null}
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
          <ScrollRegion label={t('inventory.items', 'Mengenartikel')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('common.name', 'Name')}
                  </th>
                  <th scope="col" className={thClasses}>
                    SKU
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('consumables.stock')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('consumables.minLevel', 'Mindestbestand')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ci.status', 'Status')}
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
                        <Badge variant="danger">{t('consumables.low', 'niedrig')}</Badge>
                      ) : (
                        <Badge variant="success">{t('consumables.ok', 'OK')}</Badge>
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
  onRelease,
  t,
}: {
  reservations: ReturnType<typeof useReservations>;
  onRelease: (id: string) => void;
  t: TFunction;
}) {
  const rows = reservations.data?.data ?? [];
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
          <ScrollRegion label={t('inventory.reservations', 'Reservierungen')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('inventory.item', 'Artikel')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('inventory.quantity', 'Menge')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('inventory.project')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('inventory.expiresAt')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('common.actions', 'Aktionen')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => (
                  <tr key={r.id} className={trClasses}>
                    <td className={tdMonoClasses}>
                      <span className="font-medium">{r.asset_id ?? r.consumable_id}</span>
                      {r.item_kind === 'consumable' && r.location_id ? (
                        <span className="block text-fg-muted">
                          {t('locations.kinds.bin', 'Fach')} {r.location_id.slice(0, 8)}…
                        </span>
                      ) : null}
                    </td>
                    <td className={tdNumericClasses}>{r.quantity}</td>
                    <td className={tdClasses}>
                      {r.project_ref ? <Badge variant="info">{r.project_ref}</Badge> : '—'}
                    </td>
                    <td className={cn(tdClasses, 'whitespace-nowrap tabular-nums')}>
                      {r.expires_at ? new Date(r.expires_at).toLocaleDateString() : '—'}
                    </td>
                    <td className={cn(tdClasses, 'text-right')}>
                      <Button size="sm" variant="secondary" onClick={() => onRelease(r.id)}>
                        {t('inventory.release', 'Freigeben')}
                      </Button>
                    </td>
                  </tr>
                ))}
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
          <ScrollRegion label={t('inventory.availability', 'Verfügbarkeit')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('inventory.item', 'Artikel')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('inventory.total', 'Gesamt')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('inventory.available', 'Verfügbar')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('inventory.reserved', 'Reserviert')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('inventory.assigned', 'Zugewiesen')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('inventory.repair', 'Reparatur')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map((a) => (
                  <tr key={`${a.item_kind}-${a.item_id}`} className={trClasses}>
                    <td className={tdMonoClasses}>
                      <span className="inline-flex flex-wrap items-center gap-2">
                        <span>{a.item_id.slice(0, 8)}…</span>
                        <Badge variant="neutral">{a.item_kind}</Badge>
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
