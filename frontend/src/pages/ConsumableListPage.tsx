/**
 * ConsumableListPage — the quantity items of the warehouse (UI-14, WP-320):
 * per item the total stock over all bins (stock_level = Σ on_hand), unit,
 * min_level and the hint when the total is below min_level (WP-285). Each
 * row links to its detail page, where the stock is booked per bin; the
 * list itself books nothing. The list follows the scope (SCOPED_LISTS,
 * client_id) and the filter low_stock.
 */
import { Boxes, Plus, Search } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { ApiError, consumableApi, type CreateConsumableRequest } from '../api/client';
import { useConsumableList } from '../api/hooks';
import { accessErrorMessage } from '../components/pickers/ObjectPicker';
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
import { isBelowMinimum } from './ConsumableDetailPage';

const EMPTY_FORM = { name: '', sku: '', unit: '', category: '', min_level: '0', barcode: '' };

/** createRequest is the body of POST /consumables: no initial stock, no free-text location. */
function createRequest(form: typeof EMPTY_FORM): CreateConsumableRequest {
  const text = (value: string) => value.trim() || undefined;
  const minLevel = Number(form.min_level);
  return {
    name: form.name.trim(),
    sku: text(form.sku),
    unit: text(form.unit),
    category: text(form.category),
    min_level: Number.isFinite(minLevel) && minLevel > 0 ? minLevel : 0,
    barcode: text(form.barcode),
  };
}

/** violationOf is the message of a refused create at one input. */
function violationOf(error: unknown, field: string): string | undefined {
  if (!(error instanceof ApiError)) return undefined;
  const violation = error.violations.find((v) => v.field === field);
  if (violation) return violation.detail;
  return error.field === field ? error.detail : undefined;
}

export function ConsumableListPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [search, setSearch] = useState('');
  const [lowStockOnly, setLowStockOnly] = useState(false);
  const [showCreate, setShowCreate] = useState(false);
  const [form, setForm] = useState(EMPTY_FORM);

  const { data, isLoading, error, refetch } = useConsumableList({
    search,
    low_stock: lowStockOnly || undefined,
    limit: 100,
  });

  const createMutation = useMutation({
    mutationFn: () => consumableApi.create(createRequest(form)),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['consumables'] });
      closeCreate(false);
    },
  });

  const closeCreate = (open: boolean) => {
    if (!open) {
      setForm(EMPTY_FORM);
      createMutation.reset();
    }
    setShowCreate(open);
  };

  const consumables = data?.data ?? [];
  const createError = createMutation.error;
  const field = (name: keyof typeof EMPTY_FORM) => ({
    value: form[name],
    onChange: (event: { target: { value: string } }) =>
      setForm({ ...form, [name]: event.target.value }),
    error: violationOf(createError, name),
  });

  return (
    <div className="space-y-4">
      <PageHeader
        title={t('nav.consumables')}
        description={t('consumables.summary')}
        actions={
          <Button icon={<Plus />} onClick={() => setShowCreate(true)}>
            {t('consumables.create')}
          </Button>
        }
      />

      <Toolbar>
        <Input
          type="text"
          icon={<Search />}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder={t('common.search')}
          className="w-full sm:w-64"
          aria-label={t('common.search')}
        />
        <label className="inline-flex h-9 items-center gap-2 text-sm text-fg">
          <input
            type="checkbox"
            checked={lowStockOnly}
            onChange={(e) => setLowStockOnly(e.target.checked)}
          />
          {t('consumables.lowStockOnly')}
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
          description={accessErrorMessage(error, t)}
          retryLabel={t('common.retry')}
          onRetry={() => void refetch()}
        />
      ) : null}

      {data ? (
        <Card className="p-0 overflow-hidden">
          <ScrollRegion label={t('nav.consumables')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('common.name')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('inventory.sku')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('consumables.totalStock')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('consumables.minLevel')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('ci.status')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {consumables.map((c) => (
                  <tr key={c.id} className={trClasses}>
                    <td className={tdPrimaryClasses}>
                      <Link
                        to={`/consumables/${c.id}`}
                        className="font-medium text-accent-strong underline-offset-2 hover:underline"
                      >
                        {c.name}
                      </Link>
                    </td>
                    <td className={tdMonoClasses}>{c.sku || '—'}</td>
                    <td className={cn(tdNumericClasses, 'whitespace-nowrap text-fg')}>
                      {t('consumables.quantityWithUnit', { quantity: c.stock_level, unit: c.unit })}
                    </td>
                    <td className={tdNumericClasses}>{c.min_level}</td>
                    <td className={tdClasses}>
                      {isBelowMinimum(c) ? (
                        <Badge variant="danger">{t('consumables.low')}</Badge>
                      ) : (
                        <Badge variant="success">{t('consumables.ok')}</Badge>
                      )}
                    </td>
                  </tr>
                ))}
                {consumables.length === 0 ? (
                  <TableEmptyRow
                    colSpan={5}
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

      <Modal open={showCreate} onOpenChange={closeCreate} title={t('consumables.create')}>
        <form
          className="space-y-4"
          onSubmit={(event) => {
            event.preventDefault();
            if (form.name.trim()) createMutation.mutate();
          }}
        >
          <Input label={t('common.name')} required {...field('name')} />
          <Input label={t('inventory.sku')} {...field('sku')} />
          <Input label={t('consumables.unit')} {...field('unit')} />
          <Input label={t('consumables.category')} {...field('category')} />
          <Input label={t('consumables.minLevel')} type="number" min={0} {...field('min_level')} />
          <Input label={t('consumables.barcodeOptional')} {...field('barcode')} />
          {createError ? (
            <p role="alert" className="text-sm text-danger">
              {accessErrorMessage(createError, t)}
            </p>
          ) : null}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="secondary" onClick={() => closeCreate(false)}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={!form.name.trim() || createMutation.isPending}>
              {t('common.create')}
            </Button>
          </div>
        </form>
      </Modal>
    </div>
  );
}
