import { ShieldCheck } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { useEffectivePermissions, usePermissionCatalogue } from '../api/hooks';
import { AdminSection } from '../components/admin/AdminSection';
import { Badge } from '../components/ui/Badge';
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
import { ScrollRegion } from '../components/ScrollRegion';

export function PermissionsPage() {
  const { t } = useTranslation();
  const catalogue = usePermissionCatalogue();
  const effective = useEffectivePermissions();

  const ownPermissions = new Set(effective.data?.permissions ?? []);
  const permissions = catalogue.data ?? [];

  return (
    <div className="space-y-6">
      <PageHeader title={t('permissions.title')} description={t('permissions.subtitle')} />

      {catalogue.isLoading || effective.isLoading ? (
        <SkeletonList rows={6} label={t('permissions.loading')} />
      ) : catalogue.isError ? (
        <ErrorState
          title={t('permissions.errorTitle')}
          description={catalogue.error.message}
          retryLabel={t('common.retry')}
          onRetry={() => catalogue.refetch()}
        />
      ) : permissions.length === 0 ? (
        <EmptyState
          icon={<ShieldCheck />}
          title={t('permissions.emptyTitle')}
          description={t('permissions.emptyHint')}
        />
      ) : (
        <AdminSection
          title={`${t('permissions.catalogue')} (${permissions.length})`}
          description={t('permissions.catalogueHint')}
        >
          <Card className="overflow-hidden p-0">
            <ScrollRegion label={t('permissions.catalogue')}>
              <table className={tableClasses}>
                <thead className={theadClasses}>
                  <tr className="text-fg-muted">
                    <th scope="col" className={thClasses}>
                      {t('permissions.key')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('permissions.resource')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('permissions.action')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('common.description')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('permissions.effective')}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {permissions.map((permission) => (
                    <tr key={permission.key} className={trClasses}>
                      <td className={tdMonoClasses}>{permission.key}</td>
                      <td className={tdClasses}>{permission.resource}</td>
                      <td className={tdClasses}>{permission.action}</td>
                      <td className={tdClasses}>{permission.description}</td>
                      <td className={tdClasses}>
                        {ownPermissions.has(permission.key) ? (
                          <Badge variant="success">{t('permissions.granted')}</Badge>
                        ) : (
                          <Badge variant="neutral">{t('permissions.notGranted')}</Badge>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </ScrollRegion>
          </Card>
        </AdminSection>
      )}
    </div>
  );
}
