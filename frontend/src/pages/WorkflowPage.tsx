import { useTranslation } from 'react-i18next';
import {
  useApproveWorkflowRun,
  useTriggerWorkflow,
  useWorkflowDefinitions,
  useWorkflowRuns,
} from '../api/hooks';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { SkeletonList } from '../components/ui/Skeleton';

export function WorkflowPage() {
  const { t } = useTranslation();
  const defs = useWorkflowDefinitions();
  const runs = useWorkflowRuns();
  const trigger = useTriggerWorkflow();
  const approve = useApproveWorkflowRun();
  if (defs.isLoading || runs.isLoading)
    return <SkeletonList rows={6} label={t('workflows.loading')} />;
  if (defs.isError)
    return (
      <ErrorState
        title={t('workflows.errorTitle')}
        description={defs.error.message}
        retryLabel={t('common.retry')}
        onRetry={() => defs.refetch()}
      />
    );
  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold text-gray-900 dark:text-white">{t('workflows.title')}</h1>
        <p className="text-sm text-gray-600 dark:text-gray-300">{t('workflows.subtitle')}</p>
      </div>
      <Card title={t('workflows.definitions')}>
        {defs.data?.data.length ? (
          <ul className="space-y-2">
            {defs.data.data.map((def) => (
              <li
                key={def.id}
                className="flex items-center justify-between rounded-lg border border-gray-200 p-3 dark:border-gray-800"
              >
                <div>
                  <p className="font-medium">{def.name}</p>
                  <p className="text-sm text-gray-500">
                    {String(def.trigger.event || def.trigger.type || 'manual')}
                  </p>
                </div>
                <Button size="sm" onClick={() => trigger.mutate(def.id)}>
                  {t('workflows.run')}
                </Button>
              </li>
            ))}
          </ul>
        ) : (
          <EmptyState title={t('workflows.emptyTitle')} description={t('workflows.emptyHint')} />
        )}
      </Card>
      <Card title={t('workflows.runs')}>
        {runs.data?.data.length ? (
          <ul className="space-y-3">
            {runs.data.data.map((run) => (
              <li
                key={run.id}
                className="rounded-lg border border-gray-200 p-3 dark:border-gray-800"
              >
                <div className="flex items-center justify-between">
                  <span className="font-mono text-sm">{run.id}</span>
                  <Badge
                    variant={
                      run.status === 'failed'
                        ? 'danger'
                        : run.status === 'succeeded'
                          ? 'success'
                          : 'info'
                    }
                  >
                    {run.status}
                  </Badge>
                </div>
                {run.steps?.map((step) => (
                  <p key={step.id} className="mt-1 text-sm text-gray-500">
                    #{step.step_index + 1} {step.action_type}: {step.status}
                  </p>
                ))}
                {run.status === 'waiting_approval' ? (
                  <div className="mt-3 flex gap-2">
                    <Button
                      size="sm"
                      onClick={() => approve.mutate({ id: run.id, decision: 'approved' })}
                    >
                      {t('workflows.approve')}
                    </Button>
                    <Button
                      size="sm"
                      variant="danger"
                      onClick={() => approve.mutate({ id: run.id, decision: 'rejected' })}
                    >
                      {t('workflows.reject')}
                    </Button>
                  </div>
                ) : null}
              </li>
            ))}
          </ul>
        ) : (
          <EmptyState title={t('workflows.noRuns')} description={t('workflows.noRunsHint')} />
        )}
      </Card>
    </div>
  );
}
