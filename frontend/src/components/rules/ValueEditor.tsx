import { useId, useState, type KeyboardEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { Plus, X } from 'lucide-react';
import { clientApi, localizedText } from '../../api/cmdb';
import { siteApi } from '../../api/client';
import { useCITypes } from '../../api/cmdbHooks';
import { Button, IconButton } from '../ui/Button';
import { Input } from '../ui/Input';
import { Select } from '../ui/Select';
import { LocationPicker } from '../pickers/LocationPicker';
import { ObjectPicker } from '../pickers/ObjectPicker';
import { UserTeamPicker } from '../pickers/UserTeamPicker';
import {
  LIST_OPS,
  type FieldSpec,
  type FilterOp,
  type ValueEditorKind,
} from '../../rules/builderModes';

export interface ValueEditorProps {
  label: string;
  /** spec is the field compared: its kind and editor decide the input. */
  spec: FieldSpec | undefined;
  op: FilterOp;
  value: unknown;
  onChange: (value: unknown) => void;
  error?: string;
}

/** isoToLocal shows an RFC 3339 time in the local time of a datetime-local input. */
function isoToLocal(value: unknown): string {
  if (typeof value !== 'string' || value === '') return '';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '';
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(
    date.getHours(),
  )}:${pad(date.getMinutes())}`;
}

/** localToIso sends the local time of a datetime-local input as RFC 3339 (UTC). */
function localToIso(value: string): string | undefined {
  if (value === '') return undefined;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? undefined : date.toISOString();
}

/** parseNumber sends a number input as a JSON number (integer, decimal). */
function parseNumber(value: string): number | undefined {
  if (value.trim() === '') return undefined;
  const n = Number(value);
  return Number.isFinite(n) ? n : undefined;
}

interface Choice {
  value: string;
  label: string;
}

/** useChoices loads the options of a list editor: CI types, clients, sites, enum values. */
function useChoices(spec: FieldSpec | undefined): Choice[] {
  const { i18n } = useTranslation();
  const editor = spec?.editor;
  const types = useCITypes({ limit: 200 }, { enabled: editor === 'ci_type' });
  const clients = useQuery({
    queryKey: ['clients', 'rule-builder'],
    queryFn: () => clientApi.list(),
    enabled: editor === 'client',
  });
  const sites = useQuery({
    queryKey: ['sites', 'rule-builder'],
    queryFn: () => siteApi.list({ limit: 200 }),
    enabled: editor === 'site',
  });
  switch (editor) {
    case 'ci_type':
      return (types.data?.data ?? []).map((type) => ({
        value: type.key,
        label: localizedText(type.display_name, i18n.language) || type.name || type.key,
      }));
    case 'client':
      return (clients.data?.data ?? []).map((c) => ({ value: c.id, label: c.name }));
    case 'site':
      return (sites.data?.data ?? []).map((s) => ({ value: s.id, label: s.name }));
    case 'enum':
      return (spec?.definition?.enum_values ?? []).map((v) => ({ value: v, label: v }));
  }
  return [];
}

const SELECT_EDITORS: readonly ValueEditorKind[] = ['bool', 'enum', 'ci_type', 'client', 'site'];
const PICKER_EDITORS: readonly ValueEditorKind[] = ['location', 'ci', 'asset', 'user', 'team'];

interface ScalarProps {
  label: string;
  editor: ValueEditorKind;
  choices: Choice[];
  value: unknown;
  onChange: (value: unknown) => void;
  error?: string;
  onEnter?: () => void;
}

/** ScalarInput edits one value of the field: text, number, date, time or a choice. */
function ScalarInput({ label, editor, choices, value, onChange, error, onEnter }: ScalarProps) {
  const { t } = useTranslation();
  const enter = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'Enter' && onEnter) {
      event.preventDefault();
      onEnter();
    }
  };
  switch (editor) {
    case 'number':
      return (
        <Input
          label={label}
          type="number"
          step="any"
          error={error}
          value={typeof value === 'number' ? String(value) : ''}
          onChange={(e) => onChange(parseNumber(e.target.value))}
          onKeyDown={enter}
        />
      );
    case 'date':
      return (
        <Input
          label={label}
          type="date"
          error={error}
          value={typeof value === 'string' ? value.slice(0, 10) : ''}
          onChange={(e) => onChange(e.target.value === '' ? undefined : e.target.value)}
          onKeyDown={enter}
        />
      );
    case 'datetime':
      return (
        <Input
          label={label}
          type="datetime-local"
          error={error}
          value={isoToLocal(value)}
          onChange={(e) => onChange(localToIso(e.target.value))}
          onKeyDown={enter}
        />
      );
    case 'bool':
      return (
        <Select
          label={label}
          error={error}
          value={typeof value === 'boolean' ? String(value) : ''}
          onChange={(e) => onChange(e.target.value === '' ? undefined : e.target.value === 'true')}
          options={[
            { value: '', label: t('rules.builder.chooseValue') },
            { value: 'true', label: t('rules.bool.true') },
            { value: 'false', label: t('rules.bool.false') },
          ]}
        />
      );
    case 'enum':
    case 'ci_type':
    case 'client':
    case 'site': {
      const current = typeof value === 'string' ? value : '';
      const known = current === '' || choices.some((c) => c.value === current);
      return (
        <Select
          label={label}
          error={error}
          value={current}
          onChange={(e) => onChange(e.target.value === '' ? undefined : e.target.value)}
          options={[
            { value: '', label: t('rules.builder.chooseValue') },
            ...(known ? [] : [{ value: current, label: current }]),
            ...choices,
          ]}
        />
      );
    }
    default:
      return (
        <Input
          label={label}
          error={error}
          value={typeof value === 'string' ? value : value === undefined ? '' : String(value)}
          onChange={(e) => onChange(e.target.value === '' ? undefined : e.target.value)}
          onKeyDown={enter}
        />
      );
  }
}

interface PickerProps {
  label: string;
  editor: ValueEditorKind;
  onPick: (id: string, name: string) => void;
}

/** ReferencePicker chooses one object, location, person or team; never a free id. */
function ReferencePicker({ label, editor, onPick }: PickerProps) {
  switch (editor) {
    case 'location':
      return (
        <LocationPicker
          label={label}
          value={null}
          onChange={(picked) => picked && onPick(picked.id, picked.path.join(' / ') || picked.name)}
        />
      );
    case 'ci':
    case 'asset':
      return (
        <ObjectPicker
          label={label}
          kinds={[editor]}
          value={[]}
          onChange={(picked) => {
            const first = picked[0];
            if (first) onPick(first.id, first.name);
          }}
        />
      );
    case 'user':
    case 'team':
      return (
        <UserTeamPicker
          label={label}
          kinds={[editor]}
          value={null}
          onChange={(picked) => picked && onPick(picked.id, picked.name)}
        />
      );
  }
  return null;
}

/** Chips lists the values of a list operator, each with a remove button. */
function Chips({
  label,
  values,
  display,
  onRemove,
}: {
  label: string;
  values: unknown[];
  display: (value: unknown) => string;
  onRemove: (index: number) => void;
}) {
  const { t } = useTranslation();
  if (values.length === 0) return null;
  return (
    <ul aria-label={label} className="flex flex-wrap gap-2">
      {values.map((v, i) => (
        <li
          key={`${String(v)}-${i}`}
          className="flex items-center gap-1 rounded-lg border border-line bg-panel py-0.5 pl-3 pr-1 text-sm text-fg"
        >
          <span className="[overflow-wrap:anywhere]">{display(v)}</span>
          <IconButton
            size="sm"
            icon={<X />}
            label={t('rules.builder.removeValue', { value: display(v) })}
            onClick={() => onRemove(i)}
          />
        </li>
      ))}
    </ul>
  );
}

/**
 * ValueEditor edits the value of a comparison by the kind and the data type
 * of its field (WP-315): text, number (sent as a JSON number), date and
 * time (sent as ISO 8601), boolean, the enum values of a definition, CI
 * types, clients and sites from their lists, locations, CIs, assets,
 * persons and teams through the pickers of WP-318, tag keys, and the lists
 * of in, between, has_any and has_all. exists takes set (true) or empty
 * (false). There is no free id input. It needs no RuleBuilder; WP-322 uses
 * it in the mapping.
 */
export function ValueEditor({ label, spec, op, value, onChange, error }: ValueEditorProps) {
  const { t } = useTranslation();
  const choices = useChoices(spec);
  const [names, setNames] = useState<Record<string, string>>({});
  const [draft, setDraft] = useState<unknown>(undefined);
  const hintId = useId();
  const editor: ValueEditorKind = spec?.editor ?? 'text';

  if (!spec) return null;

  if (op === 'exists') {
    return (
      <Select
        label={label}
        error={error}
        value={value === false ? 'false' : 'true'}
        onChange={(e) => onChange(e.target.value === 'true')}
        options={[
          { value: 'true', label: t('rules.exists.true') },
          { value: 'false', label: t('rules.exists.false') },
        ]}
      />
    );
  }

  const choiceLabel = (v: unknown) =>
    choices.find((c) => c.value === v)?.label ??
    (typeof v === 'string' ? names[v] : undefined) ??
    (typeof v === 'boolean' ? t(`rules.bool.${v}`) : String(v));
  const pick = (id: string, name: string) => setNames((n) => ({ ...n, [id]: name }));

  if (op === 'between') {
    const pair = Array.isArray(value) ? value : [];
    const set = (i: number, v: unknown) => {
      const next = [pair[0], pair[1]];
      next[i] = v;
      onChange(next);
    };
    return (
      <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
        <ScalarInput
          label={t('rules.builder.valueFrom', { label })}
          editor={editor}
          choices={choices}
          value={pair[0]}
          onChange={(v) => set(0, v)}
          error={error}
        />
        <ScalarInput
          label={t('rules.builder.valueTo', { label })}
          editor={editor}
          choices={choices}
          value={pair[1]}
          onChange={(v) => set(1, v)}
        />
      </div>
    );
  }

  if (LIST_OPS.includes(op)) {
    const list = Array.isArray(value) ? value : [];
    const append = (v: unknown) => {
      if (v === undefined || v === '' || list.includes(v)) return;
      onChange([...list, v]);
    };
    const remove = (i: number) => onChange(list.filter((_, j) => j !== i));
    const chips = (
      <Chips
        label={t('rules.builder.values', { label })}
        values={list}
        display={choiceLabel}
        onRemove={remove}
      />
    );
    if (PICKER_EDITORS.includes(editor)) {
      return (
        <div className="space-y-2">
          {chips}
          <ReferencePicker
            label={t('rules.builder.addValueOf', { label })}
            editor={editor}
            onPick={(id, name) => {
              pick(id, name);
              append(id);
            }}
          />
          {error ? <p className="text-xs text-danger">{error}</p> : null}
        </div>
      );
    }
    if (SELECT_EDITORS.includes(editor)) {
      return (
        <div className="space-y-2">
          {chips}
          <ScalarInput
            label={t('rules.builder.addValueOf', { label })}
            editor={editor}
            choices={choices.filter((c) => !list.includes(c.value))}
            value={undefined}
            onChange={append}
            error={error}
          />
        </div>
      );
    }
    const add = () => {
      append(draft);
      setDraft(undefined);
    };
    return (
      <div className="space-y-2">
        {chips}
        <div className="flex items-end gap-2">
          <div className="min-w-0 flex-1">
            <ScalarInput
              label={t('rules.builder.addValueOf', { label })}
              editor={editor === 'tags' ? 'text' : editor}
              choices={choices}
              value={draft}
              onChange={setDraft}
              onEnter={add}
              error={error}
            />
          </div>
          <Button variant="secondary" icon={<Plus />} onClick={add} disabled={draft === undefined}>
            {t('rules.builder.addValue')}
          </Button>
        </div>
      </div>
    );
  }

  if (PICKER_EDITORS.includes(editor)) {
    const id = typeof value === 'string' ? value : '';
    return (
      <div className="space-y-2">
        {id ? (
          <Chips
            label={label}
            values={[id]}
            display={choiceLabel}
            onRemove={() => onChange(undefined)}
          />
        ) : null}
        <ReferencePicker
          label={label}
          editor={editor}
          onPick={(picked, name) => {
            pick(picked, name);
            onChange(picked);
          }}
        />
        {error ? <p className="text-xs text-danger">{error}</p> : null}
      </div>
    );
  }

  return (
    <div className="space-y-1">
      <ScalarInput
        label={label}
        editor={editor}
        choices={choices}
        value={value}
        onChange={onChange}
        error={error}
      />
      {op === 'like' ? (
        <p id={hintId} className="text-xs text-fg-muted">
          {t('rules.builder.likeHint')}
        </p>
      ) : null}
    </div>
  );
}
