/**
 * DynamicForm — metadata-driven form renderer (spec §18). Renders field
 * definitions (global + type + instance scopes) grouped by ui_group, with
 * conditional visibility/required/read-only evaluated live against the
 * current values. End users see a plain form; the rule model stays hidden.
 */
import { useMemo } from 'react';
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
}

interface RenderedField {
  def: FieldDefinition;
  state: FieldState;
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
}: DynamicFormProps) {
  const { t } = useTranslation();
  const groups = useMemo(() => groupFields(fields, values), [fields, values]);
  const errorText = (error: FieldError | null | undefined) =>
    !error ? undefined : typeof error === 'string' ? error : t(error.key, error.params);

  return (
    <div className="space-y-6">
      {Array.from(groups.entries()).map(([group, rendered]) => (
        <fieldset key={group || '_default'} className="min-w-0">
          {group ? (
            <legend className="mb-3 text-sm font-medium text-fg-muted">{group}</legend>
          ) : null}
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            {rendered.map(({ def, state }) => (
              <DynamicField
                key={def.name}
                def={def}
                state={state}
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
}

/** Long-form controls take the full row; the others sit two to a row. */
const WIDE_TYPES = new Set(['multiline', 'textarea', 'json']);

function DynamicField(props: DynamicFieldProps) {
  return (
    <div className={WIDE_TYPES.has(props.def.data_type) ? 'min-w-0 sm:col-span-2' : 'min-w-0'}>
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
