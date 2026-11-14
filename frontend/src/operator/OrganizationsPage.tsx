import { useMemo } from 'react';
import { Link } from 'react-router';
import { useTranslation } from 'react-i18next';
import { useInfiniteQuery, useQueries } from '@tanstack/react-query';
import { Button } from '../components/ui/Button';
import { ErrorState } from '../components/ui/ErrorState';
import { PageHeader } from '../components/ui/PageHeader';
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
import { operatorApi } from './operatorClient';
import type { OrgPage, OrgSummary } from './operatorClient';
import type { QuotaUsage } from '../api/client';

/**
 * OrganizationsPage lists the organizations for the operator (PKG-07,
 * WP-888): name, slug, plan and creation date from GET /api/v1/admin/orgs
 * (cursor, 50 per page) and the use of max_cis from the usage report of each
 * row of the newest page loaded (one page at a time).
 */
export function OrganizationsPage() {
  const { t, i18n } = useTranslation();
  const formatDate = useMemo(
    () => new Intl.DateTimeFormat(i18n.language, { dateStyle: 'medium' }),
    [i18n.language],
  );
  const orgs = useInfiniteQuery<OrgPage, Error, { pages: OrgPage[] }, string[], string | undefined>(
    {
      queryKey: ['operator', 'orgs'],
      queryFn: ({ pageParam }) => operatorApi.orgs(pageParam),
      initialPageParam: undefined as string | undefined,
      getNextPageParam: (last) => (last.has_more ? last.next_cursor : undefined),
    },
  );
  const rows: OrgSummary[] = orgs.data?.pages.flatMap((p) => p.data) ?? [];
  const lastPage: OrgSummary[] = orgs.data?.pages[orgs.data.pages.length - 1]?.data ?? [];
  const usage = useQueries({
    queries: lastPage.map((org) => ({
      queryKey: ['operator', 'usage', org.id],
      queryFn: () => operatorApi.usage(org.id),
      staleTime: 60_000,
    })),
  });
  const usageOf = new Map<string, QuotaUsage | undefined>(
    lastPage.map((org, i) => [org.id, usage[i]?.data?.data.find((u) => u.quota === 'max_cis')]),
  );

  if (orgs.isError) {
    return (
      <div className="space-y-6">
        <PageHeader title={t('operator.orgs.title')} />
        <ErrorState title={t('operator.orgs.loadError')} />
      </div>
    );
  }
  return (
    <div className="space-y-6">
      <PageHeader title={t('operator.orgs.title')} description={t('operator.orgs.description')} />
      {orgs.isPending ? (
        <SkeletonList />
      ) : (
        <ScrollRegion label={t('operator.orgs.title')}>
          <table className={tableClasses}>
            <thead className={theadClasses}>
              <tr>
                <th scope="col" className={thClasses}>
                  {t('operator.orgs.name')}
                </th>
                <th scope="col" className={thClasses}>
                  {t('operator.orgs.slug')}
                </th>
                <th scope="col" className={thClasses}>
                  {t('operator.orgs.plan')}
                </th>
                <th scope="col" className={thClasses}>
                  {t('operator.orgs.cis')}
                </th>
                <th scope="col" className={thClasses}>
                  {t('operator.orgs.createdAt')}
                </th>
              </tr>
            </thead>
            <tbody>
              {rows.map((org) => {
                const cis = usageOf.get(org.id);
                return (
                  <tr key={org.id} className={trClasses}>
                    <td className={tdPrimaryClasses}>
                      <Link to={`/operator/orgs/${org.id}`} className="text-accent hover:underline">
                        {org.name}
                      </Link>
                    </td>
                    <td className={tdClasses}>{org.slug}</td>
                    <td className={tdClasses}>
                      {t(`discoverability.plans.${org.plan}`, { defaultValue: org.plan })}
                    </td>
                    <td className={tdClasses}>
                      {cis && cis.used !== null
                        ? t('planPage.quotas.usedOf', {
                            used: cis.used,
                            limit: cis.limit ?? t('planPage.quotas.unlimited'),
                          })
                        : ''}
                    </td>
                    <td className={tdClasses}>{formatDate.format(new Date(org.created_at))}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </ScrollRegion>
      )}
      {orgs.hasNextPage && (
        <Button
          variant="secondary"
          onClick={() => void orgs.fetchNextPage()}
          disabled={orgs.isFetchingNextPage}
        >
          {t('operator.orgs.more')}
        </Button>
      )}
    </div>
  );
}
