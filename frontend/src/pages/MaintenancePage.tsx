import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { maintenanceApi } from '../api/client';
import type { MaintenanceWindow } from '../api/client';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { SkeletonList } from '../components/ui/Skeleton';
import { ErrorState } from '../components/ui/ErrorState';

function statusVariant(status: string): 'neutral' | 'warning' | 'success' | 'danger' | 'info' {
  switch (status) {
    case 'scheduled':
      return 'warning';
    case 'in_progress':
      return 'info';
    case 'completed':
      return 'success';
    case 'cancelled':
      return 'danger';
    default:
      return 'neutral';
  }
}

export function MaintenancePage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [showCreate, setShowCreate] = useState(false);
  const [form, setForm] = useState({ title: '', starts_at: '', ends_at: '' });
  const [notifyResult, setNotifyResult] = useState<string | null>(null);

  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['maintenance-windows'],
    queryFn: () => maintenanceApi.list({ limit: 100 }),
  });

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['maintenance-windows'] });
  const createMutation = useMutation({
    mutationFn: () =>
      maintenanceApi.create({
        title: form.title,
        starts_at: form.starts_at,
        ends_at: form.ends_at,
      }),
    onSuccess: () => {
      invalidate();
      setShowCreate(false);
      setForm({ title: '', starts_at: '', ends_at: '' });
    },
  });
  const statusMutation = useMutation({
    mutationFn: ({ id, status }: { id: string; status: string }) =>
      maintenanceApi.update(id, { status }),
    onSuccess: invalidate,
  });
  const notifyMutation = useMutation({
    mutationFn: (id: string) => maintenanceApi.notify(id),
    onSuccess: (res) =>
      setNotifyResult(
        t('maintenance.notified', '{{count}} Kunden benachrichtigt', { count: res.notified }),
      ),
  });

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">
            {t('nav.maintenance', 'Wartungsfenster')}
          </h2>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">
            {t('maintenance.summary', 'Wartungen planen und betroffene Kunden benachrichtigen.')}
          </p>
        </div>
        <Button size="sm" onClick={() => setShowCreate(true)}>
          {t('maintenance.create', 'Wartung planen')}
        </Button>
      </div>

      {notifyResult ? (
        <Card className="border-green-300 bg-green-50 dark:bg-green-950">
          <p className="text-sm text-green-800 dark:text-green-200">{notifyResult}</p>
        </Card>
      ) : null}
      {isLoading ? <SkeletonList rows={4} label={t('app.loading')} /> : null}
      {error ? (
        <ErrorState
          title={t('app.error')}
          retryLabel={t('common.retry')}
          onRetry={() => void refetch()}
        />
      ) : null}

      <div className="space-y-3">
        {(data?.data ?? []).map((w: MaintenanceWindow) => (
          <Card
            key={w.id}
            title={w.title}
            actions={<Badge variant={statusVariant(w.status)}>{w.status}</Badge>}
          >
            <p className="text-sm text-gray-600 dark:text-gray-300">
              {new Date(w.starts_at).toLocaleString()} → {new Date(w.ends_at).toLocaleString()}
            </p>
            <p className="mt-1 text-sm text-gray-500 dark:text-gray-400">
              {(w.ci_ids ?? []).length} {t('maintenance.affectedCIs', 'betroffene CIs')}
            </p>
            <div className="mt-3 flex flex-wrap gap-2">
              {w.status === 'scheduled' ? (
                <>
                  <Button
                    size="sm"
                    variant="secondary"
                    onClick={() => statusMutation.mutate({ id: w.id, status: 'in_progress' })}
                  >
                    {t('maintenance.start', 'Starten')}
                  </Button>
                  <Button
                    size="sm"
                    onClick={() => notifyMutation.mutate(w.id)}
                    disabled={notifyMutation.isPending}
                  >
                    {t('maintenance.notify', 'Kunden benachrichtigen')}
                  </Button>
                </>
              ) : null}
              {w.status === 'in_progress' ? (
                <Button
                  size="sm"
                  onClick={() => statusMutation.mutate({ id: w.id, status: 'completed' })}
                >
                  {t('maintenance.complete', 'Abschließen')}
                </Button>
              ) : null}
            </div>
          </Card>
        ))}
      </div>

      <Modal
        open={showCreate}
        onOpenChange={setShowCreate}
        title={t('maintenance.create', 'Wartung planen')}
      >
        <div className="space-y-3">
          <Input
            label={t('common.title', 'Titel')}
            value={form.title}
            onChange={(e) => setForm({ ...form, title: e.target.value })}
          />
          <Input
            label={t('maintenance.startsAt', 'Beginn')}
            type="datetime-local"
            value={form.starts_at}
            onChange={(e) =>
              setForm({
                ...form,
                starts_at: e.target.value ? new Date(e.target.value).toISOString() : '',
              })
            }
          />
          <Input
            label={t('maintenance.endsAt', 'Ende')}
            type="datetime-local"
            value={form.ends_at}
            onChange={(e) =>
              setForm({
                ...form,
                ends_at: e.target.value ? new Date(e.target.value).toISOString() : '',
              })
            }
          />
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setShowCreate(false)}>
              {t('common.cancel', 'Abbrechen')}
            </Button>
            <Button
              onClick={() => createMutation.mutate()}
              disabled={!form.title || !form.starts_at || !form.ends_at || createMutation.isPending}
            >
              {t('common.create', 'Anlegen')}
            </Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
