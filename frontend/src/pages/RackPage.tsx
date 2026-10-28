import { useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import type { RackLayoutMount } from '../api/client';
import { useRackLayout, useRackList } from '../api/hooks';
import { RackSVG } from '../components/rack/RackSVG';
import type { RackUnit } from '../components/rack/RackSVG';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';

export type RackFace = 'front' | 'rear';

/**
 * mountLabel is the CI name the layout carries with each mount (RCK-02); the
 * page reads no CI of its own (F-252). A mount without a name shows the CI id.
 */
function mountLabel(mount: RackLayoutMount): string {
  return mount.ci_name || mount.ci_id;
}

/**
 * onFace reports whether a mount shows on a face: a mount on both faces
 * occupies its units on the front and on the rear (RCK-01, F-381). Mounts
 * without an explicit face are treated as front-mounted (the backend
 * default).
 */
function onFace(mount: RackLayoutMount, face: RackFace): boolean {
  const mounted = mount.face || 'front';
  return mounted === face || mounted === 'both';
}

/**
 * Maps the mounts of a face onto the units drawn by `RackSVG`, labelled with
 * the CI name of the layout. A mount on both faces carries the title
 * "<name> · <bothLabel>", so the drawing names both faces.
 */
export function buildRackUnits(
  mounts: RackLayoutMount[],
  face: RackFace = 'front',
  bothLabel = 'front and rear',
): RackUnit[] {
  return mounts
    .filter((mount) => onFace(mount, face))
    .map((mount) => ({
      id: mount.id,
      position: mount.position_u,
      height: Math.max(1, mount.height_u),
      label: mountLabel(mount),
      ...(mount.face === 'both' ? { title: `${mountLabel(mount)} · ${bothLabel}` } : {}),
    }));
}

export function RackPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [selectedRackId, setSelectedRackId] = useState('');
  const [face, setFace] = useState<RackFace>('front');

  const racksQuery = useRackList({ limit: 100 });
  const mountsQuery = useRackLayout(selectedRackId);

  const racks = useMemo(() => racksQuery.data?.data ?? [], [racksQuery.data]);

  useEffect(() => {
    const firstRack = racks[0];
    if (!selectedRackId && firstRack) {
      setSelectedRackId(firstRack.id);
    }
  }, [racks, selectedRackId]);

  const selectedRack = racks.find((rack) => rack.id === selectedRackId);

  const mounts = useMemo(() => mountsQuery.data?.mounts ?? [], [mountsQuery.data]);
  const visibleMounts = useMemo(
    () => mounts.filter((mount) => onFace(mount, face)),
    [mounts, face],
  );
  const bothLabel = t('rack.faceBoth');
  const units = useMemo(() => buildRackUnits(mounts, face, bothLabel), [mounts, face, bothLabel]);

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
              label: `${rack.name} (${rack.height_units} HE)`,
            }))}
          />
        </Card>
      ) : null}

      {selectedRack ? (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-[auto,1fr]">
          <Card
            title={`${selectedRack.name} · ${t(face === 'front' ? 'rack.faceFront' : 'rack.faceRear')}`}
            actions={
              <div className="flex gap-1" role="group" aria-label={t('rack.faceLabel')}>
                {(['front', 'rear'] as const).map((value) => (
                  <button
                    key={value}
                    type="button"
                    aria-pressed={face === value}
                    onClick={() => setFace(value)}
                    className={
                      face === value
                        ? 'rounded-lg bg-primary px-3 py-1 text-sm font-medium text-white focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40'
                        : 'rounded-lg px-3 py-1 text-sm font-medium text-gray-600 hover:bg-gray-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40 dark:text-gray-300 dark:hover:bg-gray-800'
                    }
                  >
                    {t(value === 'front' ? 'rack.faceFront' : 'rack.faceRear')}
                  </button>
                ))}
              </div>
            }
          >
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
                units={selectedRack.height_units}
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

          <Card
            title={`${t('rack.mounts')} (${t(face === 'front' ? 'rack.faceFront' : 'rack.faceRear')})`}
          >
            {visibleMounts.length === 0 && !mountsQuery.isLoading ? (
              <EmptyState title={t('rack.noMounts')} description={t('rack.noMountsHint')} />
            ) : (
              <ul className="space-y-2 text-sm">
                {visibleMounts.map((mount) => (
                  <li
                    key={mount.id}
                    className="flex flex-wrap items-center justify-between gap-2 border-b border-gray-100 pb-2 last:border-0 dark:border-gray-800"
                  >
                    <span className="flex min-w-0 flex-wrap items-baseline gap-2">
                      <button
                        type="button"
                        onClick={() => navigate(`/cmdb/${mount.ci_id}`)}
                        className="min-w-0 break-words text-left font-medium text-primary underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40"
                      >
                        {mountLabel(mount)}
                      </button>
                      <span className="text-xs text-gray-600 dark:text-gray-300">
                        {mount.ci_type}
                      </span>
                    </span>
                    <span className="text-xs text-gray-500 dark:text-gray-400">
                      {t('rack.position')} {mount.position_u} · {mount.height_u} HE ·{' '}
                      {mount.face === 'both' ? bothLabel : mount.face}
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
