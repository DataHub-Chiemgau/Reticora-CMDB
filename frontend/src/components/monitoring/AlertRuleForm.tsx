import { Plus, Trash2 } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { ApiError } from '../../api/client';
import type {
  AlertRule,
  AlertRuleCondition,
  AlertRuleCreateRequest,
  AlertRuleScope,
} from '../../api/client';
import { useCreateAlertRule, useSites, useUpdateAlertRule } from '../../api/hooks';
import { useCITypes, useClients } from '../../api/cmdbHooks';
import { localizedText } from '../../api/cmdb';
import { CONDITION_MODE, type ConditionCheck, type FieldSpec } from '../../rules/builderModes';
import { RuleBuilder } from '../rules/RuleBuilder';
import { ObjectPicker, type PickedObject } from '../pickers/ObjectPicker';
import { Button } from '../ui/Button';
import { Input } from '../ui/Input';
import { Modal } from '../ui/Modal';
import { Select } from '../ui/Select';

/**
 * AlertRuleForm creates and edits an alert rule (MON-04, ALR-05, WP-990):
 * name, metric, a MET-45 condition per stage (critical, warning, info) in
 * the click builder over the metric context of a series (value, avg, max,
 * min, rate, delta), the optional clear condition (hysteresis), window,
 * minimum duration and enabled; the scope (CIs, CI types including their
 * subtypes, client, site of the client, label pairs, group_by) and the
 * actions notification and webhook. At least one stage is required. A 422
 * of the server marks its field or the builder node of its path
 * (warning_condition.rules[1].field). The dialog closes with Esc and gives
 * the focus back to the control that opened it (Modal).
 */

/** The stages of a rule, highest first. */
export const STAGES = ['critical', 'warning', 'info'] as const;
export type Stage = (typeof STAGES)[number];

/** The fields of the metric context of a series (api/openapi.yaml AlertRuleCondition). */
export const METRIC_FIELDS = ['value', 'avg', 'max', 'min', 'rate', 'delta'] as const;

/** At most this many group_by labels (the API's limit). */
export const MAX_GROUP_BY = 5;

const metricFieldSpecs: FieldSpec[] = METRIC_FIELDS.map((field) => ({
  field,
  source: 'column',
  kind: 'number',
  editor: 'number',
  column: field,
}));

type ConditionField = `${Stage}_condition` | 'clear_condition';

/** The builder keys: the stages and the clear condition. */
const BUILDERS: readonly ConditionField[] = [
  'critical_condition',
  'warning_condition',
  'info_condition',
  'clear_condition',
];

interface LabelPair {
  key: string;
  value: string;
}

/**
 * The form fields a 422 path is shown at, by path prefix: "group_by[1]"
 * marks group_by, "scope.ci_ids[0]" the CI picker, "scope.labels.env" the
 * label pairs, "actions.webhook.enabled" the actions.
 */
const FIELD_KEYS = [
  'name',
  'metric_name',
  'eval_window',
  'duration',
  'enabled',
  'scope.ci_ids',
  'scope.ci_type_ids',
  'scope.client_id',
  'scope.site_id',
  'scope.labels',
  'group_by',
  'actions',
] as const;
type FieldKey = (typeof FIELD_KEYS)[number];

/** fieldKeyOf returns the form field of a 422 path; undefined for none. */
export function fieldKeyOf(path: string): FieldKey | undefined {
  return FIELD_KEYS.find(
    (key) => path === key || path.startsWith(`${key}.`) || path.startsWith(`${key}[`),
  );
}

/** The refusal of the server, by field; the condition paths by builder. */
interface Refusal {
  fields: Partial<Record<FieldKey, string>>;
  conditions: Partial<Record<ConditionField, { path: string; message: string }>>;
  /** unplaced: the messages of paths no field of the form shows. */
  unplaced: string[];
  note?: string;
}

/** refusalOf maps a 422 onto the form: condition paths to their builder, the rest to the fields. */
export function refusalOf(error: unknown, t: TFunction): Refusal | undefined {
  if (!error) return undefined;
  if (!(error instanceof ApiError)) {
    return {
      fields: {},
      conditions: {},
      unplaced: [],
      note: error instanceof Error ? error.message : String(error),
    };
  }
  if (error.status !== 422) {
    return {
      fields: {},
      conditions: {},
      unplaced: [],
      note: error.detail || error.message || t('monitoring.rule.saveFailed'),
    };
  }
  const detail = error.detail || error.message;
  const entries: [string, string][] = error.violations.map((v) => [v.field, v.detail || detail]);
  if (error.field && !entries.some(([f]) => f === error.field)) entries.push([error.field, detail]);
  const out: Refusal = { fields: {}, conditions: {}, unplaced: [] };
  for (const [field, message] of entries) {
    const builder = BUILDERS.find((b) => field === b || field.startsWith(`${b}.`));
    const key = builder ? undefined : fieldKeyOf(field);
    if (builder) {
      // The builder names its root "condition" (conditionPaths).
      out.conditions[builder] ??= { path: `condition${field.slice(builder.length)}`, message };
    } else if (key) {
      out.fields[key] ??= message;
    } else {
      // No field shows it: the alert line below the form does.
      out.unplaced.push(field ? `${field}: ${message}` : message);
    }
  }
  if (entries.length === 0) out.note = detail;
  return out;
}

function stageCondition(rule: AlertRule | undefined, field: ConditionField): AlertRuleCondition {
  if (!rule) return null;
  return (rule[field] as AlertRuleCondition | undefined) ?? null;
}

export interface AlertRuleFormProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** rule is the rule to edit; without it the form creates one. */
  rule?: AlertRule;
  onSaved?: (rule: AlertRule) => void;
}

export function AlertRuleForm({ open, onOpenChange, rule, onSaved }: AlertRuleFormProps) {
  const { t } = useTranslation();
  return (
    <Modal
      open={open}
      onOpenChange={onOpenChange}
      title={rule ? t('monitoring.rule.editTitle') : t('monitoring.rule.createTitle')}
      description={t('monitoring.rule.description')}
      size="xl"
    >
      {open ? (
        <AlertRuleFormBody
          key={rule?.id ?? 'new'}
          rule={rule}
          onDone={(saved) => {
            onSaved?.(saved);
            onOpenChange(false);
          }}
          onCancel={() => onOpenChange(false)}
        />
      ) : null}
    </Modal>
  );
}

function AlertRuleFormBody({
  rule,
  onDone,
  onCancel,
}: {
  rule?: AlertRule;
  onDone: (rule: AlertRule) => void;
  onCancel: () => void;
}) {
  const { t, i18n } = useTranslation();
  const create = useCreateAlertRule();
  const update = useUpdateAlertRule();
  const ciTypes = useCITypes({ limit: 200 });
  const clients = useClients();

  const [name, setName] = useState(rule?.name ?? '');
  const [metric, setMetric] = useState(rule?.metric_name ?? '');
  const [evalWindow, setEvalWindow] = useState(rule?.eval_window ?? '5m');
  const [duration, setDuration] = useState(rule?.duration ?? '0s');
  const [enabled, setEnabled] = useState(rule?.enabled ?? true);
  const [checks, setChecks] = useState<Partial<Record<ConditionField, ConditionCheck>>>(() => {
    const out: Partial<Record<ConditionField, ConditionCheck>> = {};
    for (const b of BUILDERS) out[b] = { ok: true, condition: stageCondition(rule, b) };
    return out;
  });
  const [cis, setCis] = useState<PickedObject[]>(
    (rule?.scope?.ci_ids ?? []).map((id) => ({ kind: 'ci', id, name: id })),
  );
  const [ciTypeIds, setCiTypeIds] = useState<string[]>(rule?.scope?.ci_type_ids ?? []);
  const [clientId, setClientId] = useState(rule?.scope?.client_id ?? '');
  const [siteId, setSiteId] = useState(rule?.scope?.site_id ?? '');
  const sites = useSites(clientId || undefined);
  const [labels, setLabels] = useState<LabelPair[]>(
    Object.entries(rule?.scope?.labels ?? {}).map(([key, value]) => ({ key, value })),
  );
  const [groupBy, setGroupBy] = useState((rule?.group_by ?? []).join(', '));
  const [notify, setNotify] = useState(rule?.actions?.notify?.enabled ?? true);
  const [webhook, setWebhook] = useState(rule?.actions?.webhook?.enabled ?? true);
  const [showIssues, setShowIssues] = useState(false);

  const mutation = rule ? update : create;
  const refusal = useMemo(() => refusalOf(mutation.error, t), [mutation.error, t]);

  const groupByList = groupBy
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean);
  const stagePresent = STAGES.some((s) => {
    const c = checks[`${s}_condition`];
    return c?.ok && c.condition != null;
  });
  const builderBroken = BUILDERS.some((b) => checks[b] && !checks[b]!.ok);
  const groupByTooLong = groupByList.length > MAX_GROUP_BY;
  const groupByDuplicate = groupByList.find((key, i) => groupByList.indexOf(key) !== i);
  const canSave =
    name.trim() !== '' &&
    metric.trim() !== '' &&
    stagePresent &&
    !groupByTooLong &&
    !groupByDuplicate &&
    !mutation.isPending;

  const setCheck = (field: ConditionField) => (check: ConditionCheck) =>
    setChecks((prev) => ({ ...prev, [field]: check }));

  const submit = () => {
    setShowIssues(true);
    if (!canSave || builderBroken) return;
    const condition = (field: ConditionField): AlertRuleCondition => {
      const c = checks[field];
      return c?.ok ? c.condition : null;
    };
    const scope: AlertRuleScope = {};
    if (cis.length) scope.ci_ids = cis.map((c) => c.id);
    if (ciTypeIds.length) scope.ci_type_ids = ciTypeIds;
    if (clientId) scope.client_id = clientId;
    // A site may stand without a client (the API allows it): editing such a
    // rule keeps it.
    if (siteId) scope.site_id = siteId;
    const labelMap = Object.fromEntries(
      labels.filter((l) => l.key.trim() !== '').map((l) => [l.key.trim(), l.value]),
    );
    if (Object.keys(labelMap).length) scope.labels = labelMap;
    const body: AlertRuleCreateRequest = {
      name: name.trim(),
      metric_name: metric.trim(),
      critical_condition: condition('critical_condition'),
      warning_condition: condition('warning_condition'),
      info_condition: condition('info_condition'),
      clear_condition: condition('clear_condition'),
      eval_window: evalWindow.trim(),
      duration: duration.trim(),
      enabled,
      scope,
      group_by: groupByList,
      actions: { notify: { enabled: notify }, webhook: { enabled: webhook } },
    };
    if (rule) {
      // The metric of a rule is fixed: the PATCH carries everything else.
      const patch: Partial<AlertRuleCreateRequest> = { ...body };
      delete patch.metric_name;
      update.mutate({ id: rule.id, data: patch }, { onSuccess: onDone });
    } else {
      create.mutate(body, { onSuccess: onDone });
    }
  };

  const fieldError = (field: FieldKey) => refusal?.fields[field];
  const fieldMessage = (field: FieldKey) =>
    fieldError(field) ? (
      <p className="text-xs text-danger" role="alert">
        {fieldError(field)}
      </p>
    ) : null;
  // The alert line: a refusal without fields, the first builder message and
  // every message no field shows, so that no 422 stays silent.
  const alertMessages = [
    ...(refusal?.note ? [refusal.note] : []),
    ...(!refusal?.note && Object.keys(refusal?.conditions ?? {}).length
      ? [Object.values(refusal?.conditions ?? {})[0]!.message]
      : []),
    ...(refusal?.unplaced.length
      ? [t('monitoring.rule.fieldErrors', { errors: refusal.unplaced.join('; ') })]
      : []),
  ];
  const stageLabel = (field: ConditionField) =>
    field === 'clear_condition'
      ? t('monitoring.rule.clearCondition')
      : t(`monitoring.rule.stage_${field.replace('_condition', '')}`);

  return (
    <form
      className="space-y-5"
      onSubmit={(event) => {
        event.preventDefault();
        submit();
      }}
      noValidate
    >
      <div className="grid gap-4 md:grid-cols-2">
        <Input
          label={t('monitoring.ruleName')}
          value={name}
          onChange={(e) => setName(e.target.value)}
          error={fieldError('name')}
          required
        />
        <Input
          label={t('monitoring.metricName')}
          value={metric}
          onChange={(e) => setMetric(e.target.value)}
          error={fieldError('metric_name')}
          disabled={!!rule}
          required
        />
        <Input
          label={t('monitoring.rule.evalWindow')}
          value={evalWindow}
          onChange={(e) => setEvalWindow(e.target.value)}
          error={fieldError('eval_window')}
        />
        <Input
          label={t('monitoring.rule.duration')}
          value={duration}
          onChange={(e) => setDuration(e.target.value)}
          error={fieldError('duration')}
        />
      </div>
      <label className="flex items-center gap-2 text-sm text-fg">
        <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
        {t('monitoring.rule.enabled')}
      </label>
      {fieldMessage('enabled')}

      <fieldset className="space-y-4">
        <legend className="text-sm font-semibold text-fg">{t('monitoring.rule.stages')}</legend>
        <p className="text-sm text-fg-muted">{t('monitoring.rule.stagesHint')}</p>
        {BUILDERS.map((field) => (
          <div key={field} className="space-y-1" data-testid={`builder-${field}`}>
            <h3 className="text-sm font-medium text-fg">{stageLabel(field)}</h3>
            <RuleBuilder
              mode={CONDITION_MODE}
              initialValue={stageCondition(rule, field)}
              fields={metricFieldSpecs}
              onChange={setCheck(field)}
              serverError={refusal?.conditions[field]}
              showIssues={showIssues}
              label={stageLabel(field)}
            />
          </div>
        ))}
        {!stagePresent ? (
          <p className="text-sm text-warning" role="status">
            {t('monitoring.rule.stageRequired')}
          </p>
        ) : null}
      </fieldset>

      <fieldset className="space-y-4">
        <legend className="text-sm font-semibold text-fg">{t('monitoring.rule.scope')}</legend>
        <ObjectPicker
          label={t('monitoring.rule.scopeCIs')}
          kinds={['ci']}
          value={cis}
          onChange={setCis}
          multiple
        />
        {fieldMessage('scope.ci_ids')}
        <fieldset className="space-y-1">
          <legend className="text-sm text-fg">{t('monitoring.rule.scopeCITypes')}</legend>
          <p className="text-xs text-fg-muted">{t('monitoring.rule.scopeCITypesHint')}</p>
          <div className="grid max-h-40 gap-1 overflow-auto rounded-lg border border-control p-2 sm:grid-cols-2">
            {(ciTypes.data?.data ?? []).map((ct) => (
              <label key={ct.id} className="flex items-center gap-2 text-sm text-fg">
                <input
                  type="checkbox"
                  checked={ciTypeIds.includes(ct.id)}
                  onChange={(e) =>
                    setCiTypeIds((prev) =>
                      e.target.checked ? [...prev, ct.id] : prev.filter((id) => id !== ct.id),
                    )
                  }
                />
                {localizedText(ct.display_name, i18n.language) || ct.name}
              </label>
            ))}
          </div>
          {fieldMessage('scope.ci_type_ids')}
        </fieldset>
        <div className="grid gap-4 md:grid-cols-2">
          <Select
            label={t('monitoring.rule.scopeClient')}
            value={clientId}
            options={[
              { value: '', label: t('monitoring.rule.anyClient') },
              ...(clients.data?.data ?? []).map((c) => ({ value: c.id, label: c.name })),
            ]}
            onChange={(e) => {
              setClientId(e.target.value);
              setSiteId('');
            }}
            error={fieldError('scope.client_id')}
          />
          <Select
            label={t('monitoring.rule.scopeSite')}
            value={siteId}
            disabled={!clientId && !siteId}
            options={[
              { value: '', label: t('monitoring.rule.anySite') },
              ...(clientId || siteId ? (sites.data?.data ?? []) : []).map((s) => ({
                value: s.id,
                label: s.name,
              })),
              // The stored site while the list does not (yet) hold it.
              ...(siteId && !(sites.data?.data ?? []).some((s) => s.id === siteId)
                ? [{ value: siteId, label: siteId }]
                : []),
            ]}
            onChange={(e) => setSiteId(e.target.value)}
            error={fieldError('scope.site_id')}
          />
        </div>
        <fieldset className="space-y-2">
          <legend className="text-sm text-fg">{t('monitoring.rule.labels')}</legend>
          {labels.map((pair, i) => (
            <div key={i} className="flex items-end gap-2">
              <Input
                label={t('monitoring.rule.labelKey')}
                value={pair.key}
                onChange={(e) =>
                  setLabels((prev) =>
                    prev.map((p, j) => (j === i ? { ...p, key: e.target.value } : p)),
                  )
                }
              />
              <Input
                label={t('monitoring.rule.labelValue')}
                value={pair.value}
                onChange={(e) =>
                  setLabels((prev) =>
                    prev.map((p, j) => (j === i ? { ...p, value: e.target.value } : p)),
                  )
                }
              />
              <Button
                type="button"
                variant="ghost"
                icon={<Trash2 />}
                onClick={() => setLabels((prev) => prev.filter((_, j) => j !== i))}
              >
                {t('monitoring.rule.removeLabel')}
              </Button>
            </div>
          ))}
          <Button
            type="button"
            variant="secondary"
            size="sm"
            icon={<Plus />}
            onClick={() => setLabels((prev) => [...prev, { key: '', value: '' }])}
          >
            {t('monitoring.rule.addLabel')}
          </Button>
          {fieldMessage('scope.labels')}
        </fieldset>
        <Input
          label={t('monitoring.rule.groupBy')}
          value={groupBy}
          onChange={(e) => setGroupBy(e.target.value)}
          error={
            groupByTooLong
              ? t('monitoring.rule.groupByTooLong', { max: MAX_GROUP_BY })
              : groupByDuplicate
                ? t('monitoring.rule.groupByDuplicate', { key: groupByDuplicate })
                : fieldError('group_by')
          }
        />
      </fieldset>

      <fieldset className="space-y-2">
        <legend className="text-sm font-semibold text-fg">{t('monitoring.rule.actions')}</legend>
        <label className="flex items-center gap-2 text-sm text-fg">
          <input type="checkbox" checked={notify} onChange={(e) => setNotify(e.target.checked)} />
          {t('monitoring.rule.actionNotify')}
        </label>
        <label className="flex items-center gap-2 text-sm text-fg">
          <input type="checkbox" checked={webhook} onChange={(e) => setWebhook(e.target.checked)} />
          {t('monitoring.rule.actionWebhook')}
        </label>
        {fieldMessage('actions')}
      </fieldset>

      {alertMessages.length ? (
        <p className="text-sm text-danger" role="alert">
          {alertMessages.join(' ')}
        </p>
      ) : null}

      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" onClick={onCancel}>
          {t('common.cancel')}
        </Button>
        <Button type="submit" loading={mutation.isPending} disabled={!canSave}>
          {t('common.save')}
        </Button>
      </div>
    </form>
  );
}
