import { useEffect, useMemo, useRef, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import { ApiError, ciApi, exportApi, type ExportJob } from '../api/client';
import { BULK_MAX_IDS, collectCIs, type CIBulkReport } from '../api/cmdb';
import { useCIList } from '../api/hooks';
import { isActive, useJob, useJobResult } from '../hooks/useJobs';
import { Button } from '../components/ui/Button';
import { Badge } from '../components/ui/Badge';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { useRowNavigation } from '../components/ui/useRowNavigation';
import { useCIFilters } from '../stores/ciFilter';
import { useScopeStore } from '../stores/scope';
import { useToastStore } from '../stores/toast';
import { getStatusBadgeVariant, getStatusTranslationKey } from './ciStatus';

interface CIListPageProps {
  onCreateCI: () => void;
}

const bulkStatusValues = ['active', 'inactive', 'maintenance', 'decommissioned'];

/** From this many ids on a bulk change runs as a job (API-08). */
const BULK_JOB_FROM_IDS = 100;

type BulkItem = CIBulkReport['items'][number];

/**
 * BulkFailures lists the ids a bulk change failed for with their error
 * (API-08 partial failure report), named as the list showed them.
 */
function BulkFailures({ items, names }: { items: BulkItem[]; names: Map<string, string> }) {
  const { t } = useTranslation();
  return (
    <ul
      className="space-y-1 text-sm text-red-700 dark:text-red-300"
      aria-label={t('ci.bulk.failures')}
    >
      {items.map((failure) => (
        <li key={failure.id}>
          <span className="font-medium">{names.get(failure.id) ?? failure.id}</span>
          {': '}
          {failure.error}
        </li>
      ))}
    </ul>
  );
}

/**
 * BulkJobReport shows the job a bulk change from 100 ids on runs as: its
 * status and progress while it runs, then its report per id (BLK-01). The
 * report holds only the ids the list collected for this user, so it names
 * no CI the user cannot see.
 */
function BulkJobReport({
  jobId,
  count,
  names,
  onClose,
  onFinished,
}: {
  jobId: string;
  count: number;
  names: Map<string, string>;
  onClose: () => void;
  onFinished: () => void;
}) {
  const { t } = useTranslation();
  const followed = useJob(jobId);
  const job = followed.data;
  const finished = job !== undefined && !isActive(job);
  // GET /jobs/{id} carries the result; the result endpoint is the fallback.
  const fallback = useJobResult(
    finished && job.status !== 'failed' && job.result === undefined ? jobId : null,
  );
  const report = (finished ? (job.result ?? fallback.data) : undefined) as CIBulkReport | undefined;

  const notified = useRef(false);
  useEffect(() => {
    if (finished && !notified.current) {
      notified.current = true;
      onFinished();
    }
  }, [finished, onFinished]);

  const failures = report?.items.filter((item) => item.result === 'error') ?? [];
  const progress = job?.progress ?? 0;

  return (
    <Card title={t('ci.bulk.jobTitle')}>
      <div className="space-y-3 text-sm">
        {!finished ? (
          <>
            <p role="status" className="text-gray-700 dark:text-gray-300">
              <span>{t('ci.bulk.queued', { count })}</span>
              {job ? (
                <span className="ml-1">
                  {t('ci.bulk.jobState', {
                    status: t(`jobs.statuses.${job.status}`, job.status),
                    progress,
                  })}
                </span>
              ) : null}
            </p>
            <div
              role="progressbar"
              aria-label={t('ci.bulk.jobProgress')}
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={progress}
              className="h-2 w-full max-w-md overflow-hidden rounded bg-gray-200 dark:bg-gray-700"
            >
              <div className="h-full bg-primary" style={{ width: `${progress}%` }} />
            </div>
          </>
        ) : null}
        {followed.error && !job ? (
          <p role="alert" className="text-red-600 dark:text-red-400">
            {t('ci.bulk.jobLoadError')}
          </p>
        ) : null}
        {job?.status === 'failed' ? (
          <p role="alert" className="text-red-600 dark:text-red-400">
            {t('ci.bulk.jobFailed', { error: job.error ?? '' })}
          </p>
        ) : null}
        {finished && job.status !== 'failed' && fallback.error ? (
          <p role="alert" className="text-red-600 dark:text-red-400">
            {t('ci.bulk.jobLoadError')}
          </p>
        ) : null}
        {report ? (
          <>
            <p role="status" className="font-medium text-gray-900 dark:text-gray-100">
              {t(report.cancelled ? 'ci.bulk.jobCancelled' : 'ci.bulk.jobDone', {
                succeeded: report.succeeded,
                total: report.total,
                failed: report.failed,
                skipped: report.skipped ?? 0,
              })}
            </p>
            {failures.length > 0 ? <BulkFailures items={failures} names={names} /> : null}
            <div className="max-h-96 overflow-auto rounded border border-gray-200 dark:border-gray-800">
              <table className="w-full text-left text-xs" aria-label={t('ci.bulk.report')}>
                <thead className="sticky top-0 bg-gray-50 dark:bg-gray-950">
                  <tr>
                    <th scope="col" className="px-3 py-2">
                      {t('ci.bulk.reportCI')}
                    </th>
                    <th scope="col" className="px-3 py-2">
                      {t('ci.bulk.reportResult')}
                    </th>
                    <th scope="col" className="px-3 py-2">
                      {t('ci.bulk.reportCode')}
                    </th>
                    <th scope="col" className="px-3 py-2">
                      {t('ci.bulk.reportError')}
                    </th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
                  {report.items.map((item) => (
                    <tr key={item.id}>
                      <td className="px-3 py-1.5">{names.get(item.id) ?? item.id}</td>
                      <td className="px-3 py-1.5">
                        <Badge
                          variant={
                            item.result === 'ok'
                              ? 'success'
                              : item.result === 'error'
                                ? 'danger'
                                : 'warning'
                          }
                        >
                          {t(`ci.bulk.results.${item.result}`, item.result)}
                        </Badge>
                      </td>
                      <td className="px-3 py-1.5 font-mono">{item.code || '—'}</td>
                      <td className="px-3 py-1.5">{item.error ?? ''}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </>
        ) : null}
        {finished ? (
          <Button variant="ghost" size="sm" onClick={onClose}>
            {t('ci.bulk.closeReport')}
          </Button>
        ) : null}
      </div>
    </Card>
  );
}

type ExportFormat = 'csv' | 'json';

type ExportState =
  | { phase: 'idle' }
  | { phase: 'running'; format: ExportFormat; status: ExportJob['status'] }
  | { phase: 'done'; format: ExportFormat; rows: number }
  | { phase: 'error'; messageKey: string };

/** Pause between two status reads of the export job. */
const EXPORT_POLL_INTERVAL_MS = 1000;
/** Status reads before the page stops waiting; the job stays on the export page. */
const EXPORT_POLL_LIMIT = 120;

class ExportDownloadError extends Error {}

/**
 * downloadExport saves the file behind the signed download URL of a completed
 * export job (EXP-01). The URL carries its own signature: it is fetched
 * without the session's bearer token, which never leaves the API origin. If
 * the object store refuses the cross-origin read, the browser opens the
 * signed URL itself (the object store answers CSV as a download).
 */
async function downloadExport(url: string, filename: string) {
  // Only a web URL is followed, never e.g. a javascript: URL.
  if (!/^https?:$/.test(new URL(url, window.location.href).protocol)) {
    throw new ExportDownloadError('download URL is not http(s)');
  }
  let href = url;
  let objectUrl: string | undefined;
  try {
    const response = await fetch(url);
    if (!response.ok) {
      throw new ExportDownloadError(`download answered ${response.status}`);
    }
    objectUrl = URL.createObjectURL(await response.blob());
    href = objectUrl;
  } catch (error) {
    if (error instanceof ExportDownloadError) {
      throw error;
    }
  }
  const anchor = document.createElement('a');
  anchor.href = href;
  anchor.download = filename;
  anchor.rel = 'noopener';
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  if (objectUrl) {
    const revoke = objectUrl;
    window.setTimeout(() => URL.revokeObjectURL(revoke), 30_000);
  }
}

/** exportErrorKey maps a failed export request to its localized message. */
function exportErrorKey(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 403) {
      return 'ci.exportJob.forbidden';
    }
    if (error.status === 503) {
      return 'ci.exportJob.unavailable';
    }
  }
  return 'ci.exportJob.failed';
}

export function CIListPage({ onCreateCI }: CIListPageProps) {
  const { t } = useTranslation();
  const { search, status, ciTypeId, setSearch, setStatus } = useCIFilters();
  const navigate = useNavigate();
  const [offset, setOffset] = useState(0);
  const [selectedIds, setSelectedIds] = useState<string[]>([]);
  const [bulkStatus, setBulkStatus] = useState('');
  const [bulkBusy, setBulkBusy] = useState(false);
  const [bulkFailures, setBulkFailures] = useState<BulkItem[]>([]);
  // The job a bulk change from 100 ids on runs as, followed until its report.
  const [bulkJob, setBulkJob] = useState<{ id: string; count: number } | null>(null);
  const [collecting, setCollecting] = useState(false);
  // Matches of the filter beyond the 500 ids a bulk change takes.
  const [cappedTotal, setCappedTotal] = useState<number | null>(null);
  // Names of the CIs the list showed or collected, for the bulk reports.
  const names = useRef(new Map<string, string>()).current;
  const limit = 25;
  const pushToast = useToastStore((state) => state.push);
  const queryClient = useQueryClient();
  // The list follows the client and site chosen in the layout (withScope).
  const scopeClientId = useScopeStore((state) => state.clientId);
  const scopeSiteId = useScopeStore((state) => state.siteId);
  const [exportState, setExportState] = useState<ExportState>({ phase: 'idle' });
  // Set on unmount so a running poll loop stops instead of updating a gone page.
  const unmounted = useRef(false);
  useEffect(() => {
    unmounted.current = false;
    return () => {
      unmounted.current = true;
    };
  }, []);

  const { data, isLoading, error, refetch } = useCIList({
    search,
    status,
    ci_type_id: ciTypeId,
    limit,
    offset,
  });

  useEffect(() => {
    for (const ci of data?.data ?? []) {
      names.set(ci.id, ci.name);
    }
  }, [data, names]);

  // The selection belongs to the filtered list: it survives paging (F-328),
  // a change of the filter or the scope drops it, so the bulk bar never acts
  // on rows outside the list the user sees.
  const filterKey = [search, status, ciTypeId, scopeClientId, scopeSiteId].join('\u0000');
  useEffect(() => {
    setSelectedIds([]);
    setCappedTotal(null);
  }, [filterKey]);
  const currentFilterKey = useRef(filterKey);
  currentFilterKey.current = filterKey;

  const selectedSet = useMemo(() => new Set(selectedIds), [selectedIds]);
  const allVisibleSelected = Boolean(
    data && data.data.length > 0 && data.data.every((ci) => selectedSet.has(ci.id)),
  );

  function toggleSelectAll() {
    if (!data) {
      return;
    }
    const visible = data.data.map((ci) => ci.id);
    setSelectedIds((current) =>
      allVisibleSelected
        ? current.filter((id) => !visible.includes(id))
        : [...current, ...visible.filter((id) => !current.includes(id))],
    );
  }

  /**
   * selectAllMatches selects every CI of the filtered list, collected page
   * by page, up to the 500 ids one bulk change takes (API-08, F-328).
   */
  async function selectAllMatches() {
    const key = filterKey;
    setCollecting(true);
    try {
      const collected = await collectCIs({ search, status, ci_type_id: ciTypeId }, BULK_MAX_IDS);
      // A filter changed meanwhile: the matches belong to the old list.
      if (unmounted.current || currentFilterKey.current !== key) {
        return;
      }
      for (const ci of collected.cis) {
        names.set(ci.id, ci.name);
      }
      setSelectedIds(collected.cis.map((ci) => ci.id));
      setCappedTotal(collected.total > collected.cis.length ? collected.total : null);
    } catch {
      pushToast({ message: t('ci.bulk.collectError'), variant: 'error' });
    } finally {
      if (!unmounted.current) {
        setCollecting(false);
      }
    }
  }

  function toggleSelect(id: string) {
    setSelectedIds((current) =>
      current.includes(id) ? current.filter((entry) => entry !== id) : [...current, id],
    );
  }

  // Table keyboard pattern (UI-04): one row in the Tab order, arrows move,
  // Enter opens the detail, Space toggles the selection.
  const rowNavigation = useRowNavigation({
    rowIds: data?.data.map((ci) => ci.id) ?? [],
    onOpen: (id) => navigate(`/cmdb/${id}`),
    onToggle: toggleSelect,
  });

  /**
   * startExport queues an export job with the active list filters (API-05,
   * EXP-01), polls it until it is completed and downloads the file over its
   * signed URL. The job takes status, CI type and client as filters; the
   * free-text search and the site are no job filters, the status line says so.
   */
  async function startExport(format: ExportFormat) {
    setExportState({ phase: 'running', format, status: 'pending' });
    try {
      let job = await exportApi.createJob({
        format,
        filters: {
          ...(status ? { status } : {}),
          ...(ciTypeId ? { ci_type_id: ciTypeId } : {}),
          ...(scopeClientId ? { client_id: scopeClientId } : {}),
        },
      });
      for (let reads = 0; job.status === 'pending' || job.status === 'running'; reads++) {
        if (unmounted.current) {
          return;
        }
        if (reads >= EXPORT_POLL_LIMIT) {
          setExportState({ phase: 'error', messageKey: 'ci.exportJob.timeout' });
          return;
        }
        setExportState({ phase: 'running', format, status: job.status });
        if (reads > 0) {
          await new Promise((resolve) => window.setTimeout(resolve, EXPORT_POLL_INTERVAL_MS));
        }
        job = await exportApi.getJob(job.id);
      }
      if (unmounted.current) {
        return;
      }
      if (job.status !== 'completed' || !job.download_url) {
        setExportState({ phase: 'error', messageKey: 'ci.exportJob.failed' });
        return;
      }
      await downloadExport(job.download_url, `cis_export.${format}`);
      if (!unmounted.current) {
        setExportState({ phase: 'done', format, rows: job.row_count ?? 0 });
      }
    } catch (error) {
      if (!unmounted.current) {
        setExportState({ phase: 'error', messageKey: exportErrorKey(error) });
      }
    }
  }

  async function applyBulkStatus() {
    if (!bulkStatus || selectedIds.length === 0 || selectedIds.length > BULK_MAX_IDS) {
      return;
    }
    setBulkBusy(true);
    setBulkFailures([]);
    try {
      // One request for all CIs; the server reports every id (API-08).
      const result = await ciApi.bulk(selectedIds, bulkStatus);
      if ('job_id' in result) {
        // From 100 ids on: the page follows the job to its report.
        setBulkJob({ id: result.job_id, count: selectedIds.length });
      } else if (result.failed === 0) {
        pushToast({
          message: t('ci.bulk.success', { count: result.succeeded }),
          variant: 'success',
        });
      } else {
        setBulkFailures(result.items.filter((item) => item.result === 'error'));
        pushToast({
          message: t('ci.bulk.partial', { succeeded: result.succeeded, failed: result.failed }),
          variant: 'error',
        });
      }
      setSelectedIds([]);
      setCappedTotal(null);
      setBulkStatus('');
      void queryClient.invalidateQueries({ queryKey: ['cis'] });
    } catch {
      pushToast({ message: t('ci.bulk.error'), variant: 'error' });
    } finally {
      setBulkBusy(false);
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
        <div>
          <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">{t('nav.cmdb')}</h2>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">{t('ci.title')}</p>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button onClick={onCreateCI}>{t('form.createCI')}</Button>
          <Button
            variant="secondary"
            loading={exportState.phase === 'running' && exportState.format === 'csv'}
            disabled={exportState.phase === 'running'}
            onClick={() => void startExport('csv')}
          >
            {t('ci.exportCsv')}
          </Button>
          <Button
            variant="secondary"
            loading={exportState.phase === 'running' && exportState.format === 'json'}
            disabled={exportState.phase === 'running'}
            onClick={() => void startExport('json')}
          >
            {t('ci.exportJson')}
          </Button>
        </div>
      </div>

      {exportState.phase === 'running' ? (
        <p role="status" className="text-sm text-gray-700 dark:text-gray-300">
          {t('ci.exportJob.progress', {
            format: exportState.format.toUpperCase(),
            status: t(`export.status_${exportState.status}`),
          })}
          {search || scopeSiteId ? ` ${t('ci.exportJob.filtersIgnored')}` : null}
        </p>
      ) : null}
      {exportState.phase === 'done' ? (
        <p role="status" className="text-sm text-gray-700 dark:text-gray-300">
          {t('ci.exportJob.done', {
            format: exportState.format.toUpperCase(),
            rows: exportState.rows,
          })}
        </p>
      ) : null}
      {exportState.phase === 'error' ? (
        <p role="alert" className="text-sm text-red-600 dark:text-red-400">
          {t(exportState.messageKey)}
        </p>
      ) : null}

      <Card>
        <div className="flex flex-wrap gap-3">
          <Input
            type="text"
            placeholder={t('common.search')}
            value={search}
            onChange={(event) => {
              setSearch(event.target.value);
              setOffset(0);
            }}
            className="max-w-sm"
            aria-label={t('accessibility.searchCIs')}
          />
          <Select
            value={status}
            onChange={(event) => {
              setStatus(event.target.value);
              setOffset(0);
            }}
            options={[
              { value: '', label: t('ci.allStatus') },
              { value: 'active', label: t('ci.statusActive') },
              { value: 'inactive', label: t('ci.statusInactive') },
              { value: 'maintenance', label: t('ci.statusMaintenance') },
              { value: 'decommissioned', label: t('ci.statusDecommissioned') },
            ]}
            className="max-w-xs"
            aria-label={t('accessibility.filterCIStatus')}
          />
        </div>
      </Card>

      {isLoading ? <SkeletonList rows={5} label={t('app.loading')} /> : null}
      {error ? (
        <ErrorState
          title={t('ci.listLoadError')}
          description={error instanceof Error ? error.message : undefined}
          retryLabel={t('common.retry')}
          onRetry={() => void refetch()}
        />
      ) : null}

      {data && data.data.length === 0 ? (
        <EmptyState
          title={t('ci.emptyTitle')}
          description={t('ci.emptyHint')}
          action={<Button onClick={onCreateCI}>{t('form.createCI')}</Button>}
        />
      ) : null}

      {bulkFailures.length > 0 ? (
        <Card title={t('ci.bulk.failures')}>
          <BulkFailures items={bulkFailures} names={names} />
        </Card>
      ) : null}

      {bulkJob ? (
        <BulkJobReport
          key={bulkJob.id}
          jobId={bulkJob.id}
          count={bulkJob.count}
          names={names}
          onClose={() => setBulkJob(null)}
          onFinished={() => void queryClient.invalidateQueries({ queryKey: ['cis'] })}
        />
      ) : null}

      {data && data.data.length > 0 ? (
        <>
          <Card className="p-0 overflow-hidden">
            {selectedIds.length > 0 ? (
              <div
                className="flex flex-wrap items-center gap-3 border-b border-gray-200 bg-primary/5 px-4 py-2 dark:border-gray-800"
                role="toolbar"
                aria-label={t('ci.bulk.toolbarLabel')}
              >
                <span className="text-sm font-medium text-gray-700 dark:text-gray-200">
                  {t('ci.bulk.selected', { count: selectedIds.length })}
                </span>
                <Select
                  value={bulkStatus}
                  onChange={(event) => setBulkStatus(event.target.value)}
                  aria-label={t('ci.bulk.setStatus')}
                  className="max-w-xs"
                  options={[
                    { value: '', label: t('ci.bulk.setStatus') },
                    ...bulkStatusValues.map((value) => ({
                      value,
                      label: t(getStatusTranslationKey(value)),
                    })),
                  ]}
                />
                <Button
                  size="sm"
                  disabled={!bulkStatus || selectedIds.length > BULK_MAX_IDS}
                  loading={bulkBusy}
                  onClick={() => void applyBulkStatus()}
                >
                  {t('ci.bulk.apply')}
                </Button>
                {data.total > selectedIds.length && selectedIds.length < BULK_MAX_IDS ? (
                  <Button
                    variant="secondary"
                    size="sm"
                    loading={collecting}
                    onClick={() => void selectAllMatches()}
                  >
                    {data.total > BULK_MAX_IDS
                      ? t('ci.bulk.selectFirstMatches', { count: BULK_MAX_IDS })
                      : t('ci.bulk.selectAllMatches', { count: data.total })}
                  </Button>
                ) : null}
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => {
                    setSelectedIds([]);
                    setCappedTotal(null);
                  }}
                >
                  {t('ci.bulk.clearSelection')}
                </Button>
                {cappedTotal !== null ? (
                  <p role="status" className="w-full text-xs text-gray-700 dark:text-gray-300">
                    {t('ci.bulk.capped', { max: BULK_MAX_IDS, total: cappedTotal })}
                  </p>
                ) : null}
                {selectedIds.length > BULK_MAX_IDS ? (
                  <p role="alert" className="w-full text-xs text-red-600 dark:text-red-400">
                    {t('ci.bulk.tooMany', { max: BULK_MAX_IDS, count: selectedIds.length })}
                  </p>
                ) : selectedIds.length >= BULK_JOB_FROM_IDS ? (
                  <p className="w-full text-xs text-gray-700 dark:text-gray-300">
                    {t('ci.bulk.jobHint', { count: BULK_JOB_FROM_IDS })}
                  </p>
                ) : null}
              </div>
            ) : null}
            <div className="overflow-x-auto">
              <table className="w-full text-left text-sm">
                <thead className="border-b bg-gray-50 dark:border-gray-800 dark:bg-gray-950">
                  <tr>
                    <th scope="col" className="w-10 px-4 py-3">
                      <input
                        type="checkbox"
                        checked={allVisibleSelected}
                        onChange={toggleSelectAll}
                        aria-label={t('ci.bulk.selectAll')}
                        className="h-4 w-4 rounded border-gray-300 accent-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40"
                      />
                    </th>
                    <th scope="col" className="px-4 py-3">
                      {t('ci.name')}
                    </th>
                    <th scope="col" className="px-4 py-3">
                      {t('ci.status')}
                    </th>
                    <th scope="col" className="px-4 py-3">
                      {t('ci.manufacturer')}
                    </th>
                    <th scope="col" className="px-4 py-3">
                      {t('ci.model')}
                    </th>
                    <th scope="col" className="px-4 py-3">
                      {t('ci.managementIp')}
                    </th>
                    <th scope="col" className="px-4 py-3">
                      {t('ci.source')}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {data.data.map((ci) => (
                    <tr
                      key={ci.id}
                      {...rowNavigation.getRowProps(ci.id)}
                      onClick={() => navigate(`/cmdb/${ci.id}`)}
                      className="cursor-pointer border-b border-gray-100 transition hover:bg-gray-50 focus-visible:bg-gray-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-primary/40 dark:border-gray-800 dark:hover:bg-gray-950 dark:focus-visible:bg-gray-950"
                    >
                      <td className="px-4 py-3">
                        <input
                          type="checkbox"
                          checked={selectedSet.has(ci.id)}
                          onChange={() => toggleSelect(ci.id)}
                          onClick={(event) => event.stopPropagation()}
                          // The row is the Tab stop; Space on it toggles this box.
                          tabIndex={-1}
                          aria-label={t('ci.bulk.selectRow', { name: ci.name })}
                          className="h-4 w-4 rounded border-gray-300 accent-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40"
                        />
                      </td>
                      <td className="px-4 py-3 font-medium text-gray-900 dark:text-gray-100">
                        <Link
                          to={`/cmdb/${ci.id}`}
                          onClick={(event) => event.stopPropagation()}
                          // The row is the Tab stop; Enter on it opens this link's target.
                          tabIndex={-1}
                          className="underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40"
                        >
                          {ci.name}
                        </Link>
                      </td>
                      <td className="px-4 py-3">
                        <Badge variant={getStatusBadgeVariant(ci.status)}>
                          {t(getStatusTranslationKey(ci.status))}
                        </Badge>
                      </td>
                      <td className="px-4 py-3">{ci.manufacturer || '—'}</td>
                      <td className="px-4 py-3">{ci.model || '—'}</td>
                      <td className="px-4 py-3 font-mono text-xs">{ci.management_ip || '—'}</td>
                      <td className="px-4 py-3">{ci.discovery_source || '—'}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </Card>
        </>
      ) : null}

      {data ? (
        <div className="flex items-center justify-between text-sm text-gray-600 dark:text-gray-300">
          <span>
            {data.total} {t('common.entries')}
          </span>
          <div className="flex gap-2">
            <Button
              variant="secondary"
              disabled={offset === 0}
              onClick={() => setOffset(Math.max(0, offset - limit))}
            >
              ← {t('common.back')}
            </Button>
            <Button
              variant="secondary"
              disabled={!data.has_more}
              onClick={() => setOffset(offset + limit)}
            >
              {t('common.next')} →
            </Button>
          </div>
        </div>
      ) : null}
    </div>
  );
}
