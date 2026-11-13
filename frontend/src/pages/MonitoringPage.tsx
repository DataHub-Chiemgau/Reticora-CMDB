import { BellRing, Play, Plus } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  useAlertRules,
  useCreateAlertRule,
  useDeleteAlertRule,
  useMetricPoints,
} from '../api/hooks';
import type { AlertRule, AlertRuleCreateRequest } from '../api/client';
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
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';
import { ScrollRegion } from '../components/ScrollRegion';

const severityVariants = {
  critical: 'danger',
  warning: 'warning',
  info: 'info',
} as const;

// The severity of a rule: its old form, else its highest stage (WP-985).
function ruleSeverity(rule: AlertRule): 'critical' | 'warning' | 'info' {
  if (rule.severity) return rule.severity;
  if (rule.critical_condition) return 'critical';
  if (rule.warning_condition) return 'warning';
  return 'info';
}

const initialAlertForm: AlertRuleCreateRequest = {
  name: '',
  metric_name: '',
  condition: 'gt',
  threshold: 0,
  duration: '5m',
  severity: 'warning',
  enabled: true,
};

export function MonitoringPage() {
  const { t } = useTranslation();
  const alerts = useAlertRules();
  const createAlert = useCreateAlertRule();
  const deleteAlert = useDeleteAlertRule();

  const [metricName, setMetricName] = useState('');
  const [ciId, setCiId] = useState('');
  const [query, setQuery] = useState({ name: '', ci_id: '' });
  const points = useMetricPoints({ name: query.name, ci_id: query.ci_id });

  const [form, setForm] = useState<AlertRuleCreateRequest>(initialAlertForm);

  const submitRule = () => {
    if (!form.name || !form.metric_name) return;
    createAlert.mutate(form, { onSuccess: () => setForm(initialAlertForm) });
  };

  const header = (
    <PageHeader title={t('monitoring.title')} description={t('monitoring.subtitle')} />
  );

  if (alerts.isLoading) {
    return (
      <div className="space-y-4">
        {header}
        <SkeletonList rows={6} label={t('monitoring.loading')} />
      </div>
    );
  }

  if (alerts.isError) {
    return (
      <div className="space-y-4">
        {header}
        <ErrorState
          title={t('monitoring.errorTitle')}
          description={alerts.error.message}
          retryLabel={t('common.retry')}
          onRetry={() => alerts.refetch()}
        />
      </div>
    );
  }

  const maxValue = points.data?.reduce((max, p) => Math.max(max, p.value), 0) ?? 0;

  return (
    <div className="space-y-6">
      {header}

      <Card title={t('monitoring.queryMetrics')}>
        <div className="grid gap-4 md:grid-cols-3 md:items-end">
          <Input
            label={t('monitoring.metricName')}
            value={metricName}
            onChange={(event) => setMetricName(event.target.value)}
          />
          <Input
            label={t('monitoring.ciId')}
            value={ciId}
            onChange={(event) => setCiId(event.target.value)}
          />
          <Button
            variant="secondary"
            icon={<Play />}
            onClick={() => setQuery({ name: metricName, ci_id: ciId })}
            disabled={!metricName}
          >
            {t('monitoring.runQuery')}
          </Button>
        </div>
        {points.isFetching ? (
          <p className="mt-4 text-sm text-fg-muted">{t('monitoring.loading')}</p>
        ) : points.data?.length ? (
          <div className="mt-4">
            <div
              className="flex h-40 items-end gap-1"
              role="img"
              aria-label={t('monitoring.series')}
            >
              {points.data.map((point) => (
                <div
                  key={point.timestamp}
                  className="flex-1 rounded-t-sm bg-accent/70"
                  style={{
                    height: `${maxValue > 0 ? Math.max((point.value / maxValue) * 100, 2) : 2}%`,
                  }}
                  title={`${new Date(point.timestamp).toLocaleString()}: ${point.value}`}
                />
              ))}
            </div>
            <p className="mt-2 text-xs text-fg-muted tabular-nums">
              {t('monitoring.points', { count: points.data.length })}
            </p>
          </div>
        ) : query.name ? (
          <div className="mt-4">
            <EmptyState title={t('monitoring.noData')} description={t('monitoring.noDataHint')} />
          </div>
        ) : null}
      </Card>

      <Card title={t('monitoring.createAlert')}>
        <div className="grid gap-4 md:grid-cols-6 md:items-end">
          <Input
            label={t('monitoring.ruleName')}
            value={form.name}
            onChange={(event) => setForm({ ...form, name: event.target.value })}
          />
          <Input
            label={t('monitoring.metricName')}
            value={form.metric_name}
            onChange={(event) => setForm({ ...form, metric_name: event.target.value })}
          />
          <Select
            label={t('monitoring.condition')}
            value={form.condition}
            options={[
              { value: 'gt', label: '>' },
              { value: 'lt', label: '<' },
              { value: 'eq', label: '=' },
            ]}
            onChange={(event) =>
              setForm({
                ...form,
                condition: event.target.value as AlertRuleCreateRequest['condition'],
              })
            }
          />
          <Input
            label={t('monitoring.threshold')}
            type="number"
            value={form.threshold}
            onChange={(event) => setForm({ ...form, threshold: Number(event.target.value) })}
          />
          <Select
            label={t('monitoring.severity')}
            value={form.severity ?? 'warning'}
            options={[
              { value: 'critical', label: t('monitoring.severityCritical') },
              { value: 'warning', label: t('monitoring.severityWarning') },
              { value: 'info', label: t('monitoring.severityInfo') },
            ]}
            onChange={(event) =>
              setForm({
                ...form,
                severity: event.target.value as AlertRuleCreateRequest['severity'],
              })
            }
          />
          <Button
            icon={<Plus />}
            onClick={submitRule}
            loading={createAlert.isPending}
            disabled={!form.name || !form.metric_name}
          >
            {t('common.save')}
          </Button>
        </div>
      </Card>

      <section className="space-y-3" aria-labelledby="monitoring-rules">
        <h2 id="monitoring-rules" className="text-base font-semibold text-fg">
          {t('monitoring.alertRules')}{' '}
          <span className="tabular-nums text-fg-muted">({alerts.data?.length ?? 0})</span>
        </h2>
        {alerts.data?.length ? (
          <Card className="overflow-hidden p-0">
            <ScrollRegion label={t('monitoring.alertRules')}>
              <table className={tableClasses}>
                <thead className={theadClasses}>
                  <tr>
                    <th scope="col" className={thClasses}>
                      {t('monitoring.ruleName')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('monitoring.metricName')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('monitoring.condition')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('monitoring.severity')}
                    </th>
                    <th scope="col" className={`${thClasses} text-right`}>
                      {t('common.actions')}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {alerts.data.map((rule) => (
                    <tr key={rule.id} className={trClasses}>
                      <td className={tdPrimaryClasses}>{rule.name}</td>
                      <td className={tdMonoClasses}>{rule.metric_name}</td>
                      <td className={`${tdClasses} whitespace-nowrap font-mono text-xs`}>
                        {rule.condition
                          ? `${rule.condition === 'gt' ? '>' : rule.condition === 'lt' ? '<' : '='} ${rule.threshold}`
                          : 'AST'}{' '}
                        ({rule.duration})
                      </td>
                      <td className={tdClasses}>
                        <Badge variant={severityVariants[ruleSeverity(rule)] ?? 'neutral'}>
                          {t(`monitoring.severity_${ruleSeverity(rule)}`)}
                        </Badge>
                      </td>
                      <td className={tdClasses}>
                        <div className="flex justify-end">
                          <Button
                            variant="ghost"
                            size="sm"
                            className="text-danger"
                            onClick={() => deleteAlert.mutate(rule.id)}
                            disabled={deleteAlert.isPending}
                          >
                            {t('common.delete')}
                          </Button>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </ScrollRegion>
          </Card>
        ) : (
          <EmptyState
            icon={<BellRing />}
            title={t('monitoring.noAlerts')}
            description={t('monitoring.noAlertsHint')}
          />
        )}
      </section>
    </div>
  );
}
