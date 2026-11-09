/**
 * GuidedCreate — the guided creation with progressive disclosure (MET-51,
 * UI-12, WP-317): type (tiles) → manufacturer and model → fields of the
 * resolved form. The fields step reads GET /ci-types/{id}/form (WP-719)
 * with the chosen variant (template, WP-251) and recomputes the states of
 * the fields on every input with the conditions of the definitions and the
 * catalog rules. Logical types skip the manufacturer step. End users see
 * no metamodel terms: a template is a "variant" here.
 *
 * The component takes the target resource, the source of the types and
 * categories and the submit callback as props, so the asset creation can
 * use it as well (WP-319).
 */
import { useEffect, useId, useMemo, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { ApiError, isTypeInactive } from '../../api/client';
import { localizedText, type ResolvedForm } from '../../api/cmdb';
import { useCIValueSuggestions, useResolvedTypeForm, useTemplates } from '../../api/cmdbHooks';
import { useModules } from '../../hooks/useEntitlements';
import { evaluateField, type FieldDefinition, type FieldError } from '../../lib/fieldmeta';
import {
  evaluateCondition,
  evaluateRule,
  parseCondition,
  parseRule,
  type Condition,
  type Rule,
} from '../../rules/rules';
import { DynamicForm, validateAll, type DynamicFormSection } from '../form/DynamicForm';
import { LocationPicker } from '../pickers/LocationPicker';
import { UserTeamPicker, type PickedHolder } from '../pickers/UserTeamPicker';
import { useDebouncedValue } from '../pickers/ObjectPicker';
import { Button } from '../ui/Button';
import { ErrorState } from '../ui/ErrorState';
import { Input } from '../ui/Input';
import { Select } from '../ui/Select';
import { cn } from '../ui/utils';
import { TYPE_CATEGORIES, TypeTiles, type TileType } from './TypeTiles';

/**
 * CI_TYPED_COLUMNS are the typed columns of a CI (typedValues in
 * backend/internal/ci/validation.go): a form field of that name is sent as
 * a column of the request, not in attributes.
 */
export const CI_TYPED_COLUMNS = [
  'name',
  'manufacturer',
  'model',
  'serial_number',
  'hardware_uuid',
  'management_ip',
  'primary_mac',
  'hostname',
  'fqdn',
  'os_name',
  'os_version',
  'firmware_version',
  'sys_object_id',
] as const;

/**
 * FORM_GROUPS are the field groups (ui_group) of the Go catalogue
 * citype.Templates(); their headings come from create.groups.
 */
export const FORM_GROUPS = [
  'identity',
  'placement',
  'management',
  'virtualization',
  'hardware',
  'software',
  'assignment',
  'wireless',
  'connectivity',
  'access',
  'classification',
  'capacity',
  'resilience',
  'ownership',
  'cloud',
] as const;

/** The technical sections of a resolved form and their headings in create.groups. */
const TECHNICAL_SECTIONS: Record<string, string> = {
  _default: 'general',
  _required: 'required',
  _instance: 'instance',
};

export type CreateStep = 'type' | 'device' | 'fields';

/** CreateType is an offered type of the guided creation. */
export interface CreateType extends TileType {
  /** isLogical skips the manufacturer and model step. */
  isLogical: boolean;
}

/**
 * TYPE_HIDDEN_DETAIL is the violation of POST /cis on ci_type_id for a type
 * the client of the new CI hides (MET-05, WP-858).
 */
export const TYPE_HIDDEN_DETAIL = 'the CI type is hidden for the client';

/** TypeSource is the list of offered types and the state of its request. */
export interface TypeSource {
  types: readonly CreateType[];
  loading: boolean;
  failed: boolean;
  retrying: boolean;
  onRetry: () => void;
}

/** GuidedCreateDraft is what the creation submits. */
export interface GuidedCreateDraft {
  typeId: string;
  /** templateId is the chosen variant, absent without one. */
  templateId?: string;
  name: string;
  status: string;
  locationId?: string;
  /** ownerUserId and ownerTeamId are the responsible user and team (CI-07, WP-805). */
  ownerUserId?: string;
  ownerTeamId?: string;
  /** columns are the filled typed columns (manufacturer and model included). */
  columns: Record<string, string>;
  /** attributes are the filled visible fields that are no typed column. */
  attributes: Record<string, unknown>;
}

export interface GuidedCreateProps {
  /** resource is the target resource; the CI creation (WP-319 adds assets). */
  resource: 'ci';
  typeSource: TypeSource;
  /** categories are the known type categories in their order. */
  categories?: readonly string[];
  /** typedColumns are the field names sent as columns (CI_TYPED_COLUMNS). */
  typedColumns?: readonly string[];
  statusOptions: Array<{ value: string; label: string }>;
  /** onSubmit creates the object; a rejected ApiError is shown at the fields. */
  onSubmit: (draft: GuidedCreateDraft) => Promise<void>;
  onCancel: () => void;
}

type FormField = ResolvedForm['sections'][number]['fields'][number];
type FormRule = ResolvedForm['rules'][number];

interface CatalogRule {
  field: string;
  kind: FormRule['kind'];
  condition?: Condition;
  rule?: Rule;
  message?: Record<string, string | undefined>;
}

interface FieldState {
  visible: boolean;
  required: boolean;
  readOnly: boolean;
  allowedValues?: string[];
}

/** ALWAYS is a predicate that holds for every record (a field never named). */
const ALWAYS = { field: '__guided_create_always', op: 'empty' as const };

function parseCatalog(rules: readonly FormRule[]): CatalogRule[] {
  const out: CatalogRule[] = [];
  for (const rule of rules) {
    try {
      out.push({
        field: rule.attribute_key,
        kind: rule.kind,
        condition: rule.condition === undefined ? undefined : parseCondition(rule.condition),
        rule: rule.kind === 'validation' && rule.ast ? parseRule(rule.ast) : undefined,
        message: rule.message as Record<string, string | undefined> | undefined,
      });
    } catch {
      // A rule the client cannot read stays with the server, which checks it.
    }
  }
  return out;
}

function applies(rule: CatalogRule, values: Record<string, unknown>) {
  return !rule.condition || evaluateCondition(rule.condition, values);
}

/**
 * fieldState mirrors fieldStates of backend/internal/citype/form_resolver.go:
 * the conditions of the definition, then the catalog rules (required and
 * readonly while their condition holds, visible hides the field while it
 * does not); a required field stays visible. lockedByPolicy is the read-only
 * state the server set without a condition (field permissions).
 */
function fieldState(
  def: FieldDefinition,
  catalog: readonly CatalogRule[],
  values: Record<string, unknown>,
  lockedByPolicy: boolean,
): FieldState {
  const state = evaluateField(def, values);
  let hidden = !state.visible;
  let required = state.required;
  let readOnly = state.readOnly;
  for (const rule of catalog) {
    if (rule.field !== def.name) continue;
    const holds = applies(rule, values);
    if (rule.kind === 'required') required = required || holds;
    if (rule.kind === 'readonly') readOnly = readOnly || holds;
    if (rule.kind === 'visible') hidden = hidden || !holds;
  }
  return {
    visible: !hidden || required,
    required,
    readOnly: readOnly || lockedByPolicy,
    allowedValues: state.allowedValues,
  };
}

function isBlank(value: unknown) {
  return (
    value === undefined ||
    value === null ||
    (typeof value === 'string' && !value.trim()) ||
    (Array.isArray(value) && value.length === 0)
  );
}

/** defaultValue converts the stored default (a string) into the value of the field. */
function defaultValue(def: FieldDefinition): unknown {
  const raw = def.default_value;
  if (raw === undefined || raw === '') return undefined;
  switch (def.data_type) {
    case 'integer':
    case 'decimal':
    case 'number': {
      const n = Number(raw);
      return Number.isNaN(n) ? undefined : n;
    }
    case 'boolean':
      return raw === 'true';
    default:
      return raw;
  }
}

/** humanize turns a field name without a label into readable words. */
function humanize(name: string) {
  const words = name.replace(/[_.-]+/g, ' ').trim();
  return words ? words.charAt(0).toUpperCase() + words.slice(1) : name;
}

interface PreparedField {
  def: FieldDefinition;
  section: string;
  /** width is the share of the 12-column grid of the layout (WP-803). */
  width: number;
  lockedByPolicy: boolean;
}

interface PreparedSection {
  key: string;
  title: string;
  visible: boolean;
  names: string[];
}

export function GuidedCreate({
  resource,
  typeSource,
  categories = TYPE_CATEGORIES,
  typedColumns = CI_TYPED_COLUMNS,
  statusOptions,
  onSubmit,
  onCancel,
}: GuidedCreateProps) {
  const { t, i18n } = useTranslation();
  const { isModuleEnabled } = useModules();
  const headingRef = useRef<HTMLHeadingElement>(null);
  const ownerGroupId = useId();

  const [step, setStep] = useState<CreateStep>('type');
  const shownStep = useRef<CreateStep>(step);
  const [typeId, setTypeId] = useState<string | undefined>(undefined);
  const [typeNotice, setTypeNotice] = useState(false);
  const [manufacturer, setManufacturer] = useState('');
  const [model, setModel] = useState('');
  const [variant, setVariant] = useState('');
  const [name, setName] = useState('');
  const [status, setStatus] = useState(statusOptions[0]?.value ?? '');
  const [locationId, setLocationId] = useState<string | null>(null);
  const [ownerUser, setOwnerUser] = useState<PickedHolder | null>(null);
  const [ownerTeam, setOwnerTeam] = useState<PickedHolder | null>(null);
  const [values, setValues] = useState<Record<string, unknown>>({});
  const [prefilled, setPrefilled] = useState<Set<string>>(() => new Set());
  const [clientErrors, setClientErrors] = useState<Record<string, FieldError | null>>({});
  const [masterErrors, setMasterErrors] = useState<Record<string, string | undefined>>({});
  const [submitError, setSubmitError] = useState<unknown>(null);
  const [submitting, setSubmitting] = useState(false);

  const type = typeSource.types.find((candidate) => candidate.id === typeId);
  const steps: CreateStep[] = type?.isLogical ? ['type', 'fields'] : ['type', 'device', 'fields'];
  const stepIndex = Math.max(steps.indexOf(step), 0);
  const withDevice = steps.includes('device');

  // The focus follows the step: the heading of the new step (not on open,
  // where the dialog focuses its first control).
  useEffect(() => {
    if (shownStep.current === step) return;
    shownStep.current = step;
    headingRef.current?.focus();
  }, [step]);

  // Variants: the active templates of the type with a published version
  // (WP-251). Without the module metamodel (E-105) or with 403/404 there is
  // no choice.
  const metamodelOn = isModuleEnabled('metamodel');
  const templates = useTemplates(
    { ci_type_id: typeId },
    { enabled: resource === 'ci' && !!typeId && metamodelOn, staleTime: 0 },
  );
  const variants = useMemo(
    () =>
      metamodelOn && !templates.isError
        ? (templates.data?.data ?? []).filter(
            (tpl) =>
              tpl.is_active && tpl.current_version !== null && tpl.current_version !== undefined,
          )
        : [],
    [metamodelOn, templates.isError, templates.data],
  );
  const variantsSettled = !metamodelOn || !typeId || !templates.isLoading;
  const templateId = variants.length === 1 ? variants[0]!.id : variant || undefined;

  // The resolved form of the type and the chosen variant (WP-719).
  const form = useResolvedTypeForm(typeId, templateId ? { template_id: templateId } : {}, {
    enabled: resource === 'ci' && variantsSettled,
    staleTime: 0,
  });
  const resolved = form.data?.ci_type_id === typeId ? form.data : undefined;

  const catalog = useMemo(() => parseCatalog(resolved?.rules ?? []), [resolved]);
  const deviceFields = useMemo(
    () => new Set<string>(withDevice ? ['name', 'manufacturer', 'model'] : ['name']),
    [withDevice],
  );

  // The fields of the form once, with their section; manufacturer and model
  // are asked in their own step, the name is a master field.
  const { fields, sections } = useMemo(() => {
    const byName = new Map<string, PreparedField>();
    const out: PreparedSection[] = [];
    for (const section of resolved?.sections ?? []) {
      const names: string[] = [];
      for (const field of section.fields as FormField[]) {
        if (deviceFields.has(field.name) || byName.has(field.name)) continue;
        const def = field.definition as unknown as FieldDefinition;
        const initial = evaluateField(def, {});
        byName.set(field.name, {
          def,
          section: section.key,
          width: field.width,
          lockedByPolicy: field.state.read_only && !initial.readOnly,
        });
        names.push(field.name);
      }
      out.push({
        key: section.key,
        title: sectionTitle(section.key, localizedText(section.title, i18n.language)),
        visible: section.visible,
        names,
      });
    }
    return { fields: byName, sections: out };
    // sectionTitle reads t through i18n.language.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [resolved, deviceFields, i18n.language]);

  function sectionTitle(key: string, title: string) {
    const technical = TECHNICAL_SECTIONS[key];
    if (technical) return t(`create.groups.${technical}`);
    if ((FORM_GROUPS as readonly string[]).includes(key) && (!title || title === key)) {
      return t(`create.groups.${key}`);
    }
    if (title) return title;
    return resolved?.source === 'default' ? key : '';
  }

  // Defaults of the definitions (and of the variant) are filled in once per
  // form and marked as prefilled until the user changes them.
  const prefillKey = resolved ? `${typeId}:${templateId ?? ''}` : '';
  const appliedPrefill = useRef('');
  useEffect(() => {
    if (!prefillKey || appliedPrefill.current === prefillKey) return;
    appliedPrefill.current = prefillKey;
    const next = { ...values };
    for (const fieldName of prefilled) delete next[fieldName];
    const marked = new Set<string>();
    for (const [fieldName, field] of fields) {
      const value = defaultValue(field.def);
      if (value === undefined || !isBlank(next[fieldName])) continue;
      next[fieldName] = value;
      marked.add(fieldName);
    }
    setValues(next);
    setPrefilled(marked);
    // Only a new form fills in; values and prefilled are read at that moment.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [prefillKey, fields]);

  const record = useMemo(
    () => ({ ...values, name, manufacturer, model, status }),
    [values, name, manufacturer, model, status],
  );
  const states = useMemo(() => {
    const out = new Map<string, FieldState>();
    for (const [name, field] of fields) {
      out.set(name, fieldState(field.def, catalog, record, field.lockedByPolicy));
    }
    return out;
  }, [fields, catalog, record]);

  /** The definition DynamicForm renders: the computed state baked in. */
  function effective(name: string): FieldDefinition | undefined {
    const field = fields.get(name);
    const state = states.get(name);
    if (!field || !state?.visible) return undefined;
    const label = field.def.label || humanize(field.def.name);
    return {
      ...field.def,
      label: prefilled.has(name) ? `${label} (${t('create.prefilled')})` : label,
      ui_group: undefined,
      required: state.required,
      enum_values: state.allowedValues ?? field.def.enum_values,
      conditional: state.readOnly ? { read_only_when: [ALWAYS] } : undefined,
    };
  }

  const renderedSections = sections
    .filter((section) => section.visible)
    .map((section) => ({
      ...section,
      defs: section.names
        .map((name) => effective(name))
        .filter((def): def is FieldDefinition => !!def),
    }))
    .filter((section) => section.defs.length > 0);
  const renderedDefs = renderedSections.flatMap((section) => section.defs);
  // The sections as layout of DynamicForm (WP-803): the titles of the
  // guided creation and the computed state of every shown field.
  const layout: DynamicFormSection[] = renderedSections.map((section) => ({
    key: section.key,
    title: { de: section.title, en: section.title },
    visible: true,
    fields: section.defs.map((def) => {
      const state = states.get(def.name)!;
      return {
        name: def.name,
        width: fields.get(def.name)?.width ?? 12,
        state: {
          visible: state.visible,
          required: state.required,
          readOnly: state.readOnly,
          allowedValues: state.allowedValues,
        },
      };
    }),
  }));
  const renderedNames = new Set(renderedDefs.map((def) => def.name));

  // Suggestions for manufacturer and model from the stock of the type.
  const debouncedManufacturer = useDebouncedValue(manufacturer);
  const debouncedModel = useDebouncedValue(model);
  const manufacturerSuggestions = useCIValueSuggestions(
    typeId ? { ciTypeId: typeId, field: 'manufacturer', input: debouncedManufacturer } : undefined,
    { enabled: resource === 'ci' && step === 'device' },
  );
  const modelSuggestions = useCIValueSuggestions(
    typeId
      ? { ciTypeId: typeId, field: 'model', input: debouncedModel, manufacturer: manufacturer }
      : undefined,
    { enabled: resource === 'ci' && step === 'device' },
  );

  // Server violations (422): at the field of the fields step, the rest in
  // the banner.
  const serverViolations = submitError instanceof ApiError ? submitError.violationsByField() : {};
  const fieldErrors: Record<string, FieldError | null> = { ...clientErrors };
  const masterFieldErrors: Record<string, string | undefined> = { ...masterErrors };
  const unmapped: string[] = [];
  const masterLabels: Record<string, string> = {
    manufacturer: t('create.fields.manufacturer'),
    model: t('create.fields.model'),
  };
  // The owner (CI-07, WP-805): a 422 names owner_user_id or owner_team_id
  // (violations or the field of a field problem); a 403 refuses a team
  // outside the team scope (TEN-05, E-11) or a field the principal may not
  // write (RBA-07). Both appear at the owner field.
  const ownerError = ownerFieldErrors(submitError, !!ownerTeam, t);
  for (const [field, detail] of Object.entries(serverViolations)) {
    const attribute = field.startsWith('attributes.') ? field.slice('attributes.'.length) : field;
    if (OWNER_FIELDS.includes(field)) {
      // Shown through ownerError.
    } else if (field === 'name' || field === 'status' || field === 'location_id') {
      masterFieldErrors[field] ??= detail;
    } else if (field === 'ci_type_id' && detail.startsWith(TYPE_HIDDEN_DETAIL)) {
      // The client of the chosen location hides the type (WP-858).
      masterFieldErrors.location_id ??= t('create.typeHiddenForClient');
    } else if (renderedNames.has(attribute)) {
      fieldErrors[attribute] ??= detail;
    } else if (masterLabels[field]) {
      unmapped.push(`${masterLabels[field]}: ${detail}`);
    } else {
      unmapped.push(`${field}: ${detail}`);
    }
  }
  let banner: string | null = null;
  if (submitError) {
    if (unmapped.length > 0) banner = unmapped.join('; ');
    else if (Object.keys(serverViolations).length > 0 || ownerError.mapped)
      banner = t('create.serverRejected');
    else banner = submitError instanceof Error ? submitError.message : String(submitError);
  }

  function chooseType(id: string) {
    if (id !== typeId) {
      // A type change drops only what depends on the type.
      setValues({});
      setPrefilled(new Set());
      appliedPrefill.current = '';
      setVariant('');
      setClientErrors({});
      setSubmitError(null);
    }
    setTypeId(id);
    setTypeNotice(false);
  }

  function setField(fieldName: string, value: unknown) {
    setValues((current) => ({ ...current, [fieldName]: value }));
    setPrefilled((current) => {
      if (!current.has(fieldName)) return current;
      const next = new Set(current);
      next.delete(fieldName);
      return next;
    });
    setClientErrors((current) => ({ ...current, [fieldName]: null }));
  }

  function validate() {
    const nextMaster: Record<string, string | undefined> = {};
    if (!name.trim()) nextMaster.name = t('create.validation.nameRequired');
    if (variants.length > 1 && !variant)
      nextMaster.variant = t('create.validation.variantRequired');
    const errors: Record<string, FieldError | null> = validateAll(renderedDefs, values);
    // Validation rules of the catalog while their condition holds.
    for (const rule of catalog) {
      if (rule.kind !== 'validation' || !rule.rule || !renderedNames.has(rule.field)) continue;
      if (errors[rule.field] || !applies(rule, record)) continue;
      const violation = evaluateRule(rule.rule, values[rule.field])[0];
      if (violation) {
        errors[rule.field] =
          (rule.message && localizedText(rule.message, i18n.language)) || violation.message;
      }
    }
    setMasterErrors(nextMaster);
    setClientErrors(errors);
    return (
      Object.values(nextMaster).every((error) => !error) &&
      Object.values(errors).every((error) => !error)
    );
  }

  function draft(): GuidedCreateDraft {
    const typed = new Set(typedColumns);
    const columns: Record<string, string> = {};
    const attributes: Record<string, unknown> = {};
    if (withDevice) {
      if (manufacturer.trim()) columns.manufacturer = manufacturer.trim();
      if (model.trim()) columns.model = model.trim();
    }
    for (const def of renderedDefs) {
      const value = values[def.name];
      if (isBlank(value)) continue;
      if (typed.has(def.name)) {
        columns[def.name] = String(value).trim();
      } else {
        attributes[def.name] = value;
      }
    }
    return {
      typeId: typeId!,
      templateId,
      name: name.trim(),
      status,
      locationId: locationId ?? undefined,
      ownerUserId: ownerUser?.id,
      ownerTeamId: ownerTeam?.id,
      columns,
      attributes,
    };
  }

  async function create() {
    if (!typeId || !validate()) return;
    setSubmitError(null);
    setSubmitting(true);
    try {
      await onSubmit(draft());
    } catch (error) {
      if (isTypeInactive(error)) {
        // The type was deactivated meanwhile: back to the tiles (WP-249).
        setTypeId(undefined);
        setValues({});
        setPrefilled(new Set());
        appliedPrefill.current = '';
        setTypeNotice(true);
        setStep('type');
        typeSource.onRetry();
      } else {
        setSubmitError(error);
      }
    } finally {
      setSubmitting(false);
    }
  }

  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (step === 'fields') {
      void create();
      return;
    }
    if (step === 'type' && !typeId) return;
    setStep(steps[stepIndex + 1] ?? 'fields');
  }

  const typesUnavailable = typeSource.failed && typeSource.types.length === 0;
  const tileTypes = typeSource.types;
  const stepLabels: Record<CreateStep, string> = {
    type: t('create.steps.type'),
    device: t('create.steps.device'),
    fields: t('create.steps.fields'),
  };

  return (
    <form className="flex flex-col gap-5" onSubmit={handleSubmit} noValidate>
      <ol
        aria-label={t('create.steps.label')}
        className="flex flex-wrap items-center gap-2 text-sm"
      >
        {steps.map((candidate, index) => (
          <li
            key={candidate}
            aria-current={candidate === step ? 'step' : undefined}
            className={cn(
              'flex items-center gap-2 rounded-full border px-3 py-1',
              candidate === step
                ? 'border-accent bg-accent/10 font-medium text-fg'
                : 'border-line text-fg-muted',
            )}
          >
            <span aria-hidden="true">{index + 1}</span>
            {stepLabels[candidate]}
          </li>
        ))}
      </ol>

      <h3 ref={headingRef} tabIndex={-1} className="text-base font-semibold text-fg outline-none">
        {stepLabels[step]}
      </h3>

      {step === 'type' ? (
        <div className="space-y-4">
          <p className="text-sm text-fg-muted">{t('create.typeIntro')}</p>
          {typeNotice ? <ErrorState title={t('create.typeInactive')} /> : null}
          {typesUnavailable ? (
            <div className="space-y-3">
              <p className="text-sm text-danger">{t('create.typesLoadFailed')}</p>
              <Button
                size="sm"
                variant="secondary"
                loading={typeSource.retrying}
                onClick={typeSource.onRetry}
              >
                {t('common.retry')}
              </Button>
            </div>
          ) : typeSource.loading ? (
            <p className="text-sm text-fg-muted">{t('create.typesLoading')}</p>
          ) : (
            <TypeTiles
              types={tileTypes}
              selectedId={typeId}
              onSelect={chooseType}
              categories={categories}
            />
          )}
        </div>
      ) : null}

      {step === 'device' ? (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <SuggestedInput
            label={t('create.fields.manufacturer')}
            value={manufacturer}
            onChange={setManufacturer}
            suggestions={manufacturerSuggestions.data ?? []}
            autoFocus
          />
          <SuggestedInput
            label={t('create.fields.model')}
            value={model}
            onChange={setModel}
            suggestions={modelSuggestions.data ?? []}
          />
        </div>
      ) : null}

      {step === 'fields' ? (
        <div className="space-y-5">
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <div className="sm:col-span-2">
              <Input
                label={`${t('create.fields.name')} *`}
                value={name}
                onChange={(event) => {
                  setName(event.target.value);
                  setMasterErrors((current) => ({ ...current, name: undefined }));
                }}
                error={masterFieldErrors.name}
                required
                autoFocus
              />
            </div>
            <Select
              label={t('create.fields.status')}
              value={status}
              onChange={(event) => setStatus(event.target.value)}
              options={statusOptions}
              error={masterFieldErrors.status}
            />
            {variants.length > 1 ? (
              <Select
                label={`${t('create.fields.variant')} *`}
                value={variant}
                onChange={(event) => {
                  setVariant(event.target.value);
                  setMasterErrors((current) => ({ ...current, variant: undefined }));
                }}
                options={[
                  { value: '', label: t('create.selectPlaceholder') },
                  ...variants.map((tpl, index) => ({
                    value: tpl.id,
                    label:
                      localizedText(tpl.display_name, i18n.language) ||
                      t('create.variantUnnamed', { number: index + 1 }),
                  })),
                ]}
                error={masterFieldErrors.variant}
              />
            ) : null}
            <div className="sm:col-span-2">
              <LocationPicker
                label={t('create.fields.location')}
                value={locationId}
                onChange={(picked) => setLocationId(picked?.id ?? null)}
              />
              {masterFieldErrors.location_id ? (
                <p className="mt-1 text-sm text-danger">{masterFieldErrors.location_id}</p>
              ) : null}
            </div>
            {resource === 'ci' ? (
              <div
                role="group"
                aria-labelledby={`${ownerGroupId}-title`}
                className="min-w-0 space-y-3 sm:col-span-2"
              >
                <h4 id={`${ownerGroupId}-title`} className="text-sm font-medium text-fg">
                  {t('ci.owner.title')}
                </h4>
                <p className="text-xs text-fg-muted">{t('ci.owner.hint')}</p>
                <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                  <div>
                    <UserTeamPicker
                      label={t('ci.owner.userLabel')}
                      kinds={['user']}
                      value={ownerUser}
                      onChange={(picked) => {
                        setOwnerUser(picked);
                        setSubmitError(null);
                      }}
                    />
                    {ownerError.owner_user_id ? (
                      <p role="alert" className="mt-1 text-sm text-danger">
                        {ownerError.owner_user_id}
                      </p>
                    ) : null}
                  </div>
                  <div>
                    <UserTeamPicker
                      label={t('ci.owner.teamLabel')}
                      kinds={['team']}
                      value={ownerTeam}
                      onChange={(picked) => {
                        setOwnerTeam(picked);
                        setSubmitError(null);
                      }}
                    />
                    {ownerError.owner_team_id ? (
                      <p role="alert" className="mt-1 text-sm text-danger">
                        {ownerError.owner_team_id}
                      </p>
                    ) : null}
                  </div>
                </div>
              </div>
            ) : null}
          </div>

          {form.isLoading ? (
            <p className="text-sm text-fg-muted">{t('create.formLoading')}</p>
          ) : null}
          {form.isError ? (
            <ErrorState
              title={t('create.formLoadFailed')}
              retryLabel={t('common.retry')}
              onRetry={() => void form.refetch()}
            />
          ) : null}

          {layout.length > 0 ? (
            <div className="border-t border-line pt-4">
              <DynamicForm
                fields={renderedDefs}
                layout={layout}
                values={values}
                errors={fieldErrors}
                onChange={setField}
              />
            </div>
          ) : null}

          <p className="rounded-lg border border-line bg-canvas px-3 py-2 text-xs text-fg-muted">
            {t('create.sourceHint')}
          </p>
        </div>
      ) : null}

      {banner ? <ErrorState title={banner} /> : null}

      <div className="sticky -bottom-6 z-10 -mx-6 -mb-6 flex justify-end gap-2 rounded-b-xl border-t border-line bg-panel px-6 py-4">
        <Button variant="secondary" onClick={onCancel}>
          {t('common.cancel')}
        </Button>
        {stepIndex > 0 ? (
          <Button variant="secondary" onClick={() => setStep(steps[stepIndex - 1]!)}>
            {t('create.back')}
          </Button>
        ) : null}
        {step === 'fields' ? (
          <Button type="submit" loading={submitting}>
            {t('common.create')}
          </Button>
        ) : (
          <Button type="submit" disabled={step === 'type' && (!typeId || typesUnavailable)}>
            {t('create.next')}
          </Button>
        )}
      </div>
    </form>
  );
}

/** OWNER_FIELDS are the request fields of the owner (WP-280). */
const OWNER_FIELDS: readonly string[] = ['owner_user_id', 'owner_team_id'];

export interface OwnerFieldErrors {
  owner_user_id?: string;
  owner_team_id?: string;
  /** mapped tells that the refusal is fully shown at an owner field. */
  mapped: boolean;
}

/**
 * ownerFieldErrors places a refusal of the owner at its field (CI-07,
 * WP-805): 422 with violations or the field of a field problem naming
 * owner_user_id or owner_team_id; 403 with the refused fields (RBA-07); a
 * 403 without fields while a team is chosen is the team scope (TEN-05,
 * E-11: a team-scoped principal creates only with a team of its scope).
 */
export function ownerFieldErrors(error: unknown, teamChosen: boolean, t: TFunction) {
  const out: OwnerFieldErrors = { mapped: false };
  if (!(error instanceof ApiError)) return out;
  if (error.status === 403) {
    const refused = error.conflictFields.filter((field) => OWNER_FIELDS.includes(field));
    if (refused.length > 0) {
      for (const field of refused) {
        out[field as 'owner_user_id' | 'owner_team_id'] =
          field === 'owner_team_id' && teamChosen
            ? t('ci.owner.teamForbidden')
            : t('ci.owner.forbidden');
      }
      out.mapped = true;
    } else if (teamChosen && error.conflictFields.length === 0) {
      // The server's own words stay in the banner: a 403 without fields
      // may also be a missing right.
      out.owner_team_id = t('ci.owner.teamForbidden');
    }
  } else {
    const byField = error.violationsByField();
    if (error.field && !(error.field in byField)) byField[error.field] = error.detail ?? '';
    for (const field of OWNER_FIELDS) {
      const detail = byField[field];
      if (detail !== undefined) {
        out[field as 'owner_user_id' | 'owner_team_id'] = detail || error.message;
        out.mapped = true;
      }
    }
  }
  return out;
}

interface SuggestedInputProps {
  label: string;
  value: string;
  onChange: (value: string) => void;
  suggestions: readonly string[];
  autoFocus?: boolean;
}

/**
 * SuggestedInput is a free text field with the stored values below it as
 * buttons; a click takes the value. The value already typed is not offered.
 */
function SuggestedInput({ label, value, onChange, suggestions, autoFocus }: SuggestedInputProps) {
  const { t } = useTranslation();
  const offered = suggestions.filter((suggestion) => suggestion !== value.trim()).slice(0, 8);
  return (
    <div className="space-y-2">
      <Input
        label={label}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        autoFocus={autoFocus}
        autoComplete="off"
      />
      {offered.length > 0 ? (
        <ul
          aria-label={t('create.suggestions', { field: label })}
          className="flex flex-wrap gap-1.5"
        >
          {offered.map((suggestion) => (
            <li key={suggestion}>
              <button
                type="button"
                onClick={() => onChange(suggestion)}
                className="rounded-full border border-line bg-panel px-2.5 py-0.5 text-xs text-fg hover:bg-fg/[0.04] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
              >
                {suggestion}
              </button>
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}
