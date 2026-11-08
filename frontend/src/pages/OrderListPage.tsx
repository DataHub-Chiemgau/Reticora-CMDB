// UI-14 (WP-812): internal orders with the state machine of WP-286 and
// WP-760. An order is created with a supplier record (supplier_id from GET
// /suppliers), a target bin (LocationPicker, kind bin) and optionally a
// client (default: the client of the scope switcher). Positions, editing and
// deleting exist only in draft. Each state offers its actions: submit,
// approve and reject under the four-eyes rule (order:approve and not the
// requester), place, goods receipt (stock:write, E-106), close and cancel.
// The server decides every request; the page only keeps refused actions out
// of sight and shows the problem detail of a refusal.
import { useState } from 'react';
import type { FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { useQuery } from '@tanstack/react-query';
import { PackageCheck, Plus, ShoppingCart } from 'lucide-react';
import {
  ApiError,
  PROBLEM_ENTITLEMENT_LIMIT,
  PROBLEM_FEATURE_NOT_ENTITLED,
  type FieldErrorInfo,
  type Order,
  type OrderItem,
  type OrderStatus,
  type ReceiveOrderRequest,
  type Supplier,
} from '../api/client';
import { clientApi } from '../api/cmdb';
import {
  useAddOrderItem,
  useCreateOrder,
  useDeleteOrder,
  useEffectivePermissions,
  useOrder,
  useOrderList,
  useOrderTransition,
  useReceiveOrder,
  useSuppliers,
  useUpdateOrder,
  type OrderTransition,
} from '../api/hooks';
import { useEntitlements } from '../hooks/useEntitlements';
import { useScopeStore } from '../stores/scope';
import { LocationPicker } from '../components/pickers/LocationPicker';
import {
  ObjectPicker,
  useObjectKinds,
  type PickedObject,
} from '../components/pickers/ObjectPicker';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { ErrorState } from '../components/ui/ErrorState';
import { PageHeader } from '../components/ui/PageHeader';
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

/** ORDER_STATUSES is the state machine of an order (WP-286, WP-760), labelled orders.statuses.*. */
export const ORDER_STATUSES = [
  'draft',
  'submitted',
  'approved',
  'rejected',
  'ordered',
  'partially_received',
  'received',
  'closed',
  'cancelled',
] as const satisfies readonly OrderStatus[];

/** CANCELLABLE are the states an order can be cancelled from (POST /orders/{id}/cancel). */
const CANCELLABLE: readonly OrderStatus[] = [
  'draft',
  'submitted',
  'approved',
  'ordered',
  'partially_received',
];

export function orderStatusVariant(
  status: string,
): 'neutral' | 'warning' | 'success' | 'danger' | 'info' {
  switch (status) {
    case 'submitted':
    case 'partially_received':
      return 'warning';
    case 'approved':
    case 'received':
      return 'success';
    case 'rejected':
    case 'cancelled':
      return 'danger';
    case 'ordered':
      return 'info';
    default:
      return 'neutral';
  }
}

/** OrderAction is a button of an order row. */
export type OrderAction =
  | 'submit'
  | 'items'
  | 'edit'
  | 'delete'
  | 'approve'
  | 'reject'
  | 'place'
  | 'receive'
  | 'close'
  | 'cancel';

export interface OrderRights {
  /** permissions are the effective rights of GET /me/permissions. */
  permissions: readonly string[];
  /** userId is user_id of GET /me/permissions. */
  userId?: string;
}

/**
 * orderActions are the actions an order offers in its state with the
 * rights of the user. Approve and reject follow the four-eyes rule of
 * WP-286: order:approve, and the requester is known and someone else. The
 * other actions need the right of their route (route_permissions.txt):
 * order:write, the goods receipt stock:write (E-106).
 */
export function orderActions(order: Order, rights: OrderRights): OrderAction[] {
  const can = (permission: string) => rights.permissions.includes(permission);
  const write = can('order:write');
  const actions: OrderAction[] = [];
  switch (order.status) {
    case 'draft':
      if (write) actions.push('submit', 'items', 'edit', 'delete');
      break;
    case 'submitted':
      if (
        can('order:approve') &&
        !!order.requested_by &&
        !!rights.userId &&
        order.requested_by !== rights.userId
      ) {
        actions.push('approve', 'reject');
      }
      break;
    case 'approved':
      if (write) actions.push('place');
      break;
    case 'ordered':
    case 'partially_received':
      if (can('stock:write')) actions.push('receive');
      break;
    case 'received':
      if (write) actions.push('close');
      break;
    default:
      break;
  }
  if (write && CANCELLABLE.includes(order.status)) actions.push('cancel');
  return actions;
}

/**
 * orderErrorMessage words a refused order request: a feature outside the
 * plan or a reached limit as the package message, otherwise the detail of
 * the problem (four-eyes, missing right, invalid transition, over-delivery)
 * with its field violations.
 */
export function orderErrorMessage(error: unknown, t: TFunction): string {
  if (error instanceof ApiError) {
    if (error.status === 403 && error.type === PROBLEM_FEATURE_NOT_ENTITLED) {
      return t('pickers.errors.notEntitled');
    }
    if (error.status === 403 && error.type === PROBLEM_ENTITLEMENT_LIMIT) {
      return t('pickers.errors.limit');
    }
    const detail = error.detail ?? error.message;
    const violations = error.violations.map((v) => `${v.field}: ${v.detail}`);
    return [detail, ...violations].join(' · ');
  }
  return error instanceof Error ? error.message : String(error);
}

function fieldErrorsOf(error: unknown): Record<string, FieldErrorInfo> {
  return error instanceof ApiError ? error.fieldErrors() : {};
}

/** newIdempotencyKey creates the Idempotency-Key of one goods receipt dialog. */
function newIdempotencyKey(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID();
  }
  return `${Date.now().toString(16)}-${Math.random().toString(16).slice(2)}`;
}

type Dialog =
  | { kind: 'create' }
  | { kind: 'edit'; order: Order }
  | { kind: 'items'; order: Order }
  | { kind: 'receive'; order: Order }
  | { kind: 'cancel'; order: Order }
  | { kind: 'delete'; order: Order }
  | null;

export function OrderListPage() {
  const { t } = useTranslation();
  const { entitlements, isEnabled } = useEntitlements();
  const entitled = isEnabled('internal_orders');
  const [status, setStatus] = useState('');
  const [dialog, setDialog] = useState<Dialog>(null);
  const [actionError, setActionError] = useState<string>();

  // Fails closed: no gated request (403) while the entitlements load.
  const list = useOrderList(
    { limit: 100, ...(status ? { status } : {}) },
    { enabled: entitlements !== undefined && entitled },
  );
  const effective = useEffectivePermissions();
  const rights: OrderRights = {
    permissions: effective.data?.permissions ?? [],
    userId: effective.data?.user_id,
  };
  const transition = useOrderTransition();

  if (!entitled) {
    return <ErrorState title={t('app.error')} description={t('pickers.errors.notEntitled')} />;
  }

  const orders = list.data?.data ?? [];
  const canCreate = rights.permissions.includes('order:write');

  const run = (order: Order, action: OrderAction) => {
    setActionError(undefined);
    if (action === 'items' || action === 'edit' || action === 'receive') {
      setDialog({ kind: action, order });
      return;
    }
    if (action === 'cancel' || action === 'delete') {
      setDialog({ kind: action, order });
      return;
    }
    transition.mutate(
      { id: order.id, action },
      { onError: (error) => setActionError(orderErrorMessage(error, t)) },
    );
  };

  return (
    <div className="space-y-4">
      <PageHeader
        title={t('nav.orders')}
        description={t('orders.summary')}
        actions={
          canCreate ? (
            <Button icon={<Plus />} onClick={() => setDialog({ kind: 'create' })}>
              {t('orders.create')}
            </Button>
          ) : null
        }
      />

      <div className="max-w-xs">
        <Select
          label={t('orders.filterStatus')}
          value={status}
          onChange={(event) => setStatus(event.target.value)}
          options={[
            { value: '', label: t('common.allStatuses') },
            ...ORDER_STATUSES.map((value) => ({
              value,
              label: t(`orders.statuses.${value}`),
            })),
          ]}
        />
      </div>

      {actionError ? (
        <p
          role="alert"
          className="rounded-lg border border-line bg-panel px-4 py-3 text-sm text-danger"
        >
          {actionError}
        </p>
      ) : null}

      {list.isLoading ? <SkeletonList rows={5} label={t('app.loading')} /> : null}
      {list.error ? (
        <ErrorState
          title={t('app.error')}
          description={orderErrorMessage(list.error, t)}
          retryLabel={t('common.retry')}
          onRetry={() => void list.refetch()}
        />
      ) : null}

      {list.data ? (
        <Card className="p-0 overflow-hidden">
          <ScrollRegion label={t('nav.orders')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('orders.number')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('common.title')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('orders.supplier')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('orders.positions')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ci.status')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('common.actions')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {orders.map((o) => (
                  <tr key={o.id} className={trClasses}>
                    <td className={tdMonoClasses}>{o.order_number}</td>
                    <td className={tdPrimaryClasses}>{o.title}</td>
                    <td className={tdClasses}>{o.supplier_name || '—'}</td>
                    <td className={tdNumericClasses}>{o.items?.length ?? 0}</td>
                    <td className={tdClasses}>
                      <Badge variant={orderStatusVariant(o.status)}>
                        {t(`orders.statuses.${o.status}`, o.status)}
                      </Badge>
                    </td>
                    <td className={cn(tdClasses, 'text-right')}>
                      <div className="flex flex-wrap justify-end gap-1">
                        {orderActions(o, rights).map((action) => (
                          <Button
                            key={action}
                            size="sm"
                            variant={
                              action === 'cancel' || action === 'delete' || action === 'reject'
                                ? 'ghost'
                                : 'secondary'
                            }
                            disabled={transition.isPending}
                            aria-label={`${t(`orders.actions.${action}`)}: ${o.order_number}`}
                            onClick={() => run(o, action)}
                          >
                            {t(`orders.actions.${action}`)}
                          </Button>
                        ))}
                      </div>
                    </td>
                  </tr>
                ))}
                {orders.length === 0 ? (
                  <TableEmptyRow
                    colSpan={6}
                    icon={<ShoppingCart />}
                    title={t('orders.emptyTitle')}
                    description={t('orders.emptyHint')}
                  />
                ) : null}
              </tbody>
            </table>
          </ScrollRegion>
        </Card>
      ) : null}

      {dialog?.kind === 'create' || dialog?.kind === 'edit' ? (
        <OrderFormDialog
          order={dialog.kind === 'edit' ? dialog.order : undefined}
          onClose={() => setDialog(null)}
        />
      ) : null}
      {dialog?.kind === 'items' ? (
        <OrderItemsDialog orderId={dialog.order.id} onClose={() => setDialog(null)} />
      ) : null}
      {dialog?.kind === 'receive' ? (
        <ReceiveDialog orderId={dialog.order.id} onClose={() => setDialog(null)} />
      ) : null}
      {dialog?.kind === 'cancel' || dialog?.kind === 'delete' ? (
        <ConfirmOrderDialog
          kind={dialog.kind}
          order={dialog.order}
          onClose={() => setDialog(null)}
          onError={setActionError}
        />
      ) : null}
    </div>
  );
}

/**
 * supplierChoices are the suppliers an order of the client may name:
 * org-wide ones and those of the client (another client's supplier is 422).
 */
export function supplierChoices(suppliers: readonly Supplier[], clientId: string): Supplier[] {
  return suppliers.filter((s) => s.client_id === null || (!!clientId && s.client_id === clientId));
}

function OrderFormDialog({ order, onClose }: { order?: Order; onClose: () => void }) {
  const { t } = useTranslation();
  const scopeClient = useScopeStore((s) => s.clientId);
  const create = useCreateOrder();
  const update = useUpdateOrder();
  const mutation = order ? update : create;
  const [title, setTitle] = useState(order?.title ?? '');
  const [supplierId, setSupplierId] = useState(order?.supplier_id ?? '');
  const [bin, setBin] = useState<string | null>(order?.target_location_id ?? null);
  const [clientId, setClientId] = useState(order ? (order.client_id ?? '') : scopeClient);
  const suppliers = useSuppliers({ limit: 200 });
  const clients = useQuery({
    queryKey: ['scope', 'clients'],
    queryFn: () => clientApi.list(),
    retry: false,
    staleTime: 60_000,
  });
  const fields = fieldErrorsOf(mutation.error);
  const choices = supplierChoices(suppliers.data?.data ?? [], clientId);
  const clientOptions = clients.data?.data ?? [];

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const done = { onSuccess: onClose };
    if (order) {
      update.mutate(
        {
          id: order.id,
          data: {
            title,
            supplier_id: supplierId,
            target_location_id: bin ?? '',
            ...(clientId ? { client_id: clientId } : {}),
          },
        },
        done,
      );
      return;
    }
    create.mutate(
      {
        title,
        ...(supplierId ? { supplier_id: supplierId } : {}),
        ...(bin ? { target_location_id: bin } : {}),
        ...(clientId ? { client_id: clientId } : {}),
      },
      done,
    );
  };

  return (
    <Modal
      open
      onOpenChange={(open) => !open && onClose()}
      title={order ? t('orders.editTitle') : t('orders.create')}
    >
      <form className="space-y-4" onSubmit={submit}>
        <Input
          label={t('common.title')}
          value={title}
          required
          onChange={(event) => setTitle(event.target.value)}
          error={fields.title?.message}
        />
        {clientOptions.length > 0 || clientId ? (
          <Select
            label={t('orders.client')}
            value={clientId}
            onChange={(event) => setClientId(event.target.value)}
            error={fields.client_id?.message}
            options={[
              { value: '', label: t('orders.orgWide') },
              ...clientOptions.map((c) => ({ value: c.id, label: c.name })),
              ...(clientId && !clientOptions.some((c) => c.id === clientId)
                ? [{ value: clientId, label: clientId }]
                : []),
            ]}
          />
        ) : null}
        <Select
          label={t('orders.supplier')}
          value={supplierId}
          onChange={(event) => setSupplierId(event.target.value)}
          error={
            fields.supplier_id?.message ??
            (suppliers.error ? orderErrorMessage(suppliers.error, t) : undefined)
          }
          options={[
            { value: '', label: t('orders.noSupplier') },
            ...choices.map((s) => ({ value: s.id, label: s.name })),
            ...(supplierId && !choices.some((s) => s.id === supplierId)
              ? [{ value: supplierId, label: order?.supplier_name ?? supplierId }]
              : []),
          ]}
        />
        <LocationPicker
          label={t('orders.targetBin')}
          value={bin}
          onChange={(picked) => setBin(picked?.id ?? null)}
          kinds={['bin']}
        />
        {fields.target_location_id ? (
          <p role="alert" className="text-sm text-danger">
            {fields.target_location_id.message}
          </p>
        ) : null}
        {mutation.error && Object.keys(fields).length === 0 ? (
          <p role="alert" className="text-sm text-danger">
            {orderErrorMessage(mutation.error, t)}
          </p>
        ) : null}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" disabled={!title.trim()} loading={mutation.isPending}>
            {order ? t('common.save') : t('common.create')}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

type PositionKind = 'consumable' | 'asset';

function OrderItemsDialog({ orderId, onClose }: { orderId: string; onClose: () => void }) {
  const { t } = useTranslation();
  const order = useOrder(orderId);
  const add = useAddOrderItem();
  const consumablesOffered = useObjectKinds(['consumable']).length > 0;
  const [kind, setKind] = useState<PositionKind>(consumablesOffered ? 'consumable' : 'asset');
  const [consumable, setConsumable] = useState<PickedObject[]>([]);
  const [description, setDescription] = useState('');
  const [quantity, setQuantity] = useState('1');
  const [unitPrice, setUnitPrice] = useState('');
  const fields = fieldErrorsOf(add.error);
  const items = order.data?.items ?? [];
  const draft = order.data?.status === 'draft';

  const qty = Number(quantity);
  const price = unitPrice.trim() === '' ? undefined : Number(unitPrice);
  const chosen = consumable[0];
  const valid =
    draft &&
    Number.isInteger(qty) &&
    qty > 0 &&
    (price === undefined || (Number.isFinite(price) && price >= 0)) &&
    (kind === 'consumable' ? !!chosen : description.trim() !== '');

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!valid) return;
    add.mutate(
      {
        id: orderId,
        data: {
          description: kind === 'consumable' && chosen ? chosen.name : description.trim(),
          quantity: qty,
          ...(price !== undefined ? { unit_price: price } : {}),
          ...(kind === 'consumable' && chosen ? { consumable_id: chosen.id } : {}),
        },
      },
      {
        onSuccess: () => {
          setConsumable([]);
          setDescription('');
          setQuantity('1');
          setUnitPrice('');
        },
      },
    );
  };

  return (
    <Modal
      open
      onOpenChange={(open) => !open && onClose()}
      size="lg"
      title={t('orders.items.title', { number: order.data?.order_number ?? '' })}
    >
      <div className="space-y-4">
        {order.isLoading ? <SkeletonList rows={3} label={t('app.loading')} /> : null}
        {order.error ? (
          <p role="alert" className="text-sm text-danger">
            {orderErrorMessage(order.error, t)}
          </p>
        ) : null}
        {order.data ? <ItemTable items={items} /> : null}
        {draft ? (
          <form className="space-y-3" onSubmit={submit} aria-label={t('orders.items.add')}>
            <Select
              label={t('orders.items.kind')}
              value={kind}
              onChange={(event) => setKind(event.target.value as PositionKind)}
              options={[
                ...(consumablesOffered
                  ? [{ value: 'consumable', label: t('orders.items.kinds.consumable') }]
                  : []),
                { value: 'asset', label: t('orders.items.kinds.asset') },
              ]}
            />
            {kind === 'consumable' ? (
              <ObjectPicker
                label={t('orders.items.consumable')}
                kinds={['consumable']}
                value={consumable}
                onChange={setConsumable}
                required
              />
            ) : (
              <Input
                label={t('common.description')}
                value={description}
                required
                onChange={(event) => setDescription(event.target.value)}
                error={fields.description?.message}
              />
            )}
            <div className="grid gap-3 sm:grid-cols-2">
              <Input
                label={t('orders.items.quantity')}
                type="number"
                min={1}
                step={1}
                value={quantity}
                required
                onChange={(event) => setQuantity(event.target.value)}
                error={fields.quantity?.message}
              />
              <Input
                label={t('orders.items.unitPrice')}
                type="number"
                min={0}
                step="0.01"
                value={unitPrice}
                onChange={(event) => setUnitPrice(event.target.value)}
                error={fields.unit_price?.message}
              />
            </div>
            {fields.consumable_id ? (
              <p role="alert" className="text-sm text-danger">
                {fields.consumable_id.message}
              </p>
            ) : null}
            {add.error && Object.keys(fields).length === 0 ? (
              <p role="alert" className="text-sm text-danger">
                {orderErrorMessage(add.error, t)}
              </p>
            ) : null}
            <div className="flex justify-end gap-2">
              <Button type="button" variant="secondary" onClick={onClose}>
                {t('common.done')}
              </Button>
              <Button type="submit" icon={<Plus />} disabled={!valid} loading={add.isPending}>
                {t('orders.items.add')}
              </Button>
            </div>
          </form>
        ) : (
          <div className="flex justify-end">
            <Button variant="secondary" onClick={onClose}>
              {t('common.done')}
            </Button>
          </div>
        )}
      </div>
    </Modal>
  );
}

function ItemTable({ items }: { items: OrderItem[] }) {
  const { t } = useTranslation();
  if (items.length === 0) return <p className="text-sm text-fg-muted">{t('orders.items.none')}</p>;
  return (
    <ScrollRegion label={t('orders.positions')}>
      <table className={tableClasses}>
        <thead className={theadClasses}>
          <tr>
            <th scope="col" className={thClasses}>
              {t('orders.items.kind')}
            </th>
            <th scope="col" className={thClasses}>
              {t('common.description')}
            </th>
            <th scope="col" className={cn(thClasses, 'text-right')}>
              {t('orders.items.quantity')}
            </th>
            <th scope="col" className={cn(thClasses, 'text-right')}>
              {t('orders.items.received')}
            </th>
            <th scope="col" className={cn(thClasses, 'text-right')}>
              {t('orders.items.unitPrice')}
            </th>
          </tr>
        </thead>
        <tbody>
          {items.map((item) => (
            <tr key={item.id} className={trClasses}>
              <td className={tdClasses}>
                {t(`orders.items.kinds.${item.consumable_id ? 'consumable' : 'asset'}`)}
              </td>
              <td className={tdPrimaryClasses}>{item.description}</td>
              <td className={tdNumericClasses}>{item.quantity}</td>
              <td className={tdNumericClasses}>{item.received_quantity ?? 0}</td>
              <td className={tdNumericClasses}>{item.unit_price ?? '—'}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </ScrollRegion>
  );
}

interface ReceiptLine {
  quantity: string;
  location: string | null;
  assets: PickedObject[];
}

/** remainingOf is the open quantity of a position: quantity − received_quantity. */
export function remainingOf(item: OrderItem): number {
  return Math.max(item.quantity - (item.received_quantity ?? 0), 0);
}

function ReceiveDialog({ orderId, onClose }: { orderId: string; onClose: () => void }) {
  const { t } = useTranslation();
  const order = useOrder(orderId);
  const receive = useReceiveOrder();
  // One key per dialog: a repeated send of this receipt is not booked twice.
  const [idempotencyKey] = useState(newIdempotencyKey);
  const [lines, setLines] = useState<Record<string, ReceiptLine>>({});
  const open = (order.data?.items ?? []).filter((item) => remainingOf(item) > 0);
  const target = order.data?.target_location_id ?? null;

  const lineOf = (item: OrderItem): ReceiptLine =>
    lines[item.id] ?? {
      quantity: item.consumable_id ? String(remainingOf(item)) : '0',
      location: target,
      assets: [],
    };
  const change = (item: OrderItem, patch: Partial<ReceiptLine>) =>
    setLines((current) => ({ ...current, [item.id]: { ...lineOf(item), ...patch } }));

  const problemOf = (item: OrderItem): string | undefined => {
    const line = lineOf(item);
    const qty = Number(line.quantity);
    if (!Number.isInteger(qty) || qty < 0) return t('orders.receive.invalidQuantity');
    if (qty > remainingOf(item)) {
      return t('orders.receive.tooMany', { count: remainingOf(item) });
    }
    if (qty === 0) return undefined;
    if (!line.location) return t('orders.receive.binRequired');
    if (!item.consumable_id && line.assets.length !== qty) {
      return t('orders.receive.assetsMismatch', { count: qty });
    }
    return undefined;
  };

  const booked = open.filter((item) => Number(lineOf(item).quantity) > 0);
  const valid = booked.length > 0 && open.every((item) => !problemOf(item));

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!valid) return;
    const data: ReceiveOrderRequest = {
      items: booked.map((item) => {
        const line = lineOf(item);
        return {
          item_id: item.id,
          quantity: Number(line.quantity),
          ...(line.location ? { location_id: line.location } : {}),
          ...(item.consumable_id ? {} : { asset_ids: line.assets.map((a) => a.id) }),
        };
      }),
    };
    receive.mutate({ id: orderId, data, key: idempotencyKey }, { onSuccess: onClose });
  };

  return (
    <Modal
      open
      onOpenChange={(isOpen) => !isOpen && onClose()}
      size="lg"
      title={t('orders.receive.title', { number: order.data?.order_number ?? '' })}
    >
      <form className="space-y-4" onSubmit={submit}>
        {order.isLoading ? <SkeletonList rows={3} label={t('app.loading')} /> : null}
        {order.error ? (
          <p role="alert" className="text-sm text-danger">
            {orderErrorMessage(order.error, t)}
          </p>
        ) : null}
        {order.data && open.length === 0 ? (
          <p className="text-sm text-fg-muted">{t('orders.receive.nothingOpen')}</p>
        ) : null}
        {open.map((item) => {
          const line = lineOf(item);
          const problem = problemOf(item);
          const qty = Number(line.quantity);
          return (
            <fieldset key={item.id} className="space-y-3 rounded-lg border border-line p-3">
              <legend className="px-1 text-sm font-medium text-fg">
                {item.description} · {t('orders.receive.remaining', { count: remainingOf(item) })}
              </legend>
              <Input
                label={t('orders.receive.quantity', { name: item.description })}
                type="number"
                min={0}
                max={remainingOf(item)}
                step={1}
                value={line.quantity}
                onChange={(event) => change(item, { quantity: event.target.value })}
                error={problem}
              />
              <LocationPicker
                label={t('orders.receive.bin', { name: item.description })}
                value={line.location}
                onChange={(picked) => change(item, { location: picked?.id ?? null })}
                kinds={['bin']}
              />
              {!item.consumable_id && qty > 0 ? (
                <ObjectPicker
                  label={t('orders.receive.assets', { name: item.description })}
                  kinds={['asset']}
                  multiple
                  value={line.assets}
                  onChange={(assets) => change(item, { assets })}
                />
              ) : null}
            </fieldset>
          );
        })}
        {receive.error ? (
          <p role="alert" className="text-sm text-danger">
            {orderErrorMessage(receive.error, t)}
          </p>
        ) : null}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button
            type="submit"
            icon={<PackageCheck />}
            disabled={!valid}
            loading={receive.isPending}
          >
            {t('orders.receive.submit')}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function ConfirmOrderDialog({
  kind,
  order,
  onClose,
  onError,
}: {
  kind: 'cancel' | 'delete';
  order: Order;
  onClose: () => void;
  onError: (message: string) => void;
}) {
  const { t } = useTranslation();
  const transition = useOrderTransition();
  const remove = useDeleteOrder();
  const busy = transition.isPending || remove.isPending;
  const fail = (error: unknown) => {
    onError(orderErrorMessage(error, t));
    onClose();
  };
  const confirm = () => {
    if (kind === 'delete') {
      remove.mutate(order.id, { onSuccess: onClose, onError: fail });
      return;
    }
    const action: OrderTransition = 'cancel';
    transition.mutate({ id: order.id, action }, { onSuccess: onClose, onError: fail });
  };
  return (
    <Modal
      open
      onOpenChange={(open) => !open && onClose()}
      title={t(`orders.confirm.${kind}Title`, { number: order.order_number })}
    >
      <div className="space-y-4">
        <p className="text-sm text-fg">{t(`orders.confirm.${kind}Body`, { title: order.title })}</p>
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button variant="danger" loading={busy} onClick={confirm}>
            {t(`orders.actions.${kind}`)}
          </Button>
        </div>
      </div>
    </Modal>
  );
}
