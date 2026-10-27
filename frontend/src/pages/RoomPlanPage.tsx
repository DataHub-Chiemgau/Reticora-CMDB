import { useEffect, useMemo, useRef, useState } from 'react';
import type { DragEvent, FormEvent, KeyboardEvent } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { ApiError, ciApi, roomApi, buildingApi } from '../api/client';
import type { Room, CI, Versioned } from '../api/client';
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

/** Quiet time after the last arrow key before the moves are saved. */
export const KEYBOARD_SAVE_DELAY_MS = 300;

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
  const roomKey = useMemo(() => ['room', roomId], [roomId]);
  const room = useQuery({
    queryKey: roomKey,
    queryFn: () => roomApi.get(roomId),
    enabled: !!roomId,
  });

  // CIs assigned to the selected room become the draggable objects.
  const cis = useQuery({
    queryKey: ['room-cis', roomId],
    queryFn: () => ciApi.list({ room_id: roomId, limit: 200 }),
    enabled: !!roomId,
  });

  // Positions moved here but not yet confirmed by the server. Arrow keys
  // accumulate on them and one debounced PATCH sends the result, so no step
  // is lost while a save is in flight (F-342).
  const pendingRef = useRef<Record<string, PlanPosition>>({});
  const [pending, setPending] = useState<Record<string, PlanPosition>>({});
  const timerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const inFlightRef = useRef(false);
  const flushAgainRef = useRef(false);
  const [saving, setSaving] = useState(false);
  const [conflict, setConflict] = useState(false);
  const [saveError, setSaveError] = useState<string>();

  const serverLayout = useMemo(() => room.data?.data.layout ?? {}, [room.data]);
  const layout = useMemo(() => ({ ...serverLayout, ...pending }), [serverLayout, pending]);
  const roomCIs = useMemo(() => cis.data?.data ?? [], [cis.data]);

  function setPendingPositions(next: Record<string, PlanPosition>) {
    pendingRef.current = next;
    setPending(next);
  }

  // A room switch drops the unsaved moves of the previous room.
  useEffect(() => {
    clearTimeout(timerRef.current);
    setPendingPositions({});
    setConflict(false);
    setSaveError(undefined);
  }, [roomId]);
  useEffect(() => () => clearTimeout(timerRef.current), []);

  /**
   * flush sends the pending positions on top of the last layout the server
   * confirmed, with If-Match of its ETag. One PATCH at a time: moves made
   * meanwhile go out with the next one, based on the new ETag. A 409 (the
   * room was changed elsewhere) drops the moves, reloads and shows a hint.
   */
  function flush() {
    if (inFlightRef.current) {
      flushAgainRef.current = true;
      return;
    }
    const sent = pendingRef.current;
    if (Object.keys(sent).length === 0) return;
    const base = queryClient.getQueryData<Versioned<Room>>(roomKey);
    inFlightRef.current = true;
    setSaving(true);
    roomApi
      .updateLayout(roomId, { ...(base?.data.layout ?? {}), ...sent }, base?.etag)
      .then((saved) => {
        queryClient.setQueryData(roomKey, saved);
        // Keep only moves made after this PATCH was sent.
        const rest = { ...pendingRef.current };
        for (const [id, pos] of Object.entries(sent)) {
          if (rest[id] === pos) delete rest[id];
        }
        setPendingPositions(rest);
        setConflict(false);
        setSaveError(undefined);
      })
      .catch((err: unknown) => {
        setPendingPositions({});
        flushAgainRef.current = false;
        if (err instanceof ApiError && err.status === 409) {
          setConflict(true);
          setSaveError(undefined);
          void queryClient.invalidateQueries({ queryKey: roomKey });
          return;
        }
        setSaveError(
          err instanceof ApiError && err.status === 422 && /\.(x|y)$/.test(err.field ?? '')
            ? t('roomplan.rangeError')
            : t('roomplan.saveError', { message: err instanceof Error ? err.message : '' }),
        );
      })
      .finally(() => {
        inFlightRef.current = false;
        setSaving(false);
        if (flushAgainRef.current) {
          flushAgainRef.current = false;
          flush();
        }
      });
  }

  /** savePosition records a move and sends it now, or after delay ms of quiet. */
  function savePosition(ciId: string, pos: PlanPosition, delay = 0) {
    setPendingPositions({ ...pendingRef.current, [ciId]: pos });
    clearTimeout(timerRef.current);
    if (delay > 0) timerRef.current = setTimeout(flush, delay);
    else flush();
  }

  const selectedBuilding = buildings.data?.data.find((b) => b.id === buildingId);
  const floorplanURL = selectedBuilding?.floorplan_object_key
    ? `/api/v1/documents/content?key=${encodeURIComponent(selectedBuilding.floorplan_object_key)}`
    : undefined;

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
    // The step starts from the latest local position, not the last render.
    const current = pendingRef.current[ci.id] ?? ciPosition(ci);
    const next = keyboardMove(current, e.key, e.shiftKey);
    if (!next) return;
    e.preventDefault();
    savePosition(ci.id, next, KEYBOARD_SAVE_DELAY_MS);
  }

  function onPositionSubmit(e: FormEvent<HTMLFormElement>, ci: CI) {
    e.preventDefault();
    const data = new FormData(e.currentTarget);
    const x = Number(data.get('x'));
    const y = Number(data.get('y'));
    if (!Number.isFinite(x) || !Number.isFinite(y)) return;
    // Out-of-range input is sent as typed: the server's 422 is shown.
    savePosition(ci.id, { x: x / 100, y: y / 100 });
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
            {saving ? <p className="mt-2 text-xs text-muted">{t('app.loading')}</p> : null}
            {conflict ? (
              <p role="alert" className="mt-2 text-sm text-amber-800 dark:text-amber-300">
                {t('roomplan.conflict')}
              </p>
            ) : null}
            {saveError ? (
              <p role="alert" className="mt-2 text-sm text-red-700 dark:text-red-300">
                {saveError}
              </p>
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
                      <Badge variant="info">{t(`ci.statuses.${ci.status}`, ci.status)}</Badge>
                    </div>
                    {/* Form alternative to dragging: key/value input of the position. */}
                    <form
                      key={`${pos.x}-${pos.y}`}
                      aria-label={t('roomplan.positionFor', { name: ci.name })}
                      onSubmit={(e) => onPositionSubmit(e, ci)}
                      noValidate
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
