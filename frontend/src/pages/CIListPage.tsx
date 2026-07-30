import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { CI } from '../api/client';
import { ciApi } from '../api/client';
import { useCIList } from '../api/hooks';
import { Button } from '../components/ui/Button';
import { Badge } from '../components/ui/Badge';
import { Card } from '../components/ui/Card';
import { Input } from '../components/ui/Input';
import { Select } from '../components/ui/Select';
import { CIFormModal } from './CIFormModal';
import { useCIFilterStore } from '../stores/ciFilter';

function getStatusBadgeVariant(status: string): 'success' | 'warning' | 'danger' | 'neutral' | 'info' {
  switch (status) {
    case 'active':
      return 'success';
    case 'maintenance':
      return 'warning';
    case 'decommissioned':
      return 'danger';
    case 'inactive':
      return 'neutral';
    default:
      return 'info';
  }
}

function getStatusTranslationKey(status: string) {
  switch (status) {
    case 'active':
      return 'ci.statusActive';
    case 'inactive':
      return 'ci.statusInactive';
    case 'maintenance':
      return 'ci.statusMaintenance';
    case 'decommissioned':
      return 'ci.statusDecommissioned';
    default:
      return 'ci.status';
  }
}

interface CIListPageProps {
  onCreateCI: () => void;
}

export function CIListPage({ onCreateCI }: CIListPageProps) {
  const { t } = useTranslation();
  const { search, status, ciTypeId, setSearch, setStatus } = useCIFilterStore();
  const [offset, setOffset] = useState(0);
  const [selectedCI, setSelectedCI] = useState<CI | null>(null);
  const [isEditOpen, setIsEditOpen] = useState(false);
  const limit = 25;

  const { data, isLoading, error } = useCIList({
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

      {isLoading ? <p className="text-sm text-gray-600 dark:text-gray-300">{t('app.loading')}</p> : null}
      {error ? <p className="text-sm text-red-600 dark:text-red-400">{t('app.error')}</p> : null}

      {data ? (
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
                {data.data.length === 0 ? (
                  <tr>
                    <td colSpan={6} className="px-4 py-8 text-center text-gray-500 dark:text-gray-400">
                      {t('common.noResults')}
                    </td>
                  </tr>
                ) : (
                  data.data.map((ci) => (
                    <tr
                      key={ci.id}
                      onClick={() => setSelectedCI(ci)}
                      className="cursor-pointer border-b border-gray-100 transition hover:bg-gray-50 dark:border-gray-800 dark:hover:bg-gray-950"
                    >
                      <td className="px-4 py-3 font-medium text-gray-900 dark:text-gray-100">{ci.name}</td>
                      <td className="px-4 py-3">
                        <Badge variant={getStatusBadgeVariant(ci.status)}>{t(getStatusTranslationKey(ci.status))}</Badge>
                      </td>
                      <td className="px-4 py-3">{ci.manufacturer || '—'}</td>
                      <td className="px-4 py-3">{ci.model || '—'}</td>
                      <td className="px-4 py-3 font-mono text-xs">{ci.management_ip || '—'}</td>
                      <td className="px-4 py-3">{ci.discovery_source || '—'}</td>
                    </tr>
                  ))
                )}
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

      {selectedCI ? (
        <CIDetailPanel
          ci={selectedCI}
          onClose={() => setSelectedCI(null)}
          onEdit={() => setIsEditOpen(true)}
        />
      ) : null}

      <CIFormModal
        open={isEditOpen}
        onOpenChange={setIsEditOpen}
        ci={selectedCI}
        onSuccess={(updatedCI) => setSelectedCI(updatedCI)}
      />
    </div>
  );
}

function CIDetailPanel({ ci, onClose, onEdit }: { ci: CI; onClose: () => void; onEdit: () => void }) {
  const { t } = useTranslation();

  return (
    <aside className="fixed inset-y-0 right-0 z-30 w-full max-w-md overflow-y-auto border-l border-gray-200 bg-white p-6 shadow-xl dark:border-gray-800 dark:bg-gray-900">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h3 className="text-lg font-bold text-gray-900 dark:text-gray-100">{ci.name}</h3>
          <div className="mt-2">
            <Badge variant={getStatusBadgeVariant(ci.status)}>{t(getStatusTranslationKey(ci.status))}</Badge>
          </div>
        </div>
        <div className="flex gap-2">
          <Button variant="secondary" size="sm" onClick={onEdit}>
            {t('common.edit')}
          </Button>
          <Button variant="ghost" size="sm" onClick={onClose} aria-label={t('accessibility.closePanel')}>
            ✕
          </Button>
        </div>
      </div>
      <dl className="mt-6 space-y-3 text-sm">
        <DetailRow label="ID" value={ci.id} />
        <DetailRow label={t('ci.status')} value={t(getStatusTranslationKey(ci.status))} />
        <DetailRow label={t('form.fields.ciType')} value={ci.ci_type_id} />
        <DetailRow label={t('ci.manufacturer')} value={ci.manufacturer} />
        <DetailRow label={t('ci.model')} value={ci.model} />
        <DetailRow label={t('ci.serialNumber')} value={ci.serial_number} />
        <DetailRow label={t('ci.managementIp')} value={ci.management_ip} />
        <DetailRow label={t('ci.firmware')} value={ci.firmware_version} />
        <DetailRow label={t('ci.source')} value={ci.discovery_source} />
        <DetailRow label={t('ci.lastSeen')} value={ci.last_seen_at} />
        <DetailRow label={t('ci.created')} value={ci.created_at} />
        <DetailRow label={t('ci.updated')} value={ci.updated_at} />
      </dl>
      {Object.keys(ci.attributes).length > 0 ? (
        <Card className="mt-6 p-4" title={t('ci.attributes')}>
          <pre className="overflow-x-auto rounded-lg bg-gray-100 p-3 text-xs dark:bg-gray-950">
            {JSON.stringify(ci.attributes, null, 2)}
          </pre>
        </Card>
      ) : null}
    </aside>
  );
}

function DetailRow({ label, value }: { label: string; value?: string }) {
  if (!value) {
    return null;
  }

  return (
    <div>
      <dt className="text-gray-500 dark:text-gray-400">{label}</dt>
      <dd className="font-medium text-gray-900 dark:text-gray-100">{value}</dd>
    </div>
  );
}
