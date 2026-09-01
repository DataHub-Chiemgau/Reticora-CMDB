import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { securityFindingApi } from '../api/client';
import type { SecurityFinding } from '../api/client';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { ErrorState } from '../components/ui/ErrorState';

function severityVariant(sev: string): 'danger' | 'warning' | 'info' | 'neutral' {
  switch (sev) {
    case 'critical': return 'danger';
    case 'high': return 'danger';
    case 'medium': return 'warning';
    case 'low': return 'info';
    default: return 'neutral';
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
    mutationFn: ({ id, status }: { id: string; status: string }) => securityFindingApi.update(id, status),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['security-findings'] });
      queryClient.invalidateQueries({ queryKey: ['security-findings-summary'] });
    },
  });

  const sev = summary.data?.by_severity ?? {};

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">{t('nav.findings', 'Sicherheitsbefunde')}</h2>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">{t('findings.summary', 'Patch-Posture und Schwachstellen je CI.')}</p>
        </div>
        <Select
          value={statusFilter}
          onChange={(e) => setStatusFilter(e.target.value)}
          aria-label={t('findings.statusFilter', 'Status')}
          className="max-w-xs"
          options={[
            { value: '', label: t('common.all', 'Alle') },
            { value: 'open', label: t('findings.open', 'Offen') },
            { value: 'acknowledged', label: t('findings.acknowledged', 'Bestätigt') },
            { value: 'resolved', label: t('findings.resolved', 'Gelöst') },
            { value: 'false_positive', label: t('findings.falsePositive', 'Fehlalarm') },
          ]}
        />
      </div>

      {summary.data ? (
        <div className="flex flex-wrap gap-3">
          {(['critical', 'high', 'medium', 'low'] as const).map((s) => (
            <Card key={s} className="min-w-28">
              <p className="text-xs uppercase tracking-wide text-gray-500">{s}</p>
              <p className="text-2xl font-bold">{sev[s] ?? 0}</p>
            </Card>
          ))}
        </div>
      ) : null}

      {isLoading ? <SkeletonList rows={4} label={t('app.loading')} /> : null}
      {error ? <ErrorState title={t('app.error')} retryLabel={t('common.retry')} onRetry={() => void refetch()} /> : null}

      <div className="space-y-3">
        {(data?.data ?? []).map((f: SecurityFinding) => (
          <Card key={f.id} title={f.title} actions={<Badge variant={severityVariant(f.severity)}>{f.severity}</Badge>}>
            <p className="text-sm text-gray-600 dark:text-gray-300">
              {f.package_name ? `${f.package_name} ${f.installed_version || ''}` : ''}
              {f.fixed_version ? ` → ${f.fixed_version}` : ''}
              {f.reference ? ` · ${f.reference}` : ''}
            </p>
            <div className="mt-3 flex flex-wrap gap-2">
              {f.status === 'open' ? (
                <>
                  <Button size="sm" variant="secondary" onClick={() => updateMutation.mutate({ id: f.id, status: 'acknowledged' })}>{t('findings.acknowledge', 'Bestätigen')}</Button>
                  <Button size="sm" onClick={() => updateMutation.mutate({ id: f.id, status: 'resolved' })}>{t('findings.resolve', 'Lösen')}</Button>
                  <Button size="sm" variant="ghost" onClick={() => updateMutation.mutate({ id: f.id, status: 'false_positive' })}>{t('findings.markFalsePositive', 'Fehlalarm')}</Button>
                </>
              ) : null}
            </div>
          </Card>
        ))}
      </div>
    </div>
  );
}
