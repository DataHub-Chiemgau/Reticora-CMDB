/**
 * ScopeSwitcher — the scope control of the layout (UI-01/UI-02): shows the
 * organization of the session and lets the user narrow lists to one client
 * and one of its sites. Only clients and sites the server returns are
 * offered, so the choice never exceeds the user's access (row-level
 * security); a stored selection that is no longer accessible is dropped.
 */
import { useEffect } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { fetchAPI, siteApi } from '../api/client';
import { clientApi } from '../api/cmdb';
import type { components } from '../api/generated/schema';
import { useScopeStore } from '../stores/scope';

type Organization = components['schemas']['Organization'];

const selectClass =
  'rounded-lg border border-gray-300 bg-white px-2 py-1 text-sm text-gray-900 dark:border-gray-700 dark:bg-gray-900 dark:text-gray-100';

export function ScopeSwitcher() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { clientId, siteId, setClient, setSite } = useScopeStore();

  const org = useQuery({
    queryKey: ['scope', 'org'],
    queryFn: () => fetchAPI<Organization>('/org'),
    retry: false,
    staleTime: 5 * 60_000,
  });
  const clients = useQuery({
    queryKey: ['scope', 'clients'],
    queryFn: () => clientApi.list(),
    retry: false,
    staleTime: 60_000,
  });
  const sites = useQuery({
    queryKey: ['scope', 'sites', clientId],
    queryFn: () => siteApi.list({ limit: 200, ...(clientId ? { client_id: clientId } : {}) }),
    retry: false,
    staleTime: 60_000,
  });

  const clientOptions = clients.data?.data ?? [];
  const siteOptions = sites.data?.data ?? [];

  // Drop a stored selection the user can no longer access.
  useEffect(() => {
    if (clientId && clients.data && !clients.data.data.some((c) => c.id === clientId)) {
      setClient('');
    }
  }, [clientId, clients.data, setClient]);
  useEffect(() => {
    if (siteId && sites.data && !sites.data.data.some((s) => s.id === siteId)) {
      setSite('');
    }
  }, [siteId, sites.data, setSite]);

  // Lists read the scope when they fetch: refetch them on a change.
  function refresh() {
    void queryClient.invalidateQueries({
      predicate: (query) => query.queryKey[0] !== 'scope',
    });
  }

  return (
    <div
      role="group"
      aria-label={t('scope.label', 'Arbeitsbereich')}
      className="flex flex-wrap items-center gap-2 text-sm"
    >
      <span
        className="max-w-40 truncate font-medium"
        title={t('scope.organization', 'Organisation')}
      >
        {org.data?.name ?? t('scope.organization', 'Organisation')}
      </span>
      <select
        aria-label={t('scope.client', 'Kunde')}
        className={selectClass}
        value={clientId}
        disabled={!clients.isSuccess}
        onChange={(event) => {
          setClient(event.target.value);
          refresh();
        }}
      >
        <option value="">{t('scope.allClients', 'Alle Kunden')}</option>
        {clientOptions.map((c) => (
          <option key={c.id} value={c.id}>
            {c.name}
          </option>
        ))}
      </select>
      <select
        aria-label={t('scope.site', 'Standort')}
        className={selectClass}
        value={siteId}
        disabled={!sites.isSuccess}
        onChange={(event) => {
          setSite(event.target.value);
          refresh();
        }}
      >
        <option value="">{t('scope.allSites', 'Alle Standorte')}</option>
        {siteOptions.map((s) => (
          <option key={s.id} value={s.id}>
            {s.name}
          </option>
        ))}
      </select>
    </div>
  );
}
