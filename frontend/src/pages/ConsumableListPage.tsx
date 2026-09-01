import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { consumableApi } from '../api/client';
import type { Consumable } from '../api/client';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { SkeletonList } from '../components/ui/Skeleton';
import { ErrorState } from '../components/ui/ErrorState';

export function ConsumableListPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [search, setSearch] = useState('');
  const [lowStockOnly, setLowStockOnly] = useState(false);
  const [showCreate, setShowCreate] = useState(false);
  const [form, setForm] = useState({ name: '', sku: '', min_level: 0 });

  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['consumables', search, lowStockOnly],
    queryFn: () => consumableApi.list({ search, low_stock: lowStockOnly, limit: 100 }),
  });

  const createMutation = useMutation({
    mutationFn: () => consumableApi.create({ name: form.name, sku: form.sku, min_level: form.min_level }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['consumables'] });
      setShowCreate(false);
      setForm({ name: '', sku: '', min_level: 0 });
    },
  });

  const moveMutation = useMutation({
    mutationFn: ({ id, direction, quantity }: { id: string; direction: 'in' | 'out'; quantity: number }) =>
      consumableApi.addMovement(id, { direction, quantity }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['consumables'] }),
  });

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">{t('nav.consumables', 'Lagerverwaltung')}</h2>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">{t('consumables.summary', 'Verbrauchsmaterial mit Bestand und Mindestbestand.')}</p>
        </div>
        <Button size="sm" onClick={() => setShowCreate(true)}>{t('consumables.create', 'Artikel anlegen')}</Button>
      </div>

      <Card>
        <div className="flex flex-wrap gap-3">
          <Input value={search} onChange={(e) => setSearch(e.target.value)} placeholder={t('common.search', 'Suchen')} className="max-w-xs" aria-label={t('common.search', 'Suchen')} />
          <label className="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
            <input type="checkbox" checked={lowStockOnly} onChange={(e) => setLowStockOnly(e.target.checked)} />
            {t('consumables.lowStockOnly', 'Nur Mindestbestand unterschritten')}
          </label>
        </div>
      </Card>

      {isLoading ? <SkeletonList rows={4} label={t('app.loading')} /> : null}
      {error ? <ErrorState title={t('app.error')} retryLabel={t('common.retry')} onRetry={() => void refetch()} /> : null}

      <div className="grid grid-cols-1 gap-3 md:grid-cols-2 lg:grid-cols-3">
        {(data?.data ?? []).map((c: Consumable) => (
          <Card key={c.id} title={c.name} actions={c.stock_level <= c.min_level ? <Badge variant="danger">{t('consumables.low', 'niedrig')}</Badge> : <Badge variant="success">{t('consumables.ok', 'OK')}</Badge>}>
            <p className="text-sm text-gray-600 dark:text-gray-300">{c.sku || '—'}</p>
            <p className="mt-1 text-lg font-semibold">{c.stock_level} {c.unit} <span className="text-xs font-normal text-gray-500">/ min {c.min_level}</span></p>
            <div className="mt-3 flex gap-2">
              <Button size="sm" variant="secondary" onClick={() => moveMutation.mutate({ id: c.id, direction: 'in', quantity: 1 })}>+1</Button>
              <Button size="sm" variant="secondary" onClick={() => moveMutation.mutate({ id: c.id, direction: 'out', quantity: 1 })} disabled={c.stock_level <= 0}>−1</Button>
            </div>
          </Card>
        ))}
      </div>

      <Modal open={showCreate} onOpenChange={setShowCreate} title={t('consumables.create', 'Artikel anlegen')}>
        <div className="space-y-3">
          <Input label={t('common.name', 'Name')} value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
          <Input label="SKU" value={form.sku} onChange={(e) => setForm({ ...form, sku: e.target.value })} />
          <Input label={t('consumables.minLevel', 'Mindestbestand')} type="number" value={form.min_level} onChange={(e) => setForm({ ...form, min_level: Number(e.target.value) })} />
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setShowCreate(false)}>{t('common.cancel', 'Abbrechen')}</Button>
            <Button onClick={() => createMutation.mutate()} disabled={!form.name || createMutation.isPending}>{t('common.create', 'Anlegen')}</Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
