/**
 * ScopeSwitcher — the scope control of the layout (UI-01/UI-02): shows the
 * organization of the session and lets the user narrow lists to one client
 * and one of its sites. Only clients and sites the server returns are
 * offered, so the choice never exceeds the user's access (row-level
 * security); a stored selection that is no longer accessible is dropped.
 * The options load in pages of 200; a stored client or site that is not on
 * the loaded pages is read with GET /clients/{id} or /sites/{id} and kept
 * when the server returns it (F-340).
 */
import { useEffect } from 'react';
import { useInfiniteQuery, useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { ApiError, fetchAPI, siteApi } from '../api/client';
import { clientApi } from '../api/cmdb';
import type { components } from '../api/generated/schema';
import { useScopeStore } from '../stores/scope';

type Organization = components['schemas']['Organization'];

const PAGE_SIZE = 200;

const selectClass =
  'rounded-lg border border-gray-300 bg-white px-2 py-1 text-sm text-gray-900 dark:border-gray-700 dark:bg-gray-900 dark:text-gray-100';
const moreClass =
  'rounded-lg px-2 py-1 text-xs text-blue-700 hover:underline disabled:opacity-50 dark:text-blue-300';

interface Page<T> {
  data: T[] | null;
  offset: number;
  has_more: boolean;
}

/** nextOffset is the offset of the page after `last`, or undefined at the end. */
function nextOffset<T>(last: Page<T>): number | undefined {
  const rows = last.data?.length ?? 0;
  return last.has_more && rows > 0 ? last.offset + rows : undefined;
}

/** options joins the loaded pages, each id once, and the stored row if it is not on them. */
function options<T extends { id: string }>(pages: Page<T>[] | undefined, stored?: T): T[] {
  const seen = new Set<string>();
  const out: T[] = [];
  for (const row of [...(pages ?? []).flatMap((p) => p.data ?? []), ...(stored ? [stored] : [])]) {
    if (seen.has(row.id)) continue;
    seen.add(row.id);
    out.push(row);
  }
  return out;
}

/** inaccessible tells whether the server refused a stored id: invalid, unknown or not visible. */
function inaccessible(error: unknown): boolean {
  return error instanceof ApiError && [400, 403, 404].includes(error.status);
}

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
  const clients = useInfiniteQuery({
    queryKey: ['scope', 'client-options'],
    queryFn: ({ pageParam }) => clientApi.list({ offset: pageParam }),
    initialPageParam: 0,
    getNextPageParam: nextOffset,
    retry: false,
    staleTime: 60_000,
  });
  const sites = useInfiniteQuery({
    queryKey: ['scope', 'site-options', clientId],
    queryFn: ({ pageParam }) =>
      siteApi.list({
        limit: PAGE_SIZE,
        ...(pageParam > 0 ? { offset: pageParam } : {}),
        ...(clientId ? { client_id: clientId } : {}),
      }),
    initialPageParam: 0,
    getNextPageParam: nextOffset,
    retry: false,
    staleTime: 60_000,
  });

  const clientLoaded = options(clients.data?.pages).some((c) => c.id === clientId);
  const siteLoaded = options(sites.data?.pages).some((s) => s.id === siteId);

  // A stored selection beyond the loaded pages is read by id (F-340).
  const storedClient = useQuery({
    queryKey: ['scope', 'client', clientId],
    queryFn: () => clientApi.get(clientId),
    enabled: !!clientId && clients.isSuccess && !clientLoaded,
    retry: false,
    staleTime: 60_000,
  });
  const storedSite = useQuery({
    queryKey: ['scope', 'site', siteId],
    queryFn: () => siteApi.get(encodeURIComponent(siteId)),
    enabled: !!siteId && sites.isSuccess && !siteLoaded,
    retry: false,
    staleTime: 60_000,
  });
  // A site belongs to one client: a stored site of another client is dropped.
  const siteOfOtherClient =
    !!clientId && storedSite.data?.id === siteId && storedSite.data.client_id !== clientId;

  const clientOptions = options(
    clients.data?.pages,
    storedClient.data?.id === clientId ? storedClient.data : undefined,
  );
  const siteOptions = options(
    sites.data?.pages,
    storedSite.data?.id === siteId && !siteOfOtherClient ? storedSite.data : undefined,
  );

  // Drop a stored selection the user can no longer access.
  useEffect(() => {
    if (clientId && !clientLoaded && inaccessible(storedClient.error)) {
      setClient('');
    }
  }, [clientId, clientLoaded, storedClient.error, setClient]);
  useEffect(() => {
    if (siteId && !siteLoaded && (inaccessible(storedSite.error) || siteOfOtherClient)) {
      setSite('');
    }
  }, [siteId, siteLoaded, storedSite.error, siteOfOtherClient, setSite]);

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
      {clients.hasNextPage && (
        <button
          type="button"
          className={moreClass}
          disabled={clients.isFetchingNextPage}
          onClick={() => void clients.fetchNextPage()}
        >
          {t('scope.moreClients', 'Weitere Kunden laden')}
        </button>
      )}
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
      {sites.hasNextPage && (
        <button
          type="button"
          className={moreClass}
          disabled={sites.isFetchingNextPage}
          onClick={() => void sites.fetchNextPage()}
        >
          {t('scope.moreSites', 'Weitere Standorte laden')}
        </button>
      )}
    </div>
  );
}
