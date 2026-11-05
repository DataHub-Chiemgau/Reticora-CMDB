/**
 * MetamodelDiff compares two keyed lists of the metamodel (UI-11, WP-314):
 * the entries added, removed and changed, each with the old and the new
 * value per field. It is generic: the template versions of TemplatesPage
 * compare their items, the patch list of WP-799 its draft items. Values are
 * compared as JSON; a caller names the fields and may format their values.
 */
import { useTranslation } from 'react-i18next';
import { Badge } from '../ui/Badge';
import { ScrollRegion } from '../ScrollRegion';
import {
  tableClasses,
  tdClasses,
  tdMonoClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../ui/table';
import { cn } from '../ui/utils';

/** One entry of a list: its key, a display name and its values per field. */
export interface DiffEntry {
  key: string;
  label?: string;
  values: Record<string, unknown>;
}

export type DiffChange = 'added' | 'removed' | 'changed';

/** The changes of the entries and fields between two lists. */
export const DIFF_CHANGES: readonly DiffChange[] = ['added', 'removed', 'changed'];

export interface DiffField {
  field: string;
  before: unknown;
  after: unknown;
}

export interface DiffRow {
  key: string;
  label: string;
  change: DiffChange;
  fields: DiffField[];
}

/** same compares two values as JSON; absent and null are equal. */
function same(a: unknown, b: unknown): boolean {
  return JSON.stringify(a ?? null) === JSON.stringify(b ?? null);
}

/**
 * diffEntries lists the changes from `before` to `after` in the order of
 * `after`, the removed entries last. An added entry lists its set fields
 * with their new value, a removed one its former values, a changed one only
 * the fields whose value differs. Unchanged entries are left out.
 */
export function diffEntries(before: DiffEntry[], after: DiffEntry[]): DiffRow[] {
  const old = new Map(before.map((e) => [e.key, e]));
  const next = new Set(after.map((e) => e.key));
  const fieldsOf = (a: DiffEntry | undefined, b: DiffEntry | undefined) => [
    ...new Set([...Object.keys(a?.values ?? {}), ...Object.keys(b?.values ?? {})]),
  ];
  const rows: DiffRow[] = [];
  for (const entry of after) {
    const prior = old.get(entry.key);
    const label = entry.label ?? entry.key;
    if (!prior) {
      const fields = fieldsOf(undefined, entry)
        .filter((f) => entry.values[f] !== undefined && entry.values[f] !== null)
        .map((field) => ({ field, before: undefined, after: entry.values[field] }));
      rows.push({ key: entry.key, label, change: 'added', fields });
      continue;
    }
    const fields = fieldsOf(prior, entry)
      .filter((f) => !same(prior.values[f], entry.values[f]))
      .map((field) => ({ field, before: prior.values[field], after: entry.values[field] }));
    if (fields.length > 0) rows.push({ key: entry.key, label, change: 'changed', fields });
  }
  for (const entry of before) {
    if (next.has(entry.key)) continue;
    const fields = fieldsOf(entry, undefined)
      .filter((f) => entry.values[f] !== undefined && entry.values[f] !== null)
      .map((field) => ({ field, before: entry.values[field], after: undefined }));
    rows.push({ key: entry.key, label: entry.label ?? entry.key, change: 'removed', fields });
  }
  return rows;
}

const CHANGE_VARIANT: Record<DiffChange, 'success' | 'danger' | 'warning'> = {
  added: 'success',
  removed: 'danger',
  changed: 'warning',
};

/** formatDiffValue renders a value: text as it is, everything else as JSON. */
export function formatDiffValue(value: unknown): string {
  if (value === undefined || value === null) return '';
  return typeof value === 'string' ? value : JSON.stringify(value);
}

export interface MetamodelDiffProps {
  before: DiffEntry[];
  after: DiffEntry[];
  /** The accessible name of the table. */
  caption: string;
  /** The label of a field; the field name by default. */
  fieldLabel?: (field: string) => string;
  /** The text of a value; formatDiffValue by default. */
  formatValue?: (field: string, value: unknown) => string;
}

export function MetamodelDiff({
  before,
  after,
  caption,
  fieldLabel = (field) => field,
  formatValue = (_field, value) => formatDiffValue(value),
}: MetamodelDiffProps) {
  const { t } = useTranslation();
  const rows = diffEntries(before, after);
  if (rows.length === 0) {
    return <p className="text-sm text-fg-muted">{t('templates.diff.none')}</p>;
  }
  const empty = t('templates.diff.empty');
  return (
    <ScrollRegion label={caption} className="rounded-lg border border-line">
      <table className={tableClasses}>
        <caption className="sr-only">{caption}</caption>
        <thead className={theadClasses}>
          <tr className="text-fg-muted">
            <th scope="col" className={thClasses}>
              {t('templates.diff.entry')}
            </th>
            <th scope="col" className={thClasses}>
              {t('templates.diff.change')}
            </th>
            <th scope="col" className={thClasses}>
              {t('templates.diff.field')}
            </th>
            <th scope="col" className={thClasses}>
              {t('templates.diff.before')}
            </th>
            <th scope="col" className={thClasses}>
              {t('templates.diff.after')}
            </th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => {
            const fields = row.fields.length > 0 ? row.fields : [null];
            return fields.map((f, i) => (
              <tr
                key={`${row.key}-${f?.field ?? ''}`}
                data-change={row.change}
                className={trClasses}
              >
                {i === 0 ? (
                  <>
                    <th
                      scope="row"
                      rowSpan={fields.length}
                      className={cn(tdPrimaryClasses, 'text-left align-top')}
                    >
                      {row.label}
                    </th>
                    <td rowSpan={fields.length} className={cn(tdClasses, 'align-top')}>
                      <Badge variant={CHANGE_VARIANT[row.change]}>
                        {t(`templates.diff.changes.${row.change}`)}
                      </Badge>
                    </td>
                  </>
                ) : null}
                <td className={tdClasses}>{f ? fieldLabel(f.field) : ''}</td>
                <td className={cn(tdMonoClasses, 'break-all')}>
                  {f && row.change !== 'added' ? formatValue(f.field, f.before) || empty : ''}
                </td>
                <td className={cn(tdMonoClasses, 'break-all')}>
                  {f && row.change !== 'removed' ? formatValue(f.field, f.after) || empty : ''}
                </td>
              </tr>
            ));
          })}
        </tbody>
      </table>
    </ScrollRegion>
  );
}
