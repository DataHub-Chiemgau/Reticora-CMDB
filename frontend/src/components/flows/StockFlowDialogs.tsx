/**
 * StockFlowDialogs — the everyday flows of the warehouse (UI-15, WP-806):
 * receive a consumable into a bin, move it from bin to bin, move a serial
 * asset to a location (E-142) and reserve an asset (WP-277). Objects and
 * locations are chosen with the pickers of WP-318, never as ids. Every
 * write goes out behind the undo window (useUndoableSubmit): the dialog
 * closes at once, "Undo" drops the request without a server call, and a
 * failure of the delayed request shows as error toast with object and
 * message. Once sent, a booking is corrected only by a counter-booking
 * (Convention 14, WP-274). Keep the dialogs mounted with the page: leaving
 * the page sends a waiting request at once. The asset and consumable detail
 * pages (WP-809, WP-320) mount the same dialogs. With the feature barcode a
 * scan button next to each picker takes the object or bin of a scanned code
 * (ScanDialog, WP-807); the scan dialog opens the dialogs with the scanned
 * object (initialItem, initialAsset).
 */
import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { ApiError } from '../../api/client';
import { useHasPermission } from '../../api/hooks';
import { useReserveAsset, useStockBooking, useStockLevels } from '../../api/cmdbHooks';
import type { LocationKind } from '../../api/cmdb';
import { useEntitlements, useModules } from '../../hooks/useEntitlements';
import { useUndoableSubmit } from '../../hooks/useUndoableSubmit';
import { useLocationNames } from '../cmdb/ObjectHistory';
import { ObjectPicker, accessErrorMessage, type PickedObject } from '../pickers/ObjectPicker';
import { LocationPicker, type PickedLocation } from '../pickers/LocationPicker';
import { UserTeamPicker, type PickedHolder } from '../pickers/UserTeamPicker';
import { dueAt } from './AssignDialogs';
import { ScanButton, WithScan } from './ScanDialog';
import { Button } from '../ui/Button';
import { Input } from '../ui/Input';
import { Modal } from '../ui/Modal';
import { Select } from '../ui/Select';

/** PROBLEM_INSUFFICIENT_STOCK is the 409 of a withdrawal above the stock of a bin (WP-756). */
export const PROBLEM_INSUFFICIENT_STOCK = 'https://reticora.io/problems/insufficient-stock';
/** PROBLEM_ASSET_RESERVED is the 409 of an asset with an active reservation (WP-277). */
export const PROBLEM_ASSET_RESERVED = 'https://reticora.io/problems/asset-reserved';
/** PROBLEM_ASSET_ASSIGNED is the 409 of an asset with a holder (WP-277). */
export const PROBLEM_ASSET_ASSIGNED = 'https://reticora.io/problems/asset-assigned';

/** Stock bookings of a consumable take only bins (WP-283). */
const BIN_KINDS: readonly LocationKind[] = ['bin'];

/**
 * stockFlowErrorMessage words a failed booking or reservation: too little
 * stock in the bin, an asset reserved or assigned already, conflict for any
 * other 409 when given (a reservation no longer active), else the package,
 * limit or permission message of accessErrorMessage or the problem detail.
 */
export function stockFlowErrorMessage(error: unknown, t: TFunction, conflict?: string): string {
  if (error instanceof ApiError && error.status === 409) {
    if (error.type === PROBLEM_INSUFFICIENT_STOCK) return t('stockFlow.errors.insufficientStock');
    if (error.type === PROBLEM_ASSET_RESERVED) return t('stockFlow.errors.assetReserved');
    if (error.type === PROBLEM_ASSET_ASSIGNED) return t('stockFlow.errors.assetAssigned');
    if (conflict) return conflict;
  }
  return accessErrorMessage(error, t);
}

/** objectLabel is what a flow shows of an object: its name and its code. */
export function objectLabel(item: PickedObject): string {
  return item.code ? `${item.name} (${item.code})` : item.name;
}

export interface StockFlowGates {
  /** receive and move: modules inventory and stocktake, feature stock. */
  receive: boolean;
  move: boolean;
  /** moveAsset: module inventory, feature stock, stock:write and asset:move (E-142). */
  moveAsset: boolean;
  /** reserve: module inventory, feature assets_basic. */
  reserve: boolean;
}

/**
 * useStockFlowGates tells which flows the release, the plan and the rights
 * offer (WP-214, WP-244, E-44): POST /stock-movements needs the feature
 * stock, the consumable routes the module stocktake, the reservation of an
 * asset the feature assets_basic. Moving an asset also needs stock:write
 * and asset:move (WP-756); the server still decides every request.
 */
export function useStockFlowGates(): StockFlowGates {
  const { isModuleEnabled } = useModules();
  const { isEnabled } = useEntitlements();
  const canWriteStock = useHasPermission('stock:write');
  const canMoveAsset = useHasPermission('asset:move');
  const inventory = isModuleEnabled('inventory');
  const stock = inventory && isModuleEnabled('stocktake') && isEnabled('stock');
  return {
    receive: stock,
    move: stock,
    moveAsset: inventory && isEnabled('stock') && canWriteStock === true && canMoveAsset === true,
    reserve: inventory && isEnabled('assets_basic'),
  };
}

export interface StockFlowDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

/**
 * useInitialObject puts the object of a scan into the picker when the
 * dialog opens with one (WP-807).
 */
function useInitialObject(
  open: boolean,
  initial: PickedObject | undefined,
  set: (value: PickedObject[]) => void,
) {
  useEffect(() => {
    if (open && initial) set([initial]);
  }, [open, initial, set]);
}

/** BinScan is the scan button of a bin picker: only bins are taken. */
function BinScan({ field, onBin }: { field: string; onBin: (bin: PickedLocation) => void }) {
  return (
    <ScanButton field={field} accept={['location']} locationKinds={BIN_KINDS} onLocation={onBin} />
  );
}

/** ObjectScan is the scan button of an object picker of one kind. */
function ObjectScan({
  field,
  kind,
  onObject,
}: {
  field: string;
  kind: 'asset' | 'consumable';
  onObject: (object: PickedObject) => void;
}) {
  return <ScanButton field={field} accept={[kind]} onObject={onObject} />;
}

export interface ConsumableFlowDialogProps extends StockFlowDialogProps {
  /** initialItem puts a consumable into the picker, e.g. the object of a scan (WP-807). */
  initialItem?: PickedObject;
  /**
   * lockItem fixes initialItem as the consumable of the dialog, e.g. on its
   * detail page (WP-320): no picker and no scan of the item.
   */
  lockItem?: boolean;
}

/** quantityOf reads a quantity input; anything but a positive number is undefined. */
export function quantityOf(value: string): number | undefined {
  const quantity = Number(value);
  return value.trim() !== '' && Number.isFinite(quantity) && quantity > 0 ? quantity : undefined;
}

function DialogActions({
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
 * ReceiveDialog books a receipt of a consumable into a bin: POST
 * /stock-movements with consumable_id, movement_type receipt,
 * to_location_id, quantity and reason (WP-756). The receipt of an order
 * (POST /orders/{id}/receive) stays with the order pages (WP-812).
 */
export function ReceiveDialog({
  open,
  onOpenChange,
  initialItem,
  lockItem,
}: ConsumableFlowDialogProps) {
  // A locked item (its detail page) shows neither picker nor scan.
  const fixed = lockItem ? initialItem : undefined;
  const { t } = useTranslation();
  const booking = useStockBooking();
  const { scheduleSubmit } = useUndoableSubmit();
  const [picked, setItem] = useState<PickedObject[]>([]);
  const item = fixed ? [fixed] : picked;
  const [bin, setBin] = useState<PickedLocation | null>(null);
  const [quantity, setQuantity] = useState('1');
  const [reason, setReason] = useState('');
  useInitialObject(open, initialItem, setItem);

  const reset = () => {
    setItem([]);
    setBin(null);
    setQuantity('1');
    setReason('');
  };
  const close = (next: boolean) => {
    if (!next) reset();
    onOpenChange(next);
  };

  const chosen = item[0];
  const amount = quantityOf(quantity);
  const submit = () => {
    if (!chosen || !bin || amount === undefined) return;
    const label = objectLabel(chosen);
    const data = {
      consumable_id: chosen.id,
      movement_type: 'receipt' as const,
      to_location_id: bin.id,
      quantity: amount,
      reason: reason.trim() || undefined,
    };
    scheduleSubmit({
      id: `receive:${chosen.id}:${bin.id}`,
      message: t('stockFlow.receivePending', { quantity: amount, item: label, bin: bin.name }),
      submitFn: () => booking.mutateAsync(data),
      failedMessage: (error) =>
        t('stockFlow.receiveFailed', { item: label, message: stockFlowErrorMessage(error, t) }),
    });
    close(false);
  };

  return (
    <Modal
      open={open}
      onOpenChange={close}
      title={t('stockFlow.receiveTitle')}
      description={fixed ? objectLabel(fixed) : undefined}
    >
      <div className="space-y-4">
        {fixed ? null : (
          <WithScan
            scan={
              <ObjectScan
                field={t('stockFlow.consumable')}
                kind="consumable"
                onObject={(object) => setItem([object])}
              />
            }
          >
            <ObjectPicker
              label={t('stockFlow.consumable')}
              kinds={['consumable']}
              value={item}
              onChange={setItem}
              required
            />
          </WithScan>
        )}
        <WithScan scan={<BinScan field={t('stockFlow.toBin')} onBin={setBin} />}>
          <LocationPicker
            label={t('stockFlow.toBin')}
            kinds={BIN_KINDS}
            value={bin?.id ?? null}
            onChange={setBin}
          />
        </WithScan>
        <Input
          type="number"
          min={0}
          step="any"
          label={t('stockFlow.quantity')}
          value={quantity}
          onChange={(event) => setQuantity(event.target.value)}
          error={
            quantity !== '' && amount === undefined ? t('stockFlow.quantityPositive') : undefined
          }
        />
        <Input
          label={t('stockFlow.reason')}
          value={reason}
          onChange={(event) => setReason(event.target.value)}
        />
        <DialogActions
          onCancel={() => close(false)}
          submitLabel={t('stockFlow.receive')}
          onSubmit={submit}
          disabled={!chosen || !bin || amount === undefined}
        />
      </div>
    </Modal>
  );
}

/**
 * MoveStockDialog moves a quantity of a consumable from bin to bin: the
 * source bin comes from the stock per bin (GET /consumables/{id}/stock,
 * WP-283) with on_hand and reserved, the target from the LocationPicker
 * (bins only, not the source); at most on_hand − reserved moves. POST
 * /stock-movements with movement_type transfer, from_location_id and
 * to_location_id.
 */
export function MoveStockDialog({
  open,
  onOpenChange,
  initialItem,
  lockItem,
}: ConsumableFlowDialogProps) {
  // A locked item (its detail page) shows neither picker nor scan.
  const fixed = lockItem ? initialItem : undefined;
  const { t } = useTranslation();
  const booking = useStockBooking();
  const { scheduleSubmit } = useUndoableSubmit();
  const [picked, setItem] = useState<PickedObject[]>([]);
  const item = fixed ? [fixed] : picked;
  const [from, setFrom] = useState('');
  const [to, setTo] = useState<PickedLocation | null>(null);
  const [quantity, setQuantity] = useState('1');
  const [reason, setReason] = useState('');
  useInitialObject(open, initialItem, setItem);

  const chosen = item[0];
  const stock = useStockLevels(open ? chosen?.id : undefined);
  const rows = chosen ? (stock.data?.data ?? []) : [];
  const names = useLocationNames(rows.length > 0);

  const reset = () => {
    setItem([]);
    setFrom('');
    setTo(null);
    setQuantity('1');
    setReason('');
  };
  const close = (next: boolean) => {
    if (!next) reset();
    onOpenChange(next);
  };

  const source = rows.find((row) => row.location_id === from);
  const available = source ? Math.max(source.on_hand - source.reserved, 0) : undefined;
  const amount = quantityOf(quantity);
  const sameBin = !!to && to.id === from;
  const tooMuch = amount !== undefined && available !== undefined && amount > available;
  let quantityError: string | undefined;
  if (quantity !== '' && amount === undefined) quantityError = t('stockFlow.quantityPositive');
  else if (tooMuch) quantityError = t('stockFlow.maxQuantity', { max: available });

  const submit = () => {
    if (!chosen || !source || !to || sameBin || amount === undefined || tooMuch) return;
    const label = objectLabel(chosen);
    const data = {
      consumable_id: chosen.id,
      movement_type: 'transfer' as const,
      from_location_id: source.location_id,
      to_location_id: to.id,
      quantity: amount,
      reason: reason.trim() || undefined,
    };
    scheduleSubmit({
      id: `move:${chosen.id}:${source.location_id}:${to.id}`,
      message: t('stockFlow.movePending', { quantity: amount, item: label, bin: to.name }),
      submitFn: () => booking.mutateAsync(data),
      failedMessage: (error) =>
        t('stockFlow.moveFailed', { item: label, message: stockFlowErrorMessage(error, t) }),
    });
    close(false);
  };

  let stockNotice: string | undefined;
  if (chosen && stock.isLoading) stockNotice = t('stockFlow.loadingStock');
  else if (chosen && stock.error) {
    // A missing right or feature says so; any other failure is a load error.
    stockNotice =
      stock.error instanceof ApiError && stock.error.status === 403
        ? accessErrorMessage(stock.error, t)
        : t('stockFlow.stockLoadError');
  } else if (chosen && stock.data && rows.length === 0) stockNotice = t('stockFlow.noStock');

  return (
    <Modal
      open={open}
      onOpenChange={close}
      title={t('stockFlow.moveTitle')}
      description={fixed ? objectLabel(fixed) : undefined}
    >
      <div className="space-y-4">
        {fixed ? null : (
          <WithScan
            scan={
              <ObjectScan
                field={t('stockFlow.consumable')}
                kind="consumable"
                onObject={(object) => {
                  setItem([object]);
                  setFrom('');
                }}
              />
            }
          >
            <ObjectPicker
              label={t('stockFlow.consumable')}
              kinds={['consumable']}
              value={item}
              onChange={(next) => {
                setItem(next);
                setFrom('');
              }}
              required
            />
          </WithScan>
        )}
        {rows.length > 0 ? (
          <Select
            label={t('stockFlow.fromBin')}
            value={from}
            onChange={(event) => setFrom(event.target.value)}
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
        <WithScan scan={<BinScan field={t('stockFlow.toBin')} onBin={setTo} />}>
          <LocationPicker
            label={t('stockFlow.toBin')}
            kinds={BIN_KINDS}
            value={to?.id ?? null}
            onChange={setTo}
          />
        </WithScan>
        {sameBin ? (
          <p role="alert" className="text-sm text-danger">
            {t('stockFlow.sameBin')}
          </p>
        ) : null}
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
          label={t('stockFlow.reason')}
          value={reason}
          onChange={(event) => setReason(event.target.value)}
        />
        <DialogActions
          onCancel={() => close(false)}
          submitLabel={t('stockFlow.move')}
          onSubmit={submit}
          disabled={!chosen || !source || !to || sameBin || amount === undefined || tooMuch}
        />
      </div>
    </Modal>
  );
}

/**
 * MoveAssetDialog moves a serial asset to a location (E-142): POST
 * /stock-movements with asset_id, movement_type transfer, to_location_id
 * and reason. PATCH /assets/{id} refuses location_id (WP-271), and
 * POST /assets/{id}/transfer changes the holder; neither is used here.
 * Offer it only with useStockFlowGates().moveAsset.
 */
export function MoveAssetDialog({
  open,
  onOpenChange,
  initialAsset,
}: StockFlowDialogProps & {
  /** initialAsset is the asset of the asset detail page (WP-809); the dialog opens with it. */
  initialAsset?: PickedObject;
}) {
  const { t } = useTranslation();
  const booking = useStockBooking();
  const { scheduleSubmit } = useUndoableSubmit();
  const [asset, setAsset] = useState<PickedObject[]>([]);
  useInitialObject(open, initialAsset, setAsset);
  const [to, setTo] = useState<PickedLocation | null>(null);
  const [reason, setReason] = useState('');

  const reset = () => {
    setAsset([]);
    setTo(null);
    setReason('');
  };
  const close = (next: boolean) => {
    if (!next) reset();
    onOpenChange(next);
  };

  const chosen = asset[0];
  const submit = () => {
    if (!chosen || !to) return;
    const label = objectLabel(chosen);
    const data = {
      asset_id: chosen.id,
      movement_type: 'transfer' as const,
      to_location_id: to.id,
      reason: reason.trim() || undefined,
    };
    scheduleSubmit({
      id: `move-asset:${chosen.id}`,
      message: t('stockFlow.moveAssetPending', { asset: label, location: to.name }),
      submitFn: () => booking.mutateAsync(data),
      failedMessage: (error) =>
        t('stockFlow.moveFailed', { item: label, message: stockFlowErrorMessage(error, t) }),
    });
    close(false);
  };

  return (
    <Modal open={open} onOpenChange={close} title={t('stockFlow.moveAssetTitle')}>
      <div className="space-y-4">
        <WithScan
          scan={
            <ObjectScan
              field={t('stockFlow.asset')}
              kind="asset"
              onObject={(object) => setAsset([object])}
            />
          }
        >
          <ObjectPicker
            label={t('stockFlow.asset')}
            kinds={['asset']}
            value={asset}
            onChange={setAsset}
            required
          />
        </WithScan>
        <WithScan
          scan={
            <ScanButton
              field={t('stockFlow.toLocation')}
              accept={['location']}
              onLocation={setTo}
            />
          }
        >
          <LocationPicker
            label={t('stockFlow.toLocation')}
            value={to?.id ?? null}
            onChange={setTo}
          />
        </WithScan>
        <Input
          label={t('stockFlow.reason')}
          value={reason}
          onChange={(event) => setReason(event.target.value)}
        />
        <DialogActions
          onCancel={() => close(false)}
          submitLabel={t('stockFlow.moveAsset')}
          onSubmit={submit}
          disabled={!chosen || !to}
        />
      </div>
    </Modal>
  );
}

type ReserveFor = 'user' | 'project';

/**
 * ReserveDialog reserves an asset for a person or a project until a day
 * (POST /assets/{id}/reserve, AST-06, WP-277): reserved_until and the
 * optional expires_at at the end of the chosen day, assignee_id (a user
 * from the UserTeamPicker) or project_ref, and a reason. Quantity
 * reservations of consumables stay with POST /reservations (WP-759).
 */
export function ReserveDialog({
  open,
  onOpenChange,
  initialAsset,
}: StockFlowDialogProps & { initialAsset?: PickedObject }) {
  const { t } = useTranslation();
  const reserve = useReserveAsset();
  const { scheduleSubmit } = useUndoableSubmit();
  const [asset, setAsset] = useState<PickedObject[]>([]);
  useInitialObject(open, initialAsset, setAsset);
  const [until, setUntil] = useState('');
  const [expires, setExpires] = useState('');
  const [reserveFor, setReserveFor] = useState<ReserveFor>('user');
  const [assignee, setAssignee] = useState<PickedHolder | null>(null);
  const [project, setProject] = useState('');
  const [reason, setReason] = useState('');

  const reset = () => {
    setAsset([]);
    setUntil('');
    setExpires('');
    setReserveFor('user');
    setAssignee(null);
    setProject('');
    setReason('');
  };
  const close = (next: boolean) => {
    if (!next) reset();
    onOpenChange(next);
  };

  const chosen = asset[0];
  const reservedUntil = dueAt(until);
  const target = reserveFor === 'user' ? !!assignee : project.trim() !== '';
  const submit = () => {
    if (!chosen || !reservedUntil || !target) return;
    const label = objectLabel(chosen);
    const data = {
      reserved_until: reservedUntil,
      expires_at: dueAt(expires),
      ...(reserveFor === 'user' && assignee
        ? { assignee_id: assignee.id }
        : { project_ref: project.trim() }),
      reason: reason.trim() || undefined,
    };
    scheduleSubmit({
      id: `reserve:${chosen.id}`,
      message: t('stockFlow.reservePending', { asset: label }),
      submitFn: () => reserve.mutateAsync({ assetId: chosen.id, data }),
      failedMessage: (error) =>
        t('stockFlow.reserveFailed', { asset: label, message: stockFlowErrorMessage(error, t) }),
    });
    close(false);
  };

  return (
    <Modal open={open} onOpenChange={close} title={t('stockFlow.reserveTitle')}>
      <div className="space-y-4">
        <WithScan
          scan={
            <ObjectScan
              field={t('stockFlow.asset')}
              kind="asset"
              onObject={(object) => setAsset([object])}
            />
          }
        >
          <ObjectPicker
            label={t('stockFlow.asset')}
            kinds={['asset']}
            value={asset}
            onChange={setAsset}
            required
          />
        </WithScan>
        <Input
          type="date"
          label={t('stockFlow.reservedUntil')}
          value={until}
          onChange={(event) => setUntil(event.target.value)}
          required
        />
        <Input
          type="date"
          label={t('stockFlow.expiresAt')}
          value={expires}
          onChange={(event) => setExpires(event.target.value)}
        />
        <Select
          label={t('stockFlow.reserveFor')}
          value={reserveFor}
          onChange={(event) => setReserveFor(event.target.value as ReserveFor)}
          options={[
            { value: 'user', label: t('stockFlow.forUser') },
            { value: 'project', label: t('stockFlow.forProject') },
          ]}
        />
        {reserveFor === 'user' ? (
          <UserTeamPicker
            label={t('stockFlow.assignee')}
            kinds={['user']}
            value={assignee}
            onChange={setAssignee}
            required
          />
        ) : (
          <Input
            label={t('stockFlow.projectRef')}
            value={project}
            onChange={(event) => setProject(event.target.value)}
            required
          />
        )}
        <Input
          label={t('stockFlow.reason')}
          value={reason}
          onChange={(event) => setReason(event.target.value)}
        />
        <DialogActions
          onCancel={() => close(false)}
          submitLabel={t('stockFlow.reserve')}
          onSubmit={submit}
          disabled={!chosen || !reservedUntil || !target}
        />
      </div>
    </Modal>
  );
}

export interface ConvertReservationDialogProps extends StockFlowDialogProps {
  /** project is the project_ref of the reservation the dialog converts. */
  project?: string;
  /** onConvert receives the chosen recipient; the caller sends the conversion. */
  onConvert: (holder: PickedHolder) => void;
}

/**
 * ConvertReservationDialog chooses the recipient of a project reservation
 * that becomes an assignment (POST /reservations/{id}/convert, WP-277): a
 * project names no user, so the UserTeamPicker gives a person or a team.
 * A reservation for a user converts without the dialog.
 */
export function ConvertReservationDialog({
  open,
  onOpenChange,
  project,
  onConvert,
}: ConvertReservationDialogProps) {
  const { t } = useTranslation();
  const [holder, setHolder] = useState<PickedHolder | null>(null);
  const close = (next: boolean) => {
    if (!next) setHolder(null);
    onOpenChange(next);
  };
  return (
    <Modal
      open={open}
      onOpenChange={close}
      title={t('stockFlow.convertTitle')}
      description={project ? t('stockFlow.convertDescription', { project }) : undefined}
    >
      <div className="space-y-4">
        <UserTeamPicker
          label={t('stockFlow.recipient')}
          value={holder}
          onChange={setHolder}
          required
        />
        <DialogActions
          onCancel={() => close(false)}
          submitLabel={t('stockFlow.convert')}
          onSubmit={() => {
            if (!holder) return;
            onConvert(holder);
            close(false);
          }}
          disabled={!holder}
        />
      </div>
    </Modal>
  );
}
