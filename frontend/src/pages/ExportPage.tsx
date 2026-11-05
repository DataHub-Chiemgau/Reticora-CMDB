import { FileDown } from 'lucide-react';
import { useState, type MouseEvent } from 'react';
import { useTranslation } from 'react-i18next';
import type { ExportJob } from '../api/client';
import { useCreateExportJob, useExportJobs } from '../api/hooks';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { AdminSection } from '../components/admin/AdminSection';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import {
  tableClasses,
  tdClasses,
  tdMonoClasses,
  tdNumericClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';
import { cn } from '../components/ui/utils';
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

const linkClasses =
  'rounded text-sm font-medium text-accent hover:underline underline-offset-2 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent';

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

  return (
    <div className="space-y-8">
      <PageHeader title={t('export.title')} description={t('export.subtitle')} />

      {jobs.isLoading ? <SkeletonList rows={6} label={t('export.loading')} /> : null}

      {jobs.isError ? (
        <ErrorState
          title={t('export.errorTitle')}
          description={jobs.error.message}
          retryLabel={t('common.retry')}
          onRetry={() => jobs.refetch()}
        />
      ) : null}

      {jobs.isSuccess ? (
        <>
          <AdminSection title={t('export.startExport')} description={t('export.startHint')}>
            <Card>
              <div className="flex flex-wrap items-end gap-3">
                <Select
                  label={t('export.format')}
                  value={selectedFormat}
                  options={[
                    { value: 'csv', label: 'CSV' },
                    ...(datevAvailable ? [{ value: 'datev', label: 'DATEV' }] : []),
                    { value: 'json', label: 'JSON' },
                  ]}
                  onChange={(event) => setFormat(event.target.value as ExportJob['format'])}
                  className="w-48"
                />
                <Button icon={<FileDown />} onClick={submit} loading={createJob.isPending}>
                  {t('export.startJob')}
                </Button>
              </div>
              {createJob.isError ? (
                <p role="alert" className="mt-3 text-sm text-danger">
                  {createJob.error.message}
                </p>
              ) : null}
            </Card>
          </AdminSection>

          <AdminSection
            title={`${t('export.jobs')} (${jobs.data?.total ?? 0})`}
            description={t('export.jobsHint')}
          >
            {downloadFailed ? (
              <p role="alert" className="text-sm text-danger">
                {t('export.downloadFailed')}
              </p>
            ) : null}
            {jobs.data?.data.length ? (
              <Card className="overflow-hidden p-0">
                <ScrollRegion label={t('export.jobs')}>
                  <table className={tableClasses}>
                    <thead className={theadClasses}>
                      <tr className="text-fg-muted">
                        <th scope="col" className={thClasses}>
                          {t('export.format')}
                        </th>
                        <th scope="col" className={thClasses}>
                          {t('export.status')}
                        </th>
                        <th scope="col" className={cn(thClasses, 'text-right')}>
                          {t('export.rows')}
                        </th>
                        <th scope="col" className={thClasses}>
                          {t('export.createdAt')}
                        </th>
                        <th scope="col" className={thClasses}>
                          {t('common.actions')}
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      {jobs.data.data.map((job) => (
                        <tr key={job.id} className={trClasses}>
                          <td className={cn(tdMonoClasses, 'uppercase')}>{job.format}</td>
                          <td className={tdClasses}>
                            <Badge variant={statusVariants[job.status] ?? 'neutral'}>
                              {t(`export.status_${job.status}`)}
                            </Badge>
                            {job.error_message ? (
                              <p className="mt-1 text-xs text-danger">{job.error_message}</p>
                            ) : null}
                          </td>
                          <td className={tdNumericClasses}>
                            {job.row_count ?? '—'}
                            {job.manifest && job.manifest.omitted.length > 0 ? (
                              <p
                                className="mt-1 text-xs text-fg-subtle"
                                title={job.manifest.omitted.map((o) => o.field).join(', ')}
                              >
                                {t('export.omittedFields', { count: job.manifest.omitted.length })}
                              </p>
                            ) : null}
                          </td>
                          <td className={cn(tdClasses, 'whitespace-nowrap tabular-nums')}>
                            {new Date(job.created_at).toLocaleString()}
                          </td>
                          <td className={tdClasses}>
                            <div className="flex flex-wrap gap-x-4 gap-y-1">
                              {job.download_url ? (
                                <a
                                  href={job.download_url}
                                  download
                                  onClick={(event) =>
                                    download(event, `export_${job.id}.${job.format}`)
                                  }
                                  className={linkClasses}
                                >
                                  {t('export.download')}
                                </a>
                              ) : null}
                              {errorReportURL(job) ? (
                                <a
                                  href={errorReportURL(job)}
                                  download
                                  onClick={(event) =>
                                    download(event, `export_${job.id}.errors.csv`)
                                  }
                                  className={linkClasses}
                                >
                                  {t('export.errorReport')}
                                </a>
                              ) : null}
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
                icon={<FileDown />}
                title={t('export.emptyTitle')}
                description={t('export.emptyHint')}
              />
            )}
          </AdminSection>
        </>
      ) : null}
    </div>
  );
}
