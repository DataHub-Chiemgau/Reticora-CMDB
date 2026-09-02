import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { useCIParent } from '../../api/cmdbHooks';
import type { ParentAssetRef } from '../../api/cmdb';
import { Badge } from '../ui/Badge';
import { Card } from '../ui/Card';

/**
 * Displays the shared inventory identity a child CI inherits from its parent
 * asset (spec §5).
 *
 * The CI record deliberately stores none of this data, and the API rejects any
 * attempt to write it on a child, so the parent stays the single source of
 * truth. This card is therefore strictly read-only: it renders values without
 * any edit affordance and points the user at the owning asset instead.
 *
 * A CI without a parent renders nothing at all, so the simple
 * "1 asset ↔ 1 CI" workflow is never burdened with parent/child complexity.
 */
export function CIParentAssetCard({ ciId }: { ciId: string }) {
  const { t } = useTranslation();
  const { data, isLoading, isError } = useCIParent(ciId);

  // A standalone CI has no parent; the query 404s. That is a normal state, not
  // an error worth showing. Guard on the link itself rather than just on
  // `data`, so an unexpected payload degrades to "no parent" instead of
  // taking the whole CI detail page down.
  if (isLoading || isError || !data?.composition) return null;

  const parent = data.parent_asset;
  const rows = parent ? inheritedRows(parent, t) : [];

  return (
    <Card title={t('composition.parentTitle', 'Übergeordnetes Asset')}>
      <div className="space-y-3 text-sm">
        <div className="flex flex-wrap items-center gap-2">
          {parent ? (
            <Link
              to={`/assets?highlight=${data.composition.parent_asset_id}`}
              className="font-medium text-primary underline-offset-2 hover:underline"
            >
              {parent.name || parent.asset_tag || data.composition.parent_asset_id}
            </Link>
          ) : (
            <span className="font-medium text-gray-900 dark:text-gray-100">
              {data.composition.parent_asset_id}
            </span>
          )}
          {data.composition.role && <Badge variant="neutral">{data.composition.role}</Badge>}
          <Badge variant="neutral">{t('composition.readOnly', 'schreibgeschützt')}</Badge>
        </div>

        <p className="text-gray-500 dark:text-gray-400">{t('composition.inheritedHint')}</p>

        {rows.length > 0 && (
          <dl className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            {rows.map(([label, value]) => (
              <div key={label}>
                <dt className="text-gray-500 dark:text-gray-400">{label}</dt>
                <dd className="break-all font-medium text-gray-900 dark:text-gray-100">{value}</dd>
              </div>
            ))}
          </dl>
        )}
      </div>
    </Card>
  );
}

/** Builds the label/value pairs for the inherited fields that are actually set. */
function inheritedRows(
  parent: ParentAssetRef,
  t: ReturnType<typeof useTranslation>['t'],
): Array<[string, string]> {
  const candidates: Array<[string, string | number | undefined]> = [
    [t('asset.assetTag', 'Inventarnummer'), parent.asset_tag],
    [t('ci.serialNumber'), parent.serial_number],
    [t('asset.barcode', 'Barcode'), parent.barcode],
    [t('asset.rfidTag', 'RFID'), parent.rfid_tag],
    [t('asset.purchaseDate', 'Kaufdatum'), parent.purchase_date],
    [
      t('asset.purchaseCost', 'Kaufpreis'),
      parent.purchase_cost != null
        ? `${parent.purchase_cost}${parent.currency ? ` ${parent.currency}` : ''}`
        : undefined,
    ],
    [t('asset.supplier', 'Lieferant'), parent.supplier],
    [t('asset.invoiceNumber', 'Rechnungsnummer'), parent.invoice_number],
    [t('asset.warrantyEnd', 'Garantie bis'), parent.warranty_end],
    [t('asset.location', 'Standort'), parent.location],
    [t('asset.status', 'Status'), parent.status],
  ];

  return candidates
    .filter(([, value]) => value !== undefined && value !== null && value !== '')
    .map(([label, value]) => [label, String(value)]);
}
