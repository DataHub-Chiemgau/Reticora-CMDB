/**
 * ScopeBanner — the notice of a detail page whose object lies outside the
 * client or site chosen in the scope control (UI-01/UI-02, E-87 option 1).
 * The page still shows the object: deep links and search stay usable, and
 * access is decided by the server's row-level security, not by the scope.
 * The banner names the chosen scope and offers to switch to the object's
 * scope or to drop the client selection.
 */
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { siteApi } from '../api/client';
import { clientApi } from '../api/cmdb';
import { useScopeStore } from '../stores/scope';
import type { ScopeSelection } from '../stores/scope';
import { Button } from './ui/Button';

/** The client and site an object belongs to; empty or absent for none. */
export interface ScopedObject {
  client_id?: string | null;
  site_id?: string | null;
}

/**
 * inScope tells whether an object lies in the chosen scope. It matches the
 * scoped lists (client_id and site_id compare for equality), so an
 * org-wide object without a client lies outside a chosen client. Without a
 * selection everything is in scope.
 */
export function inScope(scope: ScopeSelection, object: ScopedObject): boolean {
  if (scope.clientId && (object.client_id ?? '') !== scope.clientId) return false;
  if (scope.siteId && (object.site_id ?? '') !== scope.siteId) return false;
  return true;
}

export function ScopeBanner({ object }: { object: ScopedObject }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { clientId, siteId, setClient, setSite, reset } = useScopeStore();
  const outside = !inScope({ clientId, siteId }, object);

  // The same queries as the scope control, so the names come from its cache.
  const clients = useQuery({
    queryKey: ['scope', 'clients'],
    queryFn: () => clientApi.list(),
    retry: false,
    staleTime: 60_000,
    enabled: outside && !!clientId,
  });
  const sites = useQuery({
    queryKey: ['scope', 'sites', clientId],
    queryFn: () => siteApi.list({ limit: 200, ...(clientId ? { client_id: clientId } : {}) }),
    retry: false,
    staleTime: 60_000,
    enabled: outside && !!siteId,
  });

  if (!outside) return null;

  const clientName = clientId
    ? (clients.data?.data.find((c) => c.id === clientId)?.name ?? clientId)
    : '';
  const siteName = siteId ? (sites.data?.data.find((s) => s.id === siteId)?.name ?? siteId) : '';
  const scopeLabel = [clientName, siteName].filter(Boolean).join(' / ');

  // Lists read the scope when they fetch: refetch them on a change, as the
  // scope control does.
  function refresh() {
    void queryClient.invalidateQueries({
      predicate: (query) => query.queryKey[0] !== 'scope',
    });
  }

  // Switching keeps the granularity of the selection: the object's client if
  // a client was chosen, its site if a site was chosen. Afterwards the object
  // lies in scope.
  function switchToObject() {
    setClient(clientId ? (object.client_id ?? '') : '');
    setSite(siteId ? (object.site_id ?? '') : '');
    refresh();
  }

  return (
    <section
      role="status"
      aria-label={t('scope.label')}
      className="flex flex-col gap-2 rounded-lg border border-amber-300 bg-amber-50 px-4 py-3 text-sm text-amber-900 sm:flex-row sm:items-center sm:justify-between dark:border-amber-700 dark:bg-amber-950 dark:text-amber-100"
    >
      <p>{t('scope.outsideCI', { scope: scopeLabel })}</p>
      <div className="flex flex-wrap gap-2">
        <Button size="sm" variant="secondary" onClick={switchToObject}>
          {t('scope.switchToObject')}
        </Button>
        <Button
          size="sm"
          variant="secondary"
          onClick={() => {
            reset();
            refresh();
          }}
        >
          {t('scope.allClients')}
        </Button>
      </div>
    </section>
  );
}
