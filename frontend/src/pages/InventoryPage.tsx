import { useState } from 'react';
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

const MOVEMENT_TYPES = [
  'receipt',
  'warehouse_transfer',
  'bin_transfer',
  'assignment',
  'return',
  'deployment',
  'retrieval',
  'reservation',
  'reservation_release',
  'repair_transfer',
  'disposal',
  'correction',
];

const MOVEMENT_ICONS: Record<string, string> = {
  receipt: '📥',
  warehouse_transfer: '🔄',
  bin_transfer: '↔️',
  assignment: '👤',
  return: '↩️',
  deployment: '🚀',
  retrieval: '📤',
  reservation: '🔒',
  reservation_release: '🔓',
  repair_transfer: '🔧',
  disposal: '🗑️',
  correction: '✏️',
};

export function InventoryPage() {
  const { t } = useTranslation();
  const [tab, setTab] = useState<'movements' | 'items' | 'reservations' | 'availability'>(
    'movements',
  );
  const [typeFilter, setTypeFilter] = useState('');
  const [showMove, setShowMove] = useState(false);
  const [moveDraft, setMoveDraft] = useState({
    asset_id: '',
    quantity_item_id: '',
    movement_type: 'receipt',
    to_location_id: '',
    quantity: 1,
    reason: '',
  });

  const queryClient = useQueryClient();
  const movements = useMovements({ movement_type: typeFilter });
  const items = useQuantityItems();
  const reservations = useReservations('active');
  const availability = useAvailability();
  const releaseReservation = useReleaseReservation();
  const record = useMutation({
    mutationFn: () =>
      inventoryApi.record({
        asset_id: moveDraft.asset_id || undefined,
        quantity_item_id: moveDraft.quantity_item_id || undefined,
        movement_type: moveDraft.movement_type,
        to_location_id: moveDraft.to_location_id || undefined,
        quantity: moveDraft.quantity || undefined,
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

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">
            {t('inventory.title', 'Inventar')}
          </h2>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">
            {t(
              'inventory.subtitle',
              'Bewegungsjournal, Mengenartikel, Reservierungen und Verfügbarkeit.',
            )}
          </p>
        </div>
        <Button size="sm" onClick={() => setShowMove(true)}>
          {t('inventory.record', 'Bewegung erfassen')}
        </Button>
      </div>

      <div className="flex gap-1 border-b border-gray-200 dark:border-gray-700">
        {tabs.map((tb) => (
          <button
            key={tb.id}
            onClick={() => setTab(tb.id)}
            className={`px-4 py-2 text-sm font-medium ${tab === tb.id ? 'border-b-2 border-primary text-primary' : 'text-gray-500'}`}
          >
            {tb.label}
          </button>
        ))}
      </div>

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

      <Modal
        open={showMove}
        onOpenChange={setShowMove}
        title={t('inventory.record', 'Bewegung erfassen')}
      >
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            record.mutate();
          }}
        >
          <Select
            label={t('inventory.movementType', 'Bewegungsart')}
            options={MOVEMENT_TYPES.map((mt) => ({
              value: mt,
              label: `${MOVEMENT_ICONS[mt] ?? ''} ${mt}`,
            }))}
            value={moveDraft.movement_type}
            onChange={(e) => setMoveDraft({ ...moveDraft, movement_type: e.target.value })}
          />
          <Input
            label={t('inventory.assetId', 'Asset-ID (serialisiert)')}
            value={moveDraft.asset_id}
            onChange={(e) => setMoveDraft({ ...moveDraft, asset_id: e.target.value })}
          />
          <Input
            label={t('inventory.itemId', 'Mengenartikel-ID')}
            value={moveDraft.quantity_item_id}
            onChange={(e) => setMoveDraft({ ...moveDraft, quantity_item_id: e.target.value })}
          />
          {moveDraft.quantity_item_id ? (
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
              {t('common.save', 'Erfassen')}
            </Button>
          </div>
        </form>
      </Modal>
    </div>
  );
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
  return (
    <Card>
      <div className="mb-3">
        <Select
          aria-label="type"
          options={[
            { value: '', label: t('inventory.allTypes', 'Alle Arten') },
            ...MOVEMENT_TYPES.map((mt) => ({ value: mt, label: mt })),
          ]}
          value={typeFilter}
          onChange={(e) => setTypeFilter(e.target.value)}
          className="max-w-xs"
        />
      </div>
      {movements.isLoading ? <SkeletonList rows={5} label="…" /> : null}
      {movements.error ? (
        <ErrorState
          title={t('app.error')}
          retryLabel={t('common.retry')}
          onRetry={() => void movements.refetch()}
        />
      ) : null}
      <ul className="divide-y divide-gray-100 dark:divide-gray-800">
        {(movements.data?.data ?? []).map((m) => (
          <li key={m.id} className="flex items-center gap-3 py-2 text-sm">
            <span aria-hidden>{MOVEMENT_ICONS[m.movement_type] ?? '•'}</span>
            <span className="font-medium">{m.movement_type}</span>
            <span className="text-gray-500">{m.asset_id ?? m.quantity_item_id}</span>
            {m.quantity ? <Badge variant="neutral">×{m.quantity}</Badge> : null}
            {m.reason ? <span className="text-gray-400">{m.reason}</span> : null}
            <span className="ml-auto text-xs text-gray-400">
              {new Date(m.created_at).toLocaleString()}
            </span>
          </li>
        ))}
        {(movements.data?.data ?? []).length === 0 && !movements.isLoading ? (
          <li className="py-4 text-center text-sm text-gray-500">
            {t('inventory.noMovements', 'Keine Bewegungen.')}
          </li>
        ) : null}
      </ul>
    </Card>
  );
}

function ItemsTab({ items, t }: { items: ReturnType<typeof useQuantityItems>; t: TFunction }) {
  return (
    <div className="grid grid-cols-1 gap-3 md:grid-cols-2 lg:grid-cols-3">
      {items.isLoading ? <SkeletonList rows={4} label="…" /> : null}
      {(items.data?.data ?? []).map((it) => (
        <Card
          key={it.id}
          title={it.name}
          actions={
            it.stock_level <= it.min_level ? (
              <Badge variant="danger">{t('consumables.low', 'niedrig')}</Badge>
            ) : (
              <Badge variant="success">OK</Badge>
            )
          }
        >
          <p className="text-sm text-gray-600 dark:text-gray-300">{it.sku || '—'}</p>
          <p className="mt-1 text-lg font-semibold">
            {it.stock_level} {it.unit}{' '}
            <span className="text-xs font-normal text-gray-500">/ min {it.min_level}</span>
          </p>
        </Card>
      ))}
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
  return (
    <Card>
      {reservations.isLoading ? <SkeletonList rows={4} label="…" /> : null}
      <ul className="divide-y divide-gray-100 dark:divide-gray-800">
        {(reservations.data?.data ?? []).map((r) => (
          <li key={r.id} className="flex items-center gap-3 py-2 text-sm">
            <span aria-hidden>🔒</span>
            <span className="font-medium">{r.asset_id ?? r.quantity_item_id}</span>
            {r.quantity > 1 ? <Badge variant="neutral">×{r.quantity}</Badge> : null}
            {r.project_ref ? <Badge variant="info">{r.project_ref}</Badge> : null}
            {r.expires_at ? (
              <span className="text-xs text-gray-400">
                bis {new Date(r.expires_at).toLocaleDateString()}
              </span>
            ) : null}
            <Button size="sm" variant="ghost" className="ml-auto" onClick={() => onRelease(r.id)}>
              {t('inventory.release', 'Freigeben')}
            </Button>
          </li>
        ))}
        {(reservations.data?.data ?? []).length === 0 && !reservations.isLoading ? (
          <li className="py-4 text-center text-sm text-gray-500">
            {t('inventory.noReservations', 'Keine aktiven Reservierungen.')}
          </li>
        ) : null}
      </ul>
    </Card>
  );
}

function AvailabilityTab({
  availability,
  t,
}: {
  availability: ReturnType<typeof useAvailability>;
  t: TFunction;
}) {
  return (
    <Card>
      {availability.isLoading ? <SkeletonList rows={4} label="…" /> : null}
      <table className="w-full text-sm">
        <thead>
          <tr className="text-left text-xs uppercase text-gray-500">
            <th className="py-2">{t('inventory.item', 'Artikel')}</th>
            <th>{t('inventory.total', 'Gesamt')}</th>
            <th>{t('inventory.available', 'Verfügbar')}</th>
            <th>{t('inventory.reserved', 'Reserviert')}</th>
            <th>{t('inventory.assigned', 'Zugewiesen')}</th>
            <th>{t('inventory.repair', 'Reparatur')}</th>
          </tr>
        </thead>
        <tbody>
          {(availability.data?.data ?? []).map((a) => (
            <tr
              key={`${a.item_kind}-${a.item_id}`}
              className="border-t border-gray-100 dark:border-gray-800"
            >
              <td className="py-2 font-medium">
                {a.item_id.slice(0, 8)}… <Badge variant="neutral">{a.item_kind}</Badge>
              </td>
              <td>{a.total}</td>
              <td className="text-green-600">{a.available}</td>
              <td className="text-amber-600">{a.reserved}</td>
              <td>{a.assigned}</td>
              <td>{a.repair}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </Card>
  );
}
