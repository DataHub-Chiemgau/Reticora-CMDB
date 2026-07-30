import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useCreateSLA, useSLABreaches, useSLAList } from '../api/hooks';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';

const priorities = [
  { value: 'low', label: 'Low' },
  { value: 'medium', label: 'Medium' },
  { value: 'high', label: 'High' },
  { value: 'critical', label: 'Critical' },
];

export function SLAPage() {
  const { t } = useTranslation();
  const policies = useSLAList();
  const breaches = useSLABreaches({ status: 'breached' });
  const createSLA = useCreateSLA();
  const [form, setForm] = useState({
    name: '',
    priority: 'medium',
    response_target_minutes: 60,
    resolution_target_minutes: 480,
  });

  const submit = () => {
    if (!form.name) return;
    createSLA.mutate(
      { ...form, business_calendar: false },
      { onSuccess: () => setForm({ ...form, name: '' }) },
    );
  };

  if (policies.isLoading || breaches.isLoading) {
    return <SkeletonList rows={6} label={t('sla.loading')} />;
  }

  if (policies.isError) {
    return (
      <ErrorState
        title={t('sla.errorTitle')}
        description={policies.error.message}
        retryLabel={t('common.retry')}
        onRetry={() => policies.refetch()}
      />
    );
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold text-gray-900 dark:text-white">{t('sla.title')}</h1>
        <p className="text-sm text-gray-600 dark:text-gray-300">{t('sla.subtitle')}</p>
      </div>

      <Card title={t('sla.createPolicy')}>
        <div className="grid gap-4 md:grid-cols-5 md:items-end">
          <Input
            label={t('sla.name')}
            value={form.name}
            onChange={(event) => setForm({ ...form, name: event.target.value })}
          />
          <Select
            label={t('ticket.priority')}
            value={form.priority}
            options={priorities}
            onChange={(event) => setForm({ ...form, priority: event.target.value })}
          />
          <Input
            label={t('sla.responseMinutes')}
            type="number"
            value={form.response_target_minutes}
            onChange={(event) =>
              setForm({ ...form, response_target_minutes: Number(event.target.value) })
            }
          />
          <Input
            label={t('sla.resolutionMinutes')}
            type="number"
            value={form.resolution_target_minutes}
            onChange={(event) =>
              setForm({ ...form, resolution_target_minutes: Number(event.target.value) })
            }
          />
          <Button onClick={submit} disabled={createSLA.isPending || !form.name}>
            {t('common.save')}
          </Button>
        </div>
      </Card>

      <Card title={`${t('sla.policies')} (${policies.data?.total ?? 0})`}>
        {policies.data?.data.length ? (
          <div className="overflow-x-auto">
            <table className="min-w-full divide-y divide-gray-200 dark:divide-gray-700">
              <thead>
                <tr className="text-left text-sm font-medium text-gray-500 dark:text-gray-400">
                  <th className="pb-2">{t('sla.name')}</th>
                  <th className="pb-2">{t('ticket.priority')}</th>
                  <th className="pb-2">{t('sla.responseMinutes')}</th>
                  <th className="pb-2">{t('sla.resolutionMinutes')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
                {policies.data.data.map((policy) => (
                  <tr key={policy.id} className="text-sm text-gray-700 dark:text-gray-300">
                    <td className="py-2 font-medium">{policy.name}</td>
                    <td className="py-2">
                      <Badge variant="info">{policy.priority}</Badge>
                    </td>
                    <td className="py-2">{policy.response_target_minutes}</td>
                    <td className="py-2">{policy.resolution_target_minutes}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <EmptyState title={t('sla.emptyTitle')} description={t('sla.emptyHint')} />
        )}
      </Card>

      <Card title={`${t('sla.breaches')} (${breaches.data?.total ?? 0})`}>
        {breaches.data?.data.length ? (
          <ul className="space-y-2">
            {breaches.data.data.map((state) => (
              <li
                key={state.id}
                className="rounded-lg border border-gray-200 p-3 text-sm dark:border-gray-800"
              >
                <span className="font-mono">{state.ticket_id}</span>{' '}
                <Badge variant="danger">{t('sla.breached')}</Badge>
              </li>
            ))}
          </ul>
        ) : (
          <EmptyState title={t('sla.noBreaches')} description={t('sla.noBreachesHint')} />
        )}
      </Card>
    </div>
  );
}
