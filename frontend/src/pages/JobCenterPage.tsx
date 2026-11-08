/**
 * JobCenterPage — the jobs of the organization across all kinds (UI-16,
 * JOB-01): status and progress, cancel of a job the server would cancel
 * (queued, or running of a cancellable kind) for its creator or a holder of
 * the kind's permission, and the result of a job that has one (a cancelled
 * job's partial report, F-347). The list refreshes while a job is active and
 * after a cancel until the partial report is there. "Rückgängig" reverts a
 * succeeded job of a kind with revert within its window (E-25, WP-816); the
 * results of the bulk kinds and their revert appear as a table.
 */
import { ListChecks } from 'lucide-react';
import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ApiError } from '../api/client';
import { ScrollRegion } from '../components/ScrollRegion';
import { BulkReportTable, bulkReportOf, type BulkEntity } from '../components/bulk/BulkBar';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Modal } from '../components/ui/Modal';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { Toolbar, ToolbarEnd } from '../components/ui/Toolbar';
import {
  tableClasses,
  tdClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';
import { cn } from '../components/ui/utils';
import {
  JOB_STATUSES,
  isCancellable,
  isRevertible,
  revertibleUntil,
  useCancelJob,
  useJobs,
  useMayControlJob,
  useRevertJob,
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

/**
 * BULK_RESULT_KINDS are the kinds whose result is a report per id
 * (BulkResult, BulkRevertResult): shown as a table, the others as JSON.
 */
const BULK_RESULT_KINDS: Readonly<Record<string, BulkEntity>> = {
  'ci.bulk': 'ci',
  'asset.bulk': 'asset',
  'bulk.revert': 'ci',
};

/** The names of the report table: the job center knows only the ids. */
const NO_NAMES = new Map<string, string>();

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
  const revert = useRevertJob();
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

  const kindLabel = (kind: string) => t(`jobs.kinds.${kind}`, kind);
  const cancelJob = (job: Job) => {
    revert.reset();
    cancel.mutate(job.id, {
      onSuccess: () => {
        if (job.status === 'running') setAwaiting(job.id);
      },
    });
  };
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

  const revertJob = (job: Job) => {
    cancel.reset();
    revert.mutate(job.id);
  };
  const revertStatus = revert.error instanceof ApiError ? revert.error.status : undefined;
  const revertError = !revert.error
    ? null
    : revertStatus === 403
      ? t('jobs.revertForbidden')
      : revertStatus === 409
        ? t('jobs.revertConflict', {
            detail:
              revert.error instanceof ApiError
                ? revert.error.detail || revert.error.title
                : String(revert.error),
          })
        : t('jobs.revertFailed', {
            error: revert.error instanceof Error ? revert.error.message : String(revert.error),
          });
  const now = Date.now();
  const shownReport =
    shown && BULK_RESULT_KINDS[shown.kind] ? bulkReportOf(shown.result) : undefined;

  return (
    <div className="space-y-4">
      <PageHeader title={t('jobs.title')} description={t('jobs.subtitle')} />

      <Toolbar>
        <Select
          aria-label={t('jobs.status')}
          value={status}
          onChange={(event) => setStatus(event.target.value)}
          className="w-44"
          options={[
            { value: '', label: t('jobs.allStatuses') },
            ...JOB_STATUSES.map((value) => ({ value, label: t(`jobs.statuses.${value}`) })),
          ]}
        />
        {data ? (
          <ToolbarEnd>
            <span className="text-sm text-fg-muted tabular-nums">
              {data.total} {t('common.entries')}
            </span>
          </ToolbarEnd>
        ) : null}
      </Toolbar>

      {isLoading ? <SkeletonList rows={4} label={t('app.loading')} /> : null}
      {error ? (
        <ErrorState
          title={t('jobs.loadError')}
          description={error instanceof Error ? error.message : undefined}
          retryLabel={t('common.retry')}
          onRetry={() => void refetch()}
        />
      ) : null}
      {data && jobs.length === 0 ? (
        <EmptyState
          icon={<ListChecks />}
          title={t('jobs.empty')}
          description={t('jobs.emptyHint')}
        />
      ) : null}
      {cancelError ? <ErrorState title={cancelError} /> : null}
      {cancel.isSuccess ? (
        <p role="status" className="text-sm text-fg-muted">
          {t('jobs.cancelDone')}
        </p>
      ) : null}
      {revertError ? <ErrorState title={revertError} /> : null}
      {revert.isSuccess ? (
        <p role="status" className="text-sm text-fg-muted">
          {revert.data.kind === 'job'
            ? t('jobs.revertQueued', {
                kind: t(`jobs.kinds.${revert.data.job.kind}`, revert.data.job.kind),
              })
            : t('jobs.revertDone')}
        </p>
      ) : null}

      {jobs.length > 0 ? (
        <Card className="overflow-hidden p-0">
          <ScrollRegion label={t('jobs.title')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('jobs.kind')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('jobs.status')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('jobs.progress')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('jobs.created')}
                  </th>
                  <th scope="col" className={`${thClasses} text-right`}>
                    {t('common.actions')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {jobs.map((job) => {
                  const counts = job.status === 'cancelled' ? reportCounts(job.result) : undefined;
                  return (
                    <tr key={job.id} data-job-id={job.id} className={trClasses}>
                      <td className={`${tdClasses} font-medium`}>
                        {kindLabel(job.kind)}
                        {kindLabel(job.kind) !== job.kind ? (
                          <span
                            className="block font-mono text-xs font-normal text-fg-muted"
                            title={t('jobs.kindKey', { kind: job.kind })}
                          >
                            {job.kind}
                          </span>
                        ) : null}
                      </td>
                      <td className={tdClasses}>
                        <Badge variant={statusVariant[job.status] ?? 'neutral'}>
                          {t(`jobs.statuses.${job.status}`, job.status)}
                        </Badge>
                        {job.error ? <p className="mt-1 text-xs text-danger">{job.error}</p> : null}
                        {job.reverted_at ? (
                          <p className="mt-1 text-xs text-fg-muted">{t('jobs.reverted')}</p>
                        ) : null}
                        {counts ? (
                          <p className="mt-1 text-xs text-fg-muted">
                            {t('jobs.partialReportSummary', { ...counts })}
                          </p>
                        ) : null}
                      </td>
                      <td className={tdClasses}>
                        <div className="flex items-center gap-2.5">
                          <div
                            role="progressbar"
                            aria-label={t('jobs.progressOf', { kind: job.kind })}
                            aria-valuemin={0}
                            aria-valuemax={100}
                            aria-valuenow={job.progress}
                            className="h-1.5 w-28 overflow-hidden rounded-full bg-fg/10"
                          >
                            <div
                              className={cn(
                                'h-full rounded-full',
                                job.status === 'failed'
                                  ? 'bg-red-600 dark:bg-red-400'
                                  : 'bg-accent',
                              )}
                              style={{ width: `${job.progress}%` }}
                            />
                          </div>
                          <span className="w-9 text-right text-xs tabular-nums">
                            {job.progress}%
                          </span>
                        </div>
                      </td>
                      <td className={`${tdClasses} whitespace-nowrap tabular-nums`}>
                        <time dateTime={job.created_at}>
                          {new Date(job.created_at).toLocaleString(i18n.language)}
                        </time>
                      </td>
                      <td className={tdClasses}>
                        <div className="flex items-center justify-end gap-1.5">
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
                          {isRevertible(job, now) && mayControl(job) ? (
                            <Button
                              size="sm"
                              variant="secondary"
                              disabled={revert.isPending}
                              title={t('jobs.revertUntil', {
                                until: revertibleUntil(job)?.toLocaleString(i18n.language),
                              })}
                              onClick={() => revertJob(job)}
                            >
                              {t('jobs.revert')}
                            </Button>
                          ) : null}
                          {hasResult(job) ? (
                            <Button size="sm" variant="ghost" onClick={() => setResultId(job.id)}>
                              {t(job.status === 'cancelled' ? 'jobs.partialReport' : 'jobs.result')}
                            </Button>
                          ) : null}
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </ScrollRegion>
        </Card>
      ) : null}

      <Modal
        open={shown !== undefined}
        onOpenChange={(open) => {
          if (!open) setResultId(null);
        }}
        title={t(shown?.status === 'cancelled' ? 'jobs.partialReport' : 'jobs.result')}
      >
        {shown && shownReport ? (
          <div className="space-y-3 text-sm">
            <p role="status" className="font-medium text-fg">
              {t('jobs.resultSummary', {
                succeeded: shownReport.succeeded,
                total: shownReport.total,
                failed: shownReport.failed,
                skipped: shownReport.skipped,
              })}
            </p>
            <BulkReportTable
              entity={BULK_RESULT_KINDS[shown.kind] ?? 'ci'}
              report={shownReport}
              names={NO_NAMES}
            />
          </div>
        ) : (
          <pre className="max-h-96 overflow-auto rounded-lg border border-line bg-canvas p-3 font-mono text-xs text-fg">
            {JSON.stringify(shown?.result, null, 2)}
          </pre>
        )}
      </Modal>
    </div>
  );
}
