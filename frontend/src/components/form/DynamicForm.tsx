/**
 * DynamicForm — metadata-driven form renderer (spec §18). Renders field
 * definitions (global + type + instance scopes) grouped by ui_group, with
 * conditional visibility/required/read-only evaluated live against the
 * current values. End users see a plain form; the rule model stays hidden.
 */
import { useMemo } from 'react';
import { Input } from '../ui/Input';
import { Select } from '../ui/Select';
import {
  evaluateField,
  validateValue,
  type FieldDefinition,
  type FieldState,
} from '../../lib/fieldmeta';

export interface DynamicFormProps {
  fields: FieldDefinition[];
  values: Record<string, unknown>;
  onChange: (name: string, value: unknown) => void;
  errors?: Record<string, string | null>;
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
  const groups = useMemo(() => groupFields(fields, values), [fields, values]);

  return (
    <div className="space-y-6">
      {Array.from(groups.entries()).map(([group, rendered]) => (
        <fieldset key={group || '_default'} className="space-y-4">
          {group ? (
            <legend className="text-sm font-semibold uppercase tracking-wide text-gray-500 dark:text-gray-400">
              {group}
            </legend>
          ) : null}
          {rendered.map(({ def, state }) => (
            <DynamicField
              key={def.name}
              def={def}
              state={state}
              value={values[def.name]}
              error={errors[def.name] ?? undefined}
              options={referenceOptions[def.name]}
              onChange={(v) => onChange(def.name, v)}
            />
          ))}
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

function DynamicField({ def, state, value, error, options, onChange }: DynamicFieldProps) {
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
    case 'textarea':
      return (
        <div className="space-y-1.5">
          <label className="block text-sm font-medium text-gray-700 dark:text-gray-200">
            {label}
          </label>
          <textarea
            className="w-full rounded-lg border border-gray-300 bg-white px-3 py-2 text-sm shadow-sm outline-none focus:border-primary focus:ring-2 focus:ring-primary/20 dark:border-gray-700 dark:bg-gray-900 dark:text-gray-100"
            value={textValue}
            disabled={state.readOnly}
            required={state.required}
            rows={3}
            onChange={(e) => onChange(e.target.value)}
          />
          {error ? <p className="text-sm text-red-600">{error}</p> : null}
        </div>
      );
    case 'boolean':
      return (
        <label className="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-200">
          <input
            type="checkbox"
            checked={value === true}
            disabled={state.readOnly}
            onChange={(e) => onChange(e.target.checked)}
            className="h-4 w-4 rounded border-gray-300"
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
        <div className="space-y-1.5">
          <label className="block text-sm font-medium text-gray-700 dark:text-gray-200">
            {label}
          </label>
          <textarea
            className="w-full rounded-lg border border-gray-300 font-mono text-xs dark:border-gray-700 dark:bg-gray-900"
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
          {error ? <p className="text-sm text-red-600">{error}</p> : null}
        </div>
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
 * map of field name → error message (null when valid).
 */
export function validateAll(
  fields: FieldDefinition[],
  values: Record<string, unknown>,
): Record<string, string | null> {
  const errors: Record<string, string | null> = {};
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
