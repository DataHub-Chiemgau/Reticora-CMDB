import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { useLocationTree, useCreateLocation } from '../api/cmdbHooks';
import { locationApi, type LocationNode } from '../api/cmdb';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { Input } from '../components/ui/Input';
import { Select } from '../components/ui/Select';
import { Modal } from '../components/ui/Modal';
import { SkeletonList } from '../components/ui/Skeleton';
import { ErrorState } from '../components/ui/ErrorState';

const NODE_TYPES = [
  'site',
  'building',
  'floor',
  'room',
  'warehouse',
  'zone',
  'shelf',
  'bin',
  'rack',
  'desk',
  'vehicle',
  'customer',
  'logical',
  'custom',
];

const TYPE_ICONS: Record<string, string> = {
  site: '🏢',
  building: '🏗️',
  floor: '🛗',
  room: '🚪',
  warehouse: '🏭',
  zone: '▦',
  shelf: '🗄️',
  bin: '🧺',
  rack: '🗼',
  desk: '🖥️',
  vehicle: '🚚',
  customer: '👤',
  logical: '☁️',
  custom: '📦',
};

export function LocationTreePage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { data, isLoading, error, refetch } = useLocationTree();
  const create = useCreateLocation();
  const remove = useMutation({
    mutationFn: (id: string) => locationApi.delete(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['locations'] }),
  });

  const [showCreate, setShowCreate] = useState(false);
  const [parent, setParent] = useState<LocationNode | undefined>();
  const [draft, setDraft] = useState({ name: '', node_type: 'room', barcode: '' });

  const openCreate = (parentNode?: LocationNode) => {
    setParent(parentNode);
    setDraft({
      name: '',
      node_type: parentNode ? childTypeHint(parentNode.node_type) : 'site',
      barcode: '',
    });
    setShowCreate(true);
  };

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">
            {t('locations.title', 'Standorte & Lager')}
          </h2>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">
            {t(
              'locations.subtitle',
              'Einheitliche Hierarchie: Standort → Gebäude → Raum → Rack und Lager → Zone → Regal → Fach.',
            )}
          </p>
        </div>
        <Button size="sm" onClick={() => openCreate(undefined)}>
          {t('locations.createRoot', 'Wurzel anlegen')}
        </Button>
      </div>

      {isLoading ? <SkeletonList rows={5} label={t('app.loading')} /> : null}
      {error ? (
        <ErrorState
          title={t('app.error')}
          retryLabel={t('common.retry')}
          onRetry={() => void refetch()}
        />
      ) : null}

      <Card>
        {(data?.data ?? []).length === 0 && !isLoading ? (
          <p className="text-sm text-gray-500">
            {t(
              'locations.empty',
              'Noch keine Standorte. Legen Sie eine Wurzel an (z. B. Standort oder Lager).',
            )}
          </p>
        ) : (
          <ul className="space-y-1">
            {(data?.data ?? []).map((node) => (
              <LocationTreeNode
                key={node.id}
                node={node}
                depth={0}
                onAddChild={openCreate}
                onDelete={(id) => remove.mutate(id)}
                t={t}
              />
            ))}
          </ul>
        )}
      </Card>

      <Modal
        open={showCreate}
        onOpenChange={setShowCreate}
        title={
          parent
            ? t('locations.createChild', `Unterpunkt in ${parent.name}`)
            : t('locations.createRoot', 'Wurzel anlegen')
        }
      >
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate(
              {
                name: draft.name,
                node_type: draft.node_type,
                barcode: draft.barcode || undefined,
                parent_id: parent?.id,
              },
              { onSuccess: () => setShowCreate(false) },
            );
          }}
        >
          <Input
            label={t('locations.name', 'Name')}
            value={draft.name}
            required
            onChange={(e) => setDraft({ ...draft, name: e.target.value })}
          />
          <Select
            label={t('locations.type', 'Typ')}
            options={NODE_TYPES.map((nt) => ({
              value: nt,
              label: `${TYPE_ICONS[nt] ?? ''} ${nt}`,
            }))}
            value={draft.node_type}
            onChange={(e) => setDraft({ ...draft, node_type: e.target.value })}
          />
          <Input
            label={t('locations.barcode', 'Barcode/RFID (optional)')}
            value={draft.barcode}
            onChange={(e) => setDraft({ ...draft, barcode: e.target.value })}
          />
          <div className="flex justify-end gap-2">
            <Button type="button" variant="secondary" onClick={() => setShowCreate(false)}>
              {t('common.cancel', 'Abbrechen')}
            </Button>
            <Button type="submit" disabled={create.isPending}>
              {t('common.create', 'Anlegen')}
            </Button>
          </div>
        </form>
      </Modal>
    </div>
  );
}

function childTypeHint(parentType: string): string {
  const order: Record<string, string> = {
    site: 'building',
    building: 'floor',
    floor: 'room',
    room: 'rack',
    warehouse: 'zone',
    zone: 'shelf',
    shelf: 'bin',
    rack: 'custom',
  };
  return order[parentType] ?? 'custom';
}

function LocationTreeNode({
  node,
  depth,
  onAddChild,
  onDelete,
  t,
}: {
  node: LocationNode;
  depth: number;
  onAddChild: (parent: LocationNode) => void;
  onDelete: (id: string) => void;
  t: TFunction;
}) {
  const [open, setOpen] = useState(depth < 2);
  const children = node.children ?? [];
  return (
    <li>
      <div
        className="flex items-center gap-2 rounded-md px-2 py-1.5 hover:bg-gray-50 dark:hover:bg-gray-800"
        style={{ paddingLeft: `${depth * 1.25 + 0.5}rem` }}
      >
        {children.length > 0 ? (
          <button
            onClick={() => setOpen(!open)}
            aria-label={open ? 'collapse' : 'expand'}
            className="w-4 text-gray-400"
          >
            {open ? '▾' : '▸'}
          </button>
        ) : (
          <span className="w-4" />
        )}
        <span aria-hidden>{TYPE_ICONS[node.node_type] ?? '📦'}</span>
        <span className="font-medium text-gray-900 dark:text-gray-100">{node.name}</span>
        <Badge variant="neutral">{node.node_type}</Badge>
        {node.barcode ? <span className="text-xs text-gray-400">⌗{node.barcode}</span> : null}
        <span className="ml-auto flex gap-2">
          <button className="text-xs text-primary hover:underline" onClick={() => onAddChild(node)}>
            + {t('locations.addChild', 'Unterpunkt')}
          </button>
          <button
            className="text-xs text-red-600 hover:underline"
            onClick={() => onDelete(node.id)}
          >
            {t('common.delete', 'Löschen')}
          </button>
        </span>
      </div>
      {open && children.length > 0 ? (
        <ul>
          {children.map((child) => (
            <LocationTreeNode
              key={child.id}
              node={child}
              depth={depth + 1}
              onAddChild={onAddChild}
              onDelete={onDelete}
              t={t}
            />
          ))}
        </ul>
      ) : null}
    </li>
  );
}
