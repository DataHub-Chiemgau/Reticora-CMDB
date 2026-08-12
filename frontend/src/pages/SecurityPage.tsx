import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
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
import { SkeletonList } from '../components/ui/Skeleton';

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

  const report = useSecurityReport(standard);
  const retention = useRetentionPolicy();
  const saveRetention = useSaveRetentionPolicy();
  const erasure = useRunErasure();
  const rotateKeys = useRotateCredentialKeys();

  if (report.isLoading) {
    return <SkeletonList rows={6} label={t('app.loading')} />;
  }
  if (report.isError) {
    return (
      <ErrorState
        title={t('security.errorTitle')}
        description={report.error instanceof Error ? report.error.message : undefined}
        retryLabel={t('common.retry')}
        onRetry={() => void report.refetch()}
      />
    );
  }

  const data = report.data;
  const findings = data?.findings ?? [];

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-bold text-gray-900 dark:text-white">
            {t('security.title')}
          </h1>
          <p className="text-sm text-gray-600 dark:text-gray-300">{t('security.subtitle')}</p>
        </div>
        <div className="flex items-center gap-2">
          <select
            aria-label={t('security.standardLabel')}
            className="rounded-md border border-gray-300 bg-white px-2 py-1.5 text-sm dark:border-gray-700 dark:bg-gray-900 dark:text-white"
            value={standard}
            onChange={(e) => setStandard(e.target.value)}
          >
            <option value="iso27001">ISO 27001</option>
            <option value="nis2">NIS2</option>
            <option value="kritis">KRITIS</option>
          </select>
          <Button variant="secondary" onClick={() => void report.refetch()}>
            {t('security.refresh')}
          </Button>
        </div>
      </div>

      <div className="grid gap-4 md:grid-cols-3">
        <Card title={t('security.auditIntegrity')}>
          <div className="flex items-center gap-3">
            <Badge variant={data?.audit_integrity.intact ? 'success' : 'danger'}>
              {data?.audit_integrity.intact ? t('audit.intact') : t('audit.broken')}
            </Badge>
            <span className="text-sm text-gray-600 dark:text-gray-300">
              {t('audit.checkedCount', { count: data?.audit_integrity.checked ?? 0 })}
            </span>
          </div>
          {data?.audit_integrity.broken_reason ? (
            <p className="mt-2 text-sm text-red-600 dark:text-red-400">
              {data.audit_integrity.broken_reason}
            </p>
          ) : null}
        </Card>

        <Card title={t('compliance.score')}>
          <div className="text-4xl font-bold text-primary">
            {Math.round(data?.compliance_score.score ?? 0)}%
          </div>
          <p className="text-sm text-gray-500">
            {t('compliance.scoreHint', {
              passed: data?.compliance_score.passed ?? 0,
              failed: data?.compliance_score.failed ?? 0,
            })}
          </p>
        </Card>

        <Card title={t('security.bySeverity')}>
          <ul className="space-y-1 text-sm">
            {(['critical', 'high', 'medium', 'low'] as const).map((sev) => (
              <li key={sev} className="flex items-center justify-between">
                <Badge variant={sev === 'critical' || sev === 'high' ? 'danger' : 'info'}>
                  {sev}
                </Badge>
                <span className="font-mono text-gray-700 dark:text-gray-300">
                  {data?.failures_by_severity?.[sev] ?? 0}
                </span>
              </li>
            ))}
          </ul>
        </Card>
      </div>

      <Card title={`${t('security.findings')} (${findings.length})`}>
        {findings.length ? (
          <ul className="space-y-2">
            {findings.map((finding) => (
              <li
                key={finding.rule_id}
                className="rounded-lg border border-red-200 p-3 text-sm dark:border-red-900"
              >
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-medium">{finding.rule_name}</span>
                  <Badge variant="danger">{finding.severity}</Badge>
                  <Badge variant="info">{finding.category}</Badge>
                  <span className="text-gray-500">
                    {t('security.affectedCIs', { count: finding.affected_cis.length })}
                  </span>
                </div>
                {finding.remediation_hint ? (
                  <p className="mt-1 text-gray-500">{finding.remediation_hint}</p>
                ) : null}
              </li>
            ))}
          </ul>
        ) : (
          <EmptyState title={t('security.noFindings')} description={t('security.noFindingsHint')} />
        )}
      </Card>

      <Card title={t('security.retentionTitle')}>
        <form
          className="flex flex-wrap items-end gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            const days = retentionDays === '' ? undefined : Number.parseInt(retentionDays, 10);
            saveRetention.mutate({ retention_days: days, mode: retentionMode });
          }}
        >
          <label className="flex flex-col gap-1 text-sm">
            <span className="text-gray-600 dark:text-gray-300">{t('security.retentionDays')}</span>
            <input
              type="number"
              min={0}
              className="w-32 rounded-md border border-gray-300 px-2 py-1.5 dark:border-gray-700 dark:bg-gray-900 dark:text-white"
              placeholder={String(retention.data?.retention_days ?? 0)}
              value={retentionDays}
              onChange={(e) => setRetentionDays(e.target.value)}
            />
          </label>
          <label className="flex flex-col gap-1 text-sm">
            <span className="text-gray-600 dark:text-gray-300">{t('security.retentionMode')}</span>
            <select
              className="rounded-md border border-gray-300 bg-white px-2 py-1.5 dark:border-gray-700 dark:bg-gray-900 dark:text-white"
              value={retentionMode}
              onChange={(e) => setRetentionMode(e.target.value as 'anonymize' | 'delete')}
            >
              <option value="anonymize">{t('security.modeAnonymize')}</option>
              <option value="delete">{t('security.modeDelete')}</option>
            </select>
          </label>
          <Button type="submit" loading={saveRetention.isPending}>
            {t('common.save')}
          </Button>
        </form>
        {retention.data ? (
          <p className="mt-2 text-sm text-gray-500">
            {t('security.retentionCurrent', {
              days: retention.data.retention_days,
              mode: t(
                retention.data.mode === 'delete' ? 'security.modeDelete' : 'security.modeAnonymize',
              ),
            })}
          </p>
        ) : (
          <p className="mt-2 text-sm text-gray-500">{t('security.retentionNone')}</p>
        )}
        {saveRetention.isError ? (
          <p className="mt-2 text-sm text-red-600 dark:text-red-400">
            {saveRetention.error instanceof Error
              ? saveRetention.error.message
              : t('security.retentionError')}
          </p>
        ) : null}

        <div className="mt-4 border-t border-gray-200 pt-4 dark:border-gray-800">
          <Button
            variant="secondary"
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
            <p className="mt-2 text-sm text-gray-600 dark:text-gray-300">
              {t('security.erasureResult', {
                contacts: erasure.data.contacts_affected,
                users: erasure.data.users_affected,
              })}
            </p>
          ) : null}
          {erasure.isError ? (
            <p className="mt-2 text-sm text-red-600 dark:text-red-400">
              {erasure.error instanceof Error ? erasure.error.message : t('security.erasureError')}
            </p>
          ) : null}
        </div>
      </Card>

      <Card title={t('security.keyRotationTitle')}>
        <p className="text-sm text-gray-600 dark:text-gray-300">{t('security.keyRotationHint')}</p>
        <div className="mt-3">
          <Button
            variant="secondary"
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
            <p className="mt-2 text-sm text-gray-600 dark:text-gray-300">
              {t('security.keyRotationResult', {
                version: rotateKeys.data.key_version,
                rotated: rotateKeys.data.rotated,
              })}
            </p>
          ) : null}
          {rotateKeys.isError ? (
            <p className="mt-2 text-sm text-red-600 dark:text-red-400">
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
