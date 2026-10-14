/**
 * JobCenterPage — the jobs of the organization across all kinds (UI-16,
 * JOB-01): status and progress, cancel of a queued or running job and the
 * result of a succeeded one. The list refreshes while a job is active.
 */
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Modal } from '../components/ui/Modal';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import {
  JOB_STATUSES,
  isActive,
  useCancelJob,
  useJobResult,
  useJobs,
  type JobStatus,
} from '../hooks/useJobs';

const statusVariant: Record<JobStatus, 'neutral' | 'info' | 'success' | 'danger' | 'warning'> = {
  queued: 'neutral',
  running: 'info',
  succeeded: 'success',
  failed: 'danger',
  cancelled: 'warning',
};

export function JobCenterPage() {
  const { t, i18n } = useTranslation();
  const [status, setStatus] = useState('');
  const [resultId, setResultId] = useState<string | null>(null);
  const { data, isLoading, error, refetch } = useJobs({ status });
  const cancel = useCancelJob();
  const result = useJobResult(resultId);
  const jobs = data?.data ?? [];

  return (
    <div className="space-y-4">
      <div>
        <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">{t('jobs.title')}</h2>
        <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">{t('jobs.subtitle')}</p>
      </div>

      <Card>
        <Select
          label={t('jobs.status')}
          value={status}
          onChange={(event) => setStatus(event.target.value)}
          className="max-w-xs"
          options={[
            { value: '', label: t('jobs.allStatuses') },
            ...JOB_STATUSES.map((value) => ({ value, label: t(`jobs.statuses.${value}`) })),
          ]}
        />
      </Card>

      {isLoading ? <SkeletonList rows={4} label={t('app.loading')} /> : null}
      {error ? (
        <ErrorState
          title={t('jobs.loadError')}
          description={error instanceof Error ? error.message : undefined}
          retryLabel={t('common.retry')}
          onRetry={() => void refetch()}
        />
      ) : null}
      {data && jobs.length === 0 ? <EmptyState title={t('jobs.empty')} /> : null}
      {cancel.error ? (
        <p role="alert" className="text-sm text-red-600">
          {cancel.error instanceof Error ? cancel.error.message : String(cancel.error)}
        </p>
      ) : null}

      {jobs.length > 0 ? (
        <Card>
          <table className="w-full text-left text-sm">
            <thead className="text-xs uppercase text-gray-500">
              <tr>
                <th className="pb-2">{t('jobs.kind')}</th>
                <th className="pb-2">{t('jobs.status')}</th>
                <th className="pb-2">{t('jobs.progress')}</th>
                <th className="pb-2">{t('jobs.created')}</th>
                <th className="pb-2">{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
              {jobs.map((job) => (
                <tr key={job.id}>
                  <td className="py-2 font-mono text-xs">
                    {t(`jobs.kinds.${job.kind}`, job.kind)}
                  </td>
                  <td className="py-2">
                    <Badge variant={statusVariant[job.status] ?? 'neutral'}>
                      {t(`jobs.statuses.${job.status}`, job.status)}
                    </Badge>
                    {job.error ? <p className="mt-1 text-xs text-red-600">{job.error}</p> : null}
                  </td>
                  <td className="py-2">
                    <div
                      role="progressbar"
                      aria-label={t('jobs.progressOf', { kind: job.kind })}
                      aria-valuemin={0}
                      aria-valuemax={100}
                      aria-valuenow={job.progress}
                      className="h-2 w-32 overflow-hidden rounded bg-gray-200 dark:bg-gray-700"
                    >
                      <div className="h-full bg-primary" style={{ width: `${job.progress}%` }} />
                    </div>
                  </td>
                  <td className="py-2 text-xs">
                    <time dateTime={job.created_at}>
                      {new Date(job.created_at).toLocaleString(i18n.language)}
                    </time>
                  </td>
                  <td className="py-2">
                    {isActive(job) ? (
                      <Button
                        size="sm"
                        variant="secondary"
                        disabled={cancel.isPending}
                        onClick={() => cancel.mutate(job.id)}
                      >
                        {t('jobs.cancel')}
                      </Button>
                    ) : null}
                    {job.status === 'succeeded' ? (
                      <Button size="sm" variant="ghost" onClick={() => setResultId(job.id)}>
                        {t('jobs.result')}
                      </Button>
                    ) : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </Card>
      ) : null}

      <Modal
        open={resultId !== null}
        onOpenChange={(open) => {
          if (!open) setResultId(null);
        }}
        title={t('jobs.result')}
      >
        {result.isLoading ? (
          <p className="text-sm text-gray-500">{t('app.loading')}</p>
        ) : result.error ? (
          <p role="alert" className="text-sm text-red-600">
            {result.error instanceof Error ? result.error.message : String(result.error)}
          </p>
        ) : (
          <pre className="max-h-96 overflow-auto rounded bg-gray-50 p-3 text-xs dark:bg-gray-800">
            {JSON.stringify(result.data, null, 2)}
          </pre>
        )}
      </Modal>
    </div>
  );
}
