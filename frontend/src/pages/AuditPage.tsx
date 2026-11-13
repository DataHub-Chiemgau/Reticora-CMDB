import { ChevronLeft, ChevronRight, ScrollText, ShieldCheck } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import type { AuditEntry } from '../api/client';
import { useAuditLog, useVerifyAuditChain } from '../api/hooks';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { PageHeader } from '../components/ui/PageHeader';
import { SkeletonList } from '../components/ui/Skeleton';
import {
  tableClasses,
  tdClasses,
  tdMonoClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';
import { cn } from '../components/ui/utils';
import { ScrollRegion } from '../components/ScrollRegion';

/**
 * AuditPage lists the tenant's hash-chained audit trail and lets an
 * administrator re-verify the chain on demand (Epic F4). The verification
 * walks every entry in chain order and reports the first broken link, so a
 * tampered log is visible immediately instead of only in the CLI.
 */
export function AuditPage() {
  const { t, i18n } = useTranslation();
  const [offset, setOffset] = useState(0);
  const limit = 50;

  const audit = useAuditLog({ limit, offset });
  const verify = useVerifyAuditChain();

  const header = (
    <PageHeader
      title={t('audit.title')}
      description={t('audit.subtitle')}
      actions={
        <Button icon={<ShieldCheck />} onClick={() => verify.mutate()} loading={verify.isPending}>
          {verify.isPending ? t('audit.verifying') : t('audit.verifyNow')}
        </Button>
      }
    />
  );

  if (audit.isLoading) {
    return (
      <div className="space-y-4">
        {header}
        <SkeletonList rows={8} label={t('app.loading')} />
      </div>
    );
  }

  if (audit.isError) {
    return (
      <div className="space-y-4">
        {header}
        <ErrorState
          title={t('audit.errorTitle')}
          description={audit.error instanceof Error ? audit.error.message : undefined}
          retryLabel={t('common.retry')}
          onRetry={() => void audit.refetch()}
        />
      </div>
    );
  }

  const entries = audit.data?.data ?? [];
  const total = audit.data?.total ?? 0;
  const result = verify.data;

  return (
    <div className="space-y-6">
      {header}

      <Card className="px-5 py-4">
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
          <h2 className="text-base font-semibold text-fg">{t('audit.integrityTitle')}</h2>
          {result ? (
            <div className="flex flex-wrap items-center gap-3">
              <Badge variant={result.intact ? 'success' : 'danger'}>
                {result.intact ? t('audit.intact') : t('audit.broken')}
              </Badge>
              <span className="text-sm text-fg-muted">
                {t('audit.checkedCount', { count: result.checked })}
              </span>
              {!result.intact && result.broken_reason ? (
                <span className="text-sm text-danger">
                  {t('audit.brokenDetail', {
                    id: result.broken_id,
                    position: result.broken_at,
                    reason: result.broken_reason,
                  })}
                </span>
              ) : null}
            </div>
          ) : (
            <p className="text-sm text-fg-muted">{t('audit.verifyHint')}</p>
          )}
        </div>
        {verify.isError ? (
          <p className="mt-2 text-sm text-danger">
            {verify.error instanceof Error ? verify.error.message : t('audit.verifyError')}
          </p>
        ) : null}
      </Card>

      <section className="space-y-3" aria-labelledby="audit-entries">
        <h2 id="audit-entries" className="text-base font-semibold text-fg">
          {t('audit.entries')} <span className="tabular-nums text-fg-muted">({total})</span>
        </h2>
        {entries.length === 0 ? (
          <EmptyState
            icon={<ScrollText />}
            title={t('audit.empty')}
            description={t('audit.emptyHint')}
          />
        ) : (
          <Card className="overflow-hidden p-0">
            <ScrollRegion label={t('audit.entries')}>
              <table className={tableClasses}>
                <thead className={theadClasses}>
                  <tr>
                    <th scope="col" className={thClasses}>
                      {t('audit.columnTime')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('audit.columnActor')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('audit.columnAction')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('audit.columnEntity')}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {entries.map((entry) => (
                    <tr key={entry.id} className={trClasses}>
                      <td className={cn(tdClasses, 'whitespace-nowrap py-2 tabular-nums')}>
                        {new Date(entry.timestamp).toLocaleString(i18n.language)}
                      </td>
                      <td className={cn(tdMonoClasses, 'py-2')}>
                        {entry.actor_id ?? entry.actor_type ?? '—'}
                      </td>
                      <td className={cn(tdMonoClasses, 'py-2 font-medium')}>{entry.action}</td>
                      <td className={cn(tdMonoClasses, 'py-2')}>
                        <AuditObject entry={entry} />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </ScrollRegion>
          </Card>
        )}
        {total > limit ? (
          <div className="flex items-center justify-between gap-3 text-sm text-fg-muted">
            <span className="tabular-nums">
              {offset + 1}–{Math.min(offset + limit, total)} / {total}
            </span>
            <div className="flex gap-2">
              <Button
                variant="secondary"
                size="sm"
                icon={<ChevronLeft />}
                disabled={offset === 0}
                onClick={() => setOffset(Math.max(0, offset - limit))}
              >
                {t('common.back')}
              </Button>
              <Button
                variant="secondary"
                size="sm"
                iconAfter={<ChevronRight />}
                disabled={offset + limit >= total}
                onClick={() => setOffset(offset + limit)}
              >
                {t('common.next')}
              </Button>
            </div>
          </div>
        ) : null}
      </section>
    </div>
  );
}

/**
 * AuditObject names the resource an entry changed as resource_type/resource_id
 * and links a CI to its detail page.
 */
function AuditObject({ entry }: { entry: AuditEntry }) {
  if (!entry.resource_type) {
    return <>—</>;
  }
  const label = entry.resource_id
    ? `${entry.resource_type}/${entry.resource_id}`
    : entry.resource_type;
  if (entry.resource_type === 'ci' && entry.resource_id) {
    return (
      <Link
        to={`/cmdb/${encodeURIComponent(entry.resource_id)}`}
        className="rounded text-accent underline-offset-2 hover:underline"
      >
        {label}
      </Link>
    );
  }
  return <>{label}</>;
}
