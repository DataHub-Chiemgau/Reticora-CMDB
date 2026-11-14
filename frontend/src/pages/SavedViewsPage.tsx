import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { Link } from 'react-router';
import {
  useSavedViews,
  useSavedViewPresets,
  useFilterQuery,
  useSaveView,
  useClonePreset,
  useUpdateSavedView,
  useDeleteSavedView,
  useFilterPreview,
  useRelationshipTypes,
  useSearchableAttributeDefinitions,
} from '../api/cmdbHooks';
import { useQuery } from '@tanstack/react-query';
import { useEffectivePermissions, useHasPermission } from '../api/hooks';
import { ApiError, roleApi } from '../api/client';
import {
  VIEW_FIELDS,
  presetName,
  type QueryResult,
  type SavedView,
  type SavedViewPreset,
  type SavedViewSortKey,
} from '../api/cmdb';
import { Badge } from '../components/ui/Badge';
import { Bookmark, Copy, Eye, Filter, Pencil, Plus, Save, X } from 'lucide-react';
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
import { Input } from '../components/ui/Input';
import { RuleBuilder } from '../components/rules/RuleBuilder';
import { useEntitlements, useModules, navFeatureFor } from '../hooks/useEntitlements';
import {
  FILTER_ENTITY_KINDS,
  fieldCatalog,
  isFilterExpr,
  type FilterCheck,
  type FilterEntityKind,
  type FilterExpr,
} from '../rules/builderModes';

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

/**
 * viewRefusal words a refused write on a view (WP-817): 403 for a missing
 * right, 404 for a view that is gone or not the caller's to change (E-86),
 * 409 for a name the caller already uses.
 */
function viewRefusal(error: unknown, t: TFunction): string {
  if (error instanceof ApiError) {
    if (error.status === 403) return t('savedviews.errors.forbidden');
    if (error.status === 404) return t('savedviews.errors.notFound');
    if (error.status === 409) return t('savedviews.errors.conflict');
    return t('savedviews.errors.reason', { detail: error.detail || error.title || error.message });
  }
  return t('savedviews.errors.reason', {
    detail: error instanceof Error ? error.message : String(error),
  });
}

/** The row of a view in the lists: a quiet button, the open view highlighted. */
function viewRowClasses(active: boolean) {
  return cn(
    'flex min-h-9 w-full min-w-0 flex-wrap items-center gap-x-2 gap-y-1 rounded-lg px-3 py-1.5 text-left text-sm transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent',
    active ? 'bg-accent-soft font-medium text-accent-strong' : 'text-fg hover:bg-fg/[0.05]',
  );
}

/**
 * What the filter editor works on (WP-315): a new view, a preset or a saved
 * view with its filter split into the AST (where) and the flat keys the
 * builder does not edit (warranty_within_days, lacks_relationship, tags,
 * attributes …), which are kept as they are.
 */
interface Editing {
  /** key remounts the editor for another filter. */
  key: number;
  /** view is the saved view edited; a preset and a new view have none. */
  view?: SavedView;
  name: string;
  entityKind: FilterEntityKind;
  where?: FilterExpr;
  flat: Record<string, unknown>;
  /** fromRun: the filter of the running view or preset, saved with its presentation. */
  fromRun: boolean;
}

/** splitSpec separates the AST of a filter spec from its flat keys. */
function splitSpec(spec: Record<string, unknown>): {
  where?: FilterExpr;
  flat: Record<string, unknown>;
} {
  const flat: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(spec)) {
    if (key !== 'where' && key !== 'entity_kind') flat[key] = value;
  }
  const where = spec.where;
  if (where === undefined || isFilterExpr(where)) return { where, flat };
  // A where the builder cannot load stays a flat key, untouched.
  return { flat: { ...flat, where } };
}

/** The path of the node a 422 names, from its field or the start of its detail. */
function problemPath(error: ApiError): { path: string; message: string } | undefined {
  const detail = error.detail ?? '';
  const fromDetail = /^((?:filter_spec\.)?where[\w.[\]]*):\s*([\s\S]*)$/.exec(detail);
  const field = error.field ?? '';
  if (/^(filter_spec\.)?where/.test(field)) {
    // The detail may name a deeper path than field (field where, detail where.and[1]: …).
    if (fromDetail?.[1]?.startsWith(field)) {
      return { path: fromDetail[1], message: fromDetail[2] ?? '' };
    }
    return { path: field, message: detail };
  }
  return fromDetail?.[1] ? { path: fromDetail[1], message: fromDetail[2] ?? '' } : undefined;
}

interface FilterEditorProps {
  editing: Editing;
  /** writable: the view is saved by PATCH (own, or saved_view:manage); else as a new view. */
  writable: boolean;
  presentation: Presentation;
  assetsEnabled: boolean;
  onSaved: (view: SavedView) => void;
  onCancel: () => void;
}

/**
 * FilterEditor edits filter_spec.where of a view with the RuleBuilder
 * (WP-315, CH18): entity kind, name, the AST, a server preview with
 * limit 10 and saving by PATCH (writable views, the complete state) or by
 * POST (presets, read-only and new views, without from_preset).
 */
function FilterEditor({
  editing,
  writable,
  presentation,
  assetsEnabled,
  onSaved,
  onCancel,
}: FilterEditorProps) {
  const { t } = useTranslation();
  const [name, setName] = useState(editing.name);
  const [entityKind, setEntityKind] = useState<FilterEntityKind>(editing.entityKind);
  const [check, setCheck] = useState<FilterCheck>({ ok: true, where: editing.where });
  const [showIssues, setShowIssues] = useState(false);
  const [serverError, setServerError] = useState<{ path: string; message: string }>();
  const [forbiddenFields, setForbiddenFields] = useState<string[]>([]);
  const [notice, setNotice] = useState<string>();
  const definitions = useSearchableAttributeDefinitions();
  const relTypes = useRelationshipTypes(entityKind === 'ci');
  const preview = useFilterPreview();
  const saveView = useSaveView();
  const updateView = useUpdateSavedView();

  const fields = useMemo(
    () => ({
      ci: fieldCatalog('ci', { definitions: definitions.data ?? [], assets: assetsEnabled }),
      asset: fieldCatalog('asset', { definitions: definitions.data ?? [] }),
    }),
    [definitions.data, assetsEnabled],
  );
  const kinds = FILTER_ENTITY_KINDS.filter(
    (k) => k !== 'asset' || assetsEnabled || editing.entityKind === 'asset',
  );
  const flatEntries = Object.entries(editing.flat);

  const onBuilderChange = (next: FilterCheck) => {
    setCheck(next);
    setServerError(undefined);
    setNotice(undefined);
  };

  /** The filter spec to send, or undefined while the AST is not complete. */
  const completeSpec = (): Record<string, unknown> | undefined => {
    if (!check.ok) {
      setShowIssues(true);
      setNotice(t('savedviews.editor.incomplete'));
      return undefined;
    }
    return { ...editing.flat, ...(check.where ? { where: check.where } : {}) };
  };

  const failed = (error: unknown, saving: boolean) => {
    if (!(error instanceof ApiError)) {
      setNotice(t('app.error'));
      return;
    }
    if (error.status === 422) {
      const at = problemPath(error);
      if (at) {
        setServerError(at);
        setNotice(t('savedviews.editor.invalid'));
        return;
      }
    }
    if (error.status === 403 && error.conflictFields.length > 0) {
      setForbiddenFields((current) => [...new Set([...current, ...error.conflictFields])]);
      setNotice(
        t('savedviews.editor.forbiddenFields', { fields: error.conflictFields.join(', ') }),
      );
      return;
    }
    if (error.status === 403) {
      setNotice(t('savedviews.forbidden'));
      return;
    }
    if (saving && error.status === 404) {
      setNotice(t('savedviews.editor.noWritePermission'));
      return;
    }
    setNotice(error.detail || error.title || t('app.error'));
  };

  const runPreview = () => {
    const spec = completeSpec();
    if (!spec) return;
    preview.mutate({ ...spec, entity_kind: entityKind }, { onError: (e) => failed(e, false) });
  };

  const save = () => {
    const spec = completeSpec();
    if (!spec) return;
    const trimmed = name.trim();
    if (!trimmed) {
      setNotice(t('savedviews.editor.nameRequired'));
      return;
    }
    // The complete state: the server replaces filter_spec as a whole.
    const state = {
      name: trimmed,
      entity_kind: entityKind,
      filter_spec: spec,
      sort: presentation.sort,
      columns: presentation.columns,
      shared: presentation.shared,
      shared_with_roles: presentation.roles,
    };
    const done = (response: unknown) =>
      onSaved({
        ...(editing.view ?? {}),
        ...(typeof response === 'object' && response !== null ? response : {}),
        ...state,
      } as SavedView);
    if (writable && editing.view) {
      updateView.mutate(
        { id: editing.view.id, data: state },
        { onSuccess: done, onError: (e) => failed(e, true) },
      );
    } else {
      saveView.mutate(state, { onSuccess: done, onError: (e) => failed(e, true) });
    }
  };

  const hits = preview.data?.data ?? [];
  return (
    <Card title={t('savedviews.editor.title')}>
      <div className="space-y-5">
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Input
            label={t('savedviews.editor.name')}
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <Select
            label={t('savedviews.editor.entityKind')}
            value={entityKind}
            onChange={(e) => {
              setEntityKind(e.target.value as FilterEntityKind);
              setServerError(undefined);
              setForbiddenFields([]);
            }}
            options={kinds.map((k) => ({ value: k, label: t(`rules.entityKinds.${k}`) }))}
          />
        </div>
        {flatEntries.length > 0 ? (
          <div>
            <p className="text-sm font-medium text-fg">{t('savedviews.editor.flat')}</p>
            <p className="text-sm text-fg-muted">{t('savedviews.editor.flatHint')}</p>
            <ul
              aria-label={t('savedviews.editor.flat')}
              className="mt-2 flex flex-wrap gap-2 text-sm"
            >
              {flatEntries.map(([key, value]) => (
                <li
                  key={key}
                  className="rounded-lg border border-line bg-panel px-3 py-1 font-mono text-xs text-fg [overflow-wrap:anywhere]"
                >
                  {`${key}: ${fieldValueText(t, key, value, (v) => JSON.stringify(v))}`}
                </li>
              ))}
            </ul>
          </div>
        ) : null}
        <RuleBuilder
          key={`${editing.key}:${entityKind}`}
          entityKind={entityKind}
          initialValue={entityKind === editing.entityKind ? editing.where : undefined}
          fields={fields}
          relationshipTypes={(relTypes.data?.data ?? []).map((r) => ({
            key: r.key,
            label: r.display_name || r.key,
          }))}
          onChange={onBuilderChange}
          serverError={serverError}
          forbiddenFields={forbiddenFields}
          showIssues={showIssues}
        />
        {notice ? (
          <p role="alert" className="text-sm text-danger">
            {notice}
          </p>
        ) : null}
        {!writable ? (
          <p className="text-sm text-fg-muted">{t('savedviews.editor.saveAsNew')}</p>
        ) : null}
        <div className="flex flex-wrap gap-2">
          <Button
            variant="secondary"
            icon={<Eye />}
            onClick={runPreview}
            disabled={preview.isPending}
          >
            {t('savedviews.editor.preview')}
          </Button>
          <Button
            icon={<Save />}
            onClick={save}
            disabled={saveView.isPending || updateView.isPending}
          >
            {writable ? t('savedviews.editor.save') : t('savedviews.editor.saveNew')}
          </Button>
          <Button variant="ghost" onClick={onCancel}>
            {t('common.cancel')}
          </Button>
        </div>
        {preview.data ? (
          <section aria-label={t('savedviews.editor.previewTitle')} className="space-y-2">
            <p className="text-sm font-medium text-fg">
              {t('savedviews.editor.previewTotal', { count: preview.data.total ?? hits.length })}
            </p>
            <ul className="divide-y divide-line rounded-lg border border-line">
              {hits.map((r) => (
                <li key={r.id} className="px-3 py-2 text-sm text-fg">
                  {r.name}
                </li>
              ))}
              {hits.length === 0 ? (
                <li className="px-3 py-2 text-sm text-fg-muted">{t('savedviews.noResults')}</li>
              ) : null}
            </ul>
          </section>
        ) : null}
      </div>
    </Card>
  );
}

export function SavedViewsPage() {
  const { t, i18n } = useTranslation();
  const views = useSavedViews();
  const presets = useSavedViewPresets();
  const saveView = useSaveView();
  const clonePreset = useClonePreset();
  const updateView = useUpdateSavedView();
  const deleteView = useDeleteSavedView();
  // The outcome of the last write from the lists: a refusal (403, 404, 409)
  // as an alert, a clone as a status.
  const [message, setMessage] = useState<{ kind: 'error' | 'done'; text: string } | null>(null);
  const refused = (error: unknown) => setMessage({ kind: 'error', text: viewRefusal(error, t) });
  // entity_kind asset and asset.* of a CI need the page assets (WP-315).
  const { isEnabled } = useEntitlements();
  const { isPageEnabled } = useModules();
  const assetsEnabled = isEnabled(navFeatureFor.assets) && isPageEnabled('assets');
  const mayManage = useHasPermission('saved_view:manage') === true;
  const [editing, setEditing] = useState<Editing | null>(null);
  // Sharing with roles needs the role catalogue (role:read); without it
  // the page says so and the view can still be shared org-wide. The list
  // is not asked for without the right, so a reader sees no refused call.
  const mayReadRoles = useHasPermission('role:read') === true;
  const roles = useQuery({
    queryKey: ['roles', { limit: 200 }],
    queryFn: () => roleApi.list({ limit: 200 }),
    enabled: mayReadRoles,
  });
  // Only the owner changes or deletes a view (E-86), or a holder of
  // saved_view:manage (WP-307): for everybody else a view shared with the
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
  // Writable: an own view (E-86) or, with saved_view:manage, any view the
  // caller reads (WP-307); presets have no id. Until the caller's id is
  // known no view counts as own (fail closed).
  const writable = (view: SavedView | null | undefined) =>
    Boolean(view?.id && ((me && view.owner_id === me) || mayManage));
  const own = writable(runningView);

  const edit = (view?: SavedView, spec?: Record<string, unknown>, kind?: string, name = '') => {
    const { where, flat } = splitSpec(spec ?? {});
    setEditing({
      key: Date.now(),
      view,
      name,
      entityKind: kind === 'asset' ? 'asset' : 'ci',
      where,
      flat,
      fromRun: spec !== undefined,
    });
  };

  // entity_kind lives in its own column on the saved view, not inside
  // filter_spec, so it has to be folded back in before querying. Without this
  // an asset view silently queries the ci table and its asset-only predicates
  // (warranty, owner, ...) are dropped. A saved view brings its sort,
  // columns and sharing; a preset starts without them.
  const run = (name: string, spec: ActiveSpec, entityKind?: string, view?: SavedView) => {
    const effective: ActiveSpec = { ...spec, entity_kind: spec.entity_kind ?? entityKind ?? 'ci' };
    const next = presentationOf(view);
    setActiveView(name);
    // Running another view closes the filter editor.
    setEditing(null);
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

  // A system view is cloned by its key (from_preset, VIE-02); the server
  // takes over its filter, sort and columns.
  const clone = (preset: SavedViewPreset) => {
    const label = presetName(preset, i18n.language);
    const name = window.prompt(
      t('savedviews.clonePrompt'),
      t('savedviews.cloneName', { name: label }),
    );
    if (!name) return;
    setMessage(null);
    clonePreset.mutate(
      { key: preset.key, name },
      {
        onSuccess: () => setMessage({ kind: 'done', text: t('savedviews.cloned', { name }) }),
        onError: refused,
      },
    );
  };

  // A view shared with the caller is cloned as an own, unshared view.
  const cloneView = (view: SavedView) => {
    const name = window.prompt(
      t('savedviews.clonePrompt'),
      t('savedviews.cloneName', { name: view.name }),
    );
    if (!name) return;
    setMessage(null);
    saveView.mutate(
      {
        name,
        entity_kind: view.entity_kind,
        filter_spec: view.filter_spec,
        sort: view.sort ?? [],
        columns: view.columns ?? [],
        shared: false,
        shared_with_roles: [],
      },
      {
        onSuccess: () => setMessage({ kind: 'done', text: t('savedviews.cloned', { name }) }),
        onError: refused,
      },
    );
  };

  const removeView = (view: SavedView) => {
    setMessage(null);
    deleteView.mutate(view.id, {
      onSuccess: () => {
        if (runningView?.id === view.id) setRunningView(null);
      },
      onError: refused,
    });
  };

  const updateCurrent = () => {
    if (!runningView || !own) return;
    setMessage(null);
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
      { onSuccess: (updated) => setRunningView(updated as SavedView), onError: refused },
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
        actions={
          <Button icon={<Plus />} onClick={() => edit()}>
            {t('savedviews.editor.newView')}
          </Button>
        }
        title={t('savedviews.title', 'Gespeicherte Ansichten')}
        description={t(
          'savedviews.subtitle',
          'Systemansichten wie „Meine Geräte“, „Garantie läuft bald ab“ und „Server ohne Monitoring“ sowie eigene und geteilte Filter.',
        )}
      />
      {message ? (
        <p
          role={message.kind === 'error' ? 'alert' : 'status'}
          className={cn('text-sm', message.kind === 'error' ? 'text-danger' : 'text-fg-muted')}
        >
          {message.text}
        </p>
      ) : null}

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
        <div className="min-w-0 space-y-4 lg:col-span-1">
          <Card title={t('savedviews.presets', 'Systemansichten')}>
            {/* System views (VIE-02): named in the UI language, read-only,
                cloned into an own view; no edit and no delete. */}
            <ul className="-mx-2 space-y-0.5">
              {(presets.data?.data ?? []).map((p) => {
                const label = presetName(p, i18n.language);
                return (
                  <li key={p.key} className="flex items-center gap-1">
                    <button
                      type="button"
                      onClick={() => run(label, p.filter_spec as ActiveSpec, p.entity_kind)}
                      aria-current={activeView === label ? 'true' : undefined}
                      className={viewRowClasses(activeView === label)}
                    >
                      <span className="min-w-0 [overflow-wrap:anywhere]">{label}</span>
                      <Badge variant="neutral">{t(`rules.entityKinds.${p.entity_kind}`)}</Badge>
                      <Badge variant="neutral">{t('savedviews.readOnlyBadge')}</Badge>
                    </button>
                    <IconButton
                      size="sm"
                      icon={<Copy />}
                      label={t('savedviews.cloneLabel', { name: label })}
                      onClick={() => clone(p)}
                      disabled={clonePreset.isPending}
                      className="shrink-0"
                    />
                  </li>
                );
              })}
            </ul>
          </Card>
          <Card title={t('savedviews.mine', 'Meine und geteilte Ansichten')}>
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
                    {me && v.owner_id !== me ? (
                      <Badge variant="neutral">{t('savedviews.foreign')}</Badge>
                    ) : null}
                  </button>
                  {writable(v) ? (
                    <>
                      <IconButton
                        size="sm"
                        icon={<Pencil />}
                        label={t('savedviews.editLabel', { name: v.name })}
                        onClick={() => {
                          run(v.name, v.filter_spec as ActiveSpec, v.entity_kind, v);
                          edit(v, v.filter_spec, v.entity_kind, v.name);
                        }}
                        className="shrink-0"
                      />
                      <IconButton
                        size="sm"
                        icon={<X />}
                        label={`${t('common.delete')}: ${v.name}`}
                        onClick={() => removeView(v)}
                        className="shrink-0 hover:text-danger"
                      />
                    </>
                  ) : (
                    <IconButton
                      size="sm"
                      icon={<Copy />}
                      label={t('savedviews.cloneLabel', { name: v.name })}
                      onClick={() => cloneView(v)}
                      disabled={saveView.isPending}
                      className="shrink-0"
                    />
                  )}
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
          {editing ? (
            <FilterEditor
              key={editing.key}
              editing={editing}
              writable={writable(editing.view)}
              presentation={editing.fromRun ? presentation : NO_PRESENTATION}
              assetsEnabled={assetsEnabled}
              onCancel={() => setEditing(null)}
              onSaved={(view) => {
                setEditing(null);
                run(view.name, view.filter_spec as ActiveSpec, view.entity_kind, view);
              }}
            />
          ) : null}
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
                <div className="flex flex-wrap gap-2">
                  <Button
                    variant="secondary"
                    icon={<Filter />}
                    onClick={() => {
                      const { entity_kind: kind, ...spec } = filter;
                      edit(runningView ?? undefined, spec, kind, activeView);
                    }}
                  >
                    {t('savedviews.editor.editFilter')}
                  </Button>
                  <Button
                    variant="secondary"
                    icon={<Bookmark />}
                    onClick={saveCurrent}
                    disabled={saveView.isPending}
                  >
                    {t('savedviews.save', 'Als Ansicht speichern')}
                  </Button>
                </div>
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
