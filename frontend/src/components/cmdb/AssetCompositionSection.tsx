/**
 * AssetCompositionSection — parent/child composition view for assets
 * (spec §5, §14). Shows the parent asset's children (CIs/assets) with their
 * independence flags, and the read-only inherited shared fields notice.
 */
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { useCompositionChildren } from '../../api/cmdbHooks';
import { Badge } from '../ui/Badge';
import { SkeletonList } from '../ui/Skeleton';

export function AssetCompositionSection({ assetId }: { assetId: string }) {
  const { t } = useTranslation();
  const children = useCompositionChildren(assetId);
  const rows = children.data?.data ?? [];

  return (
    <div className="space-y-2">
      <h4 className="text-sm font-semibold text-gray-700 dark:text-gray-200">
        {t('composition.title', 'Komponenten (Parent/Child)')}
      </h4>
      {children.isLoading ? <SkeletonList rows={2} label="…" /> : null}
      {rows.length === 0 && !children.isLoading ? (
        <p className="text-xs text-gray-500">
          {t(
            'composition.none',
            'Keine Komponenten. Dieses Asset ist eigenständig (1:1 mit CI oder rein inventarisch).',
          )}
        </p>
      ) : (
        <ul className="space-y-1 text-sm">
          {rows.map((c) => (
            <li key={c.id} className="flex flex-wrap items-center gap-2">
              {c.child_ci_id ? (
                <Link
                  to={`/cmdb/${c.child_ci_id}`}
                  className="font-medium text-primary hover:underline"
                >
                  CI {c.child_ci_id.slice(0, 8)}…
                </Link>
              ) : (
                <span className="font-medium">Asset {c.child_asset_id?.slice(0, 8)}…</span>
              )}
              {c.role ? <Badge variant="neutral">{c.role}</Badge> : null}
              <span className="text-xs text-gray-400">
                {c.configuration_only ? t('composition.configOnly', 'nur Konfiguration') : ''}
                {c.independently_serialized
                  ? ` · ${t('composition.serialized', 'eigen serialisiert')}`
                  : ''}
                {c.independently_locatable
                  ? ` · ${t('composition.locatable', 'eigen lokalisierbar')}`
                  : ''}
              </span>
            </li>
          ))}
        </ul>
      )}
      <p className="text-xs text-gray-400">
        {t(
          'composition.inheritedHint',
          'Geteilte Inventardaten (Seriennummer, Kauf, Garantie, Standort) stammen vom Parent-Asset.',
        )}
      </p>
    </div>
  );
}
