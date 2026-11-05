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
import { Building2, X } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { ApiError, fetchAPI, siteApi } from '../api/client';
import { clientApi } from '../api/cmdb';
import type { components } from '../api/generated/schema';
import { useScopeStore } from '../stores/scope';
import { cn } from './ui/utils';

type Organization = components['schemas']['Organization'];

const PAGE_SIZE = 200;

const moreClass =
  'rounded-md px-2 py-1 text-xs font-medium text-accent hover:underline disabled:opacity-50';

/**
 * scopeSelectClass: a select that reads as part of the scope path. At rest it
 * is quiet text; with a client or site chosen it turns accent, because the
 * lists below are then narrowed and the user must see it.
 */
function scopeSelectClass(active: boolean, stacked: boolean): string {
  return [
    'field-select h-8 rounded-lg border text-sm font-medium outline-none transition-colors focus-visible:ring-2 focus-visible:ring-accent/40 disabled:opacity-60',
    stacked ? 'w-full pl-3' : 'max-w-40 truncate pl-2.5',
    active
      ? 'border-accent/40 bg-accent-soft text-accent-strong'
      : 'border-transparent bg-transparent text-fg-muted hover:border-line-strong hover:bg-panel hover:text-fg',
  ].join(' ');
}

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

export function ScopeSwitcher({ layout = 'inline' }: { layout?: 'inline' | 'stacked' }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { clientId, siteId, setClient, setSite, reset } = useScopeStore();

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

  const stacked = layout === 'stacked';
  const orgName = org.data?.name ?? t('scope.organization', 'Organisation');

  return (
    <div
      role="group"
      aria-label={t('scope.label', 'Arbeitsbereich')}
      className={cn(
        'text-sm',
        stacked ? 'flex flex-col gap-2' : 'flex min-w-0 flex-nowrap items-center gap-1',
      )}
    >
      <span
        className={cn(
          'flex min-w-0 items-center gap-2 font-medium text-fg',
          stacked ? 'px-0.5' : 'max-w-36 px-1.5',
        )}
        title={t('scope.organization', 'Organisation')}
      >
        <Building2 aria-hidden="true" className="h-4 w-4 shrink-0 text-fg-subtle" />
        <span className="truncate">{orgName}</span>
      </span>
      {stacked ? null : <Separator />}
      <select
        aria-label={t('scope.client', 'Kunde')}
        className={scopeSelectClass(clientId !== '', stacked)}
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
      {stacked ? null : <Separator />}
      <select
        aria-label={t('scope.site', 'Standort')}
        className={scopeSelectClass(siteId !== '', stacked)}
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
      {clientId || siteId ? (
        <button
          type="button"
          title={t('scope.reset', 'Bereich zurücksetzen')}
          aria-label={t('scope.reset', 'Bereich zurücksetzen')}
          className={cn(
            'inline-flex h-8 shrink-0 items-center gap-1.5 rounded-lg px-2 text-xs font-medium text-fg-muted transition-colors hover:bg-fg/[0.06] hover:text-fg focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent',
            stacked && 'w-fit',
          )}
          onClick={() => {
            reset();
            refresh();
          }}
        >
          <X aria-hidden="true" className="h-3.5 w-3.5" />
          {stacked ? t('scope.reset', 'Bereich zurücksetzen') : null}
        </button>
      ) : null}
    </div>
  );
}

/** Separator is the slash between the levels of the scope path. */
function Separator() {
  return (
    <span aria-hidden="true" className="select-none text-fg-subtle/70">
      /
    </span>
  );
}
