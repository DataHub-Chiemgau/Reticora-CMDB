import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import {
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
  FlagUpdateError,
} from '../api/cmdbHooks';
import {
  localizedText,
  type AttributeDefinition,
  type AttributeIndexFlags,
  type CIType,
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
 * describeFlagRefusal renders the refusal of a flag update in the UI
 * language with the server's reason, e.g. the 409 of a unique flag while
 * CIs share a value (MET-15, F-159).
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
    if (error.status === 403) return t('citypes.flagErrors.forbidden');
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
  const setFlags = useSetAttributeIndexFlags();
  const deleteField = useDeleteCITypeField();
  const saveError = flagTarget ? setFlags.error : upsertField.error;
  const saving = upsertField.isPending || setFlags.isPending;

  const trackJob = (definitionId: string) =>
    setTrackedJobs((ids) => (ids.includes(definitionId) ? ids : [...ids, definitionId]));

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
              <p className="mt-0.5 text-xs text-gray-500">
                {ty.key} · v{ty.version} · {t('citypes.schemaVersion', 'Schema')}{' '}
                {ty.schema_version}
                {ty.category ? ` · ${ty.category}` : ''}
              </p>
            </button>
          ))}
        </div>

        <div className="lg:col-span-2">
          {selected ? (
            <CITypeDetail
              type={selected}
              onClone={() => cloneType.mutate({ id: selected.id })}
              onToggleActive={() =>
                setActive.mutate({ id: selected.id, active: !selected.is_active })
              }
              onAddField={() => {
                setFieldDraft({ name: '', data_type: 'text', ui_group: 'general' });
                setIndexDraft(NO_INDEX);
                setFlagTarget(null);
                upsertField.reset();
                setShowFieldEditor(true);
              }}
              onEditFlags={(def) => {
                setIndexDraft({
                  searchable: def.searchable,
                  unique: def.unique,
                  unique_scope: def.unique_scope,
                });
                setFlagTarget({ definitionId: def.id, name: def.key });
                setFlags.reset();
                setShowFieldEditor(true);
              }}
              trackedJobs={trackedJobs}
              onDeleteField={(name) => deleteField.mutate({ typeId: selected.id, name })}
              t={t}
            />
          ) : (
            <Card>
              <p className="text-sm text-gray-500">
                {t('citypes.select', 'Typ auswählen, um Felder zu bearbeiten.')}
              </p>
            </Card>
          )}
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
              <p className="font-medium">{label(tpl)}</p>
              <p className="text-xs text-gray-500">
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
            : t('citypes.addField', 'Feld hinzufügen')
        }
      >
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (!selected) return;
            if (flagTarget) {
              setFlags.mutate(
                { id: flagTarget.definitionId, typeId: selected.id, flags: indexDraft },
                {
                  onSuccess: (def) => {
                    trackJob(def.id);
                    setShowFieldEditor(false);
                  },
                },
              );
              return;
            }
            upsertField.mutate(
              {
                typeId: selected.id,
                field: fieldDraft,
                index: indexDraft.searchable || indexDraft.unique ? indexDraft : undefined,
              },
              {
                onSuccess: (saved) => {
                  if (saved.definition) trackJob(saved.definition.id);
                  setShowFieldEditor(false);
                },
              },
            );
          }}
        >
          {flagTarget ? (
            <p className="text-sm text-gray-600 dark:text-gray-300">{t('citypes.editFlagsHint')}</p>
          ) : (
            <FieldInputs draft={fieldDraft} onChange={setFieldDraft} t={t} />
          )}
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={indexDraft.searchable}
              onChange={(e) => setIndexDraft({ ...indexDraft, searchable: e.target.checked })}
            />
            {t('citypes.searchable', 'Durchsuchbar (indiziert)')}
          </label>
          <label className="flex items-center gap-2 text-sm">
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

/** The inputs of a new field: name, label, type, group, values, required. */
function FieldInputs({
  draft,
  onChange,
  t,
}: {
  draft: FieldDefinition;
  onChange: (draft: FieldDefinition) => void;
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
      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={!!draft.required}
          onChange={(e) => onChange({ ...draft, required: e.target.checked })}
        />
        {t('citypes.required', 'Pflichtfeld')}
      </label>
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
  const fields = type.fields ?? [];
  // The definitions carry the id and the index flags of the fields (MET-15).
  const definitions = useAttributeDefinitions(type.id);
  const definitionOf = (name: string) =>
    (definitions.data?.data ?? []).find((d) => d.key === name && !d.is_system);
  return (
    <Card
      title={localizedText(type.display_name, i18n.language) || type.name}
      actions={
        <div className="flex gap-2">
          <Button size="sm" variant="secondary" onClick={onClone}>
            {t('citypes.clone', 'Klonen')}
          </Button>
          <Button
            size="sm"
            variant={type.is_active ? 'danger' : 'primary'}
            onClick={onToggleActive}
          >
            {type.is_active
              ? t('citypes.deactivate', 'Deaktivieren')
              : t('citypes.activate', 'Aktivieren')}
          </Button>
        </div>
      }
    >
      {type.description ? (
        <p className="text-sm text-gray-600 dark:text-gray-300">{type.description}</p>
      ) : null}
      <div className="mt-4 flex items-center justify-between">
        <h3 className="text-sm font-semibold">
          {t('citypes.fields', 'Felder')} ({fields.length})
        </h3>
        <Button size="sm" variant="secondary" onClick={onAddField}>
          {t('citypes.addField', 'Feld hinzufügen')}
        </Button>
      </div>
      <ul className="mt-2 divide-y divide-gray-100 dark:divide-gray-800">
        {fields.map((f) => {
          const def = definitionOf(f.name);
          return (
            <li key={f.name} className="flex items-center justify-between py-2 text-sm">
              <div>
                <span className="font-medium">{f.label || f.name}</span>
                <span className="ml-2 text-xs text-gray-500">
                  {f.data_type}
                  {f.ui_group ? ` · ${f.ui_group}` : ''}
                </span>
                {f.required ? (
                  <Badge variant="warning" className="ml-2">
                    *
                  </Badge>
                ) : null}
                {f.conditional ? (
                  <Badge variant="info" className="ml-2">
                    {t('citypes.conditional', 'bedingt')}
                  </Badge>
                ) : null}
                {def?.searchable ? (
                  <Badge variant="neutral" className="ml-2">
                    {t('citypes.flagSearchable')}
                  </Badge>
                ) : null}
                {def?.unique ? (
                  <Badge variant="neutral" className="ml-2">
                    {def.unique_scope === 'client'
                      ? t('citypes.flagUniqueClient')
                      : t('citypes.flagUnique')}
                  </Badge>
                ) : null}
                {def && trackedJobs.includes(def.id) ? (
                  <IndexJobStatus definitionId={def.id} t={t} />
                ) : null}
              </div>
              <div className="flex gap-3">
                {def ? (
                  <button
                    className="text-xs text-primary hover:underline"
                    aria-label={t('citypes.editFieldAction', { name: f.name })}
                    onClick={() => onEditFlags(def)}
                  >
                    {t('common.edit', 'Bearbeiten')}
                  </button>
                ) : null}
                {!type.is_system ? (
                  <button
                    className="text-xs text-red-600 hover:underline"
                    onClick={() => onDeleteField(f.name)}
                  >
                    {t('common.delete', 'Löschen')}
                  </button>
                ) : null}
              </div>
            </li>
          );
        })}
        {fields.length === 0 ? (
          <li className="py-2 text-sm text-gray-500">
            {t('citypes.noFields', 'Keine Felder definiert.')}
          </li>
        ) : null}
      </ul>
    </Card>
  );
}
