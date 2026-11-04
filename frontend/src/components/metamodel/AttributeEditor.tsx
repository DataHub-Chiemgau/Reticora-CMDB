/**
 * AttributeEditor creates and edits an attribute definition through
 * /api/v1/attribute-definitions (MET-10, MET-13, MET-15, UI-11, WP-313):
 * a new definition with its index flags in one POST, an existing one with
 * a PATCH of the changed properties. Labels per language (MET-01), data
 * type, required, group, order, values, searchable/unique/unique_scope,
 * inheritable (definitions of a type) and the applicability of a global
 * definition (applies_to, WP-255). A computed definition (WP-258) offers
 * neither required nor unique. Refusals of the server show at the field
 * they name (`field`, `violations`) and as a reason in the dialog.
 */
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import {
  type AttributeAppliesTo,
  type AttributeDefinition,
  type AttributeDefinitionCreate,
  type AttributeDefinitionPatch,
  type AttributeIndexFlags,
  type CIType,
} from '../../api/cmdb';
import { useCreateAttributeDefinition, useUpdateAttributeDefinition } from '../../api/cmdbHooks';
import { ApiError } from '../../api/client';
import { Button } from '../ui/Button';
import { Input } from '../ui/Input';
import { Modal } from '../ui/Modal';
import { Select } from '../ui/Select';

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

const ENUM_TYPES = new Set(['enum', 'multiselect', 'multi_enum']);
const APPLIES_TO: AttributeAppliesTo[] = ['all', 'categories', 'types'];

// The 403 detail of the route authorization ("missing required permission:
// ci_attribute:manage") and of a write to a system attribute definition.
const MISSING_PERMISSION = 'missing required permission';
const SYSTEM_DEFINITION_READ_ONLY = 'system attribute definitions are read-only';

/** fieldDetail is the server's message without the "<field>: " prefix of a FieldProblem. */
export function fieldDetail(error: ApiError): string {
  const raw = error.detail || error.message;
  return error.field && raw.startsWith(`${error.field}: `)
    ? raw.slice(error.field.length + 2)
    : raw;
}

/**
 * describeFlagRefusal renders the refusal of an attribute write in the UI
 * language with the server's reason, e.g. the 409 of a unique flag while
 * CIs share a value (MET-15, F-159). A system CI type (422 on ci_type_id)
 * and a system definition (403 read-only) are read-only objects, not a
 * missing permission: "Keine Berechtigung" is only the 403 of the route
 * authorization.
 */
export function describeFlagRefusal(error: unknown, t: TFunction): string {
  if (error instanceof ApiError) {
    const raw = error.detail || error.message;
    const detail = fieldDetail(error);
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

/** fieldErrors maps the field and the violations of a refusal to their inputs. */
export function fieldErrors(error: unknown): Record<string, string> {
  const out: Record<string, string> = {};
  if (!(error instanceof ApiError)) return out;
  for (const v of error.violations) {
    if (v.field) out[v.field] = v.detail;
  }
  if (error.field && !out[error.field]) out[error.field] = fieldDetail(error);
  return out;
}

export type AttributeEditorTarget =
  | { mode: 'create'; scope: 'type'; ciTypeId: string }
  | { mode: 'create'; scope: 'global' }
  | { mode: 'edit'; definition: AttributeDefinition };

interface Draft {
  key: string;
  de: string;
  en: string;
  description: string;
  data_type: string;
  group: string;
  order: string;
  enum_values: string;
  required: boolean;
  inheritable: boolean;
  flags: AttributeIndexFlags;
  applies_to: AttributeAppliesTo;
  categories: string;
  type_ids: string[];
}

const splitList = (value: string) =>
  value
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean);

function draftOf(target: AttributeEditorTarget): Draft {
  if (target.mode === 'create') {
    return {
      key: '',
      de: '',
      en: '',
      description: '',
      data_type: 'text',
      group: 'general',
      order: '',
      enum_values: '',
      required: false,
      inheritable: true,
      flags: { searchable: false, unique: false, unique_scope: 'org' },
      applies_to: 'all',
      categories: '',
      type_ids: [],
    };
  }
  const d = target.definition;
  return {
    key: d.key,
    de: d.display_name?.de ?? '',
    en: d.display_name?.en ?? '',
    description: d.description ?? '',
    data_type: d.data_type,
    group: d.group ?? '',
    order: String(d.order ?? ''),
    enum_values: (d.enum_values ?? []).join(', '),
    required: d.required,
    inheritable: d.inheritable,
    flags: { searchable: d.searchable, unique: d.unique, unique_scope: d.unique_scope },
    applies_to: d.applies_to ?? 'all',
    categories: (d.applies_to_categories ?? []).join(', '),
    type_ids: d.applies_to_type_ids ?? [],
  };
}

const sameList = (a: string[], b: string[]) =>
  a.length === b.length && a.every((v, i) => v === b[i]);

/** patchOf returns the changed properties; the three index flags travel together. */
function patchOf(
  d: AttributeDefinition,
  draft: Draft,
  computed: boolean,
): AttributeDefinitionPatch {
  const patch: AttributeDefinitionPatch = {};
  const before = draftOf({ mode: 'edit', definition: d });
  if (draft.de !== before.de || draft.en !== before.en) {
    patch.display_name = {
      ...(draft.de ? { de: draft.de } : {}),
      ...(draft.en ? { en: draft.en } : {}),
    };
  }
  if (draft.description !== before.description) patch.description = draft.description;
  if (draft.data_type !== before.data_type) patch.data_type = draft.data_type;
  if (draft.group !== before.group) patch.group = draft.group;
  if (draft.order !== before.order && draft.order.trim() !== '') patch.order = Number(draft.order);
  if (ENUM_TYPES.has(draft.data_type)) {
    const values = splitList(draft.enum_values);
    if (!sameList(values, d.enum_values ?? [])) patch.enum_values = values;
  }
  if (!computed && draft.required !== before.required) patch.required = draft.required;
  if (d.scope === 'type' && draft.inheritable !== before.inheritable) {
    patch.inheritable = draft.inheritable;
  }
  const flags = computed ? { ...draft.flags, unique: false } : draft.flags;
  if (
    flags.searchable !== before.flags.searchable ||
    flags.unique !== before.flags.unique ||
    (flags.unique && flags.unique_scope !== before.flags.unique_scope)
  ) {
    patch.searchable = flags.searchable;
    patch.unique = flags.unique;
    patch.unique_scope = flags.unique_scope;
  }
  if (d.scope === 'global') {
    const categories = splitList(draft.categories);
    if (
      draft.applies_to !== before.applies_to ||
      (draft.applies_to === 'categories' && !sameList(categories, d.applies_to_categories ?? [])) ||
      (draft.applies_to === 'types' && !sameList(draft.type_ids, d.applies_to_type_ids ?? []))
    ) {
      patch.applies_to = draft.applies_to;
      if (draft.applies_to === 'categories') patch.applies_to_categories = categories;
      if (draft.applies_to === 'types') patch.applies_to_type_ids = draft.type_ids;
    }
  }
  return patch;
}

function createOf(target: AttributeEditorTarget & { mode: 'create' }, draft: Draft) {
  const body: AttributeDefinitionCreate = {
    key: draft.key.trim(),
    data_type: draft.data_type,
    group: draft.group,
    searchable: draft.flags.searchable,
    unique: draft.flags.unique,
    unique_scope: draft.flags.unique_scope,
  };
  if (draft.de || draft.en) {
    body.display_name = {
      ...(draft.de ? { de: draft.de } : {}),
      ...(draft.en ? { en: draft.en } : {}),
    };
  }
  if (draft.description) body.description = draft.description;
  if (draft.order.trim() !== '') body.order = Number(draft.order);
  if (ENUM_TYPES.has(draft.data_type)) body.enum_values = splitList(draft.enum_values);
  if (target.scope === 'type') {
    body.ci_type_id = target.ciTypeId;
    body.required = draft.required;
    body.inheritable = draft.inheritable;
  } else {
    // A global attribute stays optional here: it applies to CIs of many types
    // (MET-13), a required one would refuse every CI write without it.
    body.required = false;
    body.applies_to = draft.applies_to;
    if (draft.applies_to === 'categories') {
      body.applies_to_categories = splitList(draft.categories);
    }
    if (draft.applies_to === 'types') body.applies_to_type_ids = draft.type_ids;
  }
  return body;
}

const checkboxLabel = 'flex items-center gap-2 text-sm text-gray-700 dark:text-gray-200';
const fieldErrorClass = 'text-sm text-red-600 dark:text-red-400';

export interface AttributeEditorProps {
  target: AttributeEditorTarget;
  onClose: () => void;
  /** The CI types the applicability of a global attribute can name. */
  types?: CIType[];
  typeLabel?: (type: CIType) => string;
}

export function AttributeEditor({
  target,
  onClose,
  types = [],
  typeLabel = (ty) => ty.name,
}: AttributeEditorProps) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState<Draft>(() => draftOf(target));
  const create = useCreateAttributeDefinition();
  const update = useUpdateAttributeDefinition();
  const editing = target.mode === 'edit' ? target.definition : undefined;
  const scope = editing ? editing.scope : target.mode === 'create' ? target.scope : 'type';
  const computed = !!editing?.computed;
  const error = editing ? update.error : create.error;
  const errors = fieldErrors(error);
  const saving = create.isPending || update.isPending;
  const set = (change: Partial<Draft>) => setDraft((d) => ({ ...d, ...change }));
  const setFlags = (change: Partial<AttributeIndexFlags>) =>
    setDraft((d) => ({ ...d, flags: { ...d.flags, ...change } }));

  const title = editing
    ? t('citypes.editField', { name: editing.key })
    : scope === 'global'
      ? t('citypes.globals.add')
      : t('citypes.addField');

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (target.mode === 'create') {
      create.mutate(createOf(target, draft), { onSuccess: onClose });
      return;
    }
    const patch = patchOf(target.definition, draft, computed);
    if (Object.keys(patch).length === 0) {
      onClose();
      return;
    }
    update.mutate({ id: target.definition.id, patch }, { onSuccess: onClose });
  };

  const dataTypes = FIELD_TYPES.includes(draft.data_type)
    ? FIELD_TYPES
    : [...FIELD_TYPES, draft.data_type];
  const errorId = (field: string) => `attribute-editor-${field}-error`;
  const checkboxError = (field: string) =>
    errors[field] ? (
      <p id={errorId(field)} className={fieldErrorClass}>
        {errors[field]}
      </p>
    ) : null;

  return (
    <Modal open onOpenChange={(open) => (open ? undefined : onClose())} title={title}>
      <form className="space-y-3" onSubmit={submit}>
        {scope === 'global' && !editing ? (
          <p className="text-sm text-gray-600 dark:text-gray-300">
            {t('citypes.globals.editorHint')}
          </p>
        ) : null}
        {editing ? (
          <p className="text-sm text-gray-600 dark:text-gray-300">{t('citypes.editHint')}</p>
        ) : (
          <Input
            label={t('citypes.fieldName')}
            value={draft.key}
            required
            onChange={(e) => set({ key: e.target.value })}
            error={errors.key}
          />
        )}
        {editing && errors.key ? <p className={fieldErrorClass}>{errors.key}</p> : null}
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
        <Select
          label={t('citypes.fieldType')}
          options={dataTypes.map((ft) => ({ value: ft, label: t(`citypes.dataTypes.${ft}`, ft) }))}
          value={draft.data_type}
          onChange={(e) => set({ data_type: e.target.value })}
          error={errors.data_type}
        />
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <Input
            label={t('citypes.fieldGroup')}
            value={draft.group}
            onChange={(e) => set({ group: e.target.value })}
            error={errors.group}
          />
          <Input
            label={t('citypes.fieldOrder')}
            type="number"
            value={draft.order}
            onChange={(e) => set({ order: e.target.value })}
            error={errors.order}
          />
        </div>
        {ENUM_TYPES.has(draft.data_type) ? (
          <Input
            label={t('citypes.enumValues')}
            value={draft.enum_values}
            onChange={(e) => set({ enum_values: e.target.value })}
            error={errors.enum_values}
          />
        ) : null}
        {computed ? (
          <p className="text-sm text-gray-600 dark:text-gray-300">{t('citypes.computedHint')}</p>
        ) : null}
        {scope === 'type' && !computed ? (
          <>
            <label className={checkboxLabel}>
              <input
                type="checkbox"
                checked={draft.required}
                aria-describedby={errors.required ? errorId('required') : undefined}
                onChange={(e) => set({ required: e.target.checked })}
              />
              {t('citypes.required')}
            </label>
            {checkboxError('required')}
          </>
        ) : null}
        {scope === 'type' ? (
          <>
            <label className={checkboxLabel}>
              <input
                type="checkbox"
                checked={draft.inheritable}
                aria-describedby={errors.inheritable ? errorId('inheritable') : undefined}
                onChange={(e) => set({ inheritable: e.target.checked })}
              />
              {t('citypes.inheritable')}
            </label>
            {checkboxError('inheritable')}
          </>
        ) : null}
        <label className={checkboxLabel}>
          <input
            type="checkbox"
            checked={draft.flags.searchable}
            aria-describedby={errors.searchable ? errorId('searchable') : undefined}
            onChange={(e) => setFlags({ searchable: e.target.checked })}
          />
          {t('citypes.searchable')}
        </label>
        {checkboxError('searchable')}
        {!computed ? (
          <>
            <label className={checkboxLabel}>
              <input
                type="checkbox"
                checked={draft.flags.unique}
                aria-invalid={errors.unique ? true : undefined}
                aria-describedby={errors.unique ? errorId('unique') : undefined}
                onChange={(e) => setFlags({ unique: e.target.checked })}
              />
              {t('citypes.unique')}
            </label>
            {checkboxError('unique')}
            {draft.flags.unique ? (
              <Select
                label={t('citypes.uniqueScope')}
                options={[
                  { value: 'org', label: t('citypes.uniqueScopeOrg') },
                  { value: 'client', label: t('citypes.uniqueScopeClient') },
                ]}
                value={draft.flags.unique_scope}
                onChange={(e) =>
                  setFlags({ unique_scope: e.target.value as AttributeIndexFlags['unique_scope'] })
                }
                error={errors.unique_scope}
              />
            ) : null}
          </>
        ) : null}
        {scope === 'global' ? (
          <AppliesToInputs
            draft={draft}
            set={set}
            errors={errors}
            types={types}
            typeLabel={typeLabel}
            t={t}
          />
        ) : null}
        {error ? (
          <p role="alert" className={fieldErrorClass}>
            {editing
              ? t('citypes.flagErrors.changesNotSaved', { reason: describeFlagRefusal(error, t) })
              : t('citypes.flagErrors.fieldNotSaved', { reason: describeFlagRefusal(error, t) })}
          </p>
        ) : null}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" disabled={saving}>
            {t('common.save')}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

/** The applicability of a global attribute of the organization (MET-13, WP-255). */
function AppliesToInputs({
  draft,
  set,
  errors,
  types,
  typeLabel,
  t,
}: {
  draft: Draft;
  set: (change: Partial<Draft>) => void;
  errors: Record<string, string>;
  types: CIType[];
  typeLabel: (type: CIType) => string;
  t: TFunction;
}) {
  const typesError = errors.applies_to_type_ids;
  return (
    <>
      <Select
        label={t('citypes.appliesTo.label')}
        options={APPLIES_TO.map((mode) => ({
          value: mode,
          label: t(`citypes.appliesTo.modes.${mode}`),
        }))}
        value={draft.applies_to}
        onChange={(e) => set({ applies_to: e.target.value as AttributeAppliesTo })}
        error={errors.applies_to}
      />
      {draft.applies_to === 'categories' ? (
        <Input
          label={t('citypes.appliesTo.categories')}
          value={draft.categories}
          onChange={(e) => set({ categories: e.target.value })}
          error={errors.applies_to_categories}
        />
      ) : null}
      {draft.applies_to === 'types' ? (
        <fieldset
          className="space-y-1"
          aria-describedby={typesError ? 'attribute-editor-applies_to_type_ids-error' : undefined}
        >
          <legend className="text-sm font-medium text-gray-700 dark:text-gray-200">
            {t('citypes.appliesTo.types')}
          </legend>
          <div className="max-h-40 space-y-1 overflow-y-auto rounded-lg border border-gray-200 p-2 dark:border-gray-700">
            {types.map((ty) => (
              <label key={ty.id} className={checkboxLabel}>
                <input
                  type="checkbox"
                  checked={draft.type_ids.includes(ty.id)}
                  onChange={(e) =>
                    set({
                      type_ids: e.target.checked
                        ? [...draft.type_ids, ty.id]
                        : draft.type_ids.filter((id) => id !== ty.id),
                    })
                  }
                />
                {typeLabel(ty)}
              </label>
            ))}
          </div>
          {typesError ? (
            <p id="attribute-editor-applies_to_type_ids-error" className={fieldErrorClass}>
              {typesError}
            </p>
          ) : null}
        </fieldset>
      ) : null}
    </>
  );
}
