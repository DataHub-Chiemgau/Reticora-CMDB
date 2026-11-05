import { CheckCircle2, ShieldCheck, Play } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import {
  useComplianceResults,
  useComplianceRules,
  useComplianceScore,
  useEvaluateCompliance,
} from '../api/hooks';
import { ScrollRegion } from '../components/ScrollRegion';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { MetricCard } from '../components/ui/MetricCard';
import { PageHeader } from '../components/ui/PageHeader';
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

const severityVariant = {
  critical: 'danger',
  high: 'danger',
  medium: 'warning',
  low: 'info',
} as const;

export function CompliancePage() {
  const { t } = useTranslation();
  const rules = useComplianceRules();
  const results = useComplianceResults();
  const score = useComplianceScore();
  const evaluate = useEvaluateCompliance();
  const header = (
    <PageHeader
      title={t('compliance.title')}
      description={t('compliance.subtitle')}
      actions={
        <Button icon={<Play />} onClick={() => evaluate.mutate()} loading={evaluate.isPending}>
          {t('compliance.evaluate')}
        </Button>
      }
    />
  );
  if (rules.isLoading || results.isLoading || score.isLoading)
    return (
      <div className="space-y-4">
        {header}
        <SkeletonList rows={6} label={t('compliance.loading')} />
      </div>
    );
  if (rules.isError)
    return (
      <div className="space-y-4">
        {header}
        <ErrorState
          title={t('compliance.errorTitle')}
          description={rules.error.message}
          retryLabel={t('common.retry')}
          onRetry={() => rules.refetch()}
        />
      </div>
    );
  const failures = results.data?.data.filter((result) => result.status === 'fail') ?? [];
  return (
    <div className="space-y-6">
      {header}
      <div className="grid gap-4 sm:grid-cols-3">
        <MetricCard
          label={t('compliance.score')}
          value={`${Math.round(score.data?.overall.score ?? 0)}%`}
          hint={t('compliance.scoreHint', {
            passed: score.data?.overall.passed ?? 0,
            failed: score.data?.overall.failed ?? 0,
          })}
        />
        <MetricCard
          label={t('compliance.resultStatuses.pass')}
          value={score.data?.overall.passed ?? 0}
        />
        <MetricCard
          label={t('compliance.resultStatuses.fail')}
          value={score.data?.overall.failed ?? 0}
          tone={(score.data?.overall.failed ?? 0) > 0 ? 'danger' : 'default'}
        />
      </div>
      <section className="space-y-3" aria-labelledby="compliance-rules">
        <h2 id="compliance-rules" className="text-base font-semibold text-fg">
          {t('compliance.rules')}{' '}
          <span className="tabular-nums text-fg-muted">({rules.data?.total ?? 0})</span>
        </h2>
        {rules.data?.data.length ? (
          <Card className="overflow-hidden p-0">
            <ScrollRegion label={t('compliance.rules')}>
              <table className={tableClasses}>
                <thead className={theadClasses}>
                  <tr>
                    <th scope="col" className={thClasses}>
                      {t('common.name')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('ticket.category')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('compliance.severity')}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {rules.data.data.map((rule) => (
                    <tr key={rule.id} className={trClasses}>
                      <td className={tdPrimaryClasses}>{rule.name}</td>
                      <td className={tdClasses}>
                        <Badge variant="info">
                          {t(`compliance.categories.${rule.category}`, rule.category)}
                        </Badge>
                      </td>
                      <td className={tdClasses}>
                        <Badge
                          variant={
                            severityVariant[rule.severity as keyof typeof severityVariant] ??
                            'neutral'
                          }
                        >
                          {t(`compliance.severities.${rule.severity}`, rule.severity)}
                        </Badge>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </ScrollRegion>
          </Card>
        ) : (
          <EmptyState
            icon={<ShieldCheck />}
            title={t('compliance.emptyRules')}
            description={t('compliance.emptyRulesHint')}
          />
        )}
      </section>
      <section className="space-y-3" aria-labelledby="compliance-failures">
        <h2 id="compliance-failures" className="text-base font-semibold text-fg">
          {t('compliance.failures')}{' '}
          <span className="tabular-nums text-fg-muted">({failures.length})</span>
        </h2>
        {failures.length ? (
          <Card className="overflow-hidden p-0">
            <ScrollRegion label={t('compliance.failures')}>
              <table className={tableClasses}>
                <thead className={theadClasses}>
                  <tr>
                    <th scope="col" className={thClasses}>
                      {t('compliance.ci')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('ci.status')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('compliance.details')}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {failures.map((result) => (
                    <tr key={result.id} className={trClasses}>
                      <td className={tdMonoClasses}>{result.ci_id}</td>
                      <td className={tdClasses}>
                        <Badge variant="danger">
                          {t(`compliance.resultStatuses.${result.status}`, result.status)}
                        </Badge>
                      </td>
                      <td className={tdClasses}>{result.details || '—'}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </ScrollRegion>
          </Card>
        ) : (
          <EmptyState
            icon={<CheckCircle2 />}
            title={t('compliance.noFailures')}
            description={t('compliance.noFailuresHint')}
          />
        )}
      </section>
    </div>
  );
}
