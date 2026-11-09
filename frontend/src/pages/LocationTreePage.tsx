import { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useSearchParams } from 'react-router-dom';
import type { TFunction } from 'i18next';
import {
  Building2,
  ChevronDown,
  ChevronRight,
  DoorOpen,
  LayoutGrid,
  MapPin,
  Package,
  Plus,
  Rows3,
  Server,
  Warehouse,
  type LucideIcon,
} from 'lucide-react';
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
  type Client,
  type LocationTreeNode as TreeNode,
  type LocationUpdateRequest,
} from '../api/cmdb';
import { ApiError } from '../api/client';
import { useHasPermission } from '../api/hooks';
import { ClientFormDialog } from './ClientsPage';
import { Badge } from '../components/ui/Badge';
import { Button, IconButton } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { PageHeader } from '../components/ui/PageHeader';
import { Input } from '../components/ui/Input';
import { Select } from '../components/ui/Select';
import { Modal } from '../components/ui/Modal';
import { SkeletonList } from '../components/ui/Skeleton';
import { ErrorState } from '../components/ui/ErrorState';
import { cn } from '../components/ui/utils';

const KIND_ICONS: Record<LocationKind, LucideIcon> = {
  site: MapPin,
  building: Building2,
  room: DoorOpen,
  rack: Server,
  warehouse: Warehouse,
  zone: LayoutGrid,
  shelf: Rows3,
  bin: Package,
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
 * ancestorsOf returns the ids of the nodes above the node `id`, from its
 * root down, or undefined when the tree has no such node.
 */
function ancestorsOf(roots: TreeNode[], id: string): string[] | undefined {
  const walk = (list: TreeNode[], trail: string[]): string[] | undefined => {
    for (const candidate of list) {
      if (candidate.id === id) return trail;
      const found = walk(candidate.children, [...trail, candidate.id]);
      if (found) return found;
    }
    return undefined;
  };
  return walk(roots, []);
}

/**
 * LocationTreePage shows and edits the canonical location tree (LOC-10):
 * sites are the roots, and each node offers only the child kinds the parent
 * matrix allows. Every node can be renamed and, except a site, moved below
 * another allowed parent (LOC-07); deleting asks first, and a refusal names
 * the kinds of the objects that still reference the node (LOC-11). Field
 * violations of the API are shown on the inputs, a missing permission (403)
 * as a visible message. `?node=<id>` (the link of a search hit, SRC-01,
 * F-334) opens the path to that node, marks it and moves focus to it.
 */
export function LocationTreePage() {
  const { t } = useTranslation();
  const { data, isLoading, error, refetch } = useLocationTree();
  const create = useCreateLocation();
  const update = useUpdateLocation();
  const remove = useDeleteLocation();
  const roots = useMemo(() => data?.data ?? [], [data]);

  const [searchParams] = useSearchParams();
  const targetId = searchParams.get('node') ?? '';
  const targetPath = useMemo(
    () => (targetId ? ancestorsOf(roots, targetId) : undefined),
    [roots, targetId],
  );
  const expanded = useMemo(() => new Set(targetPath ?? []), [targetPath]);
  const selectedId = targetPath ? targetId : undefined;

  const [showCreate, setShowCreate] = useState(false);
  const [parent, setParent] = useState<TreeNode | undefined>();
  const [draft, setDraft] = useState<{ name: string; kind: LocationKind; client_id: string }>({
    name: '',
    kind: 'site',
    client_id: '',
  });
  const clients = useClients(showCreate && !parent);
  // The site dialog offers a new client to holders of user:manage (WP-855);
  // the client created there is selected and listed even before the list is
  // read again.
  const mayManageClients = useHasPermission('user:manage') === true;
  const [creatingClient, setCreatingClient] = useState(false);
  const [createdClient, setCreatedClient] = useState<Client | undefined>();

  const [editing, setEditing] = useState<TreeNode | undefined>();
  const [editDraft, setEditDraft] = useState({ name: '', parent_id: '' });
  const [deleting, setDeleting] = useState<TreeNode | undefined>();

  const openCreate = (parentNode?: TreeNode) => {
    setParent(parentNode);
    create.reset();
    setCreatingClient(false);
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
  const listedClients = clients.data?.data ?? [];
  const clientOptions = [
    ...listedClients,
    ...(createdClient && !listedClients.some((c) => c.id === createdClient.id)
      ? [createdClient]
      : []),
  ].map((c) => ({ value: c.id, label: c.name }));

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
      <PageHeader
        title={t('locations.title', 'Standorte & Lager')}
        description={t(
          'locations.subtitle',
          'Einheitliche Hierarchie: Standort → Gebäude → Raum → Rack und Standort → Lager → Zone → Regal → Fach.',
        )}
        actions={
          <Button icon={<Plus />} onClick={() => openCreate(undefined)}>
            {t('locations.createRoot', 'Standort anlegen')}
          </Button>
        }
      />

      {isLoading ? <SkeletonList rows={5} label={t('app.loading')} /> : null}
      {error ? (
        <ErrorState
          title={t('app.error')}
          retryLabel={t('common.retry')}
          onRetry={() => void refetch()}
        />
      ) : null}
      {deleteError ? (
        <ErrorState
          title={`${t('locations.deleteFailed', 'Löschen nicht möglich')}: ${deleteError}`}
        />
      ) : null}
      {targetId && data && !targetPath ? (
        <p role="status" className="text-sm text-fg-muted">
          {t(
            'locations.nodeNotFound',
            'Der verlinkte Knoten ist nicht vorhanden oder für Sie nicht sichtbar.',
          )}
        </p>
      ) : null}

      <Card className="p-2">
        {roots.length === 0 && !isLoading ? (
          <EmptyState
            icon={<MapPin />}
            className="border-0"
            title={t(
              'locations.empty',
              'Noch keine Standorte. Legen Sie zuerst einen Standort an.',
            )}
          />
        ) : (
          <ul className="space-y-0.5">
            {roots.map((node) => (
              <LocationTreeItem
                key={node.id}
                node={node}
                depth={0}
                expanded={expanded}
                selectedId={selectedId}
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
          className="space-y-4"
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
                label: kindLabel(kind),
              }))}
              value={draft.kind}
              error={violations.kind ?? violations.parent_id}
              onChange={(e) => setDraft({ ...draft, kind: e.target.value as LocationKind })}
            />
          ) : (
            <div className="flex items-end gap-2">
              <div className="min-w-0 flex-1">
                <Select
                  label={t('locations.client', 'Mandant')}
                  options={[{ value: '', label: '—' }, ...clientOptions]}
                  value={draft.client_id}
                  required
                  error={violations.client_id}
                  onChange={(e) => setDraft({ ...draft, client_id: e.target.value })}
                />
              </div>
              {mayManageClients ? (
                <Button
                  type="button"
                  variant="secondary"
                  icon={<Plus />}
                  onClick={() => setCreatingClient(true)}
                >
                  {t('clients.createFromSite')}
                </Button>
              ) : null}
            </div>
          )}
          {generalError ? (
            <p role="alert" className="text-sm text-danger">
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
        {/* Outside the form: a submit of the client dialog must not bubble
            (through the React tree) into the site form. */}
        {creatingClient ? (
          <ClientFormDialog
            onClose={() => setCreatingClient(false)}
            onSaved={(client) => {
              setCreatedClient(client);
              setDraft((d) => ({ ...d, client_id: client.id }));
            }}
          />
        ) : null}
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
          className="space-y-4"
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
            <p role="alert" className="text-sm text-danger">
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
  expanded,
  selectedId,
  onAddChild,
  onEdit,
  onDelete,
  kindLabel,
  t,
}: {
  node: TreeNode;
  depth: number;
  /** The nodes on the path to the linked node: they are opened. */
  expanded: ReadonlySet<string>;
  /** The linked node (`?node=`): marked and focused. */
  selectedId: string | undefined;
  onAddChild: (parent: TreeNode) => void;
  onEdit: (node: TreeNode) => void;
  onDelete: (node: TreeNode) => void;
  kindLabel: (kind: LocationKind) => string;
  t: TFunction;
}) {
  const onPath = expanded.has(node.id);
  const [open, setOpen] = useState(depth < 2 || onPath);
  useEffect(() => {
    if (onPath) setOpen(true);
  }, [onPath]);
  const selected = node.id === selectedId;
  const rowRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!selected) return;
    rowRef.current?.focus({ preventScroll: true });
    rowRef.current?.scrollIntoView?.({ block: 'center' });
  }, [selected]);
  const children = node.children;
  const canHaveChildren = LOCATION_CHILD_KINDS[node.kind].length > 0;
  const KindIcon = KIND_ICONS[node.kind];
  return (
    <li>
      <div
        ref={rowRef}
        tabIndex={selected ? -1 : undefined}
        aria-current={selected ? 'true' : undefined}
        className={cn(
          'flex flex-wrap items-center gap-x-2 gap-y-0.5 rounded-lg px-2 py-1 transition-colors hover:bg-fg/[0.04]',
          selected &&
            'bg-accent-soft outline-none ring-2 ring-inset ring-accent hover:bg-accent-soft',
        )}
        style={{ paddingLeft: `${depth * 1.25 + 0.5}rem` }}
      >
        {children.length > 0 ? (
          <IconButton
            size="sm"
            variant="ghost"
            label={open ? 'collapse' : 'expand'}
            aria-expanded={open}
            icon={open ? <ChevronDown /> : <ChevronRight />}
            onClick={() => setOpen(!open)}
            className="-ml-1 h-7 w-7"
          />
        ) : (
          <span className="w-6" />
        )}
        <KindIcon aria-hidden="true" className="h-4 w-4 shrink-0 text-fg-subtle" />
        <span className="min-w-0 font-medium text-fg [overflow-wrap:anywhere]">{node.name}</span>
        <Badge variant="neutral">{kindLabel(node.kind)}</Badge>
        <span className="ml-auto flex flex-wrap items-center gap-x-0.5">
          {canHaveChildren ? (
            <Button
              size="sm"
              variant="ghost"
              icon={<Plus />}
              className="px-2 text-accent hover:text-accent"
              onClick={() => onAddChild(node)}
            >
              {t('locations.addChild', 'Unterpunkt')}
            </Button>
          ) : null}
          <Button
            size="sm"
            variant="ghost"
            className="px-2 text-accent hover:text-accent"
            onClick={() => onEdit(node)}
          >
            {t('common.edit', 'Bearbeiten')}
          </Button>
          <Button
            size="sm"
            variant="ghost"
            className="px-2 text-danger hover:text-danger"
            onClick={() => onDelete(node)}
          >
            {t('common.delete', 'Löschen')}
          </Button>
        </span>
      </div>
      {open && children.length > 0 ? (
        <ul>
          {children.map((child) => (
            <LocationTreeItem
              key={child.id}
              node={child}
              depth={depth + 1}
              expanded={expanded}
              selectedId={selectedId}
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
