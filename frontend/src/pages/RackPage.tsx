import { useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import type { CI, RackMount } from '../api/client';
import { useCIList, useRackList, useRackMounts } from '../api/hooks';
import { RackSVG } from '../components/rack/RackSVG';
import type { RackUnit } from '../components/rack/RackSVG';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';

/**
 * Maps rack mounts onto the units drawn by `RackSVG`. Mounts are labelled with
 * the CI name when the CI is known and fall back to the raw identifier, so a CI
 * that is not part of the loaded page is still visible in its slot.
 */
export function buildRackUnits(mounts: RackMount[], cisById: Map<string, CI>): RackUnit[] {
  return mounts.map((mount) => ({
    id: mount.id,
    position: mount.position_u,
    height: Math.max(1, mount.height_u),
    label: cisById.get(mount.ci_id)?.name ?? mount.ci_id,
  }));
}

export function RackPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [selectedRackId, setSelectedRackId] = useState('');

  const racksQuery = useRackList({ limit: 100 });
  const mountsQuery = useRackMounts(selectedRackId);
  const cisQuery = useCIList({ limit: 200, offset: 0 });

  const racks = useMemo(() => racksQuery.data?.data ?? [], [racksQuery.data]);

  useEffect(() => {
    const firstRack = racks[0];
    if (!selectedRackId && firstRack) {
      setSelectedRackId(firstRack.id);
    }
  }, [racks, selectedRackId]);

  const selectedRack = racks.find((rack) => rack.id === selectedRackId);

  const cisById = useMemo(
    () => new Map((cisQuery.data?.data ?? []).map((ci) => [ci.id, ci])),
    [cisQuery.data],
  );

  const mounts = useMemo(() => mountsQuery.data?.data ?? [], [mountsQuery.data]);
  const units = useMemo(() => buildRackUnits(mounts, cisById), [mounts, cisById]);

  return (
    <div className="space-y-4">
      <div>
        <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">{t('rack.title')}</h2>
        <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">{t('rack.subtitle')}</p>
      </div>

      {racksQuery.isLoading ? <SkeletonList rows={3} label={t('app.loading')} /> : null}

      {racksQuery.error ? (
        <ErrorState
          title={t('rack.loadError')}
          description={racksQuery.error instanceof Error ? racksQuery.error.message : undefined}
          retryLabel={t('common.retry')}
          onRetry={() => void racksQuery.refetch()}
        />
      ) : null}

      {racksQuery.data && racks.length === 0 ? (
        <EmptyState title={t('rack.empty')} description={t('rack.emptyHint')} />
      ) : null}

      {racks.length > 0 ? (
        <Card>
          <Select
            label={t('rack.selectRack')}
            value={selectedRackId}
            onChange={(event) => setSelectedRackId(event.target.value)}
            className="max-w-sm"
            options={racks.map((rack) => ({
              value: rack.id,
              label: `${rack.name} (${rack.height_u} HE)`,
            }))}
          />
        </Card>
      ) : null}

      {selectedRack ? (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-[auto,1fr]">
          <Card title={selectedRack.name}>
            {mountsQuery.isLoading ? (
              <SkeletonList rows={4} label={t('app.loading')} />
            ) : mountsQuery.error ? (
              <ErrorState
                title={t('rack.mountsLoadError')}
                retryLabel={t('common.retry')}
                onRetry={() => void mountsQuery.refetch()}
              />
            ) : (
              <RackSVG
                units={selectedRack.height_u}
                items={units}
                onItemClick={(item) => {
                  const mount = mounts.find((entry) => entry.id === item.id);
                  if (mount) {
                    navigate(`/cmdb/${mount.ci_id}`);
                  }
                }}
              />
            )}
          </Card>

          <Card title={t('rack.mounts')}>
            {units.length === 0 && !mountsQuery.isLoading ? (
              <EmptyState title={t('rack.noMounts')} description={t('rack.noMountsHint')} />
            ) : (
              <ul className="space-y-2 text-sm">
                {mounts.map((mount) => (
                  <li
                    key={mount.id}
                    className="flex flex-wrap items-center justify-between gap-2 border-b border-gray-100 pb-2 last:border-0 dark:border-gray-800"
                  >
                    <button
                      type="button"
                      onClick={() => navigate(`/cmdb/${mount.ci_id}`)}
                      className="font-medium text-primary underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40"
                    >
                      {cisById.get(mount.ci_id)?.name ?? mount.ci_id}
                    </button>
                    <span className="text-xs text-gray-500 dark:text-gray-400">
                      {t('rack.position')} {mount.position_u} · {mount.height_u} HE · {mount.face}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </Card>
        </div>
      ) : null}
    </div>
  );
}
