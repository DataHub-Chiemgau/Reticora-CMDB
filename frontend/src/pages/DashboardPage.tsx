import { useTranslation } from 'react-i18next';
import { useCIList, useCollectors } from '../api/hooks';
import { Card } from '../components/ui/Card';

export function DashboardPage() {
  const { t } = useTranslation();
  const totalCIs = useCIList({ limit: 1, offset: 0 });
  const activeCIs = useCIList({ limit: 1, offset: 0, status: 'active' });
  const maintenanceCIs = useCIList({ limit: 1, offset: 0, status: 'maintenance' });
  const collectors = useCollectors({ limit: 1000, offset: 0 });

  // Guard against a malformed/legacy payload where `data` is null instead of
  // an array — `.filter` on null throws during render and blanks the page.
  const collectorList = collectors.data?.data ?? [];
  const collectorCount = collectorList.filter((collector) => collector.status === 'online').length;

  const summaries = [
    { label: t('dashboard.totalCIs'), value: totalCIs.data?.total, isLoading: totalCIs.isLoading },
    {
      label: t('dashboard.activeCIs'),
      value: activeCIs.data?.total,
      isLoading: activeCIs.isLoading,
    },
    {
      label: t('dashboard.maintenanceCIs'),
      value: maintenanceCIs.data?.total,
      isLoading: maintenanceCIs.isLoading,
    },
    {
      label: t('dashboard.collectorsOnline'),
      value: collectorCount,
      isLoading: collectors.isLoading,
    },
  ];

  const hasError = totalCIs.error || activeCIs.error || maintenanceCIs.error || collectors.error;

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">
          {t('nav.dashboard')}
        </h2>
        <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">{t('dashboard.summary')}</p>
      </div>

      {hasError ? <p className="text-sm text-red-600 dark:text-red-400">{t('app.error')}</p> : null}

      <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-4">
        {summaries.map((summary) => (
          <Card key={summary.label} title={summary.label}>
            <p className="text-3xl font-semibold text-gray-900 dark:text-gray-100">
              {summary.isLoading ? t('app.loading') : (summary.value ?? 0)}
            </p>
          </Card>
        ))}
      </div>
    </div>
  );
}
