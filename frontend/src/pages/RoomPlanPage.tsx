import { useMemo, useRef, useState } from 'react';
import type { DragEvent, FormEvent, KeyboardEvent } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { ciApi, roomApi, buildingApi } from '../api/client';
import type { Room, CI } from '../api/client';
import { Badge } from '../components/ui/Badge';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Select } from '../components/ui/Select';
import { Skeleton } from '../components/ui/Skeleton';

export interface PlanPosition {
  x: number;
  y: number;
}

const clamp01 = (value: number) => Math.min(1, Math.max(0, value));

/** dropPosition maps a pointer position to normalized plan coordinates. */
export function dropPosition(
  clientX: number,
  clientY: number,
  rect: { left: number; top: number; width: number; height: number },
): PlanPosition {
  return {
    x: clamp01((clientX - rect.left) / rect.width),
    y: clamp01((clientY - rect.top) / rect.height),
  };
}

/**
 * keyboardMove returns the position after an arrow key (1 % per press, 10 %
 * with Shift), or undefined for other keys. Positions are rounded to whole
 * percent so repeated presses do not accumulate floating point noise.
 */
export function keyboardMove(
  pos: PlanPosition,
  key: string,
  shiftKey: boolean,
): PlanPosition | undefined {
  const step = shiftKey ? 0.1 : 0.01;
  const delta: Record<string, [number, number]> = {
    ArrowLeft: [-step, 0],
    ArrowRight: [step, 0],
    ArrowUp: [0, -step],
    ArrowDown: [0, step],
  };
  const d = delta[key];
  if (!d) return undefined;
  const round = (v: number) => Math.round(clamp01(v) * 100) / 100;
  return { x: round(pos.x + d[0]), y: round(pos.y + d[1]) };
}

const percent = (value: number) => Math.round(value * 100);

/**
 * RoomPlanPage renders a room's floor-plan layer with its CIs positioned by
 * drag & drop, with the arrow keys on a focused object or by entering the
 * coordinates in the object list (WCAG 2.1 AA 2.1.1 keyboard and 2.5.7
 * alternatives to dragging, UI-18/NFR-07). Positions persist as normalized coordinates on the room
 * (spec §8.5: grafische Raumdarstellung). The floor plan image (when the
 * building carries floorplan_object_key) is rendered as the background layer;
 * without an image a neutral grid stands in.
 */
export function RoomPlanPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const planRef = useRef<HTMLDivElement>(null);

  const buildings = useQuery({
    queryKey: ['buildings'],
    queryFn: () => buildingApi.list({ limit: 100 }),
  });
  const [buildingId, setBuildingId] = useState('');
  const rooms = useQuery({
    queryKey: ['rooms', buildingId],
    queryFn: () => roomApi.list({ building_id: buildingId, limit: 100 }),
    enabled: !!buildingId,
  });
  const [roomId, setRoomId] = useState('');
  const room = useQuery({
    queryKey: ['room', roomId],
    queryFn: () => roomApi.get(roomId),
    enabled: !!roomId,
  });

  // CIs assigned to the selected room become the draggable objects.
  const cis = useQuery({
    queryKey: ['room-cis', roomId],
    queryFn: () => ciApi.list({ room_id: roomId, limit: 200 }),
    enabled: !!roomId,
  });

  const layout = useMemo(() => room.data?.layout ?? {}, [room.data]);
  const roomCIs = useMemo(() => cis.data?.data ?? [], [cis.data]);

  const saveLayout = useMutation({
    mutationFn: (next: Record<string, { x: number; y: number }>) =>
      roomApi.updateLayout(roomId, next),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['room', roomId] });
    },
  });

  const selectedBuilding = buildings.data?.data.find((b) => b.id === buildingId);
  const floorplanURL = selectedBuilding?.floorplan_object_key
    ? `/api/v1/documents/content?key=${encodeURIComponent(selectedBuilding.floorplan_object_key)}`
    : undefined;

  function savePosition(ciId: string, pos: PlanPosition) {
    saveLayout.mutate({ ...layout, [ciId]: pos });
  }

  function onDrop(e: DragEvent<HTMLDivElement>) {
    e.preventDefault();
    const ciId = e.dataTransfer.getData('text/ci-id');
    if (!ciId || !planRef.current) return;
    savePosition(ciId, dropPosition(e.clientX, e.clientY, planRef.current.getBoundingClientRect()));
  }

  function ciPosition(ci: CI): PlanPosition {
    return layout[ci.id] ?? { x: 0.5, y: 0.5 };
  }

  function onMarkerKeyDown(e: KeyboardEvent<HTMLDivElement>, ci: CI) {
    if (e.key === 'Enter') {
      e.preventDefault();
      navigate(`/cmdb/${ci.id}`);
      return;
    }
    const next = keyboardMove(ciPosition(ci), e.key, e.shiftKey);
    if (!next) return;
    e.preventDefault();
    savePosition(ci.id, next);
  }

  function onPositionSubmit(e: FormEvent<HTMLFormElement>, ci: CI) {
    e.preventDefault();
    const data = new FormData(e.currentTarget);
    const x = Number(data.get('x'));
    const y = Number(data.get('y'));
    if (!Number.isFinite(x) || !Number.isFinite(y)) return;
    savePosition(ci.id, { x: clamp01(x / 100), y: clamp01(y / 100) });
  }

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">
          {t('nav.roomplan', 'Raumplan')}
        </h2>
        <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">{t('roomplan.summary')}</p>
      </div>

      <Card>
        <div className="flex flex-wrap gap-3">
          <Select
            value={buildingId}
            onChange={(e) => {
              setBuildingId(e.target.value);
              setRoomId('');
            }}
            aria-label={t('roomplan.building', 'Gebäude')}
            className="max-w-xs"
            options={[
              { value: '', label: t('roomplan.selectBuilding', 'Gebäude wählen') },
              ...(buildings.data?.data ?? []).map((b) => ({ value: b.id, label: b.name })),
            ]}
          />
          <Select
            value={roomId}
            onChange={(e) => setRoomId(e.target.value)}
            aria-label={t('roomplan.room', 'Raum')}
            className="max-w-xs"
            disabled={!buildingId}
            options={[
              { value: '', label: t('roomplan.selectRoom', 'Raum wählen') },
              ...(rooms.data?.data ?? []).map((r: Room) => ({ value: r.id, label: r.name })),
            ]}
          />
        </div>
      </Card>

      {!roomId ? (
        <EmptyState
          title={t('roomplan.empty', 'Kein Raum gewählt')}
          description={t('roomplan.emptyHint', 'Wählen Sie ein Gebäude und einen Raum.')}
        />
      ) : room.isLoading ? (
        <Skeleton className="h-96 w-full" />
      ) : room.error ? (
        <ErrorState title={t('app.error')} />
      ) : (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-4">
          <Card className="lg:col-span-3">
            <div
              ref={planRef}
              onDragOver={(e) => e.preventDefault()}
              onDrop={onDrop}
              data-testid="room-plan"
              className="relative h-[28rem] w-full overflow-hidden rounded-lg border border-gray-200 bg-gray-50 bg-[linear-gradient(to_right,rgba(0,0,0,0.05)_1px,transparent_1px),linear-gradient(to_bottom,rgba(0,0,0,0.05)_1px,transparent_1px)] bg-[size:24px_24px] dark:border-gray-700 dark:bg-gray-900"
              style={
                floorplanURL
                  ? {
                      backgroundImage: `url(${floorplanURL})`,
                      backgroundSize: 'contain',
                      backgroundRepeat: 'no-repeat',
                      backgroundPosition: 'center',
                    }
                  : undefined
              }
            >
              {roomCIs.map((ci) => {
                const pos = ciPosition(ci);
                return (
                  <div
                    key={ci.id}
                    role="button"
                    tabIndex={0}
                    draggable
                    aria-label={t('roomplan.markerLabel', {
                      name: ci.name,
                      x: percent(pos.x),
                      y: percent(pos.y),
                    })}
                    aria-describedby="roomplan-keyboard-hint"
                    onKeyDown={(e) => onMarkerKeyDown(e, ci)}
                    onDragStart={(e) => e.dataTransfer.setData('text/ci-id', ci.id)}
                    onDoubleClick={() => navigate(`/cmdb/${ci.id}`)}
                    title={`${ci.name} (${t('roomplan.dragHint')})`}
                    className="absolute -translate-x-1/2 -translate-y-1/2 cursor-grab rounded-lg border border-primary/40 bg-white/90 px-2 py-1 text-xs text-gray-900 shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary active:cursor-grabbing dark:bg-gray-800 dark:text-gray-100"
                    style={{ left: `${pos.x * 100}%`, top: `${pos.y * 100}%` }}
                  >
                    {ci.name}
                  </div>
                );
              })}
              {roomCIs.length === 0 ? (
                <p className="absolute inset-0 flex items-center justify-center text-sm text-gray-600 dark:text-gray-300">
                  {t('roomplan.noCIs', 'Keine CIs in diesem Raum zugeordnet.')}
                </p>
              ) : null}
            </div>
            <p
              id="roomplan-keyboard-hint"
              className="mt-2 text-xs text-gray-600 dark:text-gray-300"
            >
              {t('roomplan.keyboardHint')}
            </p>
            {saveLayout.isPending ? (
              <p className="mt-2 text-xs text-gray-500">{t('app.loading')}</p>
            ) : null}
          </Card>

          <Card title={t('roomplan.objects')}>
            <ul className="space-y-3 text-sm">
              {roomCIs.map((ci) => {
                const pos = ciPosition(ci);
                return (
                  <li key={ci.id} className="space-y-1.5">
                    <div className="flex items-center justify-between gap-2">
                      <span
                        draggable
                        onDragStart={(e) => e.dataTransfer.setData('text/ci-id', ci.id)}
                        className="cursor-grab text-gray-700 dark:text-gray-300"
                      >
                        {ci.name}
                      </span>
                      <Badge variant="info">{ci.status}</Badge>
                    </div>
                    {/* Form alternative to dragging: key/value input of the position. */}
                    <form
                      key={`${pos.x}-${pos.y}`}
                      aria-label={t('roomplan.positionFor', { name: ci.name })}
                      onSubmit={(e) => onPositionSubmit(e, ci)}
                      className="flex items-end gap-2"
                    >
                      <label className="text-xs text-gray-600 dark:text-gray-300">
                        {t('roomplan.positionX')}
                        <input
                          name="x"
                          type="number"
                          min={0}
                          max={100}
                          step={1}
                          defaultValue={percent(pos.x)}
                          className="mt-0.5 block w-16 rounded border border-gray-300 px-1.5 py-1 text-sm text-gray-900 dark:border-gray-700 dark:bg-gray-900 dark:text-gray-100"
                        />
                      </label>
                      <label className="text-xs text-gray-600 dark:text-gray-300">
                        {t('roomplan.positionY')}
                        <input
                          name="y"
                          type="number"
                          min={0}
                          max={100}
                          step={1}
                          defaultValue={percent(pos.y)}
                          className="mt-0.5 block w-16 rounded border border-gray-300 px-1.5 py-1 text-sm text-gray-900 dark:border-gray-700 dark:bg-gray-900 dark:text-gray-100"
                        />
                      </label>
                      <button
                        type="submit"
                        className="rounded-md border border-gray-300 px-2 py-1 text-xs text-gray-800 hover:bg-gray-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary dark:border-gray-700 dark:text-gray-100 dark:hover:bg-gray-800"
                      >
                        {t('roomplan.applyPosition')}
                      </button>
                    </form>
                  </li>
                );
              })}
            </ul>
          </Card>
        </div>
      )}
    </div>
  );
}
