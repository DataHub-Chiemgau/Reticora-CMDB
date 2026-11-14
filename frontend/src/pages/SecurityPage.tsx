import { Download, Eraser, FileText, KeyRound, RefreshCw, ShieldCheck } from 'lucide-react';
import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { documentApi, type ComplianceReportRequest } from '../api/client';
import { useClients } from '../api/cmdbHooks';
import {
  useComplianceReports,
  useCreateComplianceReport,
  useRetentionPolicy,
  useRotateCredentialKeys,
  useRunErasure,
  useSaveRetentionPolicy,
  useSecurityReport,
} from '../api/hooks';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { MetricCard } from '../components/ui/MetricCard';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import {
  tableClasses,
  tdClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';
import { ScrollRegion } from '../components/ScrollRegion';
import { useModules } from '../hooks/useEntitlements';
import { isActive, useJob } from '../hooks/useJobs';

type ReportStandard = ComplianceReportRequest['standard'];
type ReportFormat = ComplianceReportRequest['format'];

/**
 * ReportsSection generates the compliance report as a job (SEC-04,
 * WP-1129, WP-1135 AK6): standard, scope (organization or a client) and
 * format, the status of the job, and the stored reports, downloaded over
 * the presigned URL of their document.
 */
function ReportsSection({ standard: initialStandard }: { standard: string }) {
  const { t } = useTranslation();
  const clients = useClients();
  const reports = useComplianceReports();
  const create = useCreateComplianceReport();
  const [standard, setStandard] = useState<ReportStandard>(initialStandard as ReportStandard);
  const [scope, setScope] = useState('');
  const [format, setFormat] = useState<ReportFormat>('pdf');
  const [jobId, setJobId] = useState<string | null>(null);
  const [downloadError, setDownloadError] = useState(false);
  const job = useJob(jobId);
  const running = !!jobId && (!job.data || isActive(job.data));
  const done = job.data?.status === 'succeeded';
  const result = (job.data?.result ?? {}) as { document_id?: string };
  const finished = !!job.data && !isActive(job.data);
  const { refetch } = reports;
  // The report enters the list when its job ends (report_job.go): the list
  // is reloaded then, and polls itself while its scan is pending.
  useEffect(() => {
    if (finished) void refetch();
  }, [finished, jobId, refetch]);
  // The document is downloadable only once its scan is clean.
  const entry = reports.data?.data.find((r) => r.document_id === result.document_id);
  const downloadable = done && !!result.document_id && entry?.scan_status === 'clean';
  const download = async (documentId: string) => {
    setDownloadError(false);
    try {
      const { download_url } = await documentApi.contentUrl(documentId);
      window.open(download_url, '_blank', 'noopener');
    } catch {
      setDownloadError(true);
    }
  };
  return (
    <Card title={t('security.reports.title')}>
      <form
        className="flex flex-wrap items-end gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate(
            { standard, format, client_id: scope || undefined },
            {
              onSuccess: (j) => setJobId(j.id),
            },
          );
        }}
      >
        <Select
          label={t('security.standardLabel')}
          className="w-40"
          value={standard}
          onChange={(e) => setStandard(e.target.value as ReportStandard)}
          options={[
            { value: 'iso27001', label: 'ISO 27001' },
            { value: 'nis2', label: 'NIS2' },
            { value: 'kritis', label: 'KRITIS' },
          ]}
        />
        <Select
          label={t('security.reports.scope')}
          className="w-56"
          value={scope}
          onChange={(e) => setScope(e.target.value)}
          options={[
            { value: '', label: t('security.reports.organization') },
            ...(clients.data?.data ?? []).map((c) => ({ value: c.id, label: c.name })),
          ]}
        />
        <Select
          label={t('security.reports.format')}
          className="w-32"
          value={format}
          onChange={(e) => setFormat(e.target.value as ReportFormat)}
          options={[
            { value: 'pdf', label: 'PDF' },
            { value: 'csv', label: 'CSV' },
          ]}
        />
        <Button
          type="submit"
          icon={<FileText />}
          loading={create.isPending || running}
          disabled={running}
        >
          {t('security.reports.create')}
        </Button>
      </form>
      {jobId ? (
        <div className="mt-3 flex flex-wrap items-center gap-3" role="status">
          <span className="text-sm text-fg-muted">
            {job.data
              ? t('security.reports.jobStatus', {
                  status: t(`jobs.statuses.${job.data.status}`, job.data.status),
                  progress: job.data.progress,
                })
              : t('security.reports.jobQueued')}
          </span>
          {done && result.document_id && !downloadable ? (
            <span className="text-sm text-fg-muted">{t('security.reports.scanPending')}</span>
          ) : null}
          {downloadable ? (
            <Button
              size="sm"
              variant="secondary"
              icon={<Download />}
              onClick={() => void download(result.document_id as string)}
            >
              {t('security.reports.download')}
            </Button>
          ) : null}
        </div>
      ) : null}
      {create.isError ? (
        <p className="mt-2 text-sm text-danger">
          {create.error instanceof Error ? create.error.message : ''}
        </p>
      ) : null}
      {downloadError ? (
        <p className="mt-2 text-sm text-danger">{t('security.reports.downloadFailed')}</p>
      ) : null}
      {reports.data?.data.length ? (
        <ScrollRegion label={t('security.reports.title')}>
          <table className={`${tableClasses} mt-4`}>
            <thead className={theadClasses}>
              <tr>
                <th scope="col" className={thClasses}>
                  {t('security.reports.file')}
                </th>
                <th scope="col" className={thClasses}>
                  {t('security.reports.scope')}
                </th>
                <th scope="col" className={thClasses}>
                  {t('security.reports.scan')}
                </th>
                <th scope="col" className={thClasses}>
                  <span className="sr-only">{t('security.reports.download')}</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {reports.data.data.map((r) => (
                <tr key={r.document_id} className={trClasses}>
                  <td className={tdPrimaryClasses}>{r.file_name}</td>
                  <td className={tdClasses}>
                    {r.scope === 'client'
                      ? (clients.data?.data.find((c) => c.id === r.client_id)?.name ?? r.client_id)
                      : t('security.reports.organization')}
                  </td>
                  <td className={tdClasses}>{r.scan_status}</td>
                  <td className={tdClasses}>
                    <Button
                      size="sm"
                      variant="ghost"
                      icon={<Download />}
                      disabled={r.scan_status !== 'clean'}
                      onClick={() => void download(r.document_id)}
                      aria-label={t('security.reports.downloadNamed', { name: r.file_name })}
                    >
                      {t('security.reports.download')}
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </ScrollRegion>
      ) : (
        <p className="mt-3 text-sm text-fg-muted">{t('security.reports.empty')}</p>
      )}
    </Card>
  );
}

/**
 * SecurityPage is the Epic F (Sicherheit, Compliance & DSGVO) cockpit: it
 * surfaces the tenant security report (audit-chain integrity, compliance
 * findings per rule, enabled security capabilities) and the DSGVO data
 * lifecycle controls (retention policy + erasure workflow, credential key
 * rotation).
 */
export function SecurityPage() {
  const { t } = useTranslation();
  const [standard, setStandard] = useState('iso27001');
  const [retentionDays, setRetentionDays] = useState('');
  const [retentionMode, setRetentionMode] = useState<'anonymize' | 'delete'>('anonymize');

  // The report is part of the compliance module (P3); without it the page
  // keeps the DSGVO controls (WP-214).
  const modules = useModules();
  const reportOn = modules.isModuleEnabled('compliance');
  const report = useSecurityReport(standard, reportOn && !modules.isLoading);
  const retention = useRetentionPolicy();
  const saveRetention = useSaveRetentionPolicy();
  const erasure = useRunErasure();
  const rotateKeys = useRotateCredentialKeys();

  const header = (
    <PageHeader
      title={t('security.title')}
      description={t('security.subtitle')}
      actions={
        reportOn ? (
          <>
            <Select
              aria-label={t('security.standardLabel')}
              className="w-40"
              value={standard}
              onChange={(e) => setStandard(e.target.value)}
              options={[
                { value: 'iso27001', label: 'ISO 27001' },
                { value: 'nis2', label: 'NIS2' },
                { value: 'kritis', label: 'KRITIS' },
              ]}
            />
            <Button variant="secondary" icon={<RefreshCw />} onClick={() => void report.refetch()}>
              {t('security.refresh')}
            </Button>
          </>
        ) : null
      }
    />
  );

  if (modules.isLoading || (reportOn && report.isLoading)) {
    return (
      <div className="space-y-4">
        {header}
        <SkeletonList rows={6} label={t('app.loading')} />
      </div>
    );
  }
  if (reportOn && report.isError) {
    return (
      <div className="space-y-4">
        {header}
        <ErrorState
          title={t('security.errorTitle')}
          description={report.error instanceof Error ? report.error.message : undefined}
          retryLabel={t('common.retry')}
          onRetry={() => void report.refetch()}
        />
      </div>
    );
  }

  const data = report.data;
  const findings = data?.findings ?? [];

  return (
    <div className="space-y-6">
      {header}

      {reportOn ? <ReportsSection standard={standard} /> : null}

      {reportOn ? (
        <details className="space-y-4" data-testid="report-preview">
          <summary className="cursor-pointer text-base font-semibold text-fg">
            {t('security.reports.preview')}
          </summary>
          <div className="mt-4 grid gap-4 md:grid-cols-3">
            <MetricCard
              label={t('compliance.score')}
              value={`${Math.round(data?.compliance_score.score ?? 0)}%`}
              hint={t('compliance.scoreHint', {
                passed: data?.compliance_score.passed ?? 0,
                failed: data?.compliance_score.failed ?? 0,
              })}
            />

            <Card title={t('security.auditIntegrity')}>
              <div className="flex flex-wrap items-center gap-3">
                <Badge variant={data?.audit_integrity.intact ? 'success' : 'danger'}>
                  {data?.audit_integrity.intact ? t('audit.intact') : t('audit.broken')}
                </Badge>
                <span className="text-sm text-fg-muted">
                  {t('audit.checkedCount', { count: data?.audit_integrity.checked ?? 0 })}
                </span>
              </div>
              {data?.audit_integrity.broken_reason ? (
                <p className="mt-2 text-sm text-danger">{data.audit_integrity.broken_reason}</p>
              ) : null}
            </Card>

            <Card title={t('security.bySeverity')}>
              <ul className="space-y-1.5 text-sm">
                {(['critical', 'high', 'medium', 'low'] as const).map((sev) => (
                  <li key={sev} className="flex items-center justify-between">
                    <Badge variant={sev === 'critical' || sev === 'high' ? 'danger' : 'info'}>
                      {t(`compliance.severities.${sev}`, sev)}
                    </Badge>
                    <span className="font-mono text-xs tabular-nums text-fg">
                      {data?.failures_by_severity?.[sev] ?? 0}
                    </span>
                  </li>
                ))}
              </ul>
            </Card>
          </div>

          <section className="space-y-3" aria-labelledby="security-findings">
            <h2 id="security-findings" className="text-base font-semibold text-fg">
              {t('security.findings')}{' '}
              <span className="tabular-nums text-fg-muted">({findings.length})</span>
            </h2>
            {findings.length ? (
              <Card className="overflow-hidden p-0">
                <ScrollRegion label={t('security.findings')}>
                  <table className={tableClasses}>
                    <thead className={theadClasses}>
                      <tr>
                        <th scope="col" className={thClasses}>
                          {t('security.rule')}
                        </th>
                        <th scope="col" className={thClasses}>
                          {t('compliance.severity')}
                        </th>
                        <th scope="col" className={thClasses}>
                          {t('ticket.category')}
                        </th>
                        <th scope="col" className={`${thClasses} text-right`}>
                          {t('compliance.ci')}
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      {findings.map((finding) => (
                        <tr key={finding.rule_id} className={trClasses}>
                          <td className={tdPrimaryClasses}>
                            {finding.rule_name}
                            {finding.remediation_hint ? (
                              <p className="mt-0.5 text-sm font-normal text-fg-muted">
                                {finding.remediation_hint}
                              </p>
                            ) : null}
                          </td>
                          <td className={tdClasses}>
                            <Badge variant="danger">
                              {t(`compliance.severities.${finding.severity}`, finding.severity)}
                            </Badge>
                          </td>
                          <td className={tdClasses}>
                            <Badge variant="info">
                              {t(`compliance.categories.${finding.category}`, finding.category)}
                            </Badge>
                          </td>
                          <td className={`${tdClasses} whitespace-nowrap text-right tabular-nums`}>
                            {t('security.affectedCIs', { count: finding.affected_cis.length })}
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
                title={t('security.noFindings')}
                description={t('security.noFindingsHint')}
              />
            )}
          </section>
        </details>
      ) : null}

      <Card title={t('security.retentionTitle')}>
        <form
          className="flex flex-wrap items-end gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            const days = retentionDays === '' ? undefined : Number.parseInt(retentionDays, 10);
            saveRetention.mutate({ retention_days: days, mode: retentionMode });
          }}
        >
          <Input
            label={t('security.retentionDays')}
            type="number"
            min={0}
            className="w-52"
            placeholder={String(retention.data?.retention_days ?? 0)}
            value={retentionDays}
            onChange={(e) => setRetentionDays(e.target.value)}
          />
          <Select
            label={t('security.retentionMode')}
            className="w-72"
            value={retentionMode}
            onChange={(e) => setRetentionMode(e.target.value as 'anonymize' | 'delete')}
            options={[
              { value: 'anonymize', label: t('security.modeAnonymize') },
              { value: 'delete', label: t('security.modeDelete') },
            ]}
          />
          <Button type="submit" loading={saveRetention.isPending}>
            {t('common.save')}
          </Button>
        </form>
        {retention.data ? (
          <p className="mt-3 text-sm text-fg-muted">
            {t('security.retentionCurrent', {
              days: retention.data.retention_days,
              mode: t(
                retention.data.mode === 'delete' ? 'security.modeDelete' : 'security.modeAnonymize',
              ),
            })}
          </p>
        ) : (
          <p className="mt-3 text-sm text-fg-muted">{t('security.retentionNone')}</p>
        )}
        {saveRetention.isError ? (
          <p className="mt-2 text-sm text-danger">
            {saveRetention.error instanceof Error
              ? saveRetention.error.message
              : t('security.retentionError')}
          </p>
        ) : null}

        <div className="mt-4 border-t border-line pt-4">
          <Button
            variant="secondary"
            icon={<Eraser />}
            loading={erasure.isPending}
            onClick={() => {
              if (window.confirm(t('security.erasureConfirm'))) {
                erasure.mutate();
              }
            }}
          >
            {t('security.runErasure')}
          </Button>
          {erasure.data ? (
            <p className="mt-2 text-sm text-fg-muted">
              {t('security.erasureResult', {
                contacts: erasure.data.contacts_affected,
                users: erasure.data.users_affected,
              })}
            </p>
          ) : null}
          {erasure.isError ? (
            <p className="mt-2 text-sm text-danger">
              {erasure.error instanceof Error ? erasure.error.message : t('security.erasureError')}
            </p>
          ) : null}
        </div>
      </Card>

      <Card title={t('security.keyRotationTitle')}>
        <p className="text-sm text-fg-muted">{t('security.keyRotationHint')}</p>
        <div className="mt-3">
          <Button
            variant="secondary"
            icon={<KeyRound />}
            loading={rotateKeys.isPending}
            onClick={() => {
              if (window.confirm(t('security.keyRotationConfirm'))) {
                rotateKeys.mutate();
              }
            }}
          >
            {t('security.rotateKeys')}
          </Button>
          {rotateKeys.data ? (
            <p className="mt-2 text-sm text-fg-muted">
              {t('security.keyRotationResult', {
                version: rotateKeys.data.key_version,
                rotated: rotateKeys.data.rotated,
              })}
            </p>
          ) : null}
          {rotateKeys.isError ? (
            <p className="mt-2 text-sm text-danger">
              {rotateKeys.error instanceof Error
                ? rotateKeys.error.message
                : t('security.keyRotationError')}
            </p>
          ) : null}
        </div>
      </Card>

      {data?.capabilities?.length ? (
        <Card title={t('security.capabilities')}>
          <ul className="flex flex-wrap gap-2">
            {data.capabilities.map((cap) => (
              <li key={cap.feature_key}>
                <Badge variant={cap.enabled ? 'success' : 'neutral'}>{cap.feature_key}</Badge>
              </li>
            ))}
          </ul>
        </Card>
      ) : null}
    </div>
  );
}
