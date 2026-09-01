import { useMemo, useRef, useState } from 'react';
import type { DragEvent } from 'react';
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

/**
 * RoomPlanPage renders a room's floor-plan layer with its CIs positioned by
 * drag & drop. Positions persist as normalized coordinates on the room
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

  function onDrop(e: DragEvent<HTMLDivElement>) {
    e.preventDefault();
    const ciId = e.dataTransfer.getData('text/ci-id');
    if (!ciId || !planRef.current) return;
    const rect = planRef.current.getBoundingClientRect();
    const x = Math.min(1, Math.max(0, (e.clientX - rect.left) / rect.width));
    const y = Math.min(1, Math.max(0, (e.clientY - rect.top) / rect.height));
    saveLayout.mutate({ ...layout, [ciId]: { x, y } });
  }

  function ciPosition(ci: CI): { x: number; y: number } {
    return layout[ci.id] ?? { x: 0.5, y: 0.5 };
  }

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">
          {t('nav.roomplan', 'Raumplan')}
        </h2>
        <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">
          {t('roomplan.summary', 'Objekte auf dem Raumplan per Drag & Drop positionieren.')}
        </p>
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
                  ? { backgroundImage: `url(${floorplanURL})`, backgroundSize: 'contain', backgroundRepeat: 'no-repeat', backgroundPosition: 'center' }
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
                    onDragStart={(e) => e.dataTransfer.setData('text/ci-id', ci.id)}
                    onDoubleClick={() => navigate(`/cmdb/${ci.id}`)}
                    title={`${ci.name} (${t('roomplan.dragHint', 'ziehen zum Positionieren, Doppelklick öffnet')})`}
                    className="absolute -translate-x-1/2 -translate-y-1/2 cursor-grab rounded-lg border border-primary/40 bg-white/90 px-2 py-1 text-xs shadow-sm active:cursor-grabbing dark:bg-gray-800"
                    style={{ left: `${pos.x * 100}%`, top: `${pos.y * 100}%` }}
                  >
                    {ci.name}
                  </div>
                );
              })}
              {roomCIs.length === 0 ? (
                <p className="absolute inset-0 flex items-center justify-center text-sm text-gray-400">
                  {t('roomplan.noCIs', 'Keine CIs in diesem Raum zugeordnet.')}
                </p>
              ) : null}
            </div>
            {saveLayout.isPending ? (
              <p className="mt-2 text-xs text-gray-500">{t('app.loading')}</p>
            ) : null}
          </Card>

          <Card title={t('roomplan.objects', 'Objekte')}>
            <ul className="space-y-2 text-sm">
              {roomCIs.map((ci) => (
                <li key={ci.id} className="flex items-center justify-between gap-2">
                  <span
                    draggable
                    onDragStart={(e) => e.dataTransfer.setData('text/ci-id', ci.id)}
                    className="cursor-grab text-gray-700 dark:text-gray-300"
                  >
                    {ci.name}
                  </span>
                  <Badge variant="info">{ci.status}</Badge>
                </li>
              ))}
            </ul>
          </Card>
        </div>
      )}
    </div>
  );
}
