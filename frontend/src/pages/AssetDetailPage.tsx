/**
 * AssetDetailPage — the detail page of an asset (UI-14, AST-04, WP-809).
 *
 * The master data with version, holder and active reservation come from
 * GET /assets/{id} (useAsset); the CIs, the child CIs (child_mode) and the
 * documents from exactly one BFF query asset(id) (useAssetDetail). A list
 * the server refuses (FORBIDDEN) or leaves out (UNAVAILABLE, module off)
 * hides only its section. Every write goes through REST: PATCH /assets/{id}
 * with If-Match, the CI link (POST/DELETE /assets/{id}/cis), the lifecycle
 * transition, the assignment and stock flow dialogs of WP-318 and WP-806.
 * location_id and lifecycle_state are read-only here: they change through
 * movements and the lifecycle transition.
 */
import { ChevronRight, Link2, Pencil, RotateCw, Unlink } from 'lucide-react';
import { useMemo, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import type { TFunction } from 'i18next';
import { Link, useParams } from 'react-router-dom';
import { useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import {
  API_MAX_LIMIT,
  ApiError,
  type Asset,
  type AssetUpdateRequest,
  type BffFieldError,
  type Document,
  type FieldErrorInfo,
} from '../api/client';
import {
  assetDetailQuery,
  useAsset,
  useAssetDetail,
  useContract,
  useContracts,
  useHasPermission,
  useLinkAssetCI,
  useSupplier,
  useSuppliers,
  useTeam,
  useUnlinkAssetCI,
  useUpdateAsset,
  useUser,
  type AssetDetailCI,
  type AssetDetailDocument,
} from '../api/hooks';
import { useFieldProvenance, useReleaseReservation } from '../api/cmdbHooks';
import {
  DetailRow,
  LifecycleSection,
  formatDate,
  useLocationNames,
} from '../components/cmdb/CIDetailSections';
import { ObjectHistory } from '../components/cmdb/ObjectHistory';
import { ProvenanceTooltip } from '../components/cmdb/ProvenanceTooltip';
import { fieldValueText } from '../components/cmdb/fieldValue';
import { LinkedDocuments } from '../components/documents/LinkedDocuments';
import {
  AssignDialog,
  ReturnDialog,
  TransferDialog,
  type HeldAsset,
} from '../components/flows/AssignDialogs';
import {
  MoveAssetDialog,
  ReserveDialog,
  useStockFlowGates,
} from '../components/flows/StockFlowDialogs';
import { ObjectPicker, type PickedObject } from '../components/pickers/ObjectPicker';
import { UserTeamPicker, type PickedHolder } from '../components/pickers/UserTeamPicker';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { Textarea } from '../components/ui/Textarea';
import { ScopeBanner } from '../components/ScopeBanner';
import { ASSET_CATEGORIES, lifecycleVariant } from './AssetListPage';

/** CHILD_MODES are the child contracts of a child CI (AST-05), labelled under asset.childModes. */
export const CHILD_MODES = ['configuration', 'standalone'] as const;

/** The problem type of a 409 version conflict (API-07). */
const PROBLEM_CONFLICT = 'urn:reticora:problem:conflict';

/** The days before the end of the warranty from which the banner shows (stages of WP-270). */
export const WARRANTY_NOTICE_DAYS = 30;
export const WARRANTY_WARNING_DAYS = 7;

export type WarrantyLevel = 'notice' | 'warning' | 'expired';

const DAY_MS = 24 * 60 * 60 * 1000;

/**
 * warrantyLevel derives the stage of the warranty banner from
 * warranty_until (a calendar day, valid through that day): from 30 days
 * before the end a notice, from 7 days a warning, after the day expired.
 */
export function warrantyLevel(
  until: string | undefined,
  now: Date = new Date(),
): { level: WarrantyLevel; days: number } | undefined {
  if (!until || !/^\d{4}-\d{2}-\d{2}/.test(until)) return undefined;
  const end = Date.UTC(
    Number(until.slice(0, 4)),
    Number(until.slice(5, 7)) - 1,
    Number(until.slice(8, 10)),
  );
  const today = Date.UTC(now.getFullYear(), now.getMonth(), now.getDate());
  const days = Math.round((end - today) / DAY_MS);
  if (days < 0) return { level: 'expired', days };
  if (days <= WARRANTY_WARNING_DAYS) return { level: 'warning', days };
  if (days <= WARRANTY_NOTICE_DAYS) return { level: 'notice', days };
  return undefined;
}

/** noticeDate is the last day to give notice: end_date minus notice_period_days. */
export function noticeDate(endDate?: string | null, days?: number | null): string | undefined {
  if (!endDate || days === undefined || days === null) return undefined;
  const end = new Date(`${endDate.slice(0, 10)}T00:00:00Z`);
  if (Number.isNaN(end.getTime())) return undefined;
  end.setUTCDate(end.getUTCDate() - days);
  return end.toISOString().slice(0, 10);
}

/** sectionError is the GraphQL error of a list field of the aggregate (path asset.<field>). */
function sectionError(errors: BffFieldError[], field: string): BffFieldError | undefined {
  return errors.find((error) => error.path[0] === 'asset' && error.path[1] === field);
}

/** hidden tells whether the server refused the field or switched its module off. */
function hidden(error: BffFieldError | undefined): boolean {
  return error?.code === 'FORBIDDEN' || error?.code === 'UNAVAILABLE';
}

/** documentOf maps a document of the aggregate onto the REST shape LinkedDocuments shows. */
function documentOf(doc: AssetDetailDocument): Document {
  return {
    id: doc.id,
    title: doc.title,
    description: doc.description ?? undefined,
    file_name: doc.fileName,
    file_size: doc.fileSize,
    mime_type: doc.mimeType,
    version: doc.version,
    category: doc.category,
    tags: doc.tags,
    uploaded_by: doc.uploadedBy ?? '',
    scan_status: (doc.scanStatus ?? undefined) as Document['scan_status'],
    created_at: doc.createdAt,
    updated_at: doc.updatedAt,
  } as Document;
}

export function AssetDetailPage() {
  const { id = '' } = useParams<{ id: string }>();
  const { t } = useTranslation();
  const asset = useAsset(id);

  if (asset.isLoading) return <SkeletonList rows={6} label={t('app.loading')} />;
  if (asset.error || !asset.data) {
    const notFound = asset.error instanceof ApiError && asset.error.status === 404;
    return (
      <div className="space-y-4">
        <PageHeader
          title={t('asset.title')}
          breadcrumbs={[{ label: t('nav.assets'), to: '/assets' }]}
        />
        {notFound ? (
          <EmptyState title={t('asset.detail.notFound')} />
        ) : (
          <ErrorState
            title={t('asset.detail.loadError')}
            retryLabel={t('common.retry')}
            onRetry={() => void asset.refetch()}
          />
        )}
      </div>
    );
  }
  return <AssetDetail asset={asset.data} onReload={() => void asset.refetch()} />;
}

function AssetDetail({ asset, onReload }: { asset: Asset; onReload: () => void }) {
  const { t } = useTranslation();
  const detail = useAssetDetail(asset.id);
  const [editing, setEditing] = useState(false);
  const [dialog, setDialog] = useState<
    'assign' | 'return' | 'transfer' | 'reserve' | 'move' | null
  >(null);
  const mayWrite = useHasPermission('asset:write') === true;
  const picked = useMemo<PickedObject>(
    () => ({ kind: 'asset', id: asset.id, name: asset.name, code: asset.inventory_no }),
    [asset.id, asset.name, asset.inventory_no],
  );
  const holder = useHolderLabel(asset);
  const held: HeldAsset | null =
    holder && (asset.assigned_user_id || asset.assigned_team_id)
      ? {
          assetId: asset.id,
          assetLabel: asset.inventory_no ? `${asset.name} (${asset.inventory_no})` : asset.name,
          holder: (asset.assigned_user_id || asset.assigned_team_id) ?? '',
          holderLabel: holder,
        }
      : null;
  const open = (kind: typeof dialog) => () => setDialog(kind);
  const onOpenChange = (next: boolean) => {
    if (!next) setDialog(null);
  };

  const errors = detail.data?.errors ?? [];
  const aggregate = detail.data?.data?.asset ?? null;
  const lifecycle = asset.lifecycle_state;

  return (
    <div className="space-y-4">
      <PageHeader
        title={asset.name}
        breadcrumbs={[{ label: t('nav.assets'), to: '/assets' }, { label: asset.name }]}
        meta={
          <span className="flex flex-wrap items-center gap-1.5">
            {asset.inventory_no ? <Badge variant="neutral">{asset.inventory_no}</Badge> : null}
            {lifecycle ? (
              <Badge variant={lifecycleVariant(lifecycle)}>
                {t(`lifecycle.states.${lifecycle}`, lifecycle)}
              </Badge>
            ) : null}
          </span>
        }
        actions={
          mayWrite ? (
            <Button icon={<Pencil />} onClick={() => setEditing(true)}>
              {t('common.edit')}
            </Button>
          ) : undefined
        }
      />
      <ScopeBanner object={asset} />
      <WarrantyBanner until={asset.warranty_until} />

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-[minmax(0,1fr)_24rem]">
        <div className="min-w-0 space-y-4">
          <MasterDataCard asset={asset} />
          {hidden(sectionError(errors, 'cis')) ? null : (
            <CIsCard
              asset={asset}
              cis={aggregate?.cis ?? []}
              loading={detail.isLoading}
              error={detail.error ?? sectionError(errors, 'cis')}
            />
          )}
          {hidden(sectionError(errors, 'children')) ? null : (
            <ChildrenCard
              items={aggregate?.children ?? []}
              loading={detail.isLoading}
              error={detail.error ?? sectionError(errors, 'children')}
            />
          )}
          {detail.data && !detail.error && !hidden(sectionError(errors, 'documents')) ? (
            <AssetDocuments assetId={asset.id} />
          ) : null}
        </div>
        <div className="min-w-0 space-y-4">
          <ContractCard asset={asset} />
          <HolderCard
            asset={asset}
            holder={holder}
            onAssign={open('assign')}
            onReturn={open('return')}
            onTransfer={open('transfer')}
            onReserve={open('reserve')}
            onMove={open('move')}
          />
        </div>
      </div>

      <div className="space-y-2">
        <LifecycleSection
          entityType="assets"
          entityId={asset.id}
          currentState={asset.lifecycle_state}
          onAssign={open('assign')}
          onReserve={open('reserve')}
        />
      </div>
      <Card title={t('asset.detail.history')}>
        <ObjectHistory entityType="asset" id={asset.id} />
      </Card>

      <EditAssetModal open={editing} onOpenChange={setEditing} asset={asset} onReload={onReload} />
      <AssignDialog open={dialog === 'assign'} onOpenChange={onOpenChange} initialAsset={picked} />
      <ReturnDialog
        open={dialog === 'return'}
        onOpenChange={onOpenChange}
        target={dialog === 'return' ? held : null}
      />
      <TransferDialog
        open={dialog === 'transfer'}
        onOpenChange={onOpenChange}
        target={dialog === 'transfer' ? held : null}
      />
      <ReserveDialog
        open={dialog === 'reserve'}
        onOpenChange={onOpenChange}
        initialAsset={picked}
      />
      <MoveAssetDialog open={dialog === 'move'} onOpenChange={onOpenChange} initialAsset={picked} />
    </div>
  );
}

// ─── Warranty (WP-270 stages) ────────────────────────────────────────────────

function WarrantyBanner({ until }: { until?: string }) {
  const { t, i18n } = useTranslation();
  const stage = warrantyLevel(until);
  if (!stage) return null;
  const date = formatDate(until, i18n.language);
  const tone =
    stage.level === 'notice'
      ? 'border-blue-600/30 bg-blue-50 text-blue-900 dark:bg-blue-400/10 dark:text-blue-100'
      : stage.level === 'warning'
        ? 'border-amber-600/30 bg-amber-50 text-amber-900 dark:bg-amber-400/10 dark:text-amber-100'
        : 'border-red-600/30 bg-red-50 text-red-900 dark:bg-red-400/10 dark:text-red-100';
  return (
    <div
      role={stage.level === 'notice' ? 'status' : 'alert'}
      data-warranty={stage.level}
      className={`rounded-xl border px-4 py-3 text-sm ${tone}`}
    >
      {t(`asset.detail.warranty.${stage.level}`, { date, count: Math.abs(stage.days) })}
    </div>
  );
}

// ─── Master data ─────────────────────────────────────────────────────────────

/** The master data fields in their order; a field absent from the answer is not shown. */
const MASTER_FIELDS = [
  'inventory_no',
  'category',
  'manufacturer',
  'model',
  'serial_number',
  'barcode',
  'rfid_tag',
  'purchase_date',
  'purchase_price',
  'currency',
  'warranty_until',
  'cost_center',
  'invoice_number',
] as const;

const DATE_FIELDS = new Set<string>(['purchase_date', 'warranty_until']);
const MONO_FIELDS = new Set<string>(['inventory_no', 'serial_number', 'barcode', 'rfid_tag']);

function MasterDataCard({ asset }: { asset: Asset }) {
  const { t, i18n } = useTranslation();
  const locations = useLocationNames(!!asset.location_id);
  const fields = asset as unknown as Record<string, unknown>;
  const present = (field: string) =>
    field in fields && fields[field] !== undefined && fields[field] !== null;
  const value = (field: string): string | undefined => {
    const raw = fields[field];
    if (raw === undefined || raw === null || raw === '') return undefined;
    if (field === 'category') return t(`asset.categories.${String(raw)}`, String(raw));
    if (DATE_FIELDS.has(field)) return formatDate(String(raw), i18n.language);
    return String(raw);
  };
  return (
    <Card title={t('asset.detail.masterData')}>
      <dl>
        {MASTER_FIELDS.filter(present).map((field) => (
          <DetailRow
            key={field}
            label={t(`asset.columns.${field}`)}
            value={value(field)}
            mono={MONO_FIELDS.has(field)}
          />
        ))}
        {present('location_id') ? (
          <DetailRow
            label={t('asset.detail.locationReadOnly')}
            value={locations.path(asset.location_id)}
          />
        ) : null}
        {present('lifecycle_state') ? (
          <DetailRow
            label={t('asset.detail.lifecycleReadOnly')}
            value={t(`lifecycle.states.${asset.lifecycle_state}`, asset.lifecycle_state ?? '')}
          />
        ) : null}
        {present('owner_user_id') || present('owner_team_id') ? <OwnerRow asset={asset} /> : null}
        {present('notes') ? (
          <DetailRow label={t('asset.detail.notes')} value={asset.notes} />
        ) : null}
      </dl>
    </Card>
  );
}

/** OwnerRow shows the owner of the asset by name (GET /users/{id}, /teams/{id}); without user:read the kind. */
function OwnerRow({ asset }: { asset: Asset }) {
  const { t } = useTranslation();
  const mayRead = useHasPermission('user:read') === true;
  const user = useUser(asset.owner_user_id, { enabled: mayRead });
  const team = useTeam(asset.owner_team_id, { enabled: mayRead });
  const parts: string[] = [];
  const named = (kind: string, name?: string) =>
    name ? t('ci.owner.named', { kind, name }) : kind;
  if (asset.owner_user_id) {
    const name = user.data?.id === asset.owner_user_id ? user.data.display_name : undefined;
    parts.push(named(t('ci.owner.user'), name));
  }
  if (asset.owner_team_id) {
    const name = team.data?.id === asset.owner_team_id ? team.data.name : undefined;
    parts.push(named(t('ci.owner.team'), name));
  }
  return <DetailRow label={t('ci.owner.title')} value={parts.join(' · ') || undefined} />;
}

/** The editable fields of the edit dialog; location and lifecycle change elsewhere. */
const EDIT_FIELDS = [
  'name',
  'inventory_no',
  'manufacturer',
  'model',
  'serial_number',
  'barcode',
  'rfid_tag',
  'purchase_date',
  'purchase_price',
  'currency',
  'warranty_until',
  'cost_center',
  'invoice_number',
] as const;
type EditField =
  (typeof EDIT_FIELDS)[number] | 'category' | 'notes' | 'supplier_id' | 'contract_id';

const INPUT_TYPES: Partial<Record<EditField, string>> = {
  purchase_date: 'date',
  warranty_until: 'date',
  purchase_price: 'number',
};

const ALL_EDIT_FIELDS: readonly EditField[] = [
  ...EDIT_FIELDS,
  'category',
  'notes',
  'supplier_id',
  'contract_id',
];

function initialValues(asset: Asset): Record<EditField, string> {
  const fields = asset as unknown as Record<string, unknown>;
  return Object.fromEntries(
    ALL_EDIT_FIELDS.map((field) => {
      const raw = fields[field];
      return [field, raw === undefined || raw === null ? '' : String(raw)];
    }),
  ) as Record<EditField, string>;
}

function initialOwner(asset: Asset): PickedHolder | null {
  if (asset.owner_user_id) return { kind: 'user', id: asset.owner_user_id, name: '' };
  if (asset.owner_team_id) return { kind: 'team', id: asset.owner_team_id, name: '' };
  return null;
}

/** conflictMessage words a 409 version conflict or a 412 of the PATCH. */
function conflictMessage(error: unknown, t: TFunction): string | undefined {
  if (!(error instanceof ApiError)) return undefined;
  if (error.status === 412) return t('asset.detail.edit.precondition');
  if (
    error.status === 409 &&
    (error.type === PROBLEM_CONFLICT || error.currentVersion !== undefined)
  ) {
    return t('asset.detail.edit.conflict', {
      fields: error.conflictFields.length > 0 ? error.conflictFields.join(', ') : '—',
    });
  }
  return undefined;
}

/**
 * EditAssetModal changes the master data with PATCH /assets/{id} and
 * If-Match of the shown version (API-07). Only changed fields are sent; an
 * emptied field is sent empty and cleared. A 409 version conflict and a 412
 * show the conflict with the action "Reload"; a 403 with fields marks the
 * refused inputs, a 409 or 422 with field appears at its input. Supplier
 * and contract are offered when org-wide or of the asset's client, the
 * owner is chosen with the UserTeamPicker.
 */
function EditAssetModal({
  open,
  onOpenChange,
  asset,
  onReload,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  asset: Asset;
  onReload: () => void;
}) {
  const { t } = useTranslation();
  const update = useUpdateAsset();
  const [values, setValues] = useState(() => initialValues(asset));
  const [owner, setOwner] = useState<PickedHolder | null>(() => initialOwner(asset));
  const [fieldErrors, setFieldErrors] = useState<Record<string, FieldErrorInfo>>({});
  const [failure, setFailure] = useState('');
  const [conflict, setConflict] = useState('');
  // One page of the largest size (API-02); the selection keeps the entries
  // that are org-wide or of the asset's client.
  const suppliers = useSuppliers({ limit: API_MAX_LIMIT }, { enabled: open });
  const contracts = useContracts({ limit: API_MAX_LIMIT, sort: 'number' }, { enabled: open });
  const fits = (entry: { client_id: string | null }) =>
    entry.client_id === null ||
    entry.client_id === undefined ||
    entry.client_id === asset.client_id;

  const reset = () => {
    setValues(initialValues(asset));
    setOwner(initialOwner(asset));
    setFieldErrors({});
    setFailure('');
    setConflict('');
  };
  const close = (next: boolean) => {
    if (next) reset();
    onOpenChange(next);
  };
  const setValue = (field: EditField, value: string) => {
    setValues((current) => ({ ...current, [field]: value }));
  };

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure('');
    setConflict('');
    setFieldErrors({});
    const before = initialValues(asset);
    const body: Record<string, string> = {};
    for (const field of ALL_EDIT_FIELDS) {
      const next = values[field].trim();
      if (next !== before[field]) body[field] = next;
    }
    const was = initialOwner(asset);
    if ((owner?.id ?? '') !== (was?.id ?? '') || owner?.kind !== was?.kind) {
      body.owner_user_id = owner?.kind === 'user' ? owner.id : '';
      body.owner_team_id = owner?.kind === 'team' ? owner.id : '';
    }
    if (Object.keys(body).length === 0) {
      onOpenChange(false);
      return;
    }
    try {
      await update.mutateAsync({
        id: asset.id,
        data: body as AssetUpdateRequest,
        version: asset.version,
      });
      onOpenChange(false);
    } catch (err) {
      const stale = conflictMessage(err, t);
      if (stale) setConflict(stale);
      if (err instanceof ApiError) {
        setFieldErrors(err.fieldErrors());
        if (!stale) setFailure(err.detail || err.message || t('asset.detail.edit.failed'));
        return;
      }
      setFailure(t('asset.detail.edit.failed'));
    }
  }

  const input = (field: EditField) => (
    <Input
      key={field}
      label={t(`asset.detail.fields.${field}`)}
      type={INPUT_TYPES[field] ?? 'text'}
      step={field === 'purchase_price' ? '0.01' : undefined}
      value={values[field]}
      onChange={(e) => setValue(field, e.target.value)}
      error={fieldErrors[field]?.message}
      aria-invalid={field in fieldErrors ? true : undefined}
      required={field === 'name' || field === 'inventory_no'}
    />
  );
  const supplierOptions = [
    { value: '', label: '—' },
    ...(suppliers.data?.data ?? [])
      .filter((entry) => fits(entry) || entry.id === asset.supplier_id)
      .map((entry) => ({ value: entry.id, label: entry.name })),
  ];
  const contractOptions = [
    { value: '', label: '—' },
    ...(contracts.data?.data ?? [])
      .filter((entry) => fits(entry) || entry.id === asset.contract_id)
      .map((entry) => ({ value: entry.id, label: entry.number })),
  ];

  return (
    <Modal open={open} onOpenChange={close} title={t('asset.detail.edit.title')} size="lg">
      <form className="space-y-4" onSubmit={(event) => void submit(event)} noValidate>
        {conflict ? (
          <div role="alert" className="flex flex-wrap items-center gap-3 text-sm text-danger">
            <span>{conflict}</span>
            <Button
              type="button"
              size="sm"
              variant="secondary"
              icon={<RotateCw />}
              onClick={() => {
                onReload();
                onOpenChange(false);
              }}
            >
              {t('asset.detail.edit.reload')}
            </Button>
          </div>
        ) : null}
        <div className="grid gap-4 sm:grid-cols-2">
          {EDIT_FIELDS.map((field) => input(field))}
          <Select
            label={t('asset.columns.category')}
            options={[
              { value: '', label: '—' },
              ...ASSET_CATEGORIES.map((value) => ({
                value,
                label: t(`asset.categories.${value}`, value),
              })),
            ]}
            value={values.category}
            onChange={(e) => setValue('category', e.target.value)}
            error={fieldErrors.category?.message}
          />
          <Select
            label={t('asset.detail.fields.supplier_id')}
            options={supplierOptions}
            value={values.supplier_id}
            onChange={(e) => setValue('supplier_id', e.target.value)}
            error={fieldErrors.supplier_id?.message}
          />
          <Select
            label={t('asset.detail.fields.contract_id')}
            options={contractOptions}
            value={values.contract_id}
            onChange={(e) => setValue('contract_id', e.target.value)}
            error={fieldErrors.contract_id?.message}
          />
          <UserTeamPicker label={t('ci.owner.title')} value={owner} onChange={setOwner} />
        </div>
        {fieldErrors.owner_user_id || fieldErrors.owner_team_id ? (
          <p role="alert" className="text-sm text-danger">
            {(fieldErrors.owner_user_id ?? fieldErrors.owner_team_id)?.message}
          </p>
        ) : null}
        <Textarea
          label={t('asset.detail.notes')}
          value={values.notes}
          onChange={(e) => setValue('notes', e.target.value)}
          error={fieldErrors.notes?.message}
        />
        {failure ? (
          <p role="alert" className="text-sm text-danger">
            {failure}
          </p>
        ) : null}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={() => close(false)}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" loading={update.isPending}>
            {t('common.save')}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

// ─── Contract and supplier (MGT-12) ──────────────────────────────────────────

function ContractCard({ asset }: { asset: Asset }) {
  const { t, i18n } = useTranslation();
  const supplier = useSupplier(asset.supplier_id);
  const contract = useContract(asset.contract_id);
  const c = contract.data;
  const term =
    c && (c.start_date || c.end_date)
      ? `${formatDate(c.start_date ?? undefined, i18n.language) ?? '…'} – ${
          formatDate(c.end_date ?? undefined, i18n.language) ?? '…'
        }`
      : undefined;
  return (
    <Card title={t('asset.detail.contract.title')}>
      <dl>
        <div className="grid grid-cols-1 gap-0.5 py-1.5 sm:grid-cols-[6.5rem_minmax(0,1fr)] sm:gap-4">
          <dt className="text-sm text-fg-muted">{t('asset.detail.contract.supplier')}</dt>
          <dd className="min-w-0 text-sm text-fg [overflow-wrap:anywhere]">
            {asset.supplier_id ? (
              <Link
                to="/suppliers"
                className="font-medium text-accent underline-offset-2 hover:underline"
              >
                {supplier.data?.name ?? t('asset.detail.contract.unnamedSupplier')}
              </Link>
            ) : (
              <span className="text-fg-subtle">—</span>
            )}
          </dd>
        </div>
        <div className="grid grid-cols-1 gap-0.5 py-1.5 sm:grid-cols-[6.5rem_minmax(0,1fr)] sm:gap-4">
          <dt className="text-sm text-fg-muted">{t('asset.detail.contract.number')}</dt>
          <dd className="min-w-0 text-sm text-fg [overflow-wrap:anywhere]">
            {asset.contract_id ? (
              <Link
                to={`/contracts/${asset.contract_id}`}
                className="font-medium text-accent underline-offset-2 hover:underline"
              >
                {c?.number ?? t('asset.detail.contract.unnamedContract')}
              </Link>
            ) : (
              <span className="text-fg-subtle">—</span>
            )}
          </dd>
        </div>
        {asset.contract_id ? (
          <>
            <DetailRow compact label={t('asset.detail.contract.term')} value={term} />
            <DetailRow
              compact
              label={t('asset.detail.contract.noticeDate')}
              value={formatDate(noticeDate(c?.end_date, c?.notice_period_days), i18n.language)}
            />
          </>
        ) : null}
      </dl>
    </Card>
  );
}

// ─── Holder, reservation and movements ───────────────────────────────────────

/** useHolderLabel names the current holder of an asset (WP-745); without user:read its kind. */
function useHolderLabel(asset: Asset): string | undefined {
  const { t } = useTranslation();
  const mayRead = useHasPermission('user:read') === true;
  const user = useUser(asset.assigned_user_id, { enabled: mayRead });
  const team = useTeam(asset.assigned_team_id, { enabled: mayRead });
  if (asset.assigned_user_id) {
    return user.data?.id === asset.assigned_user_id ? user.data.display_name : t('ci.owner.user');
  }
  if (asset.assigned_team_id) {
    const name = team.data?.id === asset.assigned_team_id ? team.data.name : t('ci.owner.team');
    return t('assignment.teamHolder', { name });
  }
  return undefined;
}

function HolderCard({
  asset,
  holder,
  onAssign,
  onReturn,
  onTransfer,
  onReserve,
  onMove,
}: {
  asset: Asset;
  holder?: string;
  onAssign: () => void;
  onReturn: () => void;
  onTransfer: () => void;
  onReserve: () => void;
  onMove: () => void;
}) {
  const { t, i18n } = useTranslation();
  const gates = useStockFlowGates();
  const release = useReleaseReservation();
  const reservation = asset.active_reservation;
  const mayRead = useHasPermission('user:read') === true;
  const reservedFor = useUser(reservation?.assignee_id, { enabled: mayRead });
  const held = !!(asset.assigned_user_id || asset.assigned_team_id);
  const reservedLabel = reservation?.assignee_id
    ? reservedFor.data?.id === reservation.assignee_id
      ? reservedFor.data.display_name
      : t('ci.owner.user')
    : reservation?.project_ref;

  return (
    <Card title={t('asset.detail.holder.title')}>
      <dl>
        <DetailRow compact label={t('asset.detail.holder.current')} value={holder} />
        {held ? (
          <DetailRow
            compact
            label={t('asset.detail.holder.due')}
            value={formatDate(asset.assignment_due_at, i18n.language)}
          />
        ) : null}
      </dl>
      <div className="mt-3 flex flex-wrap gap-2">
        {held ? (
          <>
            <Button size="sm" variant="secondary" onClick={onReturn}>
              {t('assignment.return')}
            </Button>
            <Button size="sm" variant="secondary" onClick={onTransfer}>
              {t('assignment.transfer')}
            </Button>
          </>
        ) : (
          <Button size="sm" variant="secondary" onClick={onAssign}>
            {t('assignment.assign')}
          </Button>
        )}
        {gates.moveAsset ? (
          <Button size="sm" variant="secondary" onClick={onMove}>
            {t('stockFlow.moveAsset')}
          </Button>
        ) : null}
        {gates.reserve && !reservation ? (
          <Button size="sm" variant="secondary" onClick={onReserve}>
            {t('stockFlow.reserve')}
          </Button>
        ) : null}
      </div>
      {reservation ? (
        <div className="mt-4 border-t border-line pt-3">
          <h3 className="text-sm font-medium text-fg">{t('asset.detail.reservation.title')}</h3>
          <dl>
            <DetailRow
              compact
              label={t('asset.detail.reservation.until')}
              value={formatDate(reservation.reserved_until, i18n.language)}
            />
            <DetailRow
              compact
              label={t('asset.detail.reservation.for')}
              value={reservedLabel ?? undefined}
            />
          </dl>
          <Button
            size="sm"
            variant="secondary"
            loading={release.isPending}
            onClick={() => release.mutate(reservation.id)}
          >
            {t('asset.detail.reservation.release')}
          </Button>
          {release.error ? (
            <p role="alert" className="mt-2 text-sm text-danger">
              {release.error instanceof ApiError
                ? release.error.detail || release.error.message
                : String(release.error)}
            </p>
          ) : null}
        </div>
      ) : null}
    </Card>
  );
}

// ─── CIs (AST-04) ────────────────────────────────────────────────────────────

function aggregateErrorText(error: unknown, t: TFunction): string | undefined {
  if (!error) return undefined;
  return t('asset.detail.listError');
}

/** linkRefusal words a refused link: a 422 on asset_id (client or configuration child). */
function linkRefusal(error: unknown, t: TFunction): string | undefined {
  if (!error) return undefined;
  if (error instanceof ApiError) {
    if (error.status === 422 && error.field === 'asset_id') {
      return t('asset.detail.cis.linkRefused', { detail: error.detail ?? error.message });
    }
    return error.detail || error.message;
  }
  return String(error);
}

function CIsCard({
  asset,
  cis,
  loading,
  error,
}: {
  asset: Asset;
  cis: AssetDetailCI[];
  loading: boolean;
  error: unknown;
}) {
  const { t } = useTranslation();
  const mayWriteCI = useHasPermission('ci:write') === true;
  const mayWriteAsset = useHasPermission('asset:write') === true;
  const mayLink = mayWriteCI && mayWriteAsset;
  const link = useLinkAssetCI();
  const unlink = useUnlinkAssetCI();
  const [choice, setChoice] = useState<PickedObject[]>([]);
  const [primary, setPrimary] = useState(false);
  const [expanded, setExpanded] = useState<string | null>(null);
  const primaryId = asset.primary_ci_id;

  const submit = () => {
    const chosen = choice[0];
    if (!chosen) return;
    unlink.reset();
    link.mutate(
      { id: asset.id, data: { ci_id: chosen.id, primary } },
      {
        onSuccess: () => {
          setChoice([]);
          setPrimary(false);
        },
      },
    );
  };
  const refusal = linkRefusal(link.error ?? unlink.error, t);

  return (
    <Card title={t('asset.detail.cis.title')}>
      {loading ? <SkeletonList rows={2} label={t('app.loading')} /> : null}
      {error ? (
        <p role="alert" className="text-sm text-danger">
          {aggregateErrorText(error, t)}
        </p>
      ) : null}
      {!loading && !error && cis.length === 0 ? (
        <p className="text-sm text-fg-muted">{t('asset.detail.cis.none')}</p>
      ) : null}
      {cis.length > 0 ? (
        <ul className="-my-2 divide-y divide-line text-sm">
          {cis.map((ci) => (
            <li key={ci.id} className="py-2.5">
              <div className="flex flex-wrap items-center gap-2">
                <button
                  type="button"
                  className="inline-flex h-6 w-6 items-center justify-center rounded-md text-fg-subtle hover:text-accent"
                  aria-expanded={expanded === ci.id}
                  aria-label={t('asset.detail.cis.provenance', { name: ci.name })}
                  onClick={() => setExpanded((current) => (current === ci.id ? null : ci.id))}
                >
                  <ChevronRight
                    aria-hidden="true"
                    className={`h-4 w-4 transition-transform ${expanded === ci.id ? 'rotate-90' : ''}`}
                  />
                </button>
                <Link
                  to={`/cmdb/${ci.id}`}
                  className="min-w-0 font-medium text-accent underline-offset-2 [overflow-wrap:anywhere] hover:underline"
                >
                  {ci.name}
                </Link>
                {ci.id === primaryId ? (
                  <Badge variant="info">{t('asset.detail.cis.primary')}</Badge>
                ) : null}
                {mayLink ? (
                  <Button
                    size="sm"
                    variant="ghost"
                    icon={<Unlink />}
                    className="ml-auto"
                    loading={unlink.isPending && unlink.variables?.ciId === ci.id}
                    aria-label={t('asset.detail.cis.unlinkNamed', { name: ci.name })}
                    onClick={() => {
                      link.reset();
                      unlink.mutate({ id: asset.id, ciId: ci.id });
                    }}
                  >
                    {t('asset.detail.cis.unlink')}
                  </Button>
                ) : null}
              </div>
              {expanded === ci.id ? <CIAssetProvenance ciId={ci.id} /> : null}
            </li>
          ))}
        </ul>
      ) : null}
      {mayLink ? (
        <div className="mt-4 space-y-3 border-t border-line pt-4">
          <ObjectPicker
            label={t('asset.detail.cis.pick')}
            kinds={['ci']}
            value={choice}
            onChange={setChoice}
          />
          <label className="flex items-center gap-2 text-sm text-fg">
            <input
              type="checkbox"
              checked={primary}
              onChange={(e) => setPrimary(e.target.checked)}
            />
            {t('asset.detail.cis.makePrimary')}
          </label>
          <Button
            size="sm"
            icon={<Link2 />}
            disabled={choice.length === 0}
            loading={link.isPending}
            onClick={submit}
          >
            {t('asset.detail.cis.link')}
          </Button>
        </div>
      ) : null}
      {refusal ? (
        <p role="alert" className="mt-2 text-sm text-danger">
          {refusal}
        </p>
      ) : null}
    </Card>
  );
}

function displayValue(value: unknown): string {
  if (value === undefined || value === null || value === '') return '—';
  return typeof value === 'object' ? JSON.stringify(value) : String(value);
}

/**
 * CIAssetProvenance lists the fields of a CI whose value comes from the
 * asset (reason asset, REC-10) with the ProvenanceTooltip; it reads GET
 * /cis/{id}/provenance only once the row is expanded.
 */
function CIAssetProvenance({ ciId }: { ciId: string }) {
  const { t } = useTranslation();
  const provenance = useFieldProvenance(ciId);
  const rows = (provenance.data?.data ?? []).filter((entry) => entry.reason === 'asset');
  if (provenance.isLoading) return <SkeletonList rows={1} label={t('app.loading')} />;
  if (provenance.error) {
    return (
      <p role="alert" className="mt-2 text-sm text-danger">
        {t('asset.detail.cis.provenanceError')}
      </p>
    );
  }
  if (rows.length === 0) {
    return <p className="mt-2 pl-8 text-sm text-fg-muted">{t('asset.detail.cis.noDerived')}</p>;
  }
  return (
    <ul className="mt-2 space-y-1 pl-8">
      {rows.map((entry) => (
        <li key={entry.field} className="flex items-center gap-2 text-sm">
          <span className="text-fg-muted">{entry.field}</span>
          <span className="text-fg [overflow-wrap:anywhere]">
            {fieldValueText(t, entry.field, entry.value, displayValue)}
          </span>
          <ProvenanceTooltip provenance={entry} />
        </li>
      ))}
    </ul>
  );
}

// ─── Children (AST-05) ───────────────────────────────────────────────────────

function ChildrenCard({
  items,
  loading,
  error,
}: {
  items: AssetDetailCI[];
  loading: boolean;
  error: unknown;
}) {
  const { t } = useTranslation();
  const locations = useLocationNames(items.some((item) => !!item.locationId));
  return (
    <Card title={t('asset.detail.children.title')}>
      {loading ? <SkeletonList rows={2} label={t('app.loading')} /> : null}
      {error ? (
        <p role="alert" className="text-sm text-danger">
          {aggregateErrorText(error, t)}
        </p>
      ) : null}
      {!loading && !error && items.length === 0 ? (
        <p className="text-sm text-fg-muted">{t('asset.detail.children.none')}</p>
      ) : null}
      {items.length > 0 ? (
        <ul className="-my-2 divide-y divide-line text-sm">
          {items.map((child) => {
            const configuration = child.childMode === 'configuration';
            const mode = child.childMode ?? 'standalone';
            const target =
              !configuration && child.assetId ? `/assets/${child.assetId}` : `/cmdb/${child.id}`;
            const facts: [string, string | undefined][] = [
              [t('asset.columns.serial_number'), child.serialNumber ?? undefined],
              [t('asset.columns.location_id'), locations.name(child.locationId ?? undefined)],
              [
                t('asset.columns.lifecycle_state'),
                child.lifecycleState
                  ? t(`lifecycle.states.${child.lifecycleState}`, child.lifecycleState)
                  : undefined,
              ],
            ];
            return (
              <li key={child.id} data-child-mode={mode} className="space-y-1 py-2.5">
                <div className="flex flex-wrap items-center gap-2">
                  {configuration ? (
                    <span className="font-medium text-fg [overflow-wrap:anywhere]">
                      {child.name}
                    </span>
                  ) : (
                    <Link
                      to={target}
                      className="font-medium text-accent underline-offset-2 [overflow-wrap:anywhere] hover:underline"
                    >
                      {child.name}
                    </Link>
                  )}
                  <Badge variant={configuration ? 'neutral' : 'info'}>
                    {t(`asset.childModes.${mode}`)}
                  </Badge>
                </div>
                <dl className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-fg-muted">
                  {facts.map(([label, value]) => (
                    <div key={label} className="flex gap-1">
                      <dt>{label}:</dt>
                      <dd className="text-fg">
                        {value ?? '—'}
                        {configuration ? ` ${t('asset.detail.children.fromAsset')}` : ''}
                      </dd>
                    </div>
                  ))}
                </dl>
              </li>
            );
          })}
        </ul>
      ) : null}
    </Card>
  );
}

// ─── Documents (WP-321) ──────────────────────────────────────────────────────

/**
 * AssetDocuments shows the documents of the aggregate field documents with
 * LinkedDocuments; the first read uses the answer of the page's BFF query,
 * a refresh after an upload or a link reads the aggregate again.
 */
function AssetDocuments({ assetId }: { assetId: string }) {
  const queryClient = useQueryClient();
  const mayWrite = useHasPermission('document:write') === true;
  const loaded = useRef(false);
  const load = async () => {
    const options = assetDetailQuery(assetId);
    const result = loaded.current
      ? await queryClient.fetchQuery({ ...options, staleTime: 0 })
      : await queryClient.ensureQueryData(options);
    loaded.current = true;
    return (result.data?.asset?.documents ?? []).map(documentOf);
  };
  return <LinkedDocuments entityType="asset" entityId={assetId} load={load} canWrite={mayWrite} />;
}
