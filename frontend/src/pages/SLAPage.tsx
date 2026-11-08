import { Plus, Timer } from 'lucide-react';
import { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ApiError } from '../api/client';
import {
  useAllBusinessCalendars,
  useCreateSLA,
  useHasPermission,
  useSLABreaches,
  useSLAList,
} from '../api/hooks';
import { useClients } from '../api/cmdbHooks';
import { BusinessCalendarSection } from '../components/sla/BusinessCalendarSection';
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

/** SLA_PRIORITIES are the priorities of a policy (WP-290), labelled as ticket.priorities. */
export const SLA_PRIORITIES = ['low', 'medium', 'high', 'critical'] as const;

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
  const calendars = useAllBusinessCalendars();
  const clients = useClients();
  const canWrite = useHasPermission('sla:write') === true;
  const businessHoursId = useId();
  const [form, setForm] = useState({
    name: '',
    client_id: '',
    priority: 'medium',
    response_target_minutes: 60,
    resolution_target_minutes: 480,
    calendar_id: '',
    business_calendar: true,
  });
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | undefined>(undefined);

  // A policy may name a calendar without client or of its own client
  // (TKT-02); empty lets the calendar chain decide (WP-292).
  const selectableCalendars = (calendars.data ?? []).filter(
    (calendar) => calendar.client_id === null || calendar.client_id === form.client_id,
  );
  const calendarName = (id: string | null | undefined) =>
    id ? (calendars.data?.find((calendar) => calendar.id === id)?.name ?? id) : undefined;

  const submit = () => {
    if (!form.name) return;
    setFieldErrors({});
    setFormError(undefined);
    createSLA.mutate(
      {
        name: form.name,
        priority: form.priority,
        response_target_minutes: form.response_target_minutes,
        resolution_target_minutes: form.resolution_target_minutes,
        business_calendar: form.business_calendar,
        calendar_id: form.calendar_id || null,
        ...(form.client_id ? { client_id: form.client_id } : {}),
      },
      {
        onSuccess: () => setForm({ ...form, name: '' }),
        onError: (error) => {
          if (error instanceof ApiError) {
            const errors = Object.fromEntries(
              Object.entries(error.fieldErrors()).map(([field, info]) => [field, info.message]),
            );
            setFieldErrors(errors);
            if (Object.keys(errors).length === 0) {
              setFormError(error.detail ?? error.title ?? error.message);
            }
          } else {
            setFormError(error.message);
          }
        },
      },
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

      {canWrite ? (
        <Card title={t('sla.createPolicy')}>
          <div className="grid gap-4 md:grid-cols-4 md:items-start">
            <Input
              label={t('sla.name')}
              value={form.name}
              error={fieldErrors.name}
              onChange={(event) => setForm({ ...form, name: event.target.value })}
            />
            <Select
              label={t('sla.client')}
              value={form.client_id}
              error={fieldErrors.client_id}
              options={[
                { value: '', label: t('sla.noClient') },
                ...(clients.data?.data ?? []).map((client) => ({
                  value: client.id,
                  label: client.name,
                })),
              ]}
              onChange={(event) => {
                const clientId = event.target.value;
                const chosen = calendars.data?.find((c) => c.id === form.calendar_id);
                const keep = chosen && (chosen.client_id === null || chosen.client_id === clientId);
                setForm({
                  ...form,
                  client_id: clientId,
                  calendar_id: keep ? form.calendar_id : '',
                });
              }}
            />
            <Select
              label={t('ticket.priority')}
              value={form.priority}
              error={fieldErrors.priority}
              options={SLA_PRIORITIES.map((value) => ({
                value,
                label: t(`ticket.priorities.${value}`),
              }))}
              onChange={(event) => setForm({ ...form, priority: event.target.value })}
            />
            <Select
              label={t('sla.calendarField')}
              value={form.calendar_id}
              error={fieldErrors.calendar_id}
              options={[
                { value: '', label: t('sla.calendarChain') },
                ...selectableCalendars.map((calendar) => ({
                  value: calendar.id,
                  label: calendar.name,
                })),
              ]}
              onChange={(event) => setForm({ ...form, calendar_id: event.target.value })}
            />
            <Input
              label={t('sla.responseMinutes')}
              type="number"
              min={1}
              value={form.response_target_minutes}
              error={fieldErrors.response_target_minutes}
              onChange={(event) =>
                setForm({ ...form, response_target_minutes: Number(event.target.value) })
              }
            />
            <Input
              label={t('sla.resolutionMinutes')}
              type="number"
              min={1}
              value={form.resolution_target_minutes}
              error={fieldErrors.resolution_target_minutes}
              onChange={(event) =>
                setForm({ ...form, resolution_target_minutes: Number(event.target.value) })
              }
            />
            <label
              htmlFor={businessHoursId}
              className="flex items-center gap-2 text-sm text-fg md:mt-8"
            >
              <input
                id={businessHoursId}
                type="checkbox"
                checked={form.business_calendar}
                onChange={(event) => setForm({ ...form, business_calendar: event.target.checked })}
              />
              {t('sla.businessHours')}
            </label>
            <div className="md:mt-6">
              <Button
                icon={<Plus />}
                onClick={submit}
                loading={createSLA.isPending}
                disabled={!form.name}
              >
                {t('common.save')}
              </Button>
            </div>
          </div>
          {formError ? (
            <p className="mt-3 text-sm text-danger" role="alert">
              {formError}
            </p>
          ) : null}
        </Card>
      ) : null}

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
                    <th scope="col" className={thClasses}>
                      {t('sla.calendarField')}
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
                      <td className={tdClasses}>
                        {policy.business_calendar
                          ? (calendarName(policy.calendar_id) ?? t('sla.calendarChain'))
                          : t('sla.elapsedTime')}
                      </td>
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

      <BusinessCalendarSection />

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
