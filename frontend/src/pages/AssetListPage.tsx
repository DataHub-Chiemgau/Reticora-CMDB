/**
 * AssetListPage — the asset list (UI-14, WP-319): columns chosen from the
 * fields of Asset, sort by a column of the sort whitelist, the filters
 * lifecycle_state, category and search, keyset pages over next_cursor and
 * saved views (entity_kind asset) for columns, sort and these filters. The
 * creation is guided (UI-12): a tile per category, then the required fields
 * and the details on demand. Lifecycle changes, the CI link, supplier,
 * contract and owner live on the detail page (WP-809).
 */
import { ArrowDown, ArrowUp, ChevronDown, Columns3, Package, Plus, Search } from 'lucide-react';
import { useMemo, useState } from 'react';
import type { FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { ApiError, type Asset, type AssetCreateRequest, type FieldErrorInfo } from '../api/client';
import type { SavedView, SavedViewSortKey } from '../api/cmdb';
import { useQuery } from '@tanstack/react-query';
import { lifecycleApi } from '../api/cmdb';
import { useSavedViews, useSaveView } from '../api/cmdbHooks';
import { useAssetList, useCreateAsset, useDeleteAsset } from '../api/hooks';
import { useLocationNames } from '../components/cmdb/ObjectHistory';
import { TypeTiles, type TileType } from '../components/create/TypeTiles';
import { Card } from '../components/ui/Card';
import { Button } from '../components/ui/Button';
import { Input } from '../components/ui/Input';
import { Badge } from '../components/ui/Badge';
import { ErrorState } from '../components/ui/ErrorState';
import { Modal } from '../components/ui/Modal';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { Toolbar, ToolbarEnd } from '../components/ui/Toolbar';
import {
  tableClasses,
  tdClasses,
  tdMonoClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';
import { cn } from '../components/ui/utils';
import { useUndoableDelete } from '../hooks/useUndoableDelete';
import { useToastStore } from '../stores/toast';
import { ScrollRegion } from '../components/ScrollRegion';
import { TableEmptyRow } from './ListParts';

// Stored asset values (English keys); the labels come from the catalog.
export const ASSET_CATEGORIES = [
  'hardware',
  'software',
  'license',
  'cloud_resource',
  'accessory',
  'other',
] as const;

/**
 * ASSET_LIFECYCLE_STATES are the states of the system model physical_asset
 * (migration 000055) in their order; labels under lifecycle.states. A state
 * of an organization model without catalog entry shows the model's label.
 */
export const ASSET_LIFECYCLE_STATES = [
  'ordered',
  'received',
  'in_stock',
  'reserved',
  'preparing',
  'deployed',
  'repair',
  'retired',
  'disposed',
] as const;

/**
 * ASSET_COLUMNS are the selectable columns: fields of Asset that the field
 * table of the saved views knows (astFields["asset"], WP-309), so each one
 * passes columns and sort of ValidateView.
 */
export const ASSET_COLUMNS = [
  'inventory_no',
  'name',
  'category',
  'manufacturer',
  'model',
  'serial_number',
  'barcode',
  'rfid_tag',
  'lifecycle_state',
  'location_id',
  'cost_center',
  'purchase_date',
  'purchase_price',
  'currency',
  'warranty_until',
] as const;

export type AssetColumn = (typeof ASSET_COLUMNS)[number];

export const DEFAULT_ASSET_COLUMNS: readonly AssetColumn[] = [
  'inventory_no',
  'name',
  'category',
  'lifecycle_state',
  'location_id',
];

/** ASSET_SORTABLE are the columns of the sort whitelist of GET /assets (WP-271, WP-740). */
export const ASSET_SORTABLE: ReadonlySet<string> = new Set([
  'inventory_no',
  'name',
  'category',
  'manufacturer',
  'model',
  'serial_number',
  'lifecycle_state',
  'purchase_date',
  'warranty_until',
]);

/** VIEW_FILTER_KEYS are the filter_spec members the list applies itself. */
const VIEW_FILTER_KEYS = new Set(['entity_kind', 'lifecycle', 'asset_category', 'search']);

/** TRI_STATE_KEYS are the filter_spec members whose false selects rows. */
const TRI_STATE_KEYS = new Set(['has_owner', 'reserved']);

/** isExtendedView reports a view with conditions beyond lifecycle, category and search. */
export function isExtendedView(view: Pick<SavedView, 'filter_spec'>): boolean {
  return Object.entries(view.filter_spec ?? {}).some(([key, value]) => {
    if (VIEW_FILTER_KEYS.has(key)) return false;
    if (value === undefined || value === null || value === '') return false;
    // has_owner and reserved are tri-state: false is a condition as well.
    if (value === false) return TRI_STATE_KEYS.has(key);
    if (Array.isArray(value)) return value.length > 0;
    if (typeof value === 'object') return Object.keys(value).length > 0;
    return true;
  });
}

function isAssetColumn(field: string): field is AssetColumn {
  return (ASSET_COLUMNS as readonly string[]).includes(field);
}

/** sortParam renders the first sort key of a view as sort of GET /assets. */
function sortParam(keys: readonly SavedViewSortKey[] | undefined): string {
  const first = keys?.[0];
  if (!first || !ASSET_SORTABLE.has(first.field)) return '';
  return first.dir === 'desc' ? `-${first.field}` : first.field;
}

/** sortKeys renders sort of GET /assets as the sort keys of a view. */
function sortKeys(sort: string): SavedViewSortKey[] {
  if (!sort) return [];
  return sort.startsWith('-')
    ? [{ field: sort.slice(1), dir: 'desc' }]
    : [{ field: sort, dir: 'asc' }];
}

function lifecycleVariant(state: string) {
  switch (state) {
    case 'in_stock':
      return 'success' as const;
    case 'deployed':
    case 'reserved':
      return 'info' as const;
    case 'preparing':
    case 'repair':
    case 'ordered':
    case 'received':
      return 'warning' as const;
    case 'retired':
    case 'disposed':
      return 'danger' as const;
    default:
      return 'neutral' as const;
  }
}

export function AssetListPage() {
  const { t, i18n } = useTranslation();
  const categoryValues = ASSET_CATEGORIES.map((value) => ({
    value,
    label: t(`asset.categories.${value}`, value),
  }));
  const lifecycleValues = ASSET_LIFECYCLE_STATES.map((value) => ({
    value,
    label: t(`lifecycle.states.${value}`, value),
  }));
  const lifecycleOptions = [
    { value: '', label: t('asset.allLifecycleStates') },
    ...lifecycleValues,
  ];
  const categoryOptions = [{ value: '', label: t('common.allCategories') }, ...categoryValues];

  const [search, setSearch] = useState('');
  const [lifecycle, setLifecycle] = useState('');
  const [category, setCategory] = useState('');
  const [columns, setColumns] = useState<AssetColumn[]>([...DEFAULT_ASSET_COLUMNS]);
  const [sort, setSort] = useState('');
  // cursors is the stack of the cursors of the pages before the current one.
  const [cursors, setCursors] = useState<string[]>([]);
  const [viewId, setViewId] = useState('');
  const [showCreate, setShowCreate] = useState(false);
  const [showSaveView, setShowSaveView] = useState(false);
  const pushToast = useToastStore((state) => state.push);
  const { scheduleDelete } = useUndoableDelete();

  const cursor = cursors.length > 0 ? cursors[cursors.length - 1] : undefined;
  const { data, isLoading, error, refetch } = useAssetList(
    {
      search,
      category,
      filter: lifecycle ? [{ field: 'lifecycle_state', value: lifecycle }] : [],
      fields: columns,
      sort: sort || undefined,
      cursor,
    },
    { keepPrevious: true },
  );
  const deleteMutation = useDeleteAsset();
  const views = useSavedViews();
  const assetViews = useMemo(
    () => (views.data?.data ?? []).filter((view) => view.entity_kind === 'asset'),
    [views.data],
  );
  const selectedView = assetViews.find((view) => view.id === viewId);
  const locations = useLocationNames(columns.includes('location_id'));

  const assetList = useMemo(() => data?.data ?? [], [data]);
  // The labels of an organization model are read only for a state the
  // catalog lacks.
  const needsModelLabels = assetList.some(
    (asset) => asset.lifecycle_state && !i18n.exists(`lifecycle.states.${asset.lifecycle_state}`),
  );
  const models = useLifecycleDefinitionsWhen(needsModelLabels);
  const modelLabels = useMemo(() => {
    const labels = new Map<string, string>();
    for (const model of models ?? []) {
      if (model.applies_to !== 'asset') continue;
      for (const state of model.states ?? []) {
        if (!labels.has(state.key)) labels.set(state.key, state.label);
      }
    }
    return labels;
  }, [models]);

  function resetPages() {
    setCursors([]);
  }

  function toggleColumn(column: AssetColumn) {
    setColumns((current) => {
      if (current.includes(column)) {
        return current.length > 1 ? current.filter((entry) => entry !== column) : current;
      }
      return ASSET_COLUMNS.filter((entry) => entry === column || current.includes(entry));
    });
    resetPages();
  }

  function toggleSort(column: AssetColumn) {
    setSort((current) => (current === column ? `-${column}` : column));
    resetPages();
  }

  function applyView(id: string) {
    setViewId(id);
    resetPages();
    const view = assetViews.find((entry) => entry.id === id);
    if (!view) return;
    if (isExtendedView(view)) return;
    const viewColumns = (view.columns ?? []).filter(isAssetColumn);
    setColumns(viewColumns.length > 0 ? viewColumns : [...DEFAULT_ASSET_COLUMNS]);
    setSort(sortParam(view.sort));
    const spec = view.filter_spec ?? {};
    setLifecycle(typeof spec.lifecycle === 'string' ? spec.lifecycle : '');
    setCategory(typeof spec.asset_category === 'string' ? spec.asset_category : '');
    setSearch(typeof spec.search === 'string' ? spec.search : '');
  }

  function deleteAssetWithUndo(asset: Asset) {
    const label = asset.name ?? asset.inventory_no ?? asset.id;
    // The DELETE request is deferred behind the undo window; "Undo" simply
    // cancels the pending request, so the asset is never actually removed.
    scheduleDelete({
      id: asset.id,
      label,
      deleteFn: () =>
        deleteMutation.mutateAsync(asset.id).catch((err: unknown) => {
          // An asset with movements or a disposal record stays (409, WP-271).
          if (err instanceof ApiError && err.status === 409) {
            pushToast({
              message: err.detail || t('asset.deleteConflict'),
              variant: 'error',
            });
            return;
          }
          throw err;
        }),
      restoreFn: () => Promise.resolve(),
    });
  }

  function lifecycleLabel(state: string) {
    return t(`lifecycle.states.${state}`, modelLabels.get(state) ?? state);
  }

  function renderCell(asset: Asset, column: AssetColumn) {
    const raw = (asset as Record<string, unknown>)[column];
    // A field the field permissions leave out of the projection is absent.
    if (raw === undefined || raw === null || raw === '') return '—';
    const value = String(raw);
    switch (column) {
      case 'category':
        return t(`asset.categories.${value}`, value);
      case 'lifecycle_state':
        return <Badge variant={lifecycleVariant(value)}>{lifecycleLabel(value)}</Badge>;
      case 'location_id':
        return locations.name(value) ?? '—';
      default:
        return value;
    }
  }

  function cellClass(column: AssetColumn) {
    if (column === 'name') return tdPrimaryClasses;
    if (column === 'inventory_no' || column === 'serial_number' || column === 'barcode') {
      return tdMonoClasses;
    }
    return tdClasses;
  }

  const filtered = Boolean(search || lifecycle || category);
  const totalCapped = Boolean((data as { total_capped?: boolean } | undefined)?.total_capped);

  return (
    <div className="space-y-4">
      <PageHeader
        title={t('asset.title')}
        description={t('asset.summary')}
        actions={
          <Button icon={<Plus />} onClick={() => setShowCreate(true)}>
            {t('asset.create')}
          </Button>
        }
      />

      <Toolbar>
        <Input
          type="text"
          icon={<Search />}
          placeholder={t('common.search')}
          aria-label={t('common.search')}
          value={search}
          onChange={(e) => {
            setSearch(e.target.value);
            resetPages();
          }}
          className="w-full sm:w-64"
        />
        <Select
          aria-label={t('asset.lifecycleFilter')}
          value={lifecycle}
          onChange={(e) => {
            setLifecycle(e.target.value);
            resetPages();
          }}
          options={lifecycleOptions}
          className="w-40 sm:w-44"
        />
        <Select
          aria-label={t('asset.category')}
          value={category}
          onChange={(e) => {
            setCategory(e.target.value);
            resetPages();
          }}
          options={categoryOptions}
          className="w-40 sm:w-44"
        />
        <ColumnChooser columns={columns} onToggle={toggleColumn} />
        <Select
          aria-label={t('asset.views.label')}
          value={viewId}
          onChange={(e) => applyView(e.target.value)}
          options={[
            { value: '', label: t('asset.views.none') },
            ...assetViews.map((view) => ({
              value: view.id,
              label: isExtendedView(view)
                ? t('asset.views.extended', { name: view.name })
                : view.name,
            })),
          ]}
          className="w-44 sm:w-52"
        />
        <Button variant="secondary" onClick={() => setShowSaveView(true)}>
          {t('asset.views.save')}
        </Button>
        {data ? (
          <ToolbarEnd>
            <span className="text-sm text-fg-muted tabular-nums">
              {totalCapped ? t('asset.totalCapped', { count: data.total }) : data.total}{' '}
              {t('common.entries')}
            </span>
          </ToolbarEnd>
        ) : null}
      </Toolbar>

      {selectedView && isExtendedView(selectedView) ? (
        <p role="status" className="rounded-lg border border-line bg-panel px-4 py-3 text-sm">
          {t('asset.views.extendedHint')}{' '}
          <Link to="/saved-views" className="font-medium text-accent underline">
            {t('asset.views.open')}
          </Link>
        </p>
      ) : null}

      {isLoading ? <SkeletonList rows={5} label={t('app.loading')} /> : null}
      {error ? (
        <ErrorState
          title={t('app.error')}
          description={error instanceof Error ? error.message : undefined}
          retryLabel={t('common.retry')}
          onRetry={() => void refetch()}
        />
      ) : null}

      {data ? (
        <Card className="p-0 overflow-hidden">
          <ScrollRegion label={t('asset.title')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  {columns.map((column) => (
                    <ColumnHeader
                      key={column}
                      column={column}
                      sort={sort}
                      onSort={() => toggleSort(column)}
                    />
                  ))}
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    <span className="sr-only">{t('common.actions')}</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {assetList.map((asset) => (
                  <tr key={asset.id} className={trClasses}>
                    {columns.map((column) => (
                      <td key={column} className={cellClass(column)}>
                        {renderCell(asset, column)}
                      </td>
                    ))}
                    <td className={cn(tdClasses, 'text-right')}>
                      <Button variant="ghost" size="sm" onClick={() => deleteAssetWithUndo(asset)}>
                        {t('common.delete')}
                      </Button>
                    </td>
                  </tr>
                ))}
                {assetList.length === 0 ? (
                  <TableEmptyRow
                    colSpan={columns.length + 1}
                    icon={<Package />}
                    title={filtered ? t('common.noResults') : t('asset.emptyTitle')}
                    description={filtered ? t('common.noResultsHint') : t('asset.emptyHint')}
                  />
                ) : null}
              </tbody>
            </table>
          </ScrollRegion>
          {cursors.length > 0 || data.next_cursor ? (
            <nav
              aria-label={t('asset.pagination')}
              className="flex justify-end gap-2 border-t border-line px-4 py-2.5"
            >
              <Button
                variant="secondary"
                size="sm"
                disabled={cursors.length === 0}
                onClick={() => setCursors((current) => current.slice(0, -1))}
              >
                {t('asset.previousPage')}
              </Button>
              <Button
                variant="secondary"
                size="sm"
                disabled={!data.next_cursor}
                onClick={() => {
                  const next = data.next_cursor;
                  if (next) setCursors((current) => [...current, next]);
                }}
              >
                {t('asset.nextPage')}
              </Button>
            </nav>
          ) : null}
        </Card>
      ) : null}

      <Modal open={showCreate} onOpenChange={setShowCreate} title={t('asset.create')} size="lg">
        {showCreate ? <GuidedAssetCreate onDone={() => setShowCreate(false)} /> : null}
      </Modal>

      <Modal open={showSaveView} onOpenChange={setShowSaveView} title={t('asset.views.saveTitle')}>
        {showSaveView ? (
          <SaveViewForm
            columns={columns}
            sort={sort}
            filterSpec={{
              ...(lifecycle ? { lifecycle } : {}),
              ...(category ? { asset_category: category } : {}),
              ...(search ? { search } : {}),
            }}
            onSaved={(id) => {
              setShowSaveView(false);
              setViewId(id);
            }}
            onCancel={() => setShowSaveView(false)}
          />
        ) : null}
      </Modal>
    </div>
  );
}

/** useLifecycleDefinitionsWhen reads the lifecycle models only when enabled. */
function useLifecycleDefinitionsWhen(enabled: boolean) {
  // The query key of useLifecycleDefinitions, so both share the cache.
  const query = useQuery({
    queryKey: ['lifecycle-models'],
    queryFn: () => lifecycleApi.list({ limit: 100 }),
    enabled,
  });
  return enabled ? query.data?.data : undefined;
}

function ColumnHeader({
  column,
  sort,
  onSort,
}: {
  column: AssetColumn;
  sort: string;
  onSort: () => void;
}) {
  const { t } = useTranslation();
  const label = t(`asset.columns.${column}`);
  if (!ASSET_SORTABLE.has(column)) {
    return (
      <th scope="col" className={thClasses}>
        {label}
      </th>
    );
  }
  const direction = sort === column ? 'ascending' : sort === `-${column}` ? 'descending' : 'none';
  return (
    <th scope="col" className={thClasses} aria-sort={direction}>
      <button
        type="button"
        onClick={onSort}
        aria-label={t('asset.sortBy', { column: label })}
        className="inline-flex items-center gap-1 font-medium hover:text-fg focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent"
      >
        {label}
        {direction === 'ascending' ? <ArrowUp aria-hidden="true" className="h-3.5 w-3.5" /> : null}
        {direction === 'descending' ? (
          <ArrowDown aria-hidden="true" className="h-3.5 w-3.5" />
        ) : null}
      </button>
    </th>
  );
}

function ColumnChooser({
  columns,
  onToggle,
}: {
  columns: readonly AssetColumn[];
  onToggle: (column: AssetColumn) => void;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  return (
    <div className="relative">
      <Button
        variant="secondary"
        icon={<Columns3 />}
        aria-expanded={open}
        onClick={() => setOpen((current) => !current)}
      >
        {t('asset.columnsButton')}
        <ChevronDown aria-hidden="true" className="ml-1 h-4 w-4" />
      </Button>
      {open ? (
        <fieldset className="absolute z-20 mt-1 grid w-64 gap-1 rounded-lg border border-line bg-panel p-3 shadow-lg">
          <legend className="sr-only">{t('asset.columnsLegend')}</legend>
          {ASSET_COLUMNS.map((column) => (
            <label key={column} className="flex items-center gap-2 text-sm text-fg">
              <input
                type="checkbox"
                checked={columns.includes(column)}
                disabled={columns.length === 1 && columns.includes(column)}
                onChange={() => onToggle(column)}
              />
              {t(`asset.columns.${column}`)}
            </label>
          ))}
        </fieldset>
      ) : null}
    </div>
  );
}

function SaveViewForm({
  columns,
  sort,
  filterSpec,
  onSaved,
  onCancel,
}: {
  columns: readonly AssetColumn[];
  sort: string;
  filterSpec: Record<string, string>;
  onSaved: (id: string) => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  const pushToast = useToastStore((state) => state.push);
  const saveView = useSaveView();
  const [name, setName] = useState('');
  const [failure, setFailure] = useState('');

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!name.trim()) return;
    setFailure('');
    try {
      const view = (await saveView.mutateAsync({
        name: name.trim(),
        entity_kind: 'asset',
        filter_spec: filterSpec,
        columns: [...columns],
        sort: sortKeys(sort),
      })) as SavedView | undefined;
      pushToast({ message: t('asset.views.saved', { name: name.trim() }), variant: 'success' });
      onSaved(view?.id ?? '');
    } catch (err) {
      setFailure(err instanceof Error && err.message ? err.message : t('asset.views.saveError'));
    }
  }

  return (
    <form className="space-y-4" onSubmit={(event) => void submit(event)}>
      <Input
        label={t('asset.views.name')}
        value={name}
        onChange={(e) => setName(e.target.value)}
        required
        autoFocus
      />
      {failure ? (
        <p role="alert" className="text-sm text-danger">
          {failure}
        </p>
      ) : null}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="secondary" onClick={onCancel}>
          {t('common.cancel')}
        </Button>
        <Button type="submit" loading={saveView.isPending} disabled={!name.trim()}>
          {t('common.save')}
        </Button>
      </div>
    </form>
  );
}

/** CATEGORY_ICONS are the tile icons of the asset categories (TypeTiles icon names). */
const CATEGORY_ICONS: Record<(typeof ASSET_CATEGORIES)[number], string> = {
  hardware: 'laptop',
  software: 'app-window',
  license: 'lock',
  cloud_resource: 'cloud',
  accessory: 'keyboard',
  other: 'package',
};

/** The group of all category tiles; its heading is asset.guided.categories. */
const TILE_GROUP = 'categories';

const REQUIRED_FIELDS = ['inventory_no', 'name'] as const;
const DEVICE_FIELDS = ['manufacturer', 'model'] as const;
const DETAIL_FIELDS = [
  'serial_number',
  'barcode',
  'rfid_tag',
  'purchase_date',
  'purchase_price',
  'currency',
  'warranty_until',
  'cost_center',
] as const;
type CreateField =
  | (typeof REQUIRED_FIELDS)[number]
  | (typeof DEVICE_FIELDS)[number]
  | (typeof DETAIL_FIELDS)[number];

const FIELD_TYPES: Partial<Record<CreateField, string>> = {
  purchase_date: 'date',
  warranty_until: 'date',
  purchase_price: 'number',
};

/**
 * GuidedAssetCreate is the guided creation of an asset (UI-12): first the
 * category as tiles (TypeTiles of WP-317), then inventory number and name,
 * manufacturer and model, and the details folded. It sets neither location
 * nor lifecycle; supplier, contract and owner follow on the detail page.
 * A 409 on inventory_no, barcode or rfid_tag and a 422 appear at the field.
 */
export function GuidedAssetCreate({ onDone }: { onDone: () => void }) {
  const { t } = useTranslation();
  const createMutation = useCreateAsset();
  const [category, setCategory] = useState('');
  const [values, setValues] = useState<Record<CreateField, string>>(
    () =>
      Object.fromEntries(
        [...REQUIRED_FIELDS, ...DEVICE_FIELDS, ...DETAIL_FIELDS].map((field) => [field, '']),
      ) as Record<CreateField, string>,
  );
  const [showDetails, setShowDetails] = useState(false);
  const [fieldErrors, setFieldErrors] = useState<Record<string, FieldErrorInfo>>({});
  const [failure, setFailure] = useState('');

  const tiles: TileType[] = ASSET_CATEGORIES.map((value) => ({
    id: value,
    label: t(`asset.categories.${value}`, value),
    category: TILE_GROUP,
    icon: CATEGORY_ICONS[value],
  }));

  function setValue(field: CreateField, value: string) {
    setValues((current) => ({ ...current, [field]: value }));
    setFieldErrors((current) => {
      if (!(field in current)) return current;
      const next = { ...current };
      delete next[field];
      return next;
    });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!values.inventory_no.trim() || !values.name.trim()) return;
    setFailure('');
    setFieldErrors({});
    const body: AssetCreateRequest = {
      inventory_no: values.inventory_no.trim(),
      name: values.name.trim(),
      category,
    };
    for (const field of [...DEVICE_FIELDS, ...DETAIL_FIELDS]) {
      const value = values[field].trim();
      if (value !== '') body[field] = value;
    }
    try {
      await createMutation.mutateAsync(body);
      onDone();
    } catch (err) {
      if (err instanceof ApiError) {
        const errors = err.fieldErrors();
        setFieldErrors(errors);
        if (DETAIL_FIELDS.some((field) => field in errors)) setShowDetails(true);
        const known = Object.keys(errors).some((field) =>
          (REQUIRED_FIELDS as readonly string[])
            .concat(DEVICE_FIELDS, DETAIL_FIELDS)
            .includes(field),
        );
        if (!known) setFailure(err.message || t('asset.guided.submitError'));
        return;
      }
      setFailure(t('asset.guided.submitError'));
    }
  }

  if (!category) {
    return (
      <div className="space-y-4">
        <p className="text-sm text-fg-muted">{t('asset.guided.chooseCategory')}</p>
        <TypeTiles
          types={tiles}
          onSelect={setCategory}
          categories={[TILE_GROUP]}
          categoryPrefix="asset.guided"
        />
        <div className="flex justify-end">
          <Button variant="secondary" onClick={onDone}>
            {t('common.cancel')}
          </Button>
        </div>
      </div>
    );
  }

  const input = (field: CreateField, required = false) => (
    <Input
      key={field}
      label={t(`asset.columns.${field}`)}
      type={FIELD_TYPES[field] ?? 'text'}
      step={field === 'purchase_price' ? '0.01' : undefined}
      value={values[field]}
      onChange={(e) => setValue(field, e.target.value)}
      error={fieldErrors[field]?.message}
      required={required}
    />
  );

  return (
    <form className="space-y-4" onSubmit={(event) => void submit(event)} noValidate>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-sm font-medium text-fg">
          {t('asset.guided.chosen', { category: t(`asset.categories.${category}`, category) })}
        </span>
        <Button type="button" variant="ghost" size="sm" onClick={() => setCategory('')}>
          {t('asset.guided.change')}
        </Button>
      </div>
      <div className="grid gap-4 sm:grid-cols-2">
        {REQUIRED_FIELDS.map((field) => input(field, true))}
        {DEVICE_FIELDS.map((field) => input(field))}
      </div>
      <Button
        type="button"
        variant="ghost"
        size="sm"
        aria-expanded={showDetails}
        onClick={() => setShowDetails((current) => !current)}
      >
        {showDetails ? t('asset.guided.less') : t('asset.guided.more')}
      </Button>
      {showDetails ? (
        <div className="grid gap-4 sm:grid-cols-2">
          {DETAIL_FIELDS.map((field) => input(field))}
        </div>
      ) : null}
      {failure ? (
        <p role="alert" className="text-sm text-danger">
          {failure}
        </p>
      ) : null}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="secondary" onClick={onDone}>
          {t('common.cancel')}
        </Button>
        <Button
          type="submit"
          loading={createMutation.isPending}
          disabled={!values.inventory_no.trim() || !values.name.trim()}
        >
          {t('common.create')}
        </Button>
      </div>
    </form>
  );
}
