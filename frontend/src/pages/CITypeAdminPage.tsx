import { useCallback, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import type { TFunction } from 'i18next';
import {
  Copy,
  Info,
  LayoutDashboard,
  LayoutTemplate,
  ListTree,
  Pencil,
  Plus,
  Power,
  PowerOff,
  Search,
  Trash2,
} from 'lucide-react';
import {
  useAllCITypes,
  useApplicableAttributeDefinitions,
  useAttributeSets,
  useCITypeAttributeSets,
  useCITypeTemplates,
  useCloneCIType,
  useCreateAttributeSet,
  useCreateCIType,
  useDeleteAttributeDefinition,
  useDeleteAttributeSet,
  useGlobalAttributeDefinitions,
  useReindexAttributeDefinition,
  useSensitiveCIFields,
  useSetAttributeDefinitions,
  useSetCITypeActive,
  useTypeAttributeDefinitions,
  useActiveDraftItems,
  useUpdateAttributeSet,
  useUpdateCIType,
} from '../api/cmdbHooks';
import { useHasPermission } from '../api/hooks';
import { useModules } from '../hooks/useEntitlements';
import {
  isDraftItem,
  localizedText,
  type AttributeDefinition,
  type AttributeSet,
  type AttributeSetCreate,
  type AttributeSetPatch,
  type CIType,
  type CITypeCreateRequest,
  type CITypeUpdateRequest,
  type Drafted,
  type MetamodelDraftItem,
  type SensitiveField,
} from '../api/cmdb';
import { ApiError } from '../api/client';
import { Badge } from '../components/ui/Badge';
import { Button, IconButton } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { Input } from '../components/ui/Input';
import { Select } from '../components/ui/Select';
import { Modal } from '../components/ui/Modal';
import { PageHeader } from '../components/ui/PageHeader';
import { Toolbar } from '../components/ui/Toolbar';
import { SkeletonList } from '../components/ui/Skeleton';
import { ErrorState } from '../components/ui/ErrorState';
import { TypeTree, ancestorsOf } from '../components/metamodel/TypeTree';
import {
  AttributeEditor,
  describeFlagRefusal,
  fieldDetail,
  fieldErrors,
  type AttributeEditorTarget,
} from '../components/metamodel/AttributeEditor';
import { IndexStatus } from '../components/metamodel/IndexStatus';
import { AttributeSetEditor } from '../components/metamodel/AttributeSetEditor';
import { TypeSetAssignment } from '../components/metamodel/TypeSetAssignment';
import { MetamodelTabs } from '../components/metamodel/MetamodelTabs';
import { MetamodelTransfer } from '../components/metamodel/MetamodelTransfer';
import {
  DraftMarker,
  draftChangeFor,
  draftCreations,
  draftItemName,
  type DraftChange,
} from '../components/metamodel/DraftBanner';

// Data types of MET-11 (the catalog test labels each of them).
export { FIELD_TYPES } from '../components/metamodel/AttributeEditor';

type TypeLabel = (type: CIType) => string;

// The refusal of deactivating a type with a deactivated parent and of a parent
// that is deactivated (WP-249).
const TYPE_DEACTIVATED = 'the CI type is deactivated';

/** The origin of an attribute in the catalog of a type (UI-11). */
type Origin =
  | { kind: 'own' }
  | { kind: 'inherited'; from: CIType }
  | { kind: 'set'; set: AttributeSet }
  | { kind: 'global' };

/** setLabel is the name of an attribute set in the UI language. */
function setLabel(set: AttributeSet, language: string): string {
  return localizedText(set.display_name, language) || set.key;
}

/** A dialog of an attribute set: create, or edit an existing set. */
type SetDialogState = { mode: 'create' } | { mode: 'edit'; set: AttributeSet };

/** A dialog of the type: create, or edit an existing type. */
type TypeDialogState = { mode: 'create' } | { mode: 'edit'; type: CIType };

/** descendantsOf returns the ids of the types below a type (they cannot be its parent). */
function descendantsOf(id: string, types: CIType[]): Set<string> {
  const out = new Set<string>();
  let added = true;
  while (added) {
    added = false;
    for (const ty of types) {
      const parent = ty.parent_type_id;
      if (parent && (parent === id || out.has(parent)) && !out.has(ty.id)) {
        out.add(ty.id);
        added = true;
      }
    }
  }
  return out;
}

/** The message of a refused parent at the parent field (WP-248, WP-249). */
function parentMessage(error: ApiError, t: TFunction): string {
  const detail = fieldDetail(error);
  if (error.status === 409) return t('citypes.typeErrors.parentConflict', { detail });
  if (detail === TYPE_DEACTIVATED) return t('citypes.typeErrors.parentInactive');
  return t('citypes.typeErrors.parentInvalid', { detail });
}

/**
 * describeActionError renders a refused clone, deactivation or activation
 * (WP-249): active subtypes (409 on is_active) and a deactivated parent (409
 * on parent_type_id) by name, every other refusal with the server's reason.
 */
function describeActionError(
  action: 'clone' | 'deactivate' | 'activate',
  error: unknown,
  t: TFunction,
): string {
  if (error instanceof ApiError && error.status === 409) {
    if (error.field === 'is_active') return t('citypes.typeErrors.activeSubtypes');
    if (error.field === 'parent_type_id') {
      return t(`citypes.typeErrors.${action}ParentInactive`);
    }
  }
  return t(`citypes.typeErrors.${action}Failed`, { reason: describeFlagRefusal(error, t) });
}

/** isSensitive reports whether a mark of WP-726 covers the attribute in the type chain. */
function isSensitive(
  marks: SensitiveField[],
  definition: AttributeDefinition,
  chain: string[],
): boolean {
  return marks.some(
    (m) =>
      m.object_kind === 'ci' &&
      m.field_key === `attributes.${definition.key}` &&
      (!m.ci_type_id || chain.includes(m.ci_type_id)),
  );
}

const byOrder = (a: AttributeDefinition, b: AttributeDefinition) =>
  (a.order ?? 0) - (b.order ?? 0) || a.key.localeCompare(b.key);

/** The draft mark of an attribute row: the definition or the field of its key (E-113). */
function attributeChange(
  items: readonly MetamodelDraftItem[],
  d: AttributeDefinition,
): DraftChange | undefined {
  return d.scope === 'global'
    ? draftChangeFor(items, {
        kinds: ['attribute_definition', 'global_field'],
        id: d.id,
        key: d.key,
      })
    : draftChangeFor(items, {
        kinds: ['attribute_definition', 'ci_type_field'],
        id: d.id,
        key: d.key,
        typeId: d.ci_type_id,
      });
}

/**
 * DraftPlaceholders lists the parts a draft creates as placeholder rows
 * from the item payload (E-113): they exist only in the draft.
 */
function DraftPlaceholders({ items, t }: { items: MetamodelDraftItem[]; t: TFunction }) {
  const { i18n } = useTranslation();
  if (items.length === 0) return null;
  return (
    <>
      {items.map((item) => (
        <li
          key={`draft:${item.id}`}
          data-draft-item={item.id}
          className="flex flex-wrap items-center gap-2 py-3 text-sm"
        >
          <span className="font-medium text-fg">
            {draftItemName(item, i18n.language) || t('metamodelDrafts.noTarget')}
          </span>
          <DraftMarker change="created" />
        </li>
      ))}
    </>
  );
}

export function CITypeAdminPage() {
  const { t, i18n } = useTranslation();
  const label = useCallback<TypeLabel>(
    (ty) => localizedText(ty.display_name, i18n.language) || ty.name,
    [i18n.language],
  );
  const [search, setSearch] = useState('');
  const [showInactive, setShowInactive] = useState(false);
  const [selectedId, setSelectedId] = useState<string | undefined>();
  const [showTemplates, setShowTemplates] = useState(false);
  const [typeDialog, setTypeDialog] = useState<TypeDialogState | null>(null);
  const [editor, setEditor] = useState<AttributeEditorTarget | null>(null);
  const [openSetId, setOpenSetId] = useState<string | undefined>();

  // Attribute sets (WP-250, WP-797) belong to the module metamodel (E-105):
  // switched off, the set routes answer 404 and the page shows a note
  // instead; tree and attribute catalog stay.
  const modules = useModules();
  const metamodelOn = !modules.isLoading && modules.isModuleEnabled('metamodel');
  const setsQuery = useAttributeSets({ enabled: metamodelOn });
  const setsOff =
    (!modules.isLoading && !metamodelOn) ||
    (setsQuery.error instanceof ApiError && setsQuery.error.status === 404);
  const sets = setsOff ? [] : (setsQuery.data?.data ?? []);
  const openSet = (id: string) => {
    setOpenSetId(id);
    document.getElementById('attribute-sets')?.scrollIntoView?.({ block: 'start' });
  };

  // Every type, all pages of 200 (offset paging, WP-466): the tree needs the
  // whole chain; the search filters in the loaded tree.
  const { data, isLoading, error, refetch } = useAllCITypes({ include_inactive: showInactive });
  const types = data ?? [];
  const templates = useCITypeTemplates();
  const createType = useCreateCIType();

  const selected = types.find((ty) => ty.id === selectedId);

  const instantiateTemplate = (tpl: CIType) => {
    createType.mutate(
      {
        key: tpl.key,
        name: tpl.name,
        display_name: tpl.display_name,
        description: tpl.description,
        category: tpl.category,
        is_logical: tpl.is_logical,
        is_observable: tpl.is_observable,
        capabilities: tpl.capabilities,
        allowed_relationship_types: tpl.allowed_relationship_types,
        fields: tpl.fields,
      },
      { onSuccess: () => setShowTemplates(false) },
    );
  };

  // Below lg the tree sits above the detail: choosing a type brings the detail
  // into view, otherwise nothing seems to happen.
  const selectType = (id: string) => {
    setSelectedId(id);
    if (window.matchMedia?.('(max-width: 1023px)').matches) {
      window.requestAnimationFrame(() =>
        document.getElementById('type-detail')?.scrollIntoView?.({ block: 'start' }),
      );
    }
  };

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('citypes.title', 'CI-Typen')}
        description={t(
          'citypes.subtitle',
          'Metadaten-getriebene Typen: Felder, Gruppen, Validierung, Lebenszyklus.',
        )}
        actions={
          <>
            {/* Export and import of the metamodel (WP-800). */}
            <MetamodelTransfer />
            <Button
              variant="secondary"
              icon={<LayoutTemplate />}
              onClick={() => {
                createType.reset();
                setShowTemplates(true);
              }}
            >
              {t('citypes.fromTemplate')}
            </Button>
            <Button icon={<Plus />} onClick={() => setTypeDialog({ mode: 'create' })}>
              {t('citypes.create', 'Typ anlegen')}
            </Button>
          </>
        }
      />
      <MetamodelTabs />

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-[minmax(17rem,22rem)_minmax(0,1fr)] lg:items-start">
        {/* The type tree: a pane of its own that stays in view and scrolls by itself. */}
        <div className="flex min-w-0 flex-col gap-3 lg:sticky lg:top-[calc(var(--header-height)+1rem)]">
          <Toolbar>
            <div className="min-w-0 flex-1">
              <Input
                icon={<Search />}
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder={t('common.search', 'Suchen')}
                aria-label={t('common.search', 'Suchen')}
              />
            </div>
          </Toolbar>
          <label className="flex items-center gap-2 text-sm text-fg-muted">
            <input
              type="checkbox"
              checked={showInactive}
              onChange={(e) => setShowInactive(e.target.checked)}
            />
            {t('citypes.showInactive', 'Deaktivierte anzeigen')}
          </label>
          {isLoading ? <SkeletonList rows={4} label={t('app.loading')} /> : null}
          {error ? (
            <ErrorState
              title={t('app.error')}
              retryLabel={t('common.retry')}
              onRetry={() => void refetch()}
            />
          ) : null}
          {data ? (
            <div className="-mx-1 max-h-80 overflow-y-auto px-1 py-1 scroll-thin lg:max-h-[calc(100vh-var(--header-height)-11rem)]">
              <TypeTree
                types={types}
                selectedId={selectedId}
                onSelect={selectType}
                search={search}
                label={label}
              />
            </div>
          ) : null}
          <DraftTypes types={types} label={label} t={t} />
        </div>

        <div className="min-w-0 space-y-6">
          <div id="type-detail" className="scroll-mt-20">
            {selected ? (
              <CITypeDetail
                key={selected.id}
                type={selected}
                types={types}
                label={label}
                onSelectType={setSelectedId}
                onEdit={() => setTypeDialog({ mode: 'edit', type: selected })}
                onAddAttribute={() =>
                  setEditor({ mode: 'create', scope: 'type', ciTypeId: selected.id })
                }
                onEditAttribute={(definition) => setEditor({ mode: 'edit', definition })}
                sets={sets}
                setsEnabled={metamodelOn && !setsOff}
                onOpenSet={openSet}
                t={t}
              />
            ) : (
              <EmptyState
                icon={<ListTree />}
                title={t('citypes.select', 'Typ auswählen, um Felder zu bearbeiten.')}
              />
            )}
          </div>
          <GlobalAttributesSection
            types={types}
            label={label}
            onAdd={() => setEditor({ mode: 'create', scope: 'global' })}
            onEdit={(definition) => setEditor({ mode: 'edit', definition })}
            t={t}
          />
          <AttributeSetsSection
            off={setsOff}
            sets={sets}
            loading={modules.isLoading || setsQuery.isLoading}
            error={setsOff ? null : setsQuery.error}
            onRetry={() => void setsQuery.refetch()}
            openSetId={openSetId}
            onOpenSet={setOpenSetId}
            t={t}
          />
        </div>
      </div>

      {/* Templates modal: the catalog of type templates (GET /ci-types/templates) */}
      <Modal open={showTemplates} onOpenChange={setShowTemplates} title={t('citypes.templates')}>
        {(templates.data?.data ?? []).length === 0 && templates.isSuccess ? (
          <p className="text-sm text-fg-muted">{t('common.noData')}</p>
        ) : null}
        <div className="grid max-h-96 grid-cols-1 gap-2 overflow-y-auto scroll-thin md:grid-cols-2">
          {(templates.data?.data ?? []).map((tpl) => (
            <button
              key={tpl.key}
              type="button"
              onClick={() => instantiateTemplate(tpl)}
              className="rounded-lg border border-line px-3.5 py-3 text-left transition-colors hover:border-line-strong hover:bg-fg/[0.04] focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent"
            >
              <p className="font-medium text-fg">{label(tpl)}</p>
              <p className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-fg-muted">
                {tpl.category ? (
                  <span>
                    {/* eslint-disable-next-line no-restricted-syntax -- GLO-10: the template category is free metamodel text (ci_type.category, no enum), not a catalog key */}
                    {tpl.category}
                  </span>
                ) : null}
                {tpl.is_logical ? <Badge>{t('citypes.logicalShort')}</Badge> : null}
                <span className="tabular-nums">
                  {t('citypes.fieldCount', { count: (tpl.fields ?? []).length })}
                </span>
              </p>
            </button>
          ))}
        </div>
        {createType.error ? (
          <p role="alert" className="mt-3 text-sm text-danger">
            {t('citypes.typeErrors.notSaved', {
              reason: describeFlagRefusal(createType.error, t),
            })}
          </p>
        ) : null}
      </Modal>

      {typeDialog ? (
        <TypeDialog
          state={typeDialog}
          types={types}
          label={label}
          onClose={() => setTypeDialog(null)}
          onSaved={(saved) => {
            setTypeDialog(null);
            // A write captured in the draft (202) answers its item, no type.
            if (!isDraftItem(saved)) setSelectedId(saved.id);
          }}
          t={t}
        />
      ) : null}

      {editor ? (
        <AttributeEditor
          target={editor}
          onClose={() => setEditor(null)}
          types={types}
          typeLabel={label}
        />
      ) : null}
    </div>
  );
}

/**
 * TypeDialog creates (POST /ci-types) or edits (PATCH /ci-types/{id}) a
 * type: name, key (only on creation), labels DE/EN, description, category
 * and parent (WP-248; system types are parents too). Refusals of the parent
 * (409 cycle, 422 depth, foreign or deactivated parent, composition) show
 * at the parent field.
 */
function TypeDialog({
  state,
  types,
  label,
  onClose,
  onSaved,
  t,
}: {
  state: TypeDialogState;
  types: CIType[];
  label: TypeLabel;
  onClose: () => void;
  onSaved: (type: Drafted<CIType>) => void;
  t: TFunction;
}) {
  const editing = state.mode === 'edit' ? state.type : undefined;
  const [draft, setDraft] = useState({
    name: editing?.name ?? '',
    key: editing?.key ?? '',
    de: editing?.display_name?.de ?? '',
    en: editing?.display_name?.en ?? '',
    description: editing?.description ?? '',
    category: editing?.category ?? '',
    parent: editing?.parent_type_id ?? '',
  });
  const createType = useCreateCIType();
  const updateType = useUpdateCIType();
  const mutation = editing ? updateType : createType;
  const error = mutation.error;
  const errors = fieldErrors(error);
  if (error instanceof ApiError && error.field === 'parent_type_id') {
    errors.parent_type_id = parentMessage(error, t);
  }
  const shownAtField = [
    'name',
    'key',
    'parent_type_id',
    'display_name',
    'description',
    'category',
  ].filter((f) => errors[f]).length;
  const set = (change: Partial<typeof draft>) => setDraft((d) => ({ ...d, ...change }));

  const excluded = editing ? descendantsOf(editing.id, types).add(editing.id) : new Set<string>();
  const parents = types
    .filter((ty) => !excluded.has(ty.id))
    .sort((a, b) => label(a).localeCompare(label(b)));

  const displayName = () => ({
    ...(draft.de ? { de: draft.de } : {}),
    ...(draft.en ? { en: draft.en } : {}),
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!editing) {
      const body: CITypeCreateRequest = { name: draft.name };
      if (draft.key.trim()) body.key = draft.key.trim();
      if (draft.de || draft.en) body.display_name = displayName();
      if (draft.description) body.description = draft.description;
      if (draft.category) body.category = draft.category;
      if (draft.parent) body.parent_type_id = draft.parent;
      createType.mutate(body, { onSuccess: onSaved });
      return;
    }
    const patch: CITypeUpdateRequest = {};
    if (draft.name !== editing.name) patch.name = draft.name;
    if (
      draft.de !== (editing.display_name?.de ?? '') ||
      draft.en !== (editing.display_name?.en ?? '')
    ) {
      patch.display_name = displayName();
    }
    if (draft.description !== (editing.description ?? '')) patch.description = draft.description;
    if (draft.category !== (editing.category ?? '')) patch.category = draft.category;
    if (draft.parent !== (editing.parent_type_id ?? '')) patch.parent_type_id = draft.parent;
    if (Object.keys(patch).length === 0) {
      onClose();
      return;
    }
    updateType.mutate({ id: editing.id, ...patch }, { onSuccess: onSaved });
  };

  return (
    <Modal
      open
      onOpenChange={(open) => (open ? undefined : onClose())}
      title={
        editing ? t('citypes.typeDialog.editTitle', { name: label(editing) }) : t('citypes.create')
      }
    >
      <form className="space-y-3" onSubmit={submit}>
        <Input
          label={t('citypes.name')}
          value={draft.name}
          required
          onChange={(e) => set({ name: e.target.value })}
          error={errors.name}
        />
        <Input
          label={t('citypes.typeDialog.key')}
          value={draft.key}
          disabled={!!editing}
          readOnly={!!editing}
          onChange={(e) => set({ key: e.target.value })}
          error={errors.key}
        />
        {editing ? (
          <p className="text-xs text-fg-muted">{t('citypes.typeDialog.keyImmutable')}</p>
        ) : null}
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <Input
            label={t('citypes.labelDe')}
            value={draft.de}
            onChange={(e) => set({ de: e.target.value })}
            error={errors.display_name}
          />
          <Input
            label={t('citypes.labelEn')}
            value={draft.en}
            onChange={(e) => set({ en: e.target.value })}
          />
        </div>
        <Input
          label={t('citypes.description')}
          value={draft.description}
          onChange={(e) => set({ description: e.target.value })}
          error={errors.description}
        />
        <Input
          label={t('citypes.typeDialog.category')}
          value={draft.category}
          onChange={(e) => set({ category: e.target.value })}
          error={errors.category}
        />
        <Select
          label={t('citypes.typeDialog.parent')}
          options={[
            { value: '', label: t('citypes.typeDialog.noParent') },
            ...parents.map((ty) => ({
              value: ty.id,
              label: ty.is_system
                ? t('citypes.typeDialog.systemParent', { name: label(ty) })
                : label(ty),
            })),
          ]}
          value={draft.parent}
          onChange={(e) => set({ parent: e.target.value })}
          error={errors.parent_type_id}
        />
        {error ? (
          <p role="alert" className="text-sm text-danger">
            {shownAtField > 0
              ? t('citypes.typeErrors.checkFields')
              : t('citypes.typeErrors.notSaved', { reason: describeFlagRefusal(error, t) })}
          </p>
        ) : null}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            {t('common.cancel', 'Abbrechen')}
          </Button>
          <Button type="submit" disabled={mutation.isPending}>
            {editing ? t('common.save', 'Speichern') : t('common.create', 'Anlegen')}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

/**
 * AttributeRow is an attribute of the catalog: label, key, data type and
 * group, its origin, its flags and marks and its index status, and the
 * actions the list offers for it (none: read-only).
 */
function AttributeRow({
  definition,
  origin,
  sensitive,
  appliesTo,
  onEdit,
  editLabel,
  onDelete,
  deleteLabel,
  onReindex,
  reindexing,
  onOpenOrigin,
  draftChange,
  t,
}: {
  definition: AttributeDefinition;
  draftChange?: DraftChange;
  origin?: Origin;
  sensitive: boolean;
  appliesTo?: string;
  onEdit?: () => void;
  editLabel?: string;
  onDelete?: () => void;
  deleteLabel?: string;
  onReindex?: () => void;
  reindexing?: boolean;
  onOpenOrigin?: () => void;
  t: TFunction;
}) {
  const { i18n } = useTranslation();
  const d = definition;
  const name = localizedText(d.display_name, i18n.language) || d.key;
  return (
    <li className="flex flex-wrap items-start justify-between gap-x-4 gap-y-2 py-3 text-sm">
      <div className="min-w-0 flex-1 basis-48 space-y-1.5">
        <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
          <span className="font-medium text-fg [overflow-wrap:anywhere]">{name}</span>
          {name !== d.key ? <span className="font-mono text-xs text-fg-muted">{d.key}</span> : null}
          <span className="text-xs text-fg-muted">
            {t(`citypes.dataTypes.${d.data_type}`, d.data_type)}
          </span>
          {d.group ? <span className="text-xs text-fg-subtle">{d.group}</span> : null}
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          {origin?.kind === 'own' ? (
            <Badge variant="neutral">{t('citypes.origin.own')}</Badge>
          ) : null}
          {origin?.kind === 'inherited' ? (
            <Badge variant="info">
              {t('citypes.origin.inherited', { type: originLabel(origin, i18n.language) })}
            </Badge>
          ) : null}
          {origin?.kind === 'set' ? (
            <Badge variant="info">
              {t('citypes.origin.set', { name: setLabel(origin.set, i18n.language) })}
            </Badge>
          ) : null}
          {origin?.kind === 'global' ? (
            <Badge variant="info">{t('citypes.origin.global')}</Badge>
          ) : null}
          {d.is_system ? <Badge variant="info">{t('citypes.system')}</Badge> : null}
          {d.required ? <Badge variant="warning">{t('citypes.flagRequired')}</Badge> : null}
          {d.conditional && Object.keys(d.conditional).length > 0 ? (
            <Badge variant="info">{t('citypes.conditional')}</Badge>
          ) : null}
          {d.searchable ? <Badge variant="neutral">{t('citypes.flagSearchable')}</Badge> : null}
          {d.unique ? (
            <Badge variant="neutral">
              {d.unique_scope === 'client'
                ? t('citypes.flagUniqueClient')
                : t('citypes.flagUnique')}
            </Badge>
          ) : null}
          {origin?.kind === 'own' && d.inheritable ? (
            <Badge variant="neutral">{t('citypes.flagInheritable')}</Badge>
          ) : null}
          {d.computed ? <Badge variant="info">{t('citypes.flagComputed')}</Badge> : null}
          {sensitive ? <Badge variant="danger">{t('citypes.flagSensitive')}</Badge> : null}
          {appliesTo ? <Badge variant="neutral">{appliesTo}</Badge> : null}
          {draftChange ? <DraftMarker change={draftChange} /> : null}
        </div>
        <IndexStatus
          status={d.index_status}
          attributeKey={d.key}
          onReindex={onReindex}
          reindexing={reindexing}
        />
      </div>
      <div className="flex flex-wrap items-center gap-1">
        {onOpenOrigin && origin?.kind === 'inherited' ? (
          <Button variant="ghost" size="sm" className={linkAction} onClick={onOpenOrigin}>
            {t('citypes.origin.open', { type: originLabel(origin, i18n.language) })}
          </Button>
        ) : null}
        {onOpenOrigin && origin?.kind === 'set' ? (
          <Button variant="ghost" size="sm" className={linkAction} onClick={onOpenOrigin}>
            {t('citypes.origin.openSet', { name: setLabel(origin.set, i18n.language) })}
          </Button>
        ) : null}
        {origin?.kind === 'global' ? (
          <Button variant="ghost" size="sm" className={linkAction} asChild>
            <a href="#global-attributes">{t('citypes.origin.openGlobal')}</a>
          </Button>
        ) : null}
        {onEdit ? (
          <IconButton
            size="sm"
            icon={<Pencil />}
            label={editLabel ?? t('common.edit', 'Bearbeiten')}
            onClick={onEdit}
          />
        ) : null}
        {onDelete ? (
          <IconButton
            size="sm"
            icon={<Trash2 />}
            label={deleteLabel ?? t('common.delete', 'Löschen')}
            className={deleteIcon}
            onClick={onDelete}
          />
        ) : null}
      </div>
    </li>
  );
}

/** A text action beside a row: a link in the accent colour. */
const linkAction = 'text-accent hover:text-accent-strong';
/** A text action that removes or switches off: the danger colour at rest. */
const dangerAction = 'text-danger hover:text-danger';
/** An icon action that deletes: quiet at rest, red when it is about to be used. */
const deleteIcon = 'hover:text-danger';

function originLabel(origin: Origin, language: string): string {
  return origin.kind === 'inherited'
    ? localizedText(origin.from.display_name, language) || origin.from.name
    : '';
}

function CITypeDetail({
  type,
  types,
  label,
  onSelectType,
  onEdit,
  onAddAttribute,
  onEditAttribute,
  sets,
  setsEnabled,
  onOpenSet,
  t,
}: {
  type: CIType;
  types: CIType[];
  label: TypeLabel;
  onSelectType: (id: string) => void;
  onEdit: () => void;
  onAddAttribute: () => void;
  onEditAttribute: (definition: AttributeDefinition) => void;
  sets: AttributeSet[];
  setsEnabled: boolean;
  onOpenSet: (id: string) => void;
  t: TFunction;
}) {
  const cloneType = useCloneCIType();
  const setActive = useSetCITypeActive();
  // The form builder (WP-316) belongs to the module metamodel (E-105); an
  // unknown module key keeps it visible. Layouts are rows of the
  // organization, so a system type offers it as well (WP-259).
  const formBuilderOn = useModules().isModuleEnabled('metamodel');
  const deleteDefinition = useDeleteAttributeDefinition();
  const reindex = useReindexAttributeDefinition();
  const [lastAction, setLastAction] = useState<'clone' | 'deactivate' | 'activate'>('clone');
  // The own definitions and the global ones that apply to the type (WP-255).
  const applicable = useApplicableAttributeDefinitions(type.id);
  // Of the ancestors only the inheritable definitions (WP-248).
  const ancestors = ancestorsOf(type.id, types);
  const ancestorDefinitions = useTypeAttributeDefinitions(ancestors.map((a) => a.id));
  // The attributes of the assigned active sets in the order of the
  // assignment (MET-16, WP-709): they follow the type chain.
  const assignment = useCITypeAttributeSets(type.id, {
    enabled: setsEnabled && !type.is_system,
  });
  const assignedSets = [...(assignment.data?.attribute_sets ?? [])]
    .sort((a, b) => a.position - b.position)
    .map((a) => sets.find((s) => s.id === a.attribute_set_id))
    .filter((s): s is AttributeSet => !!s && s.is_active);
  const setDefinitions = useSetAttributeDefinitions(
    assignedSets.map((s) => s.id),
    { enabled: setsEnabled },
  );
  const sensitive = useSensitiveCIFields();
  const draftItems = useActiveDraftItems();
  const typeChange = draftChangeFor(draftItems, { kinds: ['ci_type'], id: type.id });
  const newAttributes = draftCreations(
    draftItems,
    'attribute_definition',
    (payload) => payload.ci_type_id === type.id,
  );
  const chain = [type.id, ...ancestors.map((a) => a.id)];
  const marks = sensitive.data ?? [];
  const parent = types.find((ty) => ty.id === type.parent_type_id);

  // A system type (no organization) is read-only (E-10): the server refuses
  // its attributes (422 on ci_type_id) and its activation (403), so the page
  // offers neither but the clone, the subtype and the global attributes.
  const readOnly = type.is_system;
  const actionError = lastAction === 'clone' ? cloneType.error : (setActive.error ?? undefined);

  const all = applicable.data?.data ?? [];
  const own = all.filter((d) => d.scope === 'type' && d.ci_type_id === type.id).sort(byOrder);
  const globals = all.filter((d) => d.scope === 'global').sort(byOrder);
  const inherited = ancestors.flatMap((ancestor, i) =>
    (ancestorDefinitions[i]?.data?.data ?? [])
      .filter((d) => d.inheritable && d.ci_type_id === ancestor.id)
      .sort(byOrder)
      .map((d) => ({ definition: d, from: ancestor })),
  );
  const fromSets = assignedSets.flatMap((set, i) =>
    [...(setDefinitions[i]?.data?.data ?? [])].sort(byOrder).map((d) => ({ definition: d, set })),
  );
  const loading = applicable.isLoading || ancestorDefinitions.some((q) => q.isLoading);
  const failed =
    applicable.error ??
    ancestorDefinitions.find((q) => q.error)?.error ??
    setDefinitions.find((q) => q.error)?.error;
  const count = own.length + inherited.length + fromSets.length + globals.length;

  const remove = (d: AttributeDefinition) => {
    if (!window.confirm(t('citypes.attributes.deleteConfirm', { name: d.key }))) return;
    deleteDefinition.mutate(d);
  };

  return (
    <Card className="p-0">
      <div className="space-y-3 px-5 py-4">
        <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-3">
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <h3 className="text-lg font-semibold text-fg [overflow-wrap:anywhere]">
                {label(type)}
              </h3>
              {typeChange ? <DraftMarker change={typeChange} /> : null}
            </div>
            <p className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs text-fg-muted">
              <span className="font-mono">{type.key}</span>
              <span className="tabular-nums">v{type.version}</span>
              <span className="tabular-nums">
                {t('citypes.schemaVersion')} {type.schema_version}
              </span>
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {formBuilderOn ? (
              <Button asChild variant="secondary" size="sm">
                <Link to={`/ci-types/${type.id}/form-builder`}>
                  <LayoutDashboard aria-hidden="true" />
                  {t('formBuilder.open')}
                </Link>
              </Button>
            ) : null}
            {!readOnly ? (
              <Button variant="secondary" size="sm" icon={<Pencil />} onClick={onEdit}>
                {t('citypes.editType')}
              </Button>
            ) : null}
            <Button
              variant="secondary"
              size="sm"
              icon={<Copy />}
              disabled={cloneType.isPending}
              onClick={() => {
                setLastAction('clone');
                setActive.reset();
                // The clone is a type of the organization: select it so its
                // attributes can be added right away (the way out of a system type).
                cloneType.mutate(
                  { id: type.id },
                  {
                    onSuccess: (clone) => (isDraftItem(clone) ? undefined : onSelectType(clone.id)),
                  },
                );
              }}
            >
              {t('citypes.clone', 'Klonen')}
            </Button>
            {!readOnly ? (
              <Button
                variant="secondary"
                size="sm"
                icon={type.is_active ? <PowerOff /> : <Power />}
                className={type.is_active ? dangerAction : undefined}
                disabled={setActive.isPending}
                onClick={() => {
                  setLastAction(type.is_active ? 'deactivate' : 'activate');
                  cloneType.reset();
                  setActive.mutate({ id: type.id, active: !type.is_active });
                }}
              >
                {type.is_active
                  ? t('citypes.deactivate', 'Deaktivieren')
                  : t('citypes.activate', 'Aktivieren')}
              </Button>
            ) : null}
          </div>
        </div>
        {actionError ? (
          <p role="alert" className="text-sm text-danger">
            {describeActionError(lastAction, actionError, t)}
          </p>
        ) : null}
        {type.description ? <p className="text-sm text-fg-muted">{type.description}</p> : null}
        {parent ? (
          <p className="text-sm text-fg-muted">{t('citypes.parentOf', { name: label(parent) })}</p>
        ) : null}
        {readOnly ? (
          <div
            role="note"
            className="flex gap-3 rounded-lg border border-line bg-accent-soft p-3 text-sm text-fg"
          >
            <Info aria-hidden="true" className="mt-0.5 h-4 w-4 shrink-0 text-accent-strong" />
            <div>
              <p className="font-medium">{t('citypes.systemReadOnly')}</p>
              <ul className="mt-1 list-disc space-y-1 pl-5 text-fg-muted">
                <li>{t('citypes.systemReadOnlyClone')}</li>
                <li>{t('citypes.systemReadOnlyOrgFields')}</li>
              </ul>
            </div>
          </div>
        ) : null}
      </div>
      {setsEnabled ? (
        <div className="border-t border-line px-5 py-4">
          <TypeSetAssignment type={type} sets={sets} />
        </div>
      ) : null}
      <section className="border-t border-line px-5 py-4">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h4 className="text-sm font-semibold text-fg">
            {t('citypes.attributes.title')} ({count})
          </h4>
          {!readOnly ? (
            <Button variant="secondary" size="sm" icon={<Plus />} onClick={onAddAttribute}>
              {t('citypes.addField', 'Feld hinzufügen')}
            </Button>
          ) : null}
        </div>
        {loading ? <SkeletonList rows={3} label={t('app.loading')} className="mt-3" /> : null}
        {failed ? (
          <ErrorState
            className="mt-3"
            title={t('app.error')}
            retryLabel={t('common.retry')}
            onRetry={() => {
              void applicable.refetch();
              ancestorDefinitions.forEach((q) => void q.refetch());
              setDefinitions.forEach((q) => void q.refetch());
            }}
          />
        ) : null}
        {deleteDefinition.error ? (
          <p role="alert" className="mt-3 text-sm text-danger">
            {t('citypes.attributes.deleteFailed', {
              name: deleteDefinition.variables?.key,
              reason: describeFlagRefusal(deleteDefinition.error, t),
            })}
          </p>
        ) : null}
        {reindex.error ? (
          <p role="alert" className="mt-3 text-sm text-danger">
            {t('citypes.indexStatus.reindexFailed', {
              reason: describeFlagRefusal(reindex.error, t),
            })}
          </p>
        ) : null}
        <ul className="mt-1 divide-y divide-line">
          {own.map((d) => {
            const editable = !readOnly && !d.is_system;
            return (
              <AttributeRow
                key={d.id}
                definition={d}
                origin={{ kind: 'own' }}
                draftChange={attributeChange(draftItems, d)}
                sensitive={isSensitive(marks, d, chain)}
                onEdit={editable ? () => onEditAttribute(d) : undefined}
                editLabel={t('citypes.editFieldAction', { name: d.key })}
                onDelete={editable ? () => remove(d) : undefined}
                deleteLabel={t('citypes.deleteFieldAction', { name: d.key })}
                onReindex={editable ? () => reindex.mutate(d.id) : undefined}
                reindexing={reindex.isPending}
                t={t}
              />
            );
          })}
          {inherited.map(({ definition: d, from }) => (
            <AttributeRow
              key={`${from.id}:${d.id}`}
              definition={d}
              origin={{ kind: 'inherited', from }}
              sensitive={isSensitive(marks, d, chain)}
              onOpenOrigin={() => onSelectType(from.id)}
              t={t}
            />
          ))}
          {fromSets.map(({ definition: d, set }) => (
            <AttributeRow
              key={`set:${set.id}:${d.id}`}
              definition={d}
              origin={{ kind: 'set', set }}
              sensitive={isSensitive(marks, d, chain)}
              onOpenOrigin={() => onOpenSet(set.id)}
              t={t}
            />
          ))}
          {globals.map((d) => (
            <AttributeRow
              key={`global:${d.id}`}
              definition={d}
              origin={{ kind: 'global' }}
              draftChange={attributeChange(draftItems, d)}
              sensitive={isSensitive(marks, d, chain)}
              t={t}
            />
          ))}
          <DraftPlaceholders items={newAttributes} t={t} />
        </ul>
        {!loading && !failed && count === 0 && newAttributes.length === 0 ? (
          <p className="mt-3 text-sm text-fg-muted">{t('citypes.noFields')}</p>
        ) : null}
      </section>
    </Card>
  );
}

/**
 * GlobalAttributesSection lists the global attribute definitions
 * (GET /attribute-definitions?scope=global, MET-13): the organization's
 * apply to the types of their applicability (applies_to, WP-255), the system
 * ones to every type and stay read-only. Adding (POST without ci_type_id),
 * editing, deleting and rebuilding an index need ci_attribute:manage and are
 * offered only once /me/permissions confirmed it.
 */
function GlobalAttributesSection({
  types,
  label,
  onAdd,
  onEdit,
  t,
}: {
  types: CIType[];
  label: TypeLabel;
  onAdd: () => void;
  onEdit: (definition: AttributeDefinition) => void;
  t: TFunction;
}) {
  const definitions = useGlobalAttributeDefinitions();
  const deleteDefinition = useDeleteAttributeDefinition();
  const reindex = useReindexAttributeDefinition();
  const sensitive = useSensitiveCIFields();
  const canManage = useHasPermission('ci_attribute:manage');
  const draftItems = useActiveDraftItems();
  const newGlobals = draftCreations(
    draftItems,
    'attribute_definition',
    (payload) => !payload.ci_type_id && !payload.attribute_set_id,
  );
  const marks = sensitive.data ?? [];
  const list = [...(definitions.data?.data ?? [])].sort(
    (a, b) => Number(a.is_system) - Number(b.is_system) || byOrder(a, b),
  );
  const remove = (d: AttributeDefinition) => {
    if (!window.confirm(t('citypes.globals.deleteConfirm', { name: d.key }))) return;
    deleteDefinition.mutate(d);
  };
  const typeName = (id: string) => {
    const ty = types.find((x) => x.id === id);
    return ty ? label(ty) : id;
  };
  const appliesTo = (d: AttributeDefinition) => {
    if (d.applies_to === 'categories') {
      return t('citypes.appliesTo.badge.categories', {
        list: (d.applies_to_categories ?? []).join(', '),
      });
    }
    if (d.applies_to === 'types') {
      return t('citypes.appliesTo.badge.types', {
        list: (d.applies_to_type_ids ?? []).map(typeName).join(', '),
      });
    }
    return t('citypes.appliesTo.badge.all');
  };
  return (
    <div id="global-attributes">
      <Card
        title={t('citypes.globals.title')}
        actions={
          canManage ? (
            <Button size="sm" variant="secondary" icon={<Plus />} onClick={onAdd}>
              {t('citypes.globals.add')}
            </Button>
          ) : null
        }
      >
        <p className="text-sm text-fg-muted">{t('citypes.globals.hint')}</p>
        {canManage === false ? (
          <p className="mt-1 text-xs text-fg-muted">{t('citypes.globals.readOnly')}</p>
        ) : null}
        {definitions.isLoading ? <SkeletonList rows={2} label={t('app.loading')} /> : null}
        {definitions.error ? (
          <ErrorState
            title={t('app.error')}
            retryLabel={t('common.retry')}
            onRetry={() => void definitions.refetch()}
          />
        ) : null}
        {deleteDefinition.error ? (
          <p role="alert" className="mt-2 text-sm text-danger">
            {t('citypes.attributes.deleteFailed', {
              name: deleteDefinition.variables?.key,
              reason: describeFlagRefusal(deleteDefinition.error, t),
            })}
          </p>
        ) : null}
        {reindex.error ? (
          <p role="alert" className="mt-2 text-sm text-danger">
            {t('citypes.indexStatus.reindexFailed', {
              reason: describeFlagRefusal(reindex.error, t),
            })}
          </p>
        ) : null}
        <ul className="mt-2 divide-y divide-line">
          {list.map((d) => {
            const editable = !!canManage && !d.is_system;
            return (
              <AttributeRow
                key={d.id}
                definition={d}
                sensitive={isSensitive(marks, d, [])}
                draftChange={attributeChange(draftItems, d)}
                appliesTo={d.is_system ? undefined : appliesTo(d)}
                onEdit={editable ? () => onEdit(d) : undefined}
                editLabel={t('citypes.globals.editAction', { name: d.key })}
                onDelete={editable ? () => remove(d) : undefined}
                deleteLabel={t('citypes.globals.deleteAction', { name: d.key })}
                onReindex={editable ? () => reindex.mutate(d.id) : undefined}
                reindexing={reindex.isPending}
                t={t}
              />
            );
          })}
          <DraftPlaceholders items={newGlobals} t={t} />
          {definitions.isSuccess && list.length === 0 && newGlobals.length === 0 ? (
            <li className="py-2 text-sm text-fg-muted">{t('citypes.globals.empty')}</li>
          ) : null}
        </ul>
      </Card>
    </div>
  );
}

/**
 * AttributeSetsSection lists the attribute sets (GET /attribute-sets, MET-16,
 * WP-250): the organization's and the read-only system sets. A set is
 * created, edited (key only on creation) and deleted over
 * /attribute-sets/{id}; a 409 on deleting an assigned set shows at the set.
 * Its attributes are maintained in the AttributeSetEditor. With the module
 * metamodel switched off (E-105) a note takes the place of the section.
 */
function AttributeSetsSection({
  off,
  sets,
  loading,
  error,
  onRetry,
  openSetId,
  onOpenSet,
  t,
}: {
  off: boolean;
  sets: AttributeSet[];
  loading: boolean;
  error: unknown;
  onRetry: () => void;
  openSetId?: string;
  onOpenSet: (id: string | undefined) => void;
  t: TFunction;
}) {
  const { i18n } = useTranslation();
  const deleteSet = useDeleteAttributeSet();
  const draftItems = useActiveDraftItems();
  const [dialog, setDialog] = useState<SetDialogState | null>(null);
  const list = [...sets].sort(
    (a, b) =>
      Number(a.is_system) - Number(b.is_system) ||
      setLabel(a, i18n.language).localeCompare(setLabel(b, i18n.language)),
  );
  const open = list.find((s) => s.id === openSetId);

  if (off) {
    return (
      <div id="attribute-sets">
        <Card title={t('citypes.sets.title')}>
          <p className="text-sm text-fg-muted">{t('citypes.sets.moduleOff')}</p>
        </Card>
      </div>
    );
  }

  const remove = (set: AttributeSet) => {
    if (!window.confirm(t('citypes.sets.deleteConfirm', { name: setLabel(set, i18n.language) }))) {
      return;
    }
    deleteSet.mutate(set.id, {
      onSuccess: () => {
        if (openSetId === set.id) onOpenSet(undefined);
      },
    });
  };

  return (
    <div id="attribute-sets">
      <Card
        title={t('citypes.sets.title')}
        actions={
          <Button
            size="sm"
            variant="secondary"
            icon={<Plus />}
            onClick={() => setDialog({ mode: 'create' })}
          >
            {t('citypes.sets.create')}
          </Button>
        }
      >
        <p className="text-sm text-fg-muted">{t('citypes.sets.hint')}</p>
        {loading ? <SkeletonList rows={2} label={t('app.loading')} /> : null}
        {error ? (
          <ErrorState title={t('app.error')} retryLabel={t('common.retry')} onRetry={onRetry} />
        ) : null}
        <ul className="mt-2 divide-y divide-line">
          {list.map((set) => {
            const name = setLabel(set, i18n.language);
            const deleteError =
              deleteSet.error && deleteSet.variables === set.id ? deleteSet.error : null;
            return (
              <li key={set.id} className="space-y-1 py-3 text-sm">
                <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-2">
                  <div className="min-w-0 flex-1 basis-48 space-y-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-medium text-fg">{name}</span>
                      {name !== set.key ? (
                        <span className="font-mono text-xs text-fg-muted">{set.key}</span>
                      ) : null}
                      {set.is_system ? <Badge variant="info">{t('citypes.system')}</Badge> : null}
                      {!set.is_active ? (
                        <Badge variant="warning">{t('citypes.inactive')}</Badge>
                      ) : null}
                      <SetDraftMarker items={draftItems} id={set.id} />
                    </div>
                    {set.description ? (
                      <p className="text-xs text-fg-muted">{set.description}</p>
                    ) : null}
                  </div>
                  <div className="flex flex-wrap items-center gap-1">
                    <Button
                      variant="ghost"
                      size="sm"
                      className={linkAction}
                      aria-expanded={openSetId === set.id}
                      aria-label={t('citypes.sets.attributesAction', { name })}
                      onClick={() => onOpenSet(openSetId === set.id ? undefined : set.id)}
                    >
                      {t('citypes.sets.attributes')}
                    </Button>
                    {!set.is_system ? (
                      <>
                        <IconButton
                          size="sm"
                          icon={<Pencil />}
                          label={t('citypes.sets.editAction', { name })}
                          onClick={() => setDialog({ mode: 'edit', set })}
                        />
                        <IconButton
                          size="sm"
                          icon={<Trash2 />}
                          label={t('citypes.sets.deleteAction', { name })}
                          className={deleteIcon}
                          disabled={deleteSet.isPending}
                          onClick={() => remove(set)}
                        />
                      </>
                    ) : null}
                  </div>
                </div>
                {deleteError ? (
                  <p role="alert" className="text-sm text-danger">
                    {deleteError instanceof ApiError && deleteError.status === 409
                      ? t('citypes.sets.deleteAssigned', { name })
                      : t('citypes.sets.deleteFailed', {
                          name,
                          reason: describeFlagRefusal(deleteError, t),
                        })}
                  </p>
                ) : null}
              </li>
            );
          })}
          <DraftPlaceholders items={draftCreations(draftItems, 'attribute_set')} t={t} />
          {!loading && !error && list.length === 0 ? (
            <li className="py-2 text-sm text-fg-muted">{t('citypes.sets.empty')}</li>
          ) : null}
        </ul>
        {open ? (
          <AttributeSetEditor key={open.id} set={open} onClose={() => onOpenSet(undefined)} />
        ) : null}
      </Card>
      {dialog ? (
        <SetDialog
          state={dialog}
          onClose={() => setDialog(null)}
          onSaved={(saved) => {
            setDialog(null);
            if (!isDraftItem(saved)) onOpenSet(saved.id);
          }}
          t={t}
        />
      ) : null}
    </div>
  );
}

/**
 * SetDialog creates (POST /attribute-sets) or edits (PATCH
 * /attribute-sets/{id}) an attribute set: key (only on creation), labels
 * DE/EN, description and active. Refusals show at the field they name.
 */
function SetDialog({
  state,
  onClose,
  onSaved,
  t,
}: {
  state: SetDialogState;
  onClose: () => void;
  onSaved: (set: Drafted<AttributeSet>) => void;
  t: TFunction;
}) {
  const editing = state.mode === 'edit' ? state.set : undefined;
  const [draft, setDraft] = useState({
    key: editing?.key ?? '',
    de: editing?.display_name?.de ?? '',
    en: editing?.display_name?.en ?? '',
    description: editing?.description ?? '',
    active: editing?.is_active ?? true,
  });
  const createSet = useCreateAttributeSet();
  const updateSet = useUpdateAttributeSet();
  const mutation = editing ? updateSet : createSet;
  const error = mutation.error;
  const errors = fieldErrors(error);
  const shownAtField = ['key', 'display_name', 'description', 'is_active'].filter(
    (f) => errors[f],
  ).length;
  const set = (change: Partial<typeof draft>) => setDraft((d) => ({ ...d, ...change }));
  const displayName = () => ({
    ...(draft.de ? { de: draft.de } : {}),
    ...(draft.en ? { en: draft.en } : {}),
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!editing) {
      const body: AttributeSetCreate = { key: draft.key.trim(), is_active: draft.active };
      if (draft.de || draft.en) body.display_name = displayName();
      if (draft.description) body.description = draft.description;
      createSet.mutate(body, { onSuccess: onSaved });
      return;
    }
    const patch: AttributeSetPatch = {};
    if (
      draft.de !== (editing.display_name?.de ?? '') ||
      draft.en !== (editing.display_name?.en ?? '')
    ) {
      patch.display_name = displayName();
    }
    if (draft.description !== (editing.description ?? '')) patch.description = draft.description;
    if (draft.active !== editing.is_active) patch.is_active = draft.active;
    if (Object.keys(patch).length === 0) {
      onClose();
      return;
    }
    updateSet.mutate({ id: editing.id, patch }, { onSuccess: onSaved });
  };

  return (
    <Modal
      open
      onOpenChange={(isOpen) => (isOpen ? undefined : onClose())}
      title={
        editing ? t('citypes.sets.editTitle', { name: editing.key }) : t('citypes.sets.create')
      }
    >
      <form className="space-y-3" onSubmit={submit}>
        <Input
          label={t('citypes.sets.key')}
          value={draft.key}
          required={!editing}
          disabled={!!editing}
          readOnly={!!editing}
          onChange={(e) => set({ key: e.target.value })}
          error={errors.key}
        />
        {editing ? <p className="text-xs text-fg-muted">{t('citypes.sets.keyImmutable')}</p> : null}
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <Input
            label={t('citypes.labelDe')}
            value={draft.de}
            onChange={(e) => set({ de: e.target.value })}
            error={errors.display_name}
          />
          <Input
            label={t('citypes.labelEn')}
            value={draft.en}
            onChange={(e) => set({ en: e.target.value })}
          />
        </div>
        <Input
          label={t('citypes.description')}
          value={draft.description}
          onChange={(e) => set({ description: e.target.value })}
          error={errors.description}
        />
        <label className="flex items-center gap-2 text-sm text-fg">
          <input
            type="checkbox"
            checked={draft.active}
            onChange={(e) => set({ active: e.target.checked })}
          />
          {t('citypes.sets.active')}
        </label>
        {errors.is_active ? <p className="text-sm text-danger">{errors.is_active}</p> : null}
        {error ? (
          <p role="alert" className="text-sm text-danger">
            {shownAtField > 0
              ? t('citypes.typeErrors.checkFields')
              : t('citypes.sets.notSaved', { reason: describeFlagRefusal(error, t) })}
          </p>
        ) : null}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" disabled={mutation.isPending}>
            {editing ? t('common.save') : t('common.create')}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

/** SetDraftMarker marks an attribute set a draft changes or deletes. */
function SetDraftMarker({ items, id }: { items: readonly MetamodelDraftItem[]; id: string }) {
  const change = draftChangeFor(items, { kinds: ['attribute_set'], id });
  return change ? <DraftMarker change={change} /> : null;
}

/**
 * DraftTypes lists the types an active draft creates or clones (E-113):
 * they are not in the tree until the draft is published; the marks of the
 * changed types stand in their detail card.
 */
function DraftTypes({ types, label, t }: { types: CIType[]; label: TypeLabel; t: TFunction }) {
  const { i18n } = useTranslation();
  const created = draftCreations(useActiveDraftItems(), 'ci_type');
  if (created.length === 0) return null;
  const nameOf = (item: MetamodelDraftItem) => {
    const name = draftItemName(item, i18n.language);
    if (name) return name;
    const source = item.op === 'clone' ? types.find((ty) => ty.id === item.target_id) : undefined;
    return source
      ? t('metamodelDrafts.cloneOf', { name: label(source) })
      : t('metamodelDrafts.noTarget');
  };
  return (
    <Card title={t('metamodelDrafts.newParts')} className="p-4">
      <ul className="divide-y divide-line">
        {created.map((item) => (
          <li
            key={item.id}
            data-draft-item={item.id}
            className="flex flex-wrap items-center gap-2 py-2.5 text-sm"
          >
            <span className="font-medium text-fg">{nameOf(item)}</span>
            <DraftMarker change="created" />
          </li>
        ))}
      </ul>
    </Card>
  );
}
