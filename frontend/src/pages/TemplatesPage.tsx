/**
 * TemplatesPage administers the templates of MET-16 (UI-11, WP-314): the
 * list of GET /api/v1/templates (own and read-only system templates, all
 * pages of 200 over next_cursor, filter ci_type_id on the server and
 * active/inactive in the page), creating, editing and deactivating own
 * templates (POST/PATCH), cloning every template, and per template its
 * versions: "new version" as a draft copy, the TemplateVersionEditor,
 * "publish version" (direct publishing of MET-04/MET-53, only with
 * metamodel:publish) and the comparison of two versions with
 * MetamodelDiff. Per template with a published version, "migrate" opens
 * the MigrateDialog (job template.migrate, WP-800). With an active
 * metamodel draft (WP-798) every write is captured in the draft; the page marks its targets "in draft" and shows
 * new templates and versions as placeholders (E-113). Templates are no type templates of GET
 * /ci-types/templates ("Typvorlagen"). The page belongs to the module
 * metamodel (E-105); switched off, it shows a note instead.
 */
import {
  Copy,
  FileText,
  GitCompare,
  LayoutTemplate,
  Pencil,
  Plus,
  Power,
  PowerOff,
  Rocket,
} from 'lucide-react';
import { useMemo, useState, type FormEvent, type ReactNode } from 'react';
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
import { Button, IconButton } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import { Skeleton, SkeletonList } from '../components/ui/Skeleton';
import { Toolbar, ToolbarEnd } from '../components/ui/Toolbar';
import { cn } from '../components/ui/utils';
import { MetamodelTabs } from '../components/metamodel/MetamodelTabs';
import {
  MetamodelDiff,
  formatDiffValue,
  type DiffEntry,
} from '../components/metamodel/MetamodelDiff';
import {
  TemplateVersionEditor,
  VERSION_STATUS_VARIANT,
  rulesOf,
} from '../components/metamodel/TemplateVersionEditor';
import { ancestorsOf } from '../components/metamodel/TypeTree';
import { describeFlagRefusal, fieldErrors } from '../components/metamodel/AttributeEditor';
import {
  DraftMarker,
  draftChangeFor,
  draftCreations,
  draftItemName,
} from '../components/metamodel/DraftBanner';
import { useMetamodelDraftStore } from '../stores/metamodelDraft';
import { MigrateDialog } from '../components/metamodel/MigrateDialog';

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

  if (moduleOff) {
    return (
      <div className="space-y-6">
        <TemplatesHeader />
        <MetamodelTabs />
        <EmptyState icon={<LayoutTemplate />} title={t('templates.moduleOff')} />
      </div>
    );
  }
  return id ? <TemplateDetail id={id} /> : <TemplateList />;
}

/** TemplatesHeader is the heading the three metamodel pages share (title, sentence, actions). */
function TemplatesHeader({ actions }: { actions?: ReactNode }) {
  const { t } = useTranslation();
  return (
    <PageHeader
      title={t('citypes.title')}
      description={t('templates.subtitle')}
      actions={actions}
    />
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
  const [migrating, setMigrating] = useState<Template | null>(null);
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
    return (
      <div className="space-y-6">
        <TemplatesHeader />
        <MetamodelTabs />
        <EmptyState icon={<LayoutTemplate />} title={t('templates.moduleOff')} />
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <TemplatesHeader
        actions={
          canManage ? (
            <Button icon={<Plus />} onClick={() => setDialog({ mode: 'create' })}>
              {t('templates.create')}
            </Button>
          ) : null
        }
      />
      <MetamodelTabs />

      <div className="space-y-4">
        <p className="max-w-[68ch] text-sm text-fg-muted">{t('templates.hint')}</p>

        <Toolbar className="items-end">
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
          {query.isSuccess ? (
            <ToolbarEnd>
              <span className="text-sm tabular-nums text-fg-muted">
                {t('templates.count', { count: rows.length })}
              </span>
            </ToolbarEnd>
          ) : null}
        </Toolbar>

        {query.isLoading ? <SkeletonList rows={3} label={t('app.loading')} /> : null}
        {query.error ? (
          <ErrorState
            title={t('app.error')}
            retryLabel={t('common.retry')}
            onRetry={() => void query.refetch()}
          />
        ) : null}
        {update.error ? (
          <p role="alert" className="text-sm text-danger">
            {t('templates.notSaved', { reason: describeFlagRefusal(update.error, t) })}
          </p>
        ) : null}
        {query.isSuccess && rows.length === 0 && newTemplates.length === 0 ? (
          <EmptyState
            icon={<LayoutTemplate />}
            title={t('templates.empty')}
            action={
              canManage ? (
                <Button
                  variant="secondary"
                  icon={<Plus />}
                  onClick={() => setDialog({ mode: 'create' })}
                >
                  {t('templates.create')}
                </Button>
              ) : undefined
            }
          />
        ) : null}
        {newTemplates.length > 0 ? (
          <Card title={t('metamodelDrafts.newParts')}>
            <ul className="divide-y divide-line">
              {newTemplates.map((item) => (
                <li
                  key={item.id}
                  data-draft-item={item.id}
                  className="flex flex-wrap items-center gap-2 py-3 text-sm"
                >
                  <span className="font-medium text-fg">
                    {draftItemName(item, i18n.language) || t('metamodelDrafts.noTarget')}
                  </span>
                  <DraftMarker change="created" />
                </li>
              ))}
            </ul>
          </Card>
        ) : null}
        {rows.length > 0 ? (
          <Card className="p-0 overflow-hidden">
            <ul aria-label={t('templates.listLabel')} className="divide-y divide-line">
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
                    className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 px-4 py-3 text-sm transition-colors hover:bg-fg/[0.025]"
                  >
                    <div className="min-w-0 flex-1 basis-60 space-y-1">
                      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                        <Link
                          to={`/ci-types/templates/${tpl.id}`}
                          className="rounded font-medium text-accent underline-offset-2 hover:underline"
                        >
                          {name}
                        </Link>
                        <span className="font-mono text-xs text-fg-muted">{tpl.key}</span>
                        {tpl.is_system ? (
                          <Badge variant="info">{t('templates.system')}</Badge>
                        ) : null}
                        {!tpl.is_active ? (
                          <Badge variant="neutral">{t('templates.inactive')}</Badge>
                        ) : null}
                        {change ? <DraftMarker change={change} /> : null}
                      </div>
                      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-fg-muted">
                        <span>{nameOf(tpl.ci_type_id)}</span>
                        {tpl.current_version != null ? (
                          <Badge variant="success">
                            {t('templates.versionShort', { version: tpl.current_version })}
                          </Badge>
                        ) : (
                          <Badge variant="neutral">{t('templates.unpublished')}</Badge>
                        )}
                      </div>
                    </div>
                    {canManage ? (
                      <div className="flex flex-wrap items-center gap-1">
                        {tpl.current_version != null ? (
                          <Button
                            variant="ghost"
                            size="sm"
                            icon={<Rocket />}
                            aria-label={t('templates.migrate.action', { name })}
                            onClick={() => setMigrating(tpl)}
                          >
                            {t('templates.migrate.open')}
                          </Button>
                        ) : null}
                        {own ? (
                          <>
                            <IconButton
                              size="sm"
                              icon={<Pencil />}
                              label={t('templates.editAction', { name })}
                              onClick={() => setDialog({ mode: 'edit', template: tpl })}
                            />
                            <IconButton
                              size="sm"
                              icon={tpl.is_active ? <PowerOff /> : <Power />}
                              label={t(
                                tpl.is_active
                                  ? 'templates.deactivateAction'
                                  : 'templates.activateAction',
                                { name },
                              )}
                              disabled={update.isPending}
                              onClick={() =>
                                update.mutate({ id: tpl.id, patch: { is_active: !tpl.is_active } })
                              }
                            />
                          </>
                        ) : null}
                        <IconButton
                          size="sm"
                          icon={<Copy />}
                          label={t('templates.cloneAction', { name })}
                          onClick={() => setDialog({ mode: 'clone', template: tpl })}
                        />
                      </div>
                    ) : null}
                  </li>
                );
              })}
            </ul>
          </Card>
        ) : null}
      </div>
      {migrating ? (
        <MigrateDialog open template={migrating} onClose={() => setMigrating(null)} />
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
          <p className="text-xs text-fg-muted">{t('templates.immutable')}</p>
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
          <p className="text-sm text-fg-muted">
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
          <label className="flex items-center gap-2 text-sm text-fg">
            <input type="checkbox" checked={active} onChange={(e) => setActive(e.target.checked)} />
            {t('templates.active')}
          </label>
        ) : null}
        {mutation.error && (otherErrors.length > 0 || Object.keys(errors).length === 0) ? (
          <p role="alert" className="text-sm text-danger">
            {t('templates.notSaved', { reason: describeFlagRefusal(mutation.error, t) })}
          </p>
        ) : null}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
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
  const [confirm, setConfirm] = useState<'version' | 'publish' | 'migrate' | null>(null);
  const [compare, setCompare] = useState<{ from?: number; to?: number }>({});

  const list = useMemo(
    () => [...(versions.data?.data ?? [])].sort((a, b) => b.version - a.version),
    [versions.data],
  );

  if (notFound(template.error)) {
    return (
      <div className="space-y-6">
        <TemplatesHeader />
        <MetamodelTabs />
        <EmptyState
          icon={<FileText />}
          title={t('templates.notFound')}
          action={
            <Button variant="secondary" asChild>
              <Link to="/ci-types/templates">{t('templates.back')}</Link>
            </Button>
          }
        />
      </div>
    );
  }
  if (template.isLoading) {
    return (
      <div className="space-y-6">
        <Skeleton className="h-16 w-full max-w-lg" />
        <MetamodelTabs />
        <SkeletonList rows={3} label={t('app.loading')} />
      </div>
    );
  }
  if (template.error || !template.data) {
    return (
      <div className="space-y-6">
        <TemplatesHeader />
        <MetamodelTabs />
        <ErrorState
          title={t('app.error')}
          retryLabel={t('common.retry')}
          onRetry={() => void template.refetch()}
        />
      </div>
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
    <div className="space-y-6">
      <PageHeader
        breadcrumbs={[
          { label: t('citypes.title'), to: '/ci-types' },
          { label: t('templates.tabs.templates'), to: '/ci-types/templates' },
          { label: name },
        ]}
        title={name}
        meta={
          <>
            {tpl.is_system ? <Badge variant="info">{t('templates.system')}</Badge> : null}
            {!tpl.is_active ? <Badge variant="neutral">{t('templates.inactive')}</Badge> : null}
            {templateChange ? <DraftMarker change={templateChange} /> : null}
          </>
        }
        description={tpl.description || undefined}
        actions={
          <>
            {canManage && tpl.current_version != null ? (
              <Button variant="secondary" icon={<Rocket />} onClick={() => setConfirm('migrate')}>
                {t('templates.migrate.open')}
              </Button>
            ) : null}
            {own && canManage ? (
              <Button
                variant="secondary"
                icon={<Plus />}
                onClick={() => {
                  createVersion.reset();
                  setConfirm('version');
                }}
              >
                {t('templates.newVersion')}
              </Button>
            ) : null}
          </>
        }
      />
      <MetamodelTabs />

      <dl className="flex flex-wrap gap-x-10 gap-y-3 text-sm">
        <div>
          <dt className="text-xs text-fg-muted">{t('templates.key')}</dt>
          <dd className="mt-0.5 font-mono text-xs text-fg">{tpl.key}</dd>
        </div>
        <div>
          <dt className="text-xs text-fg-muted">{t('templates.ciType')}</dt>
          <dd className="mt-0.5 font-medium text-fg">{nameOf(tpl.ci_type_id)}</dd>
        </div>
        <div>
          <dt className="text-xs text-fg-muted">{t('templates.currentVersion')}</dt>
          <dd className="mt-0.5">
            {tpl.current_version != null ? (
              <Badge variant="success">
                {t('templates.versionShort', { version: tpl.current_version })}
              </Badge>
            ) : (
              <Badge variant="neutral">{t('templates.unpublished')}</Badge>
            )}
          </dd>
        </div>
      </dl>
      {tpl.is_system ? (
        <p role="note" className="text-sm text-fg-muted">
          {t('templates.systemReadOnly')}
        </p>
      ) : null}

      <Card>
        <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
          <h3 className="text-base font-semibold text-fg">{t('templates.versions')}</h3>
          {version?.status === 'draft' && own ? (
            <div className="flex flex-wrap items-center gap-3">
              {!canPublish ? (
                <span className="text-xs text-fg-muted">
                  {t('templates.publishNeedsPermission')}
                </span>
              ) : null}
              <Button
                variant="secondary"
                icon={<Rocket />}
                disabled={!canPublish || publish.isPending}
                onClick={() => {
                  publish.reset();
                  setConfirm('publish');
                }}
              >
                {t('templates.publish', { version: version.version })}
              </Button>
            </div>
          ) : null}
        </div>
        {versions.isLoading ? (
          <SkeletonList rows={2} label={t('app.loading')} className="mt-3" />
        ) : null}
        {versions.error ? (
          <ErrorState
            className="mt-3"
            title={t('app.error')}
            retryLabel={t('common.retry')}
            onRetry={() => void versions.refetch()}
          />
        ) : null}
        {versions.isSuccess && list.length === 0 && newVersions.length === 0 ? (
          <p className="mt-2 text-sm text-fg-muted">{t('templates.noVersions')}</p>
        ) : null}
        {list.length > 0 ? (
          <ul aria-label={t('templates.versions')} className="mt-3 flex flex-wrap gap-2">
            {list.map((v) => (
              <li key={v.id}>
                <button
                  type="button"
                  aria-pressed={v.version === version?.version}
                  onClick={() => setSelected(v.version)}
                  className={cn(
                    'flex items-center gap-2 rounded-lg border px-3 py-1.5 text-sm font-medium text-fg transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent',
                    v.version === version?.version
                      ? 'border-accent bg-accent-soft'
                      : 'border-line hover:border-line-strong hover:bg-fg/[0.04]',
                  )}
                >
                  {t('templates.versionShort', { version: v.version })}
                  <Badge variant={VERSION_STATUS_VARIANT[v.status]}>
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
                className="flex items-center gap-2 rounded-lg border border-dashed border-line-strong px-3 py-1.5 text-sm text-fg"
              >
                {t('metamodelDrafts.newVersion')}
                <DraftMarker change="created" />
              </li>
            ))}
          </ul>
        ) : null}
      </Card>

      {version ? (
        <div className="space-y-3">
          {publishGeneral ? (
            <p role="alert" className="text-sm text-danger">
              {t('templates.publishFailed', { reason: publishGeneral })}
            </p>
          ) : null}
          {Object.keys(itemErrors).length > 0 ? (
            <p role="alert" className="text-sm text-danger">
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
          <h3 className="flex items-center gap-2 text-base font-semibold text-fg">
            <GitCompare aria-hidden="true" className="h-4 w-4 text-fg-muted" />
            {t('templates.compare')}
          </h3>
          <Toolbar className="mt-3 items-end">
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
          </Toolbar>
          <div className="mt-4">
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

      <MigrateDialog open={confirm === 'migrate'} template={tpl} onClose={() => setConfirm(null)} />

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
          <p role="alert" className="mb-3 text-sm text-danger">
            {t('templates.notSaved', { reason: describeFlagRefusal(createVersion.error, t) })}
          </p>
        ) : null}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={() => setConfirm(null)}>
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
          <Button variant="secondary" onClick={() => setConfirm(null)}>
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
