import { Server } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import type { RackLayoutMount } from '../api/client';
import { useRackLayout, useRackList } from '../api/hooks';
import { RackSVG } from '../components/rack/RackSVG';
import type { RackUnit } from '../components/rack/RackSVG';
import { Panel } from '../components/spatial/Panel';
import { Button } from '../components/ui/Button';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { Toolbar, ToolbarEnd } from '../components/ui/Toolbar';
import { cn } from '../components/ui/utils';

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

  const faceName = t(face === 'front' ? 'rack.faceFront' : 'rack.faceRear');

  return (
    <div className="space-y-4">
      <PageHeader title={t('rack.title')} description={t('rack.subtitle')} />

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
        <EmptyState icon={<Server />} title={t('rack.empty')} description={t('rack.emptyHint')} />
      ) : null}

      {racks.length > 0 ? (
        <Toolbar className="grid grid-cols-1 sm:flex sm:flex-wrap">
          <Select
            aria-label={t('rack.selectRack')}
            value={selectedRackId}
            onChange={(event) => setSelectedRackId(event.target.value)}
            className="w-full pr-8 sm:w-64"
            options={racks.map((rack) => ({
              value: rack.id,
              label: `${rack.name} (${rack.height_units} HE)`,
            }))}
          />
          <div
            className="inline-flex w-fit rounded-lg border border-line-strong bg-panel p-0.5"
            role="group"
            aria-label={t('rack.faceLabel')}
          >
            {(['front', 'rear'] as const).map((value) => (
              <Button
                key={value}
                variant="ghost"
                size="sm"
                aria-pressed={face === value}
                onClick={() => setFace(value)}
                className={cn(
                  'h-7',
                  face === value ? 'bg-accent-soft text-accent-strong hover:bg-accent-soft' : false,
                )}
              >
                {t(value === 'front' ? 'rack.faceFront' : 'rack.faceRear')}
              </Button>
            ))}
          </div>
          {selectedRack ? (
            <ToolbarEnd>
              <span className="text-sm tabular-nums text-fg-muted">
                {selectedRack.height_units} HE · {visibleMounts.length} {t('rack.mounts')}
              </span>
            </ToolbarEnd>
          ) : null}
        </Toolbar>
      ) : null}

      {selectedRack ? (
        <div className="grid grid-cols-1 gap-4 items-start lg:grid-cols-[auto,minmax(0,1fr)]">
          <Panel title={`${selectedRack.name} · ${faceName}`}>
            <div className="flex justify-center overflow-x-auto">
              {mountsQuery.isLoading ? (
                <SkeletonList rows={4} label={t('app.loading')} className="w-full" />
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
            </div>
          </Panel>

          <Panel title={`${t('rack.mounts')} (${faceName})`} padded={false}>
            {visibleMounts.length === 0 && !mountsQuery.isLoading ? (
              <div className="p-4">
                <EmptyState
                  icon={<Server />}
                  title={t('rack.noMounts')}
                  description={t('rack.noMountsHint')}
                />
              </div>
            ) : (
              <ul className="divide-y divide-line text-sm">
                {visibleMounts.map((mount) => (
                  <li
                    key={mount.id}
                    className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 px-4 py-2.5"
                  >
                    <span className="flex min-w-0 flex-wrap items-baseline gap-2">
                      <button
                        type="button"
                        onClick={() => navigate(`/cmdb/${mount.ci_id}`)}
                        className="min-w-0 break-words rounded text-left font-medium text-accent underline-offset-2 hover:underline focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
                      >
                        {mountLabel(mount)}
                      </button>
                      <span className="text-xs text-fg-muted">{mount.ci_type}</span>
                    </span>
                    <span className="text-xs tabular-nums text-fg-muted">
                      {t('rack.position')} {mount.position_u} · {mount.height_u} HE ·{' '}
                      {mount.face === 'both' ? bothLabel : mount.face}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </Panel>
        </div>
      ) : null}
    </div>
  );
}
