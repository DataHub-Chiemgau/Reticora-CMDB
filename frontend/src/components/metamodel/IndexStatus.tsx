/**
 * IndexStatus shows the index status of an attribute definition (DB-04,
 * UI-11, WP-796): the state of the newest index job of the definition, or of
 * the index of its key, as GET /attribute-definitions delivers it in
 * `index_status`. A failed attempt with attempts left is queued with its
 * error ("erneuter Versuch n/max"); a failed or cancelled index offers the
 * rebuild (POST /attribute-definitions/{id}/reindex). The list that renders
 * it polls while a status is queued or running (useApplicableAttributeDefinitions).
 */
import { useTranslation } from 'react-i18next';
import type { AttributeIndexStatus } from '../../api/cmdb';
import { RotateCw } from 'lucide-react';
import { Badge } from '../ui/Badge';
import { Button } from '../ui/Button';

type Variant = 'success' | 'warning' | 'danger' | 'neutral' | 'info';

const VARIANTS: Record<AttributeIndexStatus['state'], Variant> = {
  none: 'neutral',
  queued: 'info',
  running: 'info',
  succeeded: 'success',
  failed: 'danger',
  cancelled: 'warning',
};

export interface IndexStatusProps {
  status: AttributeIndexStatus | undefined;
  /** The key of the attribute, for the accessible name of the rebuild button. */
  attributeKey: string;
  /** Offered at failed and cancelled; none: no rebuild (read-only or no permission). */
  onReindex?: () => void;
  reindexing?: boolean;
}

export function IndexStatus({ status, attributeKey, onReindex, reindexing }: IndexStatusProps) {
  const { t } = useTranslation();
  if (!status) return null;
  const { state } = status;
  const retry = state === 'queued' && !!status.error && status.attempts > 0;
  const label =
    state === 'none'
      ? t('citypes.indexStatus.none')
      : retry
        ? t('citypes.indexStatus.retry', {
            attempt: status.attempts,
            max: status.max_attempts,
          })
        : t(`jobs.statuses.${state}`, state);
  const showError = (state === 'failed' || retry) && !!status.error;
  const rebuild = !!onReindex && (state === 'failed' || state === 'cancelled');
  return (
    <span className="inline-flex flex-wrap items-center gap-2">
      <span role="status">
        <Badge variant={VARIANTS[state]}>{t('citypes.indexStatus.label', { status: label })}</Badge>
      </span>
      {showError ? (
        <span className="text-xs text-danger">
          {t('citypes.indexStatus.error', { error: status.error })}
        </span>
      ) : null}
      {rebuild ? (
        <Button
          variant="ghost"
          size="sm"
          icon={<RotateCw />}
          aria-label={t('citypes.indexStatus.reindexAction', { name: attributeKey })}
          loading={reindexing}
          onClick={onReindex}
        >
          {t('citypes.indexStatus.reindex')}
        </Button>
      ) : null}
    </span>
  );
}
