import { Boxes, Plus, Search } from 'lucide-react';
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
import { PageHeader } from '../components/ui/PageHeader';
import { Toolbar, ToolbarEnd } from '../components/ui/Toolbar';
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
    mutationFn: () =>
      consumableApi.create({ name: form.name, sku: form.sku, min_level: form.min_level }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['consumables'] });
      setShowCreate(false);
      setForm({ name: '', sku: '', min_level: 0 });
    },
  });

  const moveMutation = useMutation({
    mutationFn: ({
      id,
      direction,
      quantity,
    }: {
      id: string;
      direction: 'in' | 'out';
      quantity: number;
    }) => consumableApi.addMovement(id, { direction, quantity }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['consumables'] }),
  });

  const consumables = data?.data ?? [];

  return (
    <div className="space-y-4">
      <PageHeader
        title={t('nav.consumables', 'Lagerverwaltung')}
        description={t('consumables.summary', 'Verbrauchsmaterial mit Bestand und Mindestbestand.')}
        actions={
          <Button icon={<Plus />} onClick={() => setShowCreate(true)}>
            {t('consumables.create', 'Artikel anlegen')}
          </Button>
        }
      />

      <Toolbar>
        <Input
          type="text"
          icon={<Search />}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder={t('common.search', 'Suchen')}
          className="w-full sm:w-64"
          aria-label={t('common.search', 'Suchen')}
        />
        <label className="inline-flex h-9 items-center gap-2 text-sm text-fg">
          <input
            type="checkbox"
            checked={lowStockOnly}
            onChange={(e) => setLowStockOnly(e.target.checked)}
          />
          {t('consumables.lowStockOnly', 'Nur Mindestbestand unterschritten')}
        </label>
        {data ? (
          <ToolbarEnd>
            <span className="text-sm text-fg-muted tabular-nums">
              {data.total} {t('common.entries')}
            </span>
          </ToolbarEnd>
        ) : null}
      </Toolbar>

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
          <ScrollRegion label={t('nav.consumables', 'Lagerverwaltung')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('common.name', 'Name')}
                  </th>
                  <th scope="col" className={thClasses}>
                    SKU
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('consumables.stock')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('consumables.minLevel', 'Mindestbestand')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ci.status', 'Status')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('common.actions', 'Aktionen')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {consumables.map((c: Consumable) => (
                  <tr key={c.id} className={trClasses}>
                    <td className={tdPrimaryClasses}>{c.name}</td>
                    <td className={tdMonoClasses}>{c.sku || '—'}</td>
                    <td className={cn(tdNumericClasses, 'whitespace-nowrap text-fg')}>
                      {c.stock_level} {c.unit}
                    </td>
                    <td className={tdNumericClasses}>{c.min_level}</td>
                    <td className={tdClasses}>
                      {c.stock_level < c.min_level ? (
                        <Badge variant="danger">{t('consumables.low', 'niedrig')}</Badge>
                      ) : (
                        <Badge variant="success">{t('consumables.ok', 'OK')}</Badge>
                      )}
                    </td>
                    <td className={cn(tdClasses, 'text-right')}>
                      <div className="flex justify-end gap-1">
                        <Button
                          size="sm"
                          variant="secondary"
                          aria-label={`+1 ${c.name}`}
                          onClick={() =>
                            moveMutation.mutate({ id: c.id, direction: 'in', quantity: 1 })
                          }
                        >
                          +1
                        </Button>
                        <Button
                          size="sm"
                          variant="secondary"
                          aria-label={`−1 ${c.name}`}
                          onClick={() =>
                            moveMutation.mutate({ id: c.id, direction: 'out', quantity: 1 })
                          }
                          disabled={c.stock_level <= 0}
                        >
                          −1
                        </Button>
                      </div>
                    </td>
                  </tr>
                ))}
                {consumables.length === 0 ? (
                  <TableEmptyRow
                    colSpan={6}
                    icon={<Boxes />}
                    title={
                      search || lowStockOnly ? t('common.noResults') : t('consumables.emptyTitle')
                    }
                    description={
                      search || lowStockOnly
                        ? t('common.noResultsHint')
                        : t('consumables.emptyHint')
                    }
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
        title={t('consumables.create', 'Artikel anlegen')}
      >
        <div className="space-y-4">
          <Input
            label={t('common.name', 'Name')}
            value={form.name}
            onChange={(e) => setForm({ ...form, name: e.target.value })}
          />
          <Input
            label="SKU"
            value={form.sku}
            onChange={(e) => setForm({ ...form, sku: e.target.value })}
          />
          <Input
            label={t('consumables.minLevel', 'Mindestbestand')}
            type="number"
            value={form.min_level}
            onChange={(e) => setForm({ ...form, min_level: Number(e.target.value) })}
          />
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setShowCreate(false)}>
              {t('common.cancel', 'Abbrechen')}
            </Button>
            <Button
              onClick={() => createMutation.mutate()}
              disabled={!form.name || createMutation.isPending}
            >
              {t('common.create', 'Anlegen')}
            </Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
