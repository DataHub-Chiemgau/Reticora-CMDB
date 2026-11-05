/**
 * TemplateVersionEditor edits the items of a template version (MET-16,
 * UI-11, WP-314). Only a draft of a template of the organization is
 * editable; published and retired versions and system templates are
 * read-only. An item is an attribute definition of the template's CI type,
 * of its type chain (GET /attribute-definitions?ci_type_id=) or a global one
 * (?scope=global); templates hold no sets and no keys of their own (E-37).
 * Per item the overrides only narrow: required, a default (checked with
 * validateValue against the definition and the narrowed rules before the
 * save) and the validation (a subset of the allowed values, narrower
 * bounds, a pattern). Data type, searchable, unique and unique_scope are
 * fixed (MET-15). The order changes with SortableList (E-54). The save
 * sends the whole list with PUT /templates/{id}/versions/{version}; a 422
 * shows at its item.
 */
import { Plus, Save, X } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import {
  localizedText,
  type AttributeDefinition,
  type Template,
  type TemplateVersion,
  type TemplateVersionItem,
  type TemplateVersionItemsUpdate,
} from '../../api/cmdb';
import {
  useGlobalAttributeDefinitions,
  useSaveTemplateVersion,
  useTypeAttributeDefinitions,
} from '../../api/cmdbHooks';
import { ApiError } from '../../api/client';
import {
  validateValue,
  type FieldDataType,
  type FieldDefinition,
  type FieldMessage,
  type ValidationRules,
} from '../../lib/fieldmeta';
import { Badge } from '../ui/Badge';
import { Button } from '../ui/Button';
import { Input } from '../ui/Input';
import { Select } from '../ui/Select';
import { describeFlagRefusal } from './AttributeEditor';
import { SortableList } from './SortableList';

/** One badge variant per state of a template version: the draft is work in progress. */
export const VERSION_STATUS_VARIANT = {
  draft: 'warning',
  published: 'success',
  retired: 'neutral',
} as const;

const NUMERIC_TYPES = new Set(['integer', 'decimal', 'number']);
const TEXT_TYPES = new Set(['text', 'multiline', 'textarea', 'string', 'email', 'url']);
const LIST_TYPES = new Set(['multiselect', 'multi_enum']);

/** The narrowing rules of an override in the legacy shape of the form. */
export type OverrideRules = Pick<
  ValidationRules,
  'options' | 'min' | 'max' | 'min_length' | 'max_length' | 'pattern'
>;

type Json = Record<string, unknown>;

/**
 * rulesOf reads a validation, the rule AST of MET-45 the server answers or
 * the legacy structure, into the rules the editor shows.
 */
export function rulesOf(validation: unknown): OverrideRules {
  const out: OverrideRules = {};
  if (!validation || typeof validation !== 'object') return out;
  const v = validation as Json;
  if (typeof v.op !== 'string') {
    if (Array.isArray(v.options) && v.options.length > 0) out.options = v.options.map(String);
    for (const k of ['min', 'max', 'min_length', 'max_length'] as const) {
      if (typeof v[k] === 'number') out[k] = v[k] as number;
    }
    const pattern = v.pattern ?? (Array.isArray(v.patterns) ? v.patterns[0] : undefined);
    if (typeof pattern === 'string' && pattern) out.pattern = pattern;
    return out;
  }
  const walk = (rule: Json) => {
    switch (rule.op) {
      case 'all':
        for (const child of (rule.rules as Json[] | undefined) ?? []) walk(child);
        break;
      case 'enum':
        out.options = ((rule.values as unknown[] | undefined) ?? []).map(String);
        break;
      case 'pattern':
        if (typeof rule.pattern === 'string') out.pattern = rule.pattern;
        break;
      case 'range':
        if (rule.of === 'length') {
          if (typeof rule.min === 'number') out.min_length = rule.min;
          if (typeof rule.max === 'number') out.max_length = rule.max;
        } else {
          if (typeof rule.min === 'number') out.min = rule.min;
          if (typeof rule.max === 'number') out.max = rule.max;
        }
        break;
    }
  };
  walk(v);
  return out;
}

/** astOf writes the rules as the rule AST of MET-45; undefined without a rule. */
export function astOf(rules: OverrideRules): Json | undefined {
  const list: Json[] = [];
  if (rules.options && rules.options.length > 0) list.push({ op: 'enum', values: rules.options });
  if (rules.min !== undefined || rules.max !== undefined) {
    list.push({ op: 'range', ...pick(rules.min, 'min'), ...pick(rules.max, 'max') });
  }
  if (rules.min_length !== undefined || rules.max_length !== undefined) {
    list.push({
      op: 'range',
      of: 'length',
      ...pick(rules.min_length, 'min'),
      ...pick(rules.max_length, 'max'),
    });
  }
  if (rules.pattern) list.push({ op: 'pattern', pattern: rules.pattern });
  return list.length > 0 ? { op: 'all', rules: list } : undefined;
}

function pick(value: number | undefined, name: string): Json {
  return value === undefined ? {} : { [name]: value };
}

/** The allowed values of a definition: its enum rule, else its enum_values. */
function optionsOf(definition: AttributeDefinition): string[] {
  return rulesOf(definition.validation).options ?? definition.enum_values ?? [];
}

/** formatValue renders a stored default for the input: text as it is, else JSON. */
function formatValue(value: unknown): string {
  if (value === undefined || value === null) return '';
  if (typeof value === 'string') return value;
  if (Array.isArray(value) && value.every((v) => typeof v === 'string')) return value.join(', ');
  return JSON.stringify(value);
}

/**
 * parseDefault turns the input into the JSON value of the data type: numbers
 * and booleans when they read as such (otherwise the text stays and fails
 * validateValue), lists for multi-value types, JSON for json.
 */
export function parseDefault(text: string, definition: AttributeDefinition): unknown {
  const s = text.trim();
  if (s === '') return undefined;
  if (NUMERIC_TYPES.has(definition.data_type)) {
    const n = Number(s);
    return Number.isNaN(n) ? s : n;
  }
  if (definition.data_type === 'boolean') return s === 'true' ? true : s === 'false' ? false : s;
  if (definition.data_type === 'json') {
    try {
      return JSON.parse(s) as unknown;
    } catch {
      return s;
    }
  }
  if (definition.multi_value || LIST_TYPES.has(definition.data_type)) {
    return s
      .split(',')
      .map((part) => part.trim())
      .filter(Boolean);
  }
  return text;
}

/** fieldOf is the definition as the field of validateValue, with the given rules. */
function fieldOf(
  definition: AttributeDefinition,
  label: string,
  rules: OverrideRules,
  withEnumValues: boolean,
): FieldDefinition {
  return {
    name: definition.key,
    label,
    data_type: definition.data_type as FieldDataType,
    enum_values: withEnumValues ? definition.enum_values : undefined,
    multi_value: definition.multi_value,
    validation: rules,
  };
}

/** The editable state of an item: the inputs as text. */
interface ItemDraft {
  attribute_definition_id: string;
  key: string;
  required: boolean;
  defaultText: string;
  options: string[];
  min: string;
  max: string;
  minLength: string;
  maxLength: string;
  pattern: string;
}

const numberText = (n: number | undefined) => (n === undefined ? '' : String(n));

function draftOf(item: TemplateVersionItem): ItemDraft {
  const rules = rulesOf(item.validation_override);
  return {
    attribute_definition_id: item.attribute_definition_id,
    key: item.key ?? '',
    required: !!item.required_override,
    defaultText: formatValue(item.default_override),
    options: rules.options ?? [],
    min: numberText(rules.min),
    max: numberText(rules.max),
    minLength: numberText(rules.min_length),
    maxLength: numberText(rules.max_length),
    pattern: rules.pattern ?? '',
  };
}

function emptyDraft(definition: AttributeDefinition): ItemDraft {
  return {
    attribute_definition_id: definition.id,
    key: definition.key,
    required: false,
    defaultText: '',
    options: [],
    min: '',
    max: '',
    minLength: '',
    maxLength: '',
    pattern: '',
  };
}

type Translate = (message: FieldMessage) => string;

/**
 * checkItem builds the item of the PUT and its errors: numbers that do not
 * read as numbers, an invalid pattern and a default that fails the
 * definition or the narrowed rules.
 */
function checkItem(
  draft: ItemDraft,
  position: number,
  definition: AttributeDefinition | undefined,
  label: string,
  t: TFunction,
  translate: Translate,
): { item: TemplateVersionItemsUpdate['items'][number]; errors: string[] } {
  const errors: string[] = [];
  const rules: OverrideRules = {};
  if (draft.options.length > 0) rules.options = draft.options;
  const numbers = [
    ['min', draft.min, 'templates.editor.min'],
    ['max', draft.max, 'templates.editor.max'],
    ['min_length', draft.minLength, 'templates.editor.minLength'],
    ['max_length', draft.maxLength, 'templates.editor.maxLength'],
  ] as const;
  for (const [name, text, labelKey] of numbers) {
    if (text.trim() === '') continue;
    const n = Number(text);
    if (Number.isNaN(n)) errors.push(t('templates.editor.notNumber', { field: t(labelKey) }));
    else rules[name] = n;
  }
  if (draft.pattern) {
    try {
      new RegExp(draft.pattern, 'u');
      rules.pattern = draft.pattern;
    } catch {
      errors.push(t('templates.editor.badPattern'));
    }
  }
  const item: TemplateVersionItemsUpdate['items'][number] = {
    attribute_definition_id: draft.attribute_definition_id,
    position,
  };
  if (draft.required) item.required_override = true;
  const validation = astOf(rules);
  if (validation) item.validation_override = validation as typeof item.validation_override;
  if (draft.defaultText.trim() !== '') {
    if (definition) {
      const value = parseDefault(draft.defaultText, definition);
      const base = fieldOf(definition, label, rulesOf(definition.validation), true);
      const problem =
        validateValue(base, value) ??
        (validation ? validateValue(fieldOf(definition, label, rules, false), value) : null);
      if (problem) errors.push(translate(problem));
      item.default_override = value;
    } else {
      item.default_override = draft.defaultText;
    }
  }
  return { item, errors };
}

/** The index of the item a refusal names: items[<i>]... or the attribute key. */
function itemIndexOf(field: string | undefined, drafts: ItemDraft[]): number {
  if (!field) return -1;
  const m = /^items\[(\d+)\]/.exec(field);
  if (m) return Number(m[1]);
  return drafts.findIndex((d) => d.key === field);
}

/** refusalDetail is the server's message without the "<field>: " prefix. */
function refusalDetail(detail: string, field: string | undefined): string {
  return field && detail.startsWith(`${field}: `) ? detail.slice(field.length + 2) : detail;
}

export interface TemplateVersionEditorProps {
  template: Template;
  version: TemplateVersion;
  /** The template's CI type and its ancestors (MET-06). */
  typeChain: string[];
  /** citype:manage of the user. */
  canManage: boolean;
  /** Messages per attribute key, e.g. of a refused publish (422, MET-16). */
  itemErrors?: Record<string, string>;
}

export function TemplateVersionEditor({
  template,
  version,
  typeChain,
  canManage,
  itemErrors = {},
}: TemplateVersionEditorProps) {
  const { t, i18n } = useTranslation();
  const editable = version.status === 'draft' && !template.is_system && canManage;
  const typeQueries = useTypeAttributeDefinitions(typeChain);
  const globals = useGlobalAttributeDefinitions();
  const save = useSaveTemplateVersion();
  const [drafts, setDrafts] = useState<ItemDraft[]>(() =>
    [...version.items].sort((a, b) => a.position - b.position).map(draftOf),
  );
  const [errors, setErrors] = useState<Record<string, string[]>>({});
  const [general, setGeneral] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [choice, setChoice] = useState('');

  const definitions = useMemo(() => {
    const out = new Map<string, AttributeDefinition>();
    for (const q of typeQueries) for (const d of q.data?.data ?? []) out.set(d.id, d);
    for (const d of globals.data?.data ?? []) out.set(d.id, d);
    return out;
  }, [typeQueries, globals.data]);

  const labelOf = (draft: ItemDraft) => {
    const d = definitions.get(draft.attribute_definition_id);
    const key = draft.key || d?.key || draft.attribute_definition_id;
    return (d && localizedText(d.display_name, i18n.language)) || key;
  };
  const translate: Translate = (m) => t(m.key, m.params);

  const update = (id: string, change: Partial<ItemDraft>) => {
    setSaved(false);
    setDrafts((list) =>
      list.map((d) => (d.attribute_definition_id === id ? { ...d, ...change } : d)),
    );
  };

  const choosable = [...definitions.values()]
    .filter((d) => d.scope !== 'set' && !drafts.some((x) => x.attribute_definition_id === d.id))
    .sort((a, b) => a.key.localeCompare(b.key));

  const add = () => {
    const d = definitions.get(choice);
    if (!d) return;
    setSaved(false);
    setDrafts((list) => [...list, emptyDraft(d)]);
    setChoice('');
  };

  const onSave = () => {
    setGeneral(null);
    setSaved(false);
    const checked = drafts.map((draft, i) =>
      checkItem(
        draft,
        i,
        definitions.get(draft.attribute_definition_id),
        labelOf(draft),
        t,
        translate,
      ),
    );
    const found: Record<string, string[]> = {};
    checked.forEach(({ errors: list }, i) => {
      if (list.length > 0) found[drafts[i]!.attribute_definition_id] = list;
    });
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    save.mutate(
      { id: template.id, version: version.version, data: { items: checked.map((c) => c.item) } },
      {
        onSuccess: () => setSaved(true),
        onError: (error) => {
          if (!(error instanceof ApiError)) {
            setGeneral(describeFlagRefusal(error, t));
            return;
          }
          const byItem: Record<string, string[]> = {};
          const unplaced: string[] = [];
          const named = [
            ...error.violations,
            ...(error.field && !error.violations.some((v) => v.field === error.field)
              ? [{ field: error.field, detail: error.detail || error.message }]
              : []),
          ];
          for (const v of named) {
            const index = itemIndexOf(v.field, drafts);
            const target = drafts[index];
            const detail = refusalDetail(v.detail, v.field);
            if (target) {
              (byItem[target.attribute_definition_id] ??= []).push(detail);
            } else {
              unplaced.push(detail);
            }
          }
          setErrors(byItem);
          if (Object.keys(byItem).length === 0 || unplaced.length > 0) {
            setGeneral(
              unplaced.length > 0
                ? t('templates.editor.notSaved', { reason: unplaced.join('; ') })
                : t('templates.editor.notSaved', { reason: describeFlagRefusal(error, t) }),
            );
          }
        },
      },
    );
  };

  const heading = `template-version-${version.id}`;
  return (
    <section
      aria-labelledby={heading}
      className="space-y-3 rounded-xl border border-line bg-panel p-5"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h4 id={heading} className="text-base font-semibold text-fg">
          {t('templates.editor.title', { version: version.version })}
        </h4>
        <Badge variant={VERSION_STATUS_VARIANT[version.status]}>
          {t(`templates.versionStatuses.${version.status}`)}
        </Badge>
      </div>
      {editable ? (
        <p className="max-w-[68ch] text-xs text-fg-muted">{t('templates.editor.hint')}</p>
      ) : (
        <p className="text-sm text-fg-muted">
          {template.is_system
            ? t('templates.editor.systemReadOnly')
            : version.status !== 'draft'
              ? t('templates.editor.readOnly')
              : t('templates.editor.noPermission')}
        </p>
      )}
      {drafts.length === 0 ? (
        <p className="text-sm text-fg-muted">{t('templates.editor.empty')}</p>
      ) : null}
      <SortableList
        items={drafts}
        getId={(d) => d.attribute_definition_id}
        getName={labelOf}
        disabled={!editable}
        onReorder={(next) => {
          setSaved(false);
          setDrafts(next);
        }}
        label={t('templates.editor.itemsLabel', { version: version.version })}
        className="divide-y divide-line"
        renderItem={(draft) => {
          const definition = definitions.get(draft.attribute_definition_id);
          const messages = [
            ...(errors[draft.attribute_definition_id] ?? []),
            ...(itemErrors[draft.key] ? [itemErrors[draft.key]!] : []),
          ];
          return (
            <ItemView
              draft={draft}
              label={labelOf(draft)}
              definition={definition}
              editable={editable}
              messages={messages}
              onChange={(change) => update(draft.attribute_definition_id, change)}
              onRemove={() => {
                setSaved(false);
                setDrafts((list) =>
                  list.filter((d) => d.attribute_definition_id !== draft.attribute_definition_id),
                );
              }}
            />
          );
        }}
      />
      {editable ? (
        <div className="flex flex-wrap items-end gap-2 border-t border-line pt-4">
          <div className="min-w-[14rem]">
            <Select
              label={t('templates.editor.addLabel')}
              value={choice}
              onChange={(e) => setChoice(e.target.value)}
              options={[
                { value: '', label: t('templates.editor.addPlaceholder') },
                ...choosable.map((d) => ({
                  value: d.id,
                  label: t(`templates.editor.scope.${d.scope}`, {
                    name: localizedText(d.display_name, i18n.language) || d.key,
                    key: d.key,
                  }),
                })),
              ]}
            />
          </div>
          <Button variant="secondary" icon={<Plus />} disabled={!choice} onClick={add}>
            {t('templates.editor.add')}
          </Button>
          <div className="flex-1" />
          <Button icon={<Save />} onClick={onSave} disabled={save.isPending}>
            {t('templates.editor.save')}
          </Button>
        </div>
      ) : null}
      {general ? (
        <p role="alert" className="text-sm text-danger">
          {general}
        </p>
      ) : null}
      {saved ? (
        <p role="status" className="text-sm text-green-800 dark:text-green-300">
          {t('templates.editor.saved')}
        </p>
      ) : null}
    </section>
  );
}

function ItemView({
  draft,
  label,
  definition,
  editable,
  messages,
  onChange,
  onRemove,
}: {
  draft: ItemDraft;
  label: string;
  definition: AttributeDefinition | undefined;
  editable: boolean;
  messages: string[];
  onChange: (change: Partial<ItemDraft>) => void;
  onRemove: () => void;
}) {
  const { t } = useTranslation();
  const key = draft.key || definition?.key || draft.attribute_definition_id;
  const dataType = definition?.data_type;
  const options = definition ? optionsOf(definition) : [];
  const errorId = `template-item-error-${draft.attribute_definition_id}`;
  const described = messages.length > 0 ? errorId : undefined;
  const definitionRequired = !!definition?.required;
  const summary = [
    draft.required || definitionRequired ? t('templates.editor.required') : null,
    draft.defaultText ? t('templates.editor.defaultIs', { value: draft.defaultText }) : null,
    draft.options.length > 0
      ? t('templates.editor.optionsAre', { values: draft.options.join(', ') })
      : null,
    draft.min || draft.max
      ? t('templates.editor.rangeIs', { min: draft.min || '…', max: draft.max || '…' })
      : null,
    draft.minLength || draft.maxLength
      ? t('templates.editor.lengthIs', {
          min: draft.minLength || '…',
          max: draft.maxLength || '…',
        })
      : null,
    draft.pattern ? t('templates.editor.patternIs', { pattern: draft.pattern }) : null,
  ].filter(Boolean);

  return (
    <div className="space-y-3 py-1 text-sm" data-item-key={key}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-medium text-fg">{label}</span>
          {label !== key ? <span className="font-mono text-xs text-fg-muted">{key}</span> : null}
          {dataType ? (
            <span className="text-xs text-fg-muted">
              {t(`citypes.dataTypes.${dataType}`, dataType)}
            </span>
          ) : null}
          {definition?.scope === 'global' ? (
            <Badge variant="neutral">{t('templates.editor.global')}</Badge>
          ) : null}
          {definition?.searchable ? (
            <Badge variant="neutral">{t('citypes.flagSearchable')}</Badge>
          ) : null}
          {definition?.unique ? <Badge variant="neutral">{t('citypes.flagUnique')}</Badge> : null}
        </div>
        {editable ? (
          <Button
            variant="ghost"
            size="sm"
            icon={<X />}
            className="text-danger hover:text-danger"
            aria-label={t('templates.editor.removeAction', { name: key })}
            onClick={onRemove}
          >
            {t('templates.editor.remove')}
          </Button>
        ) : null}
      </div>
      {editable ? (
        <div className="grid gap-3 sm:grid-cols-2">
          <label className="flex items-center gap-2 text-fg">
            <input
              type="checkbox"
              checked={draft.required || definitionRequired}
              disabled={definitionRequired}
              aria-describedby={described}
              onChange={(e) => onChange({ required: e.target.checked })}
            />
            {definitionRequired
              ? t('templates.editor.requiredByDefinition')
              : t('templates.editor.requiredOverride', { name: key })}
          </label>
          <Input
            label={t('templates.editor.default', { name: key })}
            value={draft.defaultText}
            aria-describedby={described}
            onChange={(e) => onChange({ defaultText: e.target.value })}
          />
          {options.length > 0 ? (
            <fieldset className="sm:col-span-2">
              <legend className="text-sm font-medium text-fg">
                {t('templates.editor.options', { name: key })}
              </legend>
              <div className="mt-1 flex flex-wrap gap-3">
                {options.map((option) => (
                  <label key={option} className="flex items-center gap-1.5 text-fg">
                    <input
                      type="checkbox"
                      checked={draft.options.includes(option)}
                      onChange={(e) =>
                        onChange({
                          options: e.target.checked
                            ? options.filter((o) => o === option || draft.options.includes(o))
                            : draft.options.filter((o) => o !== option),
                        })
                      }
                    />
                    {option}
                  </label>
                ))}
              </div>
            </fieldset>
          ) : null}
          {dataType && NUMERIC_TYPES.has(dataType) ? (
            <>
              <Input
                label={t('templates.editor.minOf', { name: key })}
                inputMode="decimal"
                value={draft.min}
                onChange={(e) => onChange({ min: e.target.value })}
              />
              <Input
                label={t('templates.editor.maxOf', { name: key })}
                inputMode="decimal"
                value={draft.max}
                onChange={(e) => onChange({ max: e.target.value })}
              />
            </>
          ) : null}
          {dataType && TEXT_TYPES.has(dataType) ? (
            <>
              <Input
                label={t('templates.editor.minLengthOf', { name: key })}
                inputMode="numeric"
                value={draft.minLength}
                onChange={(e) => onChange({ minLength: e.target.value })}
              />
              <Input
                label={t('templates.editor.maxLengthOf', { name: key })}
                inputMode="numeric"
                value={draft.maxLength}
                onChange={(e) => onChange({ maxLength: e.target.value })}
              />
              <Input
                label={t('templates.editor.patternOf', { name: key })}
                value={draft.pattern}
                onChange={(e) => onChange({ pattern: e.target.value })}
              />
            </>
          ) : null}
        </div>
      ) : summary.length > 0 ? (
        <p className="text-xs text-fg-muted">{summary.join(' · ')}</p>
      ) : null}
      {messages.length > 0 ? (
        <ul id={errorId} role="alert" className="text-sm text-danger">
          {messages.map((m) => (
            <li key={m}>{m}</li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}
