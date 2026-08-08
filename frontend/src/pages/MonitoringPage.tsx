import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  useAlertRules,
  useCreateAlertRule,
  useDeleteAlertRule,
  useMetricPoints,
} from '../api/hooks';
import type { AlertRuleCreateRequest } from '../api/client';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';

const severityVariants = {
  critical: 'danger',
  warning: 'warning',
  info: 'info',
} as const;

export function MonitoringPage() {
  const { t } = useTranslation();
  const alerts = useAlertRules();
  const createAlert = useCreateAlertRule();
  const deleteAlert = useDeleteAlertRule();

  const [metricName, setMetricName] = useState('');
  const [ciId, setCiId] = useState('');
  const [query, setQuery] = useState({ name: '', ci_id: '' });
  const points = useMetricPoints({ name: query.name, ci_id: query.ci_id });

  const [form, setForm] = useState<AlertRuleCreateRequest>({
    name: '',
    metric_name: '',
    condition: 'gt',
    threshold: 0,
    duration: '5m',
    severity: 'warning',
    enabled: true,
  });

  const submitRule = () => {
    if (!form.name || !form.metric_name) return;
    createAlert.mutate(form, { onSuccess: () => setForm({ ...form, name: '' }) });
  };

  if (alerts.isLoading) {
    return <SkeletonList rows={6} label={t('monitoring.loading')} />;
  }

  if (alerts.isError) {
    return (
      <ErrorState
        title={t('monitoring.errorTitle')}
        description={alerts.error.message}
        retryLabel={t('common.retry')}
        onRetry={() => alerts.refetch()}
      />
    );
  }

  const maxValue = points.data?.reduce((max, p) => Math.max(max, p.value), 0) ?? 0;

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold text-gray-900 dark:text-white">
          {t('monitoring.title')}
        </h1>
        <p className="text-sm text-gray-600 dark:text-gray-300">{t('monitoring.subtitle')}</p>
      </div>

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
            onClick={() => setQuery({ name: metricName, ci_id: ciId })}
            disabled={!metricName}
          >
            {t('monitoring.runQuery')}
          </Button>
        </div>
        {points.isFetching ? (
          <p className="mt-4 text-sm text-gray-500 dark:text-gray-400">{t('monitoring.loading')}</p>
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
                  className="flex-1 rounded-t bg-primary/70"
                  style={{
                    height: `${maxValue > 0 ? Math.max((point.value / maxValue) * 100, 2) : 2}%`,
                  }}
                  title={`${new Date(point.timestamp).toLocaleString()}: ${point.value}`}
                />
              ))}
            </div>
            <p className="mt-2 text-xs text-gray-500 dark:text-gray-400">
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
            onClick={submitRule}
            disabled={createAlert.isPending || !form.name || !form.metric_name}
          >
            {t('common.save')}
          </Button>
        </div>
      </Card>

      <Card title={`${t('monitoring.alertRules')} (${alerts.data?.length ?? 0})`}>
        {alerts.data?.length ? (
          <div className="overflow-x-auto">
            <table className="min-w-full divide-y divide-gray-200 dark:divide-gray-700">
              <thead>
                <tr className="text-left text-sm font-medium text-gray-500 dark:text-gray-400">
                  <th className="pb-2">{t('monitoring.ruleName')}</th>
                  <th className="pb-2">{t('monitoring.metricName')}</th>
                  <th className="pb-2">{t('monitoring.condition')}</th>
                  <th className="pb-2">{t('monitoring.severity')}</th>
                  <th className="pb-2">{t('common.actions')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
                {alerts.data.map((rule) => (
                  <tr key={rule.id} className="text-sm text-gray-700 dark:text-gray-300">
                    <td className="py-2 font-medium">{rule.name}</td>
                    <td className="py-2 font-mono text-xs">{rule.metric_name}</td>
                    <td className="py-2">
                      {rule.condition === 'gt' ? '>' : rule.condition === 'lt' ? '<' : '='}{' '}
                      {rule.threshold} ({rule.duration})
                    </td>
                    <td className="py-2">
                      <Badge variant={severityVariants[rule.severity] ?? 'neutral'}>
                        {t(`monitoring.severity_${rule.severity}`)}
                      </Badge>
                    </td>
                    <td className="py-2">
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => deleteAlert.mutate(rule.id)}
                        disabled={deleteAlert.isPending}
                      >
                        {t('common.delete')}
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <EmptyState title={t('monitoring.noAlerts')} description={t('monitoring.noAlertsHint')} />
        )}
      </Card>
    </div>
  );
}
