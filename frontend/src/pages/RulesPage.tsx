/**
 * RulesPage is the rule catalog of the metamodel under /rules (MET-45,
 * UI-11, WP-801): the list of GET /api/v1/rules with the filters
 * ci_type_id, attribute_set_id, template_version_id, attribute_key and kind,
 * page by page over next_cursor ("load more"); /rules?attribute_key=<key>
 * filters in advance. Each rule shows its key, attribute, kind, target
 * (exactly one of type, set and template version), a short form of its
 * condition, is_active and version; system rules are read-only. Creating,
 * editing and deleting go through ruleApi (POST, PATCH, DELETE): the
 * condition with the condition mode of the RuleBuilder, the value rule of a
 * validation with its validation mode, each with a preview without the
 * server. A 422 appears at its input or node, a 403 and a 409 as a note.
 *
 * With an active metamodel draft (WP-798) withDraft adds ?draft to every
 * write; the page adds nothing, a 202 answers the draft item and nothing is
 * asked for by an id. Rules with a draft item carry the badge "in draft",
 * new ones appear as placeholder rows from the item payload (E-113). The
 * rules of the attribute definitions (validation, conditional) stay where
 * they are (E-114); the page creates catalog rules only. The page belongs to
 * the module metamodel (E-105); switched off, it shows a note instead.
 */
import { ListChecks, Pencil, Plus, Trash2 } from 'lucide-react';
import { useMemo, useState, type FormEvent, type ReactNode } from 'react';
import { useSearchParams } from 'react-router';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import {
  localizedText,
  type AttributeDefinition,
  type MetamodelDraftItem,
  type MetamodelRule,
  type MetamodelRuleCreate,
  type MetamodelRuleKind,
  type MetamodelRulePatch,
  type RuleFilter,
} from '../api/cmdb';
import {
  useActiveDraftItems,
  useAllCITypes,
  useApplicableAttributeDefinitions,
  useAttributeSets,
  useCreateRule,
  useDeleteRule,
  useRules,
  useSetAttributeDefinitions,
  useTemplates,
  useUpdateRule,
  useVersionsOfTemplates,
} from '../api/cmdbHooks';
import { useHasPermission } from '../api/hooks';
import { ApiError } from '../api/client';
import { useModules } from '../hooks/useEntitlements';
import { Badge } from '../components/ui/Badge';
import { Button, IconButton } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { Toolbar } from '../components/ui/Toolbar';
import { ScrollRegion } from '../components/ScrollRegion';
import {
  tableClasses,
  tdClasses,
  tdMonoClasses,
  tdNumericClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';
import {
  DraftMarker,
  draftChangeFor,
  draftCreations,
  payloadOf,
} from '../components/metamodel/DraftBanner';
import { RuleBuilder } from '../components/rules/RuleBuilder';
import {
  CONDITION_MODE,
  RULE_KINDS,
  VALIDATION_MODE,
  conditionFieldCatalog,
  type ConditionCheck,
  type ValidationCheck,
} from '../rules/builderModes';
import { parseCondition, type Condition, type Rule } from '../rules/rules';

/** The targets of a rule (exactly one); labels ruleCatalog.targets.*. */
export const RULE_TARGETS = ['ci_type', 'attribute_set', 'template_version'] as const;
type TargetKind = (typeof RULE_TARGETS)[number];

const TARGET_FIELD: Record<TargetKind, 'ci_type_id' | 'attribute_set_id' | 'template_version_id'> =
  {
    ci_type: 'ci_type_id',
    attribute_set: 'attribute_set_id',
    template_version: 'template_version_id',
  };

const FILTER_KEYS = [
  'ci_type_id',
  'attribute_set_id',
  'template_version_id',
  'attribute_key',
  'kind',
] as const;

const notFound = (error: unknown) => error instanceof ApiError && error.status === 404;

/** The target of a rule or of the payload of a draft item. */
function targetOf(source: Record<string, unknown>): { kind: TargetKind; id: string } | undefined {
  for (const kind of RULE_TARGETS) {
    const id = source[TARGET_FIELD[kind]];
    if (typeof id === 'string' && id !== '') return { kind, id };
  }
  return undefined;
}

interface VersionInfo {
  id: string;
  label: string;
  status: string;
  ciTypeId: string;
  items: { attribute_definition_id: string; key?: string }[];
}

/**
 * useTargets reads the targets a rule can name (the list hooks of WP-313,
 * WP-797 and WP-314) and names each in the UI language.
 */
function useTargets(enabled: boolean) {
  const { t, i18n } = useTranslation();
  const types = useAllCITypes({ include_inactive: true }, { enabled });
  const sets = useAttributeSets({ enabled });
  const templates = useTemplates({}, { enabled });
  const templateList = useMemo(() => templates.data?.data ?? [], [templates.data]);
  const versionQueries = useVersionsOfTemplates(
    templateList.map((tpl) => tpl.id),
    { enabled },
  );
  const versions = useMemo<VersionInfo[]>(
    () =>
      templateList.flatMap((tpl, i) =>
        (versionQueries[i]?.data?.data ?? []).map((v) => ({
          id: v.id,
          label: t('ruleCatalog.versionLabel', {
            template: localizedText(tpl.display_name, i18n.language) || tpl.key,
            version: v.version,
            status: t(`templates.versionStatuses.${v.status}`),
          }),
          status: v.status,
          ciTypeId: tpl.ci_type_id,
          items: v.items,
        })),
      ),
    // versionQueries is a new array each render; its data decide.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [templateList, t, i18n.language, ...versionQueries.map((q) => q.data)],
  );
  const options: Record<TargetKind, { value: string; label: string }[]> = {
    ci_type: (types.data ?? []).map((ty) => ({
      value: ty.id,
      label: localizedText(ty.display_name, i18n.language) || ty.name || ty.key,
    })),
    attribute_set: (sets.data?.data ?? []).map((s) => ({
      value: s.id,
      label: localizedText(s.display_name, i18n.language) || s.key,
    })),
    template_version: versions.map((v) => ({ value: v.id, label: v.label })),
  };
  const nameOf = (kind: TargetKind, id: string) =>
    options[kind].find((o) => o.value === id)?.label ?? id;
  return { options, versions, nameOf, setsError: sets.error };
}

type Targets = ReturnType<typeof useTargets>;

/** scalarText shows a value of a comparison: texts quoted, lists joined. */
function scalarText(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(scalarText).join(', ')}]`;
  return typeof value === 'string' ? `"${value}"` : String(value);
}

/** conditionSummary is the short form of a condition in the UI language. */
export function conditionSummary(condition: unknown, t: TFunction): string {
  if (condition === null || condition === undefined) return t('ruleCatalog.always');
  let parsed: Condition;
  try {
    parsed = parseCondition(condition);
  } catch {
    return JSON.stringify(condition);
  }
  const walk = (node: Condition, nested: boolean): string => {
    if ('rules' in node) {
      if (node.op === 'not') return `${t('rules.nodeKinds.not')} (${walk(node.rules[0]!, false)})`;
      const text = node.rules
        .map((c) => walk(c, true))
        .join(` ${t(`rules.conditionJunctions.${node.op}`)} `);
      return nested && node.rules.length > 1 ? `(${text})` : text;
    }
    const op = t(`rules.conditionOps.${node.op}`);
    return 'value' in node
      ? `${node.field} ${op} ${scalarText(node.value)}`
      : `${node.field} ${op}`;
  };
  return walk(parsed, false);
}

/** describeRuleRefusal turns a refused write into a note: 403 and 409 as hints. */
function describeRuleRefusal(error: unknown, t: TFunction): string {
  if (error instanceof ApiError) {
    const detail = error.detail || error.message;
    if (error.status === 403) return t('ruleCatalog.errors.forbidden');
    if (error.status === 409) return t('ruleCatalog.errors.conflict', { detail });
    if (error.status === 404) return t('ruleCatalog.errors.notFound');
    return t('ruleCatalog.errors.reason', { detail });
  }
  return t('ruleCatalog.errors.reason', {
    detail: error instanceof Error ? error.message : String(error),
  });
}

function Header({ action }: { action?: ReactNode }) {
  const { t } = useTranslation();
  return (
    <PageHeader
      title={t('ruleCatalog.title')}
      description={t('ruleCatalog.subtitle')}
      actions={action}
    />
  );
}

type Dialog = { mode: 'create' } | { mode: 'edit'; rule: MetamodelRule };

export function RulesPage() {
  const { t } = useTranslation();
  const modules = useModules();
  const moduleOff = !modules.isLoading && !modules.isModuleEnabled('metamodel');
  if (moduleOff) {
    return (
      <div className="space-y-6">
        <Header />
        <EmptyState icon={<ListChecks />} title={t('ruleCatalog.moduleOff')} />
      </div>
    );
  }
  return <RuleCatalog />;
}

function RuleCatalog() {
  const { t } = useTranslation();
  const [params, setParams] = useSearchParams();
  const filter = useMemo<RuleFilter>(() => {
    const out: RuleFilter = {};
    for (const key of FILTER_KEYS) {
      const value = params.get(key);
      if (value) (out as Record<string, string>)[key] = value;
    }
    return out;
  }, [params]);
  const setFilter = (key: (typeof FILTER_KEYS)[number], value: string) => {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value);
    else next.delete(key);
    setParams(next, { replace: true });
  };
  const canManage = useHasPermission('citype:manage') === true;
  const query = useRules(filter);
  const targets = useTargets(true);
  const draftItems = useActiveDraftItems();
  const remove = useDeleteRule();
  const [dialog, setDialog] = useState<Dialog | null>(null);
  const [deleting, setDeleting] = useState<MetamodelRule | null>(null);

  const rules = query.data?.pages.flatMap((page) => page.data) ?? [];
  const placeholders = draftCreations(draftItems, 'rule', (payload) =>
    FILTER_KEYS.every((key) => !filter[key] || payload[key] === filter[key]),
  );

  if (notFound(query.error)) {
    return (
      <div className="space-y-6">
        <Header />
        <EmptyState icon={<ListChecks />} title={t('ruleCatalog.moduleOff')} />
      </div>
    );
  }

  const createButton = canManage ? (
    <Button icon={<Plus />} onClick={() => setDialog({ mode: 'create' })}>
      {t('ruleCatalog.create')}
    </Button>
  ) : null;

  return (
    <div className="space-y-6">
      <Header action={createButton} />
      <p className="max-w-[68ch] text-sm text-fg-muted">{t('ruleCatalog.hint')}</p>

      <Toolbar className="items-end">
        {RULE_TARGETS.map((kind) => (
          <div key={kind} className="min-w-[12rem]">
            <Select
              label={t(`ruleCatalog.filters.${kind}`)}
              value={filter[TARGET_FIELD[kind]] ?? ''}
              onChange={(e) => setFilter(TARGET_FIELD[kind], e.target.value)}
              options={[{ value: '', label: t('common.all') }, ...targets.options[kind]]}
            />
          </div>
        ))}
        <div className="min-w-[12rem]">
          <Input
            label={t('ruleCatalog.filters.attribute_key')}
            value={filter.attribute_key ?? ''}
            onChange={(e) => setFilter('attribute_key', e.target.value.trim())}
          />
        </div>
        <div className="min-w-[10rem]">
          <Select
            label={t('ruleCatalog.filters.kind')}
            value={filter.kind ?? ''}
            onChange={(e) => setFilter('kind', e.target.value)}
            options={[
              { value: '', label: t('common.all') },
              ...RULE_KINDS.map((kind) => ({ value: kind, label: t(`ruleCatalog.kinds.${kind}`) })),
            ]}
          />
        </div>
      </Toolbar>

      {query.isLoading ? <SkeletonList rows={3} label={t('app.loading')} /> : null}
      {query.error ? (
        <ErrorState
          title={t('app.error')}
          retryLabel={t('common.retry')}
          onRetry={() => void query.refetch()}
        />
      ) : null}
      {remove.error ? (
        <p role="alert" className="text-sm text-danger">
          {t('ruleCatalog.notDeleted', { reason: describeRuleRefusal(remove.error, t) })}
        </p>
      ) : null}

      {query.isSuccess && rules.length === 0 && placeholders.length === 0 ? (
        <EmptyState
          icon={<ListChecks />}
          title={t('ruleCatalog.empty')}
          action={
            canManage ? (
              <Button
                variant="secondary"
                icon={<Plus />}
                onClick={() => setDialog({ mode: 'create' })}
              >
                {t('ruleCatalog.create')}
              </Button>
            ) : undefined
          }
        />
      ) : null}

      {rules.length > 0 || placeholders.length > 0 ? (
        <Card className="overflow-hidden p-0">
          <ScrollRegion label={t('ruleCatalog.listLabel')}>
            <table className={tableClasses} aria-label={t('ruleCatalog.listLabel')}>
              <thead className={theadClasses}>
                <tr>
                  {(
                    [
                      'key',
                      'attribute',
                      'kind',
                      'target',
                      'condition',
                      'active',
                      'version',
                    ] as const
                  ).map((column) => (
                    <th key={column} scope="col" className={thClasses}>
                      {t(`ruleCatalog.columns.${column}`)}
                    </th>
                  ))}
                  <th scope="col" className={thClasses}>
                    <span className="sr-only">{t('ruleCatalog.columns.actions')}</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {placeholders.map((item) => (
                  <PlaceholderRow key={item.id} item={item} targets={targets} />
                ))}
                {rules.map((rule) => (
                  <RuleRow
                    key={rule.id}
                    rule={rule}
                    targets={targets}
                    change={draftChangeFor(draftItems, { kinds: ['rule'], id: rule.id })}
                    canManage={canManage}
                    onEdit={() => setDialog({ mode: 'edit', rule })}
                    onDelete={() => {
                      remove.reset();
                      setDeleting(rule);
                    }}
                  />
                ))}
              </tbody>
            </table>
          </ScrollRegion>
        </Card>
      ) : null}

      {query.hasNextPage ? (
        <div className="flex justify-center">
          <Button
            variant="secondary"
            disabled={query.isFetchingNextPage}
            onClick={() => void query.fetchNextPage()}
          >
            {t('ruleCatalog.loadMore')}
          </Button>
        </div>
      ) : null}

      {dialog ? (
        <RuleDialog
          key={dialog.mode === 'edit' ? dialog.rule.id : 'new'}
          dialog={dialog}
          targets={targets}
          onClose={() => setDialog(null)}
        />
      ) : null}
      {deleting ? (
        <Modal
          open
          onOpenChange={(open) => (open ? undefined : setDeleting(null))}
          title={t('ruleCatalog.deleteTitle')}
          description={t('ruleCatalog.deleteConfirm', { key: deleting.key })}
          size="sm"
        >
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setDeleting(null)}>
              {t('common.cancel')}
            </Button>
            <Button
              variant="danger"
              icon={<Trash2 />}
              disabled={remove.isPending}
              onClick={() =>
                remove.mutate(deleting.id, {
                  onSettled: () => setDeleting(null),
                })
              }
            >
              {t('ruleCatalog.delete')}
            </Button>
          </div>
        </Modal>
      ) : null}
    </div>
  );
}

/** TargetCell names the target of a rule: its kind and its name. */
function TargetCell({ source, targets }: { source: Record<string, unknown>; targets: Targets }) {
  const { t } = useTranslation();
  const target = targetOf(source);
  if (!target) return <span>{t('ruleCatalog.noTarget')}</span>;
  return (
    <span className="[overflow-wrap:anywhere]">
      {t('ruleCatalog.targetLine', {
        kind: t(`ruleCatalog.targets.${target.kind}`),
        name: targets.nameOf(target.kind, target.id),
      })}
    </span>
  );
}

function RuleRow({
  rule,
  targets,
  change,
  canManage,
  onEdit,
  onDelete,
}: {
  rule: MetamodelRule;
  targets: Targets;
  change: ReturnType<typeof draftChangeFor>;
  canManage: boolean;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const { t } = useTranslation();
  const system = rule.is_system || !rule.organization_id;
  return (
    <tr className={trClasses} data-rule-key={rule.key}>
      <td className={tdMonoClasses}>
        <span className="flex flex-wrap items-center gap-2">
          <span>{rule.key}</span>
          {system ? <Badge variant="info">{t('ruleCatalog.system')}</Badge> : null}
          {change ? <DraftMarker change={change} /> : null}
        </span>
      </td>
      <td className={tdMonoClasses}>{rule.attribute_key}</td>
      <td className={tdClasses}>{t(`ruleCatalog.kinds.${rule.kind}`)}</td>
      <td className={tdClasses}>
        <TargetCell source={rule as unknown as Record<string, unknown>} targets={targets} />
      </td>
      <td className={tdClasses}>
        <span className="font-mono text-xs [overflow-wrap:anywhere]">
          {conditionSummary(rule.condition, t)}
        </span>
      </td>
      <td className={tdClasses}>
        {rule.is_active ? (
          <Badge variant="success">{t('ruleCatalog.active')}</Badge>
        ) : (
          <Badge variant="neutral">{t('ruleCatalog.inactive')}</Badge>
        )}
      </td>
      <td className={tdNumericClasses}>{rule.version}</td>
      <td className={tdClasses}>
        {canManage && !system ? (
          <span className="flex justify-end gap-1">
            <IconButton
              size="sm"
              icon={<Pencil />}
              label={t('ruleCatalog.edit', { key: rule.key })}
              onClick={onEdit}
            />
            <IconButton
              size="sm"
              icon={<Trash2 />}
              label={t('ruleCatalog.deleteRule', { key: rule.key })}
              onClick={onDelete}
              className="hover:text-danger"
            />
          </span>
        ) : system ? (
          <span className="text-xs">{t('ruleCatalog.readOnly')}</span>
        ) : null}
      </td>
    </tr>
  );
}

/** PlaceholderRow is a rule a draft creates: the values of its payload, not productive. */
function PlaceholderRow({ item, targets }: { item: MetamodelDraftItem; targets: Targets }) {
  const { t } = useTranslation();
  const payload = payloadOf(item);
  const text = (key: string) => (typeof payload[key] === 'string' ? (payload[key] as string) : '');
  const kind = text('kind');
  return (
    <tr className={trClasses} data-draft-item={item.id}>
      <td className={tdMonoClasses}>
        <span className="flex flex-wrap items-center gap-2">
          <span>{text('key') || t('metamodelDrafts.noTarget')}</span>
          <DraftMarker change="created" />
        </span>
      </td>
      <td className={tdMonoClasses}>{text('attribute_key')}</td>
      <td className={tdClasses}>
        {(RULE_KINDS as readonly string[]).includes(kind) ? t(`ruleCatalog.kinds.${kind}`) : kind}
      </td>
      <td className={tdClasses}>
        <TargetCell source={payload} targets={targets} />
      </td>
      <td className={tdClasses}>
        <span className="font-mono text-xs [overflow-wrap:anywhere]">
          {conditionSummary(payload.condition, t)}
        </span>
      </td>
      <td className={tdClasses}>
        {payload.is_active === false ? (
          <Badge variant="neutral">{t('ruleCatalog.inactive')}</Badge>
        ) : (
          <Badge variant="success">{t('ruleCatalog.active')}</Badge>
        )}
      </td>
      <td className={tdNumericClasses}>–</td>
      <td className={tdClasses} />
    </tr>
  );
}

/** The fields of a refusal of the rule form, keyed by the field of the server. */
interface Refusal {
  fields: Record<string, string>;
  condition?: { path: string; message: string };
  ast?: { path: string; message: string };
  note?: string;
}

function refusalOf(error: unknown, t: TFunction): Refusal | undefined {
  if (!error) return undefined;
  if (!(error instanceof ApiError) || error.status !== 422) {
    return { fields: {}, note: describeRuleRefusal(error, t) };
  }
  const detail = error.detail || error.message;
  const entries: [string, string][] = error.violations.map((v) => [v.field, v.detail || detail]);
  if (error.field && !entries.some(([f]) => f === error.field)) entries.push([error.field, detail]);
  const out: Refusal = { fields: {} };
  for (const [field, message] of entries) {
    if (field === 'condition' || field.startsWith('condition.')) {
      out.condition ??= { path: field, message };
    } else if (field === 'ast' || field.startsWith('ast.')) {
      out.ast ??= { path: field, message };
    } else {
      const key = field.startsWith('message.') ? 'message' : field;
      out.fields[key] ??= message;
    }
  }
  if (entries.length === 0) out.note = describeRuleRefusal(error, t);
  return out;
}

function RuleDialog({
  dialog,
  targets,
  onClose,
}: {
  dialog: Dialog;
  targets: Targets;
  onClose: () => void;
}) {
  const { t, i18n } = useTranslation();
  const editing = dialog.mode === 'edit' ? dialog.rule : undefined;
  const initialTarget = editing
    ? targetOf(editing as unknown as Record<string, unknown>)
    : undefined;
  const [key, setKey] = useState(editing?.key ?? '');
  const [targetKind, setTargetKind] = useState<TargetKind>(initialTarget?.kind ?? 'ci_type');
  const [targetId, setTargetId] = useState(initialTarget?.id ?? '');
  const [attributeKey, setAttributeKey] = useState(editing?.attribute_key ?? '');
  const [kind, setKind] = useState<MetamodelRuleKind>(editing?.kind ?? 'required');
  const [de, setDe] = useState(editing?.message?.de ?? '');
  const [en, setEn] = useState(editing?.message?.en ?? '');
  const [isActive, setIsActive] = useState(editing?.is_active ?? true);
  const [conditionCheck, setConditionCheck] = useState<ConditionCheck>({
    ok: true,
    condition: null,
  });
  const [astCheck, setAstCheck] = useState<ValidationCheck>({ ok: true, ast: null });
  const [showIssues, setShowIssues] = useState(false);
  const create = useCreateRule();
  const update = useUpdateRule();
  const mutation = editing ? update : create;
  // Memoized so the builders, which take its condition and ast, keep their
  // props while the text fields of the dialog change.
  const refusal = useMemo(() => refusalOf(mutation.error, t), [mutation.error, t]);

  // The attribute definitions of the target: of a type (with the global
  // ones that apply), of a set, or the items of a template version.
  const version =
    targetKind === 'template_version' ? targets.versions.find((v) => v.id === targetId) : undefined;
  const typeId =
    targetKind === 'ci_type' && targetId
      ? targetId
      : targetKind === 'template_version'
        ? version?.ciTypeId
        : undefined;
  const applicable = useApplicableAttributeDefinitions(typeId);
  const setQueries = useSetAttributeDefinitions(
    targetKind === 'attribute_set' && targetId ? [targetId] : [],
  );
  const definitions = useMemo<AttributeDefinition[]>(() => {
    if (targetKind === 'attribute_set') return setQueries[0]?.data?.data ?? [];
    const all = applicable.data?.data ?? [];
    if (targetKind === 'ci_type') return all;
    const ids = new Set((version?.items ?? []).map((item) => item.attribute_definition_id));
    return all.filter((d) => ids.has(d.id));
    // setQueries is a new array each render; its data decide.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [targetKind, applicable.data, version, setQueries[0]?.data]);
  const fields = useMemo(() => conditionFieldCatalog(definitions), [definitions]);
  const attributeKeys = useMemo(() => {
    const keys = definitions.map((d) => d.key);
    for (const item of version?.items ?? []) {
      if (item.key && !keys.includes(item.key)) keys.push(item.key);
    }
    if (attributeKey && !keys.includes(attributeKey)) keys.unshift(attributeKey);
    return keys;
  }, [definitions, version, attributeKey]);
  const attributeSpec = fields.find((f) => f.field === attributeKey);
  const locked = version !== undefined && version.status !== 'draft';

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setShowIssues(true);
    if (!conditionCheck.ok) return;
    if (kind === 'validation' && (!astCheck.ok || astCheck.ast === null)) return;
    const condition = conditionCheck.condition;
    const ast: Rule | null = kind === 'validation' && astCheck.ok ? astCheck.ast : null;
    const texts: Record<string, string> = {};
    if (de.trim()) texts.de = de.trim();
    if (en.trim()) texts.en = en.trim();
    const message = Object.keys(texts).length > 0 ? texts : null;
    const done = { onSuccess: () => onClose() };
    if (editing) {
      const patch: MetamodelRulePatch = {
        attribute_key: attributeKey,
        kind,
        condition: condition as MetamodelRulePatch['condition'],
        ast: ast as MetamodelRulePatch['ast'],
        message,
        is_active: isActive,
      };
      update.mutate({ id: editing.id, patch }, done);
      return;
    }
    const body: MetamodelRuleCreate = {
      key: key.trim(),
      attribute_key: attributeKey,
      kind,
      [TARGET_FIELD[targetKind]]: targetId,
      is_active: isActive,
    };
    if (condition) body.condition = condition as MetamodelRuleCreate['condition'];
    if (ast) body.ast = ast as MetamodelRuleCreate['ast'];
    if (message) body.message = message;
    create.mutate(body, done);
  };

  const astMissing = showIssues && kind === 'validation' && astCheck.ok && astCheck.ast === null;
  const targetError =
    refusal?.fields[TARGET_FIELD[targetKind]] ??
    refusal?.fields.ci_type_id ??
    refusal?.fields.attribute_set_id ??
    refusal?.fields.template_version_id;

  return (
    <Modal
      open
      onOpenChange={(open) => (open ? undefined : onClose())}
      title={
        editing ? t('ruleCatalog.editTitle', { key: editing.key }) : t('ruleCatalog.createTitle')
      }
      size="xl"
    >
      <form onSubmit={submit} className="space-y-5" noValidate>
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
          <Input
            label={t('ruleCatalog.form.key')}
            value={key}
            required
            maxLength={100}
            disabled={!!editing}
            error={refusal?.fields.key}
            onChange={(e) => setKey(e.target.value)}
          />
          <Select
            label={t('ruleCatalog.form.kind')}
            value={kind}
            error={refusal?.fields.kind}
            onChange={(e) => setKind(e.target.value as MetamodelRuleKind)}
            options={RULE_KINDS.map((k) => ({ value: k, label: t(`ruleCatalog.kinds.${k}`) }))}
          />
          <Select
            label={t('ruleCatalog.form.targetKind')}
            value={targetKind}
            disabled={!!editing}
            onChange={(e) => {
              setTargetKind(e.target.value as TargetKind);
              setTargetId('');
              setAttributeKey('');
            }}
            options={RULE_TARGETS.map((k) => ({ value: k, label: t(`ruleCatalog.targets.${k}`) }))}
          />
          <Select
            label={t('ruleCatalog.form.target')}
            value={targetId}
            disabled={!!editing}
            error={targetError}
            onChange={(e) => {
              setTargetId(e.target.value);
              setAttributeKey('');
            }}
            options={[
              { value: '', label: t('ruleCatalog.form.chooseTarget') },
              ...(editing &&
              targetId &&
              !targets.options[targetKind].some((o) => o.value === targetId)
                ? [{ value: targetId, label: targetId }]
                : []),
              ...targets.options[targetKind],
            ]}
          />
          <Select
            label={t('ruleCatalog.form.attribute')}
            value={attributeKey}
            disabled={!targetId}
            error={refusal?.fields.attribute_key}
            onChange={(e) => setAttributeKey(e.target.value)}
            options={[
              { value: '', label: t('ruleCatalog.form.chooseAttribute') },
              ...attributeKeys.map((k) => {
                const name = localizedText(
                  definitions.find((d) => d.key === k)?.display_name,
                  i18n.language,
                );
                return { value: k, label: name && name !== k ? `${name} (${k})` : k };
              }),
            ]}
          />
          <label className="flex items-center gap-2 self-end pb-2 text-sm text-fg">
            <input
              type="checkbox"
              checked={isActive}
              onChange={(e) => setIsActive(e.target.checked)}
            />
            {t('ruleCatalog.form.isActive')}
          </label>
        </div>
        {locked ? (
          <p className="text-sm text-fg-muted">{t('ruleCatalog.form.lockedVersion')}</p>
        ) : null}

        <div className="space-y-2">
          <p className="text-sm text-fg-muted">{t('ruleCatalog.form.conditionHint')}</p>
          <RuleBuilder
            mode={CONDITION_MODE}
            initialValue={(editing?.condition as Condition | null | undefined) ?? null}
            fields={fields}
            onChange={setConditionCheck}
            serverError={refusal?.condition}
            showIssues={showIssues}
            label={t('ruleCatalog.form.condition')}
          />
        </div>

        {kind === 'validation' ? (
          <div className="space-y-2">
            <RuleBuilder
              key={attributeKey}
              mode={VALIDATION_MODE}
              initialValue={(editing?.ast as Rule | null | undefined) ?? null}
              attribute={attributeSpec}
              onChange={setAstCheck}
              serverError={refusal?.ast}
              showIssues={showIssues}
              label={t('ruleCatalog.form.ast')}
            />
            {astMissing ? (
              <p className="text-sm text-danger">{t('ruleCatalog.form.astRequired')}</p>
            ) : null}
          </div>
        ) : null}

        <fieldset className="grid grid-cols-1 gap-4 md:grid-cols-2">
          <legend className="mb-2 text-sm font-medium text-fg">
            {t('ruleCatalog.form.message')}
          </legend>
          <Input
            label={t('ruleCatalog.form.messageDe')}
            value={de}
            maxLength={500}
            error={refusal?.fields.message}
            onChange={(e) => setDe(e.target.value)}
          />
          <Input
            label={t('ruleCatalog.form.messageEn')}
            value={en}
            maxLength={500}
            onChange={(e) => setEn(e.target.value)}
          />
        </fieldset>

        {refusal?.note ? (
          <p role="alert" className="text-sm text-danger">
            {refusal.note}
          </p>
        ) : null}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button
            type="submit"
            disabled={mutation.isPending || !key.trim() || !targetId || !attributeKey}
          >
            {t('ruleCatalog.save')}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
