import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { Asset } from '../api/client';
import { useAssetList, useCreateAsset, useDeleteAsset, useUpdateAsset } from '../api/hooks';
import { Card } from '../components/ui/Card';
import { Button } from '../components/ui/Button';
import { Input } from '../components/ui/Input';
import { Badge } from '../components/ui/Badge';
import { Modal } from '../components/ui/Modal';
import { Select } from '../components/ui/Select';
import { useUndoableDelete } from '../hooks/useUndoableDelete';
import { useToastStore } from '../stores/toast';

const statusOptions = [
  { value: '', label: 'Alle' },
  { value: 'in_stock', label: 'Auf Lager' },
  { value: 'assigned', label: 'Zugewiesen' },
  { value: 'maintenance', label: 'Wartung' },
  { value: 'retired', label: 'Ausgemustert' },
  { value: 'disposed', label: 'Entsorgt' },
  { value: 'lost', label: 'Verloren' },
];

const categoryOptions = [
  { value: '', label: 'Alle' },
  { value: 'hardware', label: 'Hardware' },
  { value: 'software', label: 'Software' },
  { value: 'license', label: 'Lizenz' },
  { value: 'cloud_resource', label: 'Cloud-Ressource' },
  { value: 'accessory', label: 'Zubehör' },
  { value: 'other', label: 'Sonstiges' },
];

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
  const [search, setSearch] = useState('');
  const [status, setStatus] = useState('');
  const [category, setCategory] = useState('');
  const [showCreate, setShowCreate] = useState(false);
  const [selectedIds, setSelectedIds] = useState<string[]>([]);
  const [bulkStatus, setBulkStatus] = useState('');
  const [bulkBusy, setBulkBusy] = useState(false);
  const pushToast = useToastStore((state) => state.push);
  const { scheduleDelete } = useUndoableDelete();

  const { data, isLoading } = useAssetList({ search, status, category });
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

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold text-gray-900 dark:text-white">
          {t('asset.title', 'Inventar')}
        </h1>
        <Button onClick={() => setShowCreate(true)}>{t('common.create', 'Erstellen')}</Button>
      </div>

      <div className="flex gap-4 flex-wrap">
        <Input
          label={t('common.search', 'Suche')}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
        <Select
          label={t('ci.status', 'Status')}
          value={status}
          onChange={(e) => setStatus(e.target.value)}
          options={statusOptions}
        />
        <Select
          label={t('asset.category', 'Kategorie')}
          value={category}
          onChange={(e) => setCategory(e.target.value)}
          options={categoryOptions}
        />
      </div>

      {isLoading ? (
        <p className="text-gray-500 dark:text-gray-400">{t('app.loading', 'Laden...')}</p>
      ) : (
        <Card title={`${t('asset.title', 'Inventar')} (${data?.total ?? 0})`}>
          {selectedIds.length > 0 ? (
            <div
              className="mb-3 flex flex-wrap items-center gap-3 rounded-xl border border-gray-200 bg-primary/5 px-4 py-2 dark:border-gray-800"
              role="toolbar"
              aria-label={t('asset.bulk.toolbarLabel')}
            >
              <span className="text-sm font-medium text-gray-700 dark:text-gray-200">
                {t('asset.bulk.selected', { count: selectedIds.length })}
              </span>
              <Select
                value={bulkStatus}
                onChange={(event) => setBulkStatus(event.target.value)}
                aria-label={t('asset.bulk.setStatus')}
                className="max-w-xs"
                options={statusOptions}
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
          <div className="overflow-x-auto">
            <table className="min-w-full divide-y divide-gray-200 dark:divide-gray-700">
              <thead>
                <tr className="text-left text-sm font-medium text-gray-500 dark:text-gray-400">
                  <th scope="col" className="w-10 pb-2">
                    <input
                      type="checkbox"
                      checked={allVisibleSelected}
                      onChange={toggleSelectAll}
                      aria-label={t('asset.bulk.selectAll')}
                      className="h-4 w-4 rounded border-gray-300 accent-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40"
                    />
                  </th>
                  <th scope="col" className="pb-2">
                    {t('asset.assetTag', 'Asset-Tag')}
                  </th>
                  <th scope="col" className="pb-2">
                    {t('ci.name', 'Name')}
                  </th>
                  <th scope="col" className="pb-2">
                    {t('asset.category', 'Kategorie')}
                  </th>
                  <th scope="col" className="pb-2">
                    {t('ci.status', 'Status')}
                  </th>
                  <th scope="col" className="pb-2">
                    {t('asset.location', 'Standort')}
                  </th>
                  <th scope="col" className="pb-2">
                    {t('common.actions', 'Aktionen')}
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
                {assetList.map((asset) => (
                  <tr key={asset.id} className="text-sm text-gray-700 dark:text-gray-300">
                    <td className="py-2">
                      <input
                        type="checkbox"
                        checked={selectedIds.includes(asset.id)}
                        onChange={() => toggleSelect(asset.id)}
                        aria-label={t('asset.bulk.selectRow', { name: asset.name })}
                        className="h-4 w-4 rounded border-gray-300 accent-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40"
                      />
                    </td>
                    <td className="py-2 font-mono">{asset.asset_tag}</td>
                    <td className="py-2">{asset.name}</td>
                    <td className="py-2">{asset.category}</td>
                    <td className="py-2">
                      <Badge variant={statusVariant(asset.status)}>{asset.status}</Badge>
                    </td>
                    <td className="py-2">{asset.location || '—'}</td>
                    <td className="py-2">
                      <Button variant="ghost" size="sm" onClick={() => deleteAssetWithUndo(asset)}>
                        {t('common.delete', 'Löschen')}
                      </Button>
                    </td>
                  </tr>
                ))}
                {assetList.length === 0 && (
                  <tr>
                    <td colSpan={7} className="py-4 text-center text-gray-400">
                      {t('common.noData', 'Keine Daten')}
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </Card>
      )}

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
