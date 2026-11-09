import { useId, useState } from 'react';
import type { FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { Building, Pencil, Plus, Trash2 } from 'lucide-react';
import { ApiError } from '../api/client';
import type { Client, ClientUpdateRequest } from '../api/cmdb';
import { useClientPage, useCreateClient, useDeleteClient, useUpdateClient } from '../api/cmdbHooks';
import { useHasPermission } from '../api/hooks';
import { Button, IconButton } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { PageHeader } from '../components/ui/PageHeader';
import { SkeletonList } from '../components/ui/Skeleton';
import {
  tableClasses,
  tdClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';
import { ScrollRegion } from '../components/ScrollRegion';
import { useScopeStore } from '../stores/scope';

/** PAGE_SIZE is the page of clientApi.list (limit=200, F-340). */
const PAGE_SIZE = 200;

const UMLAUTS: Record<string, string> = { ä: 'ae', ö: 'oe', ü: 'ue', ß: 'ss' };

/**
 * suggestSlug derives the short name (slug) of a client from its name:
 * lower case, umlauts transcribed, other accents dropped, every other run of
 * characters a single "-", no "-" at either end, at most 63 characters.
 */
export function suggestSlug(name: string): string {
  return name
    .toLowerCase()
    .replace(/[äöüß]/g, (c) => UMLAUTS[c] ?? c)
    .normalize('NFKD')
    .replace(/[̀-ͯ]/g, '')
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 63)
    .replace(/-+$/g, '');
}

/** The message of a refused client write in the UI language. */
function refusal(error: unknown, t: TFunction): string {
  if (error instanceof ApiError) {
    if (error.status === 403) return t('clients.errors.forbidden');
    if (error.status === 404) return t('clients.errors.notFound');
    if (error.status === 409) return t('clients.errors.conflict');
    return t('clients.errors.reason', { detail: error.detail ?? error.message });
  }
  return t('clients.errors.reason', {
    detail: error instanceof Error ? error.message : String(error),
  });
}

/**
 * fieldMessage words the error of one field: a taken value (409) and the
 * other field problems of the server (400, 422).
 */
function fieldMessage(error: unknown, field: 'name' | 'slug', t: TFunction): string | undefined {
  if (!(error instanceof ApiError)) return undefined;
  const info = error.fieldErrors()[field];
  if (!info) return undefined;
  if (info.kind === 'conflict') {
    return field === 'slug' ? t('clients.errors.slugTaken') : t('clients.errors.nameTaken');
  }
  return info.message;
}

/**
 * ClientsPage is the client administration of the organization (UI-14,
 * WP-855): the clients (customers) with name and short name (slug), page by
 * page. Holders of user:manage create, change and delete clients; everybody
 * else only reads (the list itself needs location:read). A taken short name
 * (409) is shown at its field; a refused delete names the kinds of objects
 * that still refer to the client when the server sends them.
 */
export function ClientsPage() {
  const { t } = useTranslation();
  const mayManage = useHasPermission('user:manage') === true;
  const [offset, setOffset] = useState(0);
  const clients = useClientPage(offset);
  const [dialog, setDialog] = useState<{ client?: Client } | null>(null);
  const [deleting, setDeleting] = useState<Client | null>(null);

  const rows = clients.data?.data ?? [];
  const hasMore = clients.data?.has_more === true;
  const forbidden = clients.error instanceof ApiError && clients.error.status === 403;

  return (
    <div className="space-y-4">
      <PageHeader
        title={t('clients.title')}
        description={t('clients.subtitle')}
        actions={
          mayManage ? (
            <Button icon={<Plus />} onClick={() => setDialog({})}>
              {t('clients.create')}
            </Button>
          ) : null
        }
      />
      {!mayManage ? <p className="text-sm text-fg-muted">{t('clients.readOnly')}</p> : null}

      <Card className="overflow-hidden p-0">
        {clients.isLoading ? (
          <div className="p-5">
            <SkeletonList rows={4} label={t('app.loading')} />
          </div>
        ) : forbidden ? (
          <div className="p-5">
            <ErrorState title={t('clients.errors.forbiddenRead')} />
          </div>
        ) : clients.error ? (
          <div className="p-5">
            <ErrorState
              title={t('clients.loadError')}
              retryLabel={t('common.retry')}
              onRetry={() => void clients.refetch()}
            />
          </div>
        ) : rows.length === 0 ? (
          <div className="p-5">
            <EmptyState icon={<Building />} title={t('clients.empty')} />
          </div>
        ) : (
          <ScrollRegion label={t('clients.title')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('clients.fields.name')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('clients.fields.slug')}
                  </th>
                  {mayManage ? (
                    <th scope="col" className={thClasses}>
                      <span className="sr-only">{t('common.actions')}</span>
                    </th>
                  ) : null}
                </tr>
              </thead>
              <tbody>
                {rows.map((client) => (
                  <tr key={client.id} className={trClasses}>
                    <td className={tdPrimaryClasses}>
                      <span className="[overflow-wrap:anywhere]">{client.name}</span>
                    </td>
                    <td className={tdClasses}>
                      <code className="font-mono text-xs">{client.slug}</code>
                    </td>
                    {mayManage ? (
                      <td className={tdClasses}>
                        <span className="flex justify-end gap-1">
                          <IconButton
                            size="sm"
                            icon={<Pencil />}
                            label={t('clients.editLabel', { name: client.name })}
                            onClick={() => setDialog({ client })}
                          />
                          <IconButton
                            size="sm"
                            icon={<Trash2 />}
                            label={t('clients.deleteLabel', { name: client.name })}
                            onClick={() => setDeleting(client)}
                            className="hover:text-danger"
                          />
                        </span>
                      </td>
                    ) : null}
                  </tr>
                ))}
              </tbody>
            </table>
          </ScrollRegion>
        )}
        {offset > 0 || hasMore ? (
          <div className="flex justify-end gap-2 border-t border-line px-5 py-3">
            <Button
              variant="secondary"
              size="sm"
              disabled={offset === 0}
              onClick={() => setOffset((o) => Math.max(0, o - PAGE_SIZE))}
            >
              {t('common.back')}
            </Button>
            <Button
              variant="secondary"
              size="sm"
              disabled={!hasMore}
              onClick={() => setOffset((o) => o + PAGE_SIZE)}
            >
              {t('common.next')}
            </Button>
          </div>
        ) : null}
      </Card>

      {dialog ? (
        <ClientFormDialog
          key={dialog.client?.id ?? 'create'}
          client={dialog.client}
          onClose={() => setDialog(null)}
        />
      ) : null}
      <DeleteClientDialog client={deleting} onClose={() => setDeleting(null)} />
    </div>
  );
}

/**
 * ClientFormDialog creates a client or changes name and short name of one
 * (WP-855). While the short name of a new client is not edited by hand it
 * follows the name (suggestSlug). onSaved receives the saved client; the
 * location tree uses it to select a client it just created.
 */
export function ClientFormDialog({
  client,
  onClose,
  onSaved,
}: {
  client?: Client;
  onClose: () => void;
  onSaved?: (client: Client) => void;
}) {
  const { t } = useTranslation();
  const slugHintId = useId();
  const slugId = useId();
  const [name, setName] = useState(client?.name ?? '');
  const [slug, setSlug] = useState(client?.slug ?? '');
  // A new client's short name follows the name until it is edited by hand.
  const [slugTouched, setSlugTouched] = useState(client !== undefined);
  const [notice, setNotice] = useState<string>();
  const create = useCreateClient();
  const update = useUpdateClient();
  const mutation = client ? update : create;
  const saving = create.isPending || update.isPending;

  const nameError =
    fieldMessage(mutation.error, 'name', t) ??
    (notice === 'name' ? t('clients.errors.nameRequired') : undefined);
  const slugError =
    fieldMessage(mutation.error, 'slug', t) ??
    (notice === 'slug' ? t('clients.errors.slugRequired') : undefined);
  const general =
    mutation.error && !nameError && !slugError ? refusal(mutation.error, t) : undefined;

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const trimmedName = name.trim();
    const trimmedSlug = slug.trim();
    if (!trimmedName) {
      setNotice('name');
      return;
    }
    if (!trimmedSlug) {
      setNotice('slug');
      return;
    }
    setNotice(undefined);
    const done = {
      onSuccess: (saved: Client) => {
        onSaved?.(saved);
        onClose();
      },
    };
    if (client) {
      // Only the changed fields are sent.
      const patch: ClientUpdateRequest = {};
      if (trimmedName !== client.name) patch.name = trimmedName;
      if (trimmedSlug !== client.slug) patch.slug = trimmedSlug;
      if (Object.keys(patch).length === 0) {
        onClose();
        return;
      }
      update.mutate({ id: client.id, data: patch }, done);
      return;
    }
    create.mutate({ name: trimmedName, slug: trimmedSlug }, done);
  };

  return (
    <Modal
      open
      onOpenChange={(open) => (open ? undefined : onClose())}
      title={client ? t('clients.editTitle', { name: client.name }) : t('clients.createTitle')}
    >
      <form className="space-y-4" onSubmit={submit} noValidate>
        <Input
          label={t('clients.fields.name')}
          value={name}
          required
          error={nameError}
          onChange={(e) => {
            setName(e.target.value);
            if (!slugTouched) setSlug(suggestSlug(e.target.value));
          }}
        />
        <div className="space-y-1">
          <Input
            id={slugId}
            label={t('clients.fields.slug')}
            value={slug}
            required
            error={slugError}
            aria-describedby={slugError ? `${slugId}-error ${slugHintId}` : slugHintId}
            className="font-mono"
            onChange={(e) => {
              setSlugTouched(true);
              setSlug(e.target.value);
            }}
          />
          <p id={slugHintId} className="text-sm text-fg-muted">
            {t('clients.slugHint')}
          </p>
        </div>
        {general ? (
          <p role="alert" className="text-sm text-danger">
            {general}
          </p>
        ) : null}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" loading={saving}>
            {client ? t('common.save') : t('common.create')}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

/**
 * DeleteClientDialog confirms the deletion of a client. The server deletes
 * the client's sites with it; a refusal with dependencies (409) names the
 * kinds of objects that still refer to the client. A deleted client that
 * is the selected scope leaves the scope selection.
 */
function DeleteClientDialog({ client, onClose }: { client: Client | null; onClose: () => void }) {
  const { t } = useTranslation();
  const remove = useDeleteClient();
  const scopeClientId = useScopeStore((s) => s.clientId);
  const resetScope = useScopeStore((s) => s.reset);
  // The refusal of one client is not shown for the next.
  const close = () => {
    remove.reset();
    onClose();
  };

  let error: string | undefined;
  if (remove.error) {
    const kinds = remove.error instanceof ApiError ? remove.error.dependencies : [];
    error =
      remove.error instanceof ApiError && remove.error.status === 409 && kinds.length > 0
        ? t('clients.errors.inUse', {
            kinds: kinds.map((kind) => t(`locations.dependencies.${kind}`, kind)).join(', '),
          })
        : remove.error instanceof ApiError && remove.error.status === 404
          ? t('clients.errors.deleteNotFound')
          : refusal(remove.error, t);
  }

  return (
    <Modal
      open={!!client}
      onOpenChange={(open) => (open ? undefined : close())}
      title={t('clients.deleteTitle', { name: client?.name ?? '' })}
      description={t('clients.deleteConfirm')}
    >
      {error ? (
        <p role="alert" className="mb-3 text-sm text-danger">
          {error}
        </p>
      ) : null}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="secondary" onClick={close}>
          {t('common.cancel')}
        </Button>
        <Button
          type="button"
          variant="danger"
          loading={remove.isPending}
          onClick={() =>
            client &&
            remove.mutate(client.id, {
              onSuccess: () => {
                if (scopeClientId === client.id) resetScope();
                close();
              },
            })
          }
        >
          {t('common.delete')}
        </Button>
      </div>
    </Modal>
  );
}
