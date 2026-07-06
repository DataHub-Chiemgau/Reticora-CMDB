import { useTranslation } from 'react-i18next';
import { useCollectors } from '../api/hooks';
import type { Collector } from '../api/client';
import { Badge } from '../components/ui/Badge';
import { Card } from '../components/ui/Card';

function formatHeartbeat(value?: string) {
  if (!value) {
    return '—';
  }

  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }

  return new Intl.DateTimeFormat('de-DE', {
    dateStyle: 'medium',
    timeStyle: 'short',
  }).format(date);
}

function getCollectorVariant(status: string): 'success' | 'warning' | 'danger' | 'neutral' | 'info' {
  switch (status) {
    case 'online':
      return 'success';
    case 'offline':
      return 'danger';
    case 'maintenance':
      return 'warning';
    default:
      return 'neutral';
  }
}

function CollectorCard({ collector }: { collector: Collector }) {
  const { t } = useTranslation();

  return (
    <Card
      title={collector.name}
      actions={<Badge variant={getCollectorVariant(collector.status)}>{collector.status}</Badge>}
    >
      <dl className="space-y-2 text-sm">
        <div className="flex justify-between gap-4">
          <dt className="text-gray-500 dark:text-gray-400">{t('discovery.version')}</dt>
          <dd className="font-medium text-gray-900 dark:text-gray-100">{collector.version || '—'}</dd>
        </div>
        <div className="flex justify-between gap-4">
          <dt className="text-gray-500 dark:text-gray-400">{t('discovery.lastHeartbeat')}</dt>
          <dd className="font-medium text-right text-gray-900 dark:text-gray-100">{formatHeartbeat(collector.last_heartbeat)}</dd>
        </div>
        <div className="flex justify-between gap-4">
          <dt className="text-gray-500 dark:text-gray-400">{t('discovery.createdAt')}</dt>
          <dd className="font-medium text-right text-gray-900 dark:text-gray-100">{formatHeartbeat(collector.created_at)}</dd>
        </div>
      </dl>
    </Card>
  );
}

export function DiscoveryPage() {
  const { t } = useTranslation();
  const { data, isLoading, error } = useCollectors({ limit: 100, offset: 0 });

  return (
    <div className="space-y-6">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">{t('nav.discovery')}</h2>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">{t('discovery.summary')}</p>
        </div>
        <span className="text-sm text-gray-500 dark:text-gray-400">
          {data?.total ?? 0} {t('discovery.collectors')}
        </span>
      </div>

      {isLoading ? <p className="text-sm text-gray-600 dark:text-gray-300">{t('app.loading')}</p> : null}
      {error ? <p className="text-sm text-red-600 dark:text-red-400">{t('app.error')}</p> : null}

      {data?.data.length ? (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          {data.data.map((collector) => (
            <CollectorCard key={collector.id} collector={collector} />
          ))}
        </div>
      ) : null}

      {data && data.data.length === 0 ? (
        <Card>
          <p className="text-sm text-gray-500 dark:text-gray-400">{t('common.noResults')}</p>
        </Card>
      ) : null}
    </div>
  );
}
