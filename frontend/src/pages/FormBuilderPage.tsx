/**
 * FormBuilderPage is the form builder of a CI type (MET-50, UI-11, WP-316):
 * the layout of the form for the type or one of the templates of its type
 * chain, without a role or for one role. It loads the own layout of the
 * target and role (GET /layouts); without one the editor starts with a copy
 * of the resolved form (GET /ci-types/{id}/form, WP-719) and the head names
 * where it comes from (inherited layout or standard layout).
 *
 * The builder writes /layouts only, never the data model (MET-50): without
 * a draft "publish" writes directly after a confirmation (MET-04, POST or
 * PATCH with If-Match); with an active draft (WP-798) withDraft captures
 * the write as a draft item (202) and the editor keeps its state.
 *
 * The area "Vorschau" (WP-803, MET-52) shows the form as role X with
 * template Y (LayoutPreview); it reads only and is closed at first.
 */
import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useParams, useSearchParams } from 'react-router-dom';
import { ArrowLeft, Eye, EyeOff, Send, Trash2 } from 'lucide-react';
import {
  useActiveDraftItems,
  useAllCITypes,
  useApplicableAttributeDefinitions,
  useAttributeSets,
  useCIType,
  useCITypeAttributeSets,
  useDeleteLayout,
  useLayout,
  useLayouts,
  useResolvedTypeForm,
  useRules,
  useSaveLayout,
  useSetAttributeDefinitions,
  useTemplates,
  useTypeAttributeDefinitions,
} from '../api/cmdbHooks';
import { useRoleList } from '../api/hooks';
import {
  isDraftItem,
  localizedText,
  type AttributeDefinition,
  type CIType,
  type FormLayout,
  type FormLayoutDocument,
  type ResolvedForm,
  type Template,
} from '../api/cmdb';
import { ApiError } from '../api/client';
import { useModules } from '../hooks/useEntitlements';
import { useMetamodelDraftStore } from '../stores/metamodelDraft';
import { Button } from '../components/ui/Button';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { ancestorsOf } from '../components/metamodel/TypeTree';
import { DraftMarker, draftChangeFor, draftCreations } from '../components/metamodel/DraftBanner';
import {
  LayoutEditor,
  RESERVED_SECTION_KEYS,
  layoutComplete,
  type FieldOrigin,
  type PaletteField,
} from '../components/metamodel/LayoutEditor';
import { LayoutPreview } from '../components/metamodel/LayoutPreview';

/** The problem type of a refused value at one field (WP-510). */
const INVALID_FIELD = 'urn:reticora:problem:invalid-field';

const isStatus = (error: unknown, status: number) =>
  error instanceof ApiError && error.status === status;

/** copyOfResolved is the editable copy of a resolved form without _instance and _required. */
export function copyOfResolved(form: ResolvedForm): FormLayoutDocument {
  const reserved: readonly string[] = RESERVED_SECTION_KEYS;
  return {
    sections: form.sections
      .filter((s) => !reserved.includes(s.key))
      .map((s) => ({
        key: s.key,
        title: { de: s.title.de ?? '', en: s.title.en ?? '' },
        visible_when: [],
        fields: s.fields.map((f) => ({ name: f.name, width: f.width })),
      })),
  };
}

/** layoutKeyFor proposes the key of a new layout from the target and the role. */
export function layoutKeyFor(targetKey: string, roleName?: string): string {
  const role = (roleName ?? '')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '_')
    .replace(/^_+|_+$/g, '');
  return (role ? `${targetKey}_${role}` : targetKey).slice(0, 100);
}

function typeLabel(type: CIType, language: string): string {
  return localizedText(type.display_name, language) || type.name;
}

function templateLabel(template: Template, language: string): string {
  return localizedText(template.display_name, language) || template.key;
}

const byOrder = (a: AttributeDefinition, b: AttributeDefinition) => a.order - b.order;

export function FormBuilderPage() {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  const { id = '' } = useParams<{ id: string }>();
  const [params, setParams] = useSearchParams();
  const templateId = params.get('template_id') ?? '';
  const roleId = params.get('role_id') ?? '';
  const draft = useMetamodelDraftStore((s) => s.draft);
  const draftItems = useActiveDraftItems();

  // The module metamodel (E-105): switched off, /layouts answers 404.
  const modules = useModules();
  const moduleOff = !!modules.modules && !modules.isModuleEnabled('metamodel');

  const typeQuery = useCIType(id);
  const type = typeQuery.data;
  const typesQuery = useAllCITypes({ include_inactive: true });
  const types = typesQuery.data ?? [];
  const chain = [id, ...ancestorsOf(id, types).map((a) => a.id)];
  const templatesQuery = useTemplates({}, { enabled: !moduleOff });
  const templates = (templatesQuery.data?.data ?? []).filter((tpl) =>
    chain.includes(tpl.ci_type_id),
  );
  const template = templates.find((tpl) => tpl.id === templateId);
  const roles = useRoleList({ limit: 200 }).data?.data ?? [];
  const role = roles.find((r) => r.id === roleId);

  // The palette: the attributes of the type of the target (MET-50, read only).
  const targetTypeId = template?.ci_type_id ?? id;
  const targetType = types.find((ty) => ty.id === targetTypeId) ?? type;
  const targetAncestors = ancestorsOf(targetTypeId, types);
  const applicable = useApplicableAttributeDefinitions(targetTypeId);
  const ancestorDefinitions = useTypeAttributeDefinitions(targetAncestors.map((a) => a.id));
  const setsQuery = useAttributeSets({ enabled: !moduleOff });
  const assignment = useCITypeAttributeSets(targetTypeId, {
    enabled: !moduleOff && !!targetType && !targetType.is_system,
  });
  const assignedSets = [...(assignment.data?.attribute_sets ?? [])]
    .sort((a, b) => a.position - b.position)
    .map((a) => (setsQuery.data?.data ?? []).find((s) => s.id === a.attribute_set_id))
    .filter((s) => !!s && s.is_active)
    .map((s) => s!);
  const setDefinitions = useSetAttributeDefinitions(
    assignedSets.map((s) => s.id),
    { enabled: !moduleOff },
  );

  const typeName = (typeId: string | undefined) => {
    const found = types.find((ty) => ty.id === typeId);
    return found ? typeLabel(found, lang) : (typeId ?? '');
  };
  const originOf = (d: AttributeDefinition): FieldOrigin => {
    if (d.scope === 'global') return { kind: 'global' };
    if (d.scope === 'set') {
      const set = assignedSets.find((s) => s.id === d.attribute_set_id);
      return { kind: 'set', name: set ? localizedText(set.display_name, lang) || set.key : '' };
    }
    return d.ci_type_id === id
      ? { kind: 'own' }
      : { kind: 'inherited', from: typeName(d.ci_type_id) };
  };
  const toPalette = (d: AttributeDefinition): PaletteField => ({
    name: d.key,
    label: localizedText(d.display_name, lang) || d.key,
    dataType: d.data_type,
    required: d.required,
    origin: originOf(d),
  });
  const all = applicable.data?.data ?? [];
  const definitions = [
    ...all.filter((d) => d.scope === 'type' && d.ci_type_id === targetTypeId).sort(byOrder),
    ...targetAncestors.flatMap((ancestor, i) =>
      (ancestorDefinitions[i]?.data?.data ?? [])
        .filter((d) => d.inheritable && d.ci_type_id === ancestor.id)
        .sort(byOrder),
    ),
    ...assignedSets.flatMap((_set, i) => [...(setDefinitions[i]?.data?.data ?? [])].sort(byOrder)),
    ...all.filter((d) => d.scope === 'global').sort(byOrder),
  ];
  const seen = new Set<string>();
  const palette = definitions
    .filter((d) => (seen.has(d.key) ? false : (seen.add(d.key), true)))
    .map(toPalette);

  // The catalog rules of the type for visible_when (WP-801), every page.
  const rules = useRules({ ci_type_id: targetTypeId }, { enabled: !moduleOff });
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = rules;
  useEffect(() => {
    if (hasNextPage && !isFetchingNextPage) void fetchNextPage();
  }, [hasNextPage, isFetchingNextPage, fetchNextPage]);
  const ruleKeys = [
    ...new Set((rules.data?.pages ?? []).flatMap((page) => page.data.map((r) => r.key))),
  ];

  // The own layout of target and role, else the copy of the resolved form.
  const layouts = useLayouts(templateId ? { template_id: templateId } : { ci_type_id: id }, {
    enabled: !moduleOff,
  });
  const layoutsOff = moduleOff || isStatus(layouts.error, 404);
  const own: FormLayout | undefined = (layouts.data?.data ?? []).find(
    (l) => (l.role_id ?? '') === roleId,
  );
  const resolved = useResolvedTypeForm(
    id,
    {
      template_id: templateId || undefined,
      role_id: roleId || undefined,
      preview: roleId ? true : undefined,
    },
    { enabled: layouts.isSuccess && !own },
  );
  const source = resolved.data;
  const sourceLayout = useLayout(source?.source === 'layout' ? source.layout_id : undefined, {
    enabled: !own,
  });

  const save = useSaveLayout();
  const remove = useDeleteLayout();
  const [doc, setDoc] = useState<FormLayoutDocument | null>(null);
  const [loaded, setLoaded] = useState('');
  const [reload, setReload] = useState(0);
  const [confirm, setConfirm] = useState<'publish' | 'remove' | null>(null);
  const [notice, setNotice] = useState('');
  const [previewOpen, setPreviewOpen] = useState(false);
  const target = `${templateId || id}:${roleId}`;
  const sourceKey = own
    ? `own:${own.id}:${own.version}:${reload}`
    : source && layouts.isSuccess
      ? `copy:${target}:${source.source}:${source.layout_id ?? ''}:${source.role_id ?? ''}:${reload}`
      : '';
  const initial: FormLayoutDocument | null = own
    ? own.layout
    : source && layouts.isSuccess
      ? copyOfResolved(source)
      : null;
  if (sourceKey !== loaded) {
    setLoaded(sourceKey);
    setDoc(initial);
  }
  const targetKey = template?.key ?? type?.key ?? '';
  const proposed = layoutKeyFor(targetKey, role?.name);
  const [keyState, setKeyState] = useState({ for: '', value: '' });
  const keyFor = `${target}:${proposed}`;
  if (keyState.for !== keyFor) setKeyState({ for: keyFor, value: proposed });
  const layoutKey = keyState.value;

  // Reset the answers of the last write when the target or the role changes.
  const { reset: resetSave } = save;
  const { reset: resetRemove } = remove;
  useEffect(() => {
    setNotice('');
    resetSave();
    resetRemove();
  }, [target, resetSave, resetRemove]);

  const selectTarget = (value: string) => {
    const next = new URLSearchParams(params);
    if (value) next.set('template_id', value);
    else next.delete('template_id');
    setParams(next, { replace: true });
  };
  const selectRole = (value: string) => {
    const next = new URLSearchParams(params);
    if (value) next.set('role_id', value);
    else next.delete('role_id');
    setParams(next, { replace: true });
  };

  const dirty = !!doc && !!initial && JSON.stringify(doc) !== JSON.stringify(initial);
  const complete = !!doc && layoutComplete(doc);
  const canSave = complete && (!own || dirty) && layoutKey.trim() !== '' && !save.isPending;

  const runSave = () => {
    if (!doc) return;
    setConfirm(null);
    setNotice('');
    remove.reset();
    const request = own
      ? ({ mode: 'update', id: own.id, version: own.version, patch: { layout: doc } } as const)
      : ({
          mode: 'create',
          data: {
            key: layoutKey.trim(),
            ...(templateId ? { template_id: templateId } : { ci_type_id: id }),
            ...(roleId ? { role_id: roleId } : {}),
            priority: 0,
            is_active: true,
            layout: doc,
          },
        } as const);
    save.mutate(request, {
      onSuccess: (saved) => {
        setNotice(
          isDraftItem(saved)
            ? t('formBuilder.savedInDraft', { name: draft?.name ?? '' })
            : t('formBuilder.published'),
        );
      },
      onError: (error) => {
        if (isStatus(error, 412)) {
          setReload((r) => r + 1);
          void layouts.refetch();
        }
      },
    });
  };

  const runRemove = () => {
    if (!own) return;
    setConfirm(null);
    setNotice('');
    save.reset();
    remove.mutate(
      { id: own.id, version: own.version },
      {
        onSuccess: (answer) =>
          setNotice(
            isDraftItem(answer)
              ? t('formBuilder.savedInDraft', { name: draft?.name ?? '' })
              : t('formBuilder.removed'),
          ),
        onError: (error) => {
          if (isStatus(error, 412)) {
            setReload((r) => r + 1);
            void layouts.refetch();
          }
        },
      },
    );
  };

  // The answers of the server to a write.
  const writeError = save.error ?? remove.error;
  const apiError = writeError instanceof ApiError ? writeError : undefined;
  const fieldPath =
    apiError?.status === 422 && apiError.type === INVALID_FIELD ? apiError.field : undefined;
  const editorPath = fieldPath?.startsWith('layout.') ? fieldPath : undefined;
  const conflict = apiError?.status === 409;
  const keyError =
    !own && conflict
      ? t('formBuilder.errors.conflictKey')
      : fieldPath === 'key'
        ? (apiError?.detail ?? t('formBuilder.errors.invalid'))
        : undefined;
  const describeWrite = (): string | undefined => {
    if (!writeError) return undefined;
    if (!apiError) return writeError instanceof Error ? writeError.message : undefined;
    switch (apiError.status) {
      case 403:
        return t('formBuilder.errors.forbidden', { traceId: apiError.traceId ?? '–' });
      case 409:
        return t('formBuilder.errors.conflict');
      case 412:
        return t('formBuilder.errors.stale');
      case 422:
        if (editorPath || fieldPath === 'key') return t('formBuilder.errors.invalidAt');
        return apiError.detail ?? t('formBuilder.errors.invalid');
      default:
        return apiError.detail ?? apiError.message;
    }
  };
  const writeMessage = describeWrite();

  // The draft badge of the own layout or of a layout created in the draft.
  const ownChange = own ? draftChangeFor(draftItems, { kinds: ['layout'], id: own.id }) : undefined;
  const createdInDraft =
    !own &&
    draftCreations(
      draftItems,
      'layout',
      (payload) =>
        (templateId ? payload.template_id === templateId : payload.ci_type_id === id) &&
        (typeof payload.role_id === 'string' ? payload.role_id : '') === roleId,
    ).length > 0;
  const draftChange = ownChange ?? (createdInDraft ? 'created' : undefined);

  // Where the copy of the resolved form comes from.
  const describeSource = (): string => {
    if (!source) return '';
    if (source.source === 'default') return t('formBuilder.source.default');
    const from = sourceLayout.data;
    if (!from) return t('formBuilder.source.inheritedUnknown');
    if (from.template_id) {
      const tpl = (templatesQuery.data?.data ?? []).find((x) => x.id === from.template_id);
      return t('formBuilder.source.inherited', {
        name: tpl ? templateLabel(tpl, lang) : from.template_id,
      });
    }
    return t('formBuilder.source.inherited', { name: typeName(from.ci_type_id) });
  };
  const copiedRole =
    !roleId && source?.role_id
      ? (roles.find((r) => r.id === source.role_id)?.name ?? source.role_id)
      : undefined;

  const name = type ? typeLabel(type, lang) : '';

  if (typeQuery.error) {
    return (
      <div className="space-y-6">
        <ErrorState
          title={t('formBuilder.typeError')}
          description={typeQuery.error instanceof Error ? typeQuery.error.message : undefined}
          retryLabel={t('common.retry', 'Erneut versuchen')}
          onRetry={() => void typeQuery.refetch()}
        />
      </div>
    );
  }

  const header = (
    <PageHeader
      title={name ? t('formBuilder.titleOf', { name }) : t('formBuilder.title')}
      description={t('formBuilder.description')}
      breadcrumbs={[{ label: t('formBuilder.breadcrumb'), to: '/ci-types' }, { label: name }]}
      actions={
        <Link
          to="/ci-types"
          className="inline-flex items-center gap-1.5 text-sm font-medium text-accent-strong hover:underline"
        >
          <ArrowLeft aria-hidden="true" className="h-4 w-4" />
          {t('formBuilder.back')}
        </Link>
      }
    />
  );

  if (layoutsOff) {
    return (
      <div className="space-y-6">
        {header}
        <p role="note" className="rounded-xl border border-line bg-panel p-4 text-sm text-fg">
          {t('formBuilder.moduleOff')}
        </p>
      </div>
    );
  }

  const resolveForbidden = isStatus(resolved.error, 403);
  const resolvedTrace = resolved.error instanceof ApiError ? resolved.error.traceId : undefined;
  const loading = typeQuery.isLoading || layouts.isLoading || (!own && resolved.isLoading);

  return (
    <div className="space-y-6">
      {header}

      <section
        aria-label={t('formBuilder.selection')}
        className="grid gap-3 rounded-xl border border-line bg-panel p-4 sm:grid-cols-2"
      >
        <Select
          label={t('formBuilder.target')}
          value={templateId}
          onChange={(e) => selectTarget(e.target.value)}
          options={[
            { value: '', label: t('formBuilder.targetType', { name }) },
            ...templates.map((tpl) => ({
              value: tpl.id,
              label: t('formBuilder.targetTemplate', { name: templateLabel(tpl, lang) }),
            })),
          ]}
        />
        <Select
          label={t('formBuilder.role')}
          value={roleId}
          onChange={(e) => selectRole(e.target.value)}
          options={[
            { value: '', label: t('formBuilder.noRole') },
            ...roles.map((r) => ({ value: r.id, label: r.name })),
          ]}
        />
      </section>

      <section
        aria-label={t('formBuilder.state')}
        className="flex flex-wrap items-start justify-between gap-3 rounded-xl border border-line bg-panel p-4"
      >
        <div className="min-w-0 space-y-1 text-sm">
          <p className="flex flex-wrap items-center gap-2 font-medium text-fg">
            {own
              ? t('formBuilder.ownLayout', { key: own.key, version: own.version })
              : describeSource()}
            {draftChange ? <DraftMarker change={draftChange} /> : null}
          </p>
          {copiedRole ? (
            <p className="text-fg-muted">{t('formBuilder.copiedRole', { role: copiedRole })}</p>
          ) : null}
          {!own && source ? <p className="text-fg-muted">{t('formBuilder.copyHint')}</p> : null}
          {draft ? (
            <p className="text-fg-muted">{t('formBuilder.draftHint', { name: draft.name })}</p>
          ) : null}
        </div>
        <div className="flex flex-wrap items-end gap-2">
          {!own && doc ? (
            <div className="w-64">
              <Input
                label={t('formBuilder.key')}
                value={layoutKey}
                maxLength={100}
                error={keyError}
                onChange={(e) => setKeyState({ for: keyFor, value: e.target.value })}
              />
            </div>
          ) : null}
          {own ? (
            <Button
              variant="secondary"
              icon={<Trash2 />}
              className="text-danger hover:text-danger"
              disabled={remove.isPending}
              onClick={() => setConfirm('remove')}
            >
              {t('formBuilder.removeOwn')}
            </Button>
          ) : null}
          {doc ? (
            <Button
              icon={<Send />}
              disabled={!canSave}
              loading={save.isPending}
              onClick={() => (draft ? runSave() : setConfirm('publish'))}
            >
              {draft ? t('formBuilder.saveInDraft') : t('formBuilder.publish')}
            </Button>
          ) : null}
        </div>
      </section>

      {writeMessage ? (
        <p role="alert" className="text-sm text-danger">
          {writeMessage}
        </p>
      ) : null}
      <p role="status" className="text-sm text-fg-muted empty:hidden">
        {notice}
      </p>
      {doc && !complete ? (
        <p className="text-sm text-danger">{t('formBuilder.titlesRequired')}</p>
      ) : null}

      {resolveForbidden ? (
        <p role="alert" className="text-sm text-danger">
          {t('formBuilder.errors.resolveForbidden', { traceId: resolvedTrace ?? '–' })}
        </p>
      ) : resolved.error && !own ? (
        <ErrorState
          title={t('formBuilder.resolveError')}
          description={resolved.error instanceof Error ? resolved.error.message : undefined}
          retryLabel={t('common.retry', 'Erneut versuchen')}
          onRetry={() => void resolved.refetch()}
        />
      ) : null}
      {layouts.error ? (
        <ErrorState
          title={t('formBuilder.layoutsError')}
          description={layouts.error instanceof Error ? layouts.error.message : undefined}
          retryLabel={t('common.retry', 'Erneut versuchen')}
          onRetry={() => void layouts.refetch()}
        />
      ) : null}

      {loading && !doc ? <SkeletonList rows={4} /> : null}
      {doc ? (
        <LayoutEditor
          value={doc}
          onChange={setDoc}
          palette={palette}
          ruleKeys={ruleKeys}
          invalidPath={editorPath}
          invalidMessage={editorPath ? apiError?.detail : undefined}
        />
      ) : null}

      <section
        aria-labelledby="form-builder-preview"
        className="space-y-4 rounded-xl border border-line bg-panel p-4"
      >
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h2 id="form-builder-preview" className="text-base font-semibold text-fg">
            {t('formBuilder.preview.title')}
          </h2>
          <Button
            variant="secondary"
            icon={previewOpen ? <EyeOff /> : <Eye />}
            aria-expanded={previewOpen}
            onClick={() => setPreviewOpen((open) => !open)}
          >
            {previewOpen ? t('formBuilder.preview.close') : t('formBuilder.preview.open')}
          </Button>
        </div>
        {previewOpen ? (
          <LayoutPreview
            key={target}
            ciTypeId={id}
            templates={templates}
            initialTemplateId={templateId}
            initialRoleId={roleId}
            unsaved={dirty}
          />
        ) : null}
      </section>

      <Modal
        open={confirm === 'publish'}
        onOpenChange={(open) => setConfirm(open ? 'publish' : null)}
        title={t('formBuilder.confirmPublish.title')}
        description={t('formBuilder.confirmPublish.description')}
        size="sm"
      >
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={() => setConfirm(null)}>
            {t('common.cancel', 'Abbrechen')}
          </Button>
          <Button onClick={runSave}>{t('formBuilder.publish')}</Button>
        </div>
      </Modal>
      <Modal
        open={confirm === 'remove'}
        onOpenChange={(open) => setConfirm(open ? 'remove' : null)}
        title={t('formBuilder.confirmRemove.title')}
        description={t('formBuilder.confirmRemove.description')}
        size="sm"
      >
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={() => setConfirm(null)}>
            {t('common.cancel', 'Abbrechen')}
          </Button>
          <Button variant="danger" onClick={runRemove}>
            {t('formBuilder.removeOwn')}
          </Button>
        </div>
      </Modal>
    </div>
  );
}
