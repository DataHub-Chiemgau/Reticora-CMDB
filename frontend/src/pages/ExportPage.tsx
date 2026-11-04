import { useState, type MouseEvent } from 'react';
import { useTranslation } from 'react-i18next';
import type { ExportJob } from '../api/client';
import { useCreateExportJob, useExportJobs } from '../api/hooks';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { ScrollRegion } from '../components/ScrollRegion';
import { useEntitlements, useModules } from '../hooks/useEntitlements';
import { downloadExport } from '../lib/exportDownload';

const statusVariants: Record<
  ExportJob['status'],
  'success' | 'warning' | 'danger' | 'neutral' | 'info'
> = {
  pending: 'info',
  running: 'warning',
  completed: 'success',
  failed: 'danger',
  expired: 'neutral',
};

// errorReportURL is the error report of a completed, unexpired DATEV job: the
// asset rows the file leaves out and why (EXP-01, WP-310). The server sends
// the URL only then; the page checks it again so a stale list never offers it.
function errorReportURL(job: ExportJob): string | undefined {
  if (job.format !== 'datev' || job.status !== 'completed' || !job.error_report_url) {
    return undefined;
  }
  if (job.expires_at && new Date(job.expires_at).getTime() <= Date.now()) {
    return undefined;
  }
  return job.error_report_url;
}

export function ExportPage() {
  const { t } = useTranslation();
  const jobs = useExportJobs();
  const createJob = useCreateExportJob();
  const [format, setFormat] = useState<ExportJob['format']>('csv');
  const { isEnabled } = useEntitlements();
  const { isModuleEnabled } = useModules();
  // DATEV is offered only with export_datev, assets_basic and the module
  // inventory, as the server gates it (ENT-05, WP-790).
  const datevAvailable =
    isEnabled('export_datev') && isEnabled('assets_basic') && isModuleEnabled('inventory');
  const selectedFormat = format === 'datev' && !datevAvailable ? 'csv' : format;
  const [downloadFailed, setDownloadFailed] = useState(false);

  // The file is fetched and saved as a same-origin blob: browsers ignore the
  // download attribute of a link to the object store's origin (WebKit then
  // navigates away from the page instead of saving the file).
  const download = (event: MouseEvent<HTMLAnchorElement>, filename: string) => {
    event.preventDefault();
    setDownloadFailed(false);
    downloadExport(event.currentTarget.href, filename).catch(() => setDownloadFailed(true));
  };

  const submit = () => {
    createJob.mutate({ format: selectedFormat });
  };

  if (jobs.isLoading) {
    return <SkeletonList rows={6} label={t('export.loading')} />;
  }

  if (jobs.isError) {
    return (
      <ErrorState
        title={t('export.errorTitle')}
        description={jobs.error.message}
        retryLabel={t('common.retry')}
        onRetry={() => jobs.refetch()}
      />
    );
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold text-gray-900 dark:text-white">{t('export.title')}</h1>
        <p className="text-sm text-gray-600 dark:text-gray-300">{t('export.subtitle')}</p>
      </div>

      <Card title={t('export.startExport')}>
        <div className="grid gap-4 md:grid-cols-3 md:items-end">
          <Select
            label={t('export.format')}
            value={selectedFormat}
            options={[
              { value: 'csv', label: 'CSV' },
              ...(datevAvailable ? [{ value: 'datev', label: 'DATEV' }] : []),
              { value: 'json', label: 'JSON' },
            ]}
            onChange={(event) => setFormat(event.target.value as ExportJob['format'])}
          />
          <Button onClick={submit} disabled={createJob.isPending}>
            {t('export.startJob')}
          </Button>
        </div>
        {createJob.isError ? (
          <p className="mt-2 text-sm text-red-600 dark:text-red-400">{createJob.error.message}</p>
        ) : null}
      </Card>

      <Card title={`${t('export.jobs')} (${jobs.data?.total ?? 0})`}>
        {downloadFailed ? (
          <p role="alert" className="mb-2 text-sm text-red-600 dark:text-red-400">
            {t('export.downloadFailed')}
          </p>
        ) : null}
        {jobs.data?.data.length ? (
          <ScrollRegion label={t('export.jobs')}>
            <table className="min-w-full divide-y divide-gray-200 dark:divide-gray-700">
              <thead>
                <tr className="text-left text-sm font-medium text-gray-500 dark:text-gray-400">
                  <th className="pb-2">{t('export.format')}</th>
                  <th className="pb-2">{t('export.status')}</th>
                  <th className="pb-2">{t('export.rows')}</th>
                  <th className="pb-2">{t('export.createdAt')}</th>
                  <th className="pb-2">{t('common.actions')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
                {jobs.data.data.map((job) => (
                  <tr key={job.id} className="text-sm text-gray-700 dark:text-gray-300">
                    <td className="py-2 font-medium uppercase">{job.format}</td>
                    <td className="py-2">
                      <Badge variant={statusVariants[job.status] ?? 'neutral'}>
                        {t(`export.status_${job.status}`)}
                      </Badge>
                      {job.error_message ? (
                        <p className="mt-1 text-xs text-red-600 dark:text-red-400">
                          {job.error_message}
                        </p>
                      ) : null}
                    </td>
                    <td className="py-2">
                      {job.row_count ?? '—'}
                      {job.manifest && job.manifest.omitted.length > 0 ? (
                        <p
                          className="mt-1 text-xs text-gray-500 dark:text-gray-400"
                          title={job.manifest.omitted.map((o) => o.field).join(', ')}
                        >
                          {t('export.omittedFields', { count: job.manifest.omitted.length })}
                        </p>
                      ) : null}
                    </td>
                    <td className="py-2">{new Date(job.created_at).toLocaleString()}</td>
                    <td className="py-2">
                      {job.download_url ? (
                        <a
                          href={job.download_url}
                          download
                          onClick={(event) => download(event, `export_${job.id}.${job.format}`)}
                          className="text-sm font-medium text-primary hover:underline"
                        >
                          {t('export.download')}
                        </a>
                      ) : null}
                      {errorReportURL(job) ? (
                        <a
                          href={errorReportURL(job)}
                          download
                          onClick={(event) => download(event, `export_${job.id}.errors.csv`)}
                          className="ml-3 text-sm font-medium text-primary hover:underline"
                        >
                          {t('export.errorReport')}
                        </a>
                      ) : null}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </ScrollRegion>
        ) : (
          <EmptyState title={t('export.emptyTitle')} description={t('export.emptyHint')} />
        )}
      </Card>
    </div>
  );
}
