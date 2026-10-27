import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import {
  useClients,
  useCreateLocation,
  useDeleteLocation,
  useLocationTree,
  useUpdateLocation,
} from '../api/cmdbHooks';
import {
  LOCATION_CHILD_KINDS,
  type LocationKind,
  type LocationTreeNode as TreeNode,
  type LocationUpdateRequest,
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
 * parentOptions lists the nodes `node` may be moved below: the parent matrix
 * (LOC-10) must allow its kind there, and neither the node itself nor one of
 * its descendants qualifies (that would be a cycle). The label is the path of
 * names from the site, so equal names in different sites stay apart.
 */
function parentOptions(roots: TreeNode[], node: TreeNode): { value: string; label: string }[] {
  const out: { value: string; label: string }[] = [];
  const walk = (list: TreeNode[], trail: string[]) => {
    for (const candidate of list) {
      if (candidate.id === node.id) continue; // skips the whole subtree
      const names = [...trail, candidate.name];
      if (LOCATION_CHILD_KINDS[candidate.kind].includes(node.kind)) {
        out.push({ value: candidate.id, label: names.join(' › ') });
      }
      walk(candidate.children, names);
    }
  };
  walk(roots, []);
  return out;
}

/**
 * LocationTreePage shows and edits the canonical location tree (LOC-10):
 * sites are the roots, and each node offers only the child kinds the parent
 * matrix allows. Every node can be renamed and, except a site, moved below
 * another allowed parent (LOC-07); deleting asks first, and a refusal names
 * the kinds of the objects that still reference the node (LOC-11). Field
 * violations of the API are shown on the inputs, a missing permission (403)
 * as a visible message.
 */
export function LocationTreePage() {
  const { t } = useTranslation();
  const { data, isLoading, error, refetch } = useLocationTree();
  const create = useCreateLocation();
  const update = useUpdateLocation();
  const remove = useDeleteLocation();
  const roots = useMemo(() => data?.data ?? [], [data]);

  const [showCreate, setShowCreate] = useState(false);
  const [parent, setParent] = useState<TreeNode | undefined>();
  const [draft, setDraft] = useState<{ name: string; kind: LocationKind; client_id: string }>({
    name: '',
    kind: 'site',
    client_id: '',
  });
  const clients = useClients(showCreate && !parent);

  const [editing, setEditing] = useState<TreeNode | undefined>();
  const [editDraft, setEditDraft] = useState({ name: '', parent_id: '' });
  const [deleting, setDeleting] = useState<TreeNode | undefined>();

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

  const openEdit = (node: TreeNode) => {
    update.reset();
    setEditDraft({ name: node.name, parent_id: node.parent_id ?? '' });
    setEditing(node);
  };

  const openDelete = (node: TreeNode) => {
    remove.reset();
    setDeleting(node);
  };

  const kindLabel = (kind: LocationKind) => t(`locations.kinds.${kind}`, kind);
  /** problemText is the message of a failed request; a 403 is translated. */
  const problemText = (err: Error) =>
    err instanceof ApiError && err.status === 403
      ? t('locations.forbidden', 'Keine Berechtigung für diese Aktion.')
      : err.message;

  const violations = create.error instanceof ApiError ? create.error.violationsByField() : {};
  const generalError =
    create.error && Object.keys(violations).length === 0 ? problemText(create.error) : undefined;
  const childKinds = parent ? LOCATION_CHILD_KINDS[parent.kind] : [];
  const clientOptions = (clients.data?.data ?? []).map((c) => ({ value: c.id, label: c.name }));

  // A site is a root: it can be renamed, not moved.
  const canMove = editing !== undefined && editing.kind !== 'site';
  const editParents = editing && canMove ? parentOptions(roots, editing) : [];
  const editViolations = update.error instanceof ApiError ? update.error.violationsByField() : {};
  const editFieldError =
    editViolations.name !== undefined || (canMove && editViolations.parent_id !== undefined);
  const editGeneralError = update.error && !editFieldError ? problemText(update.error) : undefined;

  let deleteError: string | undefined;
  if (remove.error) {
    const kinds = remove.error instanceof ApiError ? remove.error.dependencies : [];
    deleteError =
      remove.error instanceof ApiError && remove.error.status === 409 && kinds.length > 0
        ? t('locations.inUse', {
            kinds: kinds.map((kind) => t(`locations.dependencies.${kind}`, kind)).join(', '),
            defaultValue: 'noch verwendet von {{kinds}}',
          })
        : problemText(remove.error);
  }

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
      {deleteError ? (
        <p role="alert" className="text-sm text-red-600 dark:text-red-400">
          {t('locations.deleteFailed', 'Löschen nicht möglich')}: {deleteError}
        </p>
      ) : null}

      <Card>
        {roots.length === 0 && !isLoading ? (
          <p className="text-sm text-gray-500">
            {t('locations.empty', 'Noch keine Standorte. Legen Sie zuerst einen Standort an.')}
          </p>
        ) : (
          <ul className="space-y-1">
            {roots.map((node) => (
              <LocationTreeItem
                key={node.id}
                node={node}
                depth={0}
                onAddChild={openCreate}
                onEdit={openEdit}
                onDelete={openDelete}
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

      <Modal
        open={editing !== undefined}
        onOpenChange={(open) => {
          if (!open) setEditing(undefined);
        }}
        title={t('locations.editTitle', {
          name: editing?.name ?? '',
          defaultValue: '{{name}} bearbeiten',
        })}
      >
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (!editing) return;
            // Only the changed fields are sent: a rename is {name}, a move
            // {parent_id}, both together one PATCH /locations/{id}.
            const patch: LocationUpdateRequest = {};
            if (editDraft.name !== editing.name) patch.name = editDraft.name;
            if (canMove && editDraft.parent_id !== (editing.parent_id ?? '')) {
              patch.parent_id = editDraft.parent_id;
            }
            if (Object.keys(patch).length === 0) {
              setEditing(undefined);
              return;
            }
            update.mutate(
              { id: editing.id, data: patch },
              { onSuccess: () => setEditing(undefined) },
            );
          }}
        >
          <Input
            label={t('locations.name', 'Name')}
            value={editDraft.name}
            required
            error={editViolations.name}
            onChange={(e) => setEditDraft({ ...editDraft, name: e.target.value })}
          />
          {canMove ? (
            <Select
              label={t('locations.parent', 'Übergeordneter Knoten')}
              options={editParents}
              value={editDraft.parent_id}
              error={editViolations.parent_id}
              onChange={(e) => setEditDraft({ ...editDraft, parent_id: e.target.value })}
            />
          ) : null}
          {editGeneralError ? (
            <p role="alert" className="text-sm text-red-600 dark:text-red-400">
              {editGeneralError}
            </p>
          ) : null}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="secondary" onClick={() => setEditing(undefined)}>
              {t('common.cancel', 'Abbrechen')}
            </Button>
            <Button type="submit" disabled={update.isPending}>
              {t('common.save', 'Speichern')}
            </Button>
          </div>
        </form>
      </Modal>

      <Modal
        open={deleting !== undefined}
        onOpenChange={(open) => {
          if (!open) setDeleting(undefined);
        }}
        title={t('locations.deleteTitle', {
          name: deleting?.name ?? '',
          defaultValue: '„{{name}}“ löschen?',
        })}
        description={t(
          'locations.deleteConfirm',
          'Der Knoten wird endgültig gelöscht. Solange Unterpunkte oder andere Objekte auf ihn verweisen, lehnt der Server das Löschen ab.',
        )}
      >
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={() => setDeleting(undefined)}>
            {t('common.cancel', 'Abbrechen')}
          </Button>
          <Button
            type="button"
            variant="danger"
            disabled={remove.isPending}
            onClick={() => {
              if (deleting) remove.mutate(deleting.id);
              setDeleting(undefined);
            }}
          >
            {t('common.delete', 'Löschen')}
          </Button>
        </div>
      </Modal>
    </div>
  );
}

function LocationTreeItem({
  node,
  depth,
  onAddChild,
  onEdit,
  onDelete,
  kindLabel,
  t,
}: {
  node: TreeNode;
  depth: number;
  onAddChild: (parent: TreeNode) => void;
  onEdit: (node: TreeNode) => void;
  onDelete: (node: TreeNode) => void;
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
          <button className="text-xs text-primary hover:underline" onClick={() => onEdit(node)}>
            {t('common.edit', 'Bearbeiten')}
          </button>
          <button className="text-xs text-red-600 hover:underline" onClick={() => onDelete(node)}>
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
              onEdit={onEdit}
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
