import { useTranslation } from 'react-i18next';
import { useEffectivePermissions, usePermissionCatalogue } from '../api/hooks';
import { Badge } from '../components/ui/Badge';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { SkeletonList } from '../components/ui/Skeleton';

export function PermissionsPage() {
  const { t } = useTranslation();
  const catalogue = usePermissionCatalogue();
  const effective = useEffectivePermissions();

  if (catalogue.isLoading || effective.isLoading) {
    return <SkeletonList rows={6} label={t('permissions.loading')} />;
  }

  if (catalogue.isError) {
    return (
      <ErrorState
        title={t('permissions.errorTitle')}
        description={catalogue.error.message}
        retryLabel={t('common.retry')}
        onRetry={() => catalogue.refetch()}
      />
    );
  }

  const ownPermissions = new Set(effective.data?.permissions ?? []);
  const permissions = catalogue.data ?? [];

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold text-gray-900 dark:text-white">
          {t('permissions.title')}
        </h1>
        <p className="text-sm text-gray-600 dark:text-gray-300">{t('permissions.subtitle')}</p>
      </div>

      {permissions.length === 0 ? (
        <EmptyState title={t('permissions.emptyTitle')} description={t('permissions.emptyHint')} />
      ) : (
        <Card title={`${t('permissions.catalogue')} (${permissions.length})`}>
          <div className="overflow-x-auto">
            <table className="min-w-full divide-y divide-gray-200 dark:divide-gray-700">
              <thead>
                <tr className="text-left text-sm font-medium text-gray-500 dark:text-gray-400">
                  <th className="pb-2">{t('permissions.key')}</th>
                  <th className="pb-2">{t('permissions.resource')}</th>
                  <th className="pb-2">{t('permissions.action')}</th>
                  <th className="pb-2">{t('common.description')}</th>
                  <th className="pb-2">{t('permissions.effective')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
                {permissions.map((permission) => (
                  <tr key={permission.key} className="text-sm text-gray-700 dark:text-gray-300">
                    <td className="py-2 font-mono text-xs">{permission.key}</td>
                    <td className="py-2">{permission.resource}</td>
                    <td className="py-2">{permission.action}</td>
                    <td className="py-2">{permission.description}</td>
                    <td className="py-2">
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
          </div>
        </Card>
      )}
    </div>
  );
}
