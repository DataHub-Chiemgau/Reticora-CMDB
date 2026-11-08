/**
 * DynamicForm — metadata-driven form renderer (spec §18). Renders field
 * definitions (global + type + instance scopes) grouped by ui_group, with
 * conditional visibility/required/read-only evaluated live against the
 * current values. End users see a plain form; the rule model stays hidden.
 *
 * With `layout` (WP-803) the form follows the sections of a resolved form
 * (WP-719): sections and fields in layout order, titles in the UI language,
 * the width as share of a 12-column grid. The server state of a field is
 * OR-combined with the live evaluation (required, read only); its
 * visibility applies to fields without a conditional visibility rule.
 */
import { useMemo } from 'react';
import { localizedText, type LocalizedText } from '../../api/cmdb';
import { useTranslation } from 'react-i18next';
import { Input } from '../ui/Input';
import { Select } from '../ui/Select';
import { Textarea } from '../ui/Textarea';
import {
  evaluateField,
  validateValue,
  type FieldDefinition,
  type FieldError,
  type FieldMessage,
  type FieldState,
} from '../../lib/fieldmeta';

export interface DynamicFormProps {
  fields: FieldDefinition[];
  values: Record<string, unknown>;
  onChange: (name: string, value: unknown) => void;
  /**
   * errors per field: client messages (validateAll) are translated into the
   * UI language, server texts are shown as sent.
   */
  errors?: Record<string, FieldError | null | undefined>;
  /** referenceOptions resolves reference-typed fields to selectable options. */
  referenceOptions?: Record<string, Array<{ value: string; label: string }>>;
  /**
   * layout arranges the fields in sections (WP-803); without it the fields
   * are grouped by ui_group and sort_order.
   */
  layout?: DynamicFormSection[];
}

/** The server state of a field in a layout (fieldmeta.FieldState, read_only as readOnly). */
export interface DynamicFormFieldState {
  visible: boolean;
  required: boolean;
  readOnly: boolean;
  allowedValues?: string[];
}

/** A field of a layout section: the name of a definition and its width (1–12). */
export interface DynamicFormLayoutField {
  name: string;
  width: number;
  state?: DynamicFormFieldState;
}

/** A section of a layout: key, title DE/EN and the fields in order. */
export interface DynamicFormSection {
  key: string;
  title: LocalizedText;
  visible: boolean;
  fields: DynamicFormLayoutField[];
}

interface RenderedField {
  def: FieldDefinition;
  state: FieldState;
  /** The width in the 12-column grid of a layout. */
  width?: number;
}

interface RenderedGroup {
  key: string;
  title: string;
  fields: RenderedField[];
}

// The column spans of the 12-column grid, spelled out for Tailwind.
const SPANS = [
  'sm:col-span-1',
  'sm:col-span-2',
  'sm:col-span-3',
  'sm:col-span-4',
  'sm:col-span-5',
  'sm:col-span-6',
  'sm:col-span-7',
  'sm:col-span-8',
  'sm:col-span-9',
  'sm:col-span-10',
  'sm:col-span-11',
  'sm:col-span-12',
];

function spanOf(width: number): string {
  const n = Math.min(12, Math.max(1, Math.round(width) || 12));
  return SPANS[n - 1] ?? 'sm:col-span-12';
}

/**
 * mergeState combines the live evaluation with the server state: required
 * and read only hold if either says so; the server visibility applies when
 * the field has no conditional visibility rule of its own.
 */
function mergeState(
  def: FieldDefinition,
  live: FieldState,
  server: DynamicFormFieldState | undefined,
): FieldState {
  if (!server) return live;
  const conditional = (def.conditional?.visible_when?.length ?? 0) > 0;
  return {
    visible: conditional ? live.visible : server.visible,
    required: live.required || server.required,
    readOnly: live.readOnly || server.readOnly,
    allowedValues: live.allowedValues ?? server.allowedValues,
  };
}

function layoutGroups(
  layout: DynamicFormSection[],
  fields: FieldDefinition[],
  values: Record<string, unknown>,
  language: string,
): RenderedGroup[] {
  const byName = new Map(fields.map((f) => [f.name, f]));
  const groups: RenderedGroup[] = [];
  for (const section of layout) {
    if (!section.visible) continue;
    const rendered: RenderedField[] = [];
    for (const field of section.fields) {
      const def = byName.get(field.name);
      if (!def) continue; // a name without definition is skipped
      const state = mergeState(def, evaluateField(def, values), field.state);
      if (!state.visible) continue;
      rendered.push({ def, state, width: field.width });
    }
    if (rendered.length === 0) continue;
    groups.push({
      key: section.key,
      title: localizedText(section.title, language),
      fields: rendered,
    });
  }
  return groups;
}

function groupFields(
  fields: FieldDefinition[],
  values: Record<string, unknown>,
): Map<string, RenderedField[]> {
  const sorted = [...fields].sort((a, b) => (a.sort_order ?? 0) - (b.sort_order ?? 0));
  const groups = new Map<string, RenderedField[]>();
  for (const def of sorted) {
    const state = evaluateField(def, values);
    if (!state.visible) continue; // conditional visibility (§3)
    const group = def.ui_group || '';
    if (!groups.has(group)) groups.set(group, []);
    groups.get(group)!.push({ def, state });
  }
  return groups;
}

export function DynamicForm({
  fields,
  values,
  onChange,
  errors = {},
  referenceOptions = {},
  layout,
}: DynamicFormProps) {
  const { t, i18n } = useTranslation();
  const language = i18n.language;
  const groups = useMemo<RenderedGroup[]>(
    () =>
      layout
        ? layoutGroups(layout, fields, values, language)
        : Array.from(groupFields(fields, values).entries()).map(([group, rendered]) => ({
            key: group || '_default',
            title: group,
            fields: rendered,
          })),
    [layout, fields, values, language],
  );
  const errorText = (error: FieldError | null | undefined) =>
    !error ? undefined : typeof error === 'string' ? error : t(error.key, error.params);

  return (
    <div className="space-y-6">
      {groups.map((group) => (
        <fieldset
          key={group.key}
          className="min-w-0"
          data-section-key={layout ? group.key : undefined}
        >
          {group.title ? (
            <legend className="mb-3 text-sm font-medium text-fg-muted">{group.title}</legend>
          ) : null}
          <div
            className={
              layout
                ? 'grid grid-cols-1 gap-4 sm:grid-cols-12'
                : 'grid grid-cols-1 gap-4 sm:grid-cols-2'
            }
          >
            {group.fields.map(({ def, state, width }) => (
              <DynamicField
                key={def.name}
                def={def}
                state={state}
                width={layout ? width : undefined}
                value={values[def.name]}
                error={errorText(errors[def.name])}
                options={referenceOptions[def.name]}
                onChange={(v) => onChange(def.name, v)}
              />
            ))}
          </div>
        </fieldset>
      ))}
    </div>
  );
}

interface DynamicFieldProps {
  def: FieldDefinition;
  state: FieldState;
  value: unknown;
  error?: string;
  options?: Array<{ value: string; label: string }>;
  onChange: (value: unknown) => void;
  /** The width in the 12-column grid of a layout; absent outside a layout. */
  width?: number;
}

/** Long-form controls take the full row; the others sit two to a row. */
const WIDE_TYPES = new Set(['multiline', 'textarea', 'json']);

function DynamicField(props: DynamicFieldProps) {
  const span =
    props.width !== undefined
      ? `min-w-0 ${spanOf(props.width)}`
      : WIDE_TYPES.has(props.def.data_type)
        ? 'min-w-0 sm:col-span-2'
        : 'min-w-0';
  return (
    <div className={span} data-field-name={props.width !== undefined ? props.def.name : undefined}>
      <DynamicFieldControl {...props} />
    </div>
  );
}

function DynamicFieldControl({ def, state, value, error, options, onChange }: DynamicFieldProps) {
  const label = (def.label || def.name) + (state.required ? ' *' : '');
  const common = {
    label,
    error: error ?? undefined,
    disabled: state.readOnly,
    required: state.required,
    title: def.description,
  };

  const textValue = value === undefined || value === null ? '' : String(value);

  switch (def.data_type) {
    case 'multiline':
    case 'textarea':
      return (
        <Textarea
          {...common}
          rows={3}
          value={textValue}
          onChange={(e) => onChange(e.target.value)}
        />
      );
    case 'boolean':
      return (
        <label className="flex min-h-9 items-center gap-2 text-sm text-fg sm:mt-[1.625rem]">
          <input
            type="checkbox"
            checked={value === true}
            disabled={state.readOnly}
            onChange={(e) => onChange(e.target.checked)}
          />
          {label}
        </label>
      );
    case 'enum':
    case 'user':
    case 'team':
    case 'location':
    case 'ci_ref':
    case 'asset_ref':
    case 'contract_ref': {
      // Reference and enum fields render as selects; reference options come
      // from the caller (autocomplete selectors), enum options from the
      // definition or the conditional allowed-values rule.
      const enumOptions = (state.allowedValues ?? def.enum_values ?? []).map((v) => ({
        value: v,
        label: v,
      }));
      const opts = options && options.length > 0 ? options : enumOptions;
      return (
        <Select
          {...common}
          options={[{ value: '', label: '—' }, ...opts]}
          value={textValue}
          onChange={(e) => onChange(e.target.value || undefined)}
        />
      );
    }
    case 'integer':
    case 'decimal':
    case 'number':
      return (
        <Input
          {...common}
          type="number"
          step={def.data_type === 'integer' ? 1 : 'any'}
          value={textValue}
          onChange={(e) => onChange(e.target.value === '' ? undefined : Number(e.target.value))}
        />
      );
    case 'date':
      return (
        <Input
          {...common}
          type="date"
          value={textValue}
          onChange={(e) => onChange(e.target.value)}
        />
      );
    case 'datetime':
      return (
        <Input
          {...common}
          type="datetime-local"
          value={textValue}
          onChange={(e) => onChange(e.target.value)}
        />
      );
    case 'json':
      return (
        <Textarea
          label={label}
          error={error ?? undefined}
          className="font-mono text-xs"
          rows={4}
          value={
            typeof value === 'string'
              ? value
              : value === undefined
                ? ''
                : JSON.stringify(value, null, 2)
          }
          disabled={state.readOnly}
          onChange={(e) => {
            try {
              onChange(JSON.parse(e.target.value));
            } catch {
              onChange(e.target.value);
            }
          }}
        />
      );
    default:
      // text, ip, mac, url, email, file, multi_enum-as-text fallback.
      return (
        <Input
          {...common}
          type={def.data_type === 'email' ? 'email' : def.data_type === 'url' ? 'url' : 'text'}
          value={textValue}
          onChange={(e) => onChange(e.target.value)}
        />
      );
  }
}

/**
 * validateAll runs client-side validation for all visible fields. Returns a
 * map of field name → error message (null when valid); DynamicForm
 * translates the messages.
 */
export function validateAll(
  fields: FieldDefinition[],
  values: Record<string, unknown>,
): Record<string, FieldMessage | null> {
  const errors: Record<string, FieldMessage | null> = {};
  for (const def of fields) {
    const state = evaluateField(def, values);
    if (!state.visible) {
      errors[def.name] = null;
      continue;
    }
    errors[def.name] = validateValue(def, values[def.name], state);
  }
  return errors;
}
