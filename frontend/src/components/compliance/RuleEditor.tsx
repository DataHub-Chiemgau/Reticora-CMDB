import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  ApiError,
  COMPLIANCE_STANDARDS,
  type ComplianceRule,
  type ComplianceStandard,
} from '../../api/client';
import { useComplianceControls, useComplianceRule, useSaveComplianceRule } from '../../api/hooks';
import {
  useApplicableAttributeDefinitions,
  useCITypes,
  useSearchableAttributeDefinitions,
} from '../../api/cmdbHooks';
import { localizedText, type AttributeDefinition } from '../../api/cmdb';
import {
  CONDITION_MODE,
  conditionFieldCatalog,
  type ConditionCheck,
  type FieldSpec,
} from '../../rules/builderModes';
import type { Condition } from '../../rules/rules';
import { RuleBuilder } from '../rules/RuleBuilder';
import { Button } from '../ui/Button';
import { Input } from '../ui/Input';
import { Modal } from '../ui/Modal';
import { Select } from '../ui/Select';

/** The severities of a compliance rule. */
export const RULE_SEVERITIES = ['low', 'medium', 'high', 'critical'] as const;

/** The finding kinds of finding.open_count.<kind> (WP-1125, without compliance_violation). */
export const FINDING_KINDS = [
  'vulnerability',
  'outdated_software',
  'outdated_firmware',
  'missing_patch',
  'unauthorized_change',
] as const;

const TEXT_COLUMNS = [
  'name',
  'status',
  'manufacturer',
  'model',
  'serial_number',
  'management_ip',
  'firmware_version',
  'os_name',
  'os_version',
  'ci_type_id',
  'client_id',
  'site_id',
  'lifecycle_state',
  'finding.max_open_severity',
] as const;

/**
 * complianceFieldSpecs are the fixed inputs of a CI a rule reads (WP-1125):
 * the typed columns, the lifecycle state and the facts of its open
 * findings. The attributes follow in complianceFieldCatalog.
 */
export const complianceFieldSpecs: FieldSpec[] = [
  ...TEXT_COLUMNS.map((field): FieldSpec => ({
    field,
    source: 'column',
    kind: 'text',
    editor: 'text',
    column: field,
  })),
  ...['finding.max_open_severity_rank', ...FINDING_KINDS.map((k) => `finding.open_count.${k}`)].map(
    (field): FieldSpec => ({
      field,
      source: 'column',
      kind: 'number',
      editor: 'number',
      column: field,
    }),
  ),
];

/**
 * complianceFieldCatalog is complianceFieldSpecs with the attributes of the
 * definitions as attributes.<key> (the name the rule engine reads,
 * WP-1125), each with the editor of its data type.
 */
export function complianceFieldCatalog(
  definitions: readonly AttributeDefinition[] = [],
): FieldSpec[] {
  const attributes = conditionFieldCatalog(definitions)
    .filter((f) => f.source === 'attribute')
    .map((f): FieldSpec => ({ ...f, field: `attributes.${f.field}` }));
  return [...complianceFieldSpecs, ...attributes];
}

/** The builders of the editor and the request field each one fills. */
const BUILDERS = ['expression', 'applies_when'] as const;
type BuilderField = (typeof BUILDERS)[number];

/** builderError maps a 422 path onto the builder node it names (condition.rules[1].field). */
export function builderError(
  error: unknown,
): Partial<Record<BuilderField, { path: string; message: string }>> & { note?: string } {
  if (!(error instanceof ApiError)) {
    return error ? { note: error instanceof Error ? error.message : String(error) } : {};
  }
  const out: Partial<Record<BuilderField, { path: string; message: string }>> & {
    note?: string;
  } = {};
  const detail = error.detail || error.message;
  const entries: [string, string][] = error.violations.map((v) => [v.field, v.detail || detail]);
  if (error.field && !entries.some(([f]) => f === error.field)) entries.push([error.field, detail]);
  for (const [field, message] of entries) {
    const builder = BUILDERS.find((b) => field === b || field.startsWith(`${b}.`));
    if (builder) {
      out[builder] ??= { path: `condition${field.slice(builder.length)}`, message };
    } else {
      out.note = field ? `${field}: ${message}` : message;
    }
  }
  if (entries.length === 0) out.note = detail;
  return out;
}

export interface RuleEditorProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** rule is the rule to edit; without it the editor creates one. */
  rule?: ComplianceRule;
  /** readOnly shows the rule without saving (no compliance:write). */
  readOnly?: boolean;
  onSaved?: (rule: ComplianceRule) => void;
}

/**
 * RuleEditor creates and edits a compliance rule (SEC-03, SEC-04, WP-1135
 * AK1): name, category, severity, CI type, applies_when and the condition
 * in the click builder over the inputs of a CI, the active switch and the
 * controls of each standard. A 422 with an AST path marks its node.
 */
export function RuleEditor({ open, onOpenChange, rule, readOnly, onSaved }: RuleEditorProps) {
  const { t } = useTranslation();
  return (
    <Modal
      open={open}
      onOpenChange={onOpenChange}
      title={
        readOnly
          ? t('compliance.editor.viewTitle')
          : rule
            ? t('compliance.editor.editTitle')
            : t('compliance.editor.createTitle')
      }
      description={t('compliance.editor.description')}
      size="xl"
    >
      {open ? (
        <RuleEditorBody
          key={rule?.id ?? 'new'}
          rule={rule}
          readOnly={readOnly}
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

function RuleEditorBody({
  rule,
  readOnly,
  onDone,
  onCancel,
}: {
  rule?: ComplianceRule;
  readOnly?: boolean;
  onDone: (rule: ComplianceRule) => void;
  onCancel: () => void;
}) {
  const { t, i18n } = useTranslation();
  const detail = useComplianceRule(rule?.id);
  const save = useSaveComplianceRule();
  const ciTypes = useCITypes({ limit: 200 });
  const [name, setName] = useState(rule?.name ?? '');
  const [category, setCategory] = useState(rule?.category ?? 'security');
  const [severity, setSeverity] = useState(rule?.severity ?? 'medium');
  const [ciTypeId, setCITypeId] = useState(rule?.ci_type_id ?? '');
  const [active, setActive] = useState(rule?.active ?? true);
  const [checks, setChecks] = useState<Partial<Record<BuilderField, ConditionCheck>>>({});
  // The attributes of the chosen CI type (and the global ones), else every
  // searchable attribute.
  const typeDefinitions = useApplicableAttributeDefinitions(ciTypeId || undefined);
  const allDefinitions = useSearchableAttributeDefinitions(!readOnly && !ciTypeId);
  const definitions = ciTypeId ? typeDefinitions.data?.data : allDefinitions.data;
  const fields = useMemo(() => complianceFieldCatalog(definitions), [definitions]);
  const [controlIds, setControlIds] = useState<string[] | undefined>(undefined);
  const [showIssues, setShowIssues] = useState(false);
  const selected = controlIds ?? detail.data?.control_ids ?? [];
  const refusal = useMemo(() => builderError(save.error), [save.error]);

  const submit = () => {
    setShowIssues(true);
    const expression = checks.expression;
    const applies = checks.applies_when;
    if (!name.trim() || !expression?.ok || !expression.condition || (applies && !applies.ok))
      return;
    save.mutate(
      {
        id: rule?.id,
        rule: {
          name: name.trim(),
          category,
          severity: severity as (typeof RULE_SEVERITIES)[number],
          // An edit without type clears it ('' is NULL on the server).
          ci_type_id: ciTypeId || (rule ? '' : undefined),
          expression: expression.condition as unknown as Record<string, unknown>,
          applies_when:
            applies?.ok && applies.condition
              ? (applies.condition as unknown as Record<string, unknown>)
              : rule
                ? null
                : undefined,
          active,
        },
        controlIds: selected,
      },
      { onSuccess: onDone },
    );
  };

  const toggle = (id: string) =>
    setControlIds((prev) => {
      const current = prev ?? detail.data?.control_ids ?? [];
      return current.includes(id) ? current.filter((c) => c !== id) : [...current, id];
    });

  return (
    <form
      className="space-y-4"
      onSubmit={(e) => {
        e.preventDefault();
        if (!readOnly) submit();
      }}
    >
      <fieldset disabled={readOnly} className="grid gap-3 sm:grid-cols-2">
        <Input
          label={t('common.name')}
          value={name}
          onChange={(e) => setName(e.target.value)}
          required
          error={showIssues && !name.trim() ? t('compliance.editor.nameRequired') : undefined}
        />
        <Input
          label={t('ticket.category')}
          value={category}
          onChange={(e) => setCategory(e.target.value)}
        />
        <Select
          label={t('compliance.severity')}
          value={severity}
          onChange={(e) => setSeverity(e.target.value)}
          options={RULE_SEVERITIES.map((s) => ({
            value: s,
            label: t(`compliance.severities.${s}`),
          }))}
        />
        <Select
          label={t('compliance.editor.ciType')}
          value={ciTypeId}
          onChange={(e) => setCITypeId(e.target.value)}
          options={[
            { value: '', label: t('compliance.editor.everyType') },
            ...(ciTypes.data?.data ?? []).map((ct) => ({
              value: ct.id,
              label: localizedText(ct.display_name, i18n.language) || ct.name,
            })),
          ]}
        />
        <label className="flex items-center gap-2 text-sm text-fg">
          <input type="checkbox" checked={active} onChange={(e) => setActive(e.target.checked)} />
          {t('compliance.editor.active')}
        </label>
      </fieldset>

      {(['applies_when', 'expression'] as const).map((field) => (
        <section key={field} className="space-y-2" data-testid={`rule-${field}`}>
          <h3 className="text-sm font-semibold text-fg">{t(`compliance.editor.${field}`)}</h3>
          <p className="text-xs text-fg-muted">{t(`compliance.editor.${field}Hint`)}</p>
          {readOnly ? (
            <pre className="overflow-x-auto rounded bg-surface-2 p-2 text-xs">
              {conditionText(rule, field)}
            </pre>
          ) : (
            <RuleBuilder
              mode={CONDITION_MODE}
              label={t(`compliance.editor.${field}`)}
              initialValue={(rule?.[field] as Condition | null | undefined) ?? null}
              fields={fields}
              onChange={(check: ConditionCheck) =>
                setChecks((prev) => ({ ...prev, [field]: check }))
              }
              serverError={refusal[field]}
              showIssues={showIssues}
            />
          )}
          {showIssues &&
          field === 'expression' &&
          checks.expression?.ok &&
          !checks.expression.condition ? (
            <p className="text-sm text-danger">{t('compliance.editor.expressionRequired')}</p>
          ) : null}
        </section>
      ))}

      <fieldset disabled={readOnly} className="space-y-3">
        <legend className="text-sm font-semibold text-fg">{t('compliance.editor.controls')}</legend>
        {COMPLIANCE_STANDARDS.map((standard) => (
          <ControlChoice key={standard} standard={standard} selected={selected} onToggle={toggle} />
        ))}
      </fieldset>

      {refusal.note ? (
        <p role="alert" className="text-sm text-danger">
          {refusal.note}
        </p>
      ) : null}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="secondary" onClick={onCancel}>
          {readOnly ? t('compliance.editor.close') : t('common.cancel')}
        </Button>
        {readOnly ? null : (
          <Button type="submit" loading={save.isPending}>
            {t('common.save')}
          </Button>
        )}
      </div>
    </form>
  );
}

/** conditionText is a stored condition of the rule as JSON (the read-only view). */
function conditionText(rule: ComplianceRule | undefined, field: BuilderField): string {
  return JSON.stringify(rule?.[field] ?? null, null, 2);
}

/** ControlChoice is the multiple choice of the controls of one standard. */
function ControlChoice({
  standard,
  selected,
  onToggle,
}: {
  standard: ComplianceStandard;
  selected: readonly string[];
  onToggle: (id: string) => void;
}) {
  const { t, i18n } = useTranslation();
  const controls = useComplianceControls(standard);
  const de = i18n.language.startsWith('de');
  return (
    <details className="rounded border border-line p-2">
      <summary className="cursor-pointer text-sm text-fg">
        {t(`compliance.standards.${standard}`)}{' '}
        <span className="text-fg-muted">
          ({controls.data?.data.filter((c) => selected.includes(c.id)).length ?? 0})
        </span>
      </summary>
      <ul className="mt-2 max-h-48 space-y-1 overflow-y-auto">
        {(controls.data?.data ?? []).map((c) => (
          <li key={c.id}>
            <label className="flex items-start gap-2 text-sm">
              <input
                type="checkbox"
                checked={selected.includes(c.id)}
                onChange={() => onToggle(c.id)}
              />
              <span>
                <span className="font-mono">{c.control_id}</span> {de ? c.title_de : c.title_en}
              </span>
            </label>
          </li>
        ))}
      </ul>
    </details>
  );
}
