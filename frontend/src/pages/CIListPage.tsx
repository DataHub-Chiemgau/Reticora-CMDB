import { useTranslation } from 'react-i18next';
import { useCIList } from '../api/hooks';
import { useCIFilterStore } from '../stores/ciFilter';
import { useState } from 'react';
import type { CI } from '../api/client';
import { ciApi } from '../api/client';

const STATUS_COLORS: Record<string, string> = {
  active: 'bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-200',
  inactive: 'bg-gray-100 text-gray-800 dark:bg-gray-700 dark:text-gray-200',
  maintenance: 'bg-yellow-100 text-yellow-800 dark:bg-yellow-900 dark:text-yellow-200',
  decommissioned: 'bg-red-100 text-red-800 dark:bg-red-900 dark:text-red-200',
};

export function CIListPage() {
  const { t } = useTranslation();
  const { search, status, ciTypeId, setSearch, setStatus } = useCIFilterStore();
  const [offset, setOffset] = useState(0);
  const limit = 25;

  const { data, isLoading, error } = useCIList({
    search,
    status,
    ci_type_id: ciTypeId,
    limit,
    offset,
  });

  const [selectedCI, setSelectedCI] = useState<CI | null>(null);

  return (
    <div className="space-y-4">
      {/* Header */}
      <div className="flex items-center justify-between">
        <h2 className="text-2xl font-bold">{t('nav.cmdb')}</h2>
        <div className="flex gap-2">
          <a
            href={ciApi.exportCIs('csv')}
            className="rounded bg-gray-200 px-3 py-1.5 text-sm hover:bg-gray-300 dark:bg-gray-700 dark:hover:bg-gray-600"
          >
            Export CSV
          </a>
          <a
            href={ciApi.exportCIs('json')}
            className="rounded bg-gray-200 px-3 py-1.5 text-sm hover:bg-gray-300 dark:bg-gray-700 dark:hover:bg-gray-600"
          >
            Export JSON
          </a>
        </div>
      </div>

      {/* Filters */}
      <div className="flex flex-wrap gap-3">
        <input
          type="text"
          placeholder={t('common.search')}
          value={search}
          onChange={(e) => { setSearch(e.target.value); setOffset(0); }}
          className="rounded border px-3 py-1.5 dark:border-gray-600 dark:bg-gray-800"
        />
        <select
          value={status}
          onChange={(e) => { setStatus(e.target.value); setOffset(0); }}
          className="rounded border px-3 py-1.5 dark:border-gray-600 dark:bg-gray-800"
        >
          <option value="">{t('ci.allStatus')}</option>
          <option value="active">{t('ci.statusActive')}</option>
          <option value="inactive">{t('ci.statusInactive')}</option>
          <option value="maintenance">{t('ci.statusMaintenance')}</option>
          <option value="decommissioned">{t('ci.statusDecommissioned')}</option>
        </select>
      </div>

      {/* Table */}
      {isLoading && <p>{t('app.loading')}</p>}
      {error && <p className="text-red-500">{t('app.error')}</p>}
      {data && (
        <>
          <div className="overflow-x-auto rounded-lg border dark:border-gray-700">
            <table className="w-full text-left text-sm">
              <thead className="border-b bg-gray-50 dark:border-gray-700 dark:bg-gray-800">
                <tr>
                  <th className="px-4 py-3">Name</th>
                  <th className="px-4 py-3">Status</th>
                  <th className="px-4 py-3">Hersteller</th>
                  <th className="px-4 py-3">Modell</th>
                  <th className="px-4 py-3">Management-IP</th>
                  <th className="px-4 py-3">Quelle</th>
                </tr>
              </thead>
              <tbody>
                {data.data.length === 0 ? (
                  <tr>
                    <td colSpan={6} className="px-4 py-8 text-center text-gray-500">
                      {t('common.noResults')}
                    </td>
                  </tr>
                ) : (
                  data.data.map((ci) => (
                    <tr
                      key={ci.id}
                      onClick={() => setSelectedCI(ci)}
                      className="cursor-pointer border-b hover:bg-gray-50 dark:border-gray-700 dark:hover:bg-gray-800"
                    >
                      <td className="px-4 py-3 font-medium">{ci.name}</td>
                      <td className="px-4 py-3">
                        <span className={`rounded-full px-2 py-0.5 text-xs font-medium ${STATUS_COLORS[ci.status] ?? ''}`}>
                          {ci.status}
                        </span>
                      </td>
                      <td className="px-4 py-3">{ci.manufacturer}</td>
                      <td className="px-4 py-3">{ci.model}</td>
                      <td className="px-4 py-3 font-mono text-xs">{ci.management_ip}</td>
                      <td className="px-4 py-3">{ci.source}</td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </div>

          {/* Pagination */}
          <div className="flex items-center justify-between text-sm">
            <span>
              {data.total} {t('common.entries')}
            </span>
            <div className="flex gap-2">
              <button
                disabled={offset === 0}
                onClick={() => setOffset(Math.max(0, offset - limit))}
                className="rounded border px-3 py-1 disabled:opacity-50 dark:border-gray-600"
              >
                ← {t('common.back')}
              </button>
              <button
                disabled={!data.has_more}
                onClick={() => setOffset(offset + limit)}
                className="rounded border px-3 py-1 disabled:opacity-50 dark:border-gray-600"
              >
                {t('common.next')} →
              </button>
            </div>
          </div>
        </>
      )}

      {/* Detail Panel */}
      {selectedCI && (
        <CIDetailPanel ci={selectedCI} onClose={() => setSelectedCI(null)} />
      )}
    </div>
  );
}

function CIDetailPanel({ ci, onClose }: { ci: CI; onClose: () => void }) {
  return (
    <div className="fixed inset-y-0 right-0 w-96 overflow-y-auto border-l bg-white p-6 shadow-xl dark:border-gray-700 dark:bg-gray-900">
      <div className="flex items-center justify-between">
        <h3 className="text-lg font-bold">{ci.name}</h3>
        <button onClick={onClose} className="text-gray-500 hover:text-gray-700">✕</button>
      </div>
      <dl className="mt-4 space-y-3 text-sm">
        <DetailRow label="ID" value={ci.id} />
        <DetailRow label="Status" value={ci.status} />
        <DetailRow label="Typ-ID" value={ci.ci_type_id} />
        <DetailRow label="Hersteller" value={ci.manufacturer} />
        <DetailRow label="Modell" value={ci.model} />
        <DetailRow label="Seriennummer" value={ci.serial_number} />
        <DetailRow label="Management-IP" value={ci.management_ip} />
        <DetailRow label="Firmware" value={ci.firmware_version} />
        <DetailRow label="Quelle" value={ci.source} />
        <DetailRow label="Zuletzt gesehen" value={ci.last_seen} />
        <DetailRow label="Erstellt" value={ci.created_at} />
        <DetailRow label="Aktualisiert" value={ci.updated_at} />
      </dl>
      {Object.keys(ci.attributes).length > 0 && (
        <div className="mt-4">
          <h4 className="font-medium">Attribute</h4>
          <pre className="mt-1 rounded bg-gray-100 p-2 text-xs dark:bg-gray-800">
            {JSON.stringify(ci.attributes, null, 2)}
          </pre>
        </div>
      )}
    </div>
  );
}

function DetailRow({ label, value }: { label: string; value?: string }) {
  if (!value) return null;
  return (
    <div>
      <dt className="text-gray-500 dark:text-gray-400">{label}</dt>
      <dd className="font-medium">{value}</dd>
    </div>
  );
}
