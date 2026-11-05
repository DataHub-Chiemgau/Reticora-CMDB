import { Package, Plus, Search } from 'lucide-react';
import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { Asset } from '../api/client';
import { useAssetList, useCreateAsset, useDeleteAsset, useUpdateAsset } from '../api/hooks';
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
export const ASSET_STATUSES = [
  'in_stock',
  'assigned',
  'maintenance',
  'retired',
  'disposed',
  'lost',
] as const;

export const ASSET_CATEGORIES = [
  'hardware',
  'software',
  'license',
  'cloud_resource',
  'accessory',
  'other',
] as const;

function statusVariant(status: string) {
  switch (status) {
    case 'in_stock':
      return 'success' as const;
    case 'assigned':
      return 'info' as const;
    case 'maintenance':
      return 'warning' as const;
    case 'retired':
    case 'disposed':
    case 'lost':
      return 'danger' as const;
    default:
      return 'neutral' as const;
  }
}

export function AssetListPage() {
  const { t } = useTranslation();
  const statusValues = ASSET_STATUSES.map((value) => ({
    value,
    label: t(`asset.statuses.${value}`, value),
  }));
  const categoryValues = ASSET_CATEGORIES.map((value) => ({
    value,
    label: t(`asset.categories.${value}`, value),
  }));
  const statusOptions = [{ value: '', label: t('common.allStatuses') }, ...statusValues];
  const categoryOptions = [{ value: '', label: t('common.allCategories') }, ...categoryValues];
  const [search, setSearch] = useState('');
  const [status, setStatus] = useState('');
  const [category, setCategory] = useState('');
  const [showCreate, setShowCreate] = useState(false);
  const [selectedIds, setSelectedIds] = useState<string[]>([]);
  const [bulkStatus, setBulkStatus] = useState('');
  const [bulkBusy, setBulkBusy] = useState(false);
  const pushToast = useToastStore((state) => state.push);
  const { scheduleDelete } = useUndoableDelete();

  const { data, isLoading, error, refetch } = useAssetList({ search, status, category });
  const createMutation = useCreateAsset();
  const deleteMutation = useDeleteAsset();
  const updateMutation = useUpdateAsset();

  useEffect(() => {
    if (!data?.data) {
      return;
    }
    const visible = new Set(data.data.map((asset) => asset.id));
    setSelectedIds((current) => current.filter((id) => visible.has(id)));
  }, [data]);

  const assetList = data?.data ?? [];
  const allVisibleSelected =
    assetList.length > 0 && assetList.every((asset) => selectedIds.includes(asset.id));

  function toggleSelectAll() {
    setSelectedIds(allVisibleSelected ? [] : assetList.map((asset) => asset.id));
  }

  function toggleSelect(id: string) {
    setSelectedIds((current) =>
      current.includes(id) ? current.filter((entry) => entry !== id) : [...current, id],
    );
  }

  async function applyBulkStatus() {
    if (!bulkStatus || selectedIds.length === 0) {
      return;
    }
    setBulkBusy(true);
    try {
      await Promise.all(
        selectedIds.map((id) => updateMutation.mutateAsync({ id, data: { status: bulkStatus } })),
      );
      pushToast({
        message: t('asset.bulk.success', { count: selectedIds.length }),
        variant: 'success',
      });
      setSelectedIds([]);
      setBulkStatus('');
    } catch {
      pushToast({ message: t('asset.bulk.error'), variant: 'error' });
    } finally {
      setBulkBusy(false);
    }
  }

  function deleteAssetWithUndo(asset: Asset) {
    // The DELETE request is deferred behind the undo window; "Undo" simply
    // cancels the pending request, so the asset is never actually removed.
    scheduleDelete({
      id: asset.id,
      label: asset.name,
      deleteFn: () => deleteMutation.mutateAsync(asset.id),
      restoreFn: () => Promise.resolve(),
    });
  }

  const [form, setForm] = useState({
    asset_tag: '',
    name: '',
    category: 'hardware',
    supplier: '',
    location: '',
  });

  const handleCreate = () => {
    if (!form.name || !form.asset_tag) return;
    createMutation.mutate(
      { ...form },
      {
        onSuccess: () => {
          setShowCreate(false);
          setForm({ asset_tag: '', name: '', category: 'hardware', supplier: '', location: '' });
        },
      },
    );
  };

  const filtered = Boolean(search || status || category);

  return (
    <div className="space-y-4">
      <PageHeader
        title={t('asset.title', 'Inventar')}
        description={t('asset.summary')}
        actions={
          <Button icon={<Plus />} onClick={() => setShowCreate(true)}>
            {t('common.create', 'Erstellen')}
          </Button>
        }
      />

      <Toolbar>
        <Input
          type="text"
          icon={<Search />}
          placeholder={t('common.search', 'Suche')}
          aria-label={t('common.search', 'Suche')}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className="w-full sm:w-64"
        />
        <Select
          aria-label={t('ci.status', 'Status')}
          value={status}
          onChange={(e) => setStatus(e.target.value)}
          options={statusOptions}
          className="w-40 sm:w-44"
        />
        <Select
          aria-label={t('asset.category', 'Kategorie')}
          value={category}
          onChange={(e) => setCategory(e.target.value)}
          options={categoryOptions}
          className="w-40 sm:w-44"
        />
        {data ? (
          <ToolbarEnd>
            <span className="text-sm text-fg-muted tabular-nums">
              {data.total} {t('common.entries')}
            </span>
          </ToolbarEnd>
        ) : null}
      </Toolbar>

      {isLoading ? <SkeletonList rows={5} label={t('app.loading', 'Laden...')} /> : null}
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
          {selectedIds.length > 0 ? (
            <div
              className="flex flex-wrap items-center gap-3 border-b border-line bg-accent-soft px-4 py-2.5"
              role="toolbar"
              aria-label={t('asset.bulk.toolbarLabel')}
            >
              <span className="text-sm font-medium text-fg">
                {t('asset.bulk.selected', { count: selectedIds.length })}
              </span>
              <Select
                value={bulkStatus}
                onChange={(event) => setBulkStatus(event.target.value)}
                aria-label={t('asset.bulk.setStatus')}
                className="max-w-xs"
                options={[{ value: '', label: t('asset.bulk.setStatus') }, ...statusValues]}
              />
              <Button
                size="sm"
                disabled={!bulkStatus}
                loading={bulkBusy}
                onClick={() => void applyBulkStatus()}
              >
                {t('asset.bulk.apply')}
              </Button>
              <Button variant="ghost" size="sm" onClick={() => setSelectedIds([])}>
                {t('asset.bulk.clearSelection')}
              </Button>
            </div>
          ) : null}
          <ScrollRegion label={t('asset.title', 'Inventar')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={cn(thClasses, 'w-10')}>
                    <input
                      type="checkbox"
                      checked={allVisibleSelected}
                      onChange={toggleSelectAll}
                      aria-label={t('asset.bulk.selectAll')}
                    />
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('asset.assetTag', 'Asset-Tag')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ci.name', 'Name')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('asset.category', 'Kategorie')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ci.status', 'Status')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('asset.location', 'Standort')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('common.actions', 'Aktionen')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {assetList.map((asset) => (
                  <tr key={asset.id} className={trClasses}>
                    <td className={tdClasses}>
                      <input
                        type="checkbox"
                        checked={selectedIds.includes(asset.id)}
                        onChange={() => toggleSelect(asset.id)}
                        aria-label={t('asset.bulk.selectRow', { name: asset.name })}
                      />
                    </td>
                    <td className={tdMonoClasses}>{asset.asset_tag || '—'}</td>
                    <td className={tdPrimaryClasses}>{asset.name}</td>
                    <td className={tdClasses}>
                      {t(`asset.categories.${asset.category}`, asset.category)}
                    </td>
                    <td className={tdClasses}>
                      <Badge variant={statusVariant(asset.status)}>
                        {t(`asset.statuses.${asset.status}`, asset.status)}
                      </Badge>
                    </td>
                    <td className={tdClasses}>{asset.location || '—'}</td>
                    <td className={cn(tdClasses, 'text-right')}>
                      <Button variant="ghost" size="sm" onClick={() => deleteAssetWithUndo(asset)}>
                        {t('common.delete', 'Löschen')}
                      </Button>
                    </td>
                  </tr>
                ))}
                {assetList.length === 0 ? (
                  <TableEmptyRow
                    colSpan={7}
                    icon={<Package />}
                    title={filtered ? t('common.noResults') : t('asset.emptyTitle')}
                    description={filtered ? t('common.noResultsHint') : t('asset.emptyHint')}
                  />
                ) : null}
              </tbody>
            </table>
          </ScrollRegion>
        </Card>
      ) : null}

      <Modal
        open={showCreate}
        onOpenChange={setShowCreate}
        title={t('asset.create', 'Asset erstellen')}
      >
        <div className="space-y-4">
          <Input
            label={t('asset.assetTag', 'Asset-Tag')}
            value={form.asset_tag}
            onChange={(e) => setForm({ ...form, asset_tag: e.target.value })}
            required
          />
          <Input
            label={t('ci.name', 'Name')}
            value={form.name}
            onChange={(e) => setForm({ ...form, name: e.target.value })}
            required
          />
          <Select
            label={t('asset.category', 'Kategorie')}
            value={form.category}
            onChange={(e) => setForm({ ...form, category: e.target.value })}
            options={categoryOptions.slice(1)}
          />
          <Input
            label={t('asset.supplier', 'Lieferant')}
            value={form.supplier}
            onChange={(e) => setForm({ ...form, supplier: e.target.value })}
          />
          <Input
            label={t('asset.location', 'Standort')}
            value={form.location}
            onChange={(e) => setForm({ ...form, location: e.target.value })}
          />
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setShowCreate(false)}>
              {t('common.cancel', 'Abbrechen')}
            </Button>
            <Button onClick={handleCreate}>{t('common.save', 'Speichern')}</Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
