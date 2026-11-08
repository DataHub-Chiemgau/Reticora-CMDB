/**
 * RelationshipTypesPage administers the relationship types (REL-01, REL-07,
 * UI-14, WP-323): it lists the system and organization types of
 * GET /relationship-types and creates, edits and deletes the organization
 * types with all REL-01 fields and their attribute schema (metadata_schema,
 * WP-295). System types are read-only, as on the server since WP-127: they
 * offer neither edit nor delete. A refusal shows in the dialog: 409 for a
 * taken key, a system key or a type in use, 400 for an invalid definition,
 * 403 for a missing relationship_type:manage.
 */
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { GitFork, Plus, Trash2 } from 'lucide-react';
import { ApiError } from '../api/client';
import {
  localizedText,
  type CIType,
  type RelationshipAttributeDefinition,
  type RelationshipImpactDirection,
  type RelationshipType,
  type RelationshipTypeCardinality,
  type RelationshipTypeCategory,
  type RelationshipTypeUpsert,
  validationRulesOf,
} from '../api/cmdb';
import {
  useAllCITypes,
  useCreateRelationshipType,
  useDeleteRelationshipType,
  useRelationshipTypes,
  useUpdateRelationshipType,
} from '../api/cmdbHooks';
import { fieldErrors } from '../components/metamodel/AttributeEditor';
import { ScrollRegion } from '../components/ScrollRegion';
import { Badge } from '../components/ui/Badge';
import { Button, IconButton } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import {
  tableClasses,
  tdClasses,
  tdMonoClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';

/** The categories of a relationship type (REL-01, RelationshipTypeCategory). */
export const RELATIONSHIP_CATEGORIES = [
  'network',
  'power',
  'hosting',
  'logical',
  'physical',
] as const satisfies readonly RelationshipTypeCategory[];

/** Which end depends on the other (REL-01, RelationshipImpactDirection). */
export const IMPACT_DIRECTIONS = [
  'source_depends_on_target',
  'target_depends_on_source',
  'none',
  'undirected',
] as const satisfies readonly RelationshipImpactDirection[];

/** The cardinalities POST /relationships enforces (REL-06, RelationshipTypeCardinality). */
export const CARDINALITIES = [
  'many_to_many',
  'one_to_one',
  'one_to_many',
  'many_to_one',
] as const satisfies readonly RelationshipTypeCardinality[];

/**
 * The data types of an edge attribute: the phase 1 types of MET-11, the
 * values of FIELD_TYPES (CITypeAdminPage) WP-295 accepts. json, file and
 * the reference types are 400 on the server and not offered.
 */
export const RELATIONSHIP_ATTRIBUTE_TYPES = [
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
] as const satisfies readonly RelationshipAttributeDefinition['data_type'][];

type AttributeType = (typeof RELATIONSHIP_ATTRIBUTE_TYPES)[number];

const ENUM_TYPES = new Set<string>(['enum', 'multiselect']);
const NUMBER_TYPES = new Set<string>(['integer', 'decimal']);
const LENGTH_TYPES = new Set<string>(['text', 'multiline', 'url', 'email']);

// The 409 details of the server (relationshiptype.ErrConflict, ErrInUse).
const KEY_TAKEN = 'a relationship type with this key already exists';
const IN_USE = 'the relationship type is used by relationships or suppressions';

/** One definition of the attribute schema as the editor holds it. */
interface AttributeDraft {
  key: string;
  de: string;
  en: string;
  data_type: AttributeType;
  required: boolean;
  default: string;
  enum_values: string;
  min: string;
  max: string;
  min_length: string;
  max_length: string;
  pattern: string;
  /** conditional is kept as it is; the editor does not change it. */
  conditional?: RelationshipAttributeDefinition['conditional'];
}

interface TypeDraft {
  key: string;
  display_name: string;
  reverse_name: string;
  directed: boolean;
  category: RelationshipTypeCategory;
  impact_direction: RelationshipImpactDirection;
  redundant: boolean;
  allowed_source_types: string[];
  allowed_target_types: string[];
  cardinality: RelationshipTypeCardinality;
  description: string;
  metadata_schema: AttributeDraft[];
}

const splitList = (value: string) =>
  value
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean);

const numberText = (value: number | undefined) => (value === undefined ? '' : String(value));

function emptyAttribute(): AttributeDraft {
  return {
    key: '',
    de: '',
    en: '',
    data_type: 'text',
    required: false,
    default: '',
    enum_values: '',
    min: '',
    max: '',
    min_length: '',
    max_length: '',
    pattern: '',
  };
}

function attributeDraftOf(def: RelationshipAttributeDefinition): AttributeDraft {
  const validation = validationRulesOf(def.validation) ?? {};
  return {
    key: def.key,
    de: def.display_name?.de ?? '',
    en: def.display_name?.en ?? '',
    data_type: (RELATIONSHIP_ATTRIBUTE_TYPES as readonly string[]).includes(def.data_type)
      ? def.data_type
      : 'text',
    required: def.required ?? false,
    default:
      def.default === undefined || def.default === null
        ? ''
        : Array.isArray(def.default)
          ? def.default.join(', ')
          : String(def.default),
    enum_values: (def.enum_values ?? []).join(', '),
    min: numberText(validation.min),
    max: numberText(validation.max),
    min_length: numberText(validation.min_length),
    max_length: numberText(validation.max_length),
    pattern: validation.pattern ?? '',
    conditional: def.conditional,
  };
}

function draftOf(type?: RelationshipType): TypeDraft {
  if (!type) {
    return {
      key: '',
      display_name: '',
      reverse_name: '',
      directed: true,
      category: 'logical',
      impact_direction: 'source_depends_on_target',
      redundant: false,
      allowed_source_types: [],
      allowed_target_types: [],
      cardinality: 'many_to_many',
      description: '',
      metadata_schema: [],
    };
  }
  return {
    key: type.key,
    display_name: type.display_name,
    reverse_name: type.reverse_name,
    directed: type.directed,
    category: type.category,
    impact_direction: type.impact_direction,
    redundant: type.redundant,
    allowed_source_types: [...type.allowed_source_types],
    allowed_target_types: [...type.allowed_target_types],
    cardinality: type.cardinality,
    description: type.description ?? '',
    metadata_schema: (type.metadata_schema ?? []).map(attributeDraftOf),
  };
}

/** defaultValue reads the default of a definition for its data type. */
function defaultValue(draft: AttributeDraft): unknown {
  const raw = draft.default.trim();
  if (raw === '') return undefined;
  if (NUMBER_TYPES.has(draft.data_type)) {
    const n = Number(raw);
    return Number.isNaN(n) ? raw : n;
  }
  if (draft.data_type === 'boolean') return raw === 'true';
  if (draft.data_type === 'multiselect') return splitList(raw);
  return draft.default;
}

/** attributeOf builds a definition in the format of WP-295 (legacy validation). */
function attributeOf(draft: AttributeDraft): RelationshipAttributeDefinition {
  const def: RelationshipAttributeDefinition = {
    key: draft.key.trim(),
    data_type: draft.data_type,
    required: draft.required,
  };
  if (draft.de.trim() || draft.en.trim()) {
    def.display_name = {
      ...(draft.de.trim() ? { de: draft.de.trim() } : {}),
      ...(draft.en.trim() ? { en: draft.en.trim() } : {}),
    };
  }
  const value = defaultValue(draft);
  if (value !== undefined) def.default = value;
  if (ENUM_TYPES.has(draft.data_type)) def.enum_values = splitList(draft.enum_values);
  const validation: Record<string, number | string> = {};
  const bound = (name: 'min' | 'max' | 'min_length' | 'max_length') => {
    const raw = draft[name].trim();
    if (raw !== '' && !Number.isNaN(Number(raw))) validation[name] = Number(raw);
  };
  if (NUMBER_TYPES.has(draft.data_type)) {
    bound('min');
    bound('max');
  }
  if (LENGTH_TYPES.has(draft.data_type)) {
    bound('min_length');
    bound('max_length');
    if (draft.pattern.trim()) validation.pattern = draft.pattern.trim();
  }
  if (Object.keys(validation).length > 0) def.validation = validation;
  if (draft.conditional) def.conditional = draft.conditional;
  return def;
}

function upsertOf(draft: TypeDraft): RelationshipTypeUpsert {
  return {
    display_name: draft.display_name.trim(),
    reverse_name: draft.reverse_name.trim(),
    directed: draft.directed,
    category: draft.category,
    impact_direction: draft.impact_direction,
    redundant: draft.redundant,
    allowed_source_types: draft.allowed_source_types,
    allowed_target_types: draft.allowed_target_types,
    cardinality: draft.cardinality,
    description: draft.description.trim(),
    metadata_schema: draft.metadata_schema.map(attributeOf),
  };
}

/**
 * refusalMessage words a refused write in the UI language: 409 for a taken
 * key (also of a system type) or a type in use, 400 for an invalid
 * definition, 403 for a missing relationship_type:manage.
 */
export function refusalMessage(
  error: unknown,
  t: TFunction,
  { systemKey = false }: { systemKey?: boolean } = {},
): string {
  if (error instanceof ApiError) {
    const detail = error.detail ?? error.message;
    if (error.status === 409) {
      if (detail === IN_USE) return t('relationshipTypes.errors.inUse');
      if (detail === KEY_TAKEN && systemKey) return t('relationshipTypes.errors.systemKey');
      return t('relationshipTypes.errors.keyTaken');
    }
    if (error.status === 403) return t('relationshipTypes.errors.forbidden');
    if (error.status === 404) return t('relationshipTypes.errors.notFound');
    if (error.status === 400) return t('relationshipTypes.errors.invalid', { detail });
    return t('relationshipTypes.errors.reason', { detail });
  }
  return t('relationshipTypes.errors.reason', {
    detail: error instanceof Error ? error.message : String(error),
  });
}

type DialogState = { mode: 'create' } | { mode: 'edit'; type: RelationshipType };

export function RelationshipTypesPage() {
  const { t } = useTranslation();
  const typesQuery = useRelationshipTypes();
  const ciTypesQuery = useAllCITypes();
  const [dialog, setDialog] = useState<DialogState | null>(null);
  const [deleting, setDeleting] = useState<RelationshipType | null>(null);
  const types = [...(typesQuery.data?.data ?? [])].sort(
    (a, b) => Number(b.is_system) - Number(a.is_system) || a.key.localeCompare(b.key),
  );

  return (
    <div className="space-y-4">
      <PageHeader
        title={t('relationshipTypes.title')}
        description={t('relationshipTypes.subtitle')}
        actions={
          <Button icon={<Plus />} onClick={() => setDialog({ mode: 'create' })}>
            {t('relationshipTypes.add')}
          </Button>
        }
      />
      <Card className="overflow-hidden p-0">
        {typesQuery.isLoading ? (
          <div className="p-4">
            <SkeletonList rows={4} label={t('app.loading')} />
          </div>
        ) : typesQuery.error ? (
          <ErrorState
            title={
              typesQuery.error instanceof ApiError && typesQuery.error.status === 403
                ? t('relationshipTypes.errors.forbiddenList')
                : t('relationshipTypes.loadError')
            }
            retryLabel={t('common.retry')}
            onRetry={() => void typesQuery.refetch()}
          />
        ) : types.length === 0 ? (
          <EmptyState title={t('relationshipTypes.empty')} icon={<GitFork />} className="py-8" />
        ) : (
          <ScrollRegion label={t('relationshipTypes.title')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('relationshipTypes.columns.name')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('relationshipTypes.columns.key')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('relationshipTypes.columns.category')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('relationshipTypes.columns.impact')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('relationshipTypes.columns.cardinality')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('relationshipTypes.columns.origin')}
                  </th>
                  <th scope="col" className={thClasses}>
                    <span className="sr-only">{t('common.actions')}</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {types.map((type) => (
                  <tr key={type.id} className={trClasses}>
                    <td className={tdPrimaryClasses}>
                      <span className="block">{type.display_name}</span>
                      <span className="block text-xs font-normal text-fg-muted">
                        {type.reverse_name}
                      </span>
                    </td>
                    <td className={tdMonoClasses}>{type.key}</td>
                    <td className={tdClasses}>
                      {t(`relationshipTypes.categories.${type.category}`, type.category)}
                    </td>
                    <td className={tdClasses}>
                      {t(
                        `relationshipTypes.impactDirections.${type.impact_direction}`,
                        type.impact_direction,
                      )}
                    </td>
                    <td className={tdClasses}>
                      {t(`relationshipTypes.cardinalities.${type.cardinality}`, type.cardinality)}
                    </td>
                    <td className={tdClasses}>
                      {type.is_system ? (
                        <Badge variant="neutral">{t('relationshipTypes.system')}</Badge>
                      ) : (
                        <Badge variant="info">{t('relationshipTypes.own')}</Badge>
                      )}
                    </td>
                    <td className={`${tdClasses} text-right`}>
                      {type.is_system ? null : (
                        <span className="inline-flex gap-1">
                          <Button
                            size="sm"
                            variant="ghost"
                            className="px-2 text-accent hover:text-accent"
                            aria-label={t('relationshipTypes.editNamed', {
                              name: type.display_name,
                            })}
                            onClick={() => setDialog({ mode: 'edit', type })}
                          >
                            {t('common.edit')}
                          </Button>
                          <Button
                            size="sm"
                            variant="ghost"
                            className="px-2 text-danger hover:text-danger"
                            aria-label={t('relationshipTypes.deleteNamed', {
                              name: type.display_name,
                            })}
                            onClick={() => setDeleting(type)}
                          >
                            {t('common.delete')}
                          </Button>
                        </span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </ScrollRegion>
        )}
      </Card>
      {dialog ? (
        <RelationshipTypeEditor
          // A new dialog starts from its own draft.
          key={dialog.mode === 'edit' ? dialog.type.key : '_new'}
          state={dialog}
          types={types}
          ciTypes={ciTypesQuery.data ?? []}
          onClose={() => setDialog(null)}
        />
      ) : null}
      <DeleteRelationshipTypeDialog type={deleting} onClose={() => setDeleting(null)} />
    </div>
  );
}

interface EditorProps {
  state: DialogState;
  types: RelationshipType[];
  ciTypes: CIType[];
  onClose: () => void;
}

const checkboxLabel = 'flex items-center gap-2 text-sm text-fg';
const legendClass = 'p-0 text-sm font-semibold text-fg';

/** RelationshipTypeEditor creates or edits an organization type. */
function RelationshipTypeEditor({ state, types, ciTypes, onClose }: EditorProps) {
  const { t, i18n } = useTranslation();
  const editing = state.mode === 'edit' ? state.type : undefined;
  const [draft, setDraft] = useState<TypeDraft>(() => draftOf(editing));
  const create = useCreateRelationshipType();
  const update = useUpdateRelationshipType();
  const error = editing ? update.error : create.error;
  const errors = fieldErrors(error);
  const saving = create.isPending || update.isPending;
  const set = (change: Partial<TypeDraft>) => setDraft((d) => ({ ...d, ...change }));
  const setAttribute = (index: number, change: Partial<AttributeDraft>) =>
    setDraft((d) => ({
      ...d,
      metadata_schema: d.metadata_schema.map((a, i) => (i === index ? { ...a, ...change } : a)),
    }));
  const systemKey = types.some((type) => type.is_system && type.key === draft.key.trim());
  const ciTypeLabel = (type: CIType) =>
    localizedText(type.display_name, i18n.language) || type.name;
  const ciTypeKeys = [...ciTypes].sort((a, b) => a.key.localeCompare(b.key));

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const body = upsertOf(draft);
    if (editing) {
      update.mutate({ key: editing.key, data: body }, { onSuccess: onClose });
      return;
    }
    create.mutate(
      {
        ...body,
        key: draft.key.trim(),
        display_name: body.display_name ?? '',
        reverse_name: body.reverse_name ?? '',
      },
      { onSuccess: onClose },
    );
  };

  const allowedList = (field: 'allowed_source_types' | 'allowed_target_types', legend: string) => (
    <fieldset className="min-w-0 space-y-2">
      <legend className={legendClass}>{legend}</legend>
      <p className="text-xs text-fg-muted">{t('relationshipTypes.fields.allowedHint')}</p>
      <div className="max-h-40 space-y-1 overflow-y-auto rounded-lg border border-line p-2">
        {ciTypeKeys.map((type) => (
          <label key={type.id} className={checkboxLabel}>
            <input
              type="checkbox"
              checked={draft[field].includes(type.key)}
              onChange={(e) =>
                set({
                  [field]: e.target.checked
                    ? [...draft[field], type.key]
                    : draft[field].filter((key) => key !== type.key),
                })
              }
            />
            <span>{ciTypeLabel(type)}</span>
            <span className="font-mono text-xs text-fg-muted">{type.key}</span>
          </label>
        ))}
      </div>
      {errors[field] ? <p className="text-sm text-danger">{errors[field]}</p> : null}
    </fieldset>
  );

  return (
    <Modal
      open
      onOpenChange={(open) => (open ? undefined : onClose())}
      title={
        editing
          ? t('relationshipTypes.editNamed', { name: editing.display_name })
          : t('relationshipTypes.add')
      }
      size="lg"
    >
      <form className="space-y-6" onSubmit={submit}>
        <div className="grid grid-cols-1 gap-x-8 gap-y-6 md:grid-cols-2">
          <fieldset className="min-w-0 space-y-3">
            <legend className={legendClass}>{t('relationshipTypes.groups.naming')}</legend>
            {editing ? (
              <p className="text-sm text-fg-muted">
                {t('relationshipTypes.keyFixed', { key: editing.key })}
              </p>
            ) : (
              <Input
                label={t('relationshipTypes.fields.key')}
                value={draft.key}
                required
                onChange={(e) => set({ key: e.target.value })}
                error={errors.key}
              />
            )}
            <Input
              label={t('relationshipTypes.fields.displayName')}
              value={draft.display_name}
              required
              onChange={(e) => set({ display_name: e.target.value })}
              error={errors.display_name}
            />
            <Input
              label={t('relationshipTypes.fields.reverseName')}
              value={draft.reverse_name}
              required
              onChange={(e) => set({ reverse_name: e.target.value })}
              error={errors.reverse_name}
            />
            <Input
              label={t('relationshipTypes.fields.description')}
              value={draft.description}
              onChange={(e) => set({ description: e.target.value })}
              error={errors.description}
            />
          </fieldset>
          <fieldset className="min-w-0 space-y-3">
            <legend className={legendClass}>{t('relationshipTypes.groups.behaviour')}</legend>
            <Select
              label={t('relationshipTypes.fields.category')}
              value={draft.category}
              onChange={(e) => set({ category: e.target.value as RelationshipTypeCategory })}
              options={RELATIONSHIP_CATEGORIES.map((value) => ({
                value,
                label: t(`relationshipTypes.categories.${value}`),
              }))}
              error={errors.category}
            />
            <Select
              label={t('relationshipTypes.fields.impactDirection')}
              value={draft.impact_direction}
              onChange={(e) =>
                set({ impact_direction: e.target.value as RelationshipImpactDirection })
              }
              options={IMPACT_DIRECTIONS.map((value) => ({
                value,
                label: t(`relationshipTypes.impactDirections.${value}`),
              }))}
              error={errors.impact_direction}
            />
            <Select
              label={t('relationshipTypes.fields.cardinality')}
              value={draft.cardinality}
              onChange={(e) => set({ cardinality: e.target.value as RelationshipTypeCardinality })}
              options={CARDINALITIES.map((value) => ({
                value,
                label: t(`relationshipTypes.cardinalities.${value}`),
              }))}
              error={errors.cardinality}
            />
            <label className={checkboxLabel}>
              <input
                type="checkbox"
                checked={draft.directed}
                onChange={(e) => set({ directed: e.target.checked })}
              />
              {t('relationshipTypes.fields.directed')}
            </label>
            <label className={checkboxLabel}>
              <input
                type="checkbox"
                checked={draft.redundant}
                onChange={(e) => set({ redundant: e.target.checked })}
              />
              {t('relationshipTypes.fields.redundant')}
            </label>
          </fieldset>
          {allowedList('allowed_source_types', t('relationshipTypes.fields.allowedSource'))}
          {allowedList('allowed_target_types', t('relationshipTypes.fields.allowedTarget'))}
        </div>

        <fieldset className="min-w-0 space-y-3">
          <legend className={legendClass}>{t('relationshipTypes.schema.title')}</legend>
          {draft.metadata_schema.length === 0 ? (
            <p className="text-sm text-fg-muted">{t('relationshipTypes.schema.empty')}</p>
          ) : null}
          {draft.metadata_schema.map((attribute, index) => (
            <AttributeRow
              key={index}
              index={index}
              attribute={attribute}
              onChange={(change) => setAttribute(index, change)}
              onRemove={() =>
                set({ metadata_schema: draft.metadata_schema.filter((_, i) => i !== index) })
              }
            />
          ))}
          {errors.metadata_schema ? (
            <p className="text-sm text-danger">{errors.metadata_schema}</p>
          ) : null}
          <Button
            type="button"
            size="sm"
            variant="secondary"
            icon={<Plus />}
            onClick={() => set({ metadata_schema: [...draft.metadata_schema, emptyAttribute()] })}
          >
            {t('relationshipTypes.schema.add')}
          </Button>
        </fieldset>

        {error ? (
          <p role="alert" className="text-sm text-danger">
            {refusalMessage(error, t, { systemKey: !editing && systemKey })}
          </p>
        ) : null}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" loading={saving}>
            {editing ? t('common.save') : t('common.create')}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

interface AttributeRowProps {
  index: number;
  attribute: AttributeDraft;
  onChange: (change: Partial<AttributeDraft>) => void;
  onRemove: () => void;
}

/** AttributeRow edits one definition of the attribute schema (WP-295). */
function AttributeRow({ index, attribute, onChange, onRemove }: AttributeRowProps) {
  const { t } = useTranslation();
  const number = index + 1;
  return (
    <fieldset className="min-w-0 space-y-3 rounded-lg border border-line p-3">
      <legend className="px-1 text-sm font-medium text-fg">
        {t('relationshipTypes.schema.attribute', { number })}
      </legend>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
        <Input
          label={t('relationshipTypes.schema.key')}
          value={attribute.key}
          required
          onChange={(e) => onChange({ key: e.target.value })}
        />
        <Input
          label={t('relationshipTypes.schema.labelDe')}
          value={attribute.de}
          onChange={(e) => onChange({ de: e.target.value })}
        />
        <Input
          label={t('relationshipTypes.schema.labelEn')}
          value={attribute.en}
          onChange={(e) => onChange({ en: e.target.value })}
        />
        <Select
          label={t('relationshipTypes.schema.dataType')}
          value={attribute.data_type}
          onChange={(e) => onChange({ data_type: e.target.value as AttributeType })}
          options={RELATIONSHIP_ATTRIBUTE_TYPES.map((value) => ({
            value,
            label: t(`citypes.dataTypes.${value}`),
          }))}
        />
        <Input
          label={t('relationshipTypes.schema.default')}
          value={attribute.default}
          onChange={(e) => onChange({ default: e.target.value })}
        />
        {ENUM_TYPES.has(attribute.data_type) ? (
          <Input
            label={t('relationshipTypes.schema.enumValues')}
            value={attribute.enum_values}
            onChange={(e) => onChange({ enum_values: e.target.value })}
          />
        ) : null}
        {NUMBER_TYPES.has(attribute.data_type) ? (
          <>
            <Input
              label={t('relationshipTypes.schema.min')}
              type="number"
              value={attribute.min}
              onChange={(e) => onChange({ min: e.target.value })}
            />
            <Input
              label={t('relationshipTypes.schema.max')}
              type="number"
              value={attribute.max}
              onChange={(e) => onChange({ max: e.target.value })}
            />
          </>
        ) : null}
        {LENGTH_TYPES.has(attribute.data_type) ? (
          <>
            <Input
              label={t('relationshipTypes.schema.minLength')}
              type="number"
              value={attribute.min_length}
              onChange={(e) => onChange({ min_length: e.target.value })}
            />
            <Input
              label={t('relationshipTypes.schema.maxLength')}
              type="number"
              value={attribute.max_length}
              onChange={(e) => onChange({ max_length: e.target.value })}
            />
            <Input
              label={t('relationshipTypes.schema.pattern')}
              value={attribute.pattern}
              onChange={(e) => onChange({ pattern: e.target.value })}
            />
          </>
        ) : null}
      </div>
      <div className="flex items-center justify-between gap-2">
        <label className={checkboxLabel}>
          <input
            type="checkbox"
            checked={attribute.required}
            onChange={(e) => onChange({ required: e.target.checked })}
          />
          {t('relationshipTypes.schema.required')}
        </label>
        <IconButton
          size="sm"
          icon={<Trash2 />}
          label={t('relationshipTypes.schema.remove', { number })}
          onClick={onRemove}
        />
      </div>
    </fieldset>
  );
}

/** DeleteRelationshipTypeDialog confirms the deletion; a type in use is 409. */
function DeleteRelationshipTypeDialog({
  type,
  onClose,
}: {
  type: RelationshipType | null;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const remove = useDeleteRelationshipType();
  // The refusal of one type is not shown for the next.
  const close = () => {
    remove.reset();
    onClose();
  };
  return (
    <Modal
      open={!!type}
      onOpenChange={(open) => (open ? undefined : close())}
      title={t('relationshipTypes.deleteTitle')}
      description={
        type ? t('relationshipTypes.deleteConfirm', { name: type.display_name }) : undefined
      }
    >
      {remove.error ? (
        <p role="alert" className="mb-3 text-sm text-danger">
          {refusalMessage(remove.error, t)}
        </p>
      ) : null}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="secondary" onClick={close}>
          {t('common.cancel')}
        </Button>
        <Button
          type="button"
          variant="danger"
          loading={remove.isPending}
          onClick={() => type && remove.mutate(type.key, { onSuccess: close })}
        >
          {t('common.delete')}
        </Button>
      </div>
    </Modal>
  );
}
