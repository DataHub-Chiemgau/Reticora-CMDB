/**
 * ProfileEditor edits the profile of one level (org, client, group, ci) for
 * one probe type (PRB-01, WP-946, WP-1080): interval_s, timeout_s,
 * down_count, up_count, the thresholds and the type parameters of the
 * parameter schema of the register (api/probe-types.json).
 *
 * A field without a value of its own shows the inherited value as
 * placeholder with its source ("Standard", "Org", "Kunde", "Gruppe"),
 * computed from the type default and the profiles of the levels above.
 * "Zurücksetzen" removes the value from the profile at once (PATCH); a
 * profile without any value is deleted (DELETE). Limits and unit of the
 * type stand at the field. A timeout not below the interval is marked
 * before sending; the server's 422 stays decisive and appears at its
 * field. A 409 (the level has a profile already) loads the existing one.
 */
import { RotateCcw } from 'lucide-react';
import { useId, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import {
  ApiError,
  type MonitoringProfile,
  type MonitoringProfileLevel,
  type ProbeOverrides,
  type ProbeType,
} from '../../api/client';
import { useProfileMutations } from '../../api/monitoringSettingsHooks';
import { Button, IconButton } from '../ui/Button';
import { Input } from '../ui/Input';
import { Select } from '../ui/Select';

/** The level a value comes from: the type default or a profile above. */
export type ValueSource = 'default' | 'org' | 'client' | 'group' | 'ci';

/** The profile of a level above, widest first. */
export interface InheritedLayer {
  source: Exclude<ValueSource, 'default'>;
  params: ProbeOverrides;
}

interface ParamSchema {
  type?: string | string[];
  minimum?: number;
  maximum?: number;
  enum?: unknown[];
  default?: unknown;
  description?: string;
}

/** One editable field: a base field, a type parameter or a threshold. */
interface FieldSpec {
  /** interval_s, params.<name> or thresholds.<name> (the field of a 422). */
  key: string;
  name: string;
  group: 'base' | 'params' | 'thresholds';
  kind: 'integer' | 'number' | 'string' | 'enum' | 'boolean';
  min?: number;
  max?: number;
  unit?: string;
  options?: string[];
  def?: unknown;
  description?: string;
}

/** The counts of a probe (probewire MinCount, MaxCount). */
const COUNT_LIMITS = { min: 1, max: 20 };

/**
 * The parameters the executors read as thresholds (collector/probe state.go
 * thresholdOf for rtt_warn_ms, loss_warn and the optical levels, redfish
 * temp_warn/temp_crit) are written under thresholds. Every other type
 * parameter, even one named like a threshold (http cert_warn_days), is
 * read only from params and is written flat there.
 */
const THRESHOLD_NAMES: ReadonlySet<string> = new Set([
  'rtt_warn_ms',
  'loss_warn',
  'temp_warn',
  'temp_crit',
  'rx_warn_dbm',
  'rx_crit_dbm',
  'tx_warn_dbm',
  'tx_crit_dbm',
]);

function isThreshold(name: string): boolean {
  return THRESHOLD_NAMES.has(name);
}

/** fieldsOf lists the editable fields of a probe type in a stable order. */
export function fieldsOf(type: ProbeType): FieldSpec[] {
  const fields: FieldSpec[] = [
    {
      key: 'interval_s',
      name: 'interval_s',
      group: 'base',
      kind: 'integer',
      min: type.min_interval_s,
      max: type.max_interval_s,
      unit: 's',
      def: type.default_interval_s,
    },
    {
      key: 'timeout_s',
      name: 'timeout_s',
      group: 'base',
      kind: 'integer',
      min: type.min_timeout_s,
      max: type.max_timeout_s,
      unit: 's',
      def: type.default_timeout_s,
    },
    {
      key: 'down_count',
      name: 'down_count',
      group: 'base',
      kind: 'integer',
      ...COUNT_LIMITS,
      def: type.default_down_count,
    },
    {
      key: 'up_count',
      name: 'up_count',
      group: 'base',
      kind: 'integer',
      ...COUNT_LIMITS,
      def: type.default_up_count,
    },
  ];
  const properties = (type.params as { properties?: Record<string, ParamSchema> }).properties ?? {};
  for (const [name, schema] of Object.entries(properties)) {
    const t = Array.isArray(schema.type) ? schema.type.find((x) => x !== 'null') : schema.type;
    let kind: FieldSpec['kind'] | undefined;
    if (schema.enum) kind = 'enum';
    else if (t === 'integer') kind = 'integer';
    else if (t === 'number') kind = 'number';
    else if (t === 'string') kind = 'string';
    else if (t === 'boolean') kind = 'boolean';
    // Arrays and objects (lists of interfaces, headers) are set per probe (WP-1079).
    if (!kind) continue;
    const threshold = isThreshold(name);
    fields.push({
      key: `${threshold ? 'thresholds' : 'params'}.${name}`,
      name,
      group: threshold ? 'thresholds' : 'params',
      kind,
      min: schema.minimum,
      max: schema.maximum,
      unit: /_ms$/.test(name) ? 'ms' : /_s$/.test(name) ? 's' : undefined,
      options: schema.enum?.map(String),
      def: schema.default,
      description: schema.description,
    });
  }
  return fields;
}

/**
 * valueOf reads the value of a field from an override set. A threshold is
 * read from thresholds and else from the flat key, as the API takes both
 * (ProbeOverrides).
 */
function valueOf(params: ProbeOverrides | undefined, field: FieldSpec): unknown {
  if (!params) return undefined;
  const flat = (params as Record<string, unknown>)[field.name];
  if (field.group === 'thresholds') {
    const thresholds = params.thresholds as Record<string, unknown> | undefined;
    return thresholds?.[field.name] ?? flat;
  }
  return flat;
}

/** inheritedOf resolves the value a field takes without a value of its own. */
export function inheritedOf(
  field: FieldSpec,
  layers: readonly InheritedLayer[],
): { value: unknown; source: ValueSource } | undefined {
  let found: { value: unknown; source: ValueSource } | undefined =
    field.def === undefined ? undefined : { value: field.def, source: 'default' };
  for (const layer of layers) {
    const v = valueOf(layer.params, field);
    if (v !== undefined && v !== null) found = { value: v, source: layer.source };
  }
  return found;
}

function textOf(value: unknown): string {
  return value === undefined || value === null ? '' : String(value);
}

/** initialValues takes the values of the own profile as form strings. */
function initialValues(fields: FieldSpec[], profile?: MonitoringProfile): Record<string, string> {
  const values: Record<string, string> = {};
  for (const field of fields) values[field.key] = textOf(valueOf(profile?.params, field));
  return values;
}

/**
 * buildParams turns the form values into an override set. PATCH replaces
 * the params of a profile, so it starts from the stored set: keys the form
 * does not show (array and object parameters such as headers, interfaces)
 * stay; a form field overwrites or removes its key, a threshold both under
 * thresholds and flat. Empty values are left out.
 */
function buildParams(
  fields: FieldSpec[],
  values: Record<string, string>,
  stored?: ProbeOverrides,
): { params: ProbeOverrides; invalid: string[] } {
  const params: Record<string, unknown> = { ...(stored ?? {}) };
  const storedThresholds = stored?.thresholds;
  const thresholds: Record<string, unknown> =
    storedThresholds && typeof storedThresholds === 'object' && !Array.isArray(storedThresholds)
      ? { ...(storedThresholds as Record<string, unknown>) }
      : {};
  delete params.thresholds;
  const invalid: string[] = [];
  for (const field of fields) {
    delete params[field.name];
    if (field.group === 'thresholds') delete thresholds[field.name];
    const raw = (values[field.key] ?? '').trim();
    if (raw === '') continue;
    let value: unknown = raw;
    if (field.kind === 'integer' || field.kind === 'number') {
      const n = Number(raw);
      if (!Number.isFinite(n) || (field.kind === 'integer' && !Number.isInteger(n))) {
        invalid.push(field.key);
        continue;
      }
      value = n;
    } else if (field.kind === 'boolean') {
      value = raw === 'true';
    }
    if (field.group === 'thresholds') thresholds[field.name] = value;
    else params[field.name] = value;
  }
  if (Object.keys(thresholds).length > 0) params.thresholds = thresholds;
  return { params: params as ProbeOverrides, invalid };
}

export interface ProfileEditorProps {
  probeType: ProbeType;
  level: MonitoringProfileLevel;
  /** The client, group or CI of the level; absent at org. */
  levelId?: string;
  /** The profile of this level and type, if there is one. */
  profile?: MonitoringProfile;
  /** The profiles of the levels above, widest first. */
  inherited: readonly InheritedLayer[];
  readOnly: boolean;
  /** Called after a write, a delete (undefined) or a 409 (reload). */
  onChanged?: (profile?: MonitoringProfile) => void;
}

export function ProfileEditor({
  probeType,
  level,
  levelId,
  profile,
  inherited,
  readOnly,
  onChanged,
}: ProfileEditorProps) {
  const { t } = useTranslation();
  const idBase = useId();
  const fields = fieldsOf(probeType);
  const [values, setValues] = useState<Record<string, string>>(() =>
    initialValues(fields, profile),
  );
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<string>();
  const [status, setStatus] = useState<string>();
  const { create, update, remove } = useProfileMutations();
  const busy = create.isPending || update.isPending || remove.isPending;

  const sourceLabel = (source: ValueSource) => t(`monitoring.settings.sources.${source}`);
  const fieldLabel = (field: FieldSpec) =>
    field.group === 'base' ? t(`monitoring.settings.fields.${field.name}`) : field.name;

  const refused = (err: unknown) => {
    if (err instanceof ApiError && err.status === 409) {
      setStatus(t('monitoring.settings.profile.conflict'));
      onChanged?.();
      return;
    }
    if (err instanceof ApiError && err.status === 422 && err.field) {
      setErrors({ [err.field]: err.detail || t('monitoring.settings.profile.invalid') });
      return;
    }
    setFailure(
      err instanceof ApiError && err.status === 403
        ? t('monitoring.settings.profile.forbidden')
        : t('monitoring.settings.profile.saveFailed'),
    );
  };

  /** write stores the values: create, patch, or delete a profile without values. */
  const write = (next: Record<string, string>) => {
    setErrors({});
    setFailure(undefined);
    setStatus(undefined);
    const { params, invalid } = buildParams(fields, next, profile?.params);
    if (invalid.length > 0) {
      setErrors(
        Object.fromEntries(invalid.map((k) => [k, t('monitoring.settings.profile.notANumber')])),
      );
      return;
    }
    // The timeout must stay below the interval (own or inherited value).
    const effective = (key: 'interval_s' | 'timeout_s') => {
      const own = (params as Record<string, unknown>)[key];
      if (typeof own === 'number') return own;
      const field = fields.find((f) => f.key === key);
      const v = field ? inheritedOf(field, inherited)?.value : undefined;
      return typeof v === 'number' ? v : Number(v);
    };
    const interval = effective('interval_s');
    const timeout = effective('timeout_s');
    if (
      ('timeout_s' in params || 'interval_s' in params) &&
      Number.isFinite(interval) &&
      Number.isFinite(timeout) &&
      timeout >= interval
    ) {
      setErrors({ timeout_s: t('monitoring.settings.profile.timeoutBelowInterval') });
      return;
    }
    const empty = Object.keys(params).length === 0;
    if (!profile) {
      if (empty) return;
      create.mutate(
        {
          level,
          level_id: level === 'org' ? null : (levelId ?? null),
          probe_type: probeType.key,
          params,
        },
        {
          onSuccess: (created) => {
            setStatus(t('monitoring.settings.profile.saved'));
            onChanged?.(created);
          },
          onError: refused,
        },
      );
      return;
    }
    if (empty) {
      remove.mutate(profile.id, {
        onSuccess: () => {
          setStatus(t('monitoring.settings.profile.deleted'));
          onChanged?.(undefined);
        },
        onError: refused,
      });
      return;
    }
    update.mutate(
      { id: profile.id, params },
      {
        onSuccess: (saved) => {
          setStatus(t('monitoring.settings.profile.saved'));
          onChanged?.(saved);
        },
        onError: refused,
      },
    );
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    write(values);
  };

  const reset = (key: string) => {
    const next = { ...values, [key]: '' };
    setValues(next);
    if (profile) write(next);
  };

  const groups: FieldSpec['group'][] = ['base', 'thresholds', 'params'];
  return (
    <form
      onSubmit={submit}
      noValidate
      className="space-y-4"
      aria-label={t('monitoring.settings.profile.formLabel', { type: probeType.key })}
    >
      {groups.map((group) => {
        const list = fields.filter((f) => f.group === group);
        if (list.length === 0) return null;
        return (
          <fieldset key={group} className="space-y-3">
            <legend className="text-sm font-medium text-fg">
              {t(`monitoring.settings.groups.${group}`)}
            </legend>
            <div className="grid gap-3 sm:grid-cols-2">
              {list.map((field) => {
                const inheritedValue = inheritedOf(field, inherited);
                const inheritedText = inheritedValue
                  ? t('monitoring.settings.profile.inherited', {
                      value: `${textOf(inheritedValue.value)}${field.unit ? ` ${field.unit}` : ''}`,
                      source: sourceLabel(inheritedValue.source),
                    })
                  : t('monitoring.settings.profile.noInherited');
                const limits =
                  field.min !== undefined || field.max !== undefined
                    ? t('monitoring.settings.profile.limits', {
                        min: field.min ?? '–',
                        max: field.max ?? '–',
                        unit: field.unit ?? '',
                      }).trim()
                    : undefined;
                const hint = [inheritedText, limits, field.description].filter(Boolean).join(' · ');
                const label = `${fieldLabel(field)}${field.unit ? ` (${field.unit})` : ''}`;
                const own = (values[field.key] ?? '') !== '';
                const control =
                  field.kind === 'enum' || field.kind === 'boolean' ? (
                    <Select
                      label={label}
                      value={values[field.key] ?? ''}
                      disabled={readOnly}
                      error={errors[field.key]}
                      onChange={(event) =>
                        setValues((v) => ({ ...v, [field.key]: event.target.value }))
                      }
                      options={[
                        {
                          value: '',
                          label: t('monitoring.settings.profile.inheritOption', {
                            value: inheritedValue ? textOf(inheritedValue.value) : '–',
                          }),
                        },
                        ...(field.kind === 'boolean'
                          ? ['true', 'false']
                          : (field.options ?? [])
                        ).map((o) => ({ value: o, label: o })),
                      ]}
                    />
                  ) : (
                    <Input
                      id={`${idBase}-${field.key}`}
                      label={label}
                      inputMode={field.kind === 'string' ? undefined : 'decimal'}
                      value={values[field.key] ?? ''}
                      placeholder={inheritedValue ? textOf(inheritedValue.value) : undefined}
                      disabled={readOnly}
                      error={errors[field.key]}
                      onChange={(event) =>
                        setValues((v) => ({ ...v, [field.key]: event.target.value }))
                      }
                    />
                  );
                return (
                  <div key={field.key} className="flex items-start gap-1">
                    <div className="min-w-0 flex-1 space-y-1">
                      {control}
                      <p className="text-xs text-fg-muted" data-testid={`hint-${field.key}`}>
                        {hint}
                      </p>
                    </div>
                    {!readOnly && own && profile ? (
                      <IconButton
                        size="sm"
                        className="mt-6"
                        icon={<RotateCcw />}
                        label={t('monitoring.settings.profile.reset', { field: fieldLabel(field) })}
                        disabled={busy}
                        onClick={() => reset(field.key)}
                      />
                    ) : null}
                  </div>
                );
              })}
            </div>
          </fieldset>
        );
      })}
      {errors.params ? <p className="text-sm text-danger">{errors.params}</p> : null}
      {failure ? (
        <p role="alert" className="text-sm text-danger">
          {failure}
        </p>
      ) : null}
      {status ? (
        <p role="status" className="text-sm text-fg-muted">
          {status}
        </p>
      ) : null}
      {!readOnly ? (
        <div className="flex justify-end">
          <Button type="submit" loading={busy}>
            {t('monitoring.settings.profile.save')}
          </Button>
        </div>
      ) : null}
    </form>
  );
}
