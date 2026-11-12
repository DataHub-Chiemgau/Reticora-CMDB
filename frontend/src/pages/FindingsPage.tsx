import { ShieldAlert } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { securityFindingApi } from '../api/client';
import type { SecurityFinding } from '../api/client';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { MetricCard } from '../components/ui/MetricCard';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import { Toolbar, ToolbarEnd } from '../components/ui/Toolbar';
import { ScrollRegion } from '../components/ScrollRegion';
import {
  tableClasses,
  tdClasses,
  tdMonoClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';
import { SkeletonList } from '../components/ui/Skeleton';
import { ErrorState } from '../components/ui/ErrorState';

function severityVariant(sev: string): 'danger' | 'warning' | 'info' | 'neutral' {
  switch (sev) {
    case 'critical':
      return 'danger';
    case 'high':
      return 'danger';
    case 'medium':
      return 'warning';
    case 'low':
      return 'info';
    default:
      return 'neutral';
  }
}

const severityTone = {
  critical: 'danger',
  high: 'warning',
  medium: 'default',
  low: 'default',
} as const;

const statusKeys: Record<string, string> = {
  open: 'findings.open',
  acknowledged: 'findings.acknowledged',
  resolved: 'findings.resolved',
  false_positive: 'findings.falsePositive',
};

function statusVariant(status: string): 'warning' | 'info' | 'success' | 'neutral' {
  switch (status) {
    case 'open':
      return 'warning';
    case 'acknowledged':
      return 'info';
    case 'resolved':
      return 'success';
    default:
      return 'neutral';
  }
}

export function FindingsPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [statusFilter, setStatusFilter] = useState('open');

  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['security-findings', statusFilter],
    queryFn: () => securityFindingApi.list({ status: statusFilter || undefined, limit: 100 }),
  });
  const summary = useQuery({
    queryKey: ['security-findings-summary'],
    queryFn: () => securityFindingApi.summary(),
  });

  const updateMutation = useMutation({
    mutationFn: ({ id, status }: { id: string; status: string }) =>
      securityFindingApi.update(id, status),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['security-findings'] });
      queryClient.invalidateQueries({ queryKey: ['security-findings-summary'] });
    },
  });

  const sev = summary.data?.by_severity ?? {};

  const findings = data?.data ?? [];

  return (
    <div className="space-y-4">
      <PageHeader
        title={t('nav.findings', 'Sicherheitsbefunde')}
        description={t('findings.summary', 'Patch-Posture und Schwachstellen je CI.')}
      />

      {summary.data ? (
        <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
          {(['critical', 'high', 'medium', 'low'] as const).map((s) => (
            <MetricCard
              key={s}
              label={t(`findings.severities.${s}`, s)}
              value={sev[s] ?? 0}
              tone={(sev[s] ?? 0) > 0 ? severityTone[s] : 'default'}
            />
          ))}
        </div>
      ) : null}

      <Toolbar>
        <Select
          value={statusFilter}
          onChange={(e) => setStatusFilter(e.target.value)}
          aria-label={t('findings.statusFilter', 'Status')}
          className="w-48"
          options={[
            { value: '', label: t('common.all', 'Alle') },
            { value: 'open', label: t('findings.open', 'Offen') },
            { value: 'acknowledged', label: t('findings.acknowledged', 'Bestätigt') },
            { value: 'resolved', label: t('findings.resolved', 'Gelöst') },
            { value: 'false_positive', label: t('findings.falsePositive', 'Fehlalarm') },
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
          title={t('app.error')}
          retryLabel={t('common.retry')}
          onRetry={() => void refetch()}
        />
      ) : null}
      {data && findings.length === 0 ? (
        <EmptyState
          icon={<ShieldAlert />}
          title={t('findings.emptyTitle')}
          description={t('findings.emptyHint')}
        />
      ) : null}

      {findings.length > 0 ? (
        <Card className="overflow-hidden p-0">
          <ScrollRegion label={t('nav.findings', 'Sicherheitsbefunde')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('ticket.ticketTitle')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('compliance.severity')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('findings.statusFilter', 'Status')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('findings.package')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('findings.fixedVersion')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('findings.reference')}
                  </th>
                  <th scope="col" className={`${thClasses} text-right`}>
                    {t('common.actions')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {findings.map((f: SecurityFinding) => (
                  <tr key={f.id} className={trClasses}>
                    <td className={tdPrimaryClasses}>{f.title}</td>
                    <td className={tdClasses}>
                      <Badge variant={severityVariant(f.severity)}>
                        {t(`findings.severities.${f.severity}`, f.severity)}
                      </Badge>
                    </td>
                    <td className={tdClasses}>
                      <Badge variant={statusVariant(f.status)}>
                        {statusKeys[f.status] ? t(statusKeys[f.status]!) : f.status}
                      </Badge>
                    </td>
                    <td className={`${tdMonoClasses} whitespace-nowrap`}>
                      {f.package_name ? `${f.package_name} ${f.installed_version || ''}` : '—'}
                    </td>
                    <td className={`${tdMonoClasses} whitespace-nowrap`}>
                      {f.fixed_version || '—'}
                    </td>
                    <td className={tdMonoClasses}>{f.reference || '—'}</td>
                    <td className={tdClasses}>
                      {f.status === 'open' ? (
                        <div className="flex items-center justify-end gap-1.5">
                          <Button
                            size="sm"
                            variant="secondary"
                            onClick={() =>
                              updateMutation.mutate({ id: f.id, status: 'acknowledged' })
                            }
                          >
                            {t('findings.acknowledge', 'Bestätigen')}
                          </Button>
                          <Button
                            size="sm"
                            variant="secondary"
                            onClick={() => updateMutation.mutate({ id: f.id, status: 'resolved' })}
                          >
                            {t('findings.resolve', 'Lösen')}
                          </Button>
                          {/* "Fehlalarm" needs a status_reason (WP-1116); the action
                              returns with the reason dialog of WP-1134. */}
                        </div>
                      ) : null}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </ScrollRegion>
        </Card>
      ) : null}
    </div>
  );
}
