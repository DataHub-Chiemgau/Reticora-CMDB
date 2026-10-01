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

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">
            {t('nav.orders', 'Bestellungen')}
          </h2>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">
            {t('orders.summary', 'Interne Bestellungen mit Genehmigungs-Workflow.')}
          </p>
        </div>
        <Button size="sm" onClick={() => setShowCreate(true)}>
          {t('orders.create', 'Bestellung anlegen')}
        </Button>
      </div>

      {isLoading ? <SkeletonList rows={4} label={t('app.loading')} /> : null}
      {error ? (
        <ErrorState
          title={t('app.error')}
          retryLabel={t('common.retry')}
          onRetry={() => void refetch()}
        />
      ) : null}

      <div className="space-y-3">
        {(data?.data ?? []).map((o: Order) => (
          <Card
            key={o.id}
            title={`${o.order_number} — ${o.title}`}
            actions={<Badge variant={statusVariant(o.status)}>{o.status}</Badge>}
          >
            <p className="text-sm text-gray-600 dark:text-gray-300">{o.supplier || '—'}</p>
            <p className="mt-1 text-sm text-gray-500 dark:text-gray-400">
              {o.items?.length ?? 0} {t('orders.positions', 'Positionen')}
            </p>
            <div className="mt-3 flex flex-wrap gap-2">
              {o.status === 'draft' ? (
                <Button size="sm" variant="secondary" onClick={() => submitMutation.mutate(o.id)}>
                  {t('orders.submit', 'Einreichen')}
                </Button>
              ) : null}
              {o.status === 'submitted' ? (
                <>
                  <Button size="sm" onClick={() => approveMutation.mutate(o.id)}>
                    {t('orders.approve', 'Genehmigen')}
                  </Button>
                  <Button size="sm" variant="danger" onClick={() => rejectMutation.mutate(o.id)}>
                    {t('orders.reject', 'Ablehnen')}
                  </Button>
                </>
              ) : null}
            </div>
          </Card>
        ))}
      </div>

      <Modal
        open={showCreate}
        onOpenChange={setShowCreate}
        title={t('orders.create', 'Bestellung anlegen')}
      >
        <div className="space-y-3">
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
