/**
 * fieldmeta — TypeScript mirror of the Go conditional-rule and validation
 * evaluator (backend/internal/fieldmeta). Forms evaluate conditional
 * visibility/required/read-only/allowed-values client-side so end users never
 * see the underlying rule model (spec §3, §18).
 */

export type FieldDataType =
  | 'text'
  | 'textarea'
  | 'integer'
  | 'decimal'
  | 'boolean'
  | 'date'
  | 'datetime'
  | 'enum'
  | 'multi_enum'
  | 'ip'
  | 'mac'
  | 'url'
  | 'email'
  | 'json'
  | 'file'
  | 'user'
  | 'team'
  | 'location'
  | 'ci_ref'
  | 'asset_ref'
  | 'contract_ref'
  // legacy aliases from the migration-000002 CHECK constraint
  | 'string'
  | 'number';

export interface Predicate {
  field: string;
  op:
    | 'eq'
    | 'ne'
    | 'in'
    | 'not_in'
    | 'gt'
    | 'gte'
    | 'lt'
    | 'lte'
    | 'contains'
    | 'empty'
    | 'not_empty';
  value?: unknown;
}

export interface ConditionalValues {
  when: Predicate[];
  values: unknown[];
}

export interface ConditionalRules {
  visible_when?: Predicate[];
  required_when?: Predicate[];
  read_only_when?: Predicate[];
  allowed_values_when?: ConditionalValues;
}

export interface ValidationRules {
  min?: number;
  max?: number;
  min_length?: number;
  max_length?: number;
  pattern?: string;
  options?: string[];
}

export interface FieldDefinition {
  name: string;
  label?: string;
  description?: string;
  data_type: FieldDataType;
  required?: boolean;
  default_value?: string;
  enum_values?: string[];
  ui_group?: string;
  sort_order?: number;
  validation?: ValidationRules;
  conditional?: ConditionalRules;
  reference_target?: string;
}

export interface FieldState {
  visible: boolean;
  required: boolean;
  readOnly: boolean;
  allowedValues?: string[];
}

function isEmpty(v: unknown): boolean {
  if (v === null || v === undefined) return true;
  if (typeof v === 'string') return v.trim() === '';
  if (Array.isArray(v)) return v.length === 0;
  if (typeof v === 'object') return Object.keys(v as object).length === 0;
  return false;
}

function toNumber(v: unknown): number | undefined {
  if (typeof v === 'number') return v;
  if (typeof v === 'string' && v.trim() !== '') {
    const n = Number(v);
    return Number.isNaN(n) ? undefined : n;
  }
  return undefined;
}

function looseEqual(a: unknown, b: unknown): boolean {
  if (isEmpty(a) && isEmpty(b)) return true;
  const an = toNumber(a);
  if (an !== undefined) {
    const bn = toNumber(b);
    return bn !== undefined && an === bn;
  }
  return String(a) === String(b);
}

function inList(actual: unknown, list: unknown): boolean {
  if (!Array.isArray(list)) return false;
  return list.some((item) => looseEqual(actual, item));
}

function containsValue(actual: unknown, needle: unknown): boolean {
  if (typeof actual === 'string') return actual.includes(String(needle));
  if (Array.isArray(actual)) return actual.some((item) => looseEqual(item, needle));
  return false;
}

export function matchPredicate(p: Predicate, actual: unknown): boolean {
  switch (p.op) {
    case 'empty':
      return isEmpty(actual);
    case 'not_empty':
      return !isEmpty(actual);
    case 'eq':
      return looseEqual(actual, p.value);
    case 'ne':
      return !looseEqual(actual, p.value);
    case 'in':
      return inList(actual, p.value);
    case 'not_in':
      return !inList(actual, p.value);
    case 'gt':
    case 'gte':
    case 'lt':
    case 'lte': {
      const a = toNumber(actual);
      const b = toNumber(p.value);
      if (a === undefined || b === undefined) return false;
      if (p.op === 'gt') return a > b;
      if (p.op === 'gte') return a >= b;
      if (p.op === 'lt') return a < b;
      return a <= b;
    }
    case 'contains':
      return containsValue(actual, p.value);
    default:
      return false;
  }
}

function matchAll(preds: Predicate[], values: Record<string, unknown>): boolean {
  return preds.every((p) => matchPredicate(p, values[p.field]));
}

/**
 * Evaluate resolves a field's effective state against the current form values.
 * Mirrors fieldmeta.Evaluate in Go.
 */
export function evaluateField(def: FieldDefinition, values: Record<string, unknown>): FieldState {
  const state: FieldState = {
    visible: true,
    required: !!def.required,
    readOnly: false,
    allowedValues: def.enum_values,
  };
  const c = def.conditional;
  if (!c) return state;

  if (c.visible_when && c.visible_when.length > 0 && !matchAll(c.visible_when, values)) {
    return { visible: false, required: false, readOnly: false, allowedValues: state.allowedValues };
  }
  if (c.required_when && c.required_when.length > 0 && matchAll(c.required_when, values)) {
    state.required = true;
  }
  if (c.read_only_when && c.read_only_when.length > 0 && matchAll(c.read_only_when, values)) {
    state.readOnly = true;
  }
  if (c.allowed_values_when && matchAll(c.allowed_values_when.when, values)) {
    state.allowedValues = c.allowed_values_when.values.map((v) => String(v));
  }
  return state;
}

/**
 * validateValue checks a value against its definition. Returns an error
 * message or null. Empty values pass unless the field is required.
 */
export function validateValue(
  def: FieldDefinition,
  value: unknown,
  state?: FieldState,
): string | null {
  const required = state?.required ?? !!def.required;
  if (isEmpty(value)) {
    return required ? `${def.label || def.name} is required` : null;
  }
  const t = def.data_type;
  switch (t) {
    case 'integer': {
      const n = toNumber(value);
      if (n === undefined || !Number.isInteger(n))
        return `${def.label || def.name} must be an integer`;
      break;
    }
    case 'decimal':
    case 'number':
      if (toNumber(value) === undefined) return `${def.label || def.name} must be a number`;
      break;
    case 'boolean':
      if (typeof value !== 'boolean') return `${def.label || def.name} must be a boolean`;
      break;
    case 'date':
      if (!/^\d{4}-\d{2}-\d{2}$/.test(String(value)))
        return `${def.label || def.name} must be YYYY-MM-DD`;
      break;
    case 'datetime':
      if (Number.isNaN(Date.parse(String(value))))
        return `${def.label || def.name} must be a datetime`;
      break;
    case 'ip': {
      const s = String(value);
      const v4 = /^(\d{1,3}\.){3}\d{1,3}$/.test(s);
      const v6 = /^[0-9a-fA-F:]+$/.test(s) && s.includes(':');
      if (!v4 && !v6) return `${def.label || def.name} must be an IP address`;
      break;
    }
    case 'mac':
      if (!/^([0-9a-fA-F]{2}[:-]){5}[0-9a-fA-F]{2}$/.test(String(value)))
        return `${def.label || def.name} must be a MAC address`;
      break;
    case 'url':
      try {
        const u = new URL(String(value));
        if (!u.protocol || !u.host) throw new Error();
      } catch {
        return `${def.label || def.name} must be a valid URL`;
      }
      break;
    case 'email':
      if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(String(value)))
        return `${def.label || def.name} must be a valid email`;
      break;
  }
  const v = def.validation;
  if (v) {
    const n = toNumber(value);
    if (v.min !== undefined && n !== undefined && n < v.min)
      return `${def.label || def.name} must be at least ${v.min}`;
    if (v.max !== undefined && n !== undefined && n > v.max)
      return `${def.label || def.name} must be at most ${v.max}`;
    if (typeof value === 'string') {
      if (v.min_length !== undefined && value.length < v.min_length)
        return `${def.label || def.name} must be at least ${v.min_length} characters`;
      if (v.max_length !== undefined && value.length > v.max_length)
        return `${def.label || def.name} must be at most ${v.max_length} characters`;
      if (v.pattern) {
        try {
          if (!new RegExp(v.pattern).test(value))
            return `${def.label || def.name} has an invalid format`;
        } catch {
          /* invalid pattern is a definition error, not a value error */
        }
      }
    }
  }
  const options = v?.options && v.options.length > 0 ? v.options : def.enum_values;
  if ((t === 'enum' || t === 'multi_enum') && options && options.length > 0) {
    if (t === 'multi_enum' && Array.isArray(value)) {
      const bad = value.find((item) => !options.includes(String(item)));
      if (bad !== undefined) return `${def.label || def.name}: ${String(bad)} is not allowed`;
    } else if (!options.includes(String(value))) {
      return `${def.label || def.name}: ${String(value)} is not an allowed value`;
    }
  }
  return null;
}
