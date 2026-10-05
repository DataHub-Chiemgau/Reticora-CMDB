import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import {
  useClients,
  useCreateLocation,
  useDeleteLocation,
  useLocationTree,
} from '../api/cmdbHooks';
import {
  LOCATION_CHILD_KINDS,
  type LocationKind,
  type LocationTreeNode as TreeNode,
} from '../api/cmdb';
import { ApiError } from '../api/client';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { Input } from '../components/ui/Input';
import { Select } from '../components/ui/Select';
import { Modal } from '../components/ui/Modal';
import { SkeletonList } from '../components/ui/Skeleton';
import { ErrorState } from '../components/ui/ErrorState';

const KIND_ICONS: Record<LocationKind, string> = {
  site: '🏢',
  building: '🏗️',
  room: '🚪',
  rack: '🗼',
  warehouse: '🏭',
  zone: '▦',
  shelf: '🗄️',
  bin: '🧺',
};

/**
 * LocationTreePage shows and edits the canonical location tree (LOC-10):
 * sites are the roots, and each node offers only the child kinds the parent
 * matrix allows. Field violations of the API are shown on the inputs.
 */
export function LocationTreePage() {
  const { t } = useTranslation();
  const { data, isLoading, error, refetch } = useLocationTree();
  const create = useCreateLocation();
  const remove = useDeleteLocation();

  const [showCreate, setShowCreate] = useState(false);
  const [parent, setParent] = useState<TreeNode | undefined>();
  const [draft, setDraft] = useState<{ name: string; kind: LocationKind; client_id: string }>({
    name: '',
    kind: 'site',
    client_id: '',
  });
  const clients = useClients(showCreate && !parent);

  const openCreate = (parentNode?: TreeNode) => {
    setParent(parentNode);
    create.reset();
    setDraft({
      name: '',
      kind: parentNode ? (LOCATION_CHILD_KINDS[parentNode.kind][0] ?? 'site') : 'site',
      client_id: '',
    });
    setShowCreate(true);
  };

  const violations = create.error instanceof ApiError ? create.error.violationsByField() : {};
  const generalError =
    create.error && Object.keys(violations).length === 0 ? create.error.message : undefined;
  const kindLabel = (kind: LocationKind) => t(`locations.kinds.${kind}`, kind);
  const childKinds = parent ? LOCATION_CHILD_KINDS[parent.kind] : [];
  const clientOptions = (clients.data?.data ?? []).map((c) => ({ value: c.id, label: c.name }));

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
              'Einheitliche Hierarchie: Standort → Gebäude → Raum → Rack und Standort → Lager → Zone → Regal → Fach.',
            )}
          </p>
        </div>
        <Button size="sm" onClick={() => openCreate(undefined)}>
          {t('locations.createRoot', 'Standort anlegen')}
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
      {remove.error ? (
        <p role="alert" className="text-sm text-red-600 dark:text-red-400">
          {t('locations.deleteFailed', 'Löschen nicht möglich')}: {remove.error.message}
        </p>
      ) : null}

      <Card>
        {(data?.data ?? []).length === 0 && !isLoading ? (
          <p className="text-sm text-gray-500">
            {t('locations.empty', 'Noch keine Standorte. Legen Sie zuerst einen Standort an.')}
          </p>
        ) : (
          <ul className="space-y-1">
            {(data?.data ?? []).map((node) => (
              <LocationTreeItem
                key={node.id}
                node={node}
                depth={0}
                onAddChild={openCreate}
                onDelete={(id) => remove.mutate(id)}
                kindLabel={kindLabel}
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
            ? t('locations.createChild', {
                name: parent.name,
                defaultValue: 'Unterpunkt in {{name}}',
              })
            : t('locations.createRoot', 'Standort anlegen')
        }
      >
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate(
              parent
                ? { kind: draft.kind, name: draft.name, parent_id: parent.id }
                : { kind: 'site', name: draft.name, client_id: draft.client_id },
              { onSuccess: () => setShowCreate(false) },
            );
          }}
        >
          <Input
            label={t('locations.name', 'Name')}
            value={draft.name}
            required
            error={violations.name}
            onChange={(e) => setDraft({ ...draft, name: e.target.value })}
          />
          {parent ? (
            <Select
              label={t('locations.type', 'Typ')}
              options={childKinds.map((kind) => ({
                value: kind,
                label: `${KIND_ICONS[kind]} ${kindLabel(kind)}`,
              }))}
              value={draft.kind}
              error={violations.kind ?? violations.parent_id}
              onChange={(e) => setDraft({ ...draft, kind: e.target.value as LocationKind })}
            />
          ) : (
            <Select
              label={t('locations.client', 'Mandant')}
              options={[{ value: '', label: '—' }, ...clientOptions]}
              value={draft.client_id}
              required
              error={violations.client_id}
              onChange={(e) => setDraft({ ...draft, client_id: e.target.value })}
            />
          )}
          {generalError ? (
            <p role="alert" className="text-sm text-red-600 dark:text-red-400">
              {generalError}
            </p>
          ) : null}
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

function LocationTreeItem({
  node,
  depth,
  onAddChild,
  onDelete,
  kindLabel,
  t,
}: {
  node: TreeNode;
  depth: number;
  onAddChild: (parent: TreeNode) => void;
  onDelete: (id: string) => void;
  kindLabel: (kind: LocationKind) => string;
  t: TFunction;
}) {
  const [open, setOpen] = useState(depth < 2);
  const children = node.children;
  const canHaveChildren = LOCATION_CHILD_KINDS[node.kind].length > 0;
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
            aria-expanded={open}
            className="w-4 text-gray-400"
          >
            {open ? '▾' : '▸'}
          </button>
        ) : (
          <span className="w-4" />
        )}
        <span aria-hidden>{KIND_ICONS[node.kind]}</span>
        <span className="font-medium text-gray-900 dark:text-gray-100">{node.name}</span>
        <Badge variant="neutral">{kindLabel(node.kind)}</Badge>
        <span className="ml-auto flex gap-2">
          {canHaveChildren ? (
            <button
              className="text-xs text-primary hover:underline"
              onClick={() => onAddChild(node)}
            >
              + {t('locations.addChild', 'Unterpunkt')}
            </button>
          ) : null}
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
            <LocationTreeItem
              key={child.id}
              node={child}
              depth={depth + 1}
              onAddChild={onAddChild}
              onDelete={onDelete}
              kindLabel={kindLabel}
              t={t}
            />
          ))}
        </ul>
      ) : null}
    </li>
  );
}
