/**
 * JobCenterPage — the jobs of the organization across all kinds (UI-16,
 * JOB-01): status and progress, cancel of a job the server would cancel
 * (queued, or running of a cancellable kind) for its creator or a holder of
 * the kind's permission, and the result of a job that has one (a cancelled
 * job's partial report, F-347). The list refreshes while a job is active and
 * after a cancel until the partial report is there.
 */
import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ApiError } from '../api/client';
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
  isCancellable,
  useCancelJob,
  useJobs,
  useMayControlJob,
  type Job,
  type JobStatus,
} from '../hooks/useJobs';

const statusVariant: Record<JobStatus, 'neutral' | 'info' | 'success' | 'danger' | 'warning'> = {
  queued: 'neutral',
  running: 'info',
  succeeded: 'success',
  failed: 'danger',
  cancelled: 'warning',
};

/** REPORT_WAIT_MS bounds how long the list is read again for a cancelled job's partial report. */
const REPORT_WAIT_MS = 30_000;

/** The counts of a bulk report (CIBulkResult); other kinds have their own result shape. */
interface ReportCounts {
  total: number;
  succeeded: number;
  failed: number;
  skipped: number;
}

function reportCounts(result: unknown): ReportCounts | undefined {
  if (typeof result !== 'object' || result === null) return undefined;
  const r = result as Record<string, unknown>;
  if (typeof r.total !== 'number' || typeof r.succeeded !== 'number') return undefined;
  return {
    total: r.total,
    succeeded: r.succeeded,
    failed: typeof r.failed === 'number' ? r.failed : 0,
    skipped: typeof r.skipped === 'number' ? r.skipped : 0,
  };
}

/** hasResult reports whether the job carries a result (GET /jobs/{id}/result answers 200). */
function hasResult(job: Job): boolean {
  return job.result !== undefined && job.result !== null;
}

export function JobCenterPage() {
  const { t, i18n } = useTranslation();
  const [status, setStatus] = useState('');
  const [resultId, setResultId] = useState<string | null>(null);
  // The job cancelled while running whose partial report is still due.
  const [awaiting, setAwaiting] = useState<string | null>(null);
  const { data, isLoading, error, refetch } = useJobs({ status }, { follow: awaiting !== null });
  const cancel = useCancelJob();
  const mayControl = useMayControlJob();
  const jobs = data?.data ?? [];
  const shown = jobs.find((job) => job.id === resultId);

  useEffect(() => {
    if (awaiting === null) return;
    const timer = window.setTimeout(() => setAwaiting(null), REPORT_WAIT_MS);
    return () => window.clearTimeout(timer);
  }, [awaiting]);
  const awaited = jobs.find((job) => job.id === awaiting);
  useEffect(() => {
    if (awaited && hasResult(awaited)) setAwaiting(null);
  }, [awaited]);

  const cancelJob = (job: Job) =>
    cancel.mutate(job.id, {
      onSuccess: () => {
        if (job.status === 'running') setAwaiting(job.id);
      },
    });
  const cancelStatus = cancel.error instanceof ApiError ? cancel.error.status : undefined;
  const cancelError = !cancel.error
    ? null
    : cancelStatus === 403
      ? t('jobs.cancelForbidden')
      : cancelStatus === 409
        ? t('jobs.cancelConflict')
        : cancelStatus === 404
          ? t('jobs.cancelNotFound')
          : t('jobs.cancelFailed', {
              error: cancel.error instanceof Error ? cancel.error.message : String(cancel.error),
            });

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
      {cancelError ? (
        <p role="alert" className="text-sm text-red-600 dark:text-red-400">
          {cancelError}
        </p>
      ) : null}
      {cancel.isSuccess ? (
        <p role="status" className="text-sm text-gray-700 dark:text-gray-300">
          {t('jobs.cancelDone')}
        </p>
      ) : null}

      {jobs.length > 0 ? (
        <Card>
          <table className="w-full text-left text-sm">
            <thead className="text-xs uppercase text-muted">
              <tr>
                <th className="pb-2">{t('jobs.kind')}</th>
                <th className="pb-2">{t('jobs.status')}</th>
                <th className="pb-2">{t('jobs.progress')}</th>
                <th className="pb-2">{t('jobs.created')}</th>
                <th className="pb-2">{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
              {jobs.map((job) => {
                const counts = job.status === 'cancelled' ? reportCounts(job.result) : undefined;
                return (
                  <tr key={job.id} data-job-id={job.id}>
                    <td className="py-2 font-mono text-xs">
                      {t(`jobs.kinds.${job.kind}`, job.kind)}
                    </td>
                    <td className="py-2">
                      <Badge variant={statusVariant[job.status] ?? 'neutral'}>
                        {t(`jobs.statuses.${job.status}`, job.status)}
                      </Badge>
                      {job.error ? <p className="mt-1 text-xs text-danger">{job.error}</p> : null}
                      {counts ? (
                        <p className="mt-1 text-xs text-gray-700 dark:text-gray-300">
                          {t('jobs.partialReportSummary', { ...counts })}
                        </p>
                      ) : null}
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
                      {isCancellable(job) && mayControl(job) ? (
                        <Button
                          size="sm"
                          variant="secondary"
                          disabled={cancel.isPending}
                          onClick={() => cancelJob(job)}
                        >
                          {t('jobs.cancel')}
                        </Button>
                      ) : null}
                      {hasResult(job) ? (
                        <Button size="sm" variant="ghost" onClick={() => setResultId(job.id)}>
                          {t(job.status === 'cancelled' ? 'jobs.partialReport' : 'jobs.result')}
                        </Button>
                      ) : null}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </Card>
      ) : null}

      <Modal
        open={shown !== undefined}
        onOpenChange={(open) => {
          if (!open) setResultId(null);
        }}
        title={t(shown?.status === 'cancelled' ? 'jobs.partialReport' : 'jobs.result')}
      >
        <pre className="max-h-96 overflow-auto rounded bg-gray-50 p-3 text-xs dark:bg-gray-800">
          {JSON.stringify(shown?.result, null, 2)}
        </pre>
      </Modal>
    </div>
  );
}
