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
import type { QueryResult } from '../api/cmdb';
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
  const query = useFilterQuery();
  const saveView = useSaveView();
  const deleteView = useDeleteSavedView();
  const [results, setResults] = useState<QueryResult[] | null>(null);
  const [activeView, setActiveView] = useState<string>('');
  // Remember what produced the current result set so it can be saved. Without
  // it there is nothing to persist: the page only ever held a display name.
  const [activeSpec, setActiveSpec] = useState<ActiveSpec | null>(null);

  // entity_kind lives in its own column on the saved view, not inside
  // filter_spec, so it has to be folded back in before querying. Without this
  // an asset view silently queries the ci table and its asset-only predicates
  // (warranty, owner, ...) are dropped.
  const run = (name: string, spec: ActiveSpec, entityKind?: string) => {
    const effective: ActiveSpec = { ...spec, entity_kind: spec.entity_kind ?? entityKind ?? 'ci' };
    setActiveView(name);
    setActiveSpec(effective);
    query.mutate(effective, { onSuccess: (res) => setResults(res.data) });
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
            {query.isPending ? <SkeletonList rows={5} label="…" /> : null}
            {query.error ? (
              <ErrorState
                title={t('app.error')}
                retryLabel={t('common.retry')}
                onRetry={() => void 0}
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
            ) : (
              <p className="text-sm text-gray-500">
                {t('savedviews.hint', 'Wählen Sie links eine Ansicht aus.')}
              </p>
            )}
          </Card>
        </div>
      </div>
    </div>
  );
}
