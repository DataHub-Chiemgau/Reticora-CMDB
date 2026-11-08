/**
 * ConsumableDetailPage — one quantity item of the warehouse (UI-14, WP-320):
 * its master data, the stock per bin with on_hand, reserved and available
 * (GET /consumables/{id}/stock and /availability, page by page over
 * next_cursor), the bookings and the reservation of a quantity in a bin,
 * and its movements (ObjectHistory, WP-806). Receipt and transfer are the
 * flow dialogs of WP-806; retrieval and correction follow the same pattern
 * here and are mounted by the inventory page as well. Every booking goes to
 * POST /stock-movements with consumable_id (WP-756). Bookings are offered
 * only with stock:write, the reservation only with reservation:write; the
 * server still decides every request.
 */
import {
  ArrowDownToLine,
  ArrowLeftRight,
  ArrowUpFromLine,
  Lock,
  Pencil,
  Warehouse,
  type LucideIcon,
} from 'lucide-react';
import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useParams } from 'react-router-dom';
import { ApiError, type Consumable } from '../api/client';
import { useConsumable, useConsumableAvailability, useHasPermission } from '../api/hooks';
import { useReserveConsumable, useStockBooking, useStockLevels } from '../api/cmdbHooks';
import { CONSUMABLE_BOOKING_TYPES } from '../api/cmdb';
import type { ConsumableBookingType, LocationKind } from '../api/cmdb';
import { useEntitlements } from '../hooks/useEntitlements';
import { ObjectHistory, formatDateTime, useLocationNames } from '../components/cmdb/ObjectHistory';
import {
  MoveStockDialog,
  ReceiveDialog,
  objectLabel,
  quantityOf,
  useStockFlowGates,
  type ConsumableFlowDialogProps,
} from '../components/flows/StockFlowDialogs';
import { dueAt } from '../components/flows/AssignDialogs';
import {
  ObjectPicker,
  accessErrorMessage,
  type PickedObject,
} from '../components/pickers/ObjectPicker';
import { LocationPicker, type PickedLocation } from '../components/pickers/LocationPicker';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import {
  tableClasses,
  tdClasses,
  tdNumericClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';
import { cn } from '../components/ui/utils';
import { ScrollRegion } from '../components/ScrollRegion';
import { TableEmptyRow } from './ListParts';

/** Bookings and reservations of a consumable take only bins (WP-283). */
const BIN_KINDS: readonly LocationKind[] = ['bin'];

/**
 * isBelowMinimum is the rule of WP-285: the total stock over all bins is
 * below min_level. Neither min_level 0 nor a total equal to it is below.
 */
export function isBelowMinimum(item: Pick<Consumable, 'stock_level' | 'min_level'>): boolean {
  return item.min_level > 0 && item.stock_level < item.min_level;
}

/** pickedConsumable is what the flow dialogs show of a consumable: name and SKU. */
export function pickedConsumable(item: Pick<Consumable, 'id' | 'name' | 'sku'>): PickedObject {
  return { kind: 'consumable', id: item.id, name: item.name, code: item.sku || undefined };
}

/** fieldError is the message of a refused request at one input (violations, FieldProblem). */
function fieldError(error: unknown, field: string): string | undefined {
  if (!(error instanceof ApiError)) return undefined;
  const violation = error.violations.find((v) => v.field === field);
  if (violation) return violation.detail;
  return error.field === field ? error.detail : undefined;
}

/** FormError shows the problem detail of a refused request in the dialog. */
function FormError({ error }: { error: unknown }) {
  const { t } = useTranslation();
  if (!error) return null;
  return (
    <p role="alert" className="text-sm text-danger">
      {accessErrorMessage(error, t)}
    </p>
  );
}

function DialogButtons({
  onCancel,
  submitLabel,
  onSubmit,
  disabled,
}: {
  onCancel: () => void;
  submitLabel: string;
  onSubmit: () => void;
  disabled: boolean;
}) {
  const { t } = useTranslation();
  return (
    <div className="flex justify-end gap-2">
      <Button variant="secondary" onClick={onCancel}>
        {t('common.cancel')}
      </Button>
      <Button onClick={onSubmit} disabled={disabled}>
        {submitLabel}
      </Button>
    </div>
  );
}

/**
 * useBinRows reads the stock per bin of the chosen consumable while a
 * dialog is open, with the names of the bins from the location tree.
 */
function useBinRows(open: boolean, consumableId: string | undefined) {
  const stock = useStockLevels(open ? consumableId : undefined);
  const rows = consumableId ? (stock.data?.data ?? []) : [];
  const names = useLocationNames(rows.length > 0);
  return { stock, rows, names };
}

type CorrectionDirection = 'increase' | 'decrease';

export interface ConsumableBookingDialogProps extends ConsumableFlowDialogProps {
  /** type is retrieval (from a bin) or correction (one bin, up or down). */
  type: Extract<ConsumableBookingType, 'retrieval' | 'correction'>;
}

/**
 * ConsumableBookingDialog books a retrieval from a bin (from_location_id)
 * or the correction of one bin: an increase with to_location_id, a
 * decrease with from_location_id, always with a reason. The request is sent
 * on submit and the dialog stays open on a refusal: the problem detail
 * (409 insufficient-stock, 422 no bin) shows in the dialog, field errors of
 * violations at their input.
 */
export function ConsumableBookingDialog({
  type,
  open,
  onOpenChange,
  initialItem,
  lockItem,
}: ConsumableBookingDialogProps) {
  const { t } = useTranslation();
  const booking = useStockBooking();
  const [picked, setPicked] = useState<PickedObject[]>([]);
  const [direction, setDirection] = useState<CorrectionDirection>('decrease');
  const [from, setFrom] = useState('');
  const [to, setTo] = useState<PickedLocation | null>(null);
  const [quantity, setQuantity] = useState('1');
  const [reason, setReason] = useState('');
  const [error, setError] = useState<unknown>(null);

  // A locked item (its detail page) shows no picker; else initialItem prefills it.
  const fixed = lockItem ? initialItem : undefined;
  useEffect(() => {
    if (open && initialItem && !lockItem) setPicked([initialItem]);
  }, [open, initialItem, lockItem]);
  const chosen = fixed ?? picked[0];
  const { stock, rows, names } = useBinRows(open, chosen?.id);

  const reset = () => {
    setPicked([]);
    setDirection('decrease');
    setFrom('');
    setTo(null);
    setQuantity('1');
    setReason('');
    setError(null);
  };
  const close = (next: boolean) => {
    if (!next) reset();
    onOpenChange(next);
  };

  const fromStock = type === 'retrieval' || direction === 'decrease';
  const source = rows.find((row) => row.location_id === from);
  const amount = quantityOf(quantity);
  const reasonMissing = type === 'correction' && reason.trim() === '';
  const ready = !!chosen && amount !== undefined && (fromStock ? !!source : !!to) && !reasonMissing;

  const submit = async () => {
    if (!chosen || amount === undefined || !ready) return;
    const bin =
      fromStock && source ? { from_location_id: source.location_id } : { to_location_id: to?.id };
    setError(null);
    try {
      await booking.mutateAsync({
        consumable_id: chosen.id,
        movement_type: type,
        ...bin,
        quantity: amount,
        reason: reason.trim() || undefined,
      });
      close(false);
    } catch (failure) {
      setError(failure);
    }
  };

  let stockNotice: string | undefined;
  if (fromStock && chosen && stock.isLoading) stockNotice = t('stockFlow.loadingStock');
  else if (fromStock && chosen && stock.error) stockNotice = accessErrorMessage(stock.error, t);
  else if (fromStock && chosen && stock.data && rows.length === 0)
    stockNotice = t('stockFlow.noStock');

  let quantityError = fieldError(error, 'quantity');
  if (quantity !== '' && amount === undefined) quantityError = t('stockFlow.quantityPositive');
  const binField = fromStock ? 'from_location_id' : 'to_location_id';

  return (
    <Modal
      open={open}
      onOpenChange={close}
      title={t(`consumables.movementTypes.${type}`)}
      description={fixed ? objectLabel(fixed) : undefined}
    >
      <div className="space-y-4">
        {fixed ? null : (
          <ObjectPicker
            label={t('stockFlow.consumable')}
            kinds={['consumable']}
            value={picked}
            onChange={(next) => {
              setPicked(next);
              setFrom('');
            }}
            required
          />
        )}
        {type === 'correction' ? (
          <Select
            label={t('consumables.correctionDirection')}
            value={direction}
            onChange={(event) => {
              setDirection(event.target.value as CorrectionDirection);
              setFrom('');
              setTo(null);
            }}
            options={[
              { value: 'decrease', label: t('consumables.correctionDecrease') },
              { value: 'increase', label: t('consumables.correctionIncrease') },
            ]}
          />
        ) : null}
        {fromStock && rows.length > 0 ? (
          <Select
            label={t(type === 'retrieval' ? 'stockFlow.fromBin' : 'consumables.bin')}
            value={from}
            onChange={(event) => setFrom(event.target.value)}
            error={fieldError(error, binField)}
            options={[
              { value: '', label: t('stockFlow.chooseBin') },
              ...rows.map((row) => ({
                value: row.location_id,
                label: t('stockFlow.binOption', {
                  bin: names.name(row.location_id),
                  onHand: row.on_hand,
                  reserved: row.reserved,
                }),
              })),
            ]}
          />
        ) : null}
        {stockNotice ? (
          <p role="status" className="text-sm text-fg-muted">
            {stockNotice}
          </p>
        ) : null}
        {fromStock ? null : (
          <>
            <LocationPicker
              label={t('consumables.bin')}
              kinds={BIN_KINDS}
              value={to?.id ?? null}
              onChange={setTo}
            />
            {fieldError(error, binField) ? (
              <p role="alert" className="text-sm text-danger">
                {fieldError(error, binField)}
              </p>
            ) : null}
          </>
        )}
        <Input
          type="number"
          min={0}
          step="any"
          label={t('stockFlow.quantity')}
          value={quantity}
          onChange={(event) => setQuantity(event.target.value)}
          error={quantityError}
        />
        <Input
          label={t(type === 'correction' ? 'consumables.reasonRequired' : 'stockFlow.reason')}
          value={reason}
          onChange={(event) => setReason(event.target.value)}
          required={type === 'correction'}
          error={fieldError(error, 'reason')}
        />
        <FormError error={error} />
        <DialogButtons
          onCancel={() => close(false)}
          submitLabel={t('consumables.book')}
          onSubmit={() => void submit()}
          disabled={!ready || booking.isPending}
        />
      </div>
    </Modal>
  );
}

export interface ReserveConsumableDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  item: PickedObject;
}

/**
 * ReserveConsumableDialog reserves a quantity of a consumable in a bin
 * (POST /reservations with item_kind consumable, consumable_id,
 * location_id, quantity and expires_at, WP-759); at most on_hand −
 * reserved of the bin. The reserved quantity shows per bin afterwards;
 * list and release stay in the reservations tab of the inventory page.
 */
export function ReserveConsumableDialog({
  open,
  onOpenChange,
  item,
}: ReserveConsumableDialogProps) {
  const { t } = useTranslation();
  const reserve = useReserveConsumable();
  const [from, setFrom] = useState('');
  const [quantity, setQuantity] = useState('1');
  const [expires, setExpires] = useState('');
  const [reason, setReason] = useState('');
  const [error, setError] = useState<unknown>(null);
  const { rows, names } = useBinRows(open, item.id);

  const close = (next: boolean) => {
    if (!next) {
      setFrom('');
      setQuantity('1');
      setExpires('');
      setReason('');
      setError(null);
    }
    onOpenChange(next);
  };

  const source = rows.find((row) => row.location_id === from);
  const available = source ? Math.max(source.on_hand - source.reserved, 0) : undefined;
  const amount = quantityOf(quantity);
  const tooMuch = amount !== undefined && available !== undefined && amount > available;
  let quantityError = fieldError(error, 'quantity');
  if (quantity !== '' && amount === undefined) quantityError = t('stockFlow.quantityPositive');
  else if (tooMuch) quantityError = t('stockFlow.maxQuantity', { max: available });
  const ready = !!source && amount !== undefined && !tooMuch;

  const submit = async () => {
    if (!source || amount === undefined || !ready) return;
    setError(null);
    try {
      await reserve.mutateAsync({
        item_kind: 'consumable',
        consumable_id: item.id,
        location_id: source.location_id,
        quantity: amount,
        expires_at: dueAt(expires),
        reason: reason.trim() || undefined,
      });
      close(false);
    } catch (failure) {
      setError(failure);
    }
  };

  return (
    <Modal
      open={open}
      onOpenChange={close}
      title={t('consumables.reserveTitle')}
      description={objectLabel(item)}
    >
      <div className="space-y-4">
        {rows.length > 0 ? (
          <Select
            label={t('consumables.bin')}
            value={from}
            onChange={(event) => setFrom(event.target.value)}
            error={fieldError(error, 'location_id')}
            options={[
              { value: '', label: t('stockFlow.chooseBin') },
              ...rows.map((row) => ({
                value: row.location_id,
                label: t('stockFlow.binOption', {
                  bin: names.name(row.location_id),
                  onHand: row.on_hand,
                  reserved: row.reserved,
                }),
              })),
            ]}
          />
        ) : (
          <p role="status" className="text-sm text-fg-muted">
            {t('stockFlow.noStock')}
          </p>
        )}
        <Input
          type="number"
          min={0}
          max={available}
          step="any"
          label={t('stockFlow.quantity')}
          value={quantity}
          onChange={(event) => setQuantity(event.target.value)}
          error={quantityError}
        />
        <Input
          type="date"
          label={t('stockFlow.expiresAt')}
          value={expires}
          onChange={(event) => setExpires(event.target.value)}
          error={fieldError(error, 'expires_at')}
        />
        <Input
          label={t('stockFlow.reason')}
          value={reason}
          onChange={(event) => setReason(event.target.value)}
        />
        <FormError error={error} />
        <DialogButtons
          onCancel={() => close(false)}
          submitLabel={t('stockFlow.reserve')}
          onSubmit={() => void submit()}
          disabled={!ready || reserve.isPending}
        />
      </div>
    </Modal>
  );
}

/** The flows of the page; each is one dialog. */
type ConsumableFlow = ConsumableBookingType | 'reserve';

/** The glyphs of the bookings, as MOVEMENT_ICONS of ObjectHistory shows them. */
const BOOKING_ICONS: Record<ConsumableBookingType, LucideIcon> = {
  receipt: ArrowDownToLine,
  retrieval: ArrowUpFromLine,
  correction: Pencil,
  transfer: ArrowLeftRight,
};

export function ConsumableDetailPage() {
  const { id = '' } = useParams();
  const { t } = useTranslation();
  const { isEnabled } = useEntitlements();
  const consumable = useConsumable(isEnabled('stock') ? id : undefined);

  if (!isEnabled('stock')) {
    return <ErrorState title={t('app.error')} description={t('pickers.errors.notEntitled')} />;
  }
  if (consumable.isLoading) return <SkeletonList rows={5} label={t('app.loading')} />;
  if (consumable.error || !consumable.data) {
    return (
      <ErrorState
        title={t('app.error')}
        description={consumable.error ? accessErrorMessage(consumable.error, t) : undefined}
        retryLabel={t('common.retry')}
        onRetry={() => void consumable.refetch()}
      />
    );
  }
  return <ConsumableDetail item={consumable.data} />;
}

function ConsumableDetail({ item }: { item: Consumable }) {
  const { t, i18n } = useTranslation();
  const { isEnabled } = useEntitlements();
  const gates = useStockFlowGates();
  const canBook = useHasPermission('stock:write') === true && gates.receive;
  const canReserve = useHasPermission('reservation:write') === true && isEnabled('stock');
  const [flow, setFlow] = useState<ConsumableFlow | null>(null);
  const picked = pickedConsumable(item);
  const below = isBelowMinimum(item);

  const dialog = (name: ConsumableFlow) => ({
    open: flow === name,
    onOpenChange: (open: boolean) => setFlow(open ? name : null),
    initialItem: picked,
    lockItem: true,
  });

  return (
    <div className="space-y-4">
      <PageHeader
        title={item.name}
        description={item.sku ? `${t('inventory.sku')} ${item.sku}` : undefined}
        breadcrumbs={[{ label: t('nav.consumables'), to: '/consumables' }, { label: item.name }]}
        meta={
          below ? (
            <Badge variant="danger">{t('consumables.low')}</Badge>
          ) : (
            <Badge variant="success">{t('consumables.ok')}</Badge>
          )
        }
        actions={
          <div className="flex flex-wrap gap-2">
            {canBook
              ? CONSUMABLE_BOOKING_TYPES.map((type, index) => {
                  const Icon = BOOKING_ICONS[type];
                  return (
                    <Button
                      key={type}
                      variant={index === 0 ? 'primary' : 'secondary'}
                      icon={<Icon />}
                      onClick={() => setFlow(type)}
                    >
                      {t(`consumables.movementTypes.${type}`)}
                    </Button>
                  );
                })
              : null}
            {canReserve ? (
              <Button variant="secondary" icon={<Lock />} onClick={() => setFlow('reserve')}>
                {t('stockFlow.reserve')}
              </Button>
            ) : null}
          </div>
        }
      />

      <Card title={t('consumables.masterData')}>
        <dl className="grid gap-3 text-sm sm:grid-cols-3">
          <Fact label={t('inventory.sku')} value={item.sku} />
          <Fact label={t('consumables.barcode')} value={item.barcode} />
          <Fact label={t('consumables.category')} value={item.category} />
          <Fact label={t('consumables.unit')} value={item.unit} />
          <Fact label={t('consumables.minLevel')} value={String(item.min_level)} />
          <Fact
            label={t('consumables.totalStock')}
            value={t('consumables.quantityWithUnit', {
              quantity: item.stock_level,
              unit: item.unit,
            })}
          />
        </dl>
        {below ? (
          <p role="status" className="mt-3 text-sm text-danger">
            {t('consumables.belowMinimum', { stock: item.stock_level, min: item.min_level })}
          </p>
        ) : null}
      </Card>

      <BinTable id={item.id} unit={item.unit} language={i18n.language} />

      <Card title={t('consumables.movements')}>
        <ObjectHistory entityType="consumable" id={item.id} label={t('consumables.movements')} />
      </Card>

      {/* The dialogs stay mounted: receive and move wait in their undo window
          and are sent when the page is left (useUndoableSubmit). */}
      <ReceiveDialog {...dialog('receipt')} />
      <MoveStockDialog {...dialog('transfer')} />
      <ConsumableBookingDialog type="retrieval" {...dialog('retrieval')} />
      <ConsumableBookingDialog type="correction" {...dialog('correction')} />
      <ReserveConsumableDialog
        open={flow === 'reserve'}
        onOpenChange={(open) => setFlow(open ? 'reserve' : null)}
        item={picked}
      />
    </div>
  );
}

function Fact({ label, value }: { label: string; value?: string }) {
  return (
    <div>
      <dt className="text-fg-muted">{label}</dt>
      <dd className="font-medium text-fg">{value || '—'}</dd>
    </div>
  );
}

/**
 * BinTable lists the bins of the consumable with on_hand, reserved and
 * available (GET /consumables/{id}/availability, page by page over
 * next_cursor) and the time of the last booking of the stock row (GET
 * /consumables/{id}/stock). Bins carry their path in the location tree.
 */
function BinTable({ id, unit, language }: { id: string; unit: string; language: string }) {
  const { t } = useTranslation();
  const availability = useConsumableAvailability(id);
  const stock = useStockLevels(id);
  const rows = (availability.data?.pages ?? []).flatMap((page) => page.data);
  const names = useLocationNames(rows.length > 0);
  const updated = new Map((stock.data?.data ?? []).map((row) => [row.location_id, row.updated_at]));

  return (
    <section className="space-y-2">
      <h2 className="text-base font-semibold text-fg">{t('consumables.stockPerBin')}</h2>
      {availability.isLoading ? <SkeletonList rows={3} label={t('app.loading')} /> : null}
      {availability.error ? (
        <ErrorState
          title={t('app.error')}
          description={accessErrorMessage(availability.error, t)}
          retryLabel={t('common.retry')}
          onRetry={() => void availability.refetch()}
        />
      ) : null}
      {availability.data ? (
        <Card className="p-0 overflow-hidden">
          <ScrollRegion label={t('consumables.stockPerBin')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('consumables.bin')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('consumables.onHand')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('inventory.reserved')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('inventory.available')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('consumables.updatedAt')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => (
                  <tr key={row.location_id} className={trClasses}>
                    <td className={tdPrimaryClasses}>{names.path(row.location_id)}</td>
                    <td className={tdNumericClasses}>
                      {t('consumables.quantityWithUnit', { quantity: row.on_hand, unit })}
                    </td>
                    <td className={tdNumericClasses}>{row.reserved}</td>
                    <td className={cn(tdNumericClasses, 'font-medium text-fg')}>{row.available}</td>
                    <td className={cn(tdClasses, 'whitespace-nowrap text-right tabular-nums')}>
                      {formatDateTime(updated.get(row.location_id), language) ?? '—'}
                    </td>
                  </tr>
                ))}
                {rows.length === 0 ? (
                  <TableEmptyRow
                    colSpan={5}
                    icon={<Warehouse />}
                    title={t('consumables.noBinsTitle')}
                    description={t('consumables.noBinsHint')}
                  />
                ) : null}
              </tbody>
            </table>
          </ScrollRegion>
        </Card>
      ) : null}
      {availability.hasNextPage ? (
        <div className="flex justify-center p-3">
          <Button
            variant="secondary"
            size="sm"
            onClick={() => void availability.fetchNextPage()}
            disabled={availability.isFetchingNextPage}
          >
            {t('consumables.loadMoreBins')}
          </Button>
        </div>
      ) : null}
    </section>
  );
}
