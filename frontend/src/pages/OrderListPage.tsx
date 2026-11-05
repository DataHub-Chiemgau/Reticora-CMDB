import { Plus, ShoppingCart } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { orderApi } from '../api/client';
import type { Order } from '../api/client';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
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

function statusVariant(status: string): 'neutral' | 'warning' | 'success' | 'danger' | 'info' {
  switch (status) {
    case 'draft':
      return 'neutral';
    case 'submitted':
      return 'warning';
    case 'approved':
      return 'success';
    case 'rejected':
      return 'danger';
    case 'ordered':
      return 'info';
    case 'received':
      return 'success';
    default:
      return 'neutral';
  }
}

export function OrderListPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [showCreate, setShowCreate] = useState(false);
  const [form, setForm] = useState({ title: '', supplier: '' });

  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['orders'],
    queryFn: () => orderApi.list({ limit: 100 }),
  });

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['orders'] });
  const createMutation = useMutation({
    mutationFn: () => orderApi.create({ title: form.title, supplier: form.supplier }),
    onSuccess: () => {
      invalidate();
      setShowCreate(false);
      setForm({ title: '', supplier: '' });
    },
  });
  const submitMutation = useMutation({ mutationFn: orderApi.submit, onSuccess: invalidate });
  const approveMutation = useMutation({ mutationFn: orderApi.approve, onSuccess: invalidate });
  const rejectMutation = useMutation({ mutationFn: orderApi.reject, onSuccess: invalidate });

  const orders = data?.data ?? [];

  return (
    <div className="space-y-4">
      <PageHeader
        title={t('nav.orders', 'Bestellungen')}
        description={t('orders.summary', 'Interne Bestellungen mit Genehmigungs-Workflow.')}
        actions={
          <Button icon={<Plus />} onClick={() => setShowCreate(true)}>
            {t('orders.create', 'Bestellung anlegen')}
          </Button>
        }
      />

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
          <ScrollRegion label={t('nav.orders', 'Bestellungen')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('orders.number')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('common.title', 'Titel')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('orders.supplier', 'Lieferant')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-right')}>
                    {t('orders.positions', 'Positionen')}
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
                {orders.map((o: Order) => (
                  <tr key={o.id} className={trClasses}>
                    <td className={tdMonoClasses}>{o.order_number}</td>
                    <td className={tdPrimaryClasses}>{o.title}</td>
                    <td className={tdClasses}>{o.supplier || '—'}</td>
                    <td className={tdNumericClasses}>{o.items?.length ?? 0}</td>
                    <td className={tdClasses}>
                      <Badge variant={statusVariant(o.status)}>
                        {t(`orders.statuses.${o.status}`, o.status)}
                      </Badge>
                    </td>
                    <td className={cn(tdClasses, 'text-right')}>
                      <div className="flex justify-end gap-1">
                        {o.status === 'draft' ? (
                          <Button
                            size="sm"
                            variant="secondary"
                            onClick={() => submitMutation.mutate(o.id)}
                          >
                            {t('orders.submit', 'Einreichen')}
                          </Button>
                        ) : null}
                        {o.status === 'submitted' ? (
                          <>
                            <Button
                              size="sm"
                              variant="secondary"
                              onClick={() => approveMutation.mutate(o.id)}
                            >
                              {t('orders.approve', 'Genehmigen')}
                            </Button>
                            <Button
                              size="sm"
                              variant="ghost"
                              onClick={() => rejectMutation.mutate(o.id)}
                            >
                              {t('orders.reject', 'Ablehnen')}
                            </Button>
                          </>
                        ) : null}
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

      <Modal
        open={showCreate}
        onOpenChange={setShowCreate}
        title={t('orders.create', 'Bestellung anlegen')}
      >
        <div className="space-y-4">
          <Input
            label={t('common.title', 'Titel')}
            value={form.title}
            onChange={(e) => setForm({ ...form, title: e.target.value })}
          />
          <Input
            label={t('orders.supplier', 'Lieferant')}
            value={form.supplier}
            onChange={(e) => setForm({ ...form, supplier: e.target.value })}
          />
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setShowCreate(false)}>
              {t('common.cancel', 'Abbrechen')}
            </Button>
            <Button
              onClick={() => createMutation.mutate()}
              disabled={!form.title || createMutation.isPending}
            >
              {t('common.create', 'Anlegen')}
            </Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
