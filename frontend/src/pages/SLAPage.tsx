import { Plus, Timer } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useCreateSLA, useSLABreaches, useSLAList } from '../api/hooks';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
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
import { ScrollRegion } from '../components/ScrollRegion';

const priorities = ['low', 'medium', 'high', 'critical'] as const;

const priorityVariant = {
  low: 'success',
  medium: 'neutral',
  high: 'warning',
  critical: 'danger',
} as const;

export function SLAPage() {
  const { t, i18n } = useTranslation();
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

  const header = <PageHeader title={t('sla.title')} description={t('sla.subtitle')} />;

  if (policies.isLoading || breaches.isLoading) {
    return (
      <div className="space-y-4">
        {header}
        <SkeletonList rows={6} label={t('sla.loading')} />
      </div>
    );
  }

  if (policies.isError) {
    return (
      <div className="space-y-4">
        {header}
        <ErrorState
          title={t('sla.errorTitle')}
          description={policies.error.message}
          retryLabel={t('common.retry')}
          onRetry={() => policies.refetch()}
        />
      </div>
    );
  }

  const formatDate = (iso: string) => new Date(iso).toLocaleString(i18n.language);

  return (
    <div className="space-y-6">
      {header}

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
            options={priorities.map((value) => ({
              value,
              label: t(`ticket.priorities.${value}`),
            }))}
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
          <Button
            icon={<Plus />}
            onClick={submit}
            loading={createSLA.isPending}
            disabled={!form.name}
          >
            {t('common.save')}
          </Button>
        </div>
      </Card>

      <section className="space-y-3" aria-labelledby="sla-policies">
        <h2 id="sla-policies" className="text-base font-semibold text-fg">
          {t('sla.policies')}{' '}
          <span className="tabular-nums text-fg-muted">({policies.data?.total ?? 0})</span>
        </h2>
        {policies.data?.data.length ? (
          <Card className="overflow-hidden p-0">
            <ScrollRegion label={t('sla.policies')}>
              <table className={tableClasses}>
                <thead className={theadClasses}>
                  <tr>
                    <th scope="col" className={thClasses}>
                      {t('sla.name')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('ticket.priority')}
                    </th>
                    <th scope="col" className={`${thClasses} text-right`}>
                      {t('sla.responseMinutes')}
                    </th>
                    <th scope="col" className={`${thClasses} text-right`}>
                      {t('sla.resolutionMinutes')}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {policies.data.data.map((policy) => (
                    <tr key={policy.id} className={trClasses}>
                      <td className={tdPrimaryClasses}>{policy.name}</td>
                      <td className={tdClasses}>
                        <Badge
                          variant={
                            priorityVariant[policy.priority as keyof typeof priorityVariant] ??
                            'neutral'
                          }
                        >
                          {t(`ticket.priorities.${policy.priority}`, policy.priority)}
                        </Badge>
                      </td>
                      <td className={tdNumericClasses}>{policy.response_target_minutes}</td>
                      <td className={tdNumericClasses}>{policy.resolution_target_minutes}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </ScrollRegion>
          </Card>
        ) : (
          <EmptyState
            icon={<Timer />}
            title={t('sla.emptyTitle')}
            description={t('sla.emptyHint')}
          />
        )}
      </section>

      <section className="space-y-3" aria-labelledby="sla-breaches">
        <h2 id="sla-breaches" className="text-base font-semibold text-fg">
          {t('sla.breaches')}{' '}
          <span className="tabular-nums text-fg-muted">({breaches.data?.total ?? 0})</span>
        </h2>
        {breaches.data?.data.length ? (
          <Card className="overflow-hidden p-0">
            <ScrollRegion label={t('sla.breaches')}>
              <table className={tableClasses}>
                <thead className={theadClasses}>
                  <tr>
                    <th scope="col" className={thClasses}>
                      {t('sla.ticket')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('sla.status')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('sla.responseDue')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('sla.resolutionDue')}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {breaches.data.data.map((state) => (
                    <tr key={state.id} className={trClasses}>
                      <td className={tdMonoClasses}>{state.ticket_id}</td>
                      <td className={tdClasses}>
                        <Badge variant="danger">{t('sla.breached')}</Badge>
                      </td>
                      <td className={`${tdClasses} whitespace-nowrap tabular-nums`}>
                        {formatDate(state.response_due_at)}
                      </td>
                      <td className={`${tdClasses} whitespace-nowrap tabular-nums`}>
                        {formatDate(state.resolution_due_at)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </ScrollRegion>
          </Card>
        ) : (
          <EmptyState title={t('sla.noBreaches')} description={t('sla.noBreachesHint')} />
        )}
      </section>
    </div>
  );
}
