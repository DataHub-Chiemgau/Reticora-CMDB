/**
 * OrgDeletionBanner is the banner of the grace period of a deletion of the
 * organization (TLC-06, WP-1157): while the state is requested every user
 * sees that the organization is read-only until the due date; a holder of
 * org:delete (org_admin) can withdraw the request from here. It reads GET
 * /org/deletion only when the release serves the module org_lifecycle.
 */
import { AlertTriangle } from 'lucide-react';
import { useQueryClient } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ApiError, isMfaRequired, orgReadOnlyMessage } from '../api/client';
import { useHasPermission, useOrgDeletion, useOrgDeletionMutations } from '../api/hooks';
import { useAuthStore } from '../auth/authStore';
import { useModules } from '../hooks/useEntitlements';
import { Button } from './ui/Button';

export function OrgDeletionBanner() {
  const isAuthenticated = useAuthStore((s) => s.isAuthenticated());
  const { modules } = useModules();
  // Only a release that lists the module enabled serves the route.
  const served = !!modules?.some((m) => m.key === 'org_lifecycle' && m.enabled);
  if (!isAuthenticated || !served) return null;
  return <OrgDeletionBannerBody />;
}

function OrgDeletionBannerBody() {
  const { t, i18n } = useTranslation();
  const deletion = useOrgDeletion();
  const queryClient = useQueryClient();
  // Another admin may request the deletion while this session runs: the
  // first write refused with 423 org-pending-deletion reloads the state, so
  // the banner shows without a page reload.
  useEffect(
    () =>
      queryClient.getMutationCache().subscribe((event) => {
        if (
          event.type === 'updated' &&
          event.action.type === 'error' &&
          orgReadOnlyMessage(event.action.error) !== undefined
        ) {
          void queryClient.invalidateQueries({ queryKey: ['org-data', 'deletion'] });
        }
      }),
    [queryClient],
  );
  const canCancel = useHasPermission('org:delete') === true;
  const { cancel } = useOrgDeletionMutations();
  const [error, setError] = useState<string>();
  const state = deletion.data?.state;
  if (state !== 'requested' || !deletion.data?.due_at) return null;
  const date = new Intl.DateTimeFormat(i18n.language, { dateStyle: 'long' }).format(
    new Date(deletion.data.due_at),
  );
  return (
    <section
      aria-label={t('orgData.banner.label')}
      data-testid="org-deletion-banner"
      className="flex flex-wrap items-start gap-3 rounded-xl border border-red-300 bg-red-50 px-4 py-3 text-sm text-red-900 dark:border-red-500/40 dark:bg-red-500/10 dark:text-red-100"
    >
      <AlertTriangle aria-hidden="true" className="mt-0.5 h-4 w-4 shrink-0" />
      <div className="min-w-0 flex-1 space-y-1">
        <p>{t('orgData.banner.text', { date })}</p>
        {error ? <p role="alert">{error}</p> : null}
      </div>
      {canCancel ? (
        <Button
          size="sm"
          variant="secondary"
          loading={cancel.isPending}
          onClick={() => {
            setError(undefined);
            cancel.mutate(undefined, {
              onError: (err) =>
                setError(
                  isMfaRequired(err)
                    ? t('orgData.mfaHint')
                    : err instanceof ApiError
                      ? err.message
                      : t('orgData.delete.failed'),
                ),
            });
          }}
        >
          {t('orgData.banner.cancel')}
        </Button>
      ) : null}
    </section>
  );
}
