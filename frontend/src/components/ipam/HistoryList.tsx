/**
 * HistoryList shows the IPAM change history of an address (GET
 * /ips/{id}/history) or of a subnet (GET /subnets/{id}/history), newest
 * first (IPM-08, IPM-19, WP-917): time, actor (the user surrogate, System
 * without user), source, field and old → new, one page of
 * IPAM_HISTORY_PAGE rows with previous and next.
 */
import type { TFunction } from 'i18next';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { fieldValueText } from '../cmdb/fieldValue';
import { IPAM_HISTORY_PAGE, type IPAMChange } from '../../api/cmdb';
import { useIPAMHistory, type IPAMHistoryKind } from '../../api/ipamHooks';
import { ScrollRegion } from '../ScrollRegion';
import { Button } from '../ui/Button';
import { tableClasses, tdClasses, thClasses, theadClasses, trClasses } from '../ui/table';

/** The catalog group of each IPAM field whose values are catalog keys (GLO-10). */
const IPAM_CATALOG_FIELDS: Readonly<Record<string, string>> = {
  state: 'ipam.states',
  type: 'ipam.types',
  role: 'ipam.roles',
  origin: 'ipam.origins',
};

/**
 * ipamValueText renders a value of the history (JSON) as short text: an
 * enum value of the address through the catalog, booleans and objects as
 * text.
 */
function ipamValueText(t: TFunction, field: string | undefined, value: unknown): string {
  if (value === undefined || value === null || value === '') return '–';
  const group = field ? IPAM_CATALOG_FIELDS[field] : undefined;
  if (group && typeof value === 'string') return t(`${group}.${value}`, value);
  return typeof value === 'object' ? JSON.stringify(value) : String(value);
}

export function HistoryList({
  kind,
  id,
  enabled = true,
}: {
  kind: IPAMHistoryKind;
  id: string;
  /** enabled holds the request back, e.g. while its tab is hidden. */
  enabled?: boolean;
}) {
  const { t, i18n } = useTranslation();
  const [offset, setOffset] = useState(0);
  const history = useIPAMHistory(kind, id, offset, enabled);
  const rows: IPAMChange[] = history.data?.data ?? [];
  const total = history.data?.total ?? rows.length;

  if (history.isError) {
    return (
      <p role="alert" className="text-sm text-red-700 dark:text-red-300">
        {t('ipam.history.loadFailed')}
      </p>
    );
  }
  if (rows.length === 0) {
    return (
      <p className="text-sm text-fg-muted">
        {history.isPending ? t('ipam.detail.loading') : t('ipam.history.empty')}
      </p>
    );
  }
  return (
    <div className="space-y-2">
      <ScrollRegion label={t('ipam.history.label')}>
        <table className={tableClasses}>
          <thead className={theadClasses}>
            <tr>
              <th scope="col" className={thClasses}>
                {t('ipam.history.at')}
              </th>
              <th scope="col" className={thClasses}>
                {t('ipam.history.actor')}
              </th>
              <th scope="col" className={thClasses}>
                {t('ipam.history.source')}
              </th>
              <th scope="col" className={thClasses}>
                {t('ipam.history.field')}
              </th>
              <th scope="col" className={thClasses}>
                {t('ipam.history.change')}
              </th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.id} className={trClasses} data-testid={`history-row-${row.id}`}>
                <td className={tdClasses}>
                  <time dateTime={row.changed_at} className="whitespace-nowrap tabular-nums">
                    {new Date(row.changed_at).toLocaleString(i18n.language)}
                  </time>
                </td>
                <td className={tdClasses}>
                  <span className="font-mono text-xs [overflow-wrap:anywhere]">
                    {row.actor_id ?? t('ipam.history.system')}
                  </span>
                </td>
                <td className={tdClasses}>{t(`ipam.history.sources.${row.source}`)}</td>
                <td className={tdClasses}>
                  {row.field_name ?? t(`ipam.history.changeTypes.${row.change_type}`)}
                </td>
                <td className={tdClasses}>
                  {row.change_type === 'update' ? (
                    <span className="[overflow-wrap:anywhere]">
                      {fieldValueText(t, row.field_name ?? '', row.old_value, (v) =>
                        ipamValueText(t, row.field_name, v),
                      )}{' '}
                      →{' '}
                      {fieldValueText(t, row.field_name ?? '', row.new_value, (v) =>
                        ipamValueText(t, row.field_name, v),
                      )}
                    </span>
                  ) : (
                    t(`ipam.history.changeTypes.${row.change_type}`)
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </ScrollRegion>
      {(offset > 0 || history.data?.has_more) && (
        <div className="flex items-center justify-end gap-2">
          <Button
            variant="secondary"
            size="sm"
            disabled={offset === 0}
            onClick={() => setOffset(Math.max(0, offset - IPAM_HISTORY_PAGE))}
          >
            {t('ipam.history.previous')}
          </Button>
          <span className="text-xs tabular-nums text-fg-muted">
            {t('ipam.history.range', { from: offset + 1, to: offset + rows.length, total })}
          </span>
          <Button
            variant="secondary"
            size="sm"
            disabled={!history.data?.has_more}
            onClick={() => setOffset(offset + IPAM_HISTORY_PAGE)}
          >
            {t('ipam.history.next')}
          </Button>
        </div>
      )}
    </div>
  );
}
