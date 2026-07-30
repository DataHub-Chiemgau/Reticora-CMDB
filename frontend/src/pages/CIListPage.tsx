import { useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { ciApi } from '../api/client';
import { useCIList } from '../api/hooks';
import { Button } from '../components/ui/Button';
import { Badge } from '../components/ui/Badge';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { useCIFilterStore } from '../stores/ciFilter';
import { getStatusBadgeVariant, getStatusTranslationKey } from './ciStatus';

interface CIListPageProps {
  onCreateCI: () => void;
}

export function CIListPage({ onCreateCI }: CIListPageProps) {
  const { t } = useTranslation();
  const { search, status, ciTypeId, setSearch, setStatus } = useCIFilterStore();
  const navigate = useNavigate();
  const [offset, setOffset] = useState(0);
  const limit = 25;

  const { data, isLoading, error, refetch } = useCIList({
    search,
    status,
    ci_type_id: ciTypeId,
    limit,
    offset,
  });

  return (
    <div className="space-y-4">
      <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
        <div>
          <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">{t('nav.cmdb')}</h2>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">{t('ci.title')}</p>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button onClick={onCreateCI}>{t('form.createCI')}</Button>
          <Button asChild variant="secondary">
            <a href={ciApi.exportCIs('csv')}>{t('ci.exportCsv')}</a>
          </Button>
          <Button asChild variant="secondary">
            <a href={ciApi.exportCIs('json')}>{t('ci.exportJson')}</a>
          </Button>
        </div>
      </div>

      <Card>
        <div className="flex flex-wrap gap-3">
          <Input
            type="text"
            placeholder={t('common.search')}
            value={search}
            onChange={(event) => {
              setSearch(event.target.value);
              setOffset(0);
            }}
            className="max-w-sm"
            aria-label={t('accessibility.searchCIs')}
          />
          <Select
            value={status}
            onChange={(event) => {
              setStatus(event.target.value);
              setOffset(0);
            }}
            options={[
              { value: '', label: t('ci.allStatus') },
              { value: 'active', label: t('ci.statusActive') },
              { value: 'inactive', label: t('ci.statusInactive') },
              { value: 'maintenance', label: t('ci.statusMaintenance') },
              { value: 'decommissioned', label: t('ci.statusDecommissioned') },
            ]}
            className="max-w-xs"
            aria-label={t('accessibility.filterCIStatus')}
          />
        </div>
      </Card>

      {isLoading ? <SkeletonList rows={5} label={t('app.loading')} /> : null}
      {error ? (
        <ErrorState
          title={t('ci.listLoadError')}
          description={error instanceof Error ? error.message : undefined}
          retryLabel={t('common.retry')}
          onRetry={() => void refetch()}
        />
      ) : null}

      {data && data.data.length === 0 ? (
        <EmptyState
          title={t('ci.emptyTitle')}
          description={t('ci.emptyHint')}
          action={<Button onClick={onCreateCI}>{t('form.createCI')}</Button>}
        />
      ) : null}

      {data && data.data.length > 0 ? (
        <Card className="p-0 overflow-hidden">
          <div className="overflow-x-auto">
            <table className="w-full text-left text-sm">
              <thead className="border-b bg-gray-50 dark:border-gray-800 dark:bg-gray-950">
                <tr>
                  <th className="px-4 py-3">{t('ci.name')}</th>
                  <th className="px-4 py-3">{t('ci.status')}</th>
                  <th className="px-4 py-3">{t('ci.manufacturer')}</th>
                  <th className="px-4 py-3">{t('ci.model')}</th>
                  <th className="px-4 py-3">{t('ci.managementIp')}</th>
                  <th className="px-4 py-3">{t('ci.source')}</th>
                </tr>
              </thead>
              <tbody>
                {data.data.map((ci) => (
                  <tr
                    key={ci.id}
                    onClick={() => navigate(`/cmdb/${ci.id}`)}
                    className="cursor-pointer border-b border-gray-100 transition hover:bg-gray-50 dark:border-gray-800 dark:hover:bg-gray-950"
                  >
                    <td className="px-4 py-3 font-medium text-gray-900 dark:text-gray-100">
                      <Link
                        to={`/cmdb/${ci.id}`}
                        onClick={(event) => event.stopPropagation()}
                        className="underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40"
                      >
                        {ci.name}
                      </Link>
                    </td>
                    <td className="px-4 py-3">
                      <Badge variant={getStatusBadgeVariant(ci.status)}>
                        {t(getStatusTranslationKey(ci.status))}
                      </Badge>
                    </td>
                    <td className="px-4 py-3">{ci.manufacturer || '—'}</td>
                    <td className="px-4 py-3">{ci.model || '—'}</td>
                    <td className="px-4 py-3 font-mono text-xs">{ci.management_ip || '—'}</td>
                    <td className="px-4 py-3">{ci.discovery_source || '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      ) : null}

      {data ? (
        <div className="flex items-center justify-between text-sm text-gray-600 dark:text-gray-300">
          <span>
            {data.total} {t('common.entries')}
          </span>
          <div className="flex gap-2">
            <Button
              variant="secondary"
              disabled={offset === 0}
              onClick={() => setOffset(Math.max(0, offset - limit))}
            >
              ← {t('common.back')}
            </Button>
            <Button
              variant="secondary"
              disabled={!data.has_more}
              onClick={() => setOffset(offset + limit)}
            >
              {t('common.next')} →
            </Button>
          </div>
        </div>
      ) : null}
    </div>
  );
}
