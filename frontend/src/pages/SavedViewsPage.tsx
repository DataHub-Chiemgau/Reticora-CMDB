import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import {
  useSavedViews,
  useSavedViewPresets,
  useFilterQuery,
  useSaveView,
  useDeleteSavedView,
} from '../api/cmdbHooks';
import { ApiError } from '../api/client';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { SkeletonList } from '../components/ui/Skeleton';
import { ErrorState } from '../components/ui/ErrorState';

/** A filter spec plus the table it targets, as accepted by POST /search/query. */
type ActiveSpec = Record<string, unknown> & { entity_kind?: string };

export function SavedViewsPage() {
  const { t } = useTranslation();
  const views = useSavedViews();
  const presets = useSavedViewPresets();
  const saveView = useSaveView();
  const deleteView = useDeleteSavedView();
  const [activeView, setActiveView] = useState<string>('');
  // Remember what produced the current result set so it can be saved. Without
  // it there is nothing to persist: the page only ever held a display name.
  const [activeSpec, setActiveSpec] = useState<ActiveSpec | null>(null);
  const query = useFilterQuery(activeSpec);
  const results = query.data?.data ?? null;
  // The search needs search:read and the read permission of the queried
  // entity kind; a 403 is a missing right, not a failure to retry (F-055).
  const forbidden = query.error instanceof ApiError && query.error.status === 403;

  // entity_kind lives in its own column on the saved view, not inside
  // filter_spec, so it has to be folded back in before querying. Without this
  // an asset view silently queries the ci table and its asset-only predicates
  // (warranty, owner, ...) are dropped.
  const run = (name: string, spec: ActiveSpec, entityKind?: string) => {
    const effective: ActiveSpec = { ...spec, entity_kind: spec.entity_kind ?? entityKind ?? 'ci' };
    setActiveView(name);
    // Running the same filter again asks the server again instead of
    // showing the cached result.
    if (activeSpec && JSON.stringify(activeSpec) === JSON.stringify(effective)) {
      void query.refetch();
      return;
    }
    setActiveSpec(effective);
  };

  const saveCurrent = () => {
    if (!activeSpec) return;
    const name = window.prompt(t('savedviews.namePrompt', 'Name der Ansicht'), activeView);
    if (!name) return;
    const { entity_kind: entityKind, ...filterSpec } = activeSpec;
    saveView.mutate({ name, entity_kind: entityKind ?? 'ci', filter_spec: filterSpec });
  };

  return (
    <div className="space-y-4">
      <div>
        <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">
          {t('savedviews.title', 'Gespeicherte Ansichten')}
        </h2>
        <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">
          {t(
            'savedviews.subtitle',
            'Vorlagen und eigene Filter — z. B. „Produktiv-Server ohne Backup“ oder „Garantie läuft in 90 Tagen ab“.',
          )}
        </p>
      </div>

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
        <div className="space-y-4 lg:col-span-1">
          <Card title={t('savedviews.presets', 'Vorlagen')}>
            <ul className="space-y-1">
              {(presets.data?.data ?? []).map((p) => (
                <li key={p.name}>
                  <button
                    onClick={() => run(p.name, p.filter_spec as ActiveSpec, p.entity_kind)}
                    className={`w-full rounded-md px-3 py-2 text-left text-sm hover:bg-gray-50 dark:hover:bg-gray-800 ${activeView === p.name ? 'bg-primary/5 text-primary' : ''}`}
                  >
                    {p.name}
                    <Badge variant="neutral" className="ml-2">
                      {p.entity_kind}
                    </Badge>
                  </button>
                </li>
              ))}
            </ul>
          </Card>
          <Card title={t('savedviews.mine', 'Meine Ansichten')}>
            {views.isLoading ? <SkeletonList rows={3} label="…" /> : null}
            <ul className="space-y-1">
              {(views.data?.data ?? []).map((v) => (
                <li key={v.id} className="flex items-center gap-1">
                  <button
                    onClick={() => run(v.name, v.filter_spec as ActiveSpec, v.entity_kind)}
                    className={`w-full rounded-md px-3 py-2 text-left text-sm hover:bg-gray-50 dark:hover:bg-gray-800 ${activeView === v.name ? 'bg-primary/5 text-primary' : ''}`}
                  >
                    {v.name}
                    {v.shared ? (
                      <Badge variant="info" className="ml-2">
                        {t('savedviews.shared', 'geteilt')}
                      </Badge>
                    ) : null}
                    {!v.shared && (v.shared_with_roles?.length ?? 0) > 0 ? (
                      <Badge variant="info" className="ml-2">
                        {t('savedviews.sharedWithRoles', {
                          count: v.shared_with_roles?.length ?? 0,
                          defaultValue: 'für {{count}} Rollen',
                        })}
                      </Badge>
                    ) : null}
                  </button>
                  <button
                    onClick={() => deleteView.mutate(v.id)}
                    aria-label={`${t('common.delete')}: ${v.name}`}
                    className="rounded-md px-2 py-2 text-sm text-gray-400 hover:text-danger"
                  >
                    ×
                  </button>
                </li>
              ))}
              {(views.data?.data ?? []).length === 0 && !views.isLoading ? (
                <li className="px-3 py-2 text-sm text-gray-500">
                  {t('savedviews.none', 'Noch keine eigenen Ansichten.')}
                </li>
              ) : null}
            </ul>
          </Card>
        </div>

        <div className="lg:col-span-2">
          <Card
            title={activeView || t('savedviews.results', 'Ergebnisse')}
            actions={
              activeSpec ? (
                <Button variant="secondary" onClick={saveCurrent} disabled={saveView.isPending}>
                  {t('savedviews.save', 'Als Ansicht speichern')}
                </Button>
              ) : null
            }
          >
            {query.isFetching ? <SkeletonList rows={5} label="…" /> : null}
            {forbidden ? (
              <ErrorState
                title={t('savedviews.forbidden', 'Keine Berechtigung für diese Ansicht')}
                description={t(
                  'savedviews.forbiddenHint',
                  'Ihnen fehlt das Leserecht für die Objekte, die diese Ansicht abfragt. Wenden Sie sich an Ihre Administration.',
                )}
              />
            ) : query.error ? (
              <ErrorState
                title={t('app.error')}
                retryLabel={t('common.retry')}
                onRetry={() => void query.refetch()}
              />
            ) : null}
            {results ? (
              <ul className="divide-y divide-gray-100 dark:divide-gray-800">
                {results.map((r) => (
                  <li key={r.id} className="py-2 text-sm">
                    <Link
                      to={r.entity_kind === 'asset' ? `/assets` : `/cmdb/${r.id}`}
                      className="font-medium text-primary hover:underline"
                    >
                      {r.name}
                    </Link>
                    {r.summary ? <p className="text-xs text-gray-500">{r.summary}</p> : null}
                  </li>
                ))}
                {results.length === 0 ? (
                  <li className="py-4 text-center text-sm text-gray-500">
                    {t('savedviews.noResults', 'Keine Treffer.')}
                  </li>
                ) : null}
              </ul>
            ) : !activeSpec ? (
              <p className="text-sm text-gray-500">
                {t('savedviews.hint', 'Wählen Sie links eine Ansicht aus.')}
              </p>
            ) : null}
          </Card>
        </div>
      </div>
    </div>
  );
}
