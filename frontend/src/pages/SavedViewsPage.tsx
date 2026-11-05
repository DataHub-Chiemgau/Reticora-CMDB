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
import { Bookmark, Save, X } from 'lucide-react';
import { Button, IconButton } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import {
  tableClasses,
  tdClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';
import { cn } from '../components/ui/utils';
import { ScrollRegion } from '../components/ScrollRegion';
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

/** The row of a view in the lists: a quiet button, the open view highlighted. */
function viewRowClasses(active: boolean) {
  return cn(
    'flex min-h-9 w-full min-w-0 flex-wrap items-center gap-x-2 gap-y-1 rounded-lg px-3 py-1.5 text-left text-sm transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent',
    active ? 'bg-accent-soft font-medium text-accent-strong' : 'text-fg hover:bg-fg/[0.05]',
  );
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
      <PageHeader
        title={t('savedviews.title', 'Gespeicherte Ansichten')}
        description={t(
          'savedviews.subtitle',
          'Vorlagen und eigene Filter — z. B. „Produktiv-Server ohne Backup“ oder „Garantie läuft in 90 Tagen ab“.',
        )}
      />

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
        <div className="min-w-0 space-y-4 lg:col-span-1">
          <Card title={t('savedviews.presets', 'Vorlagen')}>
            <ul className="-mx-2 space-y-0.5">
              {(presets.data?.data ?? []).map((p) => (
                <li key={p.name}>
                  <button
                    type="button"
                    onClick={() => run(p.name, p.filter_spec as ActiveSpec, p.entity_kind)}
                    aria-current={activeView === p.name ? 'true' : undefined}
                    className={viewRowClasses(activeView === p.name)}
                  >
                    <span className="min-w-0 [overflow-wrap:anywhere]">{p.name}</span>
                    <Badge variant="neutral">{p.entity_kind}</Badge>
                  </button>
                </li>
              ))}
            </ul>
          </Card>
          <Card title={t('savedviews.mine', 'Meine Ansichten')}>
            {views.isLoading ? <SkeletonList rows={3} label="…" /> : null}
            <ul className="-mx-2 space-y-0.5">
              {(views.data?.data ?? []).map((v) => (
                <li key={v.id} className="flex items-center gap-1">
                  <button
                    type="button"
                    onClick={() => run(v.name, v.filter_spec as ActiveSpec, v.entity_kind, v)}
                    aria-current={activeView === v.name ? 'true' : undefined}
                    className={viewRowClasses(activeView === v.name)}
                  >
                    <span className="min-w-0 [overflow-wrap:anywhere]">{v.name}</span>
                    {v.shared ? (
                      <Badge variant="info">{t('savedviews.shared', 'geteilt')}</Badge>
                    ) : null}
                    {!v.shared && (v.shared_with_roles?.length ?? 0) > 0 ? (
                      <Badge variant="info">
                        {t('savedviews.sharedWithRoles', {
                          count: v.shared_with_roles?.length ?? 0,
                          defaultValue: 'für {{count}} Rollen',
                        })}
                      </Badge>
                    ) : null}
                  </button>
                  {me && v.owner_id === me ? (
                    <IconButton
                      size="sm"
                      icon={<X />}
                      label={`${t('common.delete')}: ${v.name}`}
                      onClick={() => deleteView.mutate(v.id)}
                      className="shrink-0 hover:text-danger"
                    />
                  ) : null}
                </li>
              ))}
              {(views.data?.data ?? []).length === 0 && !views.isLoading ? (
                <li className="px-3 py-2 text-sm text-fg-muted">
                  {t('savedviews.none', 'Noch keine eigenen Ansichten.')}
                </li>
              ) : null}
            </ul>
          </Card>
        </div>

        <div className="min-w-0 space-y-4 lg:col-span-2">
          {filter ? (
            <Card title={t('savedviews.presentation', 'Darstellung und Freigabe')}>
              <div className="space-y-5">
                <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
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
                <fieldset className="min-w-0">
                  <legend className="text-sm font-medium text-fg">
                    {t('savedviews.columns', 'Spalten')}
                  </legend>
                  <p className="text-sm text-fg-muted">
                    {t('savedviews.columnsHint', 'Ohne Auswahl: Name und Zusammenfassung.')}
                  </p>
                  <div className="mt-3 grid grid-cols-2 gap-2 xl:grid-cols-3">
                    {fieldOptions.map((f) => (
                      <label key={f} className="flex items-center gap-2 text-sm text-fg">
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
                <fieldset className="min-w-0">
                  <legend className="text-sm font-medium text-fg">
                    {t('savedviews.sharing', 'Freigabe')}
                  </legend>
                  <label className="mt-3 flex items-center gap-2 text-sm text-fg">
                    <input
                      type="checkbox"
                      checked={presentation.shared}
                      onChange={(e) => change({ shared: e.target.checked })}
                    />
                    {t('savedviews.shareOrg', 'Für die ganze Organisation freigeben')}
                  </label>
                  <p className="mt-3 text-sm font-medium text-fg">
                    {t('savedviews.shareRoles', 'Für Rollen freigeben')}
                  </p>
                  {!mayReadRoles || roles.error ? (
                    <p className="text-sm text-fg-muted">
                      {t('savedviews.rolesUnavailable', 'Die Rollen können nicht geladen werden.')}
                    </p>
                  ) : (
                    <div className="mt-2 grid grid-cols-2 gap-2 xl:grid-cols-3">
                      {(roles.data?.data ?? []).map((r) => (
                        <label key={r.id} className="flex items-center gap-2 text-sm text-fg">
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
                  <p className="text-sm text-fg-muted">
                    {t(
                      'savedviews.readOnly',
                      'Diese Ansicht wurde mit Ihnen geteilt; Änderungen speichern Sie als eigene Ansicht.',
                    )}
                  </p>
                ) : null}
                {own ? (
                  <Button icon={<Save />} onClick={updateCurrent} disabled={updateView.isPending}>
                    {t('savedviews.update', 'Änderungen speichern')}
                  </Button>
                ) : null}
              </div>
            </Card>
          ) : null}
          <Card className="overflow-hidden p-0">
            <div className="flex flex-wrap items-center justify-between gap-3 px-5 pb-4 pt-5">
              <h3 className="min-w-0 text-base font-semibold text-fg [overflow-wrap:anywhere]">
                {activeView || t('savedviews.results', 'Ergebnisse')}
              </h3>
              {filter ? (
                <Button
                  variant="secondary"
                  icon={<Bookmark />}
                  onClick={saveCurrent}
                  disabled={saveView.isPending}
                >
                  {t('savedviews.save', 'Als Ansicht speichern')}
                </Button>
              ) : null}
            </div>
            {query.isFetching ? (
              <div className="px-5 pb-5">
                <SkeletonList rows={5} label="…" />
              </div>
            ) : null}
            {forbidden ? (
              <div className="px-5 pb-5">
                <ErrorState
                  title={t('savedviews.forbidden', 'Keine Berechtigung für diese Ansicht')}
                  description={t(
                    'savedviews.forbiddenHint',
                    'Ihnen fehlt das Leserecht für die Objekte, die diese Ansicht abfragt. Wenden Sie sich an Ihre Administration.',
                  )}
                />
              </div>
            ) : query.error ? (
              <div className="px-5 pb-5">
                <ErrorState
                  title={t('app.error')}
                  retryLabel={t('common.retry')}
                  onRetry={() => void query.refetch()}
                />
              </div>
            ) : null}
            {results && columns.length > 0 ? (
              // The saved columns only, in the saved order; the first one
              // links to the object.
              <ScrollRegion label={activeView || t('savedviews.results', 'Ergebnisse')}>
                <table className={tableClasses}>
                  <thead className={cn(theadClasses, 'border-t')}>
                    <tr>
                      {columns.map((c) => (
                        <th key={c} scope="col" className={thClasses}>
                          {fieldLabel(t, c)}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {results.map((r) => (
                      <tr key={r.id} className={trClasses}>
                        {columns.map((c, i) => (
                          <td key={c} className={i === 0 ? tdPrimaryClasses : tdClasses}>
                            {i === 0 ? (
                              <Link
                                to={r.entity_kind === 'asset' ? `/assets` : `/cmdb/${r.id}`}
                                className="rounded font-medium text-accent hover:underline"
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
                        <td colSpan={columns.length} className={cn(tdClasses, 'py-6 text-center')}>
                          {t('savedviews.noResults', 'Keine Treffer.')}
                        </td>
                      </tr>
                    ) : null}
                  </tbody>
                </table>
              </ScrollRegion>
            ) : results ? (
              <ul className="divide-y divide-line border-t border-line">
                {results.map((r) => (
                  <li key={r.id} className="px-5 py-3 text-sm">
                    <Link
                      to={r.entity_kind === 'asset' ? `/assets` : `/cmdb/${r.id}`}
                      className="rounded font-medium text-accent hover:underline"
                    >
                      {r.name}
                    </Link>
                    {r.summary ? <p className="text-xs text-fg-muted">{r.summary}</p> : null}
                  </li>
                ))}
                {results.length === 0 ? (
                  <li className="px-5 py-6 text-center text-sm text-fg-muted">
                    {t('savedviews.noResults', 'Keine Treffer.')}
                  </li>
                ) : null}
              </ul>
            ) : !filter ? (
              <div className="px-5 pb-5">
                <EmptyState
                  icon={<Bookmark />}
                  title={t('savedviews.hint', 'Wählen Sie links eine Ansicht aus.')}
                />
              </div>
            ) : null}
          </Card>
        </div>
      </div>
    </div>
  );
}
