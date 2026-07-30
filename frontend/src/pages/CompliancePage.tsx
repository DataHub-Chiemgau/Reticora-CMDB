import { useTranslation } from 'react-i18next';
import {
  useComplianceResults,
  useComplianceRules,
  useComplianceScore,
  useEvaluateCompliance,
} from '../api/hooks';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { SkeletonList } from '../components/ui/Skeleton';

export function CompliancePage() {
  const { t } = useTranslation();
  const rules = useComplianceRules();
  const results = useComplianceResults();
  const score = useComplianceScore();
  const evaluate = useEvaluateCompliance();
  if (rules.isLoading || results.isLoading || score.isLoading)
    return <SkeletonList rows={6} label={t('compliance.loading')} />;
  if (rules.isError)
    return (
      <ErrorState
        title={t('compliance.errorTitle')}
        description={rules.error.message}
        retryLabel={t('common.retry')}
        onRetry={() => rules.refetch()}
      />
    );
  const failures = results.data?.data.filter((result) => result.status === 'fail') ?? [];
  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-bold text-gray-900 dark:text-white">
            {t('compliance.title')}
          </h1>
          <p className="text-sm text-gray-600 dark:text-gray-300">{t('compliance.subtitle')}</p>
        </div>
        <Button onClick={() => evaluate.mutate()} loading={evaluate.isPending}>
          {t('compliance.evaluate')}
        </Button>
      </div>
      <Card title={t('compliance.score')}>
        <div className="text-4xl font-bold text-primary">
          {Math.round(score.data?.overall.score ?? 0)}%
        </div>
        <p className="text-sm text-gray-500">
          {t('compliance.scoreHint', {
            passed: score.data?.overall.passed ?? 0,
            failed: score.data?.overall.failed ?? 0,
          })}
        </p>
      </Card>
      <Card title={`${t('compliance.rules')} (${rules.data?.total ?? 0})`}>
        {rules.data?.data.length ? (
          <ul className="space-y-2">
            {rules.data.data.map((rule) => (
              <li
                key={rule.id}
                className="rounded-lg border border-gray-200 p-3 text-sm dark:border-gray-800"
              >
                <span className="font-medium">{rule.name}</span>{' '}
                <Badge variant="info">{rule.category}</Badge>
              </li>
            ))}
          </ul>
        ) : (
          <EmptyState
            title={t('compliance.emptyRules')}
            description={t('compliance.emptyRulesHint')}
          />
        )}
      </Card>
      <Card title={`${t('compliance.failures')} (${failures.length})`}>
        {failures.length ? (
          <ul className="space-y-2">
            {failures.map((result) => (
              <li
                key={result.id}
                className="rounded-lg border border-red-200 p-3 text-sm dark:border-red-900"
              >
                <span className="font-mono">{result.ci_id}</span>{' '}
                <Badge variant="danger">{result.status}</Badge>
                <p className="text-gray-500">{result.details}</p>
              </li>
            ))}
          </ul>
        ) : (
          <EmptyState
            title={t('compliance.noFailures')}
            description={t('compliance.noFailuresHint')}
          />
        )}
      </Card>
    </div>
  );
}
