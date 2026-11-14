/**
 * FormRenderer renders a form definition (WFL-01, WP-1133) for the
 * submission on the forms page and for the live preview of the form editor.
 * It builds no field logic of its own: the JSON schema (schema.properties,
 * schema.required) becomes FieldDefinitions of DynamicForm (string → text,
 * text → textarea, number → decimal, integer, boolean, enum with
 * enum_values, date); the MET-45 conditions of ui_hints.fields.<field>
 * (visible_when, required_when, readonly_when, WP-1101) are evaluated with
 * evaluateCondition on every input and passed as the state of each field in
 * a layout of one section. DynamicForm then hides (and FormFill does not
 * send) invisible fields, marks required ones and locks read-only ones. A
 * field of type signature is the SignatureField (renderControl); the
 * picture is uploaded as a PNG document on submission and the value is the
 * document id (uploadSignatures).
 *
 * The model of the editor lives here as well: parseForm reads the fields of
 * a schema with its ui_hints, buildForm writes them back. The round trip
 * keeps what the editor does not model: the other keywords of a property
 * (minLength, pattern, description, …), the other hints of a field, the
 * other top-level keys of schema and ui_hints (formExtras) and the JSON
 * type of enum values. A field the editor cannot represent (an object or
 * array property, a condition it cannot parse) is marked unsupported; the
 * editor then stays read only instead of overwriting it.
 *
 * Conditions are evaluated like the server does (WP-1101): against the
 * values that are sent, i.e. without the values of hidden and read-only
 * fields, until the states no longer change (resolveForm).
 */
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ApiError, documentApi, type DocumentLinkTarget, type JsonRecord } from '../../api/client';
import { localizedText, type LocalizedText } from '../../api/cmdb';
import type { FieldDefinition } from '../../lib/fieldmeta';
import { evaluateCondition, parseCondition, type Condition } from '../../rules/rules';
import { Button } from '../ui/Button';
import { DynamicForm, type DynamicFormSection } from './DynamicForm';
import { SignatureField } from './SignatureField';

/** The field types of the form editor (AK1). */
export const FORM_FIELD_TYPES = [
  'string',
  'text',
  'number',
  'integer',
  'boolean',
  'enum',
  'date',
  'signature',
] as const;
export type FormFieldType = (typeof FORM_FIELD_TYPES)[number];

/** The condition kinds of ui_hints.fields.<field> (WP-1101). */
export const CONDITION_KINDS = ['visible_when', 'required_when', 'readonly_when'] as const;
export type ConditionKind = (typeof CONDITION_KINDS)[number];

/** A field of the form editor. */
export interface FormField {
  key: string;
  title: LocalizedText;
  type: FormFieldType;
  required: boolean;
  enumValues: string[];
  conditions: Partial<Record<ConditionKind, Condition>>;
  /**
   * The stored property and hints as read (parseForm): buildForm keeps
   * their keys the editor does not model; rawType is the type they had.
   */
  raw?: { prop: Record<string, unknown>; hints: Record<string, unknown>; type: FormFieldType };
  /** unsupported: the editor cannot represent this field without loss. */
  unsupported?: boolean;
}

/** The pattern of a field key (AK1). */
export const FIELD_KEY_PATTERN = /^[a-z][a-z0-9_]*$/;

type SchemaProperty = {
  type?: string;
  title?: string;
  enum?: unknown[];
  format?: string;
} & Record<string, unknown>;

/** The keys of a property and of the hints of a field the editor models. */
const MODELLED_PROP_KEYS = ['type', 'title', 'enum', 'format'];
const MODELLED_HINT_KEYS = ['label', 'widget', ...CONDITION_KINDS];
type FieldHints = {
  label?: LocalizedText;
  widget?: string;
} & Partial<Record<ConditionKind, unknown>>;

function asObject(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {};
}

function fieldType(prop: SchemaProperty, hints: FieldHints): FormFieldType {
  if (prop.format === 'signature') return 'signature';
  if (prop.format === 'date') return 'date';
  if (Array.isArray(prop.enum)) return 'enum';
  switch (prop.type) {
    case 'number':
      return 'number';
    case 'integer':
      return 'integer';
    case 'boolean':
      return 'boolean';
  }
  return hints.widget === 'textarea' ? 'text' : 'string';
}

/**
 * parseForm reads the fields of a schema in the order of ui_hints.order
 * (then the order of the properties); a condition that does not parse is
 * left out.
 */
export function parseForm(
  schema: JsonRecord | undefined,
  uiHints: JsonRecord | undefined,
): FormField[] {
  const props = asObject(schema?.properties) as Record<string, SchemaProperty>;
  const required = new Set(Array.isArray(schema?.required) ? (schema.required as string[]) : []);
  const hints = asObject(asObject(uiHints).fields) as Record<string, FieldHints>;
  const order = Array.isArray(uiHints?.order) ? (uiHints.order as string[]) : [];
  const keys = [
    ...order.filter((k) => k in props),
    ...Object.keys(props).filter((k) => !order.includes(k)),
  ];
  return keys.map((key) => {
    const prop = asObject(props[key]) as SchemaProperty;
    const h = asObject(hints[key]) as FieldHints;
    const conditions: FormField['conditions'] = {};
    // Nested objects and arrays are not modelled by the editor.
    let unsupported = prop.type === 'object' || prop.type === 'array';
    for (const kind of CONDITION_KINDS) {
      if (h[kind] == null) continue;
      try {
        conditions[kind] = parseCondition(h[kind]);
      } catch {
        // An unparsable condition cannot be edited without losing it.
        unsupported = true;
      }
    }
    const label = asObject(h.label) as LocalizedText;
    const type = fieldType(prop, h);
    return {
      key,
      title: { de: label.de ?? prop.title ?? '', en: label.en ?? '' },
      type,
      required: required.has(key),
      enumValues: Array.isArray(prop.enum) ? prop.enum.map(String) : [],
      conditions,
      raw: { prop: { ...prop }, hints: { ...h }, type },
      ...(unsupported ? { unsupported } : {}),
    };
  });
}

/**
 * formExtras keeps the top-level keys of schema and ui_hints the editor
 * does not model (everything but type, properties and required resp. order
 * and fields), for buildForm.
 */
export function formExtras(
  schema: JsonRecord | undefined,
  uiHints: JsonRecord | undefined,
): { schema: JsonRecord; ui_hints: JsonRecord } {
  const strip = (obj: JsonRecord | undefined, keys: string[]) =>
    Object.fromEntries(Object.entries(obj ?? {}).filter(([k]) => !keys.includes(k))) as JsonRecord;
  return {
    schema: strip(schema, ['type', 'properties', 'required']),
    ui_hints: strip(uiHints, ['order', 'fields']),
  };
}

function omit(obj: Record<string, unknown>, keys: string[]): Record<string, unknown> {
  return Object.fromEntries(Object.entries(obj).filter(([k]) => !keys.includes(k)));
}

/** sameBase: the stored keywords of a property still fit the edited type. */
function sameBase(a: FormFieldType, b: FormFieldType): boolean {
  const text: FormFieldType[] = ['string', 'text'];
  return a === b || (text.includes(a) && text.includes(b));
}

/** enumOut maps the edited values back to the JSON type they were stored with. */
function enumOut(values: string[], stored: unknown[] | undefined): unknown[] {
  const numeric = (stored ?? []).length > 0 && (stored ?? []).every((v) => typeof v === 'number');
  return values.map((v) => {
    const same = (stored ?? []).find((s) => String(s) === v);
    if (same !== undefined) return same;
    return numeric && v.trim() !== '' && !Number.isNaN(Number(v)) ? Number(v) : v;
  });
}

/**
 * buildForm writes the fields as JSON schema and ui_hints (WP-1101 format).
 * The keys of the stored property and hints the editor does not model stay
 * (the type-specific keywords only while the type is unchanged), as do the
 * extras of schema and ui_hints (formExtras).
 */
export function buildForm(
  fields: FormField[],
  extras: { schema: JsonRecord; ui_hints: JsonRecord } = { schema: {}, ui_hints: {} },
): { schema: JsonRecord; ui_hints: JsonRecord } {
  const properties: Record<string, SchemaProperty> = {};
  const hints: Record<string, FieldHints> = {};
  for (const f of fields) {
    if (f.unsupported && f.raw) {
      // Not representable: property and hints stay exactly as stored.
      properties[f.key] = f.raw.prop as SchemaProperty;
      if (Object.keys(f.raw.hints).length > 0) hints[f.key] = f.raw.hints as FieldHints;
      continue;
    }
    const kept = f.raw
      ? sameBase(f.raw.type, f.type)
        ? omit(f.raw.prop, MODELLED_PROP_KEYS)
        : 'description' in f.raw.prop
          ? { description: f.raw.prop.description }
          : {}
      : {};
    const prop: SchemaProperty = { ...kept, type: 'string' };
    switch (f.type) {
      case 'number':
      case 'integer':
      case 'boolean':
        prop.type = f.type;
        break;
      case 'enum': {
        const stored = f.raw?.type === 'enum' ? f.raw.prop : undefined;
        // The JSON type of a stored enum stays (integer, string or none).
        if (stored) {
          if (typeof stored.type === 'string') prop.type = stored.type;
          else delete prop.type;
        }
        prop.enum = enumOut(f.enumValues, Array.isArray(stored?.enum) ? stored.enum : undefined);
        break;
      }
      case 'date':
        prop.format = 'date';
        break;
      case 'signature':
        prop.format = 'signature';
        break;
    }
    const title = f.title.de || f.title.en;
    if (title) prop.title = title;
    properties[f.key] = prop;
    const h: FieldHints = (f.raw ? omit(f.raw.hints, MODELLED_HINT_KEYS) : {}) as FieldHints;
    if (f.title.de || f.title.en) h.label = { ...f.title };
    if (f.type === 'text') h.widget = 'textarea';
    for (const kind of CONDITION_KINDS) {
      if (f.conditions[kind]) h[kind] = f.conditions[kind];
    }
    if (Object.keys(h).length > 0) hints[f.key] = h;
  }
  const required = fields.filter((f) => f.required).map((f) => f.key);
  const schema: JsonRecord = { ...extras.schema, type: 'object', properties };
  if (required.length > 0) schema.required = required;
  return {
    schema,
    ui_hints: { ...extras.ui_hints, order: fields.map((f) => f.key), fields: hints },
  };
}

/** conditionFields lists the fields a condition names. */
export function conditionFields(cond: Condition | undefined): string[] {
  if (!cond) return [];
  if ('rules' in cond) return cond.rules.flatMap(conditionFields);
  return [cond.field];
}

const DATA_TYPES: Record<FormFieldType, FieldDefinition['data_type']> = {
  string: 'text',
  text: 'textarea',
  number: 'decimal',
  integer: 'integer',
  boolean: 'boolean',
  enum: 'enum',
  date: 'date',
  signature: 'text',
};

/**
 * The field states of the values: visibility, required and read only per
 * field, evaluated against exactly these values (see resolveForm for the
 * values a submission sends).
 */
export function fieldStates(fields: FormField[], values: Record<string, unknown>) {
  const holds = (cond: Condition | undefined, fallback: boolean) =>
    cond ? evaluateCondition(cond, values) : fallback;
  return Object.fromEntries(
    fields.map((f) => {
      const required = f.required || holds(f.conditions.required_when, false);
      // A required field is never hidden (the server semantics, WP-1101).
      const visible = required || holds(f.conditions.visible_when, true);
      return [f.key, { visible, required, readOnly: holds(f.conditions.readonly_when, false) }];
    }),
  );
}

/**
 * resolveForm returns the values a submission sends and the field states
 * evaluated against them, as the server evaluates the conditions (WP-1101):
 * the values of hidden fields and of read-only fields (a submission has no
 * prefill, the value of a read-only field must stay empty) are dropped, and
 * the conditions are evaluated again against what remains until nothing
 * changes. A field hidden because another field became hidden is therefore
 * not sent either.
 */
export function resolveForm(fields: FormField[], values: Record<string, unknown>) {
  const known = new Set(fields.map((f) => f.key));
  let sent = Object.fromEntries(
    Object.entries(values).filter(([key, v]) => known.has(key) && v !== undefined),
  );
  let states = fieldStates(fields, sent);
  for (let round = 0; round <= fields.length; round++) {
    const next = Object.fromEntries(
      Object.entries(sent).filter(([key]) => states[key]?.visible && !states[key]?.readOnly),
    );
    if (Object.keys(next).length === Object.keys(sent).length) break;
    sent = next;
    states = fieldStates(fields, sent);
  }
  return { values: sent, states };
}

/** visibleValues keeps the values that are sent: neither hidden nor read-only fields. */
export function visibleValues(fields: FormField[], values: Record<string, unknown>) {
  return resolveForm(fields, values).values;
}

/** SignatureUploadError: a signature picture could not be stored. */
export class SignatureUploadError extends Error {
  constructor(readonly cause: unknown) {
    super('signature upload failed');
  }
}

/**
 * uploadSignatures uploads every signature picture of values as a PNG
 * document (POST /documents with links when given, E-61; PUT
 * /documents/{id}/content) and returns the values with the document ids.
 * onStored hears each stored id at once, so a failed submission retries
 * with it instead of creating the document again. A document whose content
 * fails is deleted again.
 */
export async function uploadSignatures(
  fields: FormField[],
  values: Record<string, unknown>,
  options: { links?: DocumentLinkTarget[]; onStored?: (key: string, id: string) => void } = {},
): Promise<Record<string, unknown>> {
  const out = { ...values };
  for (const f of fields) {
    const blob = out[f.key];
    if (f.type !== 'signature' || !(blob instanceof Blob)) continue;
    let doc;
    try {
      doc = await documentApi.create({
        title: `signature-${f.key}`,
        file_name: `signature-${f.key}.png`,
        mime_type: 'image/png',
        file_size: blob.size,
        ...(options.links && options.links.length > 0 ? { links: options.links } : {}),
      });
    } catch (err) {
      throw new SignatureUploadError(err);
    }
    try {
      await documentApi.uploadContent(doc.id, blob);
    } catch (err) {
      await documentApi.delete(doc.id).catch(() => undefined);
      throw new SignatureUploadError(err);
    }
    out[f.key] = doc.id;
    options.onStored?.(f.key, doc.id);
  }
  return out;
}

/** fieldOfError names the field of a 400/422 of a submission (values.<f>, $.<f>, <f>). */
export function fieldOfError(err: unknown, fields: FormField[]): string | undefined {
  if (!(err instanceof ApiError) || (err.status !== 400 && err.status !== 422) || !err.field) {
    return undefined;
  }
  const key = err.field.replace(/^(?:values\.|\$\.|\/values\/)/, '').split(/[./[]/)[0];
  return fields.find((f) => f.key === key)?.key;
}

export interface FormRendererProps {
  fields: FormField[];
  values: Record<string, unknown>;
  onChange: (values: Record<string, unknown>) => void;
  /** readOnly locks every field (an older version in the preview). */
  readOnly?: boolean;
  errors?: Record<string, string | undefined>;
}

export function FormRenderer({
  fields,
  values,
  onChange,
  readOnly = false,
  errors,
}: FormRendererProps) {
  const { i18n } = useTranslation();
  const language = i18n.language;
  const definitions = useMemo<FieldDefinition[]>(
    () =>
      fields.map((f) => ({
        name: f.key,
        label: localizedText(f.title, language) || f.key,
        data_type: DATA_TYPES[f.type],
        enum_values: f.type === 'enum' ? f.enumValues : undefined,
      })),
    [fields, language],
  );
  const { states } = resolveForm(fields, values);
  const layout: DynamicFormSection[] = [
    {
      key: 'form',
      title: {},
      visible: true,
      fields: fields.map((f) => ({
        name: f.key,
        width: 12,
        state: {
          visible: states[f.key]?.visible ?? true,
          required: states[f.key]?.required ?? false,
          readOnly: readOnly || (states[f.key]?.readOnly ?? false),
        },
      })),
    },
  ];
  const renderControl = Object.fromEntries(
    fields
      .filter((f) => f.type === 'signature')
      .map((f) => [
        f.key,
        (props: {
          label: string;
          value: unknown;
          state: { readOnly: boolean; required: boolean };
          error?: string;
          onChange: (v: unknown) => void;
        }) => (
          <SignatureField
            label={props.label}
            value={props.value}
            readOnly={props.state.readOnly}
            required={props.state.required}
            error={props.error}
            onChange={props.onChange}
          />
        ),
      ]),
  );
  return (
    <DynamicForm
      fields={definitions}
      values={values}
      onChange={(name, value) => onChange({ ...values, [name]: value })}
      errors={errors}
      layout={layout}
      renderControl={renderControl}
    />
  );
}

export interface FormFillProps {
  fields: FormField[];
  /** onSubmit gets the sent values with the signatures uploaded. */
  onSubmit: (values: Record<string, unknown>) => Promise<unknown> | void;
  submitting?: boolean;
  submitLabel?: string;
  /** The objects the signature documents are linked to (E-61), e.g. the CI or ticket. */
  signatureLinks?: DocumentLinkTarget[];
  /**
   * signatureUnavailable: signatures cannot be stored without exposing them
   * (a client form without a link target: an unlinked document is visible
   * organization-wide); a form with a signature field is then not
   * submitted.
   */
  signatureUnavailable?: boolean;
}

/**
 * FormFill holds the values of one submission: it checks the required
 * fields, uploads the signatures and submits the values resolveForm sends.
 * A field that becomes read-only loses its value (a submission has no
 * prefill). A 400/422 of the server appears at the field of its path.
 */
export function FormFill({
  fields,
  onSubmit,
  submitting = false,
  submitLabel,
  signatureLinks,
  signatureUnavailable = false,
}: FormFillProps) {
  const { t } = useTranslation();
  const [values, setValues] = useState<Record<string, unknown>>({});
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<string>();
  const blocked = signatureUnavailable && fields.some((f) => f.type === 'signature');

  const change = (next: Record<string, unknown>) => {
    const { states } = resolveForm(fields, next);
    setValues(Object.fromEntries(Object.entries(next).filter(([key]) => !states[key]?.readOnly)));
  };

  const submit = async () => {
    if (blocked) return;
    setMessage(undefined);
    const { states, values: sent } = resolveForm(fields, values);
    const missing: Record<string, string> = {};
    for (const f of fields) {
      const v = sent[f.key];
      if (
        states[f.key]?.visible &&
        states[f.key]?.required &&
        !states[f.key]?.readOnly &&
        (v === undefined || v === '' || v === null)
      ) {
        missing[f.key] = t('forms.required');
      }
    }
    setErrors(missing);
    if (Object.keys(missing).length > 0) return;
    setBusy(true);
    try {
      const uploaded = await uploadSignatures(fields, sent, {
        links: signatureLinks,
        // A retry after a failed submission reuses the stored document.
        onStored: (key, id) => setValues((current) => ({ ...current, [key]: id })),
      });
      await onSubmit(uploaded);
      setValues({});
    } catch (err) {
      if (err instanceof SignatureUploadError) {
        setMessage(t('forms.signature.uploadFailed'));
        return;
      }
      const field = fieldOfError(err, fields);
      if (field) {
        setErrors({ [field]: (err as ApiError).detail ?? t('forms.submitFailed') });
        return;
      }
      setMessage(t('forms.submitFailed'));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="max-w-xl space-y-4">
      {blocked ? (
        <p
          role="note"
          className="rounded-lg border border-amber-500/40 bg-amber-500/10 p-3 text-sm text-fg"
        >
          {t('forms.signature.unavailable')}
        </p>
      ) : null}
      <FormRenderer fields={fields} values={values} onChange={change} errors={errors} />
      {message ? (
        <p role="alert" className="text-sm text-danger">
          {message}
        </p>
      ) : null}
      <Button onClick={() => void submit()} loading={busy || submitting} disabled={blocked}>
        {submitLabel ?? t('forms.submit')}
      </Button>
    </div>
  );
}

export interface SubmissionViewProps {
  /** The fields of the schema version of the submission (not the current one). */
  fields: FormField[];
  values: Record<string, unknown>;
}

/**
 * SubmissionView shows the values of a submission with the labels and types
 * of the schema version it was captured against (AK5); a value the version
 * does not know is shown under its key.
 */
export function SubmissionView({ fields, values }: SubmissionViewProps) {
  const { t, i18n } = useTranslation();
  const known = new Set(fields.map((f) => f.key));
  const rows = [
    ...fields
      .filter((f) => f.key in values)
      .map((f) => ({ key: f.key, field: f as FormField | undefined })),
    ...Object.keys(values)
      .filter((k) => !known.has(k))
      .map((k) => ({ key: k, field: undefined as FormField | undefined })),
  ];
  const show = (field: FormField | undefined, v: unknown) => {
    if (field?.type === 'boolean') return v === true ? t('forms.yes') : t('forms.no');
    if (field?.type === 'signature') return t('forms.signature.stored');
    return typeof v === 'object' ? JSON.stringify(v) : String(v);
  };
  return (
    <dl className="grid grid-cols-[minmax(0,10rem)_minmax(0,1fr)] gap-x-3 gap-y-1 text-sm">
      {rows.map(({ key, field }) => (
        <div key={key} className="contents">
          <dt className="text-fg-muted">
            {field ? localizedText(field.title, i18n.language) || key : key}
          </dt>
          <dd className="text-fg [overflow-wrap:anywhere]">{show(field, values[key])}</dd>
        </div>
      ))}
    </dl>
  );
}
