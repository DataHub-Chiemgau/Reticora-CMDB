import { Play, Workflow } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import {
  useApproveWorkflowRun,
  useTriggerWorkflow,
  useWorkflowDefinitions,
  useWorkflowRuns,
} from '../api/hooks';
import { ScrollRegion } from '../components/ScrollRegion';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
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

export function WorkflowPage() {
  const { t } = useTranslation();
  const defs = useWorkflowDefinitions();
  const runs = useWorkflowRuns();
  const trigger = useTriggerWorkflow();
  const approve = useApproveWorkflowRun();
  const header = <PageHeader title={t('workflows.title')} description={t('workflows.subtitle')} />;
  if (defs.isLoading || runs.isLoading)
    return (
      <div className="space-y-4">
        {header}
        <SkeletonList rows={6} label={t('workflows.loading')} />
      </div>
    );
  if (defs.isError)
    return (
      <div className="space-y-4">
        {header}
        <ErrorState
          title={t('workflows.errorTitle')}
          description={defs.error.message}
          retryLabel={t('common.retry')}
          onRetry={() => defs.refetch()}
        />
      </div>
    );
  return (
    <div className="space-y-6">
      {header}
      <section className="space-y-3" aria-labelledby="workflow-definitions">
        <h2 id="workflow-definitions" className="text-base font-semibold text-fg">
          {t('workflows.definitions')}
        </h2>
        {defs.data?.data.length ? (
          <Card className="overflow-hidden p-0">
            <ScrollRegion label={t('workflows.definitions')}>
              <table className={tableClasses}>
                <thead className={theadClasses}>
                  <tr>
                    <th scope="col" className={thClasses}>
                      {t('common.name')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('workflows.trigger')}
                    </th>
                    <th scope="col" className={`${thClasses} text-right`}>
                      {t('common.actions')}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {defs.data.data.map((def) => (
                    <tr key={def.id} className={trClasses}>
                      <td className={tdPrimaryClasses}>{def.name}</td>
                      <td className={tdMonoClasses}>
                        {String(def.trigger.event || def.trigger.type || 'manual')}
                      </td>
                      <td className={tdClasses}>
                        <div className="flex justify-end">
                          <Button
                            size="sm"
                            variant="secondary"
                            icon={<Play />}
                            onClick={() => trigger.mutate(def.id)}
                          >
                            {t('workflows.run')}
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
            icon={<Workflow />}
            title={t('workflows.emptyTitle')}
            description={t('workflows.emptyHint')}
          />
        )}
      </section>
      <section className="space-y-3" aria-labelledby="workflow-runs">
        <h2 id="workflow-runs" className="text-base font-semibold text-fg">
          {t('workflows.runs')}
        </h2>
        {runs.data?.data.length ? (
          <Card className="overflow-hidden p-0">
            <ScrollRegion label={t('workflows.runs')}>
              <table className={tableClasses}>
                <thead className={theadClasses}>
                  <tr>
                    <th scope="col" className={thClasses}>
                      {t('workflows.run')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('ci.status')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('workflows.steps')}
                    </th>
                    <th scope="col" className={`${thClasses} text-right`}>
                      {t('common.actions')}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {runs.data.data.map((run) => (
                    <tr key={run.id} className={trClasses}>
                      <td className={tdMonoClasses}>{run.id}</td>
                      <td className={tdClasses}>
                        <Badge
                          variant={
                            run.status === 'failed'
                              ? 'danger'
                              : run.status === 'succeeded'
                                ? 'success'
                                : 'info'
                          }
                        >
                          {t(`workflows.statuses.${run.status}`, run.status)}
                        </Badge>
                      </td>
                      <td className={tdClasses}>
                        {run.steps?.length ? (
                          <ul className="space-y-0.5">
                            {run.steps.map((step) => (
                              <li key={step.id}>
                                <span className="tabular-nums">#{step.step_index + 1}</span>{' '}
                                <span className="font-mono text-xs text-fg">
                                  {step.action_type}
                                </span>
                                : {t(`workflows.statuses.${step.status}`, step.status)}
                              </li>
                            ))}
                          </ul>
                        ) : (
                          '—'
                        )}
                      </td>
                      <td className={tdClasses}>
                        {run.status === 'waiting_approval' ? (
                          <div className="flex justify-end gap-1.5">
                            <Button
                              size="sm"
                              variant="secondary"
                              onClick={() => approve.mutate({ id: run.id, decision: 'approved' })}
                            >
                              {t('workflows.approve')}
                            </Button>
                            <Button
                              size="sm"
                              variant="ghost"
                              className="text-danger"
                              onClick={() => approve.mutate({ id: run.id, decision: 'rejected' })}
                            >
                              {t('workflows.reject')}
                            </Button>
                          </div>
                        ) : null}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </ScrollRegion>
          </Card>
        ) : (
          <EmptyState title={t('workflows.noRuns')} description={t('workflows.noRunsHint')} />
        )}
      </section>
    </div>
  );
}
