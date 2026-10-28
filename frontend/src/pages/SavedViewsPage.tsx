import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { Link } from 'react-router-dom';
import {
  useSavedViews,
  useSavedViewPresets,
  useFilterQuery,
  useSaveView,
  useUpdateSavedView,
  useDeleteSavedView,
} from '../api/cmdbHooks';
import { useQuery } from '@tanstack/react-query';
import { useEffectivePermissions, useHasPermission } from '../api/hooks';
import { ApiError, roleApi } from '../api/client';
import { VIEW_FIELDS, type QueryResult, type SavedView, type SavedViewSortKey } from '../api/cmdb';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { ErrorState } from '../components/ui/ErrorState';
import { fieldValueText } from '../components/cmdb/fieldValue';

/** A filter spec plus the table it targets, as accepted by POST /search/query. */
type ActiveSpec = Record<string, unknown> & { entity_kind?: string };

/**
 * The presentation and sharing of a view (VIE-01, F-336): sort keys and
 * columns go with every run, sharing with save and update.
 */
interface Presentation {
  sort: SavedViewSortKey[];
  columns: string[];
  shared: boolean;
  roles: string[];
}

const NO_PRESENTATION: Presentation = { sort: [], columns: [], shared: false, roles: [] };

function presentationOf(view?: SavedView): Presentation {
  if (!view) return NO_PRESENTATION;
  return {
    sort: view.sort ?? [],
    columns: view.columns ?? [],
    shared: view.shared,
    roles: view.shared_with_roles ?? [],
  };
}

/**
 * The spec of a run: the filter with sort and columns of the view. A view
 * without them sends the filter alone, so its rows come by name with name
 * and summary as before.
 */
function runSpec(filter: ActiveSpec, p: Presentation): ActiveSpec {
  return {
    ...filter,
    ...(p.sort.length > 0 ? { sort: p.sort } : {}),
    ...(p.columns.length > 0 ? { columns: p.columns } : {}),
  };
}

/** The header of a column: the catalog label, attributes by their key. */
function fieldLabel(t: TFunction, field: string): string {
  if (field.startsWith('attributes.')) {
    return t('savedviews.attribute', {
      key: field.slice('attributes.'.length),
      defaultValue: 'Attribut {{key}}',
    });
  }
  return t(`savedviews.fields.${field}`, field);
}

function formatCell(value: unknown): string {
  if (value === null || value === undefined || value === '') return '–';
  if (typeof value === 'object') return JSON.stringify(value);
  return String(value);
}

/** The text of one cell; a catalog-keyed field shows its label (GLO-10). */
function cellText(t: TFunction, row: QueryResult, field: string): string {
  return fieldValueText(t, field, row.columns?.[field], formatCell);
}

export function SavedViewsPage() {
  const { t } = useTranslation();
  const views = useSavedViews();
  const presets = useSavedViewPresets();
  const saveView = useSaveView();
  const updateView = useUpdateSavedView();
  const deleteView = useDeleteSavedView();
  // Sharing with roles needs the role catalogue (role:read); without it
  // the page says so and the view can still be shared org-wide. The list
  // is not asked for without the right, so a reader sees no refused call.
  const mayReadRoles = useHasPermission('role:read') === true;
  const roles = useQuery({
    queryKey: ['roles', { limit: 200 }],
    queryFn: () => roleApi.list({ limit: 200 }),
    enabled: mayReadRoles,
  });
  // Only the owner changes or deletes a view (E-86): a view shared with the
  // caller is read-only and the server answers 404 to its PATCH and DELETE.
  // Until the caller's id is known no view counts as own (fail closed).
  const me = useEffectivePermissions().data?.user_id;
  const [activeView, setActiveView] = useState<string>('');
  // The saved view being run, if any; presets and changed filters have none.
  const [runningView, setRunningView] = useState<SavedView | null>(null);
  // Remember what produced the current result set so it can be saved. Without
  // it there is nothing to persist: the page only ever held a display name.
  const [filter, setFilter] = useState<ActiveSpec | null>(null);
  const [presentation, setPresentation] = useState<Presentation>(NO_PRESENTATION);
  const activeSpec = useMemo(
    () => (filter ? runSpec(filter, presentation) : null),
    [filter, presentation],
  );
  const query = useFilterQuery(activeSpec);
  const results = query.data?.data ?? null;
  // The search needs search:read and the read permission of the queried
  // entity kind; a 403 is a missing right, not a failure to retry (F-055).
  const forbidden = query.error instanceof ApiError && query.error.status === 403;
  const entityKind = filter?.entity_kind ?? 'ci';
  const fields = VIEW_FIELDS[entityKind] ?? VIEW_FIELDS.ci ?? [];
  const own = Boolean(me && runningView && runningView.owner_id === me);

  // entity_kind lives in its own column on the saved view, not inside
  // filter_spec, so it has to be folded back in before querying. Without this
  // an asset view silently queries the ci table and its asset-only predicates
  // (warranty, owner, ...) are dropped. A saved view brings its sort,
  // columns and sharing; a preset starts without them.
  const run = (name: string, spec: ActiveSpec, entityKind?: string, view?: SavedView) => {
    const effective: ActiveSpec = { ...spec, entity_kind: spec.entity_kind ?? entityKind ?? 'ci' };
    const next = presentationOf(view);
    setActiveView(name);
    setRunningView(view ?? null);
    // Running the same filter again asks the server again instead of
    // showing the cached result.
    if (activeSpec && JSON.stringify(activeSpec) === JSON.stringify(runSpec(effective, next))) {
      setPresentation(next);
      void query.refetch();
      return;
    }
    setFilter(effective);
    setPresentation(next);
  };

  const change = (patch: Partial<Presentation>) => setPresentation((p) => ({ ...p, ...patch }));

  // The page edits the first sort key; further keys of a view saved through
  // the API are kept.
  const setSortField = (field: string) =>
    change({
      sort: field
        ? [{ field, dir: presentation.sort[0]?.dir ?? 'asc' }, ...presentation.sort.slice(1)]
        : [],
    });
  const setSortDir = (dir: 'asc' | 'desc') => {
    const first = presentation.sort[0];
    if (first) change({ sort: [{ ...first, dir }, ...presentation.sort.slice(1)] });
  };
  // Columns keep the order in which they were chosen.
  const toggleColumn = (field: string) =>
    change({
      columns: presentation.columns.includes(field)
        ? presentation.columns.filter((c) => c !== field)
        : [...presentation.columns, field],
    });
  const toggleRole = (id: string) =>
    change({
      roles: presentation.roles.includes(id)
        ? presentation.roles.filter((r) => r !== id)
        : [...presentation.roles, id],
    });

  const saveCurrent = () => {
    if (!filter) return;
    const name = window.prompt(t('savedviews.namePrompt', 'Name der Ansicht'), activeView);
    if (!name) return;
    const { entity_kind: kind, ...filterSpec } = filter;
    saveView.mutate({
      name,
      entity_kind: kind ?? 'ci',
      filter_spec: filterSpec,
      sort: presentation.sort,
      columns: presentation.columns,
      shared: presentation.shared,
      shared_with_roles: presentation.roles,
    });
  };

  const updateCurrent = () => {
    if (!runningView || !own) return;
    updateView.mutate(
      {
        id: runningView.id,
        data: {
          sort: presentation.sort,
          columns: presentation.columns,
          shared: presentation.shared,
          shared_with_roles: presentation.roles,
        },
      },
      { onSuccess: (updated) => setRunningView(updated as SavedView) },
    );
  };

  const columns = (activeSpec?.columns as string[] | undefined) ?? [];
  const sortField = presentation.sort[0]?.field ?? '';
  const fieldOptions = [...fields, ...presentation.sort.map((k) => k.field)]
    .concat(presentation.columns)
    .filter((f, i, all) => all.indexOf(f) === i);

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
                    onClick={() => run(v.name, v.filter_spec as ActiveSpec, v.entity_kind, v)}
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
                  {me && v.owner_id === me ? (
                    <button
                      onClick={() => deleteView.mutate(v.id)}
                      aria-label={`${t('common.delete')}: ${v.name}`}
                      className="rounded-md px-2 py-2 text-sm text-muted hover:text-danger"
                    >
                      ×
                    </button>
                  ) : null}
                </li>
              ))}
              {(views.data?.data ?? []).length === 0 && !views.isLoading ? (
                <li className="px-3 py-2 text-sm text-muted">
                  {t('savedviews.none', 'Noch keine eigenen Ansichten.')}
                </li>
              ) : null}
            </ul>
          </Card>
        </div>

        <div className="space-y-4 lg:col-span-2">
          {filter ? (
            <Card title={t('savedviews.presentation', 'Darstellung und Freigabe')}>
              <div className="space-y-4">
                <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                  <Select
                    label={t('savedviews.sortBy', 'Sortieren nach')}
                    value={sortField}
                    onChange={(e) => setSortField(e.target.value)}
                    options={[
                      { value: '', label: t('savedviews.sortDefault', 'Standard (Name)') },
                      ...fieldOptions.map((f) => ({ value: f, label: fieldLabel(t, f) })),
                    ]}
                  />
                  <Select
                    label={t('savedviews.sortDir', 'Richtung')}
                    value={presentation.sort[0]?.dir ?? 'asc'}
                    disabled={!sortField}
                    onChange={(e) => setSortDir(e.target.value as 'asc' | 'desc')}
                    options={[
                      { value: 'asc', label: t('savedviews.asc', 'Aufsteigend') },
                      { value: 'desc', label: t('savedviews.desc', 'Absteigend') },
                    ]}
                  />
                </div>
                <fieldset>
                  <legend className="text-sm font-medium text-gray-700 dark:text-gray-200">
                    {t('savedviews.columns', 'Spalten')}
                  </legend>
                  <p className="text-xs text-muted">
                    {t('savedviews.columnsHint', 'Ohne Auswahl: Name und Zusammenfassung.')}
                  </p>
                  <div className="mt-2 grid grid-cols-2 gap-1 sm:grid-cols-3">
                    {fieldOptions.map((f) => (
                      <label key={f} className="flex items-center gap-2 text-sm">
                        <input
                          type="checkbox"
                          checked={presentation.columns.includes(f)}
                          onChange={() => toggleColumn(f)}
                        />
                        {fieldLabel(t, f)}
                      </label>
                    ))}
                  </div>
                </fieldset>
                <fieldset>
                  <legend className="text-sm font-medium text-gray-700 dark:text-gray-200">
                    {t('savedviews.sharing', 'Freigabe')}
                  </legend>
                  <label className="mt-2 flex items-center gap-2 text-sm">
                    <input
                      type="checkbox"
                      checked={presentation.shared}
                      onChange={(e) => change({ shared: e.target.checked })}
                    />
                    {t('savedviews.shareOrg', 'Für die ganze Organisation freigeben')}
                  </label>
                  <p className="mt-2 text-sm text-gray-700 dark:text-gray-200">
                    {t('savedviews.shareRoles', 'Für Rollen freigeben')}
                  </p>
                  {!mayReadRoles || roles.error ? (
                    <p className="text-xs text-muted">
                      {t('savedviews.rolesUnavailable', 'Die Rollen können nicht geladen werden.')}
                    </p>
                  ) : (
                    <div className="mt-1 grid grid-cols-2 gap-1 sm:grid-cols-3">
                      {(roles.data?.data ?? []).map((r) => (
                        <label key={r.id} className="flex items-center gap-2 text-sm">
                          <input
                            type="checkbox"
                            checked={presentation.roles.includes(r.id)}
                            onChange={() => toggleRole(r.id)}
                          />
                          {r.name}
                        </label>
                      ))}
                    </div>
                  )}
                </fieldset>
                {runningView && !own ? (
                  <p className="text-xs text-muted">
                    {t(
                      'savedviews.readOnly',
                      'Diese Ansicht wurde mit Ihnen geteilt; Änderungen speichern Sie als eigene Ansicht.',
                    )}
                  </p>
                ) : null}
                {own ? (
                  <Button onClick={updateCurrent} disabled={updateView.isPending}>
                    {t('savedviews.update', 'Änderungen speichern')}
                  </Button>
                ) : null}
              </div>
            </Card>
          ) : null}
          <Card
            title={activeView || t('savedviews.results', 'Ergebnisse')}
            actions={
              filter ? (
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
            {results && columns.length > 0 ? (
              // The saved columns only, in the saved order; the first one
              // links to the object.
              <div className="overflow-x-auto">
                <table className="min-w-full text-sm">
                  <thead>
                    <tr className="border-b border-gray-200 text-left dark:border-gray-700">
                      {columns.map((c) => (
                        <th key={c} scope="col" className="px-2 py-2 font-medium">
                          {fieldLabel(t, c)}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
                    {results.map((r) => (
                      <tr key={r.id}>
                        {columns.map((c, i) => (
                          <td key={c} className="px-2 py-2">
                            {i === 0 ? (
                              <Link
                                to={r.entity_kind === 'asset' ? `/assets` : `/cmdb/${r.id}`}
                                className="font-medium text-primary hover:underline"
                              >
                                {cellText(t, r, c)}
                              </Link>
                            ) : (
                              cellText(t, r, c)
                            )}
                          </td>
                        ))}
                      </tr>
                    ))}
                    {results.length === 0 ? (
                      <tr>
                        <td colSpan={columns.length} className="py-4 text-center text-muted">
                          {t('savedviews.noResults', 'Keine Treffer.')}
                        </td>
                      </tr>
                    ) : null}
                  </tbody>
                </table>
              </div>
            ) : results ? (
              <ul className="divide-y divide-gray-100 dark:divide-gray-800">
                {results.map((r) => (
                  <li key={r.id} className="py-2 text-sm">
                    <Link
                      to={r.entity_kind === 'asset' ? `/assets` : `/cmdb/${r.id}`}
                      className="font-medium text-primary hover:underline"
                    >
                      {r.name}
                    </Link>
                    {r.summary ? <p className="text-xs text-muted">{r.summary}</p> : null}
                  </li>
                ))}
                {results.length === 0 ? (
                  <li className="py-4 text-center text-sm text-muted">
                    {t('savedviews.noResults', 'Keine Treffer.')}
                  </li>
                ) : null}
              </ul>
            ) : !filter ? (
              <p className="text-sm text-muted">
                {t('savedviews.hint', 'Wählen Sie links eine Ansicht aus.')}
              </p>
            ) : null}
          </Card>
        </div>
      </div>
    </div>
  );
}
