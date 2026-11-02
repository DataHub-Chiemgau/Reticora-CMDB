import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import {
  useCIType,
  useCITypes,
  useCITypeTemplates,
  useCreateCIType,
  useCloneCIType,
  useSetCITypeActive,
  useUpsertCITypeField,
  useDeleteCITypeField,
  useAttributeDefinitions,
  useAttributeIndexJob,
  useSetAttributeIndexFlags,
  useGlobalFields,
  useGlobalAttributeDefinitions,
  useUpsertGlobalField,
  useDeleteGlobalField,
  FlagUpdateError,
} from '../api/cmdbHooks';
import { useHasPermission } from '../api/hooks';
import {
  localizedText,
  type AttributeDefinition,
  type AttributeIndexFlags,
  type CIType,
  type GlobalFieldDefinition,
} from '../api/cmdb';
import { ApiError } from '../api/client';
import type { FieldDefinition } from '../lib/fieldmeta';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { Input } from '../components/ui/Input';
import { Select } from '../components/ui/Select';
import { Modal } from '../components/ui/Modal';
import { SkeletonList } from '../components/ui/Skeleton';
import { ErrorState } from '../components/ui/ErrorState';

// Data types of MET-11: phase 1, then phase 2 (file, json, references).
export const FIELD_TYPES = [
  'text',
  'multiline',
  'integer',
  'decimal',
  'boolean',
  'date',
  'datetime',
  'enum',
  'multiselect',
  'ip',
  'mac',
  'url',
  'email',
  'uuid',
  'json',
  'file',
  'user',
  'team',
  'location',
  'ci_ref',
  'asset_ref',
  'contract_ref',
];

// A new field is neither indexed nor unique until the editor says so
// (MET-15); the flags start the index job (DB-04).
const NO_INDEX: AttributeIndexFlags = { searchable: false, unique: false, unique_scope: 'org' };

/** The existing definition whose flags the editor changes (F-325); none: a new field. */
interface FlagTarget {
  definitionId: string;
  name: string;
}

/**
 * What the field editor writes: a field of the selected CI type or an
 * organization-wide field for every CI type (PUT /ci-fields/global, MET-13).
 */
type FieldScope = 'type' | 'global';

// The 403 detail of the route authorization ("missing required permission:
// citype:manage") and of a write to a system attribute definition.
const MISSING_PERMISSION = 'missing required permission';
const SYSTEM_DEFINITION_READ_ONLY = 'system attribute definitions are read-only';

/**
 * describeFlagRefusal renders the refusal of a field or flag save in the UI
 * language with the server's reason, e.g. the 409 of a unique flag while
 * CIs share a value (MET-15, F-159). A system CI type (422 on ci_type_id)
 * and a system definition (403 read-only) are read-only objects, not a
 * missing permission: "Keine Berechtigung" is only the 403 of the route
 * authorization.
 */
function describeFlagRefusal(error: unknown, t: TFunction): string {
  if (error instanceof ApiError) {
    const raw = error.detail || error.message;
    // A FieldProblem's detail is "<field>: <message>".
    const detail =
      error.field && raw.startsWith(`${error.field}: `) ? raw.slice(error.field.length + 2) : raw;
    if (error.status === 409 && error.field === 'unique') {
      return t('citypes.flagErrors.duplicates', { detail });
    }
    if (error.status === 422 && error.field === 'ci_type_id') {
      return t('citypes.flagErrors.systemType');
    }
    if (error.status === 403) {
      if (raw.startsWith(MISSING_PERMISSION)) {
        const permission = raw.slice(MISSING_PERMISSION.length).replace(/^:\s*/, '').trim();
        return permission
          ? t('citypes.flagErrors.forbiddenPermission', { permission })
          : t('citypes.flagErrors.forbidden');
      }
      if (raw === SYSTEM_DEFINITION_READ_ONLY) return t('citypes.flagErrors.readOnly');
      return t('citypes.flagErrors.reason', { detail });
    }
    if (error.status === 404) return t('citypes.flagErrors.notFound');
    return t('citypes.flagErrors.reason', { detail });
  }
  return t('citypes.flagErrors.reason', {
    detail: error instanceof Error ? error.message : String(error),
  });
}

/** describeSaveError tells a refused field from a saved field whose flags were refused. */
function describeSaveError(error: unknown, editing: boolean, t: TFunction): string {
  if (error instanceof FlagUpdateError) {
    return t('citypes.flagErrors.partial', {
      name: error.field.name,
      reason: describeFlagRefusal(error.refusal, t),
    });
  }
  return editing
    ? t('citypes.flagErrors.flagsNotSaved', { reason: describeFlagRefusal(error, t) })
    : t('citypes.flagErrors.fieldNotSaved', { reason: describeFlagRefusal(error, t) });
}

export function CITypeAdminPage() {
  const { t, i18n } = useTranslation();
  const label = (ty: CIType) => localizedText(ty.display_name, i18n.language) || ty.name;
  const [search, setSearch] = useState('');
  const [showInactive, setShowInactive] = useState(false);
  const [selectedId, setSelectedId] = useState<string | undefined>();
  const [showTemplates, setShowTemplates] = useState(false);
  const [showFieldEditor, setShowFieldEditor] = useState(false);
  const [newTypeName, setNewTypeName] = useState('');
  const [showCreate, setShowCreate] = useState(false);
  const [fieldDraft, setFieldDraft] = useState<FieldDefinition>({ name: '', data_type: 'text' });
  const [indexDraft, setIndexDraft] = useState<AttributeIndexFlags>(NO_INDEX);
  const [flagTarget, setFlagTarget] = useState<FlagTarget | null>(null);
  const [fieldScope, setFieldScope] = useState<FieldScope>('type');
  // Definitions whose index job the field list follows after a flag change.
  const [trackedJobs, setTrackedJobs] = useState<string[]>([]);

  const { data, isLoading, error, refetch } = useCITypes({
    search,
    include_inactive: showInactive,
  });
  const templates = useCITypeTemplates();
  const createType = useCreateCIType();
  const cloneType = useCloneCIType();
  const setActive = useSetCITypeActive();
  const upsertField = useUpsertCITypeField();
  const upsertGlobalField = useUpsertGlobalField();
  const setFlags = useSetAttributeIndexFlags();
  const deleteField = useDeleteCITypeField();
  const saveError = flagTarget
    ? setFlags.error
    : fieldScope === 'global'
      ? upsertGlobalField.error
      : upsertField.error;
  const saving = upsertField.isPending || upsertGlobalField.isPending || setFlags.isPending;

  const trackJob = (definitionId: string) =>
    setTrackedJobs((ids) => (ids.includes(definitionId) ? ids : [...ids, definitionId]));

  /** openNewField opens the editor on an empty field of the type or of the organization. */
  const openNewField = (scope: FieldScope) => {
    setFieldDraft({ name: '', data_type: 'text', ui_group: 'general' });
    setIndexDraft(NO_INDEX);
    setFlagTarget(null);
    setFieldScope(scope);
    upsertField.reset();
    upsertGlobalField.reset();
    setShowFieldEditor(true);
  };

  /** openFlagEditor opens the editor on the index flags of an existing definition (F-325). */
  const openFlagEditor = (def: AttributeDefinition, scope: FieldScope) => {
    setIndexDraft({
      searchable: def.searchable,
      unique: def.unique,
      unique_scope: def.unique_scope,
    });
    setFlagTarget({ definitionId: def.id, name: def.key });
    setFieldScope(scope);
    setFlags.reset();
    setShowFieldEditor(true);
  };

  const selected = (data?.data ?? []).find((ty) => ty.id === selectedId);

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

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">
            {t('citypes.title', 'CI-Typen')}
          </h2>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">
            {t(
              'citypes.subtitle',
              'Metadaten-getriebene Typen: Felder, Gruppen, Validierung, Lebenszyklus.',
            )}
          </p>
        </div>
        <div className="flex gap-2">
          <Button size="sm" variant="secondary" onClick={() => setShowTemplates(true)}>
            {t('citypes.fromTemplate', 'Aus Vorlage')}
          </Button>
          <Button size="sm" onClick={() => setShowCreate(true)}>
            {t('citypes.create', 'Typ anlegen')}
          </Button>
        </div>
      </div>

      <Card>
        <div className="flex flex-wrap items-center gap-3">
          <Input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t('common.search', 'Suchen')}
            className="max-w-xs"
            aria-label={t('common.search', 'Suchen')}
          />
          <label className="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
            <input
              type="checkbox"
              checked={showInactive}
              onChange={(e) => setShowInactive(e.target.checked)}
            />
            {t('citypes.showInactive', 'Deaktivierte anzeigen')}
          </label>
        </div>
      </Card>

      {isLoading ? <SkeletonList rows={4} label={t('app.loading')} /> : null}
      {error ? (
        <ErrorState
          title={t('app.error')}
          retryLabel={t('common.retry')}
          onRetry={() => void refetch()}
        />
      ) : null}

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
        <div className="space-y-2 lg:col-span-1">
          {(data?.data ?? []).map((ty) => (
            <button
              key={ty.id}
              onClick={() => setSelectedId(ty.id)}
              className={`w-full rounded-lg border px-4 py-3 text-left transition ${
                selectedId === ty.id
                  ? 'border-primary bg-primary/5'
                  : 'border-gray-200 hover:border-gray-300 dark:border-gray-700'
              }`}
            >
              <div className="flex items-center justify-between">
                <span className="font-medium text-gray-900 dark:text-gray-100">{label(ty)}</span>
                <div className="flex gap-1">
                  {ty.is_system ? <Badge variant="info">{t('citypes.system')}</Badge> : null}
                  {!ty.is_active ? (
                    <Badge variant="warning">{t('citypes.inactive', 'deaktiviert')}</Badge>
                  ) : null}
                  {ty.is_logical ? (
                    <Badge variant="neutral">{t('citypes.logical', 'logisch')}</Badge>
                  ) : null}
                  {ty.is_observable ? (
                    <Badge variant="neutral">{t('citypes.observable', 'beobachtet')}</Badge>
                  ) : null}
                </div>
              </div>
              <p className="mt-0.5 text-xs text-muted">
                {ty.key} · v{ty.version} · {t('citypes.schemaVersion', 'Schema')}{' '}
                {ty.schema_version}
                {ty.category ? ` · ${ty.category}` : ''}
              </p>
            </button>
          ))}
        </div>

        <div className="space-y-4 lg:col-span-2">
          {selected ? (
            <CITypeDetail
              type={selected}
              // The clone is a type of the organization: select it so its
              // fields can be added right away (the way out of a system type).
              onClone={() =>
                cloneType.mutate(
                  { id: selected.id },
                  { onSuccess: (clone) => setSelectedId(clone.id) },
                )
              }
              onToggleActive={() =>
                setActive.mutate({ id: selected.id, active: !selected.is_active })
              }
              onAddField={() => openNewField('type')}
              onEditFlags={(def) => openFlagEditor(def, 'type')}
              trackedJobs={trackedJobs}
              onDeleteField={(name) => deleteField.mutate({ typeId: selected.id, name })}
              t={t}
            />
          ) : (
            <Card>
              <p className="text-sm text-muted">
                {t('citypes.select', 'Typ auswählen, um Felder zu bearbeiten.')}
              </p>
            </Card>
          )}
          <OrgFieldsSection
            onAddField={() => openNewField('global')}
            onEditFlags={(def) => openFlagEditor(def, 'global')}
            trackedJobs={trackedJobs}
            t={t}
          />
        </div>
      </div>

      {/* Templates modal */}
      <Modal
        open={showTemplates}
        onOpenChange={setShowTemplates}
        title={t('citypes.templates', 'Typvorlagen')}
      >
        <div className="grid max-h-96 grid-cols-1 gap-2 overflow-y-auto md:grid-cols-2">
          {(templates.data?.data ?? []).map((tpl) => (
            <button
              key={tpl.key}
              onClick={() => instantiateTemplate(tpl)}
              className="rounded-lg border border-gray-200 px-3 py-2 text-left hover:border-primary dark:border-gray-700"
            >
              <p className="font-medium text-gray-900 dark:text-gray-100">{label(tpl)}</p>
              <p className="text-xs text-muted">
                {/* eslint-disable-next-line no-restricted-syntax -- GLO-10: the template category is free metamodel text (ci_type.category, no enum), not a catalog key */}
                {tpl.category}
                {tpl.is_logical ? ` · ${t('citypes.logicalShort')}` : ''} ·{' '}
                {t('citypes.fieldCount', { count: (tpl.fields ?? []).length })}
              </p>
            </button>
          ))}
        </div>
      </Modal>

      {/* Create modal */}
      <Modal
        open={showCreate}
        onOpenChange={setShowCreate}
        title={t('citypes.create', 'Typ anlegen')}
      >
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            createType.mutate(
              { name: newTypeName },
              {
                onSuccess: () => {
                  setShowCreate(false);
                  setNewTypeName('');
                },
              },
            );
          }}
        >
          <Input
            label={t('citypes.name', 'Name')}
            value={newTypeName}
            onChange={(e) => setNewTypeName(e.target.value)}
            required
          />
          <div className="flex justify-end gap-2">
            <Button type="button" variant="secondary" onClick={() => setShowCreate(false)}>
              {t('common.cancel', 'Abbrechen')}
            </Button>
            <Button type="submit" disabled={createType.isPending}>
              {t('common.create', 'Anlegen')}
            </Button>
          </div>
        </form>
      </Modal>

      {/* Field editor modal: a new field, or the index flags of an existing one */}
      <Modal
        open={showFieldEditor}
        onOpenChange={setShowFieldEditor}
        title={
          flagTarget
            ? t('citypes.editField', { name: flagTarget.name })
            : fieldScope === 'global'
              ? t('citypes.orgFields.add')
              : t('citypes.addField', 'Feld hinzufügen')
        }
      >
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            const typeId = fieldScope === 'type' ? selected?.id : undefined;
            if (fieldScope === 'type' && !typeId) return;
            if (flagTarget) {
              setFlags.mutate(
                { id: flagTarget.definitionId, typeId, flags: indexDraft },
                {
                  onSuccess: (def) => {
                    trackJob(def.id);
                    setShowFieldEditor(false);
                  },
                },
              );
              return;
            }
            const index = indexDraft.searchable || indexDraft.unique ? indexDraft : undefined;
            const onSuccess = (saved: { definition?: AttributeDefinition }) => {
              if (saved.definition) trackJob(saved.definition.id);
              setShowFieldEditor(false);
            };
            if (typeId) {
              upsertField.mutate({ typeId, field: fieldDraft, index }, { onSuccess });
            } else {
              // Organization-wide fields stay optional: the server enforces a
              // required one on every CI write (ResolveFields org layer), but the
              // CI form renders type fields only, so nobody could fill it in.
              upsertGlobalField.mutate(
                { field: { ...fieldDraft, required: false }, index },
                { onSuccess },
              );
            }
          }}
        >
          {flagTarget ? (
            <p className="text-sm text-gray-600 dark:text-gray-300">{t('citypes.editFlagsHint')}</p>
          ) : (
            <>
              {fieldScope === 'global' ? (
                <p className="text-sm text-gray-600 dark:text-gray-300">
                  {t('citypes.orgFields.editorHint')}
                </p>
              ) : null}
              <FieldInputs
                draft={fieldDraft}
                onChange={setFieldDraft}
                allowRequired={fieldScope !== 'global'}
                t={t}
              />
            </>
          )}
          <label className="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-200">
            <input
              type="checkbox"
              checked={indexDraft.searchable}
              onChange={(e) => setIndexDraft({ ...indexDraft, searchable: e.target.checked })}
            />
            {t('citypes.searchable', 'Durchsuchbar (indiziert)')}
          </label>
          <label className="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-200">
            <input
              type="checkbox"
              checked={indexDraft.unique}
              onChange={(e) => setIndexDraft({ ...indexDraft, unique: e.target.checked })}
            />
            {t('citypes.unique', 'Eindeutig')}
          </label>
          {indexDraft.unique ? (
            <Select
              label={t('citypes.uniqueScope', 'Eindeutig je')}
              options={[
                { value: 'org', label: t('citypes.uniqueScopeOrg', 'Organisation') },
                { value: 'client', label: t('citypes.uniqueScopeClient', 'Kunde') },
              ]}
              value={indexDraft.unique_scope}
              onChange={(e) =>
                setIndexDraft({
                  ...indexDraft,
                  unique_scope: e.target.value as AttributeIndexFlags['unique_scope'],
                })
              }
            />
          ) : null}
          {saveError ? (
            <p role="alert" className="text-sm text-red-600 dark:text-red-400">
              {describeSaveError(saveError, !!flagTarget, t)}
            </p>
          ) : null}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="secondary" onClick={() => setShowFieldEditor(false)}>
              {t('common.cancel', 'Abbrechen')}
            </Button>
            <Button type="submit" disabled={saving}>
              {t('common.save', 'Speichern')}
            </Button>
          </div>
        </form>
      </Modal>
    </div>
  );
}

/**
 * The inputs of a new field: name, label, type, group, values and, unless
 * allowRequired is false (organization-wide fields), required.
 */
function FieldInputs({
  draft,
  onChange,
  allowRequired = true,
  t,
}: {
  draft: FieldDefinition;
  onChange: (draft: FieldDefinition) => void;
  allowRequired?: boolean;
  t: TFunction;
}) {
  return (
    <>
      <Input
        label={t('citypes.fieldName', 'Feldname')}
        value={draft.name}
        required
        onChange={(e) => onChange({ ...draft, name: e.target.value })}
      />
      <Input
        label={t('citypes.fieldLabel', 'Anzeigename')}
        value={draft.label ?? ''}
        onChange={(e) => onChange({ ...draft, label: e.target.value })}
      />
      <Select
        label={t('citypes.fieldType', 'Typ')}
        options={FIELD_TYPES.map((ft) => ({
          value: ft,
          label: t(`citypes.dataTypes.${ft}`, ft),
        }))}
        value={draft.data_type}
        onChange={(e) =>
          onChange({
            ...draft,
            data_type: e.target.value as FieldDefinition['data_type'],
          })
        }
      />
      <Input
        label={t('citypes.fieldGroup', 'Gruppe')}
        value={draft.ui_group ?? ''}
        onChange={(e) => onChange({ ...draft, ui_group: e.target.value })}
      />
      {draft.data_type === 'enum' || draft.data_type === 'multi_enum' ? (
        <Input
          label={t('citypes.enumValues', 'Werte (kommagetrennt)')}
          value={(draft.enum_values ?? []).join(', ')}
          onChange={(e) =>
            onChange({
              ...draft,
              enum_values: e.target.value
                .split(',')
                .map((s) => s.trim())
                .filter(Boolean),
            })
          }
        />
      ) : null}
      {allowRequired ? (
        <label className="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-200">
          <input
            type="checkbox"
            checked={!!draft.required}
            onChange={(e) => onChange({ ...draft, required: e.target.checked })}
          />
          {t('citypes.required', 'Pflichtfeld')}
        </label>
      ) : null}
    </>
  );
}

/** IndexJobStatus shows the newest index job of a definition with its status (DB-04). */
function IndexJobStatus({ definitionId, t }: { definitionId: string; t: TFunction }) {
  const job = useAttributeIndexJob(definitionId);
  const status = job.data?.status;
  const variant =
    status === 'succeeded'
      ? 'success'
      : status === 'failed' || status === 'cancelled'
        ? 'danger'
        : 'info';
  return (
    <span role="status" className="ml-2" title={job.data?.error || undefined}>
      <Badge variant={variant}>
        {status
          ? t('citypes.indexJob.label', { status: t(`citypes.indexJob.status.${status}`) })
          : t('citypes.indexJob.scheduled')}
      </Badge>
    </span>
  );
}

/**
 * FieldRow is a field of a list: its label, data type, group and flags, and
 * the actions the list offers for it (none: read-only).
 */
function FieldRow({
  field,
  definition,
  system,
  tracked,
  onEdit,
  editLabel,
  onDelete,
  deleteLabel,
  t,
}: {
  field: FieldDefinition;
  definition?: AttributeDefinition;
  /** A system field (no organization): marked and read-only. */
  system?: boolean;
  tracked: boolean;
  onEdit?: () => void;
  editLabel: string;
  onDelete?: () => void;
  deleteLabel?: string;
  t: TFunction;
}) {
  return (
    <li className="flex items-center justify-between py-2 text-sm">
      <div>
        <span className="font-medium">{field.label || field.name}</span>
        <span className="ml-2 text-xs text-muted">
          {field.data_type}
          {field.ui_group ? ` · ${field.ui_group}` : ''}
        </span>
        {system ? (
          <Badge variant="info" className="ml-2">
            {t('citypes.system')}
          </Badge>
        ) : null}
        {field.required ? (
          <Badge variant="warning" className="ml-2">
            *
          </Badge>
        ) : null}
        {field.conditional ? (
          <Badge variant="info" className="ml-2">
            {t('citypes.conditional', 'bedingt')}
          </Badge>
        ) : null}
        {definition?.searchable ? (
          <Badge variant="neutral" className="ml-2">
            {t('citypes.flagSearchable')}
          </Badge>
        ) : null}
        {definition?.unique ? (
          <Badge variant="neutral" className="ml-2">
            {definition.unique_scope === 'client'
              ? t('citypes.flagUniqueClient')
              : t('citypes.flagUnique')}
          </Badge>
        ) : null}
        {definition && tracked ? <IndexJobStatus definitionId={definition.id} t={t} /> : null}
      </div>
      <div className="flex gap-3">
        {onEdit ? (
          <button
            className="text-xs text-primary hover:underline"
            aria-label={editLabel}
            onClick={onEdit}
          >
            {t('common.edit', 'Bearbeiten')}
          </button>
        ) : null}
        {onDelete ? (
          <button
            className="text-xs text-danger hover:underline"
            aria-label={deleteLabel}
            onClick={onDelete}
          >
            {t('common.delete', 'Löschen')}
          </button>
        ) : null}
      </div>
    </li>
  );
}

function CITypeDetail({
  type,
  onClone,
  onToggleActive,
  onAddField,
  onEditFlags,
  trackedJobs,
  onDeleteField,
  t,
}: {
  type: CIType;
  onClone: () => void;
  onToggleActive: () => void;
  onAddField: () => void;
  onEditFlags: (definition: AttributeDefinition) => void;
  trackedJobs: string[];
  onDeleteField: (name: string) => void;
  t: TFunction;
}) {
  const { i18n } = useTranslation();
  // GET /ci-types lists the types without their fields; the fields come with the
  // single type (GET /ci-types/{id}), which a field or flag change invalidates.
  const detail = useCIType(type.id);
  const fields = detail.data?.fields ?? [];
  // The definitions carry the id and the index flags of the fields (MET-15).
  const definitions = useAttributeDefinitions(type.id);
  const definitionOf = (name: string) =>
    (definitions.data?.data ?? []).find((d) => d.key === name && !d.is_system);
  // A system type (no organization) is read-only for the organization until
  // inheritance (MET-06, P2): the server refuses fields on it (422 on
  // ci_type_id) and its activation, so the page offers neither but the two
  // ways that work: a clone, and the organization-wide fields below.
  const readOnly = type.is_system;
  return (
    <Card
      title={localizedText(type.display_name, i18n.language) || type.name}
      actions={
        <div className="flex gap-2">
          <Button size="sm" variant="secondary" onClick={onClone}>
            {t('citypes.clone', 'Klonen')}
          </Button>
          {!readOnly ? (
            <Button
              size="sm"
              variant={type.is_active ? 'danger' : 'primary'}
              onClick={onToggleActive}
            >
              {type.is_active
                ? t('citypes.deactivate', 'Deaktivieren')
                : t('citypes.activate', 'Aktivieren')}
            </Button>
          ) : null}
        </div>
      }
    >
      {type.description ? (
        <p className="text-sm text-gray-600 dark:text-gray-300">{type.description}</p>
      ) : null}
      {readOnly ? (
        <div
          role="note"
          className="mt-4 rounded-lg border border-blue-200 bg-blue-50 p-3 text-sm text-blue-900 dark:border-blue-800 dark:bg-blue-950/40 dark:text-blue-100"
        >
          <p>{t('citypes.systemReadOnly')}</p>
          <ul className="mt-1 list-disc space-y-1 pl-5">
            <li>{t('citypes.systemReadOnlyClone')}</li>
            <li>{t('citypes.systemReadOnlyOrgFields')}</li>
          </ul>
        </div>
      ) : null}
      <div className="mt-4 flex items-center justify-between">
        <h3 className="text-sm font-semibold">
          {t('citypes.fields', 'Felder')} ({fields.length})
        </h3>
        {!readOnly ? (
          <Button size="sm" variant="secondary" onClick={onAddField}>
            {t('citypes.addField', 'Feld hinzufügen')}
          </Button>
        ) : null}
      </div>
      {detail.isLoading ? <SkeletonList rows={3} label={t('app.loading')} /> : null}
      {detail.error ? (
        <ErrorState
          title={t('app.error')}
          retryLabel={t('common.retry')}
          onRetry={() => void detail.refetch()}
        />
      ) : null}
      <ul className="mt-2 divide-y divide-gray-100 dark:divide-gray-800">
        {fields.map((f) => {
          const def = definitionOf(f.name);
          return (
            <FieldRow
              key={f.name}
              field={f}
              definition={def}
              tracked={!!def && trackedJobs.includes(def.id)}
              onEdit={def ? () => onEditFlags(def) : undefined}
              editLabel={t('citypes.editFieldAction', { name: f.name })}
              onDelete={!readOnly ? () => onDeleteField(f.name) : undefined}
              t={t}
            />
          );
        })}
        {detail.isSuccess && fields.length === 0 ? (
          <li className="py-2 text-sm text-muted">
            {t('citypes.noFields', 'Keine Felder definiert.')}
          </li>
        ) : null}
      </ul>
    </Card>
  );
}

/**
 * OrgFieldsSection lists the global fields (GET /ci-fields/global, MET-13):
 * the organization-wide ones apply to every CI type of the organization,
 * the system types and the CIs discovery finds included, which a clone of
 * a system type does not reach. Adding, changing the index flags and
 * deleting need ci_attribute:manage and are offered only once
 * /me/permissions confirmed it; system fields stay read-only.
 */
function OrgFieldsSection({
  onAddField,
  onEditFlags,
  trackedJobs,
  t,
}: {
  onAddField: () => void;
  onEditFlags: (definition: AttributeDefinition) => void;
  trackedJobs: string[];
  t: TFunction;
}) {
  const globals = useGlobalFields();
  const definitions = useGlobalAttributeDefinitions();
  const deleteField = useDeleteGlobalField();
  const canManage = useHasPermission('ci_attribute:manage');
  const fields: GlobalFieldDefinition[] = globals.data?.data ?? [];
  const definitionOf = (name: string) =>
    (definitions.data?.data ?? []).find((d) => d.key === name && !d.is_system);
  const remove = (name: string) => {
    if (!window.confirm(t('citypes.orgFields.deleteConfirm', { name }))) return;
    deleteField.mutate(name);
  };
  return (
    <Card
      title={t('citypes.orgFields.title')}
      actions={
        canManage ? (
          <Button size="sm" variant="secondary" onClick={onAddField}>
            {t('citypes.orgFields.add')}
          </Button>
        ) : null
      }
    >
      <p className="text-sm text-gray-600 dark:text-gray-300">{t('citypes.orgFields.hint')}</p>
      {canManage === false ? (
        <p className="mt-1 text-xs text-muted">{t('citypes.orgFields.readOnly')}</p>
      ) : null}
      {globals.isLoading ? <SkeletonList rows={2} label={t('app.loading')} /> : null}
      {globals.error ? (
        <ErrorState
          title={t('app.error')}
          retryLabel={t('common.retry')}
          onRetry={() => void globals.refetch()}
        />
      ) : null}
      {deleteField.error ? (
        <p role="alert" className="mt-2 text-sm text-red-600 dark:text-red-400">
          {t('citypes.orgFields.deleteFailed', {
            name: deleteField.variables,
            reason: describeFlagRefusal(deleteField.error, t),
          })}
        </p>
      ) : null}
      <ul className="mt-2 divide-y divide-gray-100 dark:divide-gray-800">
        {fields.map((f) => {
          const system = !!f.is_system;
          const def = system ? undefined : definitionOf(f.name);
          return (
            <FieldRow
              key={`${system ? 'system' : 'org'}:${f.name}`}
              field={f}
              definition={def}
              system={system}
              tracked={!!def && trackedJobs.includes(def.id)}
              onEdit={canManage && def ? () => onEditFlags(def) : undefined}
              editLabel={t('citypes.orgFields.editAction', { name: f.name })}
              onDelete={canManage && !system ? () => remove(f.name) : undefined}
              deleteLabel={t('citypes.orgFields.deleteAction', { name: f.name })}
              t={t}
            />
          );
        })}
        {globals.isSuccess && fields.length === 0 ? (
          <li className="py-2 text-sm text-muted">{t('citypes.orgFields.empty')}</li>
        ) : null}
      </ul>
    </Card>
  );
}
