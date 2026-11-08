/**
 * StocktakeDetailPage — one stocktake (INV-01, UI-14, WP-813): head, area,
 * status and counters; start (freezes the snapshot, POST …/start) and
 * cancel (POST …/cancel); the snapshot (GET …/items) split into assets with
 * their expected location and consumables with their expected quantity per
 * bin; the scan loop while in progress (ScanDialog in continuous mode, code
 * lookup, place found from a location label or the LocationPicker, POST
 * …/scans classified by the server); the count of the consumables; the
 * difference list (GET …/differences) and the completion (POST …/complete)
 * with the movements it is expected to book. A completed or cancelled
 * stocktake is read-only. A stocktake of an earlier release without area
 * offers cancelling only: the server refuses its start (422) and its scans
 * (409).
 *
 * Start, cancel, edit and scans need stocktake:write, the completion
 * stocktake:complete (useHasPermission, fail closed). Camera and lookup need
 * the feature barcode (useScanGate); without it a find is chosen with the
 * ObjectPicker (scan_method manual). Labels need barcode as well
 * (LabelPrintDialog).
 */
import { useMemo, useState } from 'react';
import type { FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { useParams } from 'react-router-dom';
import { useQueries } from '@tanstack/react-query';
import { Ban, CheckCircle2, Pencil, Play, Printer, ScanLine, Tag } from 'lucide-react';
import {
  ApiError,
  assetApi,
  consumableApi,
  type ScanMethod,
  type StockScan,
  type Stocktake,
  type StocktakeCompletion,
  type StocktakeDifference,
  type StocktakeItem,
} from '../api/client';
import type { ScannedObject } from '../api/cmdb';
import { useScanLookup } from '../api/cmdbHooks';
import {
  useAddScan,
  useCancelStocktake,
  useCompleteStocktake,
  useHasPermission,
  useStartStocktake,
  useStocktake,
  useStocktakeDifferences,
  useStocktakeItems,
  useStocktakeScans,
  useUpdateStocktake,
} from '../api/hooks';
import {
  formatDateTime,
  movementLabel,
  useLocationNames,
  type LocationNames,
} from '../components/cmdb/ObjectHistory';
import { LabelPrintDialog, useLabelGate } from '../components/flows/LabelPrintDialog';
import { ScanDialog, useScanGate, type ScanSource } from '../components/flows/ScanDialog';
import { LocationPicker, type PickedLocation } from '../components/pickers/LocationPicker';
import { ObjectPicker, type PickedObject } from '../components/pickers/ObjectPicker';
import { Badge } from '../components/ui/Badge';
import { Button, IconButton } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { PageHeader } from '../components/ui/PageHeader';
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
import { stocktakeStatusVariant } from './StocktakeListPage';

/** SCAN_RESULTS are the classifications of a scan and an item; stocktake.results.* name them. */
export const SCAN_RESULTS = ['found', 'missing', 'unexpected'] as const;
/** SCAN_METHODS are the ways a find is recorded; stocktake.scanMethods.* name them. */
export const SCAN_METHODS = ['barcode', 'qrcode', 'rfid', 'manual'] as const;

/** NAME_LOOKUP_LIMIT caps the objects whose names the page reads one by one. */
const NAME_LOOKUP_LIMIT = 100;
/** RECENT_SCANS is the number of scans the page lists. */
const RECENT_SCANS = 10;

function resultVariant(result: string) {
  switch (result) {
    case 'found':
      return 'success' as const;
    case 'missing':
      return 'danger' as const;
    case 'unexpected':
      return 'warning' as const;
    default:
      return 'neutral' as const;
  }
}

/** errorDetail is the detail of a refused action (403, 409, 422), else its message. */
function errorDetail(error: unknown): string {
  if (error instanceof ApiError) return error.detail ?? error.message;
  return error instanceof Error ? error.message : String(error);
}

/** methodOf is the scan_method of a code read in the given way. */
function methodOf(source: ScanSource): ScanMethod {
  return source === 'qrcode' ? 'qrcode' : 'barcode';
}

/** binKey keys a consumable in a bin. */
const binKey = (consumableId: string | undefined, locationId: string | undefined) =>
  `${consumableId ?? ''}@${locationId ?? ''}`;

/** countedByBin sums the scanned quantities per consumable and bin. */
function countedByBin(scans: StockScan[]): Map<string, number> {
  const sums = new Map<string, number>();
  for (const scan of scans) {
    if (!scan.consumable_id) continue;
    const key = binKey(scan.consumable_id, scan.location_id);
    sums.set(key, (sums.get(key) ?? 0) + (scan.quantity ?? 0));
  }
  return sums;
}

// ─── Object names ────────────────────────────────────────────────────────────

interface ObjectNames {
  asset: (id?: string) => string;
  consumable: (id?: string) => string;
}

/**
 * useObjectNames reads the names of the assets and consumables of the
 * snapshot and the scans (GET /assets/{id}, /consumables/{id}), at most
 * NAME_LOOKUP_LIMIT of them; the others and those the caller cannot read
 * keep their id. Names from the difference list come along embedded.
 */
function useObjectNames(
  assetIds: string[],
  consumableIds: string[],
  embedded: StocktakeDifference[],
): ObjectNames {
  const assets = useMemo(() => [...new Set(assetIds)].slice(0, NAME_LOOKUP_LIMIT), [assetIds]);
  const consumables = useMemo(
    () => [...new Set(consumableIds)].slice(0, NAME_LOOKUP_LIMIT),
    [consumableIds],
  );
  const assetQueries = useQueries({
    queries: assets.map((id) => ({
      queryKey: ['stocktake-names', 'asset', id],
      queryFn: () => assetApi.get(id),
      retry: false,
      staleTime: 5 * 60_000,
    })),
  });
  const consumableQueries = useQueries({
    queries: consumables.map((id) => ({
      queryKey: ['stocktake-names', 'consumable', id],
      queryFn: () => consumableApi.get(id),
      retry: false,
      staleTime: 5 * 60_000,
    })),
  });
  const assetNames = new Map<string, string>();
  const consumableNames = new Map<string, string>();
  for (const entry of embedded) {
    if (entry.asset_id && entry.asset?.name) assetNames.set(entry.asset_id, entry.asset.name);
    if (entry.consumable_id && entry.consumable?.name) {
      consumableNames.set(entry.consumable_id, entry.consumable.name);
    }
  }
  assetQueries.forEach((query, index) => {
    if (query.data?.name) assetNames.set(assets[index]!, query.data.name);
  });
  consumableQueries.forEach((query, index) => {
    if (query.data?.name) consumableNames.set(consumables[index]!, query.data.name);
  });
  return {
    asset: (id) => (id ? (assetNames.get(id) ?? id) : '—'),
    consumable: (id) => (id ? (consumableNames.get(id) ?? id) : '—'),
  };
}

// ─── Page ────────────────────────────────────────────────────────────────────

export function StocktakeDetailPage() {
  const { t } = useTranslation();
  const { id = '' } = useParams();
  const query = useStocktake(id);

  if (query.isLoading) return <SkeletonList rows={4} label={t('app.loading')} />;
  if (query.error || !query.data) {
    return (
      <ErrorState
        title={t('app.error')}
        description={query.error ? errorDetail(query.error) : undefined}
        retryLabel={t('common.retry')}
        onRetry={() => void query.refetch()}
      />
    );
  }
  return <StocktakeDetail stocktake={query.data} />;
}

type Confirm = 'start' | 'cancel' | 'complete' | null;

function StocktakeDetail({ stocktake: st }: { stocktake: Stocktake }) {
  const { t, i18n } = useTranslation();
  const canWrite = useHasPermission('stocktake:write') === true;
  const canComplete = useHasPermission('stocktake:complete') === true;
  const labelsAllowed = useLabelGate();
  const locations = useLocationNames(true);

  const legacy = !st.location_id;
  const terminal = st.status === 'completed' || st.status === 'cancelled';
  const inProgress = st.status === 'in_progress';
  const withSnapshot = st.status !== 'planned' && !legacy;

  const items = useStocktakeItems(st.id, withSnapshot);
  const scans = useStocktakeScans(st.id, withSnapshot);
  const differences = useStocktakeDifferences(st.id, withSnapshot);

  const start = useStartStocktake();
  const cancel = useCancelStocktake();
  const complete = useCompleteStocktake();

  const [confirm, setConfirm] = useState<Confirm>(null);
  const [editing, setEditing] = useState(false);
  const [actionError, setActionError] = useState<string>();
  const [completion, setCompletion] = useState<StocktakeCompletion>();
  const [label, setLabel] = useState<{ asset?: { id: string; name: string } } | null>(null);

  const itemRows = useMemo(() => items.data?.data ?? [], [items.data]);
  const scanRows = useMemo(() => scans.data?.data ?? [], [scans.data]);
  const differenceRows = useMemo(() => differences.data?.data ?? [], [differences.data]);
  const names = useObjectNames(
    useMemo(
      () =>
        [...itemRows.map((item) => item.asset_id), ...scanRows.map((scan) => scan.asset_id)].filter(
          (value): value is string => !!value,
        ),
      [itemRows, scanRows],
    ),
    useMemo(
      () =>
        [
          ...itemRows.map((item) => item.consumable_id),
          ...scanRows.map((scan) => scan.consumable_id),
        ].filter((value): value is string => !!value),
      [itemRows, scanRows],
    ),
    differenceRows,
  );
  const counted = useMemo(() => countedByBin(scanRows), [scanRows]);

  const run = (action: Exclude<Confirm, null>) => {
    setActionError(undefined);
    const options = {
      onSuccess: () => setConfirm(null),
      onError: (error: unknown) => {
        setConfirm(null);
        setActionError(errorDetail(error));
      },
    };
    if (action === 'start') start.mutate(st.id, options);
    else if (action === 'cancel') cancel.mutate(st.id, options);
    else {
      complete.mutate(st.id, {
        ...options,
        onSuccess: (data) => {
          setConfirm(null);
          setCompletion(data);
        },
      });
    }
  };

  const areaPath = st.location_id ? locations.path(st.location_id) : undefined;
  const area = areaPath ?? t(`stocktake.scopes.${st.scope}`, st.scope);
  const found = Math.max(st.total_scanned - st.total_surplus, 0);
  const pending = start.isPending || cancel.isPending || complete.isPending;

  const actions = (
    <>
      {labelsAllowed && !legacy ? (
        <Button variant="secondary" icon={<Printer />} onClick={() => setLabel({})}>
          {t('stocktake.labels')}
        </Button>
      ) : null}
      {canWrite && !terminal && !legacy ? (
        <Button variant="secondary" icon={<Pencil />} onClick={() => setEditing(true)}>
          {t('stocktake.edit')}
        </Button>
      ) : null}
      {canWrite && !terminal ? (
        <Button variant="danger" icon={<Ban />} onClick={() => setConfirm('cancel')}>
          {t('stocktake.cancel')}
        </Button>
      ) : null}
      {canWrite && st.status === 'planned' && !legacy ? (
        <Button icon={<Play />} onClick={() => setConfirm('start')}>
          {t('stocktake.start')}
        </Button>
      ) : null}
      {canComplete && inProgress && !legacy ? (
        <Button icon={<CheckCircle2 />} onClick={() => setConfirm('complete')}>
          {t('stocktake.complete')}
        </Button>
      ) : null}
    </>
  );

  return (
    <div className="space-y-4">
      <PageHeader
        title={st.title}
        description={st.description || undefined}
        breadcrumbs={[{ label: t('stocktake.title'), to: '/stocktake' }, { label: st.title }]}
        meta={
          <Badge variant={stocktakeStatusVariant(st.status)}>
            {t(`stocktake.statuses.${st.status}`, st.status)}
          </Badge>
        }
        actions={actions}
      />

      {actionError ? (
        <p
          role="alert"
          className="rounded-lg border border-line bg-panel px-4 py-3 text-sm text-danger"
        >
          {actionError}
        </p>
      ) : null}
      {legacy && !terminal ? (
        <p role="status" className="rounded-lg border border-line bg-panel px-4 py-3 text-sm">
          {t('stocktake.legacyHint')}
        </p>
      ) : null}
      {terminal ? (
        <p role="status" className="rounded-lg border border-line bg-panel px-4 py-3 text-sm">
          {st.status === 'completed'
            ? t('stocktake.readOnlyCompleted')
            : t('stocktake.readOnlyCancelled')}
        </p>
      ) : null}

      <Card title={t('stocktake.overview')}>
        <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
          <div>
            <dt className="text-fg-muted">{t('stocktake.area')}</dt>
            <dd className="text-fg">{area}</dd>
          </div>
          <div>
            <dt className="text-fg-muted">{t('stocktake.dueDate')}</dt>
            <dd className="text-fg">{formatDateTime(st.due_date, i18n.language) ?? '—'}</dd>
          </div>
          <div>
            <dt className="text-fg-muted">{t('stocktake.startedAt')}</dt>
            <dd className="text-fg">{formatDateTime(st.started_at, i18n.language) ?? '—'}</dd>
          </div>
          <div>
            <dt className="text-fg-muted">{t('stocktake.completedAt')}</dt>
            <dd className="text-fg">{formatDateTime(st.completed_at, i18n.language) ?? '—'}</dd>
          </div>
        </dl>
        <Counters stocktake={st} found={found} />
      </Card>

      {completion ? (
        <CompletionResult completion={completion} names={names} locations={locations} />
      ) : null}

      {inProgress && canWrite && !legacy ? (
        <ScanPanel stocktake={st} counted={counted} locations={locations} found={found} />
      ) : null}

      {withSnapshot ? (
        <>
          <AssetSnapshot
            items={itemRows}
            scans={scanRows}
            loading={items.isLoading}
            names={names}
            locations={locations}
            inProgress={inProgress}
            onLabel={labelsAllowed ? (asset) => setLabel({ asset }) : undefined}
          />
          <ConsumableSnapshot
            stocktakeId={st.id}
            items={itemRows}
            counted={counted}
            loading={items.isLoading}
            names={names}
            locations={locations}
            countable={inProgress && canWrite}
          />
          <RecentScans scans={scanRows} names={names} locations={locations} />
          <DifferenceList
            rows={differenceRows}
            loading={differences.isLoading}
            names={names}
            locations={locations}
            onLabel={labelsAllowed ? (asset) => setLabel({ asset }) : undefined}
          />
        </>
      ) : null}

      <ConfirmDialog
        open={confirm === 'start'}
        onOpenChange={(open) => !open && setConfirm(null)}
        title={t('stocktake.confirmStart.title')}
        busy={pending}
        confirmLabel={t('stocktake.start')}
        onConfirm={() => run('start')}
      >
        <p className="text-sm">{t('stocktake.confirmStart.body', { area })}</p>
      </ConfirmDialog>
      <ConfirmDialog
        open={confirm === 'cancel'}
        onOpenChange={(open) => !open && setConfirm(null)}
        title={t('stocktake.confirmCancel.title')}
        busy={pending}
        confirmLabel={t('stocktake.cancel')}
        danger
        onConfirm={() => run('cancel')}
      >
        <p className="text-sm">{t('stocktake.confirmCancel.body')}</p>
      </ConfirmDialog>
      <ConfirmDialog
        open={confirm === 'complete'}
        onOpenChange={(open) => !open && setConfirm(null)}
        title={t('stocktake.confirmComplete.title')}
        busy={pending}
        confirmLabel={t('stocktake.complete')}
        onConfirm={() => run('complete')}
      >
        <PlannedMovements rows={differenceRows} names={names} locations={locations} />
      </ConfirmDialog>

      {editing ? <EditDialog stocktake={st} onClose={() => setEditing(false)} /> : null}

      {labelsAllowed ? (
        <LabelPrintDialog
          open={label !== null}
          onOpenChange={(open) => !open && setLabel(null)}
          asset={label?.asset}
          areaId={st.location_id}
          areaName={area}
        />
      ) : null}
    </div>
  );
}

// ─── Counters ────────────────────────────────────────────────────────────────

function Counters({ stocktake: st, found }: { stocktake: Stocktake; found: number }) {
  const { t } = useTranslation();
  const entries: Array<[string, number]> = [
    [t('stocktake.counters.expected'), st.total_expected],
    [t('stocktake.counters.found'), found],
    [t('stocktake.counters.unexpected'), st.total_surplus],
    [
      st.status === 'completed' ? t('stocktake.counters.missing') : t('stocktake.counters.open'),
      st.total_missing,
    ],
  ];
  return (
    <dl
      aria-live="polite"
      aria-label={t('stocktake.counters.label')}
      className="mt-4 grid grid-cols-2 gap-3 sm:grid-cols-4"
    >
      {entries.map(([name, value]) => (
        <div key={name} className="rounded-lg border border-line px-3 py-2">
          <dt className="text-xs text-fg-muted">{name}</dt>
          <dd className="text-lg font-semibold tabular-nums text-fg">{value}</dd>
        </div>
      ))}
    </dl>
  );
}

// ─── Scan loop ───────────────────────────────────────────────────────────────

interface PendingCount {
  consumableId: string;
  name: string;
  code?: string;
  method: ScanMethod;
}

interface ScanPanelProps {
  stocktake: Stocktake;
  counted: Map<string, number>;
  locations: LocationNames;
  found: number;
}

/**
 * ScanPanel is the scan loop of an in-progress stocktake: the place found
 * (LocationPicker or a scanned location label), the ScanDialog in
 * continuous mode and the manual find through the ObjectPicker.
 */
function ScanPanel({ stocktake: st, counted, locations, found }: ScanPanelProps) {
  const { t } = useTranslation();
  const scanAllowed = useScanGate();
  const lookup = useScanLookup();
  const addScan = useAddScan(st.id);
  const [place, setPlace] = useState<PickedLocation | null>(null);
  const [scanning, setScanning] = useState(false);
  const [last, setLast] = useState<{ text: string; error?: boolean }>();
  const [pendingCount, setPendingCount] = useState<PendingCount | null>(null);
  const [manual, setManual] = useState<PickedObject[]>([]);
  const [manualQuantity, setManualQuantity] = useState('');

  const placeName = place ? place.path.join(' › ') : undefined;
  const sumIn = (consumableId: string) =>
    place ? (counted.get(binKey(consumableId, place.id)) ?? 0) : 0;

  const record = (
    object: { kind: 'asset' | 'consumable'; id: string; name: string },
    method: ScanMethod,
    code?: string,
    quantity?: number,
  ) => {
    if (!place) {
      setLast({ text: t('stocktake.scan.noPlace'), error: true });
      return;
    }
    addScan.mutate(
      {
        ...(object.kind === 'asset'
          ? { asset_id: object.id }
          : { consumable_id: object.id, quantity }),
        location_id: place.id,
        scan_method: method,
        ...(code ? { code } : {}),
      },
      {
        onSuccess: (scan) => {
          setPendingCount(null);
          setLast({
            text: t('stocktake.scan.recorded', {
              name: object.name,
              result: t(`stocktake.results.${scan.scan_result}`, scan.scan_result),
            }),
          });
        },
        onError: (error) => setLast({ text: errorDetail(error), error: true }),
      },
    );
  };

  const onScan = (code: string, source: ScanSource) => {
    setLast(undefined);
    lookup.mutate(code, {
      onSuccess: (object: ScannedObject) => {
        if (object.kind === 'location') {
          const path = locations.path(object.id);
          setPlace({
            id: object.id,
            name: object.name,
            kind: object.location?.kind ?? 'bin',
            path: path && path !== object.id ? path.split(' › ') : [object.name],
          });
          setPendingCount(null);
          setLast({ text: t('stocktake.scan.placeSet', { name: object.name }) });
          return;
        }
        if (!place) {
          setLast({ text: t('stocktake.scan.noPlace'), error: true });
          return;
        }
        if (object.kind === 'asset') {
          record({ kind: 'asset', id: object.id, name: object.name }, methodOf(source), code);
          return;
        }
        if (place.kind !== 'bin') {
          setLast({ text: t('stocktake.scan.needBin'), error: true });
          return;
        }
        setPendingCount({
          consumableId: object.id,
          name: object.name,
          code,
          method: methodOf(source),
        });
      },
      onError: (error) =>
        setLast({
          text:
            error instanceof ApiError && error.status === 404
              ? t('scan.notFound', { code })
              : errorDetail(error),
          error: true,
        }),
    });
  };

  const submitManual = (event: FormEvent) => {
    event.preventDefault();
    const object = manual[0];
    if (!object || (object.kind !== 'asset' && object.kind !== 'consumable')) return;
    const quantity = Number(manualQuantity);
    if (object.kind === 'consumable' && !(quantity > 0)) return;
    record(
      { kind: object.kind, id: object.id, name: object.name },
      'manual',
      undefined,
      object.kind === 'consumable' ? quantity : undefined,
    );
    setManual([]);
    setManualQuantity('');
  };

  const feedback = (
    <div className="space-y-3">
      <LocationPicker
        label={t('stocktake.scan.place')}
        value={place?.id ?? null}
        onChange={setPlace}
      />
      <p className="text-sm text-fg">
        {placeName
          ? t('stocktake.scan.currentPlace', { name: placeName })
          : t('stocktake.scan.noPlace')}
      </p>
      <p
        aria-live="polite"
        role="status"
        className={cn('min-h-5 text-sm', last?.error ? 'text-danger' : 'text-fg')}
      >
        {lookup.isPending || addScan.isPending ? t('scan.resolving') : (last?.text ?? '')}
      </p>
      <p aria-live="polite" className="text-sm text-fg-muted tabular-nums">
        {t('stocktake.scan.tally', {
          found,
          unexpected: st.total_surplus,
          open: st.total_missing,
        })}
      </p>
      {pendingCount ? (
        <QuantityEntry
          title={t('stocktake.scan.countIn', { name: pendingCount.name, bin: placeName ?? '' })}
          countedSoFar={sumIn(pendingCount.consumableId)}
          busy={addScan.isPending}
          onSubmit={(quantity) =>
            record(
              { kind: 'consumable', id: pendingCount.consumableId, name: pendingCount.name },
              pendingCount.method,
              pendingCount.code,
              quantity,
            )
          }
        />
      ) : null}
    </div>
  );

  return (
    <Card
      title={t('stocktake.scan.title')}
      actions={
        scanAllowed ? (
          <Button icon={<ScanLine />} onClick={() => setScanning(true)}>
            {t('stocktake.scan.open')}
          </Button>
        ) : null
      }
    >
      <div className="space-y-4">
        {scanning ? null : feedback}
        <form onSubmit={submitManual} className="space-y-3 border-t border-line pt-4">
          <h3 className="text-sm font-semibold text-fg">{t('stocktake.scan.manualTitle')}</h3>
          <ObjectPicker
            label={t('stocktake.scan.manualObject')}
            kinds={['asset', 'consumable']}
            value={manual}
            onChange={setManual}
          />
          {manual[0]?.kind === 'consumable' ? (
            <Input
              type="number"
              min={1}
              label={t('stocktake.scan.quantity')}
              value={manualQuantity}
              onChange={(event) => setManualQuantity(event.target.value)}
            />
          ) : null}
          <div className="flex justify-end">
            <Button type="submit" disabled={!manual[0] || addScan.isPending}>
              {t('stocktake.scan.record')}
            </Button>
          </div>
        </form>
      </div>
      {scanAllowed ? (
        <ScanDialog
          open={scanning}
          onOpenChange={setScanning}
          onScan={onScan}
          busy={lookup.isPending || addScan.isPending}
          title={t('stocktake.scan.dialogTitle')}
          description={t('stocktake.scan.dialogDescription')}
        >
          {feedback}
        </ScanDialog>
      ) : null}
    </Card>
  );
}

interface QuantityEntryProps {
  title: string;
  countedSoFar: number;
  busy: boolean;
  onSubmit: (quantity: number) => void;
}

/**
 * QuantityEntry counts a consumable in a bin. Each count adds its quantity,
 * so the field shows the sum counted so far and a recount enters only the
 * additional quantity.
 */
function QuantityEntry({ title, countedSoFar, busy, onSubmit }: QuantityEntryProps) {
  const { t } = useTranslation();
  const [value, setValue] = useState('');
  const quantity = Number(value);
  return (
    <form
      className="space-y-2 rounded-lg border border-line p-3"
      onSubmit={(event) => {
        event.preventDefault();
        if (!(quantity > 0)) return;
        onSubmit(quantity);
        setValue('');
      }}
    >
      <p className="text-sm font-medium text-fg">{title}</p>
      <p className="text-sm text-fg-muted">
        {t('stocktake.quantities.countedSoFar', { sum: countedSoFar })}
      </p>
      <div className="flex items-end gap-2">
        <div className="min-w-0 flex-1">
          <Input
            type="number"
            min={1}
            label={t('stocktake.quantities.additional')}
            value={value}
            onChange={(event) => setValue(event.target.value)}
          />
        </div>
        <Button type="submit" disabled={!(quantity > 0) || busy}>
          {t('stocktake.quantities.count')}
        </Button>
      </div>
    </form>
  );
}

// ─── Snapshot ────────────────────────────────────────────────────────────────

interface AssetSnapshotProps {
  items: StocktakeItem[];
  scans: StockScan[];
  loading: boolean;
  names: ObjectNames;
  locations: LocationNames;
  inProgress: boolean;
  onLabel?: (asset: { id: string; name: string }) => void;
}

function AssetSnapshot({
  items,
  scans,
  loading,
  names,
  locations,
  inProgress,
  onLabel,
}: AssetSnapshotProps) {
  const { t } = useTranslation();
  const assets = items.filter((item) => item.item_kind === 'asset');
  // The scans are newest first: the first one of an asset is its last scan.
  const lastScan = new Map<string, StockScan>();
  for (const scan of scans) {
    if (scan.asset_id && !lastScan.has(scan.asset_id)) lastScan.set(scan.asset_id, scan);
  }
  return (
    <Card title={t('stocktake.snapshot.assets')} className="p-0 overflow-hidden">
      {loading ? <SkeletonList rows={3} label={t('app.loading')} /> : null}
      <ScrollRegion label={t('stocktake.snapshot.assets')}>
        <table className={tableClasses}>
          <thead className={theadClasses}>
            <tr>
              <th scope="col" className={thClasses}>
                {t('stocktake.columns.asset')}
              </th>
              <th scope="col" className={thClasses}>
                {t('stocktake.columns.expectedLocation')}
              </th>
              <th scope="col" className={thClasses}>
                {t('stocktake.columns.result')}
              </th>
              <th scope="col" className={thClasses}>
                {t('stocktake.columns.foundLocation')}
              </th>
              {onLabel ? (
                <th scope="col" className={cn(thClasses, 'text-right')}>
                  {t('common.actions')}
                </th>
              ) : null}
            </tr>
          </thead>
          <tbody>
            {assets.map((item) => {
              const scan = item.asset_id ? lastScan.get(item.asset_id) : undefined;
              const result: string | undefined = item.classification ?? scan?.scan_result;
              const name = names.asset(item.asset_id);
              return (
                <tr key={item.id} className={trClasses}>
                  <td className={tdPrimaryClasses}>{name}</td>
                  <td className={tdClasses}>{locations.path(item.location_id)}</td>
                  <td className={tdClasses}>
                    {result ? (
                      <Badge variant={resultVariant(result)}>
                        {t(`stocktake.results.${result}`, result)}
                      </Badge>
                    ) : (
                      <Badge>{inProgress ? t('stocktake.open') : '—'}</Badge>
                    )}
                  </td>
                  <td className={tdClasses}>
                    {scan?.location_id ? locations.path(scan.location_id) : '—'}
                  </td>
                  {onLabel ? (
                    <td className={cn(tdClasses, 'text-right')}>
                      <IconButton
                        variant="ghost"
                        icon={<Tag className="h-4 w-4" />}
                        label={t('stocktake.labelFor', { name })}
                        onClick={() => item.asset_id && onLabel({ id: item.asset_id, name })}
                      />
                    </td>
                  ) : null}
                </tr>
              );
            })}
            {assets.length === 0 && !loading ? (
              <tr>
                <td colSpan={onLabel ? 5 : 4} className={cn(tdClasses, 'text-fg-muted')}>
                  {t('stocktake.snapshot.noAssets')}
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </ScrollRegion>
    </Card>
  );
}

interface ConsumableSnapshotProps {
  stocktakeId: string;
  items: StocktakeItem[];
  counted: Map<string, number>;
  loading: boolean;
  names: ObjectNames;
  locations: LocationNames;
  countable: boolean;
}

/**
 * ConsumableSnapshot shows each consumable per bin of the snapshot with
 * expected quantity, the sum of its counts and the difference; while in
 * progress a count adds its quantity (POST …/scans with consumable_id and
 * quantity).
 */
function ConsumableSnapshot({
  stocktakeId,
  items,
  counted,
  loading,
  names,
  locations,
  countable,
}: ConsumableSnapshotProps) {
  const { t } = useTranslation();
  const rows = items.filter((item) => item.item_kind === 'consumable');
  return (
    <Card title={t('stocktake.snapshot.consumables')} className="p-0 overflow-hidden">
      {loading ? <SkeletonList rows={3} label={t('app.loading')} /> : null}
      <ScrollRegion label={t('stocktake.snapshot.consumables')}>
        <table className={tableClasses}>
          <thead className={theadClasses}>
            <tr>
              <th scope="col" className={thClasses}>
                {t('stocktake.columns.consumable')}
              </th>
              <th scope="col" className={thClasses}>
                {t('stocktake.columns.bin')}
              </th>
              <th scope="col" className={cn(thClasses, 'text-right')}>
                {t('stocktake.columns.expected')}
              </th>
              <th scope="col" className={cn(thClasses, 'text-right')}>
                {t('stocktake.columns.counted')}
              </th>
              <th scope="col" className={cn(thClasses, 'text-right')}>
                {t('stocktake.columns.difference')}
              </th>
              {countable ? (
                <th scope="col" className={thClasses}>
                  {t('stocktake.columns.count')}
                </th>
              ) : null}
            </tr>
          </thead>
          <tbody>
            {rows.map((item) => {
              const sum =
                item.counted_quantity ??
                counted.get(binKey(item.consumable_id, item.location_id)) ??
                0;
              const name = names.consumable(item.consumable_id);
              return (
                <tr key={item.id} className={trClasses}>
                  <td className={tdPrimaryClasses}>{name}</td>
                  <td className={tdClasses}>{locations.path(item.location_id)}</td>
                  <td className={tdNumericClasses}>{item.expected_quantity}</td>
                  <td className={tdNumericClasses}>{sum}</td>
                  <td className={tdNumericClasses}>{signed(sum - item.expected_quantity)}</td>
                  {countable && item.consumable_id ? (
                    <td className={tdClasses}>
                      <RowCount
                        stocktakeId={stocktakeId}
                        consumableId={item.consumable_id}
                        binId={item.location_id}
                        label={t('stocktake.quantities.additionalFor', {
                          name,
                          bin: locations.name(item.location_id),
                        })}
                      />
                    </td>
                  ) : null}
                </tr>
              );
            })}
            {rows.length === 0 && !loading ? (
              <tr>
                <td colSpan={countable ? 6 : 5} className={cn(tdClasses, 'text-fg-muted')}>
                  {t('stocktake.snapshot.noConsumables')}
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </ScrollRegion>
    </Card>
  );
}

/** signed shows a difference with its sign. */
function signed(value: number): string {
  return value > 0 ? `+${value}` : String(value);
}

function RowCount({
  stocktakeId,
  consumableId,
  binId,
  label,
}: {
  stocktakeId: string;
  consumableId: string;
  binId: string;
  label: string;
}) {
  const { t } = useTranslation();
  const addScan = useAddScan(stocktakeId);
  const [value, setValue] = useState('');
  const [error, setError] = useState<string>();
  const quantity = Number(value);
  return (
    <form
      className="flex items-center gap-2"
      onSubmit={(event) => {
        event.preventDefault();
        if (!(quantity > 0)) return;
        setError(undefined);
        addScan.mutate(
          { consumable_id: consumableId, quantity, location_id: binId, scan_method: 'manual' },
          { onSuccess: () => setValue(''), onError: (err) => setError(errorDetail(err)) },
        );
      }}
    >
      <input
        type="number"
        min={1}
        aria-label={label}
        value={value}
        onChange={(event) => setValue(event.target.value)}
        className="w-20 rounded-md border border-line bg-panel px-2 py-1 text-sm tabular-nums text-fg"
      />
      <Button
        type="submit"
        size="sm"
        variant="secondary"
        disabled={!(quantity > 0) || addScan.isPending}
      >
        {t('stocktake.quantities.count')}
      </Button>
      {error ? (
        <span role="alert" className="text-xs text-danger">
          {error}
        </span>
      ) : null}
    </form>
  );
}

function RecentScans({
  scans,
  names,
  locations,
}: {
  scans: StockScan[];
  names: ObjectNames;
  locations: LocationNames;
}) {
  const { t, i18n } = useTranslation();
  if (scans.length === 0) return null;
  return (
    <Card title={t('stocktake.recentScans')} className="p-0 overflow-hidden">
      <ScrollRegion label={t('stocktake.recentScans')}>
        <table className={tableClasses}>
          <thead className={theadClasses}>
            <tr>
              <th scope="col" className={thClasses}>
                {t('stocktake.columns.scannedAt')}
              </th>
              <th scope="col" className={thClasses}>
                {t('stocktake.columns.object')}
              </th>
              <th scope="col" className={thClasses}>
                {t('stocktake.columns.foundLocation')}
              </th>
              <th scope="col" className={thClasses}>
                {t('stocktake.columns.method')}
              </th>
              <th scope="col" className={thClasses}>
                {t('stocktake.columns.result')}
              </th>
            </tr>
          </thead>
          <tbody>
            {scans.slice(0, RECENT_SCANS).map((scan) => (
              <tr key={scan.id} className={trClasses}>
                <td className={tdClasses}>{formatDateTime(scan.scanned_at, i18n.language)}</td>
                <td className={tdPrimaryClasses}>
                  {scan.asset_id
                    ? names.asset(scan.asset_id)
                    : `${names.consumable(scan.consumable_id)} × ${scan.quantity ?? 0}`}
                </td>
                <td className={tdClasses}>
                  {scan.location_id ? locations.path(scan.location_id) : '—'}
                </td>
                <td className={tdClasses}>
                  {t(`stocktake.scanMethods.${scan.scan_method}`, scan.scan_method)}
                </td>
                <td className={tdClasses}>
                  <Badge variant={resultVariant(scan.scan_result)}>
                    {t(`stocktake.results.${scan.scan_result}`, scan.scan_result)}
                  </Badge>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </ScrollRegion>
    </Card>
  );
}

// ─── Differences and completion ─────────────────────────────────────────────

/** moved tells an asset found at another place than expected (or not expected at all). */
function moved(row: StocktakeDifference): boolean {
  return (
    row.item_kind === 'asset' &&
    !!row.found_location_id &&
    (row.classification === 'unexpected' || row.found_location_id !== row.expected_location_id)
  );
}

/** differenceLabel names the kind of an asset difference: missing, unexpected or moved. */
function differenceLabel(t: TFunction, row: StocktakeDifference): string {
  if (row.classification === 'found') return t('stocktake.differences.moved');
  return t(`stocktake.results.${row.classification}`, row.classification);
}

interface DifferenceListProps {
  rows: StocktakeDifference[];
  loading: boolean;
  names: ObjectNames;
  locations: LocationNames;
  onLabel?: (asset: { id: string; name: string }) => void;
}

function DifferenceList({ rows, loading, names, locations, onLabel }: DifferenceListProps) {
  const { t } = useTranslation();
  const assets = rows.filter((row) => row.item_kind === 'asset');
  const quantities = rows.filter((row) => row.item_kind === 'consumable');
  const assetName = (row: StocktakeDifference) => row.asset?.name ?? names.asset(row.asset_id);
  const consumableName = (row: StocktakeDifference) =>
    row.consumable?.name ?? names.consumable(row.consumable_id);
  return (
    <Card title={t('stocktake.differences.title')}>
      {loading ? <SkeletonList rows={3} label={t('app.loading')} /> : null}
      <div className="space-y-4">
        <section>
          <h3 className="mb-2 text-sm font-semibold text-fg">
            {t('stocktake.differences.assets')}
          </h3>
          {assets.length === 0 ? (
            <p className="text-sm text-fg-muted">{t('stocktake.differences.none')}</p>
          ) : (
            <ScrollRegion label={t('stocktake.differences.assets')}>
              <table className={tableClasses}>
                <thead className={theadClasses}>
                  <tr>
                    <th scope="col" className={thClasses}>
                      {t('stocktake.columns.asset')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('stocktake.columns.difference')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('stocktake.columns.expectedLocation')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('stocktake.columns.foundLocation')}
                    </th>
                    {onLabel ? (
                      <th scope="col" className={cn(thClasses, 'text-right')}>
                        {t('common.actions')}
                      </th>
                    ) : null}
                  </tr>
                </thead>
                <tbody>
                  {assets.map((row) => {
                    const name = assetName(row);
                    return (
                      <tr key={`a-${row.asset_id}`} className={trClasses}>
                        <td className={tdPrimaryClasses}>{name}</td>
                        <td className={tdClasses}>
                          <Badge
                            variant={resultVariant(
                              row.classification === 'found' ? 'unexpected' : row.classification,
                            )}
                          >
                            {differenceLabel(t, row)}
                          </Badge>
                        </td>
                        <td className={tdClasses}>
                          {row.expected_location_id
                            ? locations.path(row.expected_location_id)
                            : '—'}
                        </td>
                        <td className={tdClasses}>
                          {row.found_location_id ? locations.path(row.found_location_id) : '—'}
                        </td>
                        {onLabel ? (
                          <td className={cn(tdClasses, 'text-right')}>
                            <IconButton
                              variant="ghost"
                              icon={<Tag className="h-4 w-4" />}
                              label={t('stocktake.labelFor', { name })}
                              onClick={() => row.asset_id && onLabel({ id: row.asset_id, name })}
                            />
                          </td>
                        ) : null}
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </ScrollRegion>
          )}
        </section>
        <section>
          <h3 className="mb-2 text-sm font-semibold text-fg">
            {t('stocktake.differences.quantities')}
          </h3>
          {quantities.length === 0 ? (
            <p className="text-sm text-fg-muted">{t('stocktake.differences.none')}</p>
          ) : (
            <ScrollRegion label={t('stocktake.differences.quantities')}>
              <table className={tableClasses}>
                <thead className={theadClasses}>
                  <tr>
                    <th scope="col" className={thClasses}>
                      {t('stocktake.columns.consumable')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('stocktake.columns.bin')}
                    </th>
                    <th scope="col" className={cn(thClasses, 'text-right')}>
                      {t('stocktake.columns.expected')}
                    </th>
                    <th scope="col" className={cn(thClasses, 'text-right')}>
                      {t('stocktake.columns.counted')}
                    </th>
                    <th scope="col" className={cn(thClasses, 'text-right')}>
                      {t('stocktake.columns.difference')}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {quantities.map((row) => {
                    const bin = row.found_location_id ?? row.expected_location_id;
                    return (
                      <tr key={`c-${row.consumable_id}-${bin}`} className={trClasses}>
                        <td className={tdPrimaryClasses}>{consumableName(row)}</td>
                        <td className={tdClasses}>{bin ? locations.path(bin) : '—'}</td>
                        <td className={tdNumericClasses}>{row.expected_quantity}</td>
                        <td className={tdNumericClasses}>{row.counted_quantity}</td>
                        <td className={tdNumericClasses}>
                          {signed(row.counted_quantity - row.expected_quantity)}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </ScrollRegion>
          )}
        </section>
      </div>
    </Card>
  );
}

/**
 * PlannedMovements names the movements the completion is expected to book
 * from the difference list: a transfer for each asset found at another
 * place (found elsewhere or unexpected) and a correction per consumable and
 * bin. The stock at completion decides; the answer shows the booked ones.
 */
function PlannedMovements({
  rows,
  names,
  locations,
}: {
  rows: StocktakeDifference[];
  names: ObjectNames;
  locations: LocationNames;
}) {
  const { t } = useTranslation();
  const transfers = rows.filter(moved);
  const corrections = rows.filter(
    (row) => row.item_kind === 'consumable' && row.counted_quantity !== row.expected_quantity,
  );
  return (
    <div className="space-y-3 text-sm">
      <p>{t('stocktake.confirmComplete.body')}</p>
      {transfers.length === 0 && corrections.length === 0 ? (
        <p className="text-fg-muted">{t('stocktake.confirmComplete.none')}</p>
      ) : (
        <ul
          aria-label={t('stocktake.confirmComplete.movements')}
          className="list-disc space-y-1 pl-5"
        >
          {transfers.map((row) => (
            <li key={`t-${row.asset_id}`}>
              {t('stocktake.confirmComplete.transfer', {
                type: movementLabel(t, 'transfer'),
                name: row.asset?.name ?? names.asset(row.asset_id),
                from: row.expected_location_id ? locations.path(row.expected_location_id) : '—',
                to: locations.path(row.found_location_id),
              })}
            </li>
          ))}
          {corrections.map((row) => {
            const bin = row.found_location_id ?? row.expected_location_id;
            return (
              <li key={`c-${row.consumable_id}-${bin}`}>
                {t('stocktake.confirmComplete.correction', {
                  type: movementLabel(t, 'correction'),
                  name: row.consumable?.name ?? names.consumable(row.consumable_id),
                  bin: bin ? locations.path(bin) : '—',
                  delta: signed(row.counted_quantity - row.expected_quantity),
                })}
              </li>
            );
          })}
        </ul>
      )}
      <p className="text-fg-muted">{t('stocktake.confirmComplete.note')}</p>
    </div>
  );
}

function CompletionResult({
  completion,
  names,
  locations,
}: {
  completion: StocktakeCompletion;
  names: ObjectNames;
  locations: LocationNames;
}) {
  const { t } = useTranslation();
  return (
    <Card title={t('stocktake.completion.title')}>
      <p role="status" className="text-sm">
        {t('stocktake.completion.summary', { number: completion.corrections_applied })}
      </p>
      {completion.corrections.length > 0 ? (
        <ul className="mt-2 list-disc space-y-1 pl-5 text-sm">
          {completion.corrections.map((row) => (
            <li key={row.movement_id}>
              {t('stocktake.completion.movement', {
                type: movementLabel(t, row.movement_type),
                name:
                  row.item_kind === 'asset'
                    ? names.asset(row.asset_id)
                    : names.consumable(row.consumable_id),
                from: row.from_location_id ? locations.path(row.from_location_id) : '—',
                to: row.to_location_id ? locations.path(row.to_location_id) : '—',
                quantity: row.quantity ?? 1,
              })}
            </li>
          ))}
        </ul>
      ) : null}
    </Card>
  );
}

// ─── Dialogs ─────────────────────────────────────────────────────────────────

interface ConfirmDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  confirmLabel: string;
  busy: boolean;
  danger?: boolean;
  onConfirm: () => void;
  children: React.ReactNode;
}

function ConfirmDialog({
  open,
  onOpenChange,
  title,
  confirmLabel,
  busy,
  danger,
  onConfirm,
  children,
}: ConfirmDialogProps) {
  const { t } = useTranslation();
  return (
    <Modal open={open} onOpenChange={onOpenChange} title={title}>
      <div className="space-y-4">
        {children}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={() => onOpenChange(false)}>
            {t('common.cancel')}
          </Button>
          <Button variant={danger ? 'danger' : 'primary'} loading={busy} onClick={onConfirm}>
            {confirmLabel}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function EditDialog({ stocktake: st, onClose }: { stocktake: Stocktake; onClose: () => void }) {
  const { t } = useTranslation();
  const update = useUpdateStocktake(st.id);
  const [form, setForm] = useState({
    title: st.title,
    description: st.description ?? '',
    due_date: st.due_date?.slice(0, 10) ?? '',
  });
  const [area, setArea] = useState<string | null>(st.location_id ?? null);
  const [error, setError] = useState<string>();
  const planned = st.status === 'planned';

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!form.title.trim()) return;
    setError(undefined);
    update.mutate(
      {
        title: form.title.trim(),
        description: form.description,
        ...(form.due_date ? { due_date: form.due_date } : {}),
        ...(planned && area ? { location_id: area } : {}),
      },
      { onSuccess: onClose, onError: (err) => setError(errorDetail(err)) },
    );
  };

  return (
    <Modal open onOpenChange={(open) => !open && onClose()} title={t('stocktake.edit')}>
      <form className="space-y-4" onSubmit={submit}>
        <Input
          label={t('stocktake.name')}
          value={form.title}
          onChange={(event) => setForm({ ...form, title: event.target.value })}
          required
        />
        <Input
          label={t('common.description')}
          value={form.description}
          onChange={(event) => setForm({ ...form, description: event.target.value })}
        />
        <Input
          type="date"
          label={t('stocktake.dueDate')}
          value={form.due_date}
          onChange={(event) => setForm({ ...form, due_date: event.target.value })}
        />
        {planned ? (
          <LocationPicker
            label={t('stocktake.area')}
            value={area}
            onChange={(next) => setArea(next?.id ?? null)}
          />
        ) : null}
        {error ? (
          <p role="alert" className="text-sm text-danger">
            {error}
          </p>
        ) : null}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
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
