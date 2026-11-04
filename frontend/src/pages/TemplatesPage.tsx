/**
 * TemplatesPage administers the templates of MET-16 (UI-11, WP-314): the
 * list of GET /api/v1/templates (own and read-only system templates, all
 * pages of 200 over next_cursor, filter ci_type_id on the server and
 * active/inactive in the page), creating, editing and deactivating own
 * templates (POST/PATCH), cloning every template, and per template its
 * versions: "new version" as a draft copy, the TemplateVersionEditor,
 * "publish version" (direct publishing of MET-04/MET-53, only with
 * metamodel:publish) and the comparison of two versions with
 * MetamodelDiff. With an active metamodel draft (WP-798) every write is
 * captured in the draft; the page marks its targets "in draft" and shows
 * new templates and versions as placeholders (E-113). Templates are no type templates of GET
 * /ci-types/templates ("Typvorlagen"). The page belongs to the module
 * metamodel (E-105); switched off, it shows a note instead.
 */
import { useMemo, useState, type FormEvent } from 'react';
import { Link, useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import {
  isDraftItem,
  localizedText,
  type CIType,
  type Template,
  type TemplateCreate,
  type TemplatePatch,
  type TemplateVersion,
} from '../api/cmdb';
import {
  useActiveDraftItems,
  useAllCITypes,
  useCloneTemplate,
  useCreateTemplate,
  useCreateTemplateVersion,
  usePublishTemplateVersion,
  useTemplate,
  useTemplates,
  useTemplateVersions,
  useUpdateTemplate,
} from '../api/cmdbHooks';
import { useHasPermission } from '../api/hooks';
import { ApiError } from '../api/client';
import { useModules } from '../hooks/useEntitlements';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { MetamodelTabs } from '../components/metamodel/MetamodelTabs';
import {
  MetamodelDiff,
  formatDiffValue,
  type DiffEntry,
} from '../components/metamodel/MetamodelDiff';
import { TemplateVersionEditor, rulesOf } from '../components/metamodel/TemplateVersionEditor';
import { ancestorsOf } from '../components/metamodel/TypeTree';
import { describeFlagRefusal, fieldErrors } from '../components/metamodel/AttributeEditor';
import {
  DraftMarker,
  draftChangeFor,
  draftCreations,
  draftItemName,
} from '../components/metamodel/DraftBanner';
import { useMetamodelDraftStore } from '../stores/metamodelDraft';

/** The states of a template version (MET-16); labels templates.versionStatuses.*. */
export const TEMPLATE_VERSION_STATUSES = ['draft', 'published', 'retired'] as const;

/** The activity filter of the list (in the page, not on the server). */
const ACTIVITY_FILTERS = ['all', 'active', 'inactive'] as const;
type ActivityFilter = (typeof ACTIVITY_FILTERS)[number];

const notFound = (error: unknown) => error instanceof ApiError && error.status === 404;

function templateName(template: Template, language: string): string {
  return localizedText(template.display_name, language) || template.key;
}

export function TemplatesPage() {
  const { t } = useTranslation();
  const { id } = useParams<{ id: string }>();
  const modules = useModules();
  const moduleOff = !modules.isLoading && !modules.isModuleEnabled('metamodel');

  return (
    <div className="space-y-4">
      <div>
        <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">
          {t('citypes.title')}
        </h2>
        <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">{t('templates.subtitle')}</p>
      </div>
      <MetamodelTabs />
      {moduleOff ? (
        <p className="text-sm text-gray-600 dark:text-gray-300">{t('templates.moduleOff')}</p>
      ) : id ? (
        <TemplateDetail id={id} />
      ) : (
        <TemplateList />
      )}
    </div>
  );
}

/** useTypes reads every CI type and names one in the UI language. */
function useTypes() {
  const { i18n } = useTranslation();
  const query = useAllCITypes({ include_inactive: true });
  const types = useMemo(() => query.data ?? [], [query.data]);
  const nameOf = (id: string) => {
    const ty = types.find((x) => x.id === id);
    return ty ? localizedText(ty.display_name, i18n.language) || ty.name : id;
  };
  return { types, nameOf };
}

type Dialog =
  { mode: 'create' } | { mode: 'edit'; template: Template } | { mode: 'clone'; template: Template };

function TemplateList() {
  const { t, i18n } = useTranslation();
  const [ciTypeId, setCITypeId] = useState('');
  const [activity, setActivity] = useState<ActivityFilter>('all');
  const [dialog, setDialog] = useState<Dialog | null>(null);
  const canManage = useHasPermission('citype:manage') === true;
  const query = useTemplates(ciTypeId ? { ci_type_id: ciTypeId } : {});
  const update = useUpdateTemplate();
  const { types, nameOf } = useTypes();
  const draftItems = useActiveDraftItems();
  const newTemplates = draftCreations(draftItems, 'template');

  const rows = (query.data?.data ?? []).filter((tpl) =>
    activity === 'all' ? true : activity === 'active' ? tpl.is_active : !tpl.is_active,
  );

  if (notFound(query.error)) {
    return <p className="text-sm text-gray-600 dark:text-gray-300">{t('templates.moduleOff')}</p>;
  }

  return (
    <div className="space-y-4">
      <Card>
        <div className="flex flex-wrap items-end gap-3">
          <div className="min-w-[12rem]">
            <Select
              label={t('templates.filterType')}
              value={ciTypeId}
              onChange={(e) => setCITypeId(e.target.value)}
              options={[
                { value: '', label: t('common.all') },
                ...types.map((ty) => ({ value: ty.id, label: nameOf(ty.id) })),
              ]}
            />
          </div>
          <div className="min-w-[10rem]">
            <Select
              label={t('templates.filterActivity')}
              value={activity}
              onChange={(e) => setActivity(e.target.value as ActivityFilter)}
              options={ACTIVITY_FILTERS.map((value) => ({
                value,
                label: t(`templates.activity.${value}`),
              }))}
            />
          </div>
          <div className="flex-1" />
          {canManage ? (
            <Button size="sm" onClick={() => setDialog({ mode: 'create' })}>
              {t('templates.create')}
            </Button>
          ) : null}
        </div>
      </Card>

      <p className="text-sm text-gray-600 dark:text-gray-300">{t('templates.hint')}</p>

      {query.isLoading ? <SkeletonList rows={3} label={t('app.loading')} /> : null}
      {query.error ? (
        <ErrorState
          title={t('app.error')}
          retryLabel={t('common.retry')}
          onRetry={() => void query.refetch()}
        />
      ) : null}
      {update.error ? (
        <p role="alert" className="text-sm text-red-600 dark:text-red-400">
          {t('templates.notSaved', { reason: describeFlagRefusal(update.error, t) })}
        </p>
      ) : null}
      {query.isSuccess && rows.length === 0 && newTemplates.length === 0 ? (
        <p className="text-sm text-muted">{t('templates.empty')}</p>
      ) : null}
      {newTemplates.length > 0 ? (
        <Card title={t('metamodelDrafts.newParts')}>
          <ul className="divide-y divide-gray-100 dark:divide-gray-800">
            {newTemplates.map((item) => (
              <li
                key={item.id}
                data-draft-item={item.id}
                className="flex flex-wrap items-center gap-2 py-2 text-sm"
              >
                <span className="font-medium">
                  {draftItemName(item, i18n.language) || t('metamodelDrafts.noTarget')}
                </span>
                <DraftMarker change="created" />
              </li>
            ))}
          </ul>
        </Card>
      ) : null}
      {rows.length > 0 ? (
        <Card>
          <ul
            aria-label={t('templates.listLabel')}
            className="divide-y divide-gray-100 dark:divide-gray-800"
          >
            {rows.map((tpl) => {
              const name = templateName(tpl, i18n.language);
              const own = !tpl.is_system;
              const change = draftChangeFor(draftItems, {
                kinds: ['template', 'template_version'],
                id: tpl.id,
              });
              return (
                <li
                  key={tpl.id}
                  className="flex flex-wrap items-center justify-between gap-3 py-3 text-sm"
                >
                  <div className="min-w-0 space-y-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <Link
                        to={`/ci-types/templates/${tpl.id}`}
                        className="font-medium text-primary hover:underline"
                      >
                        {name}
                      </Link>
                      <span className="text-xs text-muted">{tpl.key}</span>
                      {tpl.is_system ? <Badge variant="info">{t('templates.system')}</Badge> : null}
                      {!tpl.is_active ? (
                        <Badge variant="neutral">{t('templates.inactive')}</Badge>
                      ) : null}
                      {change ? <DraftMarker change={change} /> : null}
                    </div>
                    <div className="text-xs text-muted">
                      {t('templates.typeAndVersion', {
                        type: nameOf(tpl.ci_type_id),
                        version:
                          tpl.current_version != null
                            ? t('templates.versionShort', { version: tpl.current_version })
                            : t('templates.unpublished'),
                      })}
                    </div>
                  </div>
                  {canManage ? (
                    <div className="flex flex-wrap gap-3">
                      {own ? (
                        <>
                          <button
                            type="button"
                            className="text-xs text-primary hover:underline"
                            aria-label={t('templates.editAction', { name })}
                            onClick={() => setDialog({ mode: 'edit', template: tpl })}
                          >
                            {t('common.edit')}
                          </button>
                          <button
                            type="button"
                            className="text-xs text-primary hover:underline"
                            aria-label={t(
                              tpl.is_active
                                ? 'templates.deactivateAction'
                                : 'templates.activateAction',
                              { name },
                            )}
                            disabled={update.isPending}
                            onClick={() =>
                              update.mutate({ id: tpl.id, patch: { is_active: !tpl.is_active } })
                            }
                          >
                            {tpl.is_active ? t('templates.deactivate') : t('templates.activate')}
                          </button>
                        </>
                      ) : null}
                      <button
                        type="button"
                        className="text-xs text-primary hover:underline"
                        aria-label={t('templates.cloneAction', { name })}
                        onClick={() => setDialog({ mode: 'clone', template: tpl })}
                      >
                        {t('templates.clone')}
                      </button>
                    </div>
                  ) : null}
                </li>
              );
            })}
          </ul>
        </Card>
      ) : null}
      {dialog ? (
        <TemplateDialog
          dialog={dialog}
          types={types}
          typeName={nameOf}
          onClose={() => setDialog(null)}
        />
      ) : null}
    </div>
  );
}

/** The dialog to create, edit or clone a template. */
function TemplateDialog({
  dialog,
  types,
  typeName,
  onClose,
}: {
  dialog: Dialog;
  types: CIType[];
  typeName: (id: string) => string;
  onClose: () => void;
}) {
  const { t, i18n } = useTranslation();
  const source = dialog.mode === 'create' ? undefined : dialog.template;
  const [key, setKey] = useState(dialog.mode === 'edit' ? dialog.template.key : '');
  const [ciTypeId, setCITypeId] = useState(source?.ci_type_id ?? '');
  const [de, setDe] = useState(source?.display_name?.de ?? '');
  const [en, setEn] = useState(source?.display_name?.en ?? '');
  const [description, setDescription] = useState(source?.description ?? '');
  const [active, setActive] = useState(source?.is_active ?? true);
  const create = useCreateTemplate();
  const update = useUpdateTemplate();
  const clone = useCloneTemplate();
  const mutation = dialog.mode === 'create' ? create : dialog.mode === 'edit' ? update : clone;
  const errors = fieldErrors(mutation.error);

  const displayName = () => {
    const out: Record<string, string> = {};
    if (de.trim()) out.de = de.trim();
    if (en.trim()) out.en = en.trim();
    return out;
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const done = { onSuccess: onClose };
    if (dialog.mode === 'create') {
      const data: TemplateCreate = {
        key: key.trim(),
        ci_type_id: ciTypeId,
        display_name: displayName(),
        description,
        is_active: true,
      };
      create.mutate(data, done);
    } else if (dialog.mode === 'edit') {
      const patch: TemplatePatch = { display_name: displayName(), description, is_active: active };
      update.mutate({ id: dialog.template.id, patch }, done);
    } else {
      clone.mutate(
        { id: dialog.template.id, key: key.trim(), display_name: displayName(), description },
        done,
      );
    }
  };

  const title =
    dialog.mode === 'create'
      ? t('templates.createTitle')
      : dialog.mode === 'edit'
        ? t('templates.editTitle', { name: templateName(dialog.template, i18n.language) })
        : t('templates.cloneTitle', { name: templateName(dialog.template, i18n.language) });
  const otherErrors = Object.entries(errors).filter(
    ([field]) => !['key', 'ci_type_id', 'display_name', 'description'].includes(field),
  );

  return (
    <Modal open onOpenChange={(open) => (open ? undefined : onClose())} title={title}>
      <form onSubmit={submit} className="space-y-3">
        <Input
          label={t('templates.key')}
          value={key}
          required
          disabled={dialog.mode === 'edit'}
          error={errors.key}
          onChange={(e) => setKey(e.target.value)}
        />
        {dialog.mode === 'edit' ? (
          <p className="text-xs text-muted">{t('templates.immutable')}</p>
        ) : null}
        {dialog.mode === 'create' ? (
          <Select
            label={t('templates.ciType')}
            value={ciTypeId}
            required
            error={errors.ci_type_id}
            onChange={(e) => setCITypeId(e.target.value)}
            options={[
              { value: '', label: t('templates.chooseType') },
              ...types.map((ty) => ({ value: ty.id, label: typeName(ty.id) })),
            ]}
          />
        ) : (
          <p className="text-sm">
            {t('templates.typeIs', { type: typeName(source?.ci_type_id ?? '') })}
          </p>
        )}
        <Input
          label={t('citypes.labelDe')}
          value={de}
          error={errors['display_name.de'] ?? errors.display_name}
          onChange={(e) => setDe(e.target.value)}
        />
        <Input
          label={t('citypes.labelEn')}
          value={en}
          error={errors['display_name.en']}
          onChange={(e) => setEn(e.target.value)}
        />
        <Input
          label={t('common.description')}
          value={description}
          error={errors.description}
          onChange={(e) => setDescription(e.target.value)}
        />
        {dialog.mode === 'edit' ? (
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={active} onChange={(e) => setActive(e.target.checked)} />
            {t('templates.active')}
          </label>
        ) : null}
        {mutation.error && (otherErrors.length > 0 || Object.keys(errors).length === 0) ? (
          <p role="alert" className="text-sm text-red-600 dark:text-red-400">
            {t('templates.notSaved', { reason: describeFlagRefusal(mutation.error, t) })}
          </p>
        ) : null}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button
            type="submit"
            disabled={mutation.isPending || !key.trim() || (dialog.mode === 'create' && !ciTypeId)}
          >
            {dialog.mode === 'clone' ? t('templates.clone') : t('common.save')}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

/** The publish refusal: a composition conflict (422) names the attribute key as field. */
function publishErrors(error: unknown): Record<string, string> {
  if (!(error instanceof ApiError) || error.status !== 422) return {};
  const out: Record<string, string> = {};
  for (const v of error.violations) if (v.field) out[v.field] = v.detail;
  if (error.field && !out[error.field]) {
    const raw = error.detail || error.message;
    out[error.field] = raw.startsWith(`${error.field}: `) ? raw.slice(error.field.length + 2) : raw;
  }
  return out;
}

function diffEntriesOf(version: TemplateVersion | undefined): DiffEntry[] {
  return (version?.items ?? []).map((item) => ({
    key: item.attribute_definition_id,
    label: item.key || item.attribute_definition_id,
    values: {
      position: item.position,
      required_override: item.required_override || undefined,
      default_override: item.default_override,
      validation_override: item.validation_override ? rulesOf(item.validation_override) : undefined,
    },
  }));
}

function TemplateDetail({ id }: { id: string }) {
  const { t, i18n } = useTranslation();
  const template = useTemplate(id);
  const versions = useTemplateVersions(id);
  const canManage = useHasPermission('citype:manage') === true;
  const canPublish = useHasPermission('metamodel:publish') === true;
  const createVersion = useCreateTemplateVersion();
  const publish = usePublishTemplateVersion();
  const { types, nameOf } = useTypes();
  const draft = useMetamodelDraftStore((s) => s.draft);
  const draftItems = useActiveDraftItems();
  const [selected, setSelected] = useState<number | undefined>();
  const [confirm, setConfirm] = useState<'version' | 'publish' | null>(null);
  const [compare, setCompare] = useState<{ from?: number; to?: number }>({});

  const list = useMemo(
    () => [...(versions.data?.data ?? [])].sort((a, b) => b.version - a.version),
    [versions.data],
  );

  if (notFound(template.error)) {
    return (
      <div className="space-y-2">
        <p className="text-sm text-gray-600 dark:text-gray-300">{t('templates.notFound')}</p>
        <Link to="/ci-types/templates" className="text-sm text-primary hover:underline">
          {t('templates.back')}
        </Link>
      </div>
    );
  }
  if (template.isLoading) return <SkeletonList rows={3} label={t('app.loading')} />;
  if (template.error || !template.data) {
    return (
      <ErrorState
        title={t('app.error')}
        retryLabel={t('common.retry')}
        onRetry={() => void template.refetch()}
      />
    );
  }

  const tpl = template.data;
  const name = templateName(tpl, i18n.language);
  const version = list.find((v) => v.version === selected) ?? list[0];
  const typeChain = [tpl.ci_type_id, ...ancestorsOf(tpl.ci_type_id, types).map((ty) => ty.id)];
  const fromVersion = compare.from ?? list[1]?.version;
  const toVersion = compare.to ?? list[0]?.version;
  const versionOptions = list.map((v) => ({
    value: String(v.version),
    label: t('templates.versionOption', {
      version: v.version,
      status: t(`templates.versionStatuses.${v.status}`),
    }),
  }));
  const itemErrors =
    publish.variables?.version === version?.version ? publishErrors(publish.error) : {};
  const publishGeneral =
    publish.error && Object.keys(publishErrors(publish.error)).length === 0
      ? describeFlagRefusal(publish.error, t)
      : null;
  const own = !tpl.is_system;
  const templateChange = draftChangeFor(draftItems, {
    kinds: ['template', 'template_version'],
    id: tpl.id,
  });
  const newVersions = draftItems.filter(
    (item) => item.kind === 'template_version' && item.op === 'create' && item.target_id === tpl.id,
  );

  return (
    <div className="space-y-4">
      <Link to="/ci-types/templates" className="text-sm text-primary hover:underline">
        {t('templates.back')}
      </Link>
      <Card>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="space-y-1">
            <h3 className="flex flex-wrap items-center gap-2 text-lg font-semibold text-gray-900 dark:text-gray-100">
              {name}
              {tpl.is_system ? <Badge variant="info">{t('templates.system')}</Badge> : null}
              {!tpl.is_active ? <Badge variant="neutral">{t('templates.inactive')}</Badge> : null}
              {templateChange ? <DraftMarker change={templateChange} /> : null}
            </h3>
            <p className="text-xs text-muted">
              {t('templates.detailMeta', {
                key: tpl.key,
                type: nameOf(tpl.ci_type_id),
                version:
                  tpl.current_version != null
                    ? t('templates.versionShort', { version: tpl.current_version })
                    : t('templates.unpublished'),
              })}
            </p>
            {tpl.description ? (
              <p className="text-sm text-gray-700 dark:text-gray-300">{tpl.description}</p>
            ) : null}
            {tpl.is_system ? (
              <p className="text-sm text-gray-600 dark:text-gray-300">
                {t('templates.systemReadOnly')}
              </p>
            ) : null}
          </div>
          {own && canManage ? (
            <Button
              size="sm"
              variant="secondary"
              onClick={() => {
                createVersion.reset();
                setConfirm('version');
              }}
            >
              {t('templates.newVersion')}
            </Button>
          ) : null}
        </div>
      </Card>

      <Card>
        <h3 className="text-sm font-semibold text-gray-900 dark:text-gray-100">
          {t('templates.versions')}
        </h3>
        {versions.isLoading ? <SkeletonList rows={2} label={t('app.loading')} /> : null}
        {versions.error ? (
          <ErrorState
            title={t('app.error')}
            retryLabel={t('common.retry')}
            onRetry={() => void versions.refetch()}
          />
        ) : null}
        {versions.isSuccess && list.length === 0 && newVersions.length === 0 ? (
          <p className="mt-2 text-sm text-muted">{t('templates.noVersions')}</p>
        ) : null}
        {list.length > 0 ? (
          <ul aria-label={t('templates.versions')} className="mt-2 flex flex-wrap gap-2">
            {list.map((v) => (
              <li key={v.id}>
                <button
                  type="button"
                  aria-pressed={v.version === version?.version}
                  onClick={() => setSelected(v.version)}
                  className={`flex items-center gap-2 rounded-lg border px-3 py-1.5 text-sm focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary ${
                    v.version === version?.version
                      ? 'border-primary'
                      : 'border-gray-200 dark:border-gray-700'
                  }`}
                >
                  {t('templates.versionShort', { version: v.version })}
                  <Badge
                    variant={
                      v.status === 'draft'
                        ? 'info'
                        : v.status === 'published'
                          ? 'success'
                          : 'neutral'
                    }
                  >
                    {t(`templates.versionStatuses.${v.status}`)}
                  </Badge>
                  <VersionDraftMarker
                    change={draftChangeFor(draftItems, { kinds: ['template_version'], id: v.id })}
                  />
                </button>
              </li>
            ))}
          </ul>
        ) : null}
        {newVersions.length > 0 ? (
          <ul className="mt-2 flex flex-wrap gap-2">
            {newVersions.map((item) => (
              <li
                key={item.id}
                data-draft-item={item.id}
                className="flex items-center gap-2 rounded-lg border border-dashed border-gray-300 px-3 py-1.5 text-sm dark:border-gray-600"
              >
                {t('metamodelDrafts.newVersion')}
                <DraftMarker change="created" />
              </li>
            ))}
          </ul>
        ) : null}
      </Card>

      {version ? (
        <div className="space-y-2">
          {version.status === 'draft' && own ? (
            <div className="flex flex-wrap items-center gap-2">
              <Button
                size="sm"
                disabled={!canPublish || publish.isPending}
                onClick={() => {
                  publish.reset();
                  setConfirm('publish');
                }}
              >
                {t('templates.publish', { version: version.version })}
              </Button>
              {!canPublish ? (
                <span className="text-xs text-muted">{t('templates.publishNeedsPermission')}</span>
              ) : null}
            </div>
          ) : null}
          {publishGeneral ? (
            <p role="alert" className="text-sm text-red-600 dark:text-red-400">
              {t('templates.publishFailed', { reason: publishGeneral })}
            </p>
          ) : null}
          {Object.keys(itemErrors).length > 0 ? (
            <p role="alert" className="text-sm text-red-600 dark:text-red-400">
              {t('templates.publishConflict')}
            </p>
          ) : null}
          <TemplateVersionEditor
            key={version.id}
            template={tpl}
            version={version}
            typeChain={typeChain}
            canManage={canManage}
            itemErrors={itemErrors}
          />
        </div>
      ) : null}

      {list.length > 1 ? (
        <Card>
          <h3 className="text-sm font-semibold text-gray-900 dark:text-gray-100">
            {t('templates.compare')}
          </h3>
          <div className="mt-2 flex flex-wrap gap-3">
            <div className="min-w-[10rem]">
              <Select
                label={t('templates.compareFrom')}
                value={String(fromVersion ?? '')}
                onChange={(e) => setCompare((c) => ({ ...c, from: Number(e.target.value) }))}
                options={versionOptions}
              />
            </div>
            <div className="min-w-[10rem]">
              <Select
                label={t('templates.compareTo')}
                value={String(toVersion ?? '')}
                onChange={(e) => setCompare((c) => ({ ...c, to: Number(e.target.value) }))}
                options={versionOptions}
              />
            </div>
          </div>
          <div className="mt-3">
            <MetamodelDiff
              caption={t('templates.compareCaption', { from: fromVersion, to: toVersion })}
              before={diffEntriesOf(list.find((v) => v.version === fromVersion))}
              after={diffEntriesOf(list.find((v) => v.version === toVersion))}
              fieldLabel={(field) => t(`templates.diff.fields.${field}`, field)}
              formatValue={(field, value) =>
                field === 'required_override'
                  ? value
                    ? t('templates.diff.yes')
                    : t('templates.diff.no')
                  : field === 'position' && typeof value === 'number'
                    ? String(value + 1)
                    : formatDiffValue(value)
              }
            />
          </div>
        </Card>
      ) : null}

      <Modal
        open={confirm === 'version'}
        onOpenChange={(open) => (open ? undefined : setConfirm(null))}
        title={t('templates.newVersionTitle')}
        description={t('templates.newVersionText', {
          version:
            tpl.current_version != null
              ? t('templates.versionShort', { version: tpl.current_version })
              : t('templates.highestVersion'),
        })}
      >
        {createVersion.error ? (
          <p role="alert" className="text-sm text-red-600 dark:text-red-400">
            {t('templates.notSaved', { reason: describeFlagRefusal(createVersion.error, t) })}
          </p>
        ) : null}
        <div className="flex justify-end gap-2">
          <Button variant="ghost" onClick={() => setConfirm(null)}>
            {t('common.cancel')}
          </Button>
          <Button
            disabled={createVersion.isPending}
            onClick={() =>
              createVersion.mutate(tpl.id, {
                onSuccess: (created) => {
                  // Captured in the draft (202): the item stands for the version.
                  if (!isDraftItem(created)) setSelected(created.version);
                  setConfirm(null);
                },
              })
            }
          >
            {t('templates.newVersionConfirm')}
          </Button>
        </div>
      </Modal>

      <Modal
        open={confirm === 'publish' && !!version}
        onOpenChange={(open) => (open ? undefined : setConfirm(null))}
        title={t('templates.publishTitle', { version: version?.version })}
        description={
          draft
            ? t('templates.publishDraftText', { draft: draft.name })
            : t('templates.publishText', { name })
        }
      >
        <div className="flex justify-end gap-2">
          <Button variant="ghost" onClick={() => setConfirm(null)}>
            {t('common.cancel')}
          </Button>
          <Button
            disabled={!canPublish || publish.isPending}
            onClick={() => {
              if (!version) return;
              setConfirm(null);
              publish.mutate({ id: tpl.id, version: version.version });
            }}
          >
            {t('templates.publishConfirm')}
          </Button>
        </div>
      </Modal>
    </div>
  );
}

/** VersionDraftMarker marks a version a draft changes or publishes. */
function VersionDraftMarker({ change }: { change: ReturnType<typeof draftChangeFor> }) {
  return change ? <DraftMarker change={change} /> : null;
}
