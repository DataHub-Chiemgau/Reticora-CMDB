import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useAuditLog, useVerifyAuditChain } from '../api/hooks';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { SkeletonList } from '../components/ui/Skeleton';

/**
 * AuditPage lists the tenant's hash-chained audit trail and lets an
 * administrator re-verify the chain on demand (Epic F4). The verification
 * walks every entry in chain order and reports the first broken link, so a
 * tampered log is visible immediately instead of only in the CLI.
 */
export function AuditPage() {
  const { t } = useTranslation();
  const [offset, setOffset] = useState(0);
  const limit = 50;

  const audit = useAuditLog({ limit, offset });
  const verify = useVerifyAuditChain();

  if (audit.isLoading) {
    return <SkeletonList rows={8} label={t('app.loading')} />;
  }

  if (audit.isError) {
    return (
      <ErrorState
        title={t('audit.errorTitle')}
        description={audit.error instanceof Error ? audit.error.message : undefined}
        retryLabel={t('common.retry')}
        onRetry={() => void audit.refetch()}
      />
    );
  }

  const entries = audit.data?.data ?? [];
  const total = audit.data?.total ?? 0;
  const result = verify.data;

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold text-gray-900 dark:text-white">{t('audit.title')}</h1>
        <p className="text-sm text-gray-600 dark:text-gray-300">{t('audit.subtitle')}</p>
      </div>

      <Card
        title={t('audit.integrityTitle')}
        actions={
          <Button onClick={() => verify.mutate()} disabled={verify.isPending}>
            {verify.isPending ? t('audit.verifying') : t('audit.verifyNow')}
          </Button>
        }
      >
        {result ? (
          <div className="flex flex-wrap items-center gap-3">
            <Badge variant={result.intact ? 'success' : 'danger'}>
              {result.intact ? t('audit.intact') : t('audit.broken')}
            </Badge>
            <span className="text-sm text-gray-600 dark:text-gray-300">
              {t('audit.checkedCount', { count: result.checked })}
            </span>
            {!result.intact && result.broken_reason ? (
              <span className="text-sm text-red-600 dark:text-red-400">
                {t('audit.brokenDetail', {
                  id: result.broken_id,
                  position: result.broken_at,
                  reason: result.broken_reason,
                })}
              </span>
            ) : null}
          </div>
        ) : (
          <p className="text-sm text-gray-600 dark:text-gray-300">{t('audit.verifyHint')}</p>
        )}
        {verify.isError ? (
          <p className="mt-2 text-sm text-red-600 dark:text-red-400">
            {verify.error instanceof Error ? verify.error.message : t('audit.verifyError')}
          </p>
        ) : null}
      </Card>

      <Card title={`${t('audit.entries')} (${total})`}>
        {entries.length === 0 ? (
          <EmptyState title={t('audit.empty')} description={t('audit.emptyHint')} />
        ) : (
          <div className="overflow-x-auto">
            <table className="min-w-full divide-y divide-gray-200 dark:divide-gray-700">
              <thead>
                <tr className="text-left text-sm font-medium text-gray-500 dark:text-gray-400">
                  <th scope="col" className="pb-2">
                    {t('audit.columnTime')}
                  </th>
                  <th scope="col" className="pb-2">
                    {t('audit.columnActor')}
                  </th>
                  <th scope="col" className="pb-2">
                    {t('audit.columnAction')}
                  </th>
                  <th scope="col" className="pb-2">
                    {t('audit.columnEntity')}
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
                {entries.map((entry) => (
                  <tr key={entry.id} className="text-sm text-gray-700 dark:text-gray-300">
                    <td className="py-2 pr-4 whitespace-nowrap">
                      {new Date(entry.created_at).toLocaleString()}
                    </td>
                    <td className="py-2 pr-4">{entry.actor_email ?? entry.actor_id ?? '—'}</td>
                    <td className="py-2 pr-4 font-mono text-xs">{entry.action}</td>
                    <td className="py-2 font-mono text-xs">
                      {entry.entity_type ? `${entry.entity_type}/${entry.entity_id ?? ''}` : '—'}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {total > limit ? (
          <div className="mt-4 flex items-center justify-between">
            <Button
              variant="ghost"
              size="sm"
              disabled={offset === 0}
              onClick={() => setOffset(Math.max(0, offset - limit))}
            >
              {t('common.back')}
            </Button>
            <span className="text-xs text-gray-500 dark:text-gray-400">
              {offset + 1}–{Math.min(offset + limit, total)} / {total}
            </span>
            <Button
              variant="ghost"
              size="sm"
              disabled={offset + limit >= total}
              onClick={() => setOffset(offset + limit)}
            >
              {t('common.next')}
            </Button>
          </div>
        ) : null}
      </Card>
    </div>
  );
}
