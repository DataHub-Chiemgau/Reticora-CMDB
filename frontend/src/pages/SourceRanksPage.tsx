/**
 * SourceRanksPage is the page /source-ranks (REC-04, WP-1115): the rank of
 * every automation source per attribute as a matrix attribute x source.
 * The rows are the attributes of GET /api/v1/reconciliation/source-ranks
 * (the stored entries of the organization, plus attributes added here),
 * the columns the automation sources of WP-1105; manual stays fixed at
 * 100. Each cell shows the effective rank and its origin: System (the
 * REC-03 table, origin default), Org (organization-wide entry) or Typ
 * (entry of the chosen CI type). The type selector (empty: organization-
 * wide) loads the ranks of a CI type.
 *
 * With reconciliation:manage a cell is edited with Enter (an integer 1-99,
 * checked before anything is sent), Escape discards; "Zurücksetzen"
 * removes the entry of the current level, the cell then shows the
 * inherited rank. "Speichern" sends the complete list of the organization's
 * entries with the changes (PUT replaces every entry, WP-1105 AK4), so the
 * entries of other CI types stay. A 422 shows its message at the cell of
 * the entry it names (entries[i].field); the other changes stay. Without
 * the right the page only reads.
 */
import { RotateCcw, Scale } from 'lucide-react';
import { useMemo, useState, type FormEvent, type KeyboardEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { localizedText } from '../api/cmdb';
import { useAllCITypes } from '../api/cmdbHooks';
import {
  ApiError,
  RANK_SOURCES,
  type RankSource,
  type SourceRankEffective,
  type SourceRankEntry,
} from '../api/client';
import { useHasPermission, useReplaceSourceRanks, useSourceRanks } from '../api/hooks';
import { Badge } from '../components/ui/Badge';
import { Button, IconButton } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import {
  tableClasses,
  tdClasses,
  tdPrimaryClasses,
  thClasses,
  theadClasses,
} from '../components/ui/table';

/** The REC-03 rank table of the server (override.SourceRanks), origin default. */
export const DEFAULT_RANKS: Record<RankSource, number> = {
  import: 95,
  workflow: 92,
  redfish: 90,
  ipmi: 90,
  agent: 85,
  snmp: 80,
  api: 75,
  integration: 75,
  wmi: 70,
  ssh: 60,
  nas: 60,
  sweep: 20,
};

/** MANUAL_RANK is the fixed rank of manual values. */
const MANUAL_RANK = 100;

type Origin = 'type' | 'org' | 'default';

interface Cell {
  rank: number;
  origin: Origin;
}

/** cellKey names an entry: CI type ('' organization-wide), attribute, source. */
function cellKey(typeId: string, attribute: string, source: string): string {
  return `${typeId}\u0000${attribute}\u0000${source}`;
}

function entryKey(entry: SourceRankEntry): string {
  return cellKey(entry.ci_type_id ?? '', entry.attribute_key, entry.source);
}

/**
 * applyChanges merges the local changes into the stored entries: a number
 * sets the entry, null removes it. The order of the stored entries stays,
 * new entries follow.
 */
export function applyChanges(
  stored: readonly SourceRankEntry[],
  changes: ReadonlyMap<string, number | null>,
): SourceRankEntry[] {
  const out: SourceRankEntry[] = [];
  const seen = new Set<string>();
  for (const entry of stored) {
    const key = entryKey(entry);
    seen.add(key);
    const change = changes.get(key);
    if (change === null) continue;
    const base: SourceRankEntry = {
      attribute_key: entry.attribute_key,
      source: entry.source,
      rank: change ?? entry.rank,
    };
    out.push(entry.ci_type_id ? { ...base, ci_type_id: entry.ci_type_id } : base);
  }
  for (const [key, rank] of changes) {
    if (rank === null || seen.has(key)) continue;
    const [typeId = '', attribute = '', source = ''] = key.split('\u0000');
    const entry: SourceRankEntry = { attribute_key: attribute, source: source as RankSource, rank };
    out.push(typeId ? { ...entry, ci_type_id: typeId } : entry);
  }
  return out;
}

/** resolve is the effective rank of a source like the server: type, org, default. */
function resolve(
  entries: ReadonlyMap<string, number>,
  typeId: string,
  attribute: string,
  source: RankSource,
): Cell {
  if (typeId) {
    const rank = entries.get(cellKey(typeId, attribute, source));
    if (rank !== undefined) return { rank, origin: 'type' };
  }
  const org = entries.get(cellKey('', attribute, source));
  if (org !== undefined) return { rank: org, origin: 'org' };
  return { rank: DEFAULT_RANKS[source], origin: 'default' };
}

/** parseRank accepts an integer 1..99. */
function parseRank(text: string): number | undefined {
  if (!/^\s*\d+\s*$/.test(text)) return undefined;
  const rank = Number(text);
  return rank >= 1 && rank <= 99 ? rank : undefined;
}

const originVariant: Record<Origin, 'neutral' | 'info' | 'success'> = {
  default: 'neutral',
  org: 'info',
  type: 'success',
};

export function SourceRanksPage() {
  const { t, i18n } = useTranslation();
  const canManage = useHasPermission('reconciliation:manage') === true;
  const [typeId, setTypeId] = useState('');
  const [added, setAdded] = useState<string[]>([]);
  const [newAttribute, setNewAttribute] = useState('');
  const [changes, setChanges] = useState<Map<string, number | null>>(new Map());
  const [editing, setEditing] = useState<{ key: string; text: string } | undefined>();
  const [cellErrors, setCellErrors] = useState<Map<string, string>>(new Map());
  const [saveError, setSaveError] = useState<string | undefined>();
  const [saved, setSaved] = useState(false);

  const types = useAllCITypes();
  const stored = useSourceRanks();
  const storedEntries = useMemo(() => stored.data?.entries ?? [], [stored.data]);

  // The rows: the attributes of the stored and changed entries that apply
  // at the chosen level (organization-wide and of the chosen type), plus
  // added ones; a reset entry keeps its row with the inherited rank.
  const attributes = useMemo(() => {
    const keys = new Set<string>(added);
    for (const entry of [...storedEntries, ...applyChanges(storedEntries, changes)]) {
      if (!entry.ci_type_id || entry.ci_type_id === typeId) keys.add(entry.attribute_key);
    }
    return [...keys].sort((a, b) => a.localeCompare(b));
  }, [storedEntries, changes, added, typeId]);

  const effectiveQuery = useSourceRanks(
    { ci_type_id: typeId || undefined, attribute_keys: attributes },
    attributes.length > 0,
  );
  const effective = useMemo(() => {
    const out = new Map<string, Cell>();
    for (const table of (effectiveQuery.data?.effective ?? []) as SourceRankEffective[]) {
      for (const r of table.ranks) {
        out.set(cellKey(typeId, table.attribute_key, r.source), {
          rank: r.rank,
          origin: r.origin as Origin,
        });
      }
    }
    return out;
  }, [effectiveQuery.data, typeId]);

  const merged = useMemo(() => {
    const out = new Map<string, number>();
    for (const entry of applyChanges(storedEntries, changes)) out.set(entryKey(entry), entry.rank);
    return out;
  }, [storedEntries, changes]);

  const replace = useReplaceSourceRanks();

  // The cell of the server unless a local change touches it; then the
  // rank resolved like the server from the changed entries.
  const cellOf = (attribute: string, source: RankSource): Cell => {
    const touched =
      changes.has(cellKey(typeId, attribute, source)) ||
      changes.has(cellKey('', attribute, source));
    const server = effective.get(cellKey(typeId, attribute, source));
    if (!touched && server) return server;
    return resolve(merged, typeId, attribute, source);
  };

  const sourceName = (source: string) => t(`sourceRanks.sources.${source}`, source);
  const originName = (origin: Origin) => t(`sourceRanks.origins.${origin}`);

  const commit = (key: string, text: string) => {
    const rank = parseRank(text);
    if (rank === undefined) {
      setCellErrors((prev) => new Map(prev).set(key, t('sourceRanks.rankInvalid')));
      return;
    }
    setChanges((prev) => new Map(prev).set(key, rank));
    setCellErrors((prev) => {
      const next = new Map(prev);
      next.delete(key);
      return next;
    });
    setEditing(undefined);
    setSaved(false);
  };

  const reset = (key: string) => {
    setChanges((prev) => {
      const next = new Map(prev);
      if (storedEntries.some((entry) => entryKey(entry) === key)) next.set(key, null);
      else next.delete(key);
      return next;
    });
    setSaved(false);
  };

  const save = async () => {
    const entries = applyChanges(storedEntries, changes);
    setSaveError(undefined);
    setCellErrors(new Map());
    try {
      await replace.mutateAsync(entries);
      setChanges(new Map());
      setSaved(true);
    } catch (err) {
      const match = err instanceof ApiError ? /^entries\[(\d+)\]/.exec(err.field ?? '') : null;
      const entry = match ? entries[Number(match[1])] : undefined;
      const message =
        err instanceof ApiError ? (err.detail ?? err.message) : t('sourceRanks.saveFailed');
      if (entry && (entry.ci_type_id ?? '') === typeId) {
        setCellErrors(new Map([[entryKey(entry), message]]));
      } else {
        setSaveError(message);
      }
    }
  };

  const addAttribute = (event: FormEvent) => {
    event.preventDefault();
    const key = newAttribute.trim();
    if (key && !attributes.includes(key)) setAdded((prev) => [...prev, key]);
    setNewAttribute('');
  };

  const onCellKey = (event: KeyboardEvent<HTMLInputElement>, key: string) => {
    if (event.key === 'Enter') {
      event.preventDefault();
      commit(key, event.currentTarget.value);
    } else if (event.key === 'Escape') {
      event.preventDefault();
      setEditing(undefined);
      setCellErrors((prev) => {
        const next = new Map(prev);
        next.delete(key);
        return next;
      });
    }
  };

  const typeOptions = [
    { value: '', label: t('sourceRanks.orgWide') },
    ...(types.data ?? []).map((type) => ({
      value: type.id,
      label: localizedText(type.display_name, i18n.language) || type.name,
    })),
  ];
  const level: Origin = typeId ? 'type' : 'org';

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('sourceRanks.title')}
        description={t('sourceRanks.description')}
        actions={
          canManage ? (
            <Button
              onClick={() => void save()}
              disabled={changes.size === 0}
              loading={replace.isPending}
            >
              {t('sourceRanks.save')}
            </Button>
          ) : undefined
        }
      />
      <Card>
        <div className="flex flex-wrap items-end gap-4">
          <div className="w-72 max-w-full">
            <Select
              label={t('sourceRanks.ciType')}
              options={typeOptions}
              value={typeId}
              onChange={(event) => {
                setTypeId(event.target.value);
                setEditing(undefined);
                setCellErrors(new Map());
              }}
            />
          </div>
          {canManage ? (
            <form className="flex items-end gap-2" onSubmit={addAttribute}>
              <Input
                label={t('sourceRanks.attribute')}
                value={newAttribute}
                onChange={(event) => setNewAttribute(event.target.value)}
              />
              <Button type="submit" variant="secondary" disabled={!newAttribute.trim()}>
                {t('sourceRanks.addAttribute')}
              </Button>
            </form>
          ) : null}
        </div>
        <p className="mt-3 text-sm text-fg-muted">
          {canManage ? t('sourceRanks.hintEdit') : t('sourceRanks.readOnly')}
        </p>
        {saveError ? (
          <p role="alert" className="mt-3 text-sm text-red-700 dark:text-red-300">
            {saveError}
          </p>
        ) : null}
        {saved ? (
          <p role="status" className="mt-3 text-sm text-fg-muted">
            {t('sourceRanks.saved')}
          </p>
        ) : null}
      </Card>
      {stored.isError ? (
        <ErrorState title={t('sourceRanks.loadFailed')} onRetry={() => void stored.refetch()} />
      ) : attributes.length === 0 ? (
        stored.isPending ? null : (
          <EmptyState
            icon={<Scale className="h-6 w-6" />}
            title={t('sourceRanks.empty')}
            description={t('sourceRanks.emptyHint')}
          />
        )
      ) : (
        <div className="overflow-x-auto rounded-xl border border-line bg-panel">
          <table className={tableClasses}>
            <caption className="sr-only">{t('sourceRanks.caption')}</caption>
            <thead className={theadClasses}>
              <tr>
                <th scope="col" className={thClasses}>
                  {t('sourceRanks.attribute')}
                </th>
                <th scope="col" className={thClasses}>
                  <span>{t('sourceRanks.sources.manual')}</span>
                  <span className="block text-xs font-normal text-fg-muted">
                    {t('sourceRanks.manualHint')}
                  </span>
                </th>
                {RANK_SOURCES.map((source) => (
                  <th key={source} scope="col" className={thClasses}>
                    {sourceName(source)}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {attributes.map((attribute) => (
                <tr key={attribute}>
                  <th scope="row" className={tdPrimaryClasses}>
                    <span className="font-mono text-xs">{attribute}</span>
                  </th>
                  <td className={tdClasses} data-cell={`${attribute}/manual`}>
                    <span className="tabular-nums">{MANUAL_RANK}</span>
                  </td>
                  {RANK_SOURCES.map((source) => {
                    const key = cellKey(typeId, attribute, source);
                    const cell = cellOf(attribute, source);
                    const error = cellErrors.get(key);
                    const label = t('sourceRanks.cellLabel', {
                      attribute,
                      source: sourceName(source),
                    });
                    return (
                      <td key={source} className={tdClasses} data-cell={`${attribute}/${source}`}>
                        {editing?.key === key ? (
                          <Input
                            aria-label={label}
                            type="text"
                            inputMode="numeric"
                            className="w-16"
                            autoFocus
                            value={editing.text}
                            error={error}
                            onChange={(event) => setEditing({ key, text: event.target.value })}
                            onKeyDown={(event) => onCellKey(event, key)}
                          />
                        ) : (
                          <div className="flex items-center gap-1.5 whitespace-nowrap">
                            {canManage ? (
                              <button
                                type="button"
                                className="rounded px-1 tabular-nums text-fg underline-offset-2 hover:underline focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent"
                                aria-label={`${label}: ${cell.rank} (${originName(cell.origin)})`}
                                onClick={() => setEditing({ key, text: String(cell.rank) })}
                              >
                                {cell.rank}
                              </button>
                            ) : (
                              <span className="tabular-nums text-fg">{cell.rank}</span>
                            )}
                            <Badge variant={originVariant[cell.origin]} dot={false}>
                              {originName(cell.origin)}
                            </Badge>
                            {canManage && cell.origin === level ? (
                              <IconButton
                                size="sm"
                                label={t('sourceRanks.reset', {
                                  attribute,
                                  source: sourceName(source),
                                })}
                                icon={<RotateCcw className="h-3.5 w-3.5" />}
                                onClick={() => reset(key)}
                              />
                            ) : null}
                          </div>
                        )}
                        {error && editing?.key !== key ? (
                          <p role="alert" className="mt-1 text-xs text-red-700 dark:text-red-300">
                            {error}
                          </p>
                        ) : null}
                      </td>
                    );
                  })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

export default SourceRanksPage;
