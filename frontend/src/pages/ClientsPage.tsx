import { useId, useMemo, useState } from 'react';
import type { FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Building, EyeOff, Pencil, Plus, Search, Trash2 } from 'lucide-react';
import { ApiError } from '../api/client';
import { clientApi, localizedText } from '../api/cmdb';
import type { Client, ClientUpdateRequest } from '../api/cmdb';
import {
  useAllCITypes,
  useClientPage,
  useCreateClient,
  useDeleteClient,
  useUpdateClient,
} from '../api/cmdbHooks';
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
  const [hiding, setHiding] = useState<Client | null>(null);

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
                  <th scope="col" className={thClasses}>
                    <span className="sr-only">{t('common.actions')}</span>
                  </th>
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
                    <td className={tdClasses}>
                      <span className="flex justify-end gap-1">
                        <IconButton
                          size="sm"
                          icon={<EyeOff />}
                          label={t('clientHiddenTypes.open', { name: client.name })}
                          onClick={() => setHiding(client)}
                        />
                        {mayManage ? (
                          <>
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
                          </>
                        ) : null}
                      </span>
                    </td>
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
      {hiding ? (
        <HiddenCITypesDialog
          key={hiding.id}
          client={hiding}
          mayManage={mayManage}
          onClose={() => setHiding(null)}
        />
      ) : null}
    </div>
  );
}

/** The message of a refused change of the hidden CI types. */
function hiddenRefusal(error: unknown, t: TFunction): string {
  if (error instanceof ApiError) {
    if (error.status === 403) return t('clientHiddenTypes.errors.forbidden');
    if (error.status === 404) return t('clientHiddenTypes.errors.notFound');
    if (error.status === 422) return t('clientHiddenTypes.errors.unknownType');
    return t('clientHiddenTypes.errors.reason', { detail: error.detail ?? error.message });
  }
  return t('clientHiddenTypes.errors.reason', {
    detail: error instanceof Error ? error.message : String(error),
  });
}

/**
 * HiddenCITypesDialog shows and changes the CI types a client hides (MET-05,
 * WP-858): a multiple choice over the active types of the catalog
 * (include_inactive lists the whole catalog, hidden types included) plus
 * deactivated types the client still hides. A hidden type is not offered
 * when a CI of the client is created; it stays active, existing CIs keep
 * it. Without user:manage the choice is read-only. Saving replaces the list
 * (PUT /clients/{id}/hidden-ci-types) and reads the type lists again.
 */
function HiddenCITypesDialog({
  client,
  mayManage,
  onClose,
}: {
  client: Client;
  mayManage: boolean;
  onClose: () => void;
}) {
  const { t, i18n } = useTranslation();
  const qc = useQueryClient();
  const listId = useId();
  const queryKey = ['clients', client.id, 'hidden-ci-types'];
  const hidden = useQuery({ queryKey, queryFn: () => clientApi.hiddenCITypes(client.id) });
  const types = useAllCITypes({ include_inactive: true });
  const [chosen, setChosen] = useState<Set<string> | null>(null);
  const [filter, setFilter] = useState('');
  const save = useMutation({
    mutationFn: (ids: string[]) => clientApi.setHiddenCITypes(client.id, ids),
    onSuccess: (saved) => {
      qc.setQueryData(queryKey, saved);
      void qc.invalidateQueries({ queryKey: ['ci-types'] });
      onClose();
    },
  });

  const selected = chosen ?? new Set(hidden.data?.ci_type_ids ?? []);
  const stored = useMemo(() => new Set(hidden.data?.ci_type_ids ?? []), [hidden.data]);
  const options = useMemo(() => {
    const needle = filter.trim().toLocaleLowerCase(i18n.language);
    return (types.data ?? [])
      .filter((ty) => ty.is_active || stored.has(ty.id))
      .map((ty) => ({
        id: ty.id,
        label: localizedText(ty.display_name, i18n.language) || ty.key || ty.name,
        key: ty.key ?? '',
        system: ty.is_system,
        active: ty.is_active,
      }))
      .filter(
        (option) =>
          !needle ||
          option.label.toLocaleLowerCase(i18n.language).includes(needle) ||
          option.key.toLocaleLowerCase(i18n.language).includes(needle),
      )
      .sort((a, b) => a.label.localeCompare(b.label, i18n.language));
  }, [types.data, stored, filter, i18n.language]);

  const toggle = (id: string, on: boolean) => {
    const next = new Set(selected);
    if (on) next.add(id);
    else next.delete(id);
    setChosen(next);
  };

  const loading = hidden.isLoading || types.isLoading;
  const failed = hidden.error ?? types.error;

  return (
    <Modal
      open
      size="lg"
      onOpenChange={(open) => (open ? undefined : onClose())}
      title={t('clientHiddenTypes.dialogTitle', { name: client.name })}
      description={t('clientHiddenTypes.intro')}
    >
      <form
        className="space-y-4"
        onSubmit={(event) => {
          event.preventDefault();
          if (mayManage) save.mutate([...selected].sort());
        }}
        noValidate
      >
        {!mayManage ? (
          <p className="text-sm text-fg-muted">{t('clientHiddenTypes.readOnly')}</p>
        ) : null}
        {loading ? (
          <SkeletonList rows={4} label={t('app.loading')} />
        ) : failed ? (
          <ErrorState
            title={t('clientHiddenTypes.loadError')}
            retryLabel={t('common.retry')}
            onRetry={() => {
              void hidden.refetch();
              void types.refetch();
            }}
          />
        ) : (
          <fieldset className="space-y-3">
            <legend className="flex w-full items-center justify-between text-sm font-medium text-fg">
              <span>{t('clientHiddenTypes.title')}</span>
              <span className="font-normal text-fg-muted">
                {t('clientHiddenTypes.count', { count: selected.size })}
              </span>
            </legend>
            <Input
              type="search"
              icon={<Search />}
              aria-label={t('clientHiddenTypes.filter')}
              placeholder={t('clientHiddenTypes.filter')}
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
            />
            {options.length === 0 ? (
              <p className="text-sm text-fg-muted">{t('clientHiddenTypes.empty')}</p>
            ) : (
              <ul
                id={listId}
                aria-label={t('clientHiddenTypes.listLabel')}
                className="max-h-80 divide-y divide-line overflow-y-auto rounded-lg border border-line"
              >
                {options.map((option) => (
                  <li key={option.id}>
                    <label className="flex cursor-pointer items-center gap-3 px-3 py-2 text-sm hover:bg-canvas">
                      <input
                        type="checkbox"
                        className="h-4 w-4"
                        checked={selected.has(option.id)}
                        disabled={!mayManage}
                        onChange={(e) => toggle(option.id, e.target.checked)}
                      />
                      <span className="min-w-0 flex-1 [overflow-wrap:anywhere]">
                        {option.label}
                      </span>
                      {option.system ? (
                        <span className="text-xs text-fg-muted">
                          {t('clientHiddenTypes.system')}
                        </span>
                      ) : null}
                      {!option.active ? (
                        <span className="text-xs text-fg-muted">
                          {t('clientHiddenTypes.inactive')}
                        </span>
                      ) : null}
                    </label>
                  </li>
                ))}
              </ul>
            )}
          </fieldset>
        )}
        {save.error ? (
          <p role="alert" className="text-sm text-danger">
            {hiddenRefusal(save.error, t)}
          </p>
        ) : null}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            {mayManage ? t('common.cancel') : t('clientHiddenTypes.close')}
          </Button>
          {mayManage ? (
            <Button type="submit" loading={save.isPending} disabled={loading || !!failed}>
              {t('common.save')}
            </Button>
          ) : null}
        </div>
      </form>
    </Modal>
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
